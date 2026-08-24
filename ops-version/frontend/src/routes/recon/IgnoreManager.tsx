import { useTranslation } from '@ops/i18n'
import { Badge, Button, Dialog } from '@ops/ui'
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
  onUnignoreService,
  onUnignoreCell,
  onClearAll,
  onClose,
}: {
  open: boolean
  ignores: IgnoreSet
  /** StableKey → 给人看的列名 */
  labelOf: (colKey: string) => string
  onUnignoreService: (name: string) => void
  onUnignoreCell: (service: string, colKey: string) => void
  onClearAll: () => void
  onClose: () => void
}) {
  const { t } = useTranslation()
  const cellEntries = Object.entries(ignores.cells).filter(([, v]) => v.length > 0)
  const nothing = ignores.services.length === 0 && cellEntries.length === 0

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
