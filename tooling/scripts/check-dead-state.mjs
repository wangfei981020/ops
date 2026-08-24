#!/usr/bin/env node
/**
 * 找出「声明了却从来没人调用」的 useState setter。
 *
 * 典型症状是一个功能**看起来接好了**：组件 import 了、渲染了、state 也在，
 * 只是没有任何地方去 set 它，于是那个弹窗/面板永远打不开。
 *
 * 实测撞到过：DiagnoseDialog 接进 Tables.tsx，`diagnosing` state 也在，
 * 但全文件没有一处 setDiagnosing(...) —— 诊断功能完全不可达。
 * 而 tsc 是通过的（state 确实"被用到"了，用在 dialog 的 props 上），
 * 页面也正常渲染，只是少了那个按钮。属于「后端算了前端没接」的同一类。
 *
 * 判据：setter 的**所有**调用传的都是空值（null / false / undefined / ''），
 * 且初始值也是空值 —— 那这个 state 永远不可能变成 truthy，
 * 依赖它的界面（弹窗、面板）也就永远不会出现。
 *
 * ⚠️ 第一版判据是「setter 只出现一次」，反向验证时发现它**抓不到真实的那个 bug**：
 * setDiagnosing 在 `onClose={() => setDiagnosing(null)}` 里出现了，计数是 2，
 * 于是判为「有调用」。而"只有关闭、没有打开"恰恰就是那个缺陷的形状。
 * 差一点就上线了一个抓不到自己要防的东西的守卫。
 *
 * 反向验证不是可选步骤：一个永远打印 ✓ 的守卫比没有守卫更危险，
 * 因为它的绿色会被当成"查过了"。
 */
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'

const repo = resolve(new URL('../..', import.meta.url).pathname)
const scopes = process.argv.slice(2)

/**
 * 剥掉注释再分析。
 *
 * ⚠️ 为什么必须剥：我在缺陷旁边写了句说明注释「…没有一处调 setDiagnosing…」，
 * 那串标识符被算成一次「裸引用」，守卫于是跳过了这个 state ——
 * **解释这个 bug 的注释，把检查这个 bug 的守卫关掉了。**
 *
 * ⚠️ 为什么不能用正则剥：试了两版，都在真实代码上出错。
 *
 *   第一版（通用块注释）：另一个产品 的路径规则页里有路径通配符字符串（斜杠+星号），
 *   它启动了一次块注释匹配，一路吃到下一个「星斜杠」，删掉中间几十行真代码，
 *   于是明明写着 onChange={(e) => setAppID(...)} 的正常代码被报成「从未被调用」。
 *
 *   第二版（只认 JSX 注释形式）：花括号后紧跟 JSDoc 是极常见的写法
 *   （对象或函数体开头就写注释），被当成 JSX 注释的开头，非贪婪匹配又在
 *   一百行外找到了真正的 JSX 注释结尾，一口气吞掉 2893 个字符。
 *
 * 注释边界本质上要求分辨「这个斜杠星在字符串里还是在代码里」，
 * 而这正是正则做不到的事。所以下面是个老实的词法扫描器：
 * 它跟踪字符串/模板串状态，因此字符串里的斜杠星永远不会被当成注释。
 */
function stripComments(src) {
  let out = ''
  let i = 0
  // normal | line | block | ' | " | `
  let state = 'normal'
  while (i < src.length) {
    const c = src[i]
    const c2 = src[i + 1]
    if (state === 'normal') {
      if (c === '/' && c2 === '/') { state = 'line'; i += 2; continue }
      if (c === '/' && c2 === '*') { state = 'block'; i += 2; continue }
      if (c === "'" || c === '"' || c === '`') { state = c; out += c; i++; continue }
      out += c; i++; continue
    }
    if (state === 'line') {
      if (c === '\n') { state = 'normal'; out += c }
      i++; continue
    }
    if (state === 'block') {
      if (c === '*' && c2 === '/') { state = 'normal'; out += ' '; i += 2; continue }
      // 保留换行，行号才不会错位
      if (c === '\n') out += c
      i++; continue
    }
    // 字符串内：反斜杠转义整个吃掉，免得 \' 被当成结束
    if (c === '\\') { out += c + (c2 ?? ''); i += 2; continue }
    if (c === state) state = 'normal'
    out += c; i++
  }
  return out
}

function* walk(dir) {
  let entries
  try { entries = readdirSync(dir, { withFileTypes: true }) } catch { return }
  for (const e of entries) {
    const p = join(dir, e.name)
    if (e.isDirectory()) { if (e.name !== 'node_modules') yield* walk(p) }
    else if (/\.tsx$/.test(e.name)) yield p
  }
}

const dirs = readdirSync(repo, { withFileTypes: true })
  .filter((e) => e.isDirectory() && e.name.startsWith('ops-'))
  .map((e) => e.name)
  .filter((n) => !scopes.length || scopes.includes(n))
  .map((n) => join(repo, n, 'frontend/src'))
  .filter((d) => { try { return statSync(d).isDirectory() } catch { return false } })

/**
 * 已建档、尚未修复的挂账。
 *
 * 不直接阻塞构建 —— 修它们要决定"按钮放哪"，那是产品决策，不该卡在别人的发版上。
 * 但每次都打印出来：**静默豁免等于遗忘**，而遗忘正是这类缺陷能活这么久的原因。
 */
const KNOWN = [
  // 的两条已修（2026-08-18）：
  //   cdn/index.tsx     setAnalysis  → 工具条加「规则分析」按钮
  //   domains/index.tsx setRenewals  → 续费按钮旁加「续费记录」
  //
  // 🔴 修完必须从这里删掉，否则守卫对这两处永远只警告不拦截 ——
  // 一条修完却留在挂账里的记录，等于把那个位置永久豁免了，
  // 而下一次有人不小心删掉按钮时，守卫会安静地放行。
  // （挂账的意义是"暂时不拦"，不是"永远不管"。）
]

const dead = []
let scanned = 0

for (const dir of dirs) {
  for (const file of walk(dir)) {
    const src = stripComments(readFileSync(file, 'utf8'))
    scanned++
    for (const m of src.matchAll(
      /\bconst\s*\[\s*\w+\s*,\s*(set[A-Z]\w*)\s*\]\s*=\s*useState[^(]*\(([^)]*)\)/g,
    )) {
      const [, setter, initRaw] = m
      const EMPTY = /^(null|false|undefined|''|""|``)$/
      if (!EMPTY.test(initRaw.trim())) continue // 初始就有值，本检查不适用

      // ⚠️ setter 被当函数引用整个传出去（`onChange={setKeyword}`）时，
      // 实参在别的组件里，这里静态看不到。必须跳过，否则会把
      // 完全正常的受控输入框全报成死代码 —— 反向验证时一次误报了两处。
      // 声明式 `const [x, setX] =` 自身也是一次裸引用，所以门槛是 > 1。
      const bareRefs = src.match(new RegExp(`\\b${setter}\\b(?!\\s*\\()`, 'g'))?.length ?? 0
      if (bareRefs > 1) continue

      // 收集所有调用点的实参。整词匹配避免 setFoo 命中 setFooBar。
      const calls = [...src.matchAll(new RegExp(`\\b${setter}\\s*\\(([^)]*)\\)`, 'g'))]
      if (calls.length === 0) {
        dead.push({ file: file.replace(repo + '/', ''), setter, why: '从未被调用' })
        continue
      }
      // 全部调用都在传空值 = 只能关、不能开
      if (calls.every((c) => EMPTY.test(c[1].trim()))) {
        dead.push({
          file: file.replace(repo + '/', ''),
          setter,
          why: `${calls.length} 处调用全部传空值，没有任何一处能让它变成 truthy`,
        })
      }
    }
  }
}

const known = dead.filter((d) => KNOWN.some((k) => k.file === d.file && k.setter === d.setter))
const fresh = dead.filter((d) => !known.includes(d))

for (const d of known) {
  const issue = KNOWN.find((k) => k.file === d.file && k.setter === d.setter).issue
  console.log(`⚠️  挂账 ${issue}: ${d.setter}() ${d.why} —— ${d.file}`)
}

if (fresh.length) {
  const dead = fresh
  console.error('✗ check-dead-state: 有 useState setter 从未被调用 —— 对应功能不可达：\n')
  for (const d of dead) console.error(`    ${d.setter}() —— ${d.why}\n      ${d.file}`)
  console.error('\n这类缺陷 tsc 查不出来（state 本身"被用到"了），页面也正常渲染，')
  console.error('只是那个入口根本不存在。补上触发器，或删掉这段死代码。')
  process.exit(1)
}

console.log(
  `✓ check-dead-state: ${scanned} 个 tsx 里的 useState setter 都有调用` +
    `${scopes.length ? `（范围 ${scopes.join(', ')}）` : '（范围 全部产品）'}` +
    `${known.length ? `，另有 ${known.length} 条已建档挂账` : ''}`,
)
