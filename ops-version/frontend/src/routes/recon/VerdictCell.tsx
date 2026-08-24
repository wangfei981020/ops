import { useTranslation } from '@ops/i18n'
import type { Cell, CellState } from './types.js'
import { CHIP, SYNC_CHIP } from './verdict.js'

/**
 * 版本号渲染：把**构建号**加粗，其余降噪。
 *
 * `20260519082034-58ac8c3-114` 里人真正在比的只有尾部那个 114。
 * 整串同一个字重会逼人逐字符读 —— 这张表一屏有几十个版本号，
 * 逐字符读和一眼扫过去是完全不同的使用体验。
 */
function Version({ tag }: { tag: string }) {
  const i = tag.lastIndexOf('-')
  // 没有分隔符（stable、latest 这类）时整串照原样，不硬拆
  if (i < 0 || i === tag.length - 1) {
    return <span className="font-mono text-xs text-muted-foreground">{tag}</span>
  }
  return (
    <span className="font-mono text-xs whitespace-nowrap text-muted-foreground">
      {tag.slice(0, i + 1)}
      <span className="font-semibold text-foreground">{tag.slice(i + 1)}</span>
    </span>
  )
}

/**
 * 一格的占位符。
 *
 * 🔴 三种空态**各有各的字**，不能都显示「—」：
 *
 *   —      这一列确实没有这个服务   → 找对方确认
 *   未采集  我们没采到               → 查我们自己的采集
 *   已忽略  主动决定不比             → 什么都不用做
 *
 * 都写「—」的话，对方 token 过期会被读成「对方把服务全下线了」——
 * 处理方向正好反了。导出的 Excel 里用的是同一套字，两边必须一致。
 */
function Placeholder({ state }: { state: CellState }) {
  const { t } = useTranslation()
  if (state === 'no_data' || state === 'ignored') {
    return (
      <span className="font-mono text-xs text-muted-foreground italic">
        {t(`opsversion:cellState.${state}`)}
      </span>
    )
  }
  return <span className="font-mono text-xs text-muted-foreground">—</span>
}

/** 把 Note 里的「发布中：…」那一段去掉，其余原样保留。 */
function noteWithoutDeploying(note: string): string {
  return note
    .split('；')
    .filter((seg) => !seg.startsWith('发布中'))
    .join('；')
}

export function VerdictCell({ cell, columnFailed }: { cell: Cell; columnFailed?: boolean }) {
  const { t } = useTranslation()
  const tag = cell.Snap?.Tag

  return (
    <div className="flex flex-col gap-0.5 py-0.5">
      {/* ⚠️ 非版本化 tag / 同名冲突**照样显示版本号** —— 它是真的，
          只是不能拿来判定是否同一制品。下面的标记会说明为什么。 */}
      {tag ? <Version tag={tag} /> : <Placeholder state={cell.State} />}

      {/* 🔴 格子上**不再画行结论** —— 结论是整行的事（行首色带 + 结论列）。
          这里只标"这一格为什么不能拿来比"，那是逐格不同的信息。 */}
      {(cell.State === 'unversioned' || cell.State === 'conflict') && (
        <span
          className={`inline-flex w-fit items-center rounded-[2px] py-px pr-[5px] pl-1 text-[10.5px] leading-[1.45] font-semibold ${
            cell.State === 'conflict' ? CHIP.diff : CHIP.unknown
          }`}
        >
          {t(`opsversion:cellState.${cell.State}`)}
        </span>
      )}

      {/* 发布中是附加标记：一个服务可以既「一致」又「正在滚动更新」。
          只看声明的 tag 会把「YAML 改了但一个 pod 都没起来」显示成已升级 */}
      {cell.Deploying && cell.Snap && (
        <span className="inline-flex w-fit items-center rounded-[2px] bg-info-bg py-px pr-[5px] pl-1 text-[10.5px] font-semibold text-info shadow-[inset_2px_0_0_var(--color-info)]">
          {t('opsversion:verdict.deploying')}
        </span>
      )}
      {cell.Deploying && cell.Snap && (
        <span className="font-mono text-[10.5px] text-muted-foreground">
          {cell.Snap.Tag} → {cell.Snap.RunningTag}
        </span>
      )}

      {/* 归因：这个差异该找谁。
          🔴 只在有差异时出现 —— 一致的格子标一个「已同步」纯属噪音 */}
      {cell.Sync && (
        <span
          className={`inline-flex w-fit items-center rounded-[2px] py-px pr-[5px] pl-1 text-[10.5px] font-semibold ${SYNC_CHIP[cell.Sync] ?? SYNC_CHIP.unknown}`}
          title={cell.SyncNote}
        >
          {t(`opsversion:syncAttr.${cell.Sync}`)}
        </span>
      )}

      {/* 🔴 说明文字只有 conflict 用红。
          no_data / unknown 的说明是「为什么判不了」，不是故障描述 —— 用红会让
          一列 token 过期看起来像对方全线崩溃。

          ⚠️ 整列采集失败时**不再逐格重复原因**：那句话对整列都一样，
          顶部横幅已经说过一次，在 100 行里再印 100 遍只会把真正逐格不同的
          信息（冲突详情、发布中的实跑版本）淹掉。格子里保留标记，
          让人知道这一格没有结论；原因去看横幅。 */}
      {/* ⚠️ 「发布中」那一段从 Note 里剔掉：上面已经有标记 + 箭头行说过两遍了，
          Note 里再写一遍完整的「声明 X，实跑 Y」是第三遍。
          剔的是**这一段**而不是整个 Note —— Note 里可能还有别的信息
          （冲突详情之类），整条藏掉会把那些一起弄丢。
          ⚠️ 导出和 MCP 拿到的 Note 仍然完整：那两处没有箭头行，需要文字。 */}
      {noteWithoutDeploying(cell.Note) && !(columnFailed && cell.State === 'no_data') && (
        <span
          className={`text-[10.5px] leading-[1.4] ${
            cell.State === 'conflict' ? 'text-danger' : 'text-muted-foreground'
          }`}
        >
          {noteWithoutDeploying(cell.Note)}
        </span>
      )}
    </div>
  )
}
