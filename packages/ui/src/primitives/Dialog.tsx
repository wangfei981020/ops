import { X } from 'lucide-react'
import { type ReactNode, useEffect, useRef } from 'react'
import { cn } from '../lib/cn.js'

export interface DialogProps {
  open: boolean
  onClose: () => void
  title: string
  /** 标题下的一行说明，讲清这个操作会影响什么 */
  description?: string
  children: ReactNode
  footer: ReactNode
  closeLabel: string
  width?: number
}

/**
 * 表单对话框。
 *
 * ⚠️ **禁止点遮罩关闭**（约定 §2.9）。这里面装的是有未保存内容的表单，
 * 误关一次就得重填一遍。抽屉是只读的所以可以点外面收起，对话框不行。
 *
 * ⚠️ 也不用浏览器原生的 confirm/alert：原生弹窗没法做主题、没法翻译，
 * 而且在不同浏览器上长得完全不一样。
 */
export function Dialog({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  closeLabel,
  width = 520,
}: DialogProps) {
  const panelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      // Esc 关闭是保留的：它是个明确的动作，不像误点遮罩
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.removeEventListener('keydown', onKey)
      document.body.style.overflow = prev
    }
  }, [open, onClose])

  useEffect(() => {
    if (open) panelRef.current?.focus()
  }, [open])

  if (!open) return null

  return (
    <div className="fixed inset-0 z-40 flex items-center justify-center p-4">
      {/* 遮罩只是背景，**不绑点击**——见组件注释 */}
      <div className="absolute inset-0 bg-overlay" />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
        style={{ width }}
        className={cn(
          'relative flex max-h-[86vh] w-full max-w-[92vw] flex-col',
          'rounded-[var(--radius-lg)] border border-border-strong bg-card shadow-modal outline-none',
        )}
      >
        <div className="flex items-start gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0 flex-1">
            <div className="text-sm font-semibold text-foreground">{title}</div>
            {description ? (
              <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">{description}</p>
            ) : null}
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={closeLabel}
            className={cn(
              'cursor-pointer rounded-[var(--radius)] p-1 text-muted-foreground',
              'transition-colors duration-150 hover:bg-secondary hover:text-foreground',
            )}
          >
            <X className="size-4" />
          </button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">{children}</div>

        <div className="flex items-center justify-end gap-2 border-t border-border px-4 py-3">
          {footer}
        </div>
      </div>
    </div>
  )
}
