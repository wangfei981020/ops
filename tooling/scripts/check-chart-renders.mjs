#!/usr/bin/env node
/**
 * 每个 chart 的每个环境都要能渲染出**结构合法**的多文档 YAML。
 *
 * # 为什么不能只看 helm 有没有报错
 *
 * 踩过一次：`_hpa.tpl` 被 `{{- include }}` 调用，前导的 `-` 把上一个文档
 * 末尾的换行吃掉了，于是本模板开头的 `---` 粘到了上一行尾部：
 *
 *     app.kubernetes.io/component: backend---
 *     apiVersion: autoscaling/v2
 *
 * 两个对象合成一个非法文档。后果极其隐蔽：
 *
 *   - helm template **不报错**
 *   - `helm get manifest` 里两个对象都在
 *   - `grep -c "kind:"` 数出来还是对的（两行 kind 都还在）
 *
 * 只有真去 `kubectl get` 才发现集群里**一个 PDB 都没有** ——
 * 而 PDB 缺失的症状要等到节点排水时才出现（多个副本被同时驱逐）。
 *
 * 所以这里数「文档分隔符切出来的块里，有几个带 kind 的」，
 * 与「文本里出现了几次 kind:」对比。两者不一致 = 有文档被粘在一起了。
 */
import { execFileSync } from 'node:child_process'
import { existsSync, readdirSync, writeFileSync, unlinkSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { ROOT, products } from './lib/products.mjs'

const problems = []
let checked = 0

for (const p of products()) {
  const chart = join(ROOT, p, 'deploy/helm')
  if (!existsSync(join(chart, 'Chart.yaml'))) continue
  const envs = readdirSync(chart).filter((f) => /^values-[\w-]+\.yaml$/.test(f))

  // 🔴 每个环境都要渲染**两遍**：可选开关关掉一遍、全部打开一遍。
  //
  //    只渲染默认值的话，被开关包住的模板根本不会进入渲染 ——
  //    `_hpa.tpl` 的缩进错了整整一段时间没人发现，就是因为
  //    autoscaling 默认是关的，守卫从来没渲染过它。
  //    这类模板恰恰是**最危险**的：平时不渲染，等真要扩容时才炸。
  const TOGGLES = [
    { name: '默认', args: [] },
    {
      // ⚠️ 只开**互不冲突**的开关。
      //    ingress 与 istio 是有意互斥的（同一域名两条入口，
      //    实际生效哪条取决于哪个控制器先接管），chart 里已有断言拦着 ——
      //    在这里一起打开只会撞上那条断言，把守卫自己变成假警报。
      name: 'autoscaling 开',
      args: [
        '--set', 'backend.autoscaling.enabled=true',
        '--set', 'frontend.autoscaling.enabled=true',
      ],
    },
  ]

  for (const env of envs) {
   for (const tg of TOGGLES) {
    let out
    try {
      out = execFileSync(
        'helm',
        ['template', 'x', chart, '-f', join(chart, env),
         '--set', 'frontend.image.tag=vTEST', '--set', 'backend.image.tag=vTEST',
         ...tg.args],
        { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] },
      )
    } catch (e) {
      problems.push(`${p}/${env}（${tg.name}）: helm template 失败\n    ${String(e.stderr || e.message).split('\n')[0]}`)
      continue
    }
    checked++

    // 文本里出现了几次顶格的 kind:
    const kindLines = (out.match(/^kind:/gm) || []).length
    // 按文档分隔符切开后，有几块含 kind
    const docs = out.split(/^---$/m).filter((d) => /^kind:/m.test(d)).length

    if (kindLines !== docs) {
      problems.push(
        `${p}/${env}（${tg.name}）: 渲染出 ${kindLines} 个 kind，但只切得出 ${docs} 个文档` +
          `\n    说明有对象被粘在一起了（多半是 {{- include }} 吃掉了 --- 前的换行）。` +
          `\n    helm 不会报错、grep 数 kind 也看不出来，但被粘住的那个对象**不会被创建**。` +
          `\n    复现：helm template x ${p}/deploy/helm -f ${env} | grep -n '[a-z]---$'`,
      )
    }
   }
  }
}

// ⚠️ 客户会写自己的 values 文件，而且一定是残缺的。
// chart 不能因为对方少写一个可选键就装不上。
//
// 这个 bug 真在生产第一次安装时撞到了：values.yaml 被清成只有一个哨兵键，
// chart 于是没有任何默认值，NOTES.txt 访问 .Values.ingress.enabled 直接
// `nil pointer evaluating interface {}.enabled` —— 而那个报错完全不提
// "你少配了哪个键"，运维只能盯着 NOTES.txt 发呆。
const MINIMAL = `requireEnvValues: false
global: {imageRegistry: "registry.example.com/x"}
frontend:
  replicaCount: 1
  image: {repository: fe, tag: "v1", pullPolicy: IfNotPresent}
  service: {type: ClusterIP, port: 80}
backend:
  replicaCount: 1
  image: {repository: be, tag: "v1", pullPolicy: IfNotPresent}
  service: {port: 8080, metricsPort: 8088}
`
{
  const tmp = join(tmpdir(), `minvals-${process.pid}.yaml`)
  writeFileSync(tmp, MINIMAL)
  for (const p of products()) {
    const chart = join(ROOT, p, 'deploy/helm')
    if (!existsSync(join(chart, 'Chart.yaml'))) continue
    try {
      execFileSync('helm', ['template', 'x', chart, '-f', tmp],
        { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
    } catch (e) {
      const msg = String(e.stderr || e.message).split('\n').slice(0, 2).join(' ')
      problems.push(
        `${p}: 用**最小 values**渲染失败\n    ${msg}` +
          `\n    chart 的 values.yaml 必须含完整默认结构 —— 客户写的 values 一定是残缺的，` +
          `\n    少一个可选键就 nil pointer，而那个报错不会告诉人少了哪个键。`,
      )
    }
  }
  unlinkSync(tmp)
}

if (problems.length > 0) {
  console.error('✗ chart 渲染结构检查未通过：\n')
  for (const m of problems) console.error(`  - ${m}\n`)
  process.exit(1)
}
console.log(`✓ chart 渲染结构：${checked} 个 chart×环境组合，文档分隔正常`)
