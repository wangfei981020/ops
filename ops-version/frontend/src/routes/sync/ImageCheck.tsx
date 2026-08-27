import { useTranslation } from '@ops/i18n'
import { Badge, Button, MultiSelect, Select } from '@ops/ui'
import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { api } from '../../lib/api.js'
import type { Org } from '../orgs/types.js'
import type { Policy, PolicyService, SyncTaskRow } from './types.js'

/**
 * 按服务查「这些版本推过去了没有」。
 *
 * 🔴 这是**推断不是实测**，界面上必须说清 —— 我们没有对方 Harbor 的账号，
 * 只能拿「我方 Harbor 里有这个 tag」+「复制记录里有它的成功任务」来推。
 * 让人以为我们真去对方那边看过，会在两种情况下害了他：
 * 复制记录被清理（明明推过却显示未同步）、对方手工删了镜像（明明没了却显示已同步）。
 */
export function ImageCheck() {
  const { t } = useTranslation()
  // 🔴 流程从「Harbor → 项目 → 范围 → 服务」缩成「规则 → 服务」。
  //
  //    项目那一步只是为了「拿我方 Harbor 里的全部 tag」，
  //    而「推过去了没有」完全来自我们自己的库 —— 跟项目无关。
  //    让人先猜一个项目，猜错了下拉里就没有他要的服务
  //    （实测：选了 appA 之后想查的服务根本不在里面）。
  //
  //    规则本身就定义了覆盖范围，列出来的每一个服务**一定有同步状态可看**。
  const [policyId, setPolicyId] = useState('')
  const [picked, setPicked] = useState<string[]>([])
  const [result, setResult] = useState<SyncTaskRow[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const policies = useQuery({
    queryKey: ['policies'],
    queryFn: () => api<Policy[]>('/api/sync/policies'),
  })

  // 服务清单来自**复制记录**，不是 Harbor 仓库列表 ——
  // 列出来的每一个都真的被推过，一定有状态可看
  const services = useQuery({
    queryKey: ['policyServices', policyId],
    queryFn: () => api<PolicyService[]>(`/api/sync/policies/${policyId || 0}/services`),
    retry: false,
  })

  const options = useMemo(
    () =>
      (services.data ?? []).map((r) => ({
        value: r.service_key,
        label: r.service_key,
        count: r.tags,
      })),
    [services.data],
  )

  // 🔴 只列**已绑定平台**的规则。
  //
  //    没绑定的规则答不了这个页面的问题 —— 「推给谁」那一列会是空的，
  //    而这个页面回答的正是「某个版本推给某方了没有」。
  //    把它们摆在下拉里，选中之后只会得到一屏没有接收方的记录。
  //
  // ⚠️ 不是静默丢弃：下面会说清"有几条没绑定、去哪绑"，
  //    否则人会以为规则丢了，或者对着少掉的选项猜。
  const bound = useMemo(() => (policies.data ?? []).filter((p) => p.org_name), [policies.data])
  const unboundCount = (policies.data ?? []).length - bound.length

  async function run() {
    setErr('')
    if (picked.length === 0) {
      setErr(t('opsversion:imgcheck.needService'))
      return
    }
    setBusy(true)
    try {
      const q = new URLSearchParams({ services: picked.join(','), limit: '200' })
      if (policyId) q.set('policy', policyId)
      const r = await api<SyncTaskRow[]>(`/api/sync/tasks?${q}`)
      setResult(r)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div>
      <div className="mb-1.5 flex flex-wrap items-center gap-2">
        <span className="text-sm font-semibold text-foreground">{t('opsversion:imgcheck.title')}</span>
        <span className="text-[11px] text-muted-foreground">{t('opsversion:imgcheck.hint')}</span>
      </div>

      {/* 🔴 说清这是推断不是实测。放在最上面而不是脚注 —— 脚注没人看 */}
      <div className="mb-2 rounded-md border border-dashed border-border-strong px-3 py-2 text-[11px] text-muted-foreground">
        {t('opsversion:imgcheck.inferenceWarning')}
      </div>

      {err && (
        <div className="mb-2 rounded-md border border-danger bg-danger-bg px-3 py-2 text-xs text-danger">
          {err}
        </div>
      )}

      <div className="mb-2 flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-3">
        {/* 只有两步：选规则（可不选=全部）、选服务。
            原来是 Harbor → 项目 → 比对范围 → 服务四步，而前两步只是为了
            拿我方 Harbor 的 tag 列表 —— 跟「推过去了没有」无关。 */}
        <Select
          label={t('opsversion:imgcheck.policy')}
          value={policyId}
          onChange={(v) => {
            setPolicyId(v)
            setPicked([])
            setResult(null)
          }}
          options={[
            { value: '', label: t('opsversion:imgcheck.anyPolicy') },
            ...bound.map((p) => ({
              value: String(p.id),
              label: `${p.name} → ${p.org_name}`,
            })),
          ]}
        />
        {/* ⚠️ 少掉的选项要交代去向，否则人会以为规则丢了 */}
        {unboundCount > 0 && (
          <span className="text-[11px] text-muted-foreground">
            {t('opsversion:imgcheck.unboundHidden', { n: unboundCount })}
          </span>
        )}
        <MultiSelect
          label={t('opsversion:imgcheck.services')}
          value={picked}
          onChange={setPicked}
          options={options}
          placeholder={t('opsversion:imgcheck.pickServices')}
          summary={(n, total) => t('opsversion:imgcheck.picked', { n, total })}
          searchPlaceholder={t('opsversion:imgcheck.filterPh')}
          clearLabel={t('opsversion:imgcheck.clear')}
          selectAllLabel={t('opsversion:imgcheck.selectAll')}
          emptyLabel={t('opsversion:imgcheck.noMatch')}
          max={20}
        />
        {/* 🔴 失败态不能只给四个字：说清为什么、并给重试 */}
        {services.isError && (
          <span className="flex flex-wrap items-center gap-1.5 text-[11px] text-danger">
            <span>
              {t('opsversion:imgcheck.reposFailed')}
              {'：'}
              {(services.error as Error)?.message || t('opsversion:imgcheck.reposFailedUnknown')}
            </span>
            <Button size="sm" variant="ghost" onClick={() => services.refetch()}>
              {t('common:action.retry')}
            </Button>
          </span>
        )}
        {/* ⚠️ 一个服务都列不出来时要说清是**没有复制记录**，
            而不是让人对着空下拉猜。这跟「查不到 ≠ 事实是否定的」是同一条。 */}
        {!services.isError && (services.data ?? []).length === 0 && !services.isLoading && (
          <span className="text-[11px] text-muted-foreground">
            {t('opsversion:imgcheck.noServices')}
          </span>
        )}
        <div className="flex-1" />
        <Button variant="primary" onClick={run} disabled={busy}>
          {busy ? t('opsversion:imgcheck.checking') : t('opsversion:imgcheck.check')}
        </Button>
      </div>

      {result && (
        result.length === 0 ? (
          // 🔴 「查出来是空」必须说清是**没有推送记录**，
          //    不能留一片空白让人以为页面坏了
          <div className="rounded-lg border border-dashed border-border-strong px-4 py-6 text-center text-xs text-muted-foreground">
            {t('opsversion:imgcheck.noTasks')}
          </div>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-border bg-card">
            <table className="w-full min-w-[720px] border-collapse text-[11px]">
              <thead>
                <tr className="border-b border-border bg-secondary">
                  <th className="px-3 py-2 text-left font-medium">{t('opsversion:imgcheck.colService')}</th>
                  <th className="px-3 py-2 text-left font-medium">{t('opsversion:imgcheck.colTag')}</th>
                  <th className="px-3 py-2 text-left font-medium">{t('opsversion:imgcheck.colTo')}</th>
                  <th className="px-3 py-2 text-left font-medium">{t('opsversion:imgcheck.colState')}</th>
                  <th className="px-3 py-2 text-left font-medium">{t('opsversion:imgcheck.colWhen')}</th>
                </tr>
              </thead>
              <tbody>
                {result.map((r, i) => (
                  <tr key={`${r.service_key}-${r.tag}-${i}`} className="border-b border-border last:border-b-0">
                    <td className="px-3 py-1.5 font-mono whitespace-nowrap text-foreground">
                      {r.service_key}
                    </td>
                    {/* 🔴 空白 ≠ 没版本。Harbor 按仓库复制时，task 的 resource 写的是
                        `repo [3 item(s) in total]`，**不带具体 tag** —— 实测过
                        143 条复制记录 tag 100% 为空。留白的话人会以为是漏渲染，
                        或者更糟：以为"推的是空版本"。说出来它为什么没有。 */}
                    <td className="px-3 py-1.5 font-mono whitespace-nowrap text-foreground">
                      {r.tag || (
                        <span
                          className="font-sans text-[11px] text-muted-foreground"
                          title={t('opsversion:sync.noTagHint')}
                        >
                          {t('opsversion:sync.noTag')}
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-1.5 whitespace-nowrap text-muted-foreground">
                      {r.org_name || r.policy_name}
                    </td>
                    <td className="px-3 py-1.5">
                      <Badge tone={/succe/i.test(r.status) ? 'ok' : 'bad'}>{r.status}</Badge>
                      {r.err_msg && <span className="ml-1.5 text-danger">{r.err_msg}</span>}
                    </td>
                    <td className="px-3 py-1.5 whitespace-nowrap text-muted-foreground">
                      {r.finished_at ? new Date(r.finished_at).toLocaleString() : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )
      )}
    </div>
  )
}
