#!/usr/bin/env node
/**
 * 守卫：Go 结构体声明了字段，但 `rows.Scan(...)` 里**没有读它**。
 *
 * # 它抓的是哪一类缺陷
 *
 * ```go
 * type Org struct {
 *     DatasourceID   int64     // ← 字段声明了
 *     DSEndpoint     string
 * }
 *
 * rows.Scan(&in.ID, &in.Name, ...)   // ← 但 Scan 里没有它们
 * ```
 *
 * 结果：那些字段**永远是零值**，而依赖它们的逻辑（这里是 Conn() 的第三层）
 * 静默失效 —— 编译通过、测试可能也通过（如果测试是自己构造结构体的）。
 *
 * 🔴 **本仓已经撞过三次**（都是同一形态的不同位置）：
 *   1. `workload_include/exclude` —— 迁移/store/前端全改了，唯独 API 请求结构没加
 *   2. `enabled` —— DTO 有、orgReq 没有，提交被静默丢弃却返回 {"ok":true}
 *   3. `DatasourceID/DSEndpoint` —— 结构体加了，SQL 查询和 Scan 都没改
 *
 * 第 3 次是在写自动化测试时才发现的：手工 SQL 查库看着数据是对的，
 * 而代码里读出来是零值 —— **"库里有" ≠ "代码读得到"**。
 *
 * # 判据
 *
 * 对每个被 `rows.Scan(&x.A, &x.B, ...)` 填充的结构体变量，
 * 收集它在同一函数里被 Scan 的字段名；再把该结构体类型声明的字段拿来比。
 * 声明了但从没在**任何** Scan / 显式赋值里出现的，报出来。
 *
 * ⚠️ 判据必须宽容，否则误报会让人把守卫关掉：
 *   - 只查**同一文件内**定义的结构体（跨文件的引用关系太容易误判）
 *   - 字段在文件里以 `x.Field =` 形式被赋值过 → 认为已接线（很多字段是 Scan 后加工的）
 *   - 结构体上标了 `// scan:skip` 的整体跳过（纯 API/DTO 结构不该被这条管）
 */

import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { ROOT, products } from './lib/products.mjs'

function walk(dir, out = []) {
  if (!existsSync(dir)) return out
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'vendor') continue
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (name.endsWith('.go') && !name.endsWith('_test.go')) out.push(p)
  }
  return out
}

const scopes = process.argv.slice(2)
const ALL = products()
const TARGETS = scopes.length ? ALL.filter((p) => scopes.includes(p)) : ALL

const problems = []
let checked = 0

for (const prod of TARGETS) {
  // 同包（= 同目录）所有 .go 拼在一起，只用来判「字段有没有被填充」。
  //
  // 🔴 填充**经常发生在另一个文件**里：结构体定义在 org.go，
  //    而给它赋值的加载函数在同一个包的 scope.go —— 这是完全正常的 Go 组织方式。
  //    只看单文件会把它报成"永远是零值"，而这个字段明明填了。
  //
  // ⚠️ 只放宽"填充"这一侧的搜索范围，**结构体与 Scan 的配对仍限本文件** ——
  //    否则两个文件里各有一个同名类型时会互相顶掉，那才是真漏报。
  const pkgCache = new Map()
  const pkgSrcOf = (file) => {
    const dir = dirname(file)
    if (!pkgCache.has(dir)) {
      let all = ''
      for (const f of readdirSync(dir)) {
        if (f.endsWith('.go') && !f.endsWith('_test.go')) {
          all += readFileSync(join(dir, f), 'utf8') + '\n'
        }
      }
      pkgCache.set(dir, all)
    }
    return pkgCache.get(dir)
  }

  for (const file of walk(join(ROOT, prod, 'backend'))) {
    const src = readFileSync(file, 'utf8')
    if (!src.includes('rows.Scan(') && !src.includes('QueryRow')) continue

    // 本文件里定义的结构体 → 它声明的字段
    const structs = new Map()
    for (const m of src.matchAll(/type\s+(\w+)\s+struct\s*\{([\s\S]*?)\n\}/g)) {
      const [, name, body] = m
      if (/\/\/\s*scan:skip/.test(body)) continue
      const fields = []
      for (const line of body.split('\n')) {
        const f = line.match(/^\s{1,2}(\w+)\s+[\w\[\]\*\.]+/)
        if (f && /^[A-Z]/.test(f[1])) fields.push(f[1])
      }
      if (fields.length) structs.set(name, fields)
    }
    if (structs.size === 0) continue

    // 被 Scan 填充的变量 → 类型（靠 `var x TypeName` 或 `x := TypeName{}` 推断）
    for (const [typeName, fields] of structs) {
      const varRe = new RegExp(
        `(?:var\\s+(\\w+)\\s+${typeName}\\b|(\\w+)\\s*:=\\s*${typeName}\\{)`, 'g')
      const vars = new Set()
      for (const v of src.matchAll(varRe)) vars.add(v[1] || v[2])
      if (vars.size === 0) continue

      // 这个类型有没有真的出现在 Scan 里（没有就不是"读库结构体"，跳过）
      const scanned = new Set()
      let touched = false
      for (const v of vars) {
        for (const s of src.matchAll(/\.Scan\(([\s\S]*?)\)\s*;?\s*(?:err|$|\n)/g)) {
          const args = s[1]
          if (!args.includes(`&${v}.`)) continue
          touched = true
          for (const a of args.matchAll(new RegExp(`&${v}\\.(\\w+)`, 'g'))) scanned.add(a[1])
        }
      }
      if (!touched) continue
      checked++

      for (const f of fields) {
        if (scanned.has(f)) continue
        // Scan 之后被**以任何方式填充**的都算接线了。三种形态都要认，
        // 少认一种就是误报 —— 而误报会让人把整个守卫关掉，比漏报更糟。
        // 实测这三种在本仓都存在：
        //   ① 单独赋值      in.CredentialEnc = cred.String
        //   ② 多重赋值      d.InsecureTLS, d.Enabled = insecure == 1, enabled == 1
        //   ③ 取地址填充    json.Unmarshal([]byte(cj), &p.Columns)
        // ⚠️ 在**同包**范围内找填充，不只本文件 —— 见 pkgSrcOf 的说明
        const hay = pkgSrcOf(file)
        const filled =
          // ①②：同一行内，`=` 左边出现 .Field
          new RegExp(`^[^=\\n]*\\.${f}\\b[^=\\n]*=`, 'm').test(hay) ||
          // ③：被取地址传给别的函数（Unmarshal / Decode / Scan 之外的填充）
          new RegExp(`&\\w+\\.${f}\\b`).test(hay)
        if (filled) continue
        problems.push({
          file: relative(ROOT, file),
          why: `${typeName}.${f} 声明了，但既没在 rows.Scan 里读、也没被赋值 —— 它永远是零值`,
        })
      }
    }
  }
}

if (problems.length) {
  console.error('✗ check-go-scan-fields: 有字段声明了却从没被填充：\n')
  for (const p of problems) console.error(`    ${p.file}\n      ${p.why}`)
  console.error(`
这类字段编译通过、测试也可能通过（若测试自己构造结构体），
但运行期永远是零值 —— 依赖它的逻辑静默失效。

本仓已撞过三次：workload_include/exclude、enabled、DatasourceID。
第三次是写自动化测试才发现的：手工 SQL 查库数据是对的，代码读出来却是零值。
**"库里有" ≠ "代码读得到"。**

修法：把字段加进 SELECT 与 rows.Scan；确实不从库里来的，
在结构体里加一行 \`// scan:skip\` 说明理由。`)
  process.exit(1)
}
console.log(`✓ check-go-scan-fields: ${checked} 个读库结构体的字段都有填充` +
  `（范围 ${scopes.length ? scopes.join(', ') : '全部产品'}）`)
