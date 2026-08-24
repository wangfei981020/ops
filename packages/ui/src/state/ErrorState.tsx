import { AlertCircle } from 'lucide-react'
import type { ReactNode } from 'react'
import { cn } from '../lib/cn.js'
import type { LoadError } from './types.js'

export interface ErrorStateProps {
  title: string
  error: LoadError
  /**
   * 重试。**必填**（`retryable: false` 时会自动隐藏按钮，但回调仍要给）。
   *
   * 一个没有出路的错误页等于死胡同。就算重试大概率还是失败，
   * 用户也需要一个"我可以再试一次"的动作，而不是只能刷新整页。
   */
  onRetry: () => void
  /**
   * 重试按钮文案。**必填、且必须来自语言包。**
   *
   * 早期版本这里给了个中文默认值，结果英文界面上的错误态出现一个中文按钮 ——
   * 错误路径平时走不到，这种漏译能一直活到客户手里。
   * 去掉默认值后，漏传就是编译错误。
   */
  retryLabel: string
  /** 额外出路，比如「查看凭据配置」「联系管理员」 */
  actions?: ReactNode
  className?: string
}

export function ErrorState({
  title,
  error,
  onRetry,
  retryLabel,
  actions,
  className,
}: ErrorStateProps) {
  return (
    <div
      // 错误态用 danger 色的边框 + 淡底，与空态（中性色）在视觉上一眼可分。
      // 两者共用同一个外观是「失败退化成空态」的另一种形态。
      className={cn(
        'flex flex-col items-start gap-2.5 rounded-[var(--radius-md)] px-5 py-5',
        'border border-[color-mix(in_oklch,var(--ops-danger)_35%,transparent)] bg-danger-bg',
        className,
      )}
      role="alert"
    >
      <AlertCircle className="size-5 text-danger" aria-hidden="true" />
      <p className="text-sm font-semibold text-foreground">{title}</p>
      <p className="max-w-[46ch] text-xs leading-relaxed text-foreground/80">{error.cause}</p>
      {error.detail ? (
        // 技术细节用等宽字，且可选中复制 —— 排障的人要把它贴进工单。
        //
        // ⚠️ `break-all` 而不是 `break-words`：这里是**等宽字体的原始错误串**
        // （URL、连接串、堆栈），本来就没有空格可断。break-words 只在词边界断，
        // 对一整串没有空格的 URL 无效 —— 得允许在任意字符处断开。
        // 而且这一段是 select-all（方便复制去贴工单），被裁掉就复制不全。
        <p className="select-all break-all font-mono text-[11px] text-muted-foreground">
          {error.detail}
        </p>
      ) : null}
      <div className="mt-1 flex flex-wrap gap-2">
        {error.retryable ? (
          <button
            type="button"
            onClick={onRetry}
            className={cn(
              'inline-flex h-8 cursor-pointer items-center rounded-[var(--radius)] px-3',
              'bg-primary text-xs font-medium text-primary-foreground',
              'transition-colors duration-150 hover:bg-primary-hover',
            )}
          >
            {retryLabel}
          </button>
        ) : null}
        {actions}
      </div>
    </div>
  )
}
