/**
 * 语言包里不许出现 Markdown 标记。
 *
 * 界面用的是 React 文本节点，`**加粗**` 不会被解析，会**原样显示成星号**。
 * 而这只在那句文案真的被渲染出来时才暴露 —— 写的时候看不出，
 * code review 也看不出（JSON 里那行读起来很正常）。
 *
 * 这个坑在效果图上栽过两次、在门户文案上第三次，所以做成防线。
 * 要强调就换引号「」或改写句子，别指望渲染层帮你解析。
 */
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '../..')
/**
 * 语言包有两处合法位置，**两处都要扫**。
 *
 * ⚠️ 原来只写了共享包，于是把语言包放在产品内的产品完全不受这个守卫约束 ——
 * 而它照常打印「✓ 语言包无 Markdown 标记」。
 * 实测后果：`审计**一直在记录**` 在界面上把星号原样渲染了出来，
 * 而守卫是绿的。这已经是同一类问题第三次出现（check-i18n-usage、
 * check-error-keys 都栽在「只扫一个产品/一个目录」上）。
 *
 * 判据：一个只检查部分范围的守卫，它的绿色会被当成"全都查过了"。
 */
const DIRS = [join(ROOT, 'packages/i18n/locales')]
for (const e of readdirSync(ROOT, { withFileTypes: true })) {
  if (!e.isDirectory() || !e.name.startsWith('ops-')) continue
  const d = join(ROOT, e.name, 'frontend/src/locales')
  if (existsSync(d)) DIRS.push(d)
}

// 只查确定会出问题的：成对的 ** 与行首 - / * 列表符
const PATTERNS = [
  // ⚠️ 路径通配符 `/**` 不是加粗。接口级策略的文案里要写
  // 「子树用 /** 结尾（如 /api/v1/hosts/**）」，三个 /** 会被朴素的成对正则
  // 当成一段加粗 —— 误报一次，人就会开始绕过这道检查。
  // 所以要求开头的 ** 前面不是 /。
  { re: /(^|[^/*])\*\*[^*]+\*\*/, name: 'Markdown 加粗 **…**' },
  { re: /^[-*] /m, name: '行首列表符' },
  { re: /`[^`]+`/, name: 'Markdown 反引号' },
]

const problems = []
function walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p)
    else if (name.endsWith('.json')) scan(p)
  }
}
function scan(file) {
  const data = JSON.parse(readFileSync(file, 'utf8'))
  const rel = file.slice(ROOT.length + 1)
  const visit = (node, path) => {
    if (typeof node === 'string') {
      for (const { re, name } of PATTERNS) {
        if (re.test(node)) problems.push(`${rel} · ${path}：含${name}`)
      }
      return
    }
    if (node && typeof node === 'object') {
      for (const [k, v] of Object.entries(node)) visit(v, path ? `${path}.${k}` : k)
    }
  }
  visit(data, '')
}

for (const dir of DIRS) walk(dir)
if (problems.length) {
  console.error(`✗ 语言包里有 ${problems.length} 处 Markdown 标记，界面上会原样显示：\n`)
  for (const p of problems) console.error(`  ${p}`)
  console.error('\n界面是 React 文本节点，不解析 Markdown。要强调请改用「」或重写句子。')
  process.exit(1)
}
console.log('✓ 语言包无 Markdown 标记')
