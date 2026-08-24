import { useTranslation } from '@ops/i18n'
import {
  AsyncBoundary,
  Banner,
  Button,
  type ColumnDef,
  DataTable,
  Dialog,
  EmptyState,
  fromQuery,
  Select,
  TableSkeleton,
} from '@ops/ui'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useMemo, useState } from 'react'
import { api, download, toLoadError } from '../../lib/api.js'
import { can, type Session } from '../../lib/session.js'
import { IgnoreManager } from './IgnoreManager.js'
import { ServiceDrill } from './ServiceDrill.js'
import {
  type ColumnChoice,
  type CompareResult,
  colKey,
  colLabel,
  columnsOf,
  type Org,
  type Row,
  respColKey,
  stableOf,
} from './types.js'
import { loadFreshness, type RefreshOutcome, refreshColumns } from './useFreshness.js'
import { useIgnores } from './useIgnores.js'
import { VerdictCell } from './VerdictCell.js'
import { STAT, STRIPE, VERDICT_ORDER } from './verdict.js'

interface PlanResp {
  id: number
  name: string
  /** ⚠️ project_id 可选：加项目层之前存的方案里没有这个字段 */
  columns: { org_id: number; project_id?: number; env: string }[]
  /** ⚠️ 可能是 null：老方案没有忽略规则 */
  ignores: { services: string[] | null; cells: Record<string, string[]> | null } | null
  only_diff: boolean
}

/**
 * 解析服务白名单输入。逗号、空格、换行都当分隔符 ——
 * 人从别处粘一串服务名过来时，分隔符是什么完全看运气。
 */
function parseServices(s: string): string[] {
  return s
    .split(/[\s,，、]+/)
    .map((x) => x.trim())
    .filter(Boolean)
}

export function ReconPage({ session }: { session: Session }) {
  const { t } = useTranslation()
  const [picked, setPicked] = useState<Set<string> | null>(null)
  const [onlyDiff, setOnlyDiff] = useState(false)
  const [result, setResult] = useState<CompareResult | null>(null)
  // ─── 忽略 ───
  //
  // 🔴 忽略是**人为决定不比**，与「筛选」不是一回事：
  //    筛选是「这次先看这几个」，忽略是「这个格子本来就不该比」。
  //    所以忽略随方案存下来，筛选不存。
  const ig = useIgnores()
  const [ignoreOpen, setIgnoreOpen] = useState(false)
  // 批量忽略用的勾选。只在当前结果里有意义，重新比对后清空
  const [checked, setChecked] = useState<Set<string>>(new Set())
  // 当前套用的方案。空 = 未保存的临时组合
  const [planId, setPlanId] = useState('')
  const [saving, setSaving] = useState(false)
  const [planName, setPlanName] = useState('')
  const [confirmDelPlan, setConfirmDelPlan] = useState(false)
  const [err, setErr] = useState('')
  // 只比这些服务。按服务名（镜像名最后一段）—— 那是各平台唯一对得齐的东西，
  // 一份配置对所有平台生效，不用每家各配一遍
  const [svcFilter, setSvcFilter] = useState('')
  // 结果内的即时筛选。与上面那个「只比这些服务」是两回事：
  // 那个决定「查什么」（要重新请求），这个只是在已经拿到的结果里找 ——
  // 100 多行时想定位某个服务，重新查一次太慢也没必要
  const [keyword, setKeyword] = useState('')
  // 点统计条筛判定。null = 不筛
  const [verdictPick, setVerdictPick] = useState<string | null>(null)
  // 钻取：矩阵里放不下 ns/workload/实跑版本，更放不下变更历史
  const [drill, setDrill] = useState<Row | null>(null)
  const [exporting, setExporting] = useState(false)

  const qc = useQueryClient()
  const orgs = useQuery({
    queryKey: ['orgs'],
    queryFn: () => api<Org[]>('/api/orgs'),
  })
  const plans = useQuery({
    queryKey: ['plans'],
    queryFn: () => api<PlanResp[]>('/api/plans'),
  })

  const choices = useMemo(() => columnsOf(orgs.data ?? []), [orgs.data])
  // 默认全选。用 null 区分「还没初始化」与「用户主动取消了全部」
  const selected = picked ?? new Set(choices.map(colKey))

  // ─── 数据新鲜度 ───
  //
  // 🔴 比对/导出前自动刷新**过期的**列，而不是无条件全采：
  //    生产上一列 5–15 秒，6 列全采要一分钟以上，而多数时候只有一两列过期。
  // ⚠️ 刷新失败的列**不阻断**整次操作，但必须显式标出来 ——
  //    静默用旧数据的话，"已刷新"就成了假的，比不刷新更危险。
  // ⚠️ staleTime 让「点比对时顺手再拿一次」不至于紧接着又打一遍接口
  const freshQ = useQuery({
    queryKey: ['col-freshness'],
    queryFn: loadFreshness,
    staleTime: 10_000,
  })
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [progress, setProgress] = useState<{ done: number; total: number; current: string } | null>(
    null,
  )
  const [outcomes, setOutcomes] = useState<RefreshOutcome[]>([])

  /** 刷新选中列里过期的那些；返回失败的列（供界面标注） */
  async function refreshIfNeeded(
    cols: { orgId: number; projectId: number; env: string; orgName: string }[],
  ) {
    if (!autoRefresh) return [] as RefreshOutcome[]
    // 🔴 走 queryClient 而不是直接调 loadFreshness ——
    //    直接调的话，正在飞的那次请求不会被复用，同一份数据打两遍。
    //    fetchQuery 会等待进行中的请求，也会尊重 staleTime。
    const fresh = await qc.fetchQuery({
      queryKey: ['col-freshness'],
      queryFn: loadFreshness,
      staleTime: 10_000,
    })
    const res = await refreshColumns(cols, fresh, (done, total, current) =>
      setProgress({ done, total, current }),
    )
    setProgress(null)
    setOutcomes(res)
    qc.invalidateQueries({ queryKey: ['col-freshness'] })
    return res.filter((r) => r.state === 'failed')
  }

  /**
   * StableKey（orgID/projectID/env）翻成给人看的列名。
   *
   * ⚠️ 翻不出来时**退回原串**而不是显示空白 ——
   *    方案里引用了一个已删掉的平台时会这样，那时显示原串至少还能对照，
   *    显示空白则是一条谁也看不懂的规则，人只能整条删掉。
   */
  /**
   * 界面上要点名的「被整行忽略的服务」。
   *
   * 优先用**结果里真正命中的**（后端把通配展开后报回来的），
   * 没有结果时（还没比对）才退回规则本身 ——
   * 直接显示规则的话，`bi-*` 这种只会显示成一条星号，等于没说。
   */
  const hiddenNames = useMemo(
    () => (result?.ignored_rows?.length ? result.ignored_rows : ig.ignores.services),
    [result, ig.ignores.services],
  )

  const labelOfStable = useCallback(
    (k: string) => {
      const c = (result?.columns ?? []).find((x) => stableOf(x) === k)
      return c ? respColKey(c) : k
    },
    [result],
  )

  const compare = useMutation({
    mutationFn: async () => {
      const cols = choices.filter((c) => selected.has(colKey(c)))
      // 没有可比的列时直接拒绝，别发一个注定 400 的请求
      if (cols.length === 0) throw new Error('no column selected')
      await refreshIfNeeded(cols)
      return api<CompareResult>('/api/compare', {
        method: 'POST',
        body: JSON.stringify({
          columns: cols.map((c) => ({ org_id: c.orgId, project_id: c.projectId, env: c.env })),
          only_diff: onlyDiff,
          service_include: parseServices(svcFilter),
          ignores: ig.ignores,
        }),
      })
    },
    onSuccess: (r) => {
      setResult(r)
      // ⚠️ 勾选只对当前结果有意义。留着的话，勾着一批上一轮才存在的服务名
      // 去点批量忽略，会忽略掉屏幕上根本看不见的东西。
      setChecked(new Set())
    },
  })

  function applyPlan(id: string) {
    setPlanId(id)
    const p = plans.data?.find((x) => String(x.id) === id)
    if (!p) return
    // 🔴 老方案存的 JSON 里没有 project_id，解出来是 undefined。
    //    这里必须回落到「该环境自己挂的项目」，与 columnsOf / 后端 resolveProject 同一条规则 ——
    //    直接拼 `undefined` 或 0 进 key 的话，勾选状态一个都对不上，
    //    表现是「套用方案后一列都没选中」，而方案本身好好的。
    const planKey = (c: { org_id: number; project_id?: number; env: string }) => {
      const hit = choices.find(
        (x) =>
          x.orgId === c.org_id &&
          x.env === c.env &&
          (c.project_id ? x.projectId === c.project_id : true),
      )
      return hit ? colKey(hit) : `${c.org_id}/${c.project_id ?? 0}/${c.env}`
    }
    setPicked(new Set(p.columns.map(planKey)))
    setOnlyDiff(p.only_diff)
    // 忽略规则跟着方案回来 —— 这正是把它存进方案的意义。
    // ⚠️ 后端给的可能是 null（老方案没这个字段），必须兜底。
    ig.setIgnores({
      services: p.ignores?.services ?? [],
      cells: p.ignores?.cells ?? {},
    })
  }

  // 保存当前的列组合。id 为空 = 新建，否则覆盖那个方案。
  // 🔴 保存的是**选择**（哪些列、忽略了什么），不是某一次的对账结果 ——
  //    结果每次刷新都会变，存下来只会变成一份很快就骗人的旧数据。
  const savePlan = useMutation({
    mutationFn: async (mode: 'create' | 'update') => {
      const cols = choices.filter((c) => selected.has(colKey(c)))
      if (cols.length === 0) throw new Error(t('opsversion:recon.needColumn'))
      const name = mode === 'create' ? planName.trim() : (current?.name ?? '')
      if (!name) throw new Error(t('opsversion:recon.planNameRequired'))
      return api<{ id: number }>(mode === 'create' ? '/api/plans' : `/api/plans/${planId}`, {
        method: mode === 'create' ? 'POST' : 'PUT',
        body: JSON.stringify({
          name,
          columns: cols.map((c) => ({ org_id: c.orgId, project_id: c.projectId, env: c.env })),
          only_diff: onlyDiff,
          // 忽略规则随方案存 —— 这正是它存在的意义：
          // 下次套用这个方案，不必再把「对方不跑这套」重勾一遍
          ignores: ig.ignores,
        }),
      })
    },
    onSuccess: async (r) => {
      setSaving(false)
      setPlanName('')
      setErr('')
      await plans.refetch()
      if (r?.id) setPlanId(String(r.id))
    },
    onError: (e) => setErr((e as Error).message),
  })

  const deletePlan = useMutation({
    mutationFn: () => api(`/api/plans/${planId}`, { method: 'DELETE' }),
    onSuccess: async () => {
      setConfirmDelPlan(false)
      setPlanId('')
      setErr('')
      await plans.refetch()
    },
    onError: (e) => {
      setConfirmDelPlan(false)
      setErr((e as Error).message)
    },
  })

  const current = plans.data?.find((x) => String(x.id) === planId)

  // 🔴 导出**重新查一次**而不是把界面上已渲染的结果序列化：
  //    界面上的结果可能是几分钟前点「开始对账」时的，
  //    而导出的文件会被转发出去当结论用，必须是导出这一刻的数据。
  //    文件里的「数据时点」写的也是这次查询的时点，两者一致。
  // 当前是否处于筛选态，以及一句给导出文件用的说明
  //
  // 🔴 「只看差异」也是一种筛选，必须算进来。
  //    漏掉它的话：界面 71 行、导出 114 行，而「数据说明」页一个字都不提 ——
  //    收到附件的人会把这份全量当成筛过的，或者反过来，两种都会得出错的结论。
  //    （onlyDiff 是后端筛的，result.rows 本身就已经是筛过的，
  //      所以这里只要让它进入 filtering，导出就会跟着走可见行那条路。）
  const filtering = Boolean(keyword.trim() || verdictPick || onlyDiff)
  const filterDesc = [
    onlyDiff ? t('opsversion:recon.onlyDiffNote') : '',
    keyword.trim() ? `服务名含「${keyword.trim()}」` : '',
    verdictPick ? `判定为「${t(`opsversion:verdict.${verdictPick}`)}」` : '',
  ]
    .filter(Boolean)
    .join('，')

  async function doExport() {
    setExporting(true)
    setErr('')
    try {
      const cols = choices.filter((c) => selected.has(colKey(c)))
      if (cols.length < 2) throw new Error(t('opsversion:recon.needTwoColumns'))
      // 导出同样先刷 —— 导出的文件会被转发、存档，
      // 数据时点写在 Excel 第一页，但前提是这份数据本身足够新
      await refreshIfNeeded(cols)
      await download('/api/export', {
        method: 'POST',
        body: JSON.stringify({
          columns: cols.map((c) => ({ org_id: c.orgId, project_id: c.projectId, env: c.env })),
          only_diff: false,
          plan_name: current?.name ?? '',
          // 🔴 导出的必须是**界面上看到的那些**。
          //    界面筛到 14 行、导出却是 100 行的话，人拿着导出的表去开会，
          //    说的和看的对不上。有筛选时按可见行的服务名导，没筛选时用原白名单。
          service_include: filtering
            ? visibleRows.map((r) => r.ServiceKey)
            : parseServices(svcFilter),
          // 🔴 忽略同样要带。不带的话导出的表里会冒出界面上已经忽略掉的行 ——
          //    人拿着这份表去开会，指着一行问「这个怎么回事」，
          //    而那正是他自己标过「不用比」的。
          ignores: ig.ignores,
          filter_note: filtering ? filterDesc : '',
        }),
      })
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setExporting(false)
    }
  }

  const shown = choices.filter((c) => selected.has(colKey(c)))

  // 🔴 筛选只作用在**已渲染的结果**上，不重新请求。
  //    重新请求的话，筛一次等一次，而且服务端每次都要重算整张表。
  const allRows = result?.rows ?? []
  const visibleRows = useMemo(() => {
    const kw = keyword.trim().toLowerCase()
    return allRows.filter((r) => {
      if (kw && !r.ServiceKey.toLowerCase().includes(kw)) return false
      // 🔴 按**行结论**筛，不再逐格找。
      //    原来是"这一行有没有某种判定的格子"，而现在一行只有一个结论 ——
      //    点统计条上的「不一致 26」就该正好留下 26 行，
      //    逐格找的话会因为一行有多种格子而对不上。
      if (verdictPick && r.Verdict !== verdictPick) return false
      return true
    })
  }, [allRows, keyword, verdictPick])

  // 矩阵的列是**动态**的：一列服务名 + 每个参与对比的 (组织,环境)
  const columns = useMemo<ColumnDef<Row, unknown>[]>(() => {
    const head: ColumnDef<Row, unknown>[] = [
      {
        // 批量忽略用。⚠️ 只在**当前结果**里有意义 —— 重新比对后清空，
        // 否则勾着一批上一轮才存在的服务名，点批量忽略会忽略掉看不见的东西。
        id: 'pick',
        header: () => null,
        cell: ({ row }) => (
          <input
            type="checkbox"
            aria-label={row.original.ServiceKey}
            checked={checked.has(row.original.ServiceKey)}
            onChange={(e) => {
              setChecked((p) => {
                const n = new Set(p)
                if (e.target.checked) n.add(row.original.ServiceKey)
                else n.delete(row.original.ServiceKey)
                return n
              })
            }}
          />
        ),
      },
      {
        id: 'service',
        // 「=镜像名最后一段」原来每行重复一遍，挪到表头说一次就够
        header: () => (
          <span>
            {t('opsversion:recon.service')}
            <span className="ml-1 font-normal text-muted-foreground">
              {t('opsversion:recon.serviceHint')}
            </span>
          </span>
        ),
        accessorFn: (r) => r.ServiceKey,
        cell: ({ row }) => {
          // 🔴 行首判定色带 —— 本产品的标志性读法。
          // 一行有 N 个格子，人先扫这条带子决定「这一行要不要细看」，
          // 再横向读具体版本号。150 行的表里这是唯一能一眼定位问题行的东西。
          // 结论由后端算好（compare.RowVerdict），前端只翻译成颜色。
          // 前端自己再算一遍必然和后端分叉，而分叉时不报错。
          const kind = row.original.Verdict
          return (
            <div className="flex items-stretch gap-2">
              <span className={`w-[3px] shrink-0 rounded-[1px] ${STRIPE[kind]}`} aria-hidden />
              <div className="min-w-0">
                <button
                  type="button"
                  onClick={() => setDrill(row.original)}
                  className="cursor-pointer font-mono text-xs text-foreground underline-offset-2 hover:text-brand hover:underline"
                >
                  {row.original.ServiceKey}
                </button>
                <div className="flex items-center gap-2">
                  {/* 🔴 结论要有**文字**，不能只靠颜色。
                      导出的 Excel 有「结论」列白纸黑字写着，界面上原来只有
                      3px 色带 —— 人得靠颜色猜是哪一档，色盲更是完全读不到。

                      ⚠️ 放在服务名底下而不是最右一列：视线从左边进入，
                         色带和结论文字挨着一次读完；平台多要横滚时，
                         首列是钉住的，放最右的话横滚一下结论就看不见了。 */}
                  <span className={`text-[11px] font-semibold ${STAT[kind]}`}>
                    {t(`opsversion:verdict.${kind}`)}
                  </span>
                  {/* 整行忽略入口。
                      ⚠️ 改成 hover 才出现：它原来常驻，100 行就重复 100 遍，
                         和「=镜像名最后一段」两条小字把表体填满了噪音。
                         用 focus-within 兜键盘用户 —— 只认 hover 的话
                         Tab 过来的人根本触发不了这个按钮。 */}
                  <button
                    type="button"
                    onClick={() => ig.ignoreServices([row.original.ServiceKey])}
                    className="cursor-pointer text-[11px] text-muted-foreground opacity-0 underline-offset-2 group-hover/row:opacity-100 focus:opacity-100 hover:text-danger hover:underline"
                  >
                    {t('opsversion:ignore.rowAction')}
                  </button>
                </div>
              </div>
            </div>
          )
        },
      },
    ]
    const cols = result?.columns ?? []
    // 整列采集失败的列。逐格重复同一句失败原因是纯噪音 —— 顶部横幅已经说了
    const failedCols = new Set((result?.unhealthy_columns ?? []).map(respColKey))
    cols.forEach((c, idx) => {
      const colId = respColKey(c)
      head.push({
        id: `col-${idx}`,
        header: () => (
          <div>
            {/* 🔴 项目名必须进表头。这张表会被导出、被转发 ——
                两列都写「A公司/UAT」的话，收到的人分不出哪列是哪个项目，
                而两列的服务集合本来就不一样，对不上会以为是漏部署。 */}
            <div>{c.OrgName}</div>
            <div className="text-[11px] font-normal text-muted-foreground">
              {c.ProjectName ? `${c.ProjectName} · ${c.Env}` : c.Env}
            </div>
          </div>
        ),
        accessorFn: (r) => r.Cells[idx]?.Snap?.Tag ?? '',
        cell: ({ row }) => {
          const cell = row.original.Cells[idx]
          if (!cell) return <span>—</span>
          // 🔴 没有基准之后**任何列都能忽略**。
          //    原来第一列（基准）不给入口，理由是"忽略了参照物整行就没意义"——
          //    现在是横着比这几列彼此，没有参照物这回事。
          return (
            <div className="group/cell relative">
              <VerdictCell cell={cell} columnFailed={failedCols.has(colId)} />
              {
                <button
                  type="button"
                  title={t('opsversion:ignore.cellAction')}
                  aria-label={t('opsversion:ignore.cellAction')}
                  onClick={() => ig.ignoreCell(row.original.ServiceKey, stableOf(c))}
                  className="absolute right-0 top-0 hidden cursor-pointer rounded px-1 text-[11px] text-muted-foreground hover:text-danger group-hover/cell:block"
                >
                  ⊘
                </button>
              }
            </div>
          )
        },
      })
    })
    return head
  }, [result, t, checked, ig])

  const state = fromQuery(
    orgs,
    (d) => d.length === 0,
    (e) => toLoadError(e, t),
  )

  return (
    <div className="flex flex-col gap-4 p-5">
      <AsyncBoundary
        state={state}
        pending={<TableSkeleton columns={[30, 20, 20]} rows={6} />}
        empty={
          <EmptyState
            title={t('opsversion:org.empty')}
            reason={t('opsversion:recon.empty')}
            action={null}
          />
        }
        errorTitle={t('opsversion:recon.title')}
        retryLabel={t('common:action.retry')}
        onRetry={() => orgs.refetch()}
      >
        {() => (
          <>
            {err && (
              <Banner
                tone="bad"
                action={
                  <Button variant="ghost" onClick={() => setErr('')}>
                    {t('common:action.close')}
                  </Button>
                }
              >
                {err}
              </Banner>
            )}

            {/* 控制条 */}
            <div className="flex flex-col gap-2 rounded-lg border border-border bg-card p-3 md:flex-row md:flex-wrap md:items-center">
              <span className="text-xs text-muted-foreground">{t('opsversion:recon.plan')}</span>
              <Select
                label={t('opsversion:recon.plan')}
                value={planId}
                onChange={applyPlan}
                options={[
                  { value: '', label: t('opsversion:recon.unsaved') },
                  ...(plans.data ?? []).map((p) => ({ value: String(p.id), label: p.name })),
                ]}
              />
              {can(session, 'plan.write') &&
                (planId ? (
                  <>
                    <Button
                      variant="ghost"
                      onClick={() => savePlan.mutate('update')}
                      disabled={savePlan.isPending}
                    >
                      {t('opsversion:recon.planUpdate')}
                    </Button>
                    <Button variant="ghost" onClick={() => setConfirmDelPlan(true)}>
                      {t('common:action.delete')}
                    </Button>
                  </>
                ) : (
                  <Button variant="ghost" onClick={() => setSaving(true)}>
                    {t('opsversion:recon.planSave')}
                  </Button>
                ))}
              <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <input
                  type="checkbox"
                  checked={onlyDiff}
                  onChange={(e) => setOnlyDiff(e.target.checked)}
                />
                {t('opsversion:recon.onlyDiff')}
              </label>
              <input
                value={svcFilter}
                onChange={(e) => setSvcFilter(e.target.value)}
                placeholder={t('opsversion:recon.servicePh')}
                title={t('opsversion:recon.serviceHelp')}
                className="w-60 rounded-md border border-border bg-background px-2 py-1 text-xs text-foreground"
              />
              <div className="flex-1" />
              {can(session, 'export') && (
                <Button onClick={doExport} disabled={exporting}>
                  {exporting ? t('opsversion:recon.exporting') : t('opsversion:recon.export')}
                </Button>
              )}
              {/* ⚠️ 必须能关掉：对方系统正挂着时，强制刷新会让人**什么都看不了**；
                     而排障时想看的恰恰是"挂之前那份数据"。 */}
              <label className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <input
                  type="checkbox"
                  checked={autoRefresh}
                  onChange={(e) => setAutoRefresh(e.target.checked)}
                />
                {t('opsversion:recon.autoRefresh')}
              </label>
              {can(session, 'view') && (
                <Button
                  variant="primary"
                  onClick={() => compare.mutate()}
                  disabled={compare.isPending}
                >
                  {t('opsversion:recon.run')}
                </Button>
              )}
            </div>

            {/* 刷新进度。
                🔴 必须看得见：自动刷新如果是黑盒，用户只看到按钮转圈几十秒，
                   不知道卡在哪一列、还要多久，第一反应是"它是不是挂了"。 */}
            {progress ? (
              <div className="rounded-lg border border-border bg-card px-3 py-2 text-xs text-foreground">
                {t('opsversion:recon.refreshing', {
                  done: progress.done,
                  total: progress.total,
                  current: progress.current || '…',
                })}
              </div>
            ) : null}

            {/* 刷新失败的列。
                🔴 用旧数据继续，但必须说出来 —— 静默用旧数据的话，
                   "已刷新"是假的，人会拿着过期数据去下判断。 */}
            {outcomes.some((o) => o.state === 'failed') ? (
              <Banner tone="warn">
                {t('opsversion:recon.staleCols', {
                  cols: outcomes
                    .filter((o) => o.state === 'failed')
                    .map((o) => o.label)
                    .join('、'),
                })}
              </Banner>
            ) : null}

            {/* 列选择。列是 (组织,环境) 的自由组合 —— 不必同环境对同环境 */}
            <div className="rounded-lg border border-border bg-card p-3">
              <div className="mb-2 text-[11px] text-muted-foreground">
                {t('opsversion:recon.columns')}
              </div>
              <div className="flex flex-wrap gap-3">
                {choices.map((c) => {
                  const k = colKey(c)
                  return (
                    <label key={k} className="flex items-center gap-1.5 text-xs text-foreground">
                      <input
                        type="checkbox"
                        checked={selected.has(k)}
                        onChange={(e) => {
                          const next = new Set(selected)
                          e.target.checked ? next.add(k) : next.delete(k)
                          setPicked(next)
                        }}
                      />
                      {colLabel(c)}
                    </label>
                  )
                })}
              </div>
            </div>

            {/* 🔴 几乎全是「没有」时提醒列可能选错了。
                这种结果绝大多数不是两边真的都没部署，而是把两批毫不相干的
                平台放进了同一次比对 —— 而「全是灰的」和「确实没差异」看起来很像 */}
            {result?.mostly_missing && (
              <Banner tone="warn">{t('opsversion:recon.mostlyMissing')}</Banner>
            )}

            {/* 🔴 整列采集失败必须显著提示：表面只是几个灰格子，
                但这次对账的结论已经不完整了 */}
            {result?.unhealthy_columns?.length ? (
              <Banner tone="bad">
                {t('opsversion:recon.incomplete', {
                  cols: result.unhealthy_columns
                    .map((c) => `${respColKey(c)}（${t(`opsversion:syncStatus.${c.SyncStatus}`)}）`)
                    .join('、'),
                })}
                <div className="mt-1 text-xs">{t('opsversion:verdictHint.no_data')}</div>
              </Banner>
            ) : null}

            {/* 🔴 还没比对过时给一个空态，不能留一片 660px 的空白。
                这是**登录后的落地页** —— 第一次打开的人对着一屏空白，
                不知道要点哪里。镜像同步页的空态做得对（说清为什么空 + 给一个动作），
                照那个做。 */}
            {!result && !compare.isPending && (
              <EmptyState
                title={t('opsversion:recon.startTitle')}
                reason={t('opsversion:recon.startReason', { n: selected.size })}
                action={
                  selected.size >= 2
                    ? { label: t('opsversion:recon.run'), onClick: () => compare.mutate() }
                    : null
                }
              />
            )}

            {result && (
              <>
                {/* 统计条：数字用判定色，左侧 2px 竖条呼应行首色带 ——
                    与表里的读法是同一套语言，不是另外一种装饰。

                    🔴 每一格都是筛选入口：数字就在眼前，点它筛出对应的行
                    是最自然的下一步。做成纯展示的话，人看到「落后 44」
                    还得自己去表里翻那 44 行在哪。 */}
                <div className="flex flex-wrap overflow-hidden rounded-lg border border-border bg-card">
                  {VERDICT_ORDER.map((v) => {
                    const kind = v
                    const n = result.summary[v] ?? 0
                    const on = verdictPick === v
                    return (
                      <button
                        key={v}
                        type="button"
                        // 数量为 0 的不让点：点了必然是空表，那不是筛选是死路
                        disabled={n === 0}
                        onClick={() => setVerdictPick(on ? null : v)}
                        aria-pressed={on}
                        className={`relative min-w-[92px] flex-1 border-r border-border px-3 py-2 text-left last:border-r-0 ${
                          n === 0
                            ? 'cursor-default opacity-60'
                            : 'cursor-pointer hover:bg-secondary'
                        } ${on ? 'bg-secondary' : ''}`}
                      >
                        <span
                          className={`absolute inset-y-0 left-0 w-[2px] ${STRIPE[kind]}`}
                          aria-hidden
                        />
                        <div
                          className={`font-mono text-[17px] leading-tight font-semibold ${STAT[kind]}`}
                        >
                          {n}
                        </div>
                        <div className="text-[10.5px] text-muted-foreground">
                          {t(`opsversion:verdict.${v}`)}
                          {on ? ' ✓' : ''}
                        </div>
                      </button>
                    )
                  })}
                </div>
                {/* 🔴 筛选后必须说清「筛掉了多少」。
                    只显示剩下的行数，人会以为数据就这么少 ——
                    这是「筛选态不能看起来像全部」，与失败态不能退化成空态同源。 */}
                <div className="flex flex-wrap items-center gap-2">
                  <input
                    value={keyword}
                    onChange={(e) => setKeyword(e.target.value)}
                    placeholder={t('opsversion:recon.searchPh')}
                    className="w-64 rounded-md border border-border bg-background px-2 py-1 text-xs text-foreground"
                  />
                  <span className="text-[11px] text-muted-foreground">
                    {visibleRows.length === allRows.length
                      ? t('opsversion:recon.rowCount', { n: allRows.length })
                      : t('opsversion:recon.rowFiltered', {
                          n: visibleRows.length,
                          total: allRows.length,
                        })}
                  </span>
                  {(keyword || verdictPick) && (
                    <Button
                      variant="ghost"
                      onClick={() => {
                        setKeyword('')
                        setVerdictPick(null)
                      }}
                    >
                      {t('opsversion:recon.clearFilter')}
                    </Button>
                  )}

                  <div className="flex-1" />

                  {/* 批量忽略：勾了才出现。常驻一个禁用按钮只会占地方 */}
                  {checked.size > 0 && (
                    <>
                      <span className="text-[11px] text-muted-foreground">
                        {t('opsversion:ignore.picked', { n: checked.size })}
                      </span>
                      <Button
                        onClick={() => {
                          ig.ignoreServices([...checked])
                          setChecked(new Set())
                        }}
                      >
                        {t('opsversion:ignore.batchAction')}
                      </Button>
                    </>
                  )}

                  {/* 🔴 「已忽略 …」必须**常驻**，且要**点名**。
                      忽略掉的行从表里消失了 —— 不写出来的话，几个月后没人说得清
                      某个服务为什么不在这张表上，而这张表是拿去跟客户对账的。
                      通配规则尤其要点名：`bi-*` 到底吃掉了 2 个还是 20 个，只看规则是不知道的。

                      ⚠️ 只有一个 chip：之前「本次隐藏了 N 个」和「已忽略 N 个」
                      并排放，说的是同一件事，两条挤在一起反而没人细看。 */}
                  {!ig.summary.empty && (
                    <button
                      type="button"
                      onClick={() => setIgnoreOpen(true)}
                      title={hiddenNames.join('、')}
                      className="cursor-pointer rounded-md border border-border px-2 py-1 text-[11px] text-muted-foreground hover:border-border-strong hover:text-foreground"
                    >
                      {t('opsversion:ignore.summaryRows', {
                        n: ig.summary.rows,
                        names: hiddenNames.slice(0, 3).join('、'),
                        more: hiddenNames.length > 3 ? '…' : '',
                      })}
                      {/* 格子数为 0 时不显示 ——「· 0 个格子」是纯噪音 */}
                      {ig.summary.cells > 0 &&
                        ` · ${t('opsversion:ignore.summaryCells', { n: ig.summary.cells })}`}
                    </button>
                  )}
                </div>

                {/* 筛完一行都不剩时要说清是筛没的，不是没数据 */}
                {visibleRows.length === 0 && allRows.length > 0 ? (
                  <div className="rounded-lg border border-dashed border-border-strong px-4 py-8 text-center text-xs text-muted-foreground">
                    {t('opsversion:recon.noMatch', { total: allRows.length })}
                  </div>
                ) : (
                  <DataTable
                    // 🔴 只给**要处理的**行上底色，一致的保持素底。
                    //    四种都铺满（照搬导出）的话，占一半的「一致」最抢眼，
                    //    注意力分配是反的。定义见 styles.css 的 .ops-row-*
                    rowClassName={(r) =>
                      r.Verdict === 'same' ? undefined : `ops-row ops-row-${r.Verdict}`
                    }
                    columns={columns}
                    data={visibleRows}
                    rowKey={(r) => r.ServiceKey}
                    minWidth={720}
                  />
                )}
              </>
            )}
            {drill && <ServiceDrill row={drill} onClose={() => setDrill(null)} />}

            <IgnoreManager
              open={ignoreOpen}
              ignores={ig.ignores}
              labelOf={labelOfStable}
              onUnignoreService={ig.unignoreService}
              onUnignoreCell={ig.unignoreCell}
              onClearAll={() => {
                ig.clearAll()
                setIgnoreOpen(false)
              }}
              onClose={() => setIgnoreOpen(false)}
            />

            <Dialog
              open={saving}
              onClose={() => setSaving(false)}
              title={t('opsversion:recon.planSave')}
              description={t('opsversion:recon.planSaveHint')}
              closeLabel={t('common:action.close')}
              footer={
                <>
                  <Button variant="default" onClick={() => setSaving(false)}>
                    {t('common:action.cancel')}
                  </Button>
                  <Button
                    variant="primary"
                    onClick={() => savePlan.mutate('create')}
                    disabled={savePlan.isPending}
                  >
                    {t('common:action.save')}
                  </Button>
                </>
              }
            >
              <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                {t('opsversion:recon.planName')}
                <input
                  className="rounded-md border border-border bg-background px-2 py-1.5 text-sm text-foreground"
                  value={planName}
                  onChange={(e) => setPlanName(e.target.value)}
                />
              </label>
            </Dialog>

            <Dialog
              open={confirmDelPlan}
              onClose={() => setConfirmDelPlan(false)}
              title={t('opsversion:recon.planDelete')}
              description={t('opsversion:recon.planDeleteHint', { name: current?.name ?? '' })}
              closeLabel={t('common:action.close')}
              footer={
                <>
                  <Button variant="default" onClick={() => setConfirmDelPlan(false)}>
                    {t('common:action.cancel')}
                  </Button>
                  <Button
                    variant="danger"
                    onClick={() => deletePlan.mutate()}
                    disabled={deletePlan.isPending}
                  >
                    {t('common:action.delete')}
                  </Button>
                </>
              }
            >
              <div className="text-sm text-foreground">{current?.name}</div>
            </Dialog>
          </>
        )}
      </AsyncBoundary>
    </div>
  )
}
