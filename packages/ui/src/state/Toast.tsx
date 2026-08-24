import { useCallback, useEffect, useState } from 'react'

export type ToastKind = 'ok' | 'err'

/**
 * 操作结果浮层。
 *
 * 🔴 全站**只能有这一套**反馈机制。
 *
 *   原来平台页用右下角浮层、镜像同步页用页面内联提示条 ——
 *   两种视觉、两种位置、两种消失方式。同一个应用里学两遍，
 *   而且内联那种会**跟着页面滚走**。
 *
 * 🔴 **放右下角，不能放右上角**：
 *
 *   这个应用右上角全是操作按钮 —— 新增平台 / 全部采集 / 立即拉取。
 *   而**触发 toast 的正是那些按钮**，放右上角就是"刚点的按钮被自己弹出的提示盖住"，
 *   每点一次必现一次。顶部是操作区，本来就不该放浮层。
 *
 * ⚠️ z-50 要高过表格的 sticky 表头，否则会被压在下面。
 */
export function Toast({ msg, kind }: { msg: string; kind: ToastKind }) {
  return (
    <div
      role="status"
      aria-live="polite"
      className={
        'fixed bottom-5 right-5 z-50 max-w-md rounded-md border px-3 py-2 text-xs shadow-lg ' +
        (kind === 'ok'
          ? 'border-success bg-success-bg text-success'
          : 'border-danger bg-danger-bg text-danger')
      }
    >
      {msg}
    </div>
  )
}

/**
 * 配套的状态管理。
 *
 * ⚠️ 失败的提示停留更久（8s vs 3.5s）：成功只需要"知道成了"，
 * 而失败那句话里往往带着下一步该做什么，3 秒读不完。
 */
export function useToast(): {
  toast: { msg: string; kind: ToastKind } | null
  show: (msg: string, kind?: ToastKind) => void
} {
  const [toast, setToast] = useState<{ msg: string; kind: ToastKind } | null>(null)
  const [until, setUntil] = useState(0)

  const show = useCallback((msg: string, kind: ToastKind = 'ok') => {
    setToast({ msg, kind })
    setUntil(Date.now() + (kind === 'err' ? 8000 : 3500))
  }, [])

  useEffect(() => {
    if (!toast) return
    const left = until - Date.now()
    if (left <= 0) {
      setToast(null)
      return
    }
    // ⚠️ 用「到期时刻」而不是每次 setTimeout：连着弹两条时，
    //    后一条应该重新计时，而不是被前一条的定时器提前清掉。
    const id = setTimeout(() => setToast(null), left)
    return () => clearTimeout(id)
  }, [toast, until])

  return { toast, show }
}
