/**
 * Vite 插件：把首屏引导脚本内联进 index.html 的 <head> 最前面。
 *
 * 为什么必须是插件而不是手写进 index.html：
 * 每个应用都有自己的 index.html，手抄一遍就有一份会漂移。
 * 主题防闪这种"改了也不报错、只是偶尔白闪一帧"的东西，漂移了没人会发现。
 */

import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// 这里刻意不声明 vite 的类型依赖 —— design 是纯设计系统包，
// 让它依赖构建工具的类型会把 vite 拖进每个消费方的依赖图。
interface IndexHtmlTag {
  tag: string
  children?: string
  injectTo?: 'head' | 'head-prepend' | 'body' | 'body-prepend'
  attrs?: Record<string, string | boolean>
}

interface MinimalPlugin {
  name: string
  enforce?: 'pre' | 'post'
  transformIndexHtml: {
    order: 'pre'
    handler: (html: string) => { html: string; tags: IndexHtmlTag[] }
  }
}

export function opsBootScript(): MinimalPlugin {
  return {
    name: 'ops-boot-script',
    enforce: 'pre',
    transformIndexHtml: {
      order: 'pre',
      handler(html: string) {
        const path = fileURLToPath(new URL('../src/boot.js', import.meta.url))
        const code = readFileSync(path, 'utf8')
        return {
          html,
          tags: [
            {
              tag: 'script',
              // head-prepend：必须排在任何 <link rel=stylesheet> 之前，
              // 否则浏览器已经用默认配色画了第一帧。
              injectTo: 'head-prepend',
              children: code,
            },
          ],
        }
      },
    },
  }
}

/**
 * Vite 插件：把「静态资源自愈」脚本内联进 index.html 的 <head> 最前面。
 *
 * 解决滚动更新期间新旧 pod 并存导致的白屏——
 * 机制与取舍见 src/asset-guard.js 的注释。
 *
 * 🔴 必须内联且提前：挂掉的是**入口 JS**，写在应用代码里的处理注册不上。
 * ⚠️ 与 opsBootScript 分开，是因为两者职责不同（一个防主题白闪、一个防资源 404），
 *    而且不是每个产品都跑在滚动更新的 k8s 里 —— 让消费方自己决定要不要启用。
 */
export function opsAssetGuard(): MinimalPlugin {
  return {
    name: 'ops-asset-guard',
    enforce: 'pre',
    transformIndexHtml: {
      order: 'pre',
      handler(html: string) {
        const path = fileURLToPath(new URL('../src/asset-guard.js', import.meta.url))
        const code = readFileSync(path, 'utf8')
        return {
          html,
          tags: [
            {
              tag: 'script',
              // head-prepend：必须排在 Vite 注入的入口 <script> 之前，
              // 否则那个 script 挂掉时监听器还没注册上。
              injectTo: 'head-prepend',
              children: code,
            },
          ],
        }
      },
    },
  }
}
