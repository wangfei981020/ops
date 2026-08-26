import { useTranslation } from '@ops/i18n'
import { Badge, type BadgeTone, Button, MultiSelect, Select } from '@ops/ui'
import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { api } from '../../lib/api.js'
import type { Org } from '../orgs/types.js'
import type { Policy, PolicyService, SyncTaskRow } from './types.js'

interface HarborProject {
  name: string
  repo_count: number
}
interface HarborRepo {
  full_name: string
  service_key: string
  artifact_count: number
}
interface TagCheck {
  tag: string
  pushed_at: string
  state: string
  note: string
  err_msg: string
}
interface ServiceCheck {
  service_key: string
  full_name: string
  tags: TagCheck[]
  /** 🔴 与「tags 为空」分开：这是「我们没查成 / 服务不存在」，不是「没有版本」 */
  err: string
}

/**
 * 深入视图一次看多少个版本。
 *
 * 🔴 每个版本都要打一次 Harbor 的 artifacts 接口，不能不设上限。
 *    但**设了上限就必须说出来** —— 不说的话，人看到 10 行会当成"我方一共就这些版本"，
 *    然后据此得出"更早的都推过了"这种正好相反的结论。
 */
const DEEP_TAG_LIMIT = 10

const STATE_TONE: Record<string, BadgeTone> = {
  synced: 'ok',
  not_synced: 'bad',
  sync_failed: 'bad',
}

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
  // 🔴 「和我方 Harbor 逐版本比对」做成**可展开的深入视图**，不是必经步骤。
  //
  //    常见问题（推过去了没有）两步就答完；
  //    深入问题（我方有 v5、只推到 v3）点开才查 —— 那一层要打 Harbor，慢且贵。
  // ⚠️ 项目**不再让人选**：从我方快照的 image_repo 推（见后端 ProjectOfService）。
  const [deepOf, setDeepOf] = useState<string | null>(null)
  const [deep, setDeep] = useState<ServiceCheck[] | null>(null)
  const [deepBusy, setDeepBusy] = useState(false)
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

  async function runDeep(serviceKey: string) {
    if (deepOf === serviceKey) {
      setDeepOf(null)
      setDeep(null)
      return
    }
    setDeepOf(serviceKey)
    setDeep(null)
    setDeepBusy(true)
    setErr('')
    try {
      // project 不传 —— 后端从我方快照的 image_repo 推
      const r = await api<ServiceCheck[]>('/api/images/check', {
        method: 'POST',
        body: JSON.stringify({ services: [serviceKey], tag_limit: DEEP_TAG_LIMIT }),
      })
      setDeep(r)
    } catch (e) {
      setErr((e as Error).message)
      setDeepOf(null)
    } finally {
      setDeepBusy(false)
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
            ...(policies.data ?? []).map((p) => ({
              value: String(p.id),
              label: p.org_name ? `${p.name} → ${p.org_name}` : `${p.name}（未绑定）`,
            })),
          ]}
        />
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
                      {/* 🔴 深入视图做成**这一行旁边的一个动作**，不是必经步骤。
                          只在该服务的第一行显示，否则同一个服务的每个版本旁边
                          都挂一个按钮，一屏全是按钮。 */}
                      {result.findIndex((x) => x.service_key === r.service_key) === i && (
                        <button
                          type="button"
                          onClick={() => runDeep(r.service_key)}
                          className="ml-2 cursor-pointer text-[11px] text-muted-foreground underline-offset-2 hover:text-brand hover:underline"
                        >
                          {deepOf === r.service_key
                            ? t('opsversion:imgcheck.collapse')
                            : t('opsversion:imgcheck.deep')}
                        </button>
                      )}
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
                {/* 展开的深入视图：我方 Harbor 有哪些版本、各自推没推过去 */}
                {deepOf && (
                  <tr>
                    {/* ⚠️ 视觉上必须一眼看出这是"上面某一行展开出来的"，
                        而不是又一张平级的表 —— 左边一条 brand 竖线 + 内缩 + 实底色。
                        原来只给了半透明底，在浅色主题下几乎和白底一样。 */}
                    <td
                      colSpan={5}
                      className="border-l-2 border-brand bg-secondary py-2 pr-3 pl-6"
                    >
                      {deepBusy ? (
                        <span className="text-[11px] text-muted-foreground">
                          {t('opsversion:imgcheck.deepLoading', { svc: deepOf })}
                        </span>
                      ) : (
                        (deep ?? []).map((sc) => (
                          <div key={sc.service_key}>
                            <div className="mb-1 text-[11px] text-muted-foreground">
                              {t('opsversion:imgcheck.deepTitle', {
                                svc: sc.service_key,
                                repo: sc.full_name,
                              })}
                            </div>
                            {/* 🔴 err 与「没有版本」分开：前者是我们没查成或名字错了 */}
                            {/* 🔴 到了上限就说清楚，别让人把"最近 10 个"当成"一共 10 个" */}
                            {!sc.err && sc.tags.length >= DEEP_TAG_LIMIT && (
                              <div className="mb-1 text-[11px] text-warning">
                                {t('opsversion:imgcheck.deepCapped', { n: DEEP_TAG_LIMIT })}
                              </div>
                            )}
                            {sc.err ? (
                              <div className="text-[11px] text-danger">{sc.err}</div>
                            ) : (
                              <table className="w-full border-collapse text-[11px]">
                                <tbody>
                                  {sc.tags.map((tg) => (
                                    <tr key={tg.tag} className="border-b border-border last:border-b-0">
                                      <td className="py-1 pr-3 font-mono whitespace-nowrap text-foreground">
                                        {tg.tag}
                                      </td>
                                      <td className="py-1 pr-3 whitespace-nowrap text-muted-foreground">
                                        {tg.pushed_at || '—'}
                                      </td>
                                      <td className="py-1 pr-3">
                                        <Badge tone={STATE_TONE[tg.state] ?? 'mute'}>
                                          {t(`opsversion:imgcheck.state.${tg.state}`)}
                                        </Badge>
                                      </td>
                                      <td className="py-1 text-muted-foreground">
                                        {tg.note}
                                        {tg.err_msg && (
                                          <span className="text-danger"> · {tg.err_msg}</span>
                                        )}
                                      </td>
                                    </tr>
                                  ))}
                                </tbody>
                              </table>
                            )}
                          </div>
                        ))
                      )}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        )
      )}
    </div>
  )
}
