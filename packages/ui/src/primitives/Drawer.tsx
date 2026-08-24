import { X } from 'lucide-react'
import { type ReactNode, useEffect, useRef } from 'react'
import { cn } from '../lib/cn.js'

export interface DrawerProps {
  open: boolean
  onClose: () => void
  title: ReactNode
  /** 标题下方的一行副信息，如 IP、集群 */
  subtitle?: ReactNode
  /** 底部固定操作区 */
  footer?: ReactNode
  /** 关闭按钮的无障碍名，来自语言包。组件库不含面向用户的字符串。 */
  closeLabel: string
  children: ReactNode
  width?: number
}

/**
 * 右侧抽屉。用于"看某一条的细节"，不打断列表上下文。
 *
 * 为什么用抽屉而不是跳详情页：排障时人是在**列表里比较**——
 * 看完这台看下一台。跳走再回来，筛选、滚动位置、翻页全得重建，
 * 而那些正是他刚刚花时间调出来的。
 */
export function Drawer({
  open,
  onClose,
  title,
  subtitle,
  footer,
  closeLabel,
  children,
  width = 520,
}: DrawerProps) {
  const panelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    // 抽屉打开时锁住背景滚动：否则滚轮会滚动身后的表格，
    // 关掉抽屉后发现列表跑到了别处，人会以为自己点错了什么
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
    <div className="fixed inset-0 z-30">
      {/*
        遮罩点击关闭。
        ⚠️ 这和「弹窗禁止点遮罩关闭」不冲突：那条针对的是有未保存内容的表单弹窗，
        误关代价高。详情抽屉是只读的，点外面收起是所有人的预期。
        将来抽屉里出现可编辑表单时，这里要改成拦截 + 二次确认。
      */}
      <button
        type="button"
        aria-label={closeLabel}
        onClick={onClose}
        className="absolute inset-0 cursor-default bg-overlay"
      />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        tabIndex={-1}
        style={{ width }}
        className={cn(
          'absolute top-0 right-0 flex h-full max-w-[92vw] flex-col',
          'border-l border-border-strong bg-card shadow-modal outline-none',
        )}
      >
        <div className="flex items-start gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold text-foreground">{title}</div>
            {subtitle ? (
              <div className="mt-0.5 truncate text-xs text-muted-foreground">{subtitle}</div>
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

        <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>

        {footer ? <div className="border-t border-border px-4 py-3">{footer}</div> : null}
      </div>
    </div>
  )
}

/** 抽屉里的一节。 */
export function DrawerSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="border-b border-border px-4 py-3 last:border-b-0">
      <h3 className="mb-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        {title}
      </h3>
      {children}
    </section>
  )
}

/** 键值对行。值缺失时由调用方传 <NoValue>，不要在这里兜底成空字符串。 */
export function DrawerField({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline gap-3 py-1 text-[13px]">
      <span className="w-[88px] shrink-0 text-muted-foreground">{label}</span>
      <span className="min-w-0 flex-1 break-words text-foreground">{children}</span>
    </div>
  )
}
