import type { ReactNode } from 'react'
import { cn } from '../lib/cn.js'

export interface EmptyStateAction {
  label: string
  onClick: () => void
}

export interface EmptyStateProps {
  title: string
  /**
   * 为什么是空的。**必填。**
   *
   * 「暂无数据」是最差的答案 —— 它没有告诉用户任何可以行动的信息。
   * 好的空态回答三件事之一：还没配数据源 / 筛选条件太窄 / 确实一条都没有。
   */
  reason: string
  /**
   * 下一步能做什么。**必须显式给值。**
   *
   * 类型是 `EmptyStateAction | null` 而不是可选参数：确实无事可做时，
   * 作者必须亲手写一个 `null`，逼他想过一遍「这里真的没有下一步吗」。
   * 写成可选的话，绝大多数人会直接不传。
   */
  action: EmptyStateAction | null
  icon?: ReactNode
  className?: string
}

export function EmptyState({ title, reason, action, icon, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center gap-3 px-6 py-12 text-center',
        className,
      )}
    >
      {icon ? <div className="text-muted-foreground [&>svg]:size-6">{icon}</div> : null}
      <p className="text-sm font-semibold text-foreground">{title}</p>
      {/*
        ⚠️ `break-words` 不能省。
        reason 里经常是**原样透传的上游错误**，而那些错误里有很长且不含空格的东西
        （URL-encoded 的 PromQL、连接串）。不折行的话浏览器不会断开它，
        `max-w-[42ch]` 只是限制了盒子宽度，内容照样横向溢出被裁掉。
        实测闲置浪费页的空态：**718px 内容读不到**，而被裁掉的正是
        `dial tcp: lookup vmselect.monitoring.svc ... no such host` ——
        排障需要的那一句。
        ⚠️ 这里改一处，全站所有空态都受益：它们都可能收到长错误串。
      */}
      <p className="max-w-[42ch] break-words text-xs leading-relaxed text-muted-foreground">
        {reason}
      </p>
      {action ? (
        <button
          type="button"
          onClick={action.onClick}
          className={cn(
            'mt-1 inline-flex h-8 cursor-pointer items-center rounded-[var(--radius)] px-3',
            'bg-primary text-xs font-medium text-primary-foreground',
            'transition-colors duration-150 hover:bg-primary-hover',
          )}
        >
          {action.label}
        </button>
      ) : null}
    </div>
  )
}
