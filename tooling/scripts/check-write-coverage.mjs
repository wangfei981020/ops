#!/usr/bin/env node
/**
 * 后端每个写接口（POST/PUT/DELETE）都必须有前端入口。
 *
 * # 为什么这条要守
 *
 * 后端写完、权限码配好、审计也登记了，**前端一处没调** —— 这种缺口
 * 从任何单侧看都是正常的：
 *
 *   看后端：路由在、测试过、perm.go 里有码 → 一切正常
 *   看前端：页面能开、没有报错、没有空白 → 一切正常
 *
 * 只有把两侧对起来才看得见。某个同类产品 的域名模块就是这么漏的：
 * 后端 25 个写接口，前端只接了 6 个 —— 新增/编辑/删除域名、
 * 整套 DNS 解析记录管理、单域名同步，全都够不着。
 *
 * ⚠️ 这不是"功能没做"，是"做完了没接线"。而没接线的那半在演示时
 * 完全看不出来 —— 页面是好的，只是那些事你做不了。
 *
 * # 判据
 *
 * 后端：扫 r.POST/PUT/DELETE("/x") 得到路由集合。
 * 前端：扫源码里出现的字面量路径，把 :param 段按位置通配后比对。
 *
 * 允许列表（ALLOW）里的是**确认不需要界面入口**的，每条必须写理由。
 */
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs'
import { join, relative } from 'node:path'
import { ROOT, products } from './lib/products.mjs'

/**
 * 确认不需要前端入口的写接口。加进来必须写清楚为什么。
 *
 * ⚠️ 别把"还没做"塞进这里 —— 那样这个守卫就退化成一张待办清单，
 * 而待办清单是不会让构建失败的。
 */
const ALLOW = new Map([
  ['POST /api/portal-auth', '运维平台调进来的免登录入口，不是界面动作'],
  ['POST /api/login', '登录页直接用 fetch，不走统一的 api 客户端'],
  ['POST /api/mcp', 'AI 客户端走 JSON-RPC 调，不是界面动作'],
  ['POST /api/oidc/token', 'OIDC 协议端点，由 RP 按协议调用'],
  // ⚠️ 这条的名字骗人：RevertAll **不回滚任何东西**，它返回一串候选 change_id
  // 外加一句「请逐条确认后回滚——批量回滚会跳过冲突检测，风险太高」。
  // 前端已经在按它建议的方式做（逐条 /api/audit-changes/:cid/revert，带冲突检测）。
  // 给它加一个「一键整条回滚」的按钮，等于把后端明确否决过的做法做出来。
  ['POST /api/v1/auth/login', '登录页直接用 fetch，不走统一的 api() 封装'],
  ['POST /api/v1/incidents/:p/ack', '认领已按需求下线：接口与权限码保留（是"先不做"不是"不做"），界面上无入口'],
  ['POST /api/audit-logs/:p/revert', '后端故意只返回候选而不执行；正道是逐条回滚，前端已接'],
])

function walk(dir, out = [], exts = /\.(ts|tsx|go)$/) {
  if (!existsSync(dir)) return out
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist') continue
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, out, exts)
    else if (exts.test(name)) out.push(p)
  }
  return out
}

/**
 * 把 /api/domains/:ciid/renew 归一成可比对的形状。
 *
 * ⚠️ 要剥掉 query string。前端写的是
 * `/api/gke/upgrade/baseline?cluster_id=${id}`，后端注册的是
 * `/gke/upgrade/baseline` —— 不剥的话这条永远对不上，
 * 于是一个**已经接好**的入口被报成"没接线"。
 */
function norm(p) {
  return (
    p
      .split('?')[0]
      .replace(/:[A-Za-z_]+/g, ':p')
      // ⚠️ 剥掉版本段，两侧才比得上。
      //
      // 后端这边同一个产品会解析出两种形状：直接注册在
      // `auth := r.Group("/api/v1", ...)` 上的路由带完整前缀（/api/v1/report），
      // 而经函数传进去的组（registerRules(g) 里的 g.POST("/rules")）
      // 解析不出前缀，只剩 /rules，被补成 /api/rules。
      // 前端封装 post('/report') 一律补成 /api/report ——
      // 于是**同一个产品里，一半路由能对上、另一半永远对不上**，
      // 而对不上的那一半会被报成"没接线"。我因此收到过 4 条假问题，
      // 而它们的入口就在页面上，点一下就能用。
      .replace(/^\/api\/v\d+(?=\/|$)/, '/api')
      .replace(/\/+$/, '')
  )
}

/**
 * 扫描范围。
 *
 * 不传参数 = 扫全部产品（CI 和手动跑都该是这个行为）。
 * 传产品名 = 只扫那几个，给**单产品构建**用。
 *
 * ⚠️ 原来这个脚本**根本不读 argv** —— 传了产品名照样全扫，
 * 于是把它挂进任何一个产品的 build，都会因为别的产品的历史欠账而失败。
 * 结果就是它一直没被挂进任何构建链，只能靠人想起来手动跑。
 *
 * ⚠️ 但默认绝不能变成只扫当前产品：一个只检查部分产品的守卫，
 * 它的绿色会被当成"全都查过了"。所以下面的输出里一定要打印范围。
 */
const scopes = process.argv.slice(2)
const ALL = products()
const TARGETS = scopes.length ? ALL.filter((p) => scopes.includes(p)) : ALL
if (scopes.length && TARGETS.length === 0) {
  console.error(`✗ check-write-coverage: 没有匹配的产品（传入 ${scopes.join(', ')}）`)
  process.exit(1)
}

/**
 * 挂账：已知没有前端入口，且**写明了为什么可以这样**。
 *
 * 🔴 这个守卫原来没有挂账机制 —— 于是真有正当理由的缺口只有两条路：
 * 改坏守卫，或者把接口删掉。两条都比"记一笔账"差。
 * （读覆盖守卫早就有这个机制，这里一直缺，是同一形状的东西没做齐。）
 *
 * ⚠️ 登记不是豁免，是记账。缺口修完必须把这一条删掉 ——
 * 留着等于把那个位置永久豁免了。
 */
const KNOWN = {
  // 目前一条都没有。⚠️ 加之前先想清楚：登记不是豁免，是记账。
}

const problems = []
let checked = 0
let waived = 0

for (const prod of TARGETS) {
  const beDir = join(ROOT, prod, 'backend')
  const feDir = join(ROOT, prod, 'frontend/src')
  if (!existsSync(beDir) || !existsSync(feDir)) continue

  // ── 后端写路由 ──
  const routes = new Map() // norm path → {method, raw, file}
  for (const f of walk(beDir)) {
    // ⚠️ 测试文件里注册的是假路由。`guard_test.go` 里一条
    // `r.POST("/api/hosts", h)` 让这个守卫报了很久的「POST /api/hosts 没有前端入口」——
    // 而那个接口**根本不存在**。守卫报假问题的代价是：人开始学着忽略它。
    if (f.endsWith('_test.go')) continue
    const src = readFileSync(f, 'utf8')
    // ⚠️ 路由组的变量名**不只叫 r**。另一个产品 用的是
    //     g.POST / auth.POST / authed.POST / public.POST
    // 而这里原本写死 `\br\.` —— 于是该产品一条写路由都没扫到，
    // routes.size === 0 走下面的 continue **静默跳过整个产品**，
    // 守卫还照常打印「均有前端入口」。导入功能没接线就是这么漏过去的。
    // 大小写敏感足以避开 net/http 的 http.Post（gin 的是全大写）。
    // 路由组的前缀**必须从源码解析**，不能假设。
    //
    // ⚠️ 原来这里写死 `/api`，而四个产品里只有 某个同类产品 是 /api，
    // 另外三个都是 /api/v1。于是 另一个产品 的 `api.POST("/tokens")`
    // 被算成 /api/tokens，而前端调的是 /api/v1/tokens ——
    // **23 个已经接好的入口被报成「没接线」**，包括我刚亲手实测创建过令牌的那个。
    // 一个 23/24 误报的守卫等于没有守卫：人只会把它从构建链里摘掉。
    const groups = {}
    for (const g of src.matchAll(/(\w+)\s*:?=\s*\w+\.Group\(\s*"([^"]+)"/g)) {
      groups[g[1]] = g[2].replace(/\/+$/, '')
    }
    for (const m of src.matchAll(/\b([A-Za-z_]\w*)\.(POST|PUT|DELETE)\(\s*"([^"]+)"/g)) {
      const [, varName, method, path] = m
      // 顶层 r.POST 注册的是绝对路径；组内注册的是相对路径，要拼前缀
      const prefix = groups[varName] ?? ''
      let raw = path.startsWith('/api') ? path : prefix + path
      if (!raw.startsWith('/api')) raw = `/api${raw}`
      routes.set(`${method} ${norm(raw)}`, { raw, file: relative(ROOT, f) })
    }
    // ⚠️ 第二种注册写法：Go 1.22 标准库 net/http 的 ServeMux，
    //     mux.HandleFunc("POST /api/orgs", h)
    // **方法在字符串里**，上面那条只认 `x.POST(` 的正则一条都匹配不到。
    // ops-version 全量用标准库，于是整个产品落进 routes.size===0 这一支 ——
    // 幸好有那个兜底，否则它会带着「均有前端入口」的绿字完全逃过本检查。
    // 这是本守卫第五次因为「只认一种写法」而漏报，每次都是同一个教训：
    // 扫源码的守卫必须穷举真实存在的写法，不能只认自己最先见到的那种。
    for (const m of src.matchAll(
      /\bHandleFunc\(\s*"(POST|PUT|DELETE|PATCH)\s+([^"]+)"/g,
    )) {
      const [, method, path] = m
      // 标准库的路径参数是 {id}，统一成 :id 交给 norm 处理
      const raw = path.replace(/\{(\w+)\}/g, ':$1')
      routes.set(`${method} ${norm(raw)}`, { raw, file: relative(ROOT, f) })
    }
  }
  // ⚠️ 一个产品**一条写路由都没有**是可疑的，不能当正常情况跳过。
  // 静默 continue 正是上面那次漏报的放大器：既扫不到、也不吭声。
  if (routes.size === 0) {
    problems.push(`${prod}: 后端没扫到任何写路由（POST/PUT/DELETE）。` +
      `要么确实没有，要么路由注册写法没被识别 —— 后者会让这个产品完全逃过本检查`)
    continue
  }
  checked++

  // ── 前端发起的写请求 ──
  //
  // ⚠️ 必须**带上方法**比对，不能只比路径。
  // 只比路径的话，`apiGet('/api/lifecycle-statuses')` 这一个读调用，
  // 会把同路径的 `POST /api/lifecycle-statuses` 也算成"有入口" ——
  // 而那个新增入口根本不存在。
  //
  // ⚠️ 但也不能只认 `apiAction(path, 'POST')` 这一种写法。真实代码里至少有四种：
  //
  //     apiAction('/api/x', 'POST', body)      // 显式
  //     apiAction('/api/x/1/test')             // 省略了方法，默认就是 POST
  //     fetch('/api/logout', { method: 'POST' })
  //     call('/api/license', { method: 'POST' })   // 页面自己的封装
  //
  // 只认第一种时，后三种全被报成"没有前端入口"—— 9 条假问题。
  // 守卫报假问题的代价不是烦人，是**人开始学着忽略它**，
  // 然后混在里面的真问题也一起被忽略了。
  //
  // 所以判据改成：找每个 /api/... 字面量，在它**后面一小段**里找方法标记；
  // 找不到标记时，看它前面挨着的是不是 apiAction（那就是默认的 POST）。
  const used = new Set()
  const LITERAL = /[`'"](\/api\/[^`'"]*)[`'"]/g
  // ⚠️ 第二种约定：产品自己的封装把前缀藏起来了。
  // 另一个产品 的 lib/api.ts 导出 post/put/del，页面写的是
  //     post('/rules', {...})       → 实际请求 /api/v1/rules
  // 前端字面量里根本没有 "/api"，所以上面那条 LITERAL 一条也匹配不到，
  // 结果 24 个**已经接好**的入口被报成"没接线"。
  // 守卫报假问题的代价不是烦人，是人开始学着忽略它 —— 然后
  // 混在里面的真问题（import 确实没接）也一起被忽略。
  const WRAPPED = /\b(post|put|del)\s*(?:<[^(]*>)?\(\s*[`'"](\/[^`'"]*)[`'"]/g
  const WRAP_VERB = { post: 'POST', put: 'PUT', del: 'DELETE' }
  // ⚠️ 第三种约定：**动词在函数名里**，路径带完整前缀。
  //     apiPost('/api/v1/tokens', body)
  //     apiDelete(`/api/v1/tokens/${id}`)
  // 上面那条 LITERAL 会匹配到路径，但它是往**后面**找方法标记的，
  // 而这里方法在前面（函数名里），于是一条都判不出来。
  // 另一个产品 全站用的就是这种写法。
  const VERB_IN_NAME = /\bapi(Post|Put|Delete|Patch)\s*(?:<[^(]*>)?\(\s*[`'"]([^`'"]+)[`'"]/g
  for (const f of walk(feDir, [], /\.(ts|tsx)$/)) {
    const src = readFileSync(f, 'utf8')
    for (const m of src.matchAll(LITERAL)) {
      const path = norm(m[1].replace(/\$\{[^}]*\}/g, ':p'))
      const after = src.slice(m.index + m[0].length, m.index + m[0].length + 160)
      // 窗口要够大：`apiAction<{ ok?: boolean; error?: string }>(` 光泛型就 30 多字符，
      // 还常常换行写。取窄了会把这些调用判成"没有方法标记"。
      const before = src.slice(Math.max(0, m.index - 160), m.index)
      // ⚠️ 第四种写法：路径和方法**都在三元里**，一次调用管新增和编辑两件事。
      //     api(isEdit ? `/api/orgs/${id}` : '/api/orgs', { method: isEdit ? 'PUT' : 'POST' })
      // 只取后面窗口里的**第一个**方法标记时，`/api/orgs` 会被配上 'PUT'，
      // 于是「POST /api/orgs 没有前端入口」——而新增表单就在那里，点一下就能用。
      // 静态分析判不出哪个分支配哪个路径，这里把窗口里出现的方法**全部**算上：
      // 多算一个前端入口不会造成漏报（本守卫只查「后端有、前端没有」），
      // 而少算一个就是假问题，假问题会让人学着忽略整个守卫。
      const verbs = [...after.matchAll(/['"](POST|PUT|DELETE)['"]/g)].map((v) => v[1])
      if (verbs.length) {
        for (const v of new Set(verbs)) used.add(`${v} ${path}`)
      } else if (/\bapiAction\s*(?:<[^(]*>)?\(\s*$/.test(before)) {
        // apiAction 的方法参数默认 POST（lib/fetchJson.ts）
        used.add(`POST ${path}`)
      }
    }
    for (const m of src.matchAll(VERB_IN_NAME)) {
      const path = norm(m[2].replace(/\$\{[^}]*\}/g, ':p'))
      used.add(`${m[1].toUpperCase()} ${path.startsWith('/api') ? path : `/api${path}`}`)
    }

    // 封装式调用：post('/rules') / del('/silences/:id')
    for (const m of src.matchAll(WRAPPED)) {
      const path = norm(m[2].replace(/\$\{[^}]*\}/g, ':p'))
      // 归一到与后端同一种形状：后端扫到的是去掉 group 前缀的 /rules，
      // 被上面统一补成 /api/rules，这里也补一次
      used.add(`${WRAP_VERB[m[1]]} ${path.startsWith('/api') ? path : '/api' + path}`)
    }
  }

  const missing = []
  for (const [key, info] of routes) {
    if (ALLOW.has(key)) continue
    const method = key.split(' ')[0]
    const sig = `${method} ${norm(info.raw)}`
    if (used.has(sig)) continue
    if (KNOWN[sig]) {
      waived++ // 已登记，见文件顶部的 KNOWN
      continue
    }
    missing.push({ key, ...info })
  }

  if (missing.length > 0) {
    const byModule = new Map()
    for (const m of missing) {
      const mod = m.raw.split('/')[2] ?? '?'
      byModule.set(mod, [...(byModule.get(mod) ?? []), m.key])
    }
    const lines = [...byModule.entries()]
      .sort((a, b) => b[1].length - a[1].length)
      .map(([mod, ks]) => `      ${mod} (${ks.length})\n${ks.map((k) => `        ${k}`).join('\n')}`)
    problems.push(
      `${prod}: ${missing.length}/${routes.size} 个写接口没有前端入口\n${lines.join('\n')}` +
        `\n    这不是"功能没做"，是"做完了没接线" —— 后端看正常、前端看也正常，` +
        `\n    只有把两侧对起来才看得见。确认不需要入口的请加进 ALLOW 并写理由。`,
    )
  }
}

if (problems.length > 0) {
  console.error('✗ 写接口覆盖检查未通过：\n')
  for (const m of problems) console.error(`  - ${m}\n`)
  process.exit(1)
}
// ⚠️ 挂账数必须报出来。只说「均有前端入口」而不提挂账的话，
//    这句话本身就是假的 —— 而一条假的绿色比红色更糟。
console.log(
  `✓ 写接口覆盖：${checked} 个产品，后端写接口均有前端入口` +
    (waived > 0 ? `（挂账 ${waived} 条）` : '') +
    `（范围 ${scopes.length ? `${TARGETS.length}/${ALL.length} 个产品：${TARGETS.join(', ')}` : '全部产品'}）`,
)
