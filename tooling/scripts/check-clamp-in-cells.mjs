#!/usr/bin/env node
/**
 * 守卫：表格单元格里用 `line-clamp-*` 必须同时写 `whitespace-normal`。
 *
 * # 为什么
 *
 * `DataTable` 给每个 `td` 加了 `whitespace-nowrap` —— 表格单元格不折行，
 * 那是个合理的默认。但它会**继承**给单元格里的所有文本节点，
 * 于是 `line-clamp-2` 彻底失效：
 *
 *   文字不折行 → 只有一行可数 → line-clamp 无从裁行
 *              → 超出部分被 overflow:hidden **横向**裁掉
 *
 * 实测（巡检页结果列，460px 宽）：`-webkit-line-clamp: 2` 在、
 * `overflow: hidden` 也在，但 `scrollWidth - clientWidth = 69px` 和 `132px` ——
 * 裁的是横向不是行数，那段任务结果既读不全、当时也没有「展开」按钮。
 *
 * # ⚠️ 为什么必须是守卫而不是"记住这件事"
 *
 * 代码里 `line-clamp-2` **写得完全正确**。失效的原因在另一个文件的
 * 另一个 class 上，而两者之间没有任何静态联系。
 * 看代码看不出来，code review 也看不出来 ——
 * 只有把页面渲染出来、量 `scrollWidth` 才能发现。
 *
 * 所以这条规则只能由工具守。
 *
 * # 判据
 *
 * 同一个 className 字符串里出现 `line-clamp-N` 时，必须同时出现
 * `whitespace-normal`（或 `whitespace-pre-wrap` —— 那个也允许折行）。
 *
 * ⚠️ 不做"是否真在表格里"的判断：那需要顺着组件树推，而推错的代价
 * （漏报）比多要一个 class 的代价大得多。多写一个 `whitespace-normal`
 * 在非表格场景里也无害。
 */

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { ROOT } from './lib/products.mjs'

const scopes = process.argv.slice(2)

function walk(dir, out = []) {
  let entries
  try {
    entries = readdirSync(dir)
  } catch {
    return out
  }
  for (const e of entries) {
    if (e === 'node_modules' || e === 'dist' || e === '.git') continue
    const p = join(dir, e)
    if (statSync(p).isDirectory()) walk(p, out)
    else out.push(p)
  }
  return out
}

const inScope = (f) =>
  scopes.length === 0 || scopes.some((s) => relative(ROOT, f).startsWith(s))

const files = walk(ROOT).filter((f) => /\.tsx$/.test(f) && inScope(f))

/** 抓 className="..." / className={`...`} 里的内容 */
const CLASS_ATTR = /className=(?:"([^"]*)"|\{`([^`]*)`\})/g

/**
 * 把 className 的内容切成**互斥的片段**再逐个判。
 *
 * ⚠️ 不能把整串当一个整体判 —— 第一版就是这么错的。
 *
 *	实际写法是模板字符串里套三元：
 *	  className={`text-xs ${open ? 'whitespace-pre-wrap' : 'line-clamp-2'}`}
 *	两个分支**永不同时生效**，但整串里既有 line-clamp 也有 whitespace，
 *	于是守卫认为"配齐了"→ 放过。变异验证时把 whitespace-normal 删掉，
 *	守卫依然是绿的 —— 它放过了自己本来就是来防的那件事。
 *
 *	这和 check-i18n-usage 当年漏掉三元里的 key 是同一个坑（第七次了）。
 *
 * 切法：按引号切出所有字符串字面量，每个字面量是一个独立片段；
 * 引号之外的裸文本（模板里直接写的 class）合并成一个片段。
 */
function classSegments(cls) {
  const quoted = [...cls.matchAll(/'([^']*)'|"([^"]*)"/g)].map((m) => m[1] ?? m[2] ?? '')
  const bare = cls.replace(/'[^']*'|"[^"]*"/g, ' ').replace(/\$\{[^}]*\}/g, ' ')
  return [...quoted, bare]
}

const offenders = []
for (const f of files) {
  const src = readFileSync(f, 'utf8')
  for (const m of src.matchAll(CLASS_ATTR)) {
    const cls = m[1] ?? m[2] ?? ''
    if (!/\bline-clamp-\d/.test(cls)) continue
    // 逐片段判：只要**有一个**片段里 line-clamp 落单，就是问题
    for (const seg of classSegments(cls)) {
      if (!/\bline-clamp-\d/.test(seg)) continue
      if (/\bwhitespace-(normal|pre-wrap|pre-line)\b/.test(seg)) continue
      offenders.push({
        file: relative(ROOT, f),
        line: src.slice(0, m.index).split('\n').length,
        snippet: seg.replace(/\s+/g, ' ').trim().slice(0, 90),
      })
    }
  }
}

if (offenders.length > 0) {
  console.error('✗ 用了 line-clamp 但没写 whitespace-normal：\n')
  for (const o of offenders) {
    console.error(`  ${o.file}:${o.line}`)
    console.error(`      ${o.snippet}`)
  }
  console.error(`
DataTable 给每个 td 加了 \`whitespace-nowrap\`，它会继承给单元格里的文本。
文字不折行 → line-clamp 只有一行可数 → 裁的是**横向**，不是行数，
超出的内容被直接吞掉，而且看不出来。

实测巡检页结果列：-webkit-line-clamp:2 在、overflow:hidden 在，
但 scrollWidth - clientWidth = 69px / 132px —— 文字被横向裁掉了。

⚠️ 这个错误在代码里**看不出来**：line-clamp-2 写得完全正确，
失效的原因在另一个文件的另一个 class 上。所以由这个守卫来管。

在同一个 className 里补上 \`whitespace-normal break-words\`。`)
  process.exit(1)
}

console.log(
  `✓ line-clamp 的用法都带了 whitespace-normal（扫了 ${files.length} 个 tsx，范围 ${
    scopes.length ? scopes.join(', ') : '全部产品'
  }）`,
)
