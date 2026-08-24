#!/usr/bin/env node
/**
 * 前端菜单权限码 ↔ 后端权限表的契约检查。
 *
 * 前端 `nav.ts` 每个菜单项挂一个 `perm: 'menu:xxx'`，后端 `perm.go` 用同一批码
 * 做接口级校验。两边分处不同语言、不同目录 —— 只要有一处改名，就会漂。
 *
 * 而漂掉的表现是**静默的**：
 *   · 前端写了个后端不存在的码 → 该菜单对所有非管理员消失。
 *     管理员自己看不出问题（管理员全放行），报障的只有普通用户，
 *     而他会说"我这里少了个菜单"，没人会立刻想到是拼写。
 *   · 后端有码但前端没菜单 → 权限白配了，管理员在角色里勾了却没有任何效果。
 *
 * 两个方向都查。第二类只 WARN 不拦：页面还没做完时后端码先行是正常的。
 *
 * 用法：node tooling/scripts/check-perm-codes.mjs
 */

import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '../..')

const SOURCES = [
  {
    product: 'ops-version',
    backend: 'ops-version/backend/internal/auth/rbac.go',
    nav: 'ops-version/frontend/src/layouts/nav.ts',
  },
]

let failed = 0

for (const src of SOURCES) {
  const backendSrc = read(join(ROOT, src.backend))
  const navSrc = read(join(ROOT, src.nav))
  if (backendSrc === null || navSrc === null) continue

  // 后端认识的权限码。
  //
  // ⚠️ 两种形态都要认：
  //   menu:xxx                          —— 菜单前缀式
  //   PermXxx Perm = "org.write"        —— 扁平式（本产品用这种）
  // 只认一种的后果实测过：抽到 0 个，而守卫**照样报"共 N 处不一致"**，
  // 让人以为是权限码写错了，实际是提取正则不认这个产品的写法。
  const known = new Set([
    ...(backendSrc.match(/menu:[a-z0-9_]+/g) ?? []),
    ...[...backendSrc.matchAll(/Perm\w+\s+Perm\s*=\s*"([a-z][a-z0-9._]*)"/g)].map((m) => m[1]),
  ])
  // 前端用到的码。
  //
  // ⚠️ 不要写「key 后面紧跟 perm」这种依赖字段顺序的正则：
  // 格式化工具会重排字段，一重排就抽不全，而脚本照样打印 ✓ ——
  // 第一版就是这么漏掉 6 个菜单还报"全部有效"的。
  // 这里改成：先抽所有 perm，再按字符位置回溯最近的 key 只为了报错好读。
  const used = new Map() // code -> [菜单 key...]
  for (const m of navSrc.matchAll(/perm: '([^']*)'/g)) {
    push(used, m[1], nearestKeyBefore(navSrc, m.index))
  }

  if (known.size === 0) {
    console.error(`✗ ${src.product}: 后端 ${src.backend} 里一个权限码都没抽到（正则失效？）`)
    failed++
    continue
  }

  // 方向一：前端用了后端不认识的码 —— 拦
  const unknown = [...used.entries()].filter(([code]) => !known.has(code))
  for (const [code, keys] of unknown) {
    console.error(`✗ ${src.product}: 菜单 ${keys.join('/')} 的 perm '${code}' 后端不存在`)
    failed++
  }

  // 方向二：后端有码但前端没有对应菜单 —— 只提示
  const unused = [...known].filter((c) => !used.has(c)).sort()

  // 每个菜单项都必须有 perm。漏写在 TS 里是编译错误（perm 是必填），
  // 但如果有人写成 perm: '' 就绕过去了，这里补一刀
  const blank = [...used.keys()].filter((c) => c.trim() === '')
  if (blank.length > 0) {
    console.error(`✗ ${src.product}: 有菜单项的 perm 是空串`)
    failed++
  }

  // 🔴 这一刀是为了让"防线自己失效"变成显式失败，而不是一句 ✓。
  //
  // ⚠️ 但**不能断言"每个菜单项都有 perm"** —— perm 是可选的：
  //    不挂 perm 的菜单对所有登录用户可见，那是合法设计，不是漏配。
  //    （原来按 ops 系某个产品的写法断言 perm 数 == 菜单项数，
  //    换个产品就把"公开菜单"报成"漏配权限"。）
  //
  // 真正要防的是**正则失效**：nav.ts 明明有菜单项，却一个都没抽到。
  // 菜单项的特征是 `key: 'xxx'`（路由在 router 里映射）或 `path: 'xxx'`，两种都认。
  const itemCount =
    (navSrc.match(/key: '[^']*'/g) ?? []).length + (navSrc.match(/path: '[^']*'/g) ?? []).length
  if (itemCount === 0) {
    console.error(
      `✗ ${src.product}: nav.ts 里一个菜单项都没抽到 —— 写法变了，本脚本的正则该跟着改`,
    )
    failed++
  }

  console.log(
    `${unknown.length === 0 && blank.length === 0 ? '✓' : '✗'} ${src.product}: ` +
      `${itemCount} 个菜单项 / ${used.size} 个权限码，后端共 ${known.size} 个`,
  )
  // 故意不做独立菜单的权限码。
  //
  // ⚠️ 这不是"忽略清单"，是**登记清单**：每一条都要写清为什么。
  // 不登记的话，这条提示会一直挂着，而一条永远存在的提示等于没有提示 ——
  // 下一个人看到它只会想"一直都这样"，于是真的漏配也被一起无视了。
  const intentionallyNoMenu = {
    'menu:cmdb_task_runs':
      '执行记录已并进「定时任务」页（一条任务和它的执行历史在同一页），不再单独占一个菜单。' +
      '权限码仍然生效：没有它的人在那一页看不到「执行记录」按钮。',
  }
  const reallyUnused = unused.filter((c) => !intentionallyNoMenu[c])
  const declared = unused.filter((c) => intentionallyNoMenu[c])
  if (declared.length > 0) {
    console.log(`  故意不做独立菜单（${declared.length} 个）：`)
    for (const c of declared) console.log(`    ${c} —— ${intentionallyNoMenu[c]}`)
  }
  if (reallyUnused.length > 0) {
    console.log(`  尚未接入的后端权限码（${reallyUnused.length} 个）：${reallyUnused.join(' ')}`)
  }
}

// ── 按钮权限码：菜单之外，页面里的 WriteButton 也要核 ──
//
// # 为什么必须单独扫一遍
//
// 原来这个脚本只查 nav.ts 的菜单权限码，而**按钮权限码同样会写错**，
// 后果还更隐蔽：写错的菜单至少管理员能看出少了一项，而写错的按钮
// 对管理员完全正常（不受限身份一律放行），只对普通角色永久置灰。
//
// 实测一次写错四个：`cmdb:manage_cost`（实际 manage_cost_rates）、
// `cmdb:manage_version_upgrade`（实际 manage_upgrade）、
// `cmdb:renew_domain`（根本不存在）、CDN 账号写用了 manage_cdn（实际 sync_cdn）。
// 全都能编译、能渲染、管理员点着也正常。
{
  const backendCodes = new Set()
  for (const src of SOURCES) {
    const go = read(join(ROOT, src.backend))
    if (!go) continue
    for (const m of go.matchAll(/"(cmdb:[a-z_]+)"/g)) backendCodes.add(m[1])
  }

  const usedInButtons = new Map() // code -> [文件]
  const walk = (dir) => {
    let entries
    try {
      entries = readdirSync(dir, { withFileTypes: true })
    } catch {
      return
    }
    for (const e of entries) {
      const full = `${dir}/${e.name}`
      if (e.isDirectory()) walk(full)
      else if (/\.tsx?$/.test(e.name)) {
        const src = readFileSync(full, 'utf8')
        // perm="cmdb:x" / perm={PERM} 的常量定义 / perm={'cmdb:x'}
        for (const m of src.matchAll(/(?:perm=\{?['"]|const \w*PERM\w* = ')(cmdb:[a-z_]+)/g)) {
          push(usedInButtons, m[1], full)
        }
      }
    }
  }
  for (const src of SOURCES) walk(`${ROOT}/${src.product}/frontend/src/routes`)

  const badButtons = [...usedInButtons.keys()].filter((c) => !backendCodes.has(c))
  if (badButtons.length > 0) {
    console.error(`\n✗ 页面里用了后端不存在的按钮权限码（${badButtons.length} 个）：`)
    for (const c of badButtons) {
      console.error(`  ${c}`)
      for (const f of usedInButtons.get(c)) console.error(`      ${f.replace(ROOT + '/', '')}`)
    }
    console.error(
      `\n⚠️ 这类错误不会报错、不会白屏：不受限管理员一律放行，所以自测完全正常，` +
        `\n   而普通角色看到的是一个永远置灰的按钮，且提示里写着一个不存在的权限码。`,
    )
    failed++
  } else {
    console.log(`  按钮权限码 ${usedInButtons.size} 个，全部与后端对得上`)
  }
}

if (failed > 0) {
  console.error(`\n共 ${failed} 处不一致。前端权限码必须与后端 perm.go 同名。`)
  process.exit(1)
}

/** 回溯 pos 之前最近的 `key: 'xxx'`，只用于让报错信息指得出是哪个菜单。 */
function nearestKeyBefore(src, pos) {
  let found = '?'
  for (const m of src.slice(0, pos).matchAll(/key: '([\w-]+)'/g)) found = m[1]
  return found
}

/**
 * 通用检查：前端用到的权限码，后端必须真的发得出来。
 *
 * 🔴 上面那部分是 某个同类产品 专用的（码格式写死成 `cmdb:xxx`、SOURCES 只有一个产品），
 * 于是其余产品的权限码**从来没有被检查过**。
 * ops-version 因此漏过一个真 bug：术语从「实例」改成「组织」时后端权限码
 * 从 `inst.write` 改成了 `org.write`，前端的 `can(session, 'inst.write')` 没跟着改 ——
 * 结果组织页的所有写按钮对**所有人**（包括超管）永久隐藏，
 * 不报错、不告警，只是按钮不见了。
 *
 * 这一段不认任何码格式，只做集合比对：
 *   前端 can(session, 'X') 里的 X  ⊆  后端 Perm 常量的取值
 * 反向不查 —— 后端有而前端没用到是正常的（比如只给 MCP 用的权限）。
 */
function checkGenericPerms() {
  const products = readdirSync(ROOT, { withFileTypes: true })
    .filter((d) => d.isDirectory() && d.name.startsWith('ops-') && d.name !== 'ops-kit')
    .map((d) => d.name)

  for (const prod of products) {
    const rbac = join(ROOT, prod, 'backend/internal/auth/rbac.go')
    const feDir = join(ROOT, prod, 'frontend/src')
    if (!existsSync(rbac) || !existsSync(feDir)) continue

    // 后端：Perm 常量的**取值**（不是常量名）
    const backend = new Set()
    for (const m of read(rbac).matchAll(/Perm\s*=\s*"([^"]+)"/g)) backend.add(m[1])
    if (backend.size === 0) continue

    // 前端：can(session, 'X') / can(x, 'X') 里的字面量
    const used = new Map()
    const walkFe = (dir) => {
      for (const name of readdirSync(dir)) {
        const p = join(dir, name)
        if (statSync(p).isDirectory()) {
          walkFe(p)
          continue
        }
        if (!/\.tsx?$/.test(name)) continue
        const src = readFileSync(p, 'utf8')
        for (const m of src.matchAll(/\bcan\(\s*[\w.]+\s*,\s*'([^']+)'/g)) {
          push(used, m[1], p.replace(ROOT + '/', ''))
        }
      }
    }
    walkFe(feDir)

    const bad = [...used.keys()].filter((c) => !backend.has(c))
    if (bad.length) {
      failed++
      console.error(`\n✗ ${prod}: 前端用了后端不存在的权限码 —— 这些判断**恒为 false**，`)
      console.error('  表现是按钮对所有人（包括超管）永久隐藏，不报错也不告警：')
      for (const c of bad) {
        console.error(`    ${c}`)
        for (const f of used.get(c)) console.error(`      ${f}`)
      }
      console.error(`  后端实际有的码：${[...backend].sort().join(' / ')}`)
    } else if (used.size > 0) {
      console.log(`✓ ${prod}: ${used.size} 个前端权限码与后端 ${backend.size} 个全部对得上`)
    }
  }
}

checkGenericPerms()

function push(map, k, v) {
  const cur = map.get(k)
  if (cur) {
    if (!cur.includes(v)) cur.push(v)
  } else {
    map.set(k, [v])
  }
}

function read(p) {
  try {
    return readFileSync(p, 'utf8')
  } catch {
    console.error(`✗ 读不到 ${p}`)
    failed++
    return null
  }
}
