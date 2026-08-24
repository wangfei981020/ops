/**
 * 判定的**视觉语义**：五态各自长什么样，只在这里定义一次。
 *
 * 🔴 为什么要单独一个文件：判定色至少出现在三个地方 ——
 * 单元格、行首色带、顶部统计条。三处各写一份 map，
 * 改了其中一处忘了另外两处，界面会出现「统计条说 3 个不一致、
 * 表里只有 1 行是红的」这种自相矛盾，而它不报错、也不好复现。
 *
 * 🔴 判定本身**不在这里算** —— 后端 compare.RowVerdict 是唯一出口，
 * 前端只负责把它翻译成颜色。原来前端有自己一套 rowKind()，
 * 和后端各算各的，迟早分叉且分叉时不报错。
 */

/** 行结论。与后端 compare.Verdict 一一对应，不多不少。 */
export type Verdict = 'same' | 'diff' | 'missing' | 'unknown' | 'ignored'

/**
 * 🔴 配色与**导出的 Excel 严格一致**：同一件事在两个地方必须同一个颜色。
 *
 *   一致     绿
 *   不一致   红   ← 用户原话"不一致就爆红"
 *   缺失     黄
 *   无法判定 虚线灰
 *   已忽略   淡灰
 *
 * ⚠️ 「无法判定」不用红。对方 token 一过期整整一列都是它 ——
 * 画成红一眼看去像「对方全线故障」，而事实只是我们没拿到数据。
 * 也不能画成普通灰（会被当成中性事实一扫而过）：
 * 用**虚线边框**，灰色说明它不是故障，虚线说明这里缺了东西。
 *
 * ⚠️ 「已忽略」比「无法判定」更淡：它是唯一一个看到了也**什么都不用做**的态。
 */
export const CHIP: Record<Verdict, string> = {
  same: 'bg-success-bg text-success shadow-[inset_2px_0_0_var(--color-success)]',
  diff: 'bg-danger-bg text-danger shadow-[inset_2px_0_0_var(--color-danger)]',
  missing: 'bg-warning-bg text-warning shadow-[inset_2px_0_0_var(--color-warning)]',
  unknown: 'border border-dashed border-border-strong text-muted-foreground',
  ignored: 'text-muted-foreground/70',
}

/** 行首色带。unknown 用虚线质感（CSS 里画成断续条），其余是实心。 */
export const STRIPE: Record<Verdict, string> = {
  same: 'bg-success',
  diff: 'bg-danger',
  missing: 'bg-warning',
  unknown: 'ops-stripe-dashed',
  ignored: 'bg-border',
}

/** 统计条上的数字颜色。 */
export const STAT: Record<Verdict, string> = {
  same: 'text-success',
  diff: 'text-danger',
  missing: 'text-warning',
  unknown: 'text-muted-foreground',
  ignored: 'text-muted-foreground',
}

/**
 * 统计条的顺序：**要处理的排前面**。
 *
 * ⚠️ 与后端的判定优先级（缺失 > 不一致 > 无法判定 > 一致）不是一回事：
 * 那个决定"一行算哪一档"，这个只决定"统计条上谁排左边"。
 * 这里把 diff 放最前，因为它是最常见的行动项。
 */
export const VERDICT_ORDER: Verdict[] = ['diff', 'missing', 'unknown', 'same', 'ignored']

/**
 * 一格的状态。与后端 compare.CellState 一一对应。
 *
 * 🔴 三种空态**各有各的字**，不能都显示「—」：
 *
 *   —      这一列确实没有这个服务   → 找对方确认
 *   未采集  我们没采到               → 查我们自己的采集
 *   已忽略  主动决定不比             → 什么都不用做
 *
 * 都写「—」的话，对方 token 过期会被读成「对方把服务全下线了」——
 * 处理方向正好反了。
 */
export type CellState = 'version' | 'missing' | 'no_data' | 'unversioned' | 'conflict' | 'ignored'

/**
 * 归因的视觉表达。
 *
 * 🔴 配色的分工是刻意的，与判定色的分工同源：
 *   not_synced / sync_failed → 红：**是我们的锅**，对方想发都发不了
 *   synced                   → 灰：镜像到位了，差异的原因在对方，我们没什么要做的
 *   unknown                  → 虚线灰：我们不知道，去把复制规则绑上组织
 *
 * ⚠️ synced 刻意**不用绿**：绿会被读成「这一格没问题」，
 * 而它其实仍然是一个差异 —— 只是责任不在我们。判定色负责表达"有差异"，
 * 归因只回答"该找谁"，两套语义不能互相抢。
 */
export const SYNC_CHIP: Record<string, string> = {
  synced: 'bg-muted text-muted-foreground shadow-[inset_2px_0_0_var(--color-border-strong)]',
  sync_failed: 'bg-danger-bg text-danger shadow-[inset_2px_0_0_var(--color-danger)]',
  not_synced: 'bg-danger-bg text-danger shadow-[inset_2px_0_0_var(--color-danger)]',
  unknown: 'border border-dashed border-border-strong text-muted-foreground',
}
