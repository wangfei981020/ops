import { cn } from '../lib/cn.js'

/**
 * 状态徽章。
 *
 * tone 是**语义**不是颜色：调用方写 `tone="bad"` 表示"这是故障"，
 * 而不是 `color="red"`。这样换主题、换品牌色时语义不会跟着漂。
 */
export type BadgeTone = 'ok' | 'warn' | 'bad' | 'info' | 'mute'

const TONES: Record<BadgeTone, string> = {
  ok: 'bg-success-bg text-success',
  warn: 'bg-warning-bg text-warning',
  bad: 'bg-danger-bg text-danger',
  info: 'bg-info-bg text-info',
  // 未知/已停用：中性灰。刻意不用任何语义色 ——
  // 「不知道」和「正常」必须能一眼分开。
  mute: 'bg-[color-mix(in_oklch,var(--ops-n600)_16%,transparent)] text-muted-foreground',
}

export interface BadgeProps {
  tone: BadgeTone
  children: React.ReactNode
  /** 左侧圆点。状态类建议开，纯标签类关掉。 */
  dot?: boolean
  /**
   * 悬停说明。
   *
   * ⚠️ 用于解释**为什么是这个颜色**，而不是重复徽章上的文字。
   *	典型场景：状态是 ok 但被降成了 warn（任务成功却没人收到结果）——
   *	不说明的话，人只会觉得"这个绿的怎么变黄了"。
   */
  title?: string
  className?: string
}

export function Badge({ tone, children, dot = true, title, className }: BadgeProps) {
  return (
    <span
      title={title}
      className={cn(
        'inline-flex h-5 items-center gap-1.5 rounded-[var(--radius-sm)] px-1.5',
        'text-[11.5px] font-medium leading-none whitespace-nowrap',
        TONES[tone],
        className,
      )}
    >
      {dot ? <span className="size-1.5 shrink-0 rounded-full bg-current" aria-hidden="true" /> : null}
      {children}
    </span>
  )
}
