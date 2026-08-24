import type { ButtonHTMLAttributes, ReactNode } from 'react'
import { cn } from '../lib/cn.js'

export type ButtonVariant = 'primary' | 'default' | 'ghost' | 'danger'
export type ButtonSize = 'sm' | 'md'

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  size?: ButtonSize
  /** 请求进行中。会自动禁用按钮 —— 不禁用就会被连点，写操作直接重复提交。 */
  loading?: boolean
  icon?: ReactNode
  /**
   * 纯图标按钮（不显示文字）。
   *
   * # 什么时候用
   *
   * 只用于**只读、高频、且图标语义公认**的操作：下载 / 同步 / 测试 / 看日志 / 历史。
   *
   * # 🔴 什么时候绝不能用
   *
   * 1. **破坏性操作**（删除、踢下线）—— 收进 `⋯` 菜单，不做常驻按钮（CONVENTIONS §2.7.7）
   * 2. **会花钱的操作**（续费）—— 它比删除更严格：删除还能从备份恢复，扣费不能撤销。
   *    P0-9 刚把「续费」从紫色主按钮降级过，图标化等于又把它变得更容易误点
   * 3. **语义特殊、没有公认图标的**（如「DNS 验证已就绪」）——
   *    变成图标后没人知道是什么，只能靠 hover，而**触屏上没有 hover**
   * 4. **工具条主按钮**（新增 / 接入）—— 那是这一页的主动作，要让人一眼看见
   *
   * # ⚠️ 一行里并排三个图标，比并排三个文字更难扫
   *
   * 人得逐个 hover 才知道是什么。行内**最多留一个**图标按钮（主操作），
   * 其余收进 `⋯`。
   */
  iconOnly?: boolean
}

const VARIANTS: Record<ButtonVariant, string> = {
  // hover 只动颜色，不动 scale：scale 会让相邻元素位移，一排按钮里尤其明显
  primary:
    'bg-primary text-primary-foreground border-transparent hover:bg-primary-hover',
  default: 'bg-card text-foreground border-border hover:bg-secondary hover:border-border-strong',
  ghost: 'bg-transparent text-muted-foreground border-transparent hover:bg-secondary hover:text-foreground',
  danger: 'bg-danger text-danger-foreground border-transparent hover:opacity-90',
}

const SIZES: Record<ButtonSize, string> = {
  sm: 'h-7 px-2.5 text-xs gap-1.5',
  md: 'h-8 px-3 text-sm gap-2',
}

/**
 * 纯图标按钮的尺寸：正方形，去掉为文字留的横向内边距。
 *
 * ⚠️ 不要做得比 28px 更小。图标按钮已经比文字按钮难点中了
 * （没有文字那一段可点区域），再缩就成了触屏上的靶子。
 *
 * 🔴 必须带 `shrink-0`。行操作区大多是 flex，默认 `flex-shrink: 1` ——
 * 实测证书行里 28px 的下载按钮被压成 **24.5px**。
 * 文字按钮被压一点还看得出是什么，图标按钮被压就只是个更难点中的小方块。
 */
const ICON_SIZES: Record<ButtonSize, string> = {
  sm: 'size-7 shrink-0',
  md: 'size-8 shrink-0',
}

export function Button({
  variant = 'default',
  size = 'md',
  loading = false,
  icon,
  iconOnly,
  disabled,
  className,
  children,
  ...rest
}: ButtonProps) {
  return (
    <button
      type="button"
      // loading 时一并 disabled：只显示转圈但仍可点，是重复提交的头号来源
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={cn(
        'inline-flex cursor-pointer items-center justify-center whitespace-nowrap rounded-[var(--radius)]',
        'border font-medium transition-colors duration-150',
        'disabled:pointer-events-none disabled:opacity-50',
        VARIANTS[variant],
        // 纯图标：正方形，去掉为文字留的横向内边距
        iconOnly ? ICON_SIZES[size] : SIZES[size],
        className,
      )}
      {...rest}
    >
      {loading ? <Spinner /> : icon}
      {iconOnly ? null : children}
    </button>
  )
}

function Spinner() {
  return (
    <svg
      className="size-3.5 animate-spin"
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden="true"
      role="presentation"
    >
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="2.5" opacity="0.25" />
      <path
        d="M21 12a9 9 0 0 0-9-9"
        stroke="currentColor"
        strokeWidth="2.5"
        strokeLinecap="round"
      />
    </svg>
  )
}
