import { AlertTriangle, Info, XCircle } from 'lucide-react'
import type { ReactNode } from 'react'
import { cn } from '../lib/cn.js'

export type BannerTone = 'info' | 'warn' | 'bad'

export interface BannerProps {
  tone: BannerTone
  children: ReactNode
  /** 右侧动作，比如「查看授权」。没有出路的横幅只会让人反复读它 */
  action?: ReactNode
  className?: string
}

const TONES: Record<BannerTone, { box: string; icon: typeof Info }> = {
  info: { box: 'bg-info-bg text-info border-info/25', icon: Info },
  warn: { box: 'bg-warning-bg text-warning border-warning/25', icon: AlertTriangle },
  bad: { box: 'bg-danger-bg text-danger border-danger/25', icon: XCircle },
}

/**
 * 全局横幅。用于"整个系统当前处于某种非正常状态"，比如授权过期转只读。
 *
 * ⚠️ 刻意**不可关闭**：状态还在，关掉只是让人看不见它。
 * 一个用户关掉横幅之后再遇到写操作失败，就完全没有线索了。
 *
 * ⚠️ 三档色调必须真的分开用：把"14 天后到期"和"已经只读"渲染成同一个红条，
 * 客户会把前者也当成故障来报；反过来把只读渲染成灰色提示，
 * 则会让人一直以为是自己点错了。
 */
export function Banner({ tone, children, action, className }: BannerProps) {
  const { box, icon: Icon } = TONES[tone]
  return (
    <div
      role="status"
      className={cn(
        'flex items-center gap-2.5 border-b px-5 py-2 text-[13px] leading-relaxed',
        box,
        className,
      )}
    >
      <Icon className="size-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 flex-1">{children}</span>
      {action}
    </div>
  )
}
