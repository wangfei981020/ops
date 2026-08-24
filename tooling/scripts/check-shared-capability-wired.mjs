#!/usr/bin/env node
/**
 * 共享包里**面向终端用户的能力**，有没有在产品界面上留出入口。
 *
 * # 为什么需要这一道
 *
 * 已有的 check-read-coverage 查的是「**后端接口**有没有前端入口」。
 * 而 撞到的是同一个形状、差了一层的东西：
 *
 *   @ops/ui 里 ThemeToggle / LocaleToggle / PreferencesMenu 三个组件
 *   早就写好了，@ops/design 的主题机制、@ops/i18n 的两份完整语言包
 *   （各 554 条、一条不缺）也全是好的 —— **缺的只有那个开关本身**。
 *   另一个产品 接了，ops-version 没接，谁都没发现。
 *
 * 用户的原话是「怎么没有看到切换白天黑夜的，切换中英文的也没看到」。
 *
 * # 这类缺口为什么发现不了
 *
 * 它**不报任何错**：
 *   - 组件在共享包里编译得好好的
 *   - 语言包完整，翻译一条不少
 *   - 产品构建通过，页面正常渲染
 *   - 少的只是一个入口，而没人会去数入口
 *
 * 唯一能发现它的方式是有人打开界面找那个按钮 —— 也就是用户。
 *
 * # 判据
 *
 * 一个能力可以由**多个组件之一**提供（主题既能用一键 ThemeToggle，
 * 也能在 PreferencesMenu 菜单里切）。只要引用了其中任意一个就算接上。
 *
 * ⚠️ 只查「有没有 import/引用」，不查「渲染到了哪里」——
 *    后者要跑起来才知道，而守卫是静态的。
 *    引用了却没渲染的情况交给验收时的实际点击（那是人的活）。
 */
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

const ROOT = new URL('../..', import.meta.url).pathname

/**
 * 每个有界面的产品都应该给用户留出入口的共享能力。
 *
 * 🔴 加一条之前先问：**这真的是每个产品都该有的吗？**
 *    只有一两个产品需要的东西不属于这里 —— 那会让守卫变成噪音，
 *    而一道总在报错、大家习惯性忽略的守卫，比没有守卫更糟。
 */
const CAPABILITIES = [
  {
    name: '切换明暗主题',
    providers: ['ThemeToggle', 'PreferencesMenu'],
    why: '夜里打开一屏白光是劝退的；@ops/design 的主题机制早就在了',
  },
  {
    name: '切换语言',
    providers: ['LocaleToggle'],
    why: '语言包是完整的两份，没有开关等于只做了一半；看不懂的人连登录框都读不了',
  },
]

function walk(dir, out = []) {
  let entries
  try {
    entries = readdirSync(dir)
  } catch {
    return out
  }
  for (const e of entries) {
    if (e === 'node_modules' || e === 'dist' || e.startsWith('.')) continue
    const p = join(dir, e)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (/\.(tsx|ts)$/.test(e)) out.push(p)
  }
  return out
}

const only = process.argv[2]
const products = readdirSync(ROOT)
  .filter((d) => d.startsWith('ops-'))
  .filter((d) => !only || d === only)
  .filter((d) => {
    try {
      return statSync(join(ROOT, d, 'frontend', 'src')).isDirectory()
    } catch {
      return false
    }
  })

if (products.length === 0) {
  console.log(`✓ check-shared-capability-wired: 没有要检查的产品${only ? `（${only} 没有前端）` : ''}`)
  process.exit(0)
}

const gaps = []
for (const p of products) {
  const src = walk(join(ROOT, p, 'frontend', 'src'))
    .map((f) => readFileSync(f, 'utf8'))
    .join('\n')
  for (const cap of CAPABILITIES) {
    if (!cap.providers.some((c) => new RegExp(`\\b${c}\\b`).test(src))) {
      gaps.push({ product: p, cap })
    }
  }
}

if (gaps.length === 0) {
  console.log(
    `✓ check-shared-capability-wired: ${products.length} 个产品都接上了共享能力的入口` +
      `（${CAPABILITIES.map((c) => c.name).join(' / ')}）`,
  )
  process.exit(0)
}

console.error('✗ 共享包里有能力，产品界面上没有入口：\n')
for (const { product, cap } of gaps) {
  console.error(`  ${product} —— 缺「${cap.name}」`)
  console.error(`      能力在：@ops/ui 的 ${cap.providers.join(' / ')}`)
  console.error(`      为什么要有：${cap.why}\n`)
}
console.error(
  '这类缺口**不报任何错**：组件编译得好好的、语言包完整、页面正常渲染，\n' +
    '少的只是一个入口，而没人会去数入口 —— 只有用户打开界面找那个按钮时才会发现。\n\n' +
    '接法参考 ops-version/frontend/src/layouts/Preferences.tsx（50 行的薄封装：\n' +
    'ui 包刻意不依赖 i18n，所以每个产品要把自己的 t() 结果传进去）。\n' +
    '⚠️ 顶栏和**登录页**都要挂 —— 看不懂中文的人连登录框都读不了。',
)
process.exit(1)
