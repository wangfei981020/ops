#!/usr/bin/env node
/**
 * 语言包完整性检查。
 *
 * 拦的是这一类事故：加了个新功能，只写了 zh-CN 的文案，
 * 英文界面就零星漏出中文。这种漏不会报错、不会白屏，
 * 只有恰好切到英文的人才会撞见 —— 也就是客户先撞见。
 *
 * 以 zh-CN 为基准做**双向**比对：
 *   缺 key  → 英文界面漏中文
 *   多 key  → 中文那边删了文案却忘了删翻译，属于死代码，一样要清
 *
 * 用法：node tooling/scripts/check-i18n.mjs
 */

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '../..')
const LOCALES_DIR = join(ROOT, 'packages/i18n/locales')
const BASE_LOCALE = 'zh-CN'

/** 把嵌套对象拍平成 "a.b.c" 形式的 key 集合。 */
function flatten(obj, prefix = '', out = new Set()) {
  for (const [k, v] of Object.entries(obj)) {
    const key = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      flatten(v, key, out)
    } else {
      out.add(key)
    }
  }
  return out
}

/**
 * i18next 的复数后缀。
 *
 * 不同语言需要的复数形式数量**不一样**，这是本脚本最容易写错的地方：
 * 英文需要 one / other 两条，中文只需要 other 一条。
 * 若按字面 key 直接比对，正确的语言包反而会被判成「多余 key」。
 *
 * 正确做法是比对**基础 key**，再按各语言自己的 CLDR 类别校验形式是否齐全。
 */
const PLURAL_SUFFIXES = ['zero', 'one', 'two', 'few', 'many', 'other']

function splitPlural(key) {
  const i = key.lastIndexOf('_')
  if (i === -1) return { base: key, category: null }
  const suffix = key.slice(i + 1)
  return PLURAL_SUFFIXES.includes(suffix)
    ? { base: key.slice(0, i), category: suffix }
    : { base: key, category: null }
}

/** 某语言按 CLDR 需要哪些复数类别。 */
function requiredCategories(locale) {
  try {
    return new Set(new Intl.PluralRules(locale).resolvedOptions().pluralCategories)
  } catch {
    return new Set(['other'])
  }
}

/** 基础 key → 该语言实际提供的复数类别集合（非复数 key 的值为 null）。 */
function groupByBase(keys) {
  const map = new Map()
  for (const k of keys) {
    const { base, category } = splitPlural(k)
    if (!map.has(base)) map.set(base, category === null ? null : new Set())
    if (category !== null) {
      const cur = map.get(base)
      if (cur === null) map.set(base, new Set([category]))
      else cur.add(category)
    }
  }
  return map
}

/** 取出所有 {{var}} 占位符名。 */
function placeholders(value) {
  if (typeof value !== 'string') return new Set()
  return new Set([...value.matchAll(/\{\{\s*(\w+)\s*\}\}/g)].map((m) => m[1]))
}

function readNamespace(locale, ns) {
  return JSON.parse(readFileSync(join(LOCALES_DIR, locale, ns), 'utf8'))
}

function valueAt(obj, dotted) {
  return dotted.split('.').reduce((acc, k) => (acc == null ? undefined : acc[k]), obj)
}

const locales = readdirSync(LOCALES_DIR).filter((d) => statSync(join(LOCALES_DIR, d)).isDirectory())

if (!locales.includes(BASE_LOCALE)) {
  console.error(`✗ 基准语言 ${BASE_LOCALE} 不存在`)
  process.exit(1)
}

const others = locales.filter((l) => l !== BASE_LOCALE)
const baseFiles = readdirSync(join(LOCALES_DIR, BASE_LOCALE)).filter((f) => f.endsWith('.json'))
const problems = []

for (const locale of others) {
  const files = new Set(
    readdirSync(join(LOCALES_DIR, locale)).filter((f) => f.endsWith('.json')),
  )

  for (const ns of baseFiles) {
    if (!files.has(ns)) {
      problems.push(`${locale}: 缺整个命名空间 ${ns}`)
      continue
    }
    files.delete(ns)

    const base = readNamespace(BASE_LOCALE, ns)
    const target = readNamespace(locale, ns)
    const baseKeys = flatten(base)
    const targetKeys = flatten(target)
    const baseGroups = groupByBase(baseKeys)
    const targetGroups = groupByBase(targetKeys)
    const needed = requiredCategories(locale)

    for (const [baseKey, baseCats] of baseGroups) {
      if (!targetGroups.has(baseKey)) {
        problems.push(`${locale}/${ns}: 缺 key  ${baseKey}`)
        continue
      }
      const targetCats = targetGroups.get(baseKey)

      // 一边是复数 key、一边是普通 key，说明改了其中一个语言忘了改另一个
      if ((baseCats === null) !== (targetCats === null)) {
        problems.push(
          `${locale}/${ns}: ${baseKey} 复数形态不一致（基准${baseCats === null ? '非复数' : '复数'}、本语言${targetCats === null ? '非复数' : '复数'}）`,
        )
        continue
      }

      if (baseCats !== null && targetCats !== null) {
        // 按本语言自己的 CLDR 规则校验，而不是照抄基准语言的形式
        for (const cat of needed) {
          if (!targetCats.has(cat)) {
            problems.push(`${locale}/${ns}: ${baseKey} 缺复数形式 _${cat}（${locale} 必需）`)
          }
        }
        for (const cat of targetCats) {
          if (!needed.has(cat)) {
            problems.push(`${locale}/${ns}: ${baseKey} 多出复数形式 _${cat}（${locale} 用不到）`)
          }
        }
      }

      // 占位符对不上，运行时会渲染出字面的 {{count}} 给用户看。
      // 复数 key 拿任意一个已存在的形式来比即可。
      const bk = baseCats === null ? baseKey : `${baseKey}_${[...baseCats][0]}`
      const tk = targetCats === null ? baseKey : `${baseKey}_${[...targetCats][0]}`
      const bp = placeholders(valueAt(base, bk))
      const tp = placeholders(valueAt(target, tk))
      for (const p of bp) {
        if (!tp.has(p)) problems.push(`${locale}/${ns}: ${baseKey} 缺占位符 {{${p}}}`)
      }
      for (const p of tp) {
        if (!bp.has(p)) problems.push(`${locale}/${ns}: ${baseKey} 多出占位符 {{${p}}}`)
      }
    }
    for (const baseKey of targetGroups.keys()) {
      if (!baseGroups.has(baseKey)) {
        problems.push(`${locale}/${ns}: 多余 key  ${baseKey}（基准语言已无此项）`)
      }
    }
  }

  for (const extra of files) {
    problems.push(`${locale}: 多余的命名空间 ${extra}`)
  }
}

if (problems.length > 0) {
  console.error(`✗ 语言包不一致，共 ${problems.length} 处：\n`)
  for (const p of problems) console.error(`  ${p}`)
  console.error('\n补齐后再构建。漏翻不会报错，只会让客户看到半中半英的界面。')
  process.exit(1)
}

const total = baseFiles.reduce((n, ns) => n + flatten(readNamespace(BASE_LOCALE, ns)).size, 0)
console.log(`✓ 语言包一致：${locales.length} 种语言 × ${baseFiles.length} 个命名空间，${total} 个 key`)
