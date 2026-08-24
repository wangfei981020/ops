import { cn } from '../lib/cn.js'

/**
 * 「没有值」的四种语义。
 *
 * 把它们统一渲染成空白或 0，是这套系统上一版最大的一类问题：
 * 「采集失败」和「本来就没有」在界面上长得一模一样，
 * 看的人会按「本来就没有」去理解，然后基于错的前提做决定。
 *
 *   na          该字段对这类对象本就不适用（自建机没有云厂商机型）
 *   notIngested 数据源没配，所以拿不到（集群没接 Prometheus）
 *   stopped     因状态而不再产生值（主机已销毁，停止计费）
 *   unknown     采集过但没拿到，原因不明 —— 这是**故障信号**，不是空值
 */
export type NoValueKind = 'na' | 'notIngested' | 'stopped' | 'unknown'

export interface NoValueProps {
  kind: NoValueKind
  /** 各语义的显示文案，由调用方从语言包取，组件不内置中文。 */
  labels: Record<NoValueKind, string>
  className?: string
}

const STYLES: Record<NoValueKind, string> = {
  na: 'text-muted-foreground',
  notIngested: 'text-muted-foreground italic',
  stopped: 'text-muted-foreground',
  // unknown 用警告色：它代表采集链路出了问题，值得被看见。
  // 和其他三种一样淡的话，故障就被淹没在正常的空值里了。
  unknown: 'text-warning',
}

export function NoValue({ kind, labels, className }: NoValueProps) {
  return (
    <span className={cn('text-xs', STYLES[kind], className)} title={labels[kind]}>
      {labels[kind]}
    </span>
  )
}
