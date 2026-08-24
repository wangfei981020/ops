#!/usr/bin/env node
/**
 * 守卫：前端类型里的 snake_case 字段名，后端必须真的给过。
 *
 * # 为什么需要这个守卫
 *
 * 的验证里，**八条问题是同一个根因**：前端接口类型里的字段名
 * 和后端返回的对不上。表现全都不是报错，而是「答非所问」：
 *
 *   告警页       前端读 summary / target，后端给 rule_note / object
 *                → 「摘要」列 279 行全是「—」，「对象」列压根不存在
 *   防火墙页     前端读 risky / allowed，后端给 high_risk / protocols
 *                → 后端判出 18 条高危，页面上一条都没标红
 *   闲置浪费页   前端读 cpu_request_m，后端给 cpu_req_m
 *                → 504 行「申请」值全渲染成 0，而这一页用来决定砍哪些资源
 *   资源使用率   前端读 series，后端只给 Prometheus 原始信封
 *                → 查询返回 61 个点，页面说「没有数据点，可能是对象名写错了」
 *   定时任务     前端类型没声明 failures / findings，后端一直在返回
 *                → 「展开执行记录」和列表页给的是同一句话
 *
 * 危险性排第一的是闲置浪费那条：「申请 0 / 实测 679m」会被读成"这东西没人用，
 * 砍掉"，而真相是内存用了 24%（有的到 88%），照它砍会直接 OOM。
 *
 * # ⚠️ 为什么"下次注意"不管用
 *
 * 168 个路由里只有 23 个有 swag 注解，其余接口的 TS 类型全靠手写。
 * 手写就会有笔误，而笔误在这里**不会报错**——字段不存在只是 undefined，
 * 渲染成「—」或被 `?? 0` 兜成 0。**一个不报错的错误，靠注意力是防不住的。**
 *
 * 根治办法是给所有路由补 swag 注解、从 OpenAPI 生成类型（已进待办）。
 * 这个守卫是在那之前的止血：它抓不到"类型对了但语义错了"，
 * 但能抓到"这个字段名后端从来没出现过"，也就是上面八条里的七条。
 *
 * # 判据
 *
 * 后端出参的键名来自三处，全都要扫：
 *   1. struct 的 `json:"xxx"` tag
 *   2. `gin.H{"xxx": ...}` 里的字面量键
 *   3. `c.JSON(..., gin.H{...})` 内联的键（同 2，正则一并覆盖）
 *
 * 前端侧只看 `interface` / `type` 里声明的 snake_case 字段 ——
 * camelCase 的多半是前端自己造的视图模型，不参与接口契约。
 *
 * 只报"后端全集里完全不存在"的名字。**不做按路由精确匹配**：
 * 那需要真正的类型信息，而如果我们有类型信息，就该直接生成类型而不是写守卫。
 * 宁可漏报（少数字段名在别处巧合出现过），也不要误报把人训练成忽略它。
 */

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..', '..')

/**
 * 历史欠账。
 *
 * 这里的每一条都是"前端声明了、后端全集里找不到"的字段名，但复核后确认无害
 * （前端自造的中间形状、后端用变量拼的键名，等等）。
 *
 * ⚠️ 允许清单每次运行都会打印条数。**它只能变少，不能变多。**
 * 新增字段名对不上时守卫会直接失败，而不是悄悄加进这张表 ——
 * 那样这个守卫就退化成了装饰。
 */
const ALLOW = new Set([
  // 前端自造的分页/筛选形状，不来自任何接口
  'per_page',
  'sort_by',
])

function walk(dir, out = []) {
  for (const e of readdirSync(dir)) {
    if (e === 'node_modules' || e === 'dist' || e === '.git') continue
    const p = join(dir, e)
    const st = statSync(p)
    if (st.isDirectory()) walk(p, out)
    else out.push(p)
  }
  return out
}

/**
 * 扫描范围。
 *
 * ⚠️ 前端侧按**产品**限范围，后端侧永远全扫。
 *
 *	理由：一个产品的构建不该被另一个产品的欠账挡住。
 *	实测就撞到了——另一个产品 有两条真问题（见下），如果不分范围，
 *	某个同类产品 的构建会一直红着，而红着的守卫最后只会被人加 `|| true` 绕过。
 *
 *	后端仍然全扫，因为键名全集越大越不容易误报，
 *	而误报是这个守卫唯一致命的失败模式。
 *
 * 用法：check-field-names.mjs [前端目录…]，不传 = 全扫。
 */
const scopes = process.argv.slice(2)
const files = walk(ROOT).filter((f) => !f.includes(`${'/'}node_modules${'/'}`))
const inScope = (f) =>
  scopes.length === 0 || scopes.some((s) => relative(ROOT, f).startsWith(s))

// ── 1. 后端给过的所有键名 ──────────────────────────────
const backendKeys = new Set()
for (const f of files.filter((x) => x.endsWith('.go'))) {
  const src = readFileSync(f, 'utf8')
  // json tag：`json:"foo,omitempty"` → foo
  for (const m of src.matchAll(/json:"([a-z0-9_]+)(?:,[^"]*)?"/g)) backendKeys.add(m[1])
  //
  // ⚠️ 键名的写法**不止 `"foo":` 一种**，这里必须放宽到所有字符串字面量。
  //
  //	第一版只抓 `"foo":` 形式，于是漏掉了这些：
  //	  item["risk_reason"] = reason        ← map 赋值，最常见的可选字段写法
  //	  out["empty_hint"] = "..."           ← 同上
  //	  SELECT min_replicas FROM ...        ← 列名直接当 JSON 键
  //	结果 risk_reason / empty_hint / min_replicas 这些**后端明明有**的字段
  //	全被报成"从来没返回过"，一次运行 20 多条误报。
  //
  //	而误报的代价比漏报大得多：一个会误报的守卫，第二次就没人看了
  //	（本项目的 check-write-coverage 曾因为只比路径不比方法报出 9 条假问题）。
  //	所以判据放宽成"这个名字在后端源码里出现过吗"——
  //	抓不到"字段名拼对了但用错了接口"，但能抓到"这个名字后端压根不存在"，
  //	也就是 031 那八条里的七条。
  for (const m of src.matchAll(/"([a-z][a-z0-9_]*)"/g)) backendKeys.add(m[1])
  // 裸列名（SQL 里的 snake_case 标识符）也算：很多接口直接把列名当出参键名
  for (const m of src.matchAll(/\b([a-z][a-z0-9]*(?:_[a-z0-9]+)+)\b/g)) backendKeys.add(m[1])
}

// ── 2. 前端类型里声明的 snake_case 字段 ────────────────
/** 只在 interface / type 块内部找，避免把对象字面量的键当成类型声明 */
function typeBlocks(src) {
  const blocks = []
  const re = /(?:export\s+)?(?:interface|type)\s+\w+[^{]*\{/g
  let m
  while ((m = re.exec(src))) {
    let depth = 1
    let i = re.lastIndex
    while (i < src.length && depth > 0) {
      if (src[i] === '{') depth++
      else if (src[i] === '}') depth--
      i++
    }
    blocks.push({ text: src.slice(re.lastIndex, i), start: re.lastIndex })
  }
  return blocks
}

const offenders = []
for (const f of files.filter((x) => /\.tsx?$/.test(x) && !x.endsWith('.d.ts') && inScope(x))) {
  const src = readFileSync(f, 'utf8')
  if (!/interface|type\s+\w+\s*=/.test(src)) continue
  for (const b of typeBlocks(src)) {
    // 字段声明形如 `foo_bar?: number` / `foo_bar: string`
    for (const m of b.text.matchAll(/^\s*([a-z][a-z0-9]*(?:_[a-z0-9]+)+)\??\s*:/gm)) {
      const name = m[1]
      if (ALLOW.has(name) || backendKeys.has(name)) continue
      const line = src.slice(0, b.start + m.index).split('\n').length
      offenders.push({ file: relative(ROOT, f), line, name })
    }
  }
}

if (offenders.length > 0) {
  console.error('✗ 前端类型里声明的字段名，后端从来没有返回过：\n')
  for (const o of offenders) {
    console.error(`  ${o.name}`)
    console.error(`      ${o.file}:${o.line}`)
  }
  console.error(`
字段名对不上**不会报任何错**：读到的是 undefined，界面上只是显示成「—」，
或者被 \`?? 0\` 兜成 0 —— 而 0 会被当成真实数值去做决定。
里因此出过 8 条问题，最危险的一条把「申请 12000m」显示成「申请 0」，
差点被拿去当作"这个服务没人用，可以砍掉"的依据。

请先 curl 一次真实接口，按响应里的**实际键名**改前端类型；
如果确认是前端自造的、不来自接口的字段，加进本文件的 ALLOW 清单并写明理由。`)
  process.exit(1)
}

console.log(
  `✓ 前端类型里的 snake_case 字段名都能在后端出参里找到（范围 ${
    scopes.length ? scopes.join(', ') : '全部产品'
  }，后端键名全集 ${backendKeys.size} 个，历史豁免 ${ALLOW.size} 条）`,
)
