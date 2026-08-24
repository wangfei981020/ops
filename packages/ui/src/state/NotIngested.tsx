import { PlugZap } from 'lucide-react'
import type { ReactNode } from 'react'

export interface NotIngestedProps {
  /** 「还没有采到防火墙规则」——说的是现象，不是结论 */
  title: string
  /**
   * 后端给的原因。**必填**，而且应当直接用后端返回的 `empty_hint`，
   * 不要在前端另写一句。
   *
   * 后端才知道到底是"没配凭据"还是"配了但缺 roles/dns.reader"，
   * 前端拿到的只是一个空数组 —— 在这里自己编一句原因，
   * 编出来的多半是错的，而错的原因比没有原因更浪费时间。
   */
  reason: string
  /** 下一步。没有出路的空态只会让人反复刷新 */
  action?: ReactNode
}

/**
 * 页面级的「未接入」空态。
 *
 * # 和普通空态的区别
 *
 * `EmptyState` 说的是"这里确实没有东西"；
 * 这个组件说的是"**我们没能拿到数据**，所以不知道有没有东西"。
 *
 * 两者绝不能长得一样。实测过的一句后端提示最能说明问题：
 *
 * > 没有任何 IAM 数据。任何 GCP 项目都至少有一条权限绑定，
 * > 所以这说明「尚未采集成功」，而不是「没有风险」。
 *
 * 渲染成普通空态的话，一个采集挂掉的权限审计页看起来就是"你很安全"——
 * 这正是整个系统在防的那类问题，而且是最贵的一种：它让人**放心**。
 *
 * # 为什么不用警告色
 *
 * 未接入不是故障，是"还没配"。用红色会让每个刚装完系统的人以为出了事。
 * 用中性色 + 明确的下一步，比吓人一跳更有用。
 */
export function NotIngested({ title, reason, action }: NotIngestedProps) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-[var(--radius-lg)] border border-dashed border-border px-6 py-10 text-center">
      <PlugZap className="size-5 text-muted-foreground" aria-hidden="true" />
      <p className="text-[13px] font-medium text-foreground">{title}</p>
      {/* 原因给足行宽：这类提示往往是两三句话，挤成一行没人会读完 */}
      {/* ⚠️ 同 EmptyState：reason 里可能是原样透传的长错误串，不折行会被裁掉 */}
      <p className="max-w-[560px] break-words text-xs leading-relaxed text-muted-foreground">
        {reason}
      </p>
      {action ? <div className="mt-1.5">{action}</div> : null}
    </div>
  )
}
