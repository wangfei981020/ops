#!/usr/bin/env node
/**
 * 守卫：前端不能对「后端可能给 null」的数组字段直接解引用。
 *
 * # 为什么需要这个守卫
 *
 * Go 的 nil 切片序列化成 JSON `null`，不是 `[]`。而前端接口类型上写的是
 * `string[]`（不可空），于是这行代码在类型检查里完全合法：
 *
 *     {p.service_include.length > 0 && ...}
 *
 * 后端只要有一条记录那个字段没填，页面就 `Cannot read properties of null`。
 *
 * ⚠️ 这个失败**极难从症状定位**：
 *   - 接口 200，返回体看着正常（就是多了几个 null）
 *   - 后端日志干净，没有任何异常
 *   - 表现是整个弹窗/整页白屏，而不是那一小块出问题
 *     （React 渲染期抛异常会卸载整棵子树）
 *   - 只在「那个字段是空的」那条数据上复现 —— 本地造的测试数据往往都填了，
 *     所以本地一切正常，客户那边一点就白
 *
 * 实测在 ops-version 撞到两次：
 *   projects.service_include  → 平台编辑弹窗白屏（新建的项目没填规则）
 *   harbor.policy_filter      → Harbor 配置编辑白屏（没配过过滤规则的都会）
 *
 * # 判据
 *
 * 在 .ts/.tsx 里找 `xxx.<snake_case 字段>.<数组方法>` 的写法，
 * 且该字段在某个 interface 里被声明成**不带 `| null`/`| undefined` 的裸数组**。
 *
 * 只认 snake_case：那是后端来的字段。前端自己造的 camelCase 变量不在此列 ——
 * 它们的 null 由 TypeScript 管得住。
 *
 * # 怎么修（两条都要做，缺一不可）
 *
 *   ① 后端：出参的切片字段兜成 `[]`，别放 nil 出去
 *      （最好在产生它的那个函数里兜一次，别在每个调用点各兜一次 —— 后者总会漏）
 *   ② 前端：类型如实写成 `T[] | null`，让 tsc 逼着你写 `?? []`
 *
 * 只做 ① 的话，哪天后端换个人写个新接口又会漏；只做 ② 的话类型是对了，
 * 但每个使用点都得记得兜底。两条一起做，这类问题才真的关掉。
 */
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

const ROOT = new URL('../..', import.meta.url).pathname.replace(/\/$/, '')
const product = process.argv[2]
if (!product) {
  console.error('用法: check-nullable-arrays.mjs <产品目录名>')
  process.exit(2)
}
const scope = join(ROOT, product)

/** 数组方法：调用它们时 null 一定炸 */
const ARRAY_OPS =
  'length|map|join|filter|forEach|slice|some|every|find|findIndex|reduce|includes|flatMap|sort|concat|at'

function walk(dir, out = []) {
  let entries
  try {
    entries = readdirSync(dir)
  } catch {
    return out
  }
  for (const e of entries) {
    const p = join(dir, e)
    if (statSync(p).isDirectory()) {
      if (!/node_modules|\/dist|\.git/.test(p)) walk(p, out)
    } else if (/\.tsx?$/.test(p)) out.push(p)
  }
  return out
}

const files = walk(scope)

// ── 一遍：收集声明成「裸数组」的 snake_case 字段 ──
//
// 同时记下它在哪声明的，报错时要指给人看。
const bare = new Map() // 字段名 → 声明位置
for (const f of files) {
  const lines = readFileSync(f, 'utf8').split('\n')
  lines.forEach((line, i) => {
    // 形如：  service_include: string[]        ← 裸数组，要管
    //         service_include: string[] | null ← 已如实声明，不管
    //         service_include?: string[]       ← 可选，tsc 已经管得住
    const m = line.match(/^\s*([a-z][a-z0-9]*(?:_[a-z0-9]+)+)\s*:\s*[A-Za-z_$][\w.]*\[\]\s*$/)
    if (m) bare.set(m[1], `${relative(ROOT, f)}:${i + 1}`)
  })
}

// ── 二遍：找直接解引用 ──
const hits = []
for (const f of files) {
  const lines = readFileSync(f, 'utf8').split('\n')
  lines.forEach((line, i) => {
    // 已经兜过底的写法要放过：`?.` 和 `?? []`
    const re = new RegExp(`\\.([a-z][a-z0-9]*(?:_[a-z0-9]+)+)\\.(${ARRAY_OPS})\\b`, 'g')
    for (const m of line.matchAll(re)) {
      if (!bare.has(m[1])) continue
      // `(x.field ?? []).map(...)` —— 同一行里出现兜底就认为处理过了
      if (new RegExp(`${m[1]}\\s*(\\?\\?|\\|\\|)`).test(line)) continue
      hits.push({
        at: `${relative(ROOT, f)}:${i + 1}`,
        field: m[1],
        op: m[2],
        decl: bare.get(m[1]),
      })
    }
  })
}

if (hits.length > 0) {
  console.error(`✗ check-nullable-arrays: ${hits.length} 处直接解引用了可能为 null 的接口数组字段：\n`)
  for (const h of hits) {
    console.error(`  ${h.at}`)
    console.error(`    .${h.field}.${h.op}(...) —— 后端给 null 时这里会抛异常，整棵子树白屏`)
    console.error(`    声明在 ${h.decl}`)
  }
  console.error(`
  两条都要做：
    ① 后端把该字段的空值兜成 []（在产生它的函数里兜，别在调用点各兜各的）
    ② 前端类型改成 \`T[] | null\`，让 tsc 逼你在每个使用点写 \`?? []\`
`)
  process.exit(1)
}

console.log(
  `✓ check-nullable-arrays: 接口数组字段都没有裸解引用` +
    `（${bare.size} 个裸数组字段，扫了 ${files.length} 个文件，范围 ${product}）`,
)
