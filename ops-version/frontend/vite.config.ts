import { opsAssetGuard, opsBootScript } from '@ops/design/vite'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const VERSION = process.env.VERSION ?? 'dev'
const COMMIT = process.env.GIT_COMMIT ?? 'unknown'

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    // 首屏主题引导，必须排在样式之前 —— 漏了会白闪一帧
    opsBootScript(),
    // 滚动更新期间新旧 pod 并存会让入口 JS 404 → 纯白页面。
    // 这段脚本捕获资源加载失败并自动刷新一次，把白屏兜成"最多闪一下"。
    opsAssetGuard(),
    {
      name: 'ops-version-banner',
      closeBundle() {
        const warn = VERSION === 'dev' ? '  ← 未传 --build-arg VERSION，产物会标成 dev' : ''
        console.log(`\n[ops] 构建版本 ${VERSION} (${COMMIT})${warn}\n`)
      },
    },
  ],
  define: {
    __APP_VERSION__: JSON.stringify(VERSION),
    __APP_COMMIT__: JSON.stringify(COMMIT),
  },
  server: {
    port: 5274,
    proxy: {
      // 本地开发指向 k8s-proxy 的固定端口（约定：不用临时 port-forward）
      '/api': { target: process.env.API_TARGET ?? 'http://127.0.0.1:30837', changeOrigin: true },
    },
  },
  build: { sourcemap: true },
})
