#!/usr/bin/env node
/**
 * 守卫：MCP 工具名不能出现在给**人**看的文案里。
 *
 * # 为什么需要这个守卫
 *
 * 后端有些接口**同时**服务两个读者：网页界面和 AI（经 MCP）。
 * 于是「下一步该干什么」这句话很容易只写一份，而那一份通常是给 AI 写的：
 *
 *   集群健康页的「处置」列：
 *     list_pods 按 restarts 排序，再用 diagnose_pod 查根因
 *     list_workloads 看 replicas_ready/replicas_desired
 *     resource_waste 看实际用量，据此调 limit
 *
 * **13 条里有 9 条是这样。** 一个运维打开这一页，看到「用 list_pods」——
 * 他在网页里执行不了它，也不知道那是什么。更糟的是其中还混着一条
 * 真能直接跑的 kubectl 命令，说明作者在两种读者之间摇摆，两边都没伺候好。
 *
 * # 判据
 *
 * 工具名清单**从 mcp.go 自动提取**，不手写 —— 手写的清单加了新工具就会漏。
 * 然后在后端「给人看的字段」里找这些名字：
 *
 *   Action:  / Title: / Detail: / Note: / Hint: / Reason: / Msg:
 *   以及语言包（packages/i18n/locales/**）里的任何值
 *
 * `MCPHint:` / `mcp_hint` / `mcp_note` 字段是**豁免**的 —— 那些字段存在的意义就是装工具链。
 * 这也是修法：不要删掉工具提示，把它挪到给 AI 的字段里。
 *
 * # 显式豁免：`//ops:mcp-only`
 *
 * 有些输出**只有 AI 会读**（MCP 传输层自己拼的提示、没有界面入口的接口）。
 * 给那个函数的注释里加一行 `//ops:mcp-only`，本守卫就跳过它。
 *
 * ⚠️ 必须是**显式声明**，不能靠"前端调不调这个路由"去推断：
 *	实测那个推断在**插值路径**（`/api/cdn/accounts/${id}/verify`）上会误判成
 *	MCP-only，据此豁免等于悄悄放过真的泄漏。声明式的判据错了至少看得见。
 *
 * ⚠️ 只匹配**独立出现**的工具名（前后是非标识符字符）。
 * `list_pods` 出现在 `/api/list_pods` 这种路径里不算。
 *
 * 🔴 **拼接串要整段看**：Go 里长文案普遍写成
 *
 *	"note": "闲置 = 实付 − 已按 request 分摊…" +
 *		"…先用 resource_waste 校准 request，再缩节点。"
 *
 *	只抓第一段的话，工具名藏在第二段就完全扫不到 —— 实测漏掉一处
 *	（`/k8s/idle-cost` 是成本页在调的，运维在网页里执行不了 resource_waste）。
 */

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { ROOT } from './lib/products.mjs'

const scopes = process.argv.slice(2)

function walk(dir, out = []) {
  let entries
  try {
    entries = readdirSync(dir)
  } catch {
    return out
  }
  for (const e of entries) {
    if (e === 'node_modules' || e === 'dist' || e === '.git') continue
    const p = join(dir, e)
    if (statSync(p).isDirectory()) walk(p, out)
    else out.push(p)
  }
  return out
}

const files = walk(ROOT)
const inScope = (f) =>
  scopes.length === 0 || scopes.some((s) => relative(ROOT, f).startsWith(s))

// ── 1. 从 mcp.go 提取工具名 ────────────────────────────
// 工具定义形如：{"list_pods", "描述…", "/api/k8s/pods", …}
//
// ⚠️ **必须用第三个元素是 `/api/` 路径**作为特征，不能只看 `{"名字", "`。
//
//	mcp.go 里的参数定义长得几乎一样：
//	  []mcpParam{{"cluster_id", "integer", "集群ID", false}, {"limit", "integer", …}}
//	只按 `{"xxx", "` 抓的话，limit / status / host / severity 这些**参数名**
//	会被当成工具名，然后守卫报出一堆误报
//	（第一版实测报了 10 多条，全是假的：「内存 limit 不足」里的 limit）。
//
//	而误报是这类守卫唯一致命的失败模式 —— 报过一次假问题，
//	第二次就没人看了。本项目的 check-write-coverage 正因为只比路径不比方法
//	报出 9 条假问题，害我误判过两次。
const tools = new Set()
for (const f of files.filter((x) => x.endsWith('mcp.go'))) {
  const src = readFileSync(f, 'utf8')
  // 写法二：`"name": "list_orgs",` —— map[string]any 形式的工具定义。
  //
  // 🔴 只认写法一的后果实测过：在一个用写法二的产品上，
  //    提取到的工具名**一个都不是它的**，守卫却照样打印
  //    「✓ N 个 MCP 工具名都没出现在文案里（范围 该产品）」——
  //    范围写着它，检查的却是别人。绿色的谎，正是这个文件要防的东西。
  for (const m of src.matchAll(/"name":\s*"([a-z][a-z0-9_]{3,})"/g)) {
    if (m[1].includes('_')) tools.add(m[1])
  }
  for (const m of src.matchAll(/\{"([a-z][a-z0-9_]{3,})",\s*"[^"]*",\s*"\/api\//g)) {
    // ⚠️ 只收**带下划线**的工具名。
    //
    //	单个词的工具名（triage / diagnose）在自然语言里就是普通词：
    //	英文文案「…not useful for triage」用的是「分诊」这个词义，
    //	不是在教人调 triage 工具 —— 第二版实测就报了这一条假问题。
    //
    //	带下划线的名字（list_pods / resource_waste）不可能是自然语言，
    //	所以它们是唯一能可靠判定的部分。
    //	代价是漏掉单词型工具名，这个漏报是划算的：
    //	一个会误报的守卫，第二次运行就没人看了。
    if (m[1].includes('_')) tools.add(m[1])
  }
}

if (tools.size === 0) {
  console.error('✗ 没能从 mcp.go 提取到任何工具名 —— 这个守卫此刻等于没有，先修提取逻辑')
  process.exit(1)
}

// ── 2. 在给人看的文案里找它们 ──────────────────────────
/**
 * 后端里"给人看"的字段名。⚠️ MCPHint 刻意不在此列 —— 那是给 AI 的。
 *
 * 🔴 两种写法都要认：Go 结构体字段 `Hint:` 和 map 字面量 `"hint":`。
 *
 *	第一版只认结构体写法，于是 `gin.H{"hint": "用 pipeline + run 调 pipeline_log …"}`
 *	一路绿灯 —— 而流水线页三个接口**都是界面在调**，那句话就直接显示给运维看了。
 *	判据只认一种写法，等于只防住一半。
 */
const HUMAN_FIELDS =
  /(?:\b(Action|Title|Detail|Note|Hint|Reason|Msg|Summary|Label)|"(action|title|detail|note|hint|reason|msg|summary|label)")\s*:\s*("(?:[^"\\\\]|\\\\.)*"(?:\s*\+\s*(?:\n\s*)?"(?:[^"\\\\]|\\\\.)*")*)/g

/**
 * 这一处是不是在一次**日志**调用里。
 *
 * 🔴 日志不算：`logx.J("gke_upgrade", …, map[string]any{"note": "…请查 gke_upgrade_history"})`
 *	的读者就是运维和开发，工具名正是排障时最有用的线索。
 *
 * ⚠️ 把判据从结构体字段放宽到 map 字面量之后，日志里的 map 也被扫进来了 ——
 *	不排除的话这个守卫会引导人把日志里的工具名也删掉，
 *	结果是界面没变好、日志反而少了线索。误报的代价不是烦人，
 *	是人开始学着忽略它。
 */
function inLogCall(src, idx) {
  const before = src.slice(Math.max(0, idx - 400), idx)
  const at = Math.max(before.lastIndexOf('logx.'), before.lastIndexOf('log.Printf('))
  if (at < 0) return false
  return !before.slice(at).includes('c.JSON')
}

/**
 * 这一处是不是落在标了 `//ops:mcp-only` 的函数里。
 *
 * 判据：往前找最近的 `func ` 定义，看它**上方的注释块**里有没有那行标记。
 */
function inMCPOnlyFunc(src, idx) {
  const before = src.slice(0, idx)
  const at = before.lastIndexOf('\nfunc ')
  if (at < 0) return false
  // 函数定义上方连续的注释行
  const head = before.slice(0, at + 1)
  const lines = head.split('\n')
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = lines[i].trim()
    if (l === '') continue
    if (!l.startsWith('//')) break
    if (/^\/\/\s*ops:mcp-only\b/.test(l)) return true
  }
  return false
}

const hits = []

for (const f of files.filter((x) => x.endsWith('.go') && inScope(x))) {
  const src = readFileSync(f, 'utf8')
  for (const m of src.matchAll(HUMAN_FIELDS)) {
    if (inLogCall(src, m.index)) continue
    if (inMCPOnlyFunc(src, m.index)) continue
    const field = m[1] ?? m[2]
    const text = m[3]
    for (const tool of tools) {
      // 独立出现才算：路径里的 /api/list_pods 不是在教人用工具
      if (new RegExp(`(^|[^\\w/])${tool}([^\\w]|$)`).test(text)) {
        hits.push({
          file: relative(ROOT, f),
          line: src.slice(0, m.index).split('\n').length,
          field,
          tool,
          text: text.length > 70 ? `${text.slice(0, 70)}…` : text,
        })
        break
      }
    }
  }
}

/**
 * 语言包里**按 key 豁免**的字段。
 *
 * 🔴 豁免的判据是「这条文案的读者是不是集成方」，不是「它在哪个文件」。
 *
 *	MCP 令牌页的用法说明就是写给要接 MCP 的人看的 —— 不列工具名，
 *	对方还得自己去调 tools/list 才知道能干什么。这类文案里出现工具名
 *	是它的**职责**，不是泄漏。
 *
 * ⚠️ 但豁免只能精确到 key，不能整个文件跳过：
 *	同一份语言包里 99% 的文案仍然是给普通用户看的，
 *	整份放过等于把这道守卫对语言包关掉。
 */
const LOCALE_EXEMPT_KEYS = ['"usage"', '"mcpUsage"', '"toolList"']

// 语言包里也不该有：那是纯粹给人看的
for (const f of files.filter((x) => x.includes('/i18n/locales/') && x.endsWith('.json'))) {
  const src = readFileSync(f, 'utf8')
  for (const tool of tools) {
    const re = new RegExp(`(^|[^\\w/])${tool}([^\\w]|$)`)
    for (const [i, line] of src.split('\n').entries()) {
      if (LOCALE_EXEMPT_KEYS.some((k) => line.trimStart().startsWith(k))) continue
      if (re.test(line)) {
        hits.push({
          file: relative(ROOT, f),
          line: i + 1,
          field: 'locale',
          tool,
          text: line.trim().slice(0, 70),
        })
      }
    }
  }
}

if (hits.length > 0) {
  console.error('✗ MCP 工具名出现在给人看的文案里：\n')
  for (const h of hits) {
    console.error(`  ${h.tool}  （字段 ${h.field}）`)
    console.error(`      ${h.file}:${h.line}`)
    console.error(`      ${h.text}`)
  }
  console.error(`
这些是 MCP 工具名，只有 AI 能调。写在界面上的话，一个运维看到
「用 list_pods 按 restarts 排序」——他在网页里执行不了它，也不知道那是什么。
集群健康页 13 条处置建议里有 9 条是这样。

修法**不是删掉提示**，而是把它挪到给 AI 的字段：
  Action:  "点计数看是哪些 Pod 和各自的重启次数"      ← 给人：界面上真能做的下一步
  MCPHint: "list_pods 按 restarts 排序，再 diagnose_pod"  ← 给 AI
两个读者共用一句话，无论怎么写都有一方读不懂。`)
  process.exit(1)
}

console.log(
  `✓ ${tools.size} 个 MCP 工具名都没有出现在给人看的文案里（范围 ${
    scopes.length ? scopes.join(', ') : '全部产品'
  }）`,
)
