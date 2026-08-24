#!/usr/bin/env node
/**
 * 守卫：受控输入框的 onChange **不能做会删字符的规范化**。
 *
 * # 为什么
 *
 * 受控组件里，onChange 拿到的值经过转换后存进 state，再由 state 渲染回 value。
 * 只要这个转换会**删掉字符**，用户就永远打不出那个字符 —— 它在按下的同一帧被抹掉。
 *
 * # 🔴 这不是假设，是已经发生过的（2026-08-19）
 *
 * ops-version 的平台配置有五个多行框（集群 / ns 包含 / ns 排除 / 服务包含 / 服务排除）：
 *
 *   value={fromLines(e.ns_include)}                          // string[] → 文本
 *   onChange={(v) => setEnv(i, { ns_include: toLines(v) })}  // 文本 → string[]
 *
 *   const toLines = (s) => s.split('\n').map(x => x.trim()).filter(Boolean)
 *
 * 敲回车 → 值变 "ops-*\n" → filter(Boolean) 丢掉尾部空行 → ["ops-*"]
 * → 重渲染 → "ops-*"。**换行当场被吃掉，第二行根本敲不进去。**
 *
 * 用户只能改用逗号分隔，而后端并不认逗号 —— 一路错到采集范围上。
 * ⚠️ trim() 是同一个病：末尾空格也打不出来。
 *
 * 规范化的位置是**提交**，不是按键。
 *
 * # 判据：跟着**互逆函数对**走，不是跟着 JSX 属性走
 *
 * ⚠️ 我第一版判据是「`value={A(...)}` 与 `onChange={...B(...)}` 成对出现」——
 *    在 ops-version 上 checked=0，**一处都没匹配到**，守卫全绿而 bug 还在。
 *    因为 value/onChange 藏在 `area()` 这个辅助函数里，调用处长这样：
 *
 *      area(fromLines(e.ns_include), (v) => setEnv(i, { ns_include: toLines(v) }))
 *
 *    「守卫绿了」和「没有问题」是两回事 —— 判据匹配不到目标时也是绿的。
 *
 * 现在认两种形态：
 *   ① 内联：`value={A(...)` … `onChange={` … `B(`
 *   ② 互逆对：同一表达式里出现 `fromX(` 与 `toX(`（也支持 parse/format、decode/encode）
 *
 * 两种都再去找转换函数的定义，实现含 `.filter(` 或 `.trim()` 才报。
 *
 * 判据窄是故意的：onChange 里出现 trim 未必有问题（比如只用来判空而不回写），
 * 宽判据的误报会让人把整个守卫关掉，比漏报更糟。
 */

import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { ROOT, products } from './lib/products.mjs'

function walk(dir, out = []) {
  if (!existsSync(dir)) return out
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist') continue
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (/\.tsx?$/.test(name)) out.push(p)
  }
  return out
}

const scopes = process.argv.slice(2)
const ALL = products()
const TARGETS = scopes.length ? ALL.filter((p) => scopes.includes(p)) : ALL
if (scopes.length && TARGETS.length === 0) {
  console.error(`✗ check-onchange-normalize: 没有匹配的产品（传入 ${scopes.join(', ')}）`)
  process.exit(1)
}

const problems = []
let checked = 0

for (const prod of TARGETS) {
  const files = walk(join(ROOT, prod, 'frontend/src'))
  const all = files.map((f) => ({ f, src: readFileSync(f, 'utf8') }))

  // 收集本产品所有 `const B = (...) => ...` / `function B(...)` 的实现体
  const defs = new Map()
  for (const { src } of all) {
    for (const m of src.matchAll(/(?:export\s+)?(?:const|let)\s+(\w+)\s*=\s*\(?[^=;]{0,120}=>/g)) {
      defs.set(m[1], src.slice(m.index, m.index + 400))
    }
    for (const m of src.matchAll(/(?:export\s+)?function\s+(\w+)\s*\(/g)) {
      defs.set(m[1], src.slice(m.index, m.index + 400))
    }
  }

  for (const { f, src } of all) {
    // 形态①：内联 value={A( … onChange={ … B(
    // 形态②：互逆对 —— 同一表达式里 fromX(…) 与 toX(…) 同时出现。
    //   `area(fromLines(x), (v) => setEnv(i, { k: toLines(v) }))` 就是这一种，
    //   而它正是实际出问题的写法。
    const hits = [
      ...src.matchAll(/value=\{(\w+)\([\s\S]{0,200}?onChange=\{[\s\S]{0,200}?\b(\w+)\(/g),
      ...src.matchAll(/\b(?:from|format|encode)(\w+)\([\s\S]{0,300}?\b(?:to|parse|decode)(\w+)\(/g),
    ]
    for (const m of hits) {
      // 互逆对要求两侧后缀一致（fromLines ↔ toLines），否则不是一对
      const isPair = /^(?:from|format|encode)/.test(m[0])
      if (isPair && m[1] !== m[2]) continue
      const to = isPair
        ? (src.slice(m.index, m.index + m[0].length).match(/\b(?:to|parse|decode)\w+/) || [])[0]
        : m[2]
      const body = to && defs.get(to)
      if (!body) continue
      checked++
      // 只看这个函数自身的实现体（到第一个换行后的 `)` 或行尾为止已由 400 字符窗口近似）
      const drops = []
      if (/\.filter\(/.test(body)) drops.push('.filter(')
      if (/\.trim\(\)/.test(body)) drops.push('.trim()')
      if (drops.length === 0) continue
      problems.push({
        file: relative(ROOT, f),
        line: src.slice(0, m.index).split('\n').length,
        why: `受控输入框把 onChange 的值交给 ${to}()，而它的实现里有 ${drops.join(' 和 ')} —— ` +
          '会删字符的规范化放在按键上，用户就永远打不出被删掉的那个字符',
      })
    }
  }
}

if (problems.length > 0) {
  console.error('✗ check-onchange-normalize: 受控输入框在 onChange 里做了会删字符的规范化：\n')
  for (const p of problems) {
    console.error(`    ${p.file}:${p.line}`)
    console.error(`      ${p.why}`)
  }
  console.error(`
受控组件里 onChange 的返回值会渲染回 value，所以任何**删字符**的转换
都会在按下的同一帧把字符抹掉 —— 表现是"这个键没反应"，而不是报错。

实测：ops-version 平台配置的五个多行框，因为 toLines 里有 filter(Boolean)，
回车全部失效，用户只能改用后端不认的逗号分隔。

修法：输入期只做无损转换（如纯 split），把 trim/filter/去重挪到**保存时**。`)
  process.exit(1)
}

console.log(
  `✓ check-onchange-normalize: ${checked} 处受控转换都没有在按键上删字符` +
    `（范围 ${scopes.length ? scopes.join(', ') : '全部产品'}）`,
)
