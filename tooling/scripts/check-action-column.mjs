#!/usr/bin/env node
/**
 * 守卫：标了 `meta: { action: true }` 的操作列必须是**最后一列**。
 *
 * # 为什么
 *
 * 操作列固定在最右侧（DataTable 的 sticky），而"最右侧"的前提是它真的在最后。
 * 不在最后的话，被钉住的其实是倒数第二个位置 —— 而真正的最后一列
 * 会在横向滚动时飘走，看起来像页面坏了。
 *
 * # 🔴 这不是假设，是已经发生过的
 *
 * 节点页的操作列是第 12 列，后面还挂着「实时用量」——
 * 那一列是在 `index.tsx` 里 `...nodeColumns()` **之后追加**的，
 * 绕过了 `columns.tsx`。实测 1512px 窗口下：
 *
 *   13 列，横向溢出 147px，最后一列 left=1805 > 可视区右边界 1728
 *   → 整列在屏幕外，不横向滚根本看不见
 *
 * 集群页、数据源页也是同样的形态（操作列后面还有列）。
 *
 * ⚠️ "往列数组后面追加一列"是个很自然的写法，没人会想到它破坏了操作列的位置。
 * 所以这条只能由工具守 —— 约定拦不住，它已经发生了三次。
 *
 * # 判据
 *
 * 在 `columns.tsx` 里找 `meta: { action: true }` 的列，确认它之后没有别的列定义。
 * 再在页面里找 `...xxxColumns(` 之后是否还追加了列 —— 那是绕过列定义文件的入口。
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
    else if (/\.tsx$/.test(name)) out.push(p)
  }
  return out
}

const scopes = process.argv.slice(2)
const ALL = products()
const TARGETS = scopes.length ? ALL.filter((p) => scopes.includes(p)) : ALL
if (scopes.length && TARGETS.length === 0) {
  console.error(`✗ check-action-column: 没有匹配的产品（传入 ${scopes.join(', ')}）`)
  process.exit(1)
}

const problems = []
let checked = 0

for (const prod of TARGETS) {
  const feDir = join(ROOT, prod, 'frontend/src')
  for (const f of walk(feDir)) {
    const src = readFileSync(f, 'utf8')

    // ① 每个列数组里：action 列之后不能再有列
    //
    // 🔴 判定必须限定在**同一个列数组**内。原来是 `indexOf('action: true')`
    //    然后扫整个文件剩余部分 —— 一个文件里定义了多张表时（同步页有
    //    Harbor/规则/执行记录三张），第一张表的操作列后面当然还有第二张表的列，
    //    于是**必然误报**。误报会让人把守卫关掉，比漏报更糟。
    //
    // 列数组的边界：`const xxxCols: ColumnDef<...>[] = [ ... ]`
    for (const arr of src.matchAll(
      /(?:const|let)\s+\w+\s*:\s*ColumnDef<[^>]*>\[\]\s*=\s*\[([\s\S]*?)\n\s{0,4}\]/g,
    )) {
      const body = arr[1]
      const at = body.indexOf('action: true')
      if (at < 0) continue
      checked++
      if (/\n\s*(?:accessorKey|id):\s*'/.test(body.slice(at))) {
        problems.push({
          file: relative(ROOT, f),
          line: src.slice(0, arr.index + at).split('\n').length,
          why: '同一个列数组里，操作列之后还有别的列 —— 它不是最后一列',
        })
      }
    }

    // ② 页面里：展开列工厂之后又追加了列
    //
    //	⚠️ 这一条是真正的元凶。列定义文件里排得好好的，
    //	页面里一句 `...nodeColumns(...)` 后面接一个 `{ id: 'live', ... }`
    //	就把操作列挤到倒数第二个位置了 —— 而列定义文件里看不出任何异常。
    //
    //	🔴 但"追加"本身不是问题，**追加到操作列之后**才是。
    //
    //	集群页追加的那一列就是操作列自己（页面提供操作列、工厂只给数据列），
    //	那完全正常。我第一版不分青红皂白只要追加就报，当场误报了它 ——
    //	而守卫误报的代价不是烦人，是人开始学着忽略它。
    //
    //	判据：看这个页面对应的 columns.tsx 里**有没有** action 标记。
    //	有 → 工厂已经把操作列排在末尾了，再追加就是挤开它 → 报
    //	没有 → 操作列由页面自己提供，追加是正常写法 → 不报
    for (const m of src.matchAll(/\.\.\.(\w*[Cc]olumns)\([\s\S]{0,900}?\),?\s*\n\s*\{/g)) {
      const tail = src.slice(m.index, m.index + 1600)
      if (!/\n\s*(?:accessorKey|id):\s*'/.test(tail)) continue
      const colFile = join(f, '..', 'columns.tsx')
      if (!existsSync(colFile)) continue
      // 工厂里没有操作列 → 操作列由页面提供，追加正常
      if (!readFileSync(colFile, 'utf8').includes('action: true')) continue
      problems.push({
        file: relative(ROOT, f),
        line: src.slice(0, m.index).split('\n').length,
        why: `在 ${m[1]}() 之后追加了列，而它里面的操作列已经排在末尾 —— 追加会把操作列挤离最后一位`,
      })
    }
  }
}

/*
 * 第二类问题：**有操作列却没标 `meta: { action: true }`**。
 *
 * 🔴 这条是补的 —— 原来只查「标了的必须在最后」，
 * 于是一处都没标时守卫**全绿**，而 sticky 在共享包里躺着从没生效过：
 * ops-version 五个页面的操作列全部没标，横向滚动时按钮跟着飘走。
 *
 * 判据：一个列数组里**出现了 <Button> 却没有 action: true**。
 * 粗，但不误报 —— 表格里出现按钮而没有任何一列被钉住，几乎必然是漏标。
 * ⚠️ 试过用正则去匹配「最后一个列块」，匹配不到单行写法
 * （`{ id: 'ops', header: '', ... }` 一行写完）。判据宁可粗，不能脆。
 */
for (const prod of TARGETS) {
  for (const f of walk(join(ROOT, prod, 'frontend/src'))) {
    const src = readFileSync(f, 'utf8')
    for (const arr of src.matchAll(
      /(?:const|let)\s+(\w+)\s*:\s*ColumnDef<[^>]*>\[\]\s*=\s*\[([\s\S]*?)\n\s{0,4}\]/g,
    )) {
      const [, varName, body] = arr
      if (!body.includes('<Button')) continue
      if (body.includes('action: true')) continue
      problems.push({
        file: relative(ROOT, f),
        line: src.slice(0, arr.index).split('\n').length,
        why: `列数组 ${varName} 里有按钮，却没有一列标 meta: { action: true } —— ` +
          'sticky 不生效，横向滚动时操作按钮会飘走',
      })
    }
  }
}

if (problems.length > 0) {
  console.error('✗ check-action-column: 操作列有问题：\n')
  for (const p of problems) {
    console.error(`    ${p.file}:${p.line}`)
    console.error(`      ${p.why}`)
  }
  console.error(`
操作列固定在最右侧靠的是 sticky，而"最右侧"的前提是它真的在最后。
不在最后的话，被钉住的其实是倒数第二个位置，而真正的最后一列
会在横向滚动时飘走 —— 看起来像页面坏了。

实测节点页：13 列、横向溢出 147px，最后一列整个在屏幕外。

修法：把追加的那一列挪到列工厂**内部**（列定义文件里），
让操作列始终排在数组末尾。`)
  process.exit(1)
}

console.log(
  `✓ check-action-column: ${checked} 处操作列都在最后一列` +
    `（范围 ${scopes.length ? scopes.join(', ') : '全部产品'}）`,
)
