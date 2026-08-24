import { useTranslation } from '@ops/i18n'
import { AsyncBoundary, Badge, Dialog, Skeleton, fromQuery } from '@ops/ui'
import { useQuery } from '@tanstack/react-query'
import { api, toLoadError } from '../../lib/api.js'
import { type Row, respColKey } from './types.js'
import { CHIP } from './verdict.js'

interface Change {
  org_id: number
  org_name: string
  env: string
  service_key: string
  old_tag: string
  new_tag: string
  change_type: string
  changed_at: string
}

/**
 * 服务钻取：一行在矩阵里只放得下 tag 和判定，
 * 但判断「该不该管这个差异」还需要三样东西 ——
 * 各列各自的 ns / workload / 实跑版本、以及**这个版本是什么时候上的**。
 *
 * 🔴 变更历史是「落后多少天」的唯一依据。没有它，
 * 「落后 4 个版本」既可能是昨天刚发的、也可能是卡了半年 —— 处理优先级完全不同。
 */
export function ServiceDrill({ row, onClose }: { row: Row; onClose: () => void }) {
  const { t } = useTranslation()

  const changes = useQuery({
    queryKey: ['changes', row.ServiceKey],
    queryFn: () =>
      api<Change[]>(`/api/changes?service_key=${encodeURIComponent(row.ServiceKey)}&limit=50`),
  })

  const state = fromQuery(
    changes,
    (d) => d.length === 0,
    (e) => toLoadError(e, t),
  )

  return (
    <Dialog
      open
      onClose={onClose}
      title={row.ServiceKey}
      description={t('opsversion:drill.hint')}
      closeLabel={t('common:action.close')}
      width={880}
      footer={null}
    >
      <div className="flex flex-col gap-4">
        {/* 各列的完整信息。矩阵里放不下的 ns / workload / 实跑版本都在这里 */}
        <div>
          <div className="mb-1.5 text-xs font-semibold text-foreground">
            {t('opsversion:drill.perColumn')}
          </div>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[560px] border-collapse text-xs">
              <thead>
                <tr className="bg-muted text-[10.5px] text-muted-foreground">
                  <th className="px-2 py-1.5 text-left font-semibold">
                    {t('opsversion:recon.column')}
                  </th>
                  <th className="px-2 py-1.5 text-left font-semibold">
                    {t('opsversion:drill.tag')}
                  </th>
                  <th className="px-2 py-1.5 text-left font-semibold">
                    {t('opsversion:drill.running')}
                  </th>
                  <th className="px-2 py-1.5 text-left font-semibold">
                    {t('opsversion:drill.namespace')}
                  </th>
                  <th className="px-2 py-1.5 text-left font-semibold">
                    {t('opsversion:drill.workloads')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {row.Cells.map((c) => {
                  // 🔴 这里标的是**这一格的状态**（有没有、采没采到、能不能比），
                  //    不是行结论 —— 行结论是整行一个，标在弹窗标题上。
                  const bad = c.State === 'conflict'
                  return (
                    <tr key={respColKey(c.Column)} className="border-b border-border">
                      <td className="px-2 py-1.5">
                        <div className="text-foreground">
                          {c.Column.ProjectName
                            ? `${c.Column.OrgName}·${c.Column.ProjectName}`
                            : c.Column.OrgName}
                        </div>
                        <div className="text-[10.5px] text-muted-foreground">{c.Column.Env}</div>
                        {c.State !== 'version' && (
                          <span
                            className={`mt-0.5 inline-flex items-center rounded-[2px] py-px pr-[5px] pl-1 text-[10.5px] font-semibold ${
                              bad ? CHIP.diff : CHIP.unknown
                            }`}
                          >
                            {t(`opsversion:cellState.${c.State}`)}
                          </span>
                        )}
                      </td>
                      <td className="px-2 py-1.5 font-mono text-foreground">
                        {c.Snap?.Tag || '—'}
                      </td>
                      {/* 🔴 实跑版本单独一列：只看声明的 tag 会把
                          「YAML 改了但一个 pod 都没起来」显示成已升级 */}
                      <td className="px-2 py-1.5 font-mono">
                        {c.Snap?.RunningTag ? (
                          <span
                            className={
                              c.Snap.RunningTag !== c.Snap.Tag ? 'text-info' : 'text-muted-foreground'
                            }
                          >
                            {c.Snap.RunningTag}
                          </span>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </td>
                      <td className="px-2 py-1.5 font-mono text-muted-foreground">
                        {c.Snap?.Namespace || '—'}
                      </td>
                      <td className="px-2 py-1.5 text-muted-foreground">
                        {c.Snap?.Workloads?.length ? c.Snap.Workloads.join('、') : '—'}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>

        {/* 变更历史 */}
        <div>
          <div className="mb-1.5 text-xs font-semibold text-foreground">
            {t('opsversion:drill.history')}
          </div>
          <AsyncBoundary
            state={state}
            pending={<Skeleton className="h-24 w-full" />}
            empty={
              <div className="rounded-md border border-dashed border-border-strong px-3 py-4 text-center text-xs text-muted-foreground">
                {t('opsversion:drill.noHistory')}
              </div>
            }
            errorTitle={t('opsversion:drill.history')}
            retryLabel={t('common:action.retry')}
            onRetry={() => changes.refetch()}
          >
            {(list) => (
              <div className="max-h-64 overflow-y-auto">
                <table className="w-full border-collapse text-xs">
                  <tbody>
                    {list.map((c, i) => (
                      <tr key={`${c.org_id}-${c.env}-${c.changed_at}-${i}`} className="border-b border-border">
                        <td className="py-1.5 pr-2 whitespace-nowrap text-muted-foreground">
                          {new Date(c.changed_at).toLocaleString()}
                        </td>
                        <td className="py-1.5 pr-2 whitespace-nowrap text-muted-foreground">
                          {c.org_name} / {c.env}
                        </td>
                        <td className="py-1.5 pr-2">
                          <Badge tone={c.change_type === 'rollback' ? 'warn' : 'mute'}>
                            {t(`opsversion:changeType.${c.change_type}`)}
                          </Badge>
                        </td>
                        <td className="py-1.5 font-mono">
                          <span className="text-muted-foreground">{c.old_tag || '—'}</span>
                          <span className="px-1 text-muted-foreground">→</span>
                          <span className="text-foreground">{c.new_tag || '—'}</span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </AsyncBoundary>
        </div>
      </div>
    </Dialog>
  )
}
