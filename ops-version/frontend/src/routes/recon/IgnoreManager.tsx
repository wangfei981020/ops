import { useTranslation } from '@ops/i18n'
import { Badge, Button, Dialog } from '@ops/ui'
import { useState } from 'react'
import type { IgnoreSet } from './useIgnores.js'

/**
 * 忽略规则管理。
 *
 * 🔴 存在的理由就一个：**能解除**。
 * 对方以后可能上线这个服务，那时不该逼人重建整个方案 ——
 * 「点一下就永久消失」是不可接受的语义。
 *
 * ⚠️ 列标识（StableKey）是 `orgID/projectID/env` 这种机器串，
 *    不能直接摆给人看。要翻成「平台·项目/环境」，翻不出来才退回原串
 *    （方案里引用了一个已删掉的平台时会这样，那时显示原串至少还能对照）。
 */
export function IgnoreManager({
  open,
  ignores,
  labelOf,
  hiddenNames,
  onAddService,
  onUnignoreService,
  onUnignoreCell,
  onClearAll,
  onClose,
}: {
  open: boolean
  ignores: IgnoreSet
  /** StableKey → 给人看的列名 */
  labelOf: (colKey: string) => string
  /**
   * 手动添加一条整行忽略，支持 * 通配。
   *
   * 🔴 光有"从结果里勾选"是不够的：想排掉 `*-game-frontend` 这类一整批时，
   *    得先让它们出现在结果里、再一个个勾 —— 我方 那边是 67 个，没人会去勾。
   *    后端一直支持通配（IgnoreSet.IgnoredRow 走 matchService），
   *    只是界面没给填进去的地方。
   */
  onAddService: (pattern: string) => void
  /**
   * 这些规则**实际命中**了哪些服务（后端把通配展开后报回来的）。
   *
   * 🔴 只显示规则是不够的：`*-game-frontend` 到底吃掉了 2 个还是 79 个，
   *    光看规则永远不知道。而这张表是拿去跟客户对账的 ——
   *    "某个服务为什么不在表上"必须随时答得出来。
   */
  hiddenNames: string[]
  onUnignoreService: (name: string) => void
  onUnignoreCell: (service: string, colKey: string) => void
  onClearAll: () => void
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState('')
  // 加完给一句明确回执 —— 不然「加进去了没有」只能靠自己去下面列表里数
  const [justAdded, setJustAdded] = useState(0)
  const cellEntries = Object.entries(ignores.cells).filter(([, v]) => v.length > 0)
  const nothing = ignores.services.length === 0 && cellEntries.length === 0

  const add = () => {
    // 一次可以贴多条 —— 从别处整理好的清单直接贴进来，一条条敲 67 次没人会做。
    //
    // 🔴 制表符是**列**分隔符，不是"另一条规则"的分隔符 —— 这两件事必须分开。
    //
    //    从 Excel 选中多列复制过来，一行是 `服务名\t版本号\t结论`：
    //      · 只按换行切 → 整行成一条规则，永远匹配不上任何服务，
    //        加得进去、列表里也看得见，但什么都忽略不掉（"加了没反应"）
    //      · 把 \t 也当条目分隔 → 版本号和「未采集」全变成规则，
    //        列表里冒出一堆 `20260820-095`、`未采集` 这种垃圾（实测就是这样）
    //
    //    正确做法：先按行切，每行**只取第一列**。
    //
    // ⚠️ \r 也要认：Excel / Windows 复制出来的换行是 \r\n。
    const parts = draft
      .split(/[\n\r]+/)
      // 手打时习惯用逗号/分号分隔多条，这一层照顾这种写法
      .flatMap((line) => line.split(/[,;]/))
      // 制表符之后是表格的其余列，丢掉
      .map((x) => (x.split('\t')[0] ?? '').trim())
      .filter(Boolean)
    if (parts.length === 0) return
    for (const p of parts) onAddService(p)
    setDraft('')
    setJustAdded(parts.length)
  }

  return (
    <Dialog
      open={open}
      title={t('opsversion:ignore.manageTitle')}
      onClose={onClose}
      closeLabel={t('common:action.close')}
      footer={
        <>
          {!nothing && (
            <Button onClick={onClearAll}>{t('opsversion:ignore.clearAll')}</Button>
          )}
          <Button variant="primary" onClick={onClose}>
            {t('common:action.close')}
          </Button>
        </>
      }
    >
      {/* 🔴 添加入口放在空态判断**之外**：一条规则都没有的时候恰恰最需要它，
          放进 else 分支就成了"必须先有一条才能加第二条"。 */}
      <section className="mb-4">
        <div className="mb-1.5 text-xs font-semibold text-foreground">
          {t('opsversion:ignore.addSection')}
        </div>
        <div className="mb-1.5 text-[11px] leading-relaxed text-muted-foreground">
          {t('opsversion:ignore.addHint')}
        </div>
        <div className="flex items-start gap-2">
          <textarea
            // 🔴 最少 3 行。placeholder 本身是两行示例，而框只有 1 行高时
            //    只露出第一行，看着就像"框里已经有内容了" —— 实测有人因此
            //    以为规则已经填好、点不动「添加」（框其实是空的，按钮自然是禁用态）。
            rows={Math.min(6, Math.max(3, draft.split('\n').length))}
            value={draft}
            placeholder={t('opsversion:ignore.addPlaceholder')}
            onChange={(e) => {
              setDraft(e.target.value)
              setJustAdded(0)
            }}
            onKeyDown={(e) => {
              // Enter 提交、Shift+Enter 换行 —— 贴多行清单时不能一按回车就提交
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                add()
              }
            }}
            className="w-full resize-y rounded-md border border-input bg-background px-2.5 py-1.5 font-mono text-[11px] text-foreground"
          />
          <Button variant="primary" disabled={draft.trim() === ''} onClick={add}>
            {t('opsversion:ignore.add')}
          </Button>
        </div>
        {justAdded > 0 && (
          <div className="mt-1.5 text-[11px] text-success">
            {t('opsversion:ignore.added', { n: justAdded })}
          </div>
        )}
      </section>

      {/* 被规则命中的完整清单。规则是「意图」，这里是「后果」，两个都要看得见。 */}
      {hiddenNames.length > 0 && (
        <section className="mb-4">
          <div className="mb-1.5 text-xs font-semibold text-foreground">
            {t('opsversion:ignore.hitSection', { n: hiddenNames.length })}
          </div>
          <div className="mb-1.5 text-[11px] text-muted-foreground">
            {t('opsversion:ignore.hitHint')}
          </div>
          {/* 79 个服务名要能一眼扫完，也不能把弹窗撑爆 —— 多列 + 限高滚动 */}
          <div className="max-h-52 overflow-y-auto rounded-md border border-border bg-background p-2">
            <ul className="grid gap-x-4 gap-y-0.5 sm:grid-cols-2 lg:grid-cols-3">
              {hiddenNames.map((n) => (
                <li key={n} className="truncate font-mono text-[11px] text-muted-foreground" title={n}>
                  {n}
                </li>
              ))}
            </ul>
          </div>
        </section>
      )}

      {nothing ? (
        <p className="text-xs text-muted-foreground">{t('opsversion:ignore.none')}</p>
      ) : (
        <div className="space-y-4">
          {ignores.services.length > 0 && (
            <section>
              <div className="mb-1.5 text-xs font-semibold text-foreground">
                {t('opsversion:ignore.rowSection', { n: ignores.services.length })}
              </div>
              <div className="mb-1.5 text-[11px] text-muted-foreground">
                {t('opsversion:ignore.rowHint')}
              </div>
              <ul className="space-y-1">
                {ignores.services.map((s) => (
                  <li
                    key={s}
                    className="flex items-center gap-2 rounded-md border border-border bg-background px-2.5 py-1.5"
                  >
                    <span className="font-mono text-[11px] text-foreground">{s}</span>
                    {s.includes('*') && <Badge tone="info">{t('opsversion:ignore.wildcard')}</Badge>}
                    <div className="flex-1" />
                    <Button size="sm" variant="ghost" onClick={() => onUnignoreService(s)}>
                      {t('opsversion:ignore.remove')}
                    </Button>
                  </li>
                ))}
              </ul>
            </section>
          )}

          {cellEntries.length > 0 && (
            <section>
              <div className="mb-1.5 text-xs font-semibold text-foreground">
                {t('opsversion:ignore.cellSection', {
                  n: cellEntries.reduce((a, [, v]) => a + v.length, 0),
                })}
              </div>
              <div className="mb-1.5 text-[11px] text-muted-foreground">
                {t('opsversion:ignore.cellHint')}
              </div>
              <ul className="space-y-1">
                {cellEntries.map(([svc, colKeys]) => (
                  <li
                    key={svc}
                    className="rounded-md border border-border bg-background px-2.5 py-1.5"
                  >
                    <div className="font-mono text-[11px] text-foreground">{svc}</div>
                    <div className="mt-1 flex flex-wrap gap-1.5">
                      {colKeys.map((k) => (
                        <span
                          key={k}
                          className="inline-flex items-center gap-1 rounded border border-border px-1.5 py-0.5 text-[11px] text-muted-foreground"
                        >
                          {labelOf(k)}
                          <button
                            type="button"
                            aria-label={t('opsversion:ignore.remove')}
                            onClick={() => onUnignoreCell(svc, k)}
                            className="cursor-pointer text-muted-foreground hover:text-danger"
                          >
                            ×
                          </button>
                        </span>
                      ))}
                    </div>
                  </li>
                ))}
              </ul>
            </section>
          )}

          {/* ⚠️ 必须说清「改完要重新比对」——
              忽略是在请求里带的，不点重比的话表格还是上一次的结果，
              人会以为「解除没生效」。 */}
          <p className="text-[11px] text-muted-foreground">{t('opsversion:ignore.recompareHint')}</p>
        </div>
      )}
    </Dialog>
  )
}
