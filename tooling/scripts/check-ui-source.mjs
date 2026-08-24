#!/usr/bin/env node
/**
 * Tailwind v4 只扫描本项目源码。用了 @ops/ui 却没在 CSS 入口写
 * `@source '.../packages/ui/src'` 的话，共享组件包里用到、而产品代码里
 * 恰好没用过的工具类会整片丢不生成。
 *
 * 这个缺陷不会报错、不会白屏、控制台干净 —— 组件照常渲染，只是没样式。
 * 实测后果：AppShell 的 flex-1 / min-w-0 没生成，main 从 1728px 塌到 479px，
 * 而页面看上去只是「有点窄」，肉眼极难归因到 CSS 构建配置。
 *
 * 判据必须是「解析出的路径真的指到 packages/ui」，不能只 grep 有没有
 * @source 这个词 —— 指错目录同样会静默丢样式。
 */
import { existsSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'

const cwd = process.cwd()
const pkgPath = resolve(cwd, 'package.json')
if (!existsSync(pkgPath)) process.exit(0)

const pkg = JSON.parse(readFileSync(pkgPath, 'utf8'))
const deps = { ...pkg.dependencies, ...pkg.devDependencies }
if (!deps['@ops/ui']) process.exit(0)   // 没用共享组件包就与本检查无关

const cssPath = ['src/styles.css', 'src/index.css', 'src/main.css']
  .map((p) => resolve(cwd, p))
  .find(existsSync)

if (!cssPath) {
  console.error('✗ check-ui-source: 依赖 @ops/ui 但找不到 CSS 入口（src/styles.css）')
  process.exit(1)
}

const css = readFileSync(cssPath, 'utf8')
const sources = [...css.matchAll(/@source\s+['"]([^'"]+)['"]/g)].map((m) => m[1])

// 逐条把相对路径解析成绝对路径，再看是否真的落在 packages/ui 里。
// 只检查字符串包含 'packages/ui' 会被 `@source '../packages/ui'`（指错层级、
// 目录根本不存在）骗过去，而那种写法的后果跟没写完全一样。
const hit = sources.find((s) => {
  const abs = resolve(dirname(cssPath), s)
  return abs.replace(/\\/g, '/').includes('/packages/ui') && existsSync(abs)
})

if (!hit) {
  console.error(`✗ check-ui-source: ${pkg.name} 依赖 @ops/ui，但 ${cssPath.replace(cwd + '/', '')} 里`)
  console.error("  没有指向 packages/ui/src 的有效 @source —— @ops/ui 的工具类会静默丢失。")
  console.error(`  已声明的 @source: ${sources.length ? sources.join(', ') : '(无)'}`)
  console.error("  修复：@source '../../../packages/ui/src';")
  process.exit(1)
}

console.log(`✓ check-ui-source: ${pkg.name} 已扫描 @ops/ui 源码 (${hit})`)
