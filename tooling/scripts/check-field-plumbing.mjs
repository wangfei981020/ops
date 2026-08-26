#!/usr/bin/env node
/**
 * 守卫：成对的结构体之间，字段不能少。
 *
 * # 为什么需要它
 *
 * 新增一个可配置字段要动三层：**存储结构 → API 收发结构 → 前端类型**。
 * 漏掉中间那层的表现是**静默丢弃**：
 *
 *   漏 Req（收） → 前端填了、JSON 解析时丢掉 → 「填了没反应」
 *   漏 DTO（发） → 存进去了、编辑时读不出来 → 「保存后再打开是空的」
 *
 * 都不报错、日志里也看不见 —— 只能靠人点界面发现，
 * 而人只会点自己刚做的那条路径。ops-version 已经栽过三次（见 PAIRS 里的说明）。
 *
 * # 为什么是显式配对表而不是自动推断
 *
 * 试过自动推断（拿 store 结构体的 json tag 去 API 包里找），十几条全是误报：
 * 大量 store 结构体是**直接返回**给前端的（`ok(w, list)`），
 * 它的 json tag 本身就是 API 契约，没有第二层要对齐；
 * 而 Req 只该收可写字段，把状态、时间戳算进去也是误报。
 * 「哪个字段该在哪一层」是语义问题，静态分析判不准 ——
 * 而**误报的代价是人把整个守卫关掉**，比漏报更糟。
 *
 * 所以只查明确成对的那几组。新增配对时加一行，成本很低。
 */
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '../..')

const PAIRS = [
  {
    product: 'ops-version',
    store: 'OrgEnv',
    // 收：前端提交的环境配置。漏字段 = 填了没反应
    req: 'envReq',
    // 发：编辑时读回来。漏字段 = 保存后再打开是空的
    dto: 'envDTO',
    // 只存在于存储层的内部字段
    //
    // ⚠️ ds_* 是从 datasources 表 **JOIN 出来的派生值**，不是用户填的：
    //    用户在界面上选的是 datasource_id，地址/认证方式/凭据由那条数据源提供。
    //    它们既不该进 Req（前端填了也没意义，会被 JOIN 的值覆盖），
    //    更不该进 DTO —— 🔴 ds_credential_enc 是**加密后的凭据**，
    //    出参等于把别的平台也在用的那份凭据发给前端。
    //    凭据的规矩是「写得进、永不回显」，这条对数据源同样成立。
    skip: [
      'credential_enc',
      'ds_endpoint', 'ds_auth_type', 'ds_credential_enc', 'ds_name', 'ds_provider_type',
    ],
    // Req 收但 DTO 不发的（凭据类：写得进、永不回显）
    reqOnly: ['username', 'password', 'api_key', 'insecure_tls'],
    // DTO 发但 Req 不收的（派生状态 / 采集器写的事实）
    //
    // ⚠️ last_collect_* 是**只出不进**的：它们由采集器写，不是用户填的配置。
    //    放进 Req 等于允许前端伪造"采集成功" —— 而那会让一列过期数据
    //    被当成刚采的，比对结果看着正常却是错的。
    dtoOnly: [
      'has_credential', 'last_collect_at', 'last_collect_status', 'last_collect_error',
      // 降级采集标记：同样是采集器写的事实。
      // 允许前端设置它 = 允许伪造"这一列不是降级采的"，而降级意味着
      // 副本为 0 的服务看不见，那一列的 missing 全都不该当成确定结论。
      'last_collect_degraded', 'last_collect_degraded_note',
    ],
    note: '2026-08-19 栽过：加 workload_include/exclude 时改了迁移/store/providers/collector/前端表单，唯独 envReq 没加，前端填了被静默丢弃',
  },
]

function structFields(src, name) {
  const m = src.match(new RegExp(`type\\s+${name}\\s+struct\\s*\\{([\\s\\S]*?)\\n\\}`))
  if (!m) return null
  const out = new Set()
  for (const f of m[1].matchAll(/`json:"([^",]+)/g)) if (f[1] !== '-') out.add(f[1])
  return out
}

function walk(dir, out = []) {
  for (const n of readdirSync(dir)) {
    const p = join(dir, n)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (n.endsWith('.go') && !n.endsWith('_test.go')) out.push(p)
  }
  return out
}

/** store 层结构体没有 json tag，按 Go 字段名转 snake_case 比对 */
function goFieldsSnake(src, name) {
  const m = src.match(new RegExp(`type\\s+${name}\\s+struct\\s*\\{([\\s\\S]*?)\\n\\}`))
  if (!m) return null
  const out = new Set()
  for (const line of m[1].split('\n')) {
    const f = line.match(/^\s*([A-Z]\w*)\s+[\[\]\*\w.]+/)
    if (!f) continue
    out.add(
      f[1]
        .replace(/([a-z0-9])([A-Z])/g, '$1_$2')
        .replace(/([A-Z]+)([A-Z][a-z])/g, '$1_$2')
        .toLowerCase(),
    )
  }
  return out
}

let failed = 0
for (const p of PAIRS) {
  const be = join(ROOT, p.product, 'backend')
  if (!existsSync(be)) continue
  const files = walk(be)
  const read = (n) => {
    for (const f of files) {
      const s = readFileSync(f, 'utf8')
      const g = n === p.store ? goFieldsSnake(s, n) : structFields(s, n)
      if (g?.size) return g
    }
    return null
  }
  const st = read(p.store)
  const req = read(p.req)
  const dto = read(p.dto)
  if (!st || !req || !dto) {
    console.error(`✗ ${p.product}: 找不到 ${p.store} / ${p.req} / ${p.dto} 之一，配对表该更新了`)
    failed++
    continue
  }
  const skip = new Set([...p.skip, ...p.reqOnly, ...p.dtoOnly])
  const want = [...st].filter((k) => !skip.has(k))
  const missReq = want.filter((k) => !req.has(k))
  const missDto = want.filter((k) => !dto.has(k))
  if (missReq.length || missDto.length) {
    failed++
    console.error(`\n✗ ${p.product}: ${p.store} 的字段没有贯通到 API 层`)
    if (missReq.length) {
      console.error(`  ${p.req} 缺（前端填了会被静默丢弃）：${missReq.join(', ')}`)
    }
    if (missDto.length) {
      console.error(`  ${p.dto} 缺（保存后再打开读不回来）：${missDto.join(', ')}`)
    }
    console.error(`  历史：${p.note}`)
  }
}

if (failed) {
  console.error('\n这类缺陷不报错、日志里也看不见 —— 只能靠人点界面发现。')
  process.exit(1)
}
console.log(`✓ ${PAIRS.length} 组收发结构体的字段都对得上`)
