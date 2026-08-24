import { useQuery } from '@tanstack/react-query'
import { useEffect } from 'react'
import { api } from './api.js'

export interface Branding {
  app_name: string
  logo_data: string
  favicon_data: string
  tagline: string
  updated_by: string
}

const EMPTY: Branding = {
  app_name: '', logo_data: '', favicon_data: '', tagline: '', updated_by: '',
}

/**
 * 读品牌配置。
 *
 * ⚠️ 拿不到就退回全空（各处用产品内置的名字与图标）——
 * 品牌是**装饰**，它读失败不该让人连页面都打不开。
 */
export function useBranding() {
  const q = useQuery({
    queryKey: ['branding'],
    queryFn: () => api<Branding>('/api/branding'),
    // 品牌几乎不变，缓存久一点，免得每次切页都打一次接口
    staleTime: 5 * 60 * 1000,
    retry: false,
  })
  return q.data ?? EMPTY
}

/**
 * 把自定义 favicon 挂到文档上。
 *
 * 🔴 只能用 JS 动态改 `<link rel="icon">` —— favicon 在 index.html 里是静态的，
 * 而"每个客户不同的图标"是运行期才知道的事，构建期塞不进去。
 *
 * ⚠️ 必须**复用同一个 link 元素**（按 id 找），不能每次 append 一个新的：
 * 浏览器对多个 icon link 的取舍各不相同，追加会出现"改了但没变"或者
 * "刷新一次变一次"的诡异现象。
 */
export function useFavicon(dataUri: string) {
  useEffect(() => {
    if (!dataUri) return
    const id = 'ops-favicon'
    let link = document.getElementById(id) as HTMLLinkElement | null
    if (!link) {
      link = document.createElement('link')
      link.id = id
      link.rel = 'icon'
      document.head.appendChild(link)
    }
    link.href = dataUri
  }, [dataUri])
}

/**
 * 把自定义系统名写进标签页标题。
 *
 * ⚠️ 与 favicon 分开：客户可能只想换名字不换图，或者反过来。
 * 合成一个 hook 会让"只设置了其中一个"时另一个被清掉。
 */
export function useAppTitle(appName: string, fallback: string) {
  useEffect(() => {
    document.title = appName.trim() || fallback
  }, [appName, fallback])
}
