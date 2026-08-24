import type { CSSProperties } from 'react'
import { cn } from '../lib/cn.js'

export function Skeleton({
  className,
  style,
}: {
  className?: string
  style?: CSSProperties
}) {
  return (
    <div
      className={cn('animate-pulse rounded-[var(--radius-sm)] bg-border', className)}
      style={style}
    />
  )
}

export interface TableSkeletonProps {
  rows?: number
  /** 各列宽度百分比。**必须与真实表格的列宽一致**，否则数据到位时整片跳动。 */
  columns: number[]
  /** 表头是否也参与闪烁。默认 false —— 表头是已知的，闪它只会制造噪声。 */
  shimmerHeader?: boolean
}

/** 让每行长短略有差异，避免整片等宽色块看起来像坏掉的表格而不是加载中。 */
const JITTER = [1, 0.86, 0.94, 0.72, 0.9, 0.8, 0.96, 0.76]

/**
 * 表格骨架屏。
 *
 * 用骨架而不是转圈：转圈不占位，内容到位时整页往下跳（CLS）。
 * 骨架必须复刻真实布局的行数与列宽，否则等于换了一种方式抖动。
 */
export function TableSkeleton({ rows = 8, columns, shimmerHeader = false }: TableSkeletonProps) {
  return (
    <div className="w-full" aria-busy="true" aria-live="polite">
      <div className="flex items-center gap-4 border-b border-border px-3 py-2">
        {columns.map((w, i) => (
          <div
            key={`h-${i}-${w}`}
            style={{ width: `${w}%` }}
            className={cn(
              'h-2.5 rounded-[var(--radius-sm)] bg-border',
              shimmerHeader && 'animate-pulse',
            )}
          />
        ))}
      </div>
      {Array.from({ length: rows }, (_, r) => (
        <div
          key={`r-${r}`}
          className="flex items-center gap-4 border-b border-border px-3"
          style={{ height: 'var(--ops-row-h)' }}
        >
          {columns.map((w, i) => (
            <Skeleton
              key={`c-${i}-${w}`}
              className="h-2.5"
              style={{ width: `${w * (JITTER[(r + i) % JITTER.length] ?? 1)}%` }}
            />
          ))}
        </div>
      ))}
    </div>
  )
}
