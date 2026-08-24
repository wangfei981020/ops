import { I18nextProvider } from '@ops/i18n'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App.js'
import { i18n } from './i18n.js'
import './styles.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 切回标签页就重新拉：版本数据几分钟就过期，
      // 拿着陈旧数据下「对方落后了」这种结论，比看到转圈危险得多
      refetchOnWindowFocus: true,
      // 只重试一次。鉴权失败、权限不足重试多少次都是同样结果，
      // 真正的网络抖动一次就够了
      retry: 1,
    },
  },
})

const root = document.getElementById('root')
if (!root) throw new Error('#root not found')

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <I18nextProvider i18n={i18n}>
        <App />
      </I18nextProvider>
    </QueryClientProvider>
  </StrictMode>,
)
