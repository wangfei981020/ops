import { Badge, type BadgeTone } from './Badge.js'

/**
 * 枚举字段的徽章 —— 把「没枚举到的值长什么样」从调用方的自觉，变成组件的保证。
 *
 * # 为什么需要它
 *
 * 手写三元链渲染后端枚举字段，栽过四次，每次都是**兜底分支吞掉了新值**：
 *
 * | | 兜底是什么 | 结果 |
 * |---|---|---|
 * | | `failed` | 预演成功显示成红色「失败」 |
 * | | `failed` | 还没处理到的显示成红色「失败」 |
 * | | `pending` | 匹配不上的域名永远显示「续费中」 |
 *
 * 三次都是同一个形状：`a ? X : b ? Y : Z`，而 `Z` 是一个**具体状态**。
 * 只要后端多一个取值、或者同一字段在不同阶段有不同语义，它就掉进 Z 里 ——
 * 不报错、不空白，只是**安静地显示成另一件事**。
 *
 * 🔴 危害不是难看：假的「失败」会诱发重试（而续费重试可能重复扣费），
 * 假的「处理中」会让人以为还要等（而它其实被跳过了，域名会到期）。
 *
 * # 用法
 *
 * ```tsx
 * <EnumBadge
 *   value={item.status}
 *   cases={{
 *     succeed:   { tone: 'ok',   label: t('common:job.status.succeed') },
 *     failed:    { tone: 'bad',  label: t('common:job.status.failed') },
 *     not_found: { tone: 'bad',  label: t('common:job.status.notFound') },
 *   }}
 *   unknown={(v) => t('common:job.status.unknown', { value: v })}
 * />
 * ```
 *
 * 没列进 `cases` 的值一律走 `unknown`（警示色 + 显示原值），
 * **不会**被归到任意一档。这正是手写三元链做不到的那一点。
 *
 * ⚠️ 本包刻意不依赖 i18n，所以 label 传的是**已翻译好的字符串**，
 *	由各产品把自己的 t() 结果传进来。
 */
export interface EnumCase {
  tone: BadgeTone
  label: string
  /** 悬停说明，解释为什么是这个颜色 */
  title?: string
}

export interface EnumBadgeProps {
  /** 后端给的原始值。null/undefined/'' 都算「没有值」，走 empty。 */
  value: string | null | undefined
  cases: Record<string, EnumCase>
  /**
   * 值不在 cases 里时怎么显示。
   *
   * ⚠️ 必须把原值带出来 —— 「未知」而不说是什么，人无从判断该去查哪里。
   *	对应 CONVENTIONS §3.4「未识别枚举必须 WARN」的前端版本。
   */
  unknown: (value: string) => string
  /**
   * 值为空时怎么显示。不传就复用 unknown('')。
   *
   * ⚠️ 「字段是空的」和「字段是个没见过的值」是两件事：
   *	前者多半是没采到/还没算，后者是契约变了。
   */
  empty?: string
  dot?: boolean
  className?: string
}

export function EnumBadge({ value, cases, unknown, empty, dot, className }: EnumBadgeProps) {
  const v = (value ?? '').trim()
  if (!v) {
    return (
      <Badge tone="mute" dot={dot} className={className}>
        {empty ?? unknown('')}
      </Badge>
    )
  }
  const hit = cases[v]
  if (hit) {
    return (
      <Badge tone={hit.tone} dot={dot} title={hit.title} className={className}>
        {hit.label}
      </Badge>
    )
  }
  // 🔴 兜底只能是「未知」，而且带上原值。
  //	这一档是整个组件存在的理由 —— 手写三元链时它总是被写成某个具体状态。
  return (
    <Badge tone="warn" dot={dot} className={className}>
      {unknown(v)}
    </Badge>
  )
}
