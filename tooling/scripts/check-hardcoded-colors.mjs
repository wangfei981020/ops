#!/usr/bin/env node
/**
 * 硬编码颜色检查。
 *
 * 白标能不能成立，只取决于一件事：组件里的颜色**有没有绕过 token 层**。
 * 只要有一处写死了 `bg-indigo-500`，客户换品牌色后那一处就不会跟着变，
 * 而且大概率没人发现 —— 因为它看起来仍然"是个紫色按钮"。
 *
 * 判定标准：
 *   ✗ 颜色字面量        #6E6CEF / rgb(...) / oklch(...) 直接写在类名里
 *   ✗ Tailwind 默认色板  bg-blue-500 / text-red-600 / border-slate-200
 *   ✗ 黑白字面类        bg-white / text-black / stroke-white
 *   ✓ var(--ops-*)      仍然经过 token 层，白标照样生效
 *   ✓ 语义类            bg-primary / text-muted-foreground / border-border
 *
 * 用法：node tooling/scripts/check-hardcoded-colors.mjs
 */

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { frontendDirs } from './lib/products.mjs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '../..')
// ⚠️ 产品部分自动发现，不要写死（见 lib/products.mjs 的说明）
const TARGETS = ['packages/ui/src', 'packages/design/src', ...frontendDirs()]
const EXT = /\.(tsx?|jsx?)$/

const PALETTE =
  'slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose'
const PROP = 'bg|text|border|ring|fill|stroke|from|via|to|shadow|outline|decoration|accent|caret|divide|placeholder'

const RULES = [
  {
    // 十六进制色。排除注释里的说明文字 —— 注释里举反例是允许的。
    re: /(?<!\/\/.*)(?<!\* .*)#[0-9a-fA-F]{3,8}\b/g,
    msg: '十六进制色值',
  },
  {
    re: new RegExp(`\\b(?:${PROP})-\\[(?:#|rgb|hsl|oklch|oklab|color\\()`, 'g'),
    msg: '任意值里的颜色字面量',
  },
  {
    re: new RegExp(`\\b(?:${PROP})-(?:${PALETTE})-\\d{2,3}\\b`, 'g'),
    msg: 'Tailwind 默认色板',
  },
  {
    re: new RegExp(`\\b(?:${PROP})-(?:white|black)\\b`, 'g'),
    msg: '黑白字面类（用 primary-foreground / foreground 代替）',
  },
]

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === 'node_modules' || name === 'dist') continue
      walk(p, out)
    } else if (EXT.test(name)) {
      out.push(p)
    }
  }
  return out
}

const problems = []
let scanned = 0

for (const target of TARGETS) {
  let files
  try {
    files = walk(join(ROOT, target))
  } catch {
    continue // 目录还没建，跳过
  }
  for (const file of files) {
    scanned++
    const lines = readFileSync(file, 'utf8').split('\n')
    lines.forEach((line, i) => {
      // 整行注释直接跳过：文档里需要能写出反例
      const trimmed = line.trim()
      if (trimmed.startsWith('//') || trimmed.startsWith('*') || trimmed.startsWith('/*')) return

      for (const rule of RULES) {
        rule.re.lastIndex = 0
        const m = rule.re.exec(line)
        if (m) {
          problems.push({
            file: relative(ROOT, file),
            line: i + 1,
            match: m[0],
            msg: rule.msg,
          })
          break
        }
      }
    })
  }
}

if (problems.length > 0) {
  console.error(`✗ 发现 ${problems.length} 处硬编码颜色：\n`)
  for (const p of problems) {
    console.error(`  ${p.file}:${p.line}  ${p.match}   ← ${p.msg}`)
  }
  console.error('\n改用语义类名（bg-primary / text-muted-foreground / border-border）。')
  console.error('白标只要有一处绕过 token 层就不成立，而这种问题肉眼看不出来。')
  process.exit(1)
}

console.log(`✓ 无硬编码颜色（扫描 ${scanned} 个文件）`)
