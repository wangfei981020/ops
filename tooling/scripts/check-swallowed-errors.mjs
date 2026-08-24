#!/usr/bin/env node
/**
 * Go 代码里把**解析类错误**用 `_ =` 丢掉。
 *
 * # 为什么单独拦这一类
 *
 * `_ = f.SetCellValue(...)` 这种写不写都一样（excelize 的 setter 几乎不会失败，
 * 失败了也只是那一格没值），拦它是噪音。
 *
 * 但**解析**不一样：`_ = json.Unmarshal(raw, &p)` 出错时，
 * Go 会把类型不匹配的那个字段留成**零值**，而其余字段照常解析成功 ——
 * 于是调用方看到的是「大部分参数生效了，就那一个没生效」，而且不报错。
 *
 * 实测过：MCP 客户端把 limit 传成字符串 "1"，
 * 服务端 p.Limit 静默变 0 → 走默认上限 200 →
 * **AI 传 limit=1 拿回 71 行**，而工具描述里明明警告过
 * 「全量结果 7 万余字符会撑爆上下文」。
 *
 * ⚠️ "参数被静默忽略"比直接报错危险得多：报错了调用方会改，
 *    静默忽略则是它以为自己已经限制了，然后拿着全量继续往下走。
 *
 * # 判据
 *
 * 只拦**反序列化**：Unmarshal / Decode / ParseInt 这类"把外部输入变成内部值"的调用。
 * 它们的错误一律意味着「调用方给的东西不对」，而那是必须说出来的。
 */
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

const ROOT = new URL('../..', import.meta.url).pathname

/** 这些函数的错误不许丢 —— 丢了就是"外部输入不对却装作没事"。 */
const PARSERS = [
  'json.Unmarshal',
  'json.NewDecoder',
  'yaml.Unmarshal',
  'xml.Unmarshal',
  'strconv.Atoi',
  'strconv.ParseInt',
  'strconv.ParseFloat',
  'strconv.ParseBool',
  'time.Parse',
]

function walk(dir, out = []) {
  let entries
  try {
    entries = readdirSync(dir)
  } catch {
    return out
  }
  for (const e of entries) {
    if (e === 'node_modules' || e === 'vendor' || e === 'dist' || e.startsWith('.')) continue
    const p = join(dir, e)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (e.endsWith('.go') && !e.endsWith('_test.go')) out.push(p)
  }
  return out
}

const only = process.argv[2]
const products = readdirSync(ROOT)
  .filter((d) => d.startsWith('ops-'))
  .filter((d) => !only || d === only)
  .filter((d) => {
    try {
      return statSync(join(ROOT, d, 'backend')).isDirectory()
    } catch {
      return false
    }
  })

const hits = []
for (const prod of products) {
  for (const file of walk(join(ROOT, prod, 'backend'))) {
    const lines = readFileSync(file, 'utf8').split('\n')
    lines.forEach((line, i) => {
      // ⚠️ 跳过注释行 —— 否则连"解释这个坑的注释"都会被当成代码抓，
      //    而一道会误报的守卫很快就会被人加个 --skip 绕过去。
      if (line.trim().startsWith('//')) return
      if (!line.includes('_ =')) return
      const fn = PARSERS.find((f) => line.includes(f + '('))
      if (!fn) return
      // 允许显式豁免，但必须在同一行写明理由
      if (/\/\/.*(允许丢弃|ignore-err|无所谓)/.test(line)) return
      hits.push({ file: relative(ROOT, file), line: i + 1, fn, text: line.trim() })
    })
  }
}

if (hits.length === 0) {
  console.log(
    `✓ check-swallowed-errors: ${products.length} 个后端没有把解析错误丢掉` +
      `（查了 ${PARSERS.length} 类反序列化调用）`,
  )
  process.exit(0)
}

// ── 基线（棘轮）──────────────────────────────────────────────────────
//
//	ops-version 是 0，从一开始就守住了。某个同类产品 是后接进来的，存量 23 处 ——
//	一次性清完要逐个判断"这里丢掉错误是不是有意的"，而那 23 处的判断
//	各不相同（SANs 解析失败退化成空列表 / 权限 JSON 坏了 fail-closed /
//	审计 diff 解析失败详情空白）。
//
//	所以先用基线锁住：**只减不增**。新写的一处都不许有。
//	⚠️ 基线是欠条，不是许可。降下来才算还上。
const BASELINES = {}

const byProduct = {}
for (const h of hits) {
  const p = h.file.split('/')[0]
  byProduct[p] = (byProduct[p] || 0) + 1
}
const over = []
for (const [p, n] of Object.entries(byProduct)) {
  const base = BASELINES[p] ?? 0
  if (n > base) over.push({ p, n, base })
}
if (over.length === 0) {
  console.log(
    `✓ check-swallowed-errors: 未超基线（` +
      Object.entries(byProduct).map(([p, n]) => `${p}=${n}/${BASELINES[p] ?? 0}`).join(', ') +
      `）`,
  )
  process.exit(0)
}

console.error('✗ 解析错误被 `_ =` 丢掉了（超出基线）：\n')
for (const o of over) {
  console.error(`  ${o.p}: ${o.n} 处，基线 ${o.base}\n`)
}
for (const h of hits) {
  console.error(`  ${h.file}:${h.line}`)
  console.error(`      ${h.text}\n`)
}
console.error(
  'Go 在类型不匹配时会把那个字段留成**零值**，而其余字段照常解析成功 ——\n' +
    '调用方看到的是「大部分参数生效了，就那一个没生效」，而且不报错。\n\n' +
    '实测过：MCP 客户端把 limit 传成字符串 "1"，\n' +
    'p.Limit 静默变 0 → 走默认上限 → AI 传 limit=1 拿回 71 行。\n\n' +
    '⚠️ "参数被静默忽略"比直接报错危险得多：报错了调用方会改，\n' +
    '   静默忽略则是它以为自己已经限制了，然后拿着全量继续往下走。\n\n' +
    '改法：把错误返回给调用方，并说清楚**哪个参数、要什么类型**。\n' +
    '参考 ops-version/backend/internal/api/mcp.go 的 decodeArgs()。',
)
process.exit(1)
