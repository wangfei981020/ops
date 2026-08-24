#!/usr/bin/env node
/**
 * @ops/ui 的组件（AppShell 等）会自己调 t('ns:key') 取文案。
 * 这是一份**隐式契约**：产品不写这些 key，组件就在界面上渲染出原始 key。
 *
 * check-i18n-usage 抓不到它 —— 那个脚本扫的是产品自己的源码，
 * 而这些调用在共享包里；产品代码里一次都没出现过。
 *
 * 实测漏掉的后果：侧边栏搜索框上写着 "search.open" 四个字母，
 * 功能完全正常，只有那几个字是错的，扫一眼界面根本不会停下来看。
 *
 * 判据要按**产品实际注册的命名空间**来算，不能笼统合并所有语言包 ——
 * 产品内 locales 和共享包 locales 是两套来源，产品可能只注册了其中一部分。
 */
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'

const repo = resolve(new URL('../..', import.meta.url).pathname)
const uiSrc = resolve(repo, 'packages/ui/src')

// 1. 收集 @ops/ui 自己要的 key
const required = new Set()
const walk = (dir) => {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = resolve(dir, e.name)
    if (e.isDirectory()) walk(p)
    else if (/\.tsx?$/.test(e.name)) {
      for (const m of readFileSync(p, 'utf8').matchAll(/\bt\(\s*'([a-z][\w-]*):([\w.]+)'/g)) {
        required.add(`${m[1]}:${m[2]}`)
      }
    }
  }
}
walk(uiSrc)

// 2. 找出产品，解析各自的 i18n 注册表
const cwd = process.cwd()
const products = existsSync(resolve(cwd, 'package.json')) && existsSync(resolve(cwd, 'src/i18n.ts'))
  ? [cwd]
  : readdirSync(repo, { withFileTypes: true })
      .filter((e) => e.isDirectory() && e.name.startsWith('ops-'))
      .map((e) => resolve(repo, e.name, 'frontend'))
      .filter((p) => existsSync(resolve(p, 'src/i18n.ts')))

const get = (obj, path) => path.split('.').reduce((o, k) => (o == null ? o : o[k]), obj)

let failed = false
for (const prod of products) {
  const i18nPath = resolve(prod, 'src/i18n.ts')
  const src = readFileSync(i18nPath, 'utf8')

  // import 变量名 → 文件路径
  const byVar = {}
  for (const m of src.matchAll(/import\s+(\w+)\s+from\s+'([^']+\.json)'/g)) {
    const spec = m[2]
    const abs = spec.startsWith('.')
      ? resolve(dirname(i18nPath), spec)
      : resolve(repo, 'packages/i18n', spec.replace('@ops/i18n/', ''))
    if (existsSync(abs)) byVar[m[1]] = abs
  }

  // 语言标签块 `'zh-CN': { ns: 变量, ... }` 里的命名空间映射。
  //
  // ⚠️ 三个产品有三种写法，全都合法：
  //   另一个产品  const resources = {  每行一个命名空间
  //   某个同类产品           const resources = {  47 个全塞一行
  //   另一个产品          createI18n({ ... })  内联，压根没有 resources 变量
  // 所以判据只能锚在**语言标签**上 —— 那是 Resources 类型强制的形状，
  // 三种写法里都一样。
  //
  // ⚠️ 之前锚在 'const resources' 上，对 另一个产品 时 indexOf 返回 -1、
  // slice(-1) 悄悄退化成只取最后一个字符，于是「一个命名空间都没注册」，
  // 报出来的却是像模像样的「缺 4 条文案」。守卫自己错得像真的，最难查。
  const nsFiles = {}
  for (const blk of src.matchAll(/'[a-z]{2}-[A-Z]{2}'\s*:\s*\{([^}]*)\}/g)) {
    for (const m of blk[1].matchAll(/([a-z][\w-]*)\s*:\s*(\w+)/g)) {
      if (byVar[m[2]] && !nsFiles[m[1]]) nsFiles[m[1]] = byVar[m[2]]
    }
  }

  const missing = []
  for (const key of [...required].sort()) {
    const [ns, path] = key.split(':')
    const file = nsFiles[ns]
    if (!file) { missing.push(`${key}  (命名空间 ${ns} 未注册)`); continue }
    const val = get(JSON.parse(readFileSync(file, 'utf8')), path)
    if (typeof val !== 'string') missing.push(`${key}  (${file.replace(repo + '/', '')})`)
  }

  const name = prod.replace(repo + '/', '').replace('/frontend', '')
  if (missing.length) {
    failed = true
    console.error(`✗ check-shell-i18n: ${name} 缺 @ops/ui 需要的文案：`)
    for (const m of missing) console.error(`    ${m}`)
  } else {
    console.log(`✓ check-shell-i18n: ${name} 已提供 @ops/ui 需要的 ${required.size} 条文案`)
  }
}

if (failed) {
  console.error('\n组件取不到 key 时不报错，会把 key 本身当文案渲染 ——')
  console.error('界面上就会出现一个写着 "search.open" 的搜索框。')
  process.exit(1)
}
