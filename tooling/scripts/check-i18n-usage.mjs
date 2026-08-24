#!/usr/bin/env node
/**
 * 构建期防线：代码里用到的 i18n key 必须真的存在。
 *
 * # 为什么需要它
 *
 * i18next 取不到 key 时**不报错**，它把 key 本身当文案渲染出来。
 * 于是界面上出现一个写着 `write.create` 的按钮 ——
 * 功能完全正常，逻辑完全正确，只是那几个字是错的。
 *
 * 这属于全站在防的那一类问题：不报错、不白屏、监控看不见，
 * 只有人眼看到才发现。而错文案往往藏在弹窗、错误态这些平时走不到的路径里，
 * 能一路活到客户手里。实测就是靠截图才看见的（MCP 页的创建按钮）。
 *
 * check-i18n.mjs 管的是**两种语言之间**对不对齐，
 * 这个脚本管的是**代码与语言包之间**对不对得上，两者互补。
 *
 * # 为什么只扫字面量
 *
 * `t(\`users:roles.${code}\`)` 这种拼出来的 key 静态查不了，直接跳过。
 * 覆盖不到全部不等于没价值 —— 绝大多数 key 是写死的字面量。
 * 想让拼接的 key 也被覆盖，就得在运行期兜（另一码事）。
 *
 * # CLDR 复数
 *
 * `t('certs:total', {count})` 在语言包里存的是 `total_one` / `total_other`，
 * 没有 `total` 这个 key。不认这条规则的话，这个脚本会把几十个
 * 完全正确的复数 key 全报成缺失 —— 而一个天天误报的检查等于没有检查，
 * 它只会被人加个 --skip 绕过去。
 */
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { basename, join } from 'node:path'
import { frontendDirs } from './lib/products.mjs'

const ROOT = new URL('../..', import.meta.url).pathname
const LOCALE_DIR = join(ROOT, 'packages/i18n/locales/zh-CN')
// ⚠️ 产品清单**自动发现**，不要写死。
// 原来这里是硬编码的两个产品，旁边写着"新产品加进来时补一行"——
// 而 另一个产品 建出来之后没人补，于是它完全不受这个守卫约束，
// 守卫却照样打印「✓ 全部通过」。绿色被当成了"全查过了"。
/**
 * 扫描范围。
 *
 * 不传参数 = **扫全部产品**（CI 和手动跑都该是这个行为，不能变）。
 * 传产品名 = 只扫那几个产品，给**单产品构建**用。
 *
 * # ⚠️ 为什么需要收窄，以及为什么默认必须仍是全扫
 *
 * 这个守卫全仓扫，于是 A 产品进行中的半成品文案会挡住 B 产品发版 ——
 * 实测撞到两次（另一个产品 正在加页面，某个同类产品 因此构建不了）。
 * 而挡不住的时候人会去加 `|| true`，那比收窄糟得多。
 *
 * 但默认**绝不能**变成只扫当前产品：`lib/products.mjs` 的注释里已经
 * 记过一次教训 —— 三个守卫各写一份硬编码清单，另一个产品 建出来后
 * 一行都没补，结果那个产品完全不受约束，而守卫照样打印「✓ 全部通过」。
 * **一个只检查部分产品的守卫，它的绿色会被当成"全都查过了"。**
 *
 * 所以下面的输出里一定要打印范围。
 */
const scopes = process.argv.slice(2)
const ALL_DIRS = frontendDirs()
const SRC_DIRS = scopes.length
  ? ALL_DIRS.filter((d) => scopes.some((s) => d.startsWith(s)))
  : ALL_DIRS

// defaultNS 从 @ops/i18n 源码读，不硬编码 —— 那边改了这里必须跟着变，
// 而硬编码的一份不会报错，只会开始漏检。
const DEFAULT_NS =
  readFileSync(join(ROOT, 'packages/i18n/src/index.ts'), 'utf8').match(
    /defaultNS:\s*'([^']+)'/,
  )?.[1] ?? 'common'

const PLURAL_SUFFIXES = ['_zero', '_one', '_two', '_few', '_many', '_other']

const bundles = {}
for (const f of readdirSync(LOCALE_DIR)) {
  if (f.endsWith('.json')) {
    bundles[basename(f, '.json')] = JSON.parse(readFileSync(join(LOCALE_DIR, f), 'utf8'))
  }
}

/**
 * 产品内语言包。
 *
 * 语言包有两处合法位置，两种都在用：
 *   packages/i18n/locales/     共享包 —— 某个同类产品 / 另一个产品 / 另一个产品
 *   <产品>/frontend/src/locales 产品内 —— 另一个产品
 *
 * ⚠️ 原来这里只读共享包，于是产品内放语言包的产品会被整片误报成
 * 「命名空间不存在」。而误报的代价不是多看几行日志：一个天天报错的守卫
 * 会被从构建链里摘掉，然后它本来能抓的问题就再也没人抓了。
 * 本项目就因此有 11 个命名空间一直在这个守卫的视野之外。
 */
const localBundles = {}   // 产品名 → { ns: json }
function bundlesFor(dir) {
  const product = dir.split('/')[0]
  if (localBundles[product]) return localBundles[product]
  const localDir = join(ROOT, product, 'frontend/src/locales/zh-CN')
  const merged = { ...bundles }
  try {
    for (const f of readdirSync(localDir)) {
      if (f.endsWith('.json')) {
        merged[basename(f, '.json')] = JSON.parse(readFileSync(join(localDir, f), 'utf8'))
      }
    }
  } catch {
    // 没有产品内 locales —— 用共享包即可，不是错误
  }
  localBundles[product] = merged
  return merged
}

/** key 存在吗？认 CLDR 复数后缀。返回 null = 整个命名空间都不存在。 */
function lookup(ns, key, bag = bundles) {
  let node = bag[ns]
  if (node === undefined) return null
  const parts = key.split('.')
  for (const p of parts.slice(0, -1)) {
    if (typeof node !== 'object' || node === null || !(p in node)) return false
    node = node[p]
  }
  if (typeof node !== 'object' || node === null) return false
  const last = parts[parts.length - 1]
  return last in node || PLURAL_SUFFIXES.some((s) => `${last}${s}` in node)
}

function* walk(dir) {
  let entries
  try {
    entries = readdirSync(dir)
  } catch {
    return // 产品目录还不存在，跳过
  }
  for (const e of entries) {
    const p = join(dir, e)
    if (statSync(p).isDirectory()) yield* walk(p)
    else if (/\.tsx?$/.test(e)) yield p
  }
}

// t('ns:key') / t("ns:key") / t(`ns:key`) —— 严格式。
// 只有这一式会把「命名空间根本不存在」也报出来（写错 ns 是低级错误，值得单独提示）。
const CALL = /t\(\s*[`'"]([a-zA-Z0-9_]+):([a-zA-Z0-9_.$\\{}]+)[`'"]/g

// ⚠️ 任何位置出现的 'ns:key' 字面量，只要 ns 是真实存在的命名空间就校验。
//
// 严格式要求引号**紧跟** `t(`，于是漏掉了最常见的一种写法：
//
//     t(r.ignored ? 'hostrecords:cert.unignore' : 'hostrecords:cert.ignore')
//
// 两个 key 都扫不到。实测因此漏过一整组文案 —— 界面上出现了一个
// 写着「cert.ignore」的菜单项，而这个守卫**通过了**。
// 守卫放过了它本来就是来防的那件事。
//
// 也覆盖 key 存在变量里、放在数组/对象里的写法。
const ANY_KEY = /[`'"]([a-zA-Z0-9_]+):([a-zA-Z0-9_.$\\{}]+)[`'"]/g

/**
 * 不带命名空间前缀的调用，例如 t('common.retry') 或 t('retry')。
 *
 * ⚠️ 这类调用一直是这个守卫的盲区 —— 上面两式都要求有冒号。
 *
 * 而 defaultNS 是 'common'，所以 `t('common.retry')` 找的是
 * **common 命名空间里名为 `common.retry` 的 key**，不是 common 的 retry。
 * 两者只差一个字符，写错了看起来完全合理，i18next 也不吭声。
 * 本产品因此一次性写坏 15 处（9 个文件），界面上按钮直接显示
 * "common.action.cancel"，而这个守卫报「✓ 全部通过」。
 *
 * 多数坏在错误态/弹窗里，平时根本不显示 —— 靠肉眼过界面是抓不到的。
 */
const BARE = /\bt\(\s*'([a-z][a-zA-Z0-9_]*(?:\.[a-zA-Z0-9_]+)*)'\s*[,)]/g

const missing = new Map()
const unknownNs = new Map()
let checked = 0

for (const dir of SRC_DIRS) {
  const bag = bundlesFor(dir)
  for (const file of walk(join(ROOT, dir))) {
    const src = readFileSync(file, 'utf8')
    const rel = file.slice(ROOT.length)
    for (const [, key] of src.matchAll(BARE)) {
      checked++
      // defaultNS 下解析。lookup 返回 false = 命名空间在但 key 不在。
      if (lookup(DEFAULT_NS, key, bag) === false) {
        const label = `${DEFAULT_NS}(默认命名空间):${key}`
        if (!missing.has(label)) missing.set(label, new Set())
        missing.get(label).add(rel)
      }
    }
    for (const [, ns, key] of src.matchAll(CALL)) {
      if (key.includes('$')) continue // 模板拼出来的，静态查不了
      checked++
      const r = lookup(ns, key, bag)
      if (r === null) {
        if (!unknownNs.has(ns)) unknownNs.set(ns, new Set())
        unknownNs.get(ns).add(rel)
      } else if (r === false) {
        const id = `${ns}:${key}`
        if (!missing.has(id)) missing.set(id, new Set())
        missing.get(id).add(rel)
      }
    }
    // 宽松式：ns 不认识就跳过（那多半是普通字符串，比如 'http://…' 里的 http:），
    // 认识就必须能查到 key。
    // ⚠️ 先剥掉注释：注释里举例写的 `common:license.x` 不是真引用，
    // 报出来只会逼人去给一个不存在的 key 编一条文案。
    const code = src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/.*$/gm, '$1')
    for (const [, ns, key] of code.matchAll(ANY_KEY)) {
      if (key.includes('$')) continue
      if (lookup(ns, key, bag) !== false) continue
      checked++
      const id = `${ns}:${key}`
      if (!missing.has(id)) missing.set(id, new Set())
      missing.get(id).add(rel)
    }
  }
}

if (missing.size === 0 && unknownNs.size === 0) {
  // ⚠️ 范围必须打印出来。只扫了一个产品却只说「✓ 全部通过」，
  // 那句绿色会被当成"所有产品都查过了"
  console.log(
    `✓ ${checked} 处 i18n 引用在语言包里都有对应文案（范围 ${
      scopes.length ? `${SRC_DIRS.length}/${ALL_DIRS.length} 个前端：${scopes.join(', ')}` : '全部产品'
    }）`,
  )
  process.exit(0)
}

console.error('✗ 代码引用了语言包里没有的文案：\n')
for (const [id, files] of [...missing].sort()) {
  console.error(`  ${id}`)
  for (const f of files) console.error(`      ${f}`)
}
for (const [ns, files] of [...unknownNs].sort()) {
  console.error(`  命名空间 ${ns} 不存在（${LOCALE_DIR.slice(ROOT.length)}/${ns}.json）`)
  for (const f of files) console.error(`      ${f}`)
}
console.error(
  '\ni18next 取不到 key 时不报错，会把 key 本身当文案渲染出来 ——' +
    '\n界面上就会出现一个写着 "write.create" 的按钮，功能正常、只有那几个字是错的。' +
    '\n补上文案，或改用已有的 key。',
)
process.exit(1)
