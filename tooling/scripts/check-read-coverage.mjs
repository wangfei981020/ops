#!/usr/bin/env node
/**
 * 守卫：后端的 GET 路由必须有前端入口，否则登记进下面的挂账表。
 *
 * # 为什么
 *
 * 「后端有能力、前端没入口」是本项目撞得最多的一类缺陷 ——
 * 一轮验收里出现 **18 次**，的两个弹窗、
 * 的 35 条路由，都是同一件事。
 *
 * 它的共同特征是**不报错**：后端跑得好好的，前端页面也正常渲染，
 * 少的只是一个入口，而没人会去数入口。
 *
 * ⚠️ 与 check-write-coverage 的分工：那个管写操作（POST/PUT/DELETE），
 * 这个管读操作（GET）。分开是因为两者的豁免理由完全不同 ——
 * 写操作没入口基本都是缺陷，读操作则有大量**合法的** MCP 专用接口。
 *
 * # 🔴 这个守卫最难的部分是「前端到底调没调」
 *
 * 判据写窄了会报一堆假问题，而**守卫报假问题的代价不是烦人，
 * 是人开始学着忽略它** —— 然后混在里面的真问题也一起被忽略。
 * 本仓库已经在这件事上栽过五次，五种写法都在下面 CALL_PATTERNS 里登记着。
 */

import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { ROOT, products } from './lib/products.mjs'

/** 与 check-write-coverage 同一份实现：跳过 node_modules / dist / vendor */
function walk(dir, out = [], exts = /\.(ts|tsx|go)$/) {
  if (!existsSync(dir)) return out
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name === 'vendor') continue
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, out, exts)
    else if (exts.test(name)) out.push(p)
  }
  return out
}

/**
 * 把路径归一成可比对的形状。
 *
 * ⚠️ `${...}` 要**替换**成 `:p`，不能截断。
 *
 *	我第一版写的是"从第一个 $ 处切断"，于是
 *	  `/api/audit-logs/${logID}/changes`  →  `/api/audit-logs/`
 *	  `/api/certs/${c.ciId}/download`     →  `/api/certs/`
 *	两个**已经接好**的入口被报成缺口。截断丢的是路径后半段，
 *	而后半段恰恰是区分不同接口的那部分。
 */
function norm(p) {
  return p
    .replace(/\$\{[^}]*\}/g, ':p')
    .replace(/\{[A-Za-z_]+\}/g, ':p')
    .replace(/:[A-Za-z_]+/g, ':p')
    .split('?')[0]
    .replace(/\/+$/, '')
}

/**
 * 前端调用的五种写法。**每一种都是踩过坑之后补进来的。**
 *
 * 1. 裸字面量        apiGet('/api/records')
 * 2. 生成的类型化客户端  api.GET('/k8s/node-list')   ← ⚠️ **不带 /api 前缀**
 * 3. 模板串          `/api/certs/${id}/download`
 * 4. 嵌套模板串       `/api/records${q ? `?x=${q}` : ''}`   ← 内层反引号会切断匹配
 * 5. 非 fetch 的跳转  window.location.href = `/api/certs/${id}/download`
 *
 * 🔴 第 2 种是最容易漏的：生成客户端把 `/api` 前缀藏在了配置里，
 * 前端源码里根本搜不到 `/api/k8s/node-list` 这个串。
 * 只认第 1 种时，**46 条已接好的路由**被报成缺口。
 */
function collectCalled(feDir) {
  const called = new Set()

  for (const f of walk(feDir, [], /\.(ts|tsx)$/)) {
    const src = readFileSync(f, 'utf8')

    // ①③⑤ 带 /api 前缀的字面量与模板串（要求闭合引号 → 拿到完整路径）
    for (const m of src.matchAll(/[`'"](\/api\/[^`'"]*)[`'"]/g)) {
      called.add(norm(m[1]))
    }
    // ④ 不要求闭合引号：嵌套模板串（`/api/records${q ? `?x=1` : ''}`）
    //	内层反引号会切断上面那条匹配。
    //
    //	⚠️ 但截出来的**就是完整路径** —— 被切掉的是 query string，
    //	而 query string 本来就要剥。所以直接进 called，不做前缀匹配。
    //
    //	🔴 我第一版在这里做了前缀匹配（"path 以某个 prefix 开头就算接了"），
    //	结果 `/api/certs` 认领了 `/api/certs/:p/bundle` ——
    //	把**没接的判成接了**。守卫往这个方向错，比误报危险得多：
    //	误报会被人骂，漏报没有人知道。
    for (const m of src.matchAll(/[`'"](\/api\/[A-Za-z0-9_\-/]+)/g)) {
      called.add(norm(m[1]))
    }
    // ② 生成的类型化客户端：api.GET('/k8s/node-list') —— 自己补 /api
    for (const m of src.matchAll(
      /\bapi\.(?:GET|POST|PUT|DELETE|PATCH)\(\s*[`'"]([^`'"]+)[`'"]/g,
    )) {
      called.add(norm('/api' + m[1]))
    }
  }
  return called
}

/** 后端注册的 GET 路由 */
function collectRoutes(beDir) {
  const routes = new Map() // norm → 原始路径
  for (const f of walk(beDir, [], /\.go$/)) {
    if (f.endsWith('_test.go') || f.includes('/vendor/')) continue
    const src = readFileSync(f, 'utf8')
    for (const m of src.matchAll(/\.GET\(\s*"([^"]+)"/g)) {
      const raw = m[1].startsWith('/api') ? m[1] : '/api' + m[1]
      routes.set(norm(raw), { raw, file: relative(ROOT, f) })
    }
    // ⚠️ 第二种注册写法：Go 1.22 标准库 net/http 的 ServeMux，
    //     mux.HandleFunc("GET /api/changes", h)
    // **方法在字符串里**，上面只认 `.GET(` 的正则一条都匹配不到。
    // ops-version 全量用标准库 —— 于是它一条 GET 路由都没被扫到，
    // 而本守卫（不像 check-write-coverage）此前**连兜底都没有**，
    // 整个产品静默通过，还照常打印绿字。
    // 「静默跳过」比误报危险得多：误报会被人骂，漏报没有人知道。
    for (const m of src.matchAll(/\bHandleFunc\(\s*"GET\s+([^"]+)"/g)) {
      const raw = m[1].replace(/\{(\w+)\}/g, ':$1')
      // 只收 /api 路径，与前端调用侧的口径一致。
      // /healthz 这类探针路径本来就不该有前端入口，收进来只会产生
      // 一条永远修不掉的假问题 —— 而假问题会让人学着忽略整个守卫。
      if (!raw.startsWith('/api/')) continue
      routes.set(norm(raw), { raw, file: relative(ROOT, f) })
    }
  }
  return routes
}

/**
 * 挂账：已知没有前端入口，且**写明了为什么可以这样**。
 *
 * 四类合法理由：
 *   alias    被重构后的 *-list 接口取代，保留只是为了不破坏 MCP
 *   mcp      故意只给 AI 用（诊断类），界面上有对应的其它入口
 *   redirect 浏览器**重定向**端点（OAuth 回调之类），前端本就不该 fetch 它
 *   gap      真缺口，已建档，等排期
 *
 * 🔴 `gap` 这一档修完必须删 —— 一条修完却留在挂账里的记录，
 * 等于把那个位置永久豁免了。
 */
const KNOWN = {
  // ── alias：被 *-list 取代，保留给 MCP ──
  '/api/k8s/nodes': 'alias → /api/k8s/node-list',
  '/api/k8s/pods': 'alias → /api/k8s/pod-list',
  '/api/k8s/workloads': 'alias → /api/k8s/workload-list',
  '/api/k8s/namespaces': 'alias → /api/k8s/namespace-list',
  '/api/k8s/services': 'alias → /api/k8s/service-list',
  '/api/k8s/pvcs': 'alias → /api/k8s/pvc-list',
  '/api/k8s/events': 'alias → /api/k8s/event-list',
  '/api/cloud-loadbalancers': 'alias → /api/cloud-lb-list',
  '/api/cloud-subnets': 'alias → /api/cloud-subnet-list',
  '/api/k8s/expose-surface': 'alias → /api/exposure-list',
  '/api/k8s/cost/overview': 'alias → /api/cost/overview',

  // ── redirect：浏览器重定向端点，前端不该 fetch ──
  // OIDC 回调是 IdP 跳回来的，浏览器直接落到这个地址；
  // 前端去 fetch 它反而是错的（会拿不到 cookie、也走不了重定向）。
  '/api/auth/oidc/callback': 'redirect：IdP 授权后浏览器跳回来的地址，不存在"前端调用"这回事',

  // ── mcp：故意只给 AI，界面上走别的入口 ──
  '/api/triage': 'mcp 分诊入口；界面对应「全局态势」的关注项',
  '/api/diagnose-cost': 'mcp 诊断；界面走成本页各自的下钻',
  '/api/diagnose-domain': 'mcp 诊断；界面走域名页的健康列',
  '/api/k8s/diagnose-cluster': 'mcp 诊断；界面走集群健康页',
  '/api/k8s/diagnose-sweep':
    'mcp 自检：把整个集群的异常 Pod 跑一遍规则，报「几个判出了具体根因、几个只给了泛化结论」。' +
    '⚠️ **故意不做界面入口**：它一次要打 40 次 APIServer 并逐个读日志（要经 kubelet 代理），' +
    '做成界面上随手能点的按钮，等于给人一个能把 APIServer 打满的红按钮。' +
    '它的用途是"衡量诊断覆盖到什么程度"，属于工程自检而不是日常排障动作',
  '/api/auth/sso/callback': 'OIDC 回调，由浏览器跳转触发，不是前端主动调',
  '/api/audit-logs/:p':
    '冗余：它返回的是「列表行数据 + changes」，而前端已从列表拿到行数据、' +
    '另外调 /changes。**不是缺口**，是一个没有消费方的便利接口',

  // ── gap：真缺口，已建档 ，等排期 ──
  '/api/obs/kubesphere':
    'mcp 兜底：原始 kapis 透传，工具说明里就写着"兜底用，查流水线优先用 ' +
    'pipeline_runs/pipeline_log"。界面上的具体用途已由「构建流水线」页覆盖。' +
    '⚠️ 不给它做界面入口是有意的：让人手输任意 kapis 路径去打一个特权端点是个陷阱',
  '/api/certs/:p/bundle':
    'machine-only：**禁止做前端入口**。它是给目标机自助拉证书的（A+ 拉取式部署），' +
    '走 deploy_token 自鉴权、注册在登录中间件之前、独立限流 30 次/分钟/IP。' +
    '给人用的那条是 /api/certs/:id/download（走 JWT + 审计 + 打成 zip），**已经接了**。' +
    '⚠️ 照"路由没有前端入口"的字面判据去接它，等于给一个机器端点做人界面，' +
    '并绕过它自己的鉴权设计',
  '/api/cloud-addresses':
    '冗余：它是 /api/cloud-ips（聚合视图）的**子集** —— 后者已包含全部静态 IP，' +
    '还多算了 idle（预留但没绑=在白花钱），IP 地址页已接全（筛选/排序/徽章）。' +
    '**不是缺口**，建一个重复页面反而更糟',
  '/api/cloud-projects/:p/sync-status':
    '冗余：账号级 /api/cloud-accounts/:id/sync-status **已返回逐项目明细**' +
    '（projects[].running），云账号页也在用它做逐项目的按钮态。' +
    '为每个项目单开一个轮询是 N 个请求换 1 个请求，**更差**',
}

const scopes = process.argv.slice(2)
const ALL = products()
const TARGETS = scopes.length ? ALL.filter((p) => scopes.includes(p)) : ALL
if (scopes.length && TARGETS.length === 0) {
  console.error(`✗ check-read-coverage: 没有匹配的产品（传入 ${scopes.join(', ')}）`)
  process.exit(1)
}

const fresh = [] // 没登记的缺口 → 拦
const stale = [] // 登记了但其实已经接上了 → 提醒删挂账
let known = 0
let checked = 0

for (const prod of TARGETS) {
  const beDir = join(ROOT, prod, 'backend')
  const feDir = join(ROOT, prod, 'frontend/src')
  if (!existsSync(beDir) || !existsSync(feDir)) continue

  const routes = collectRoutes(beDir)
  const called = collectCalled(feDir)
  // ⚠️ 一个产品**一条读路由都没扫到**是可疑的，不能当正常情况跳过。
  // check-write-coverage 早就有这个兜底，本守卫一直没有 ——
  // 于是 ops-version（标准库路由）整个产品静默通过。
  // 扫源码的守卫必须能说出「我什么都没看到」，否则它的绿字毫无意义。
  if (routes.size === 0) {
    problems.push(
      `${prod}: 后端没扫到任何读路由（GET）。` +
        `要么确实没有，要么路由注册写法没被识别 —— 后者会让这个产品完全逃过本检查`,
    )
    continue
  }
  checked += routes.size

  for (const [path, info] of routes) {
    if (called.has(path)) {
      if (KNOWN[path]?.startsWith('gap')) stale.push({ path, why: KNOWN[path] })
      continue
    }
    if (KNOWN[path]) {
      known++
      continue
    }
    fresh.push({ path, ...info, prod })
  }
}

if (stale.length > 0) {
  console.log('ℹ️  这些挂账已经接上了，请从 KNOWN 里删掉（留着等于永久豁免这个位置）：')
  for (const s of stale) console.log(`    ${s.path}  —— ${s.why}`)
}

if (fresh.length > 0) {
  console.error('✗ check-read-coverage: 后端 GET 路由没有前端入口，且没有登记：\n')
  for (const f of fresh) {
    console.error(`    ${f.path}`)
    console.error(`      注册于 ${f.file}`)
  }
  console.error(`
「后端有能力、前端没入口」是本项目撞得最多的一类缺陷（031 一轮里 18 次）。
它不报错：后端好好的、页面也正常渲染，少的只是一个入口，而没人会去数入口。

两条出路，**都要动手，不能放着**：
  · 补上前端入口
  · 确认它是别名 / MCP 专用 / 已建档的缺口 → 登记进本文件的 KNOWN，并写明理由

⚠️ 登记不是豁免，是记账。gap 那一档修完必须删掉。`)
  process.exit(1)
}

console.log(
  `✓ check-read-coverage: ${checked} 条 GET 路由都有前端入口或已登记` +
    `（挂账 ${known} 条，范围 ${scopes.length ? scopes.join(', ') : '全部产品'}）`,
)
