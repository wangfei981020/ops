import { useTranslation } from '@ops/i18n'
import {
  AsyncBoundary,
  Badge,
  type BadgeTone,
  Banner,
  Button,
  type ColumnDef,
  DataTable,
  Dialog,
  EmptyState,
  Select,
  TableSkeleton,
  fromQuery,
  Toast,
  useToast,
} from '@ops/ui'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api, toLoadError } from '../../lib/api.js'
import { type Session, can } from '../../lib/session.js'
import type { Org } from '../orgs/types.js'
import { WebhookSection } from './WebhookSection.js'
import { HarborForm } from './HarborForm.js'
import { ImageCheck } from './ImageCheck.js'
import { NotifySection } from './NotifySection.js'
import type { Execution, Harbor, Policy, ProbeResult, ExecPage } from './types.js'

/** 同步状态 → 语气。与组织页用同一套映射口径 */
const SYNC_TONE: Record<string, BadgeTone> = {
  success: 'ok',
  never: 'mute',
  auth_failed: 'bad',
  forbidden: 'bad',
  unreachable: 'warn',
  error: 'bad',
  // 🔴 不能用 bad（红）：这不是故障，是一个**可选能力没开**。
  //    凭据只有项目级权限时读不到复制记录，但按服务查版本照常可用。
  //    染成红色会让人以为系统坏了，跑去查地址和密码。
  unsupported: 'mute',
}

/**
 * 执行状态 → 语气。
 *
 * 🔴 `InProgress` 不能用 mute（灰）：灰会被当成「跟我无关」一扫而过，
 * 而进行中恰恰是**结论还没出来**的状态，看表的人需要知道这一条稍后要再看一次。
 */
const EXEC_TONE: Record<string, BadgeTone> = {
  Succeeded: 'ok',
  Failed: 'bad',
  InProgress: 'info',
  Stopped: 'warn',
}

/** 执行记录每页条数。看流水只需要「最近有没有失败」，不需要占一屏 */
const EXEC_PAGE = 10

export function SyncPage({ session }: { session: Session }) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState<Harbor | null | undefined>(undefined)
  const [confirmDel, setConfirmDel] = useState<Harbor | null>(null)
  const { toast: msg0, show: showMsg } = useToast()
  // 保持原有 setMsg({text,kind}) 的调用形态，内部转给统一的 toast
  const msg = msg0 ? { text: msg0.msg, kind: msg0.kind } : null
  const setMsg = (v: { text: string; kind: 'ok' | 'err' } | null) => {
    if (v) showMsg(v.text, v.kind)
  }
  const [busy, setBusy] = useState(false)
  // 配置类的东西（Harbor / 复制规则 / 通知渠道）收进抽屉：
  // 它们配一次就不动了，而执行记录是天天要看的 —— 让常看的占主位
  const [settingsOpen, setSettingsOpen] = useState(false)

  const harbors = useQuery({ queryKey: ['harbors'], queryFn: () => api<Harbor[]>('/api/harbors') })
  const policies = useQuery({ queryKey: ['policies'], queryFn: () => api<Policy[]>('/api/sync/policies') })
  // 🔴 执行记录是**流水**，只需要看「最近有没有失败」——
  //    一次给 100 条会把下面的「按服务查同步」整个挤到屏幕外。
  //    默认 10 条 + 分页，要翻历史再往下翻。
  // 游标是 (时间, id) 的组合 —— 见 types.ts 上的说明
  type Cursor = { id: number; at?: string }
  const [execCursor, setExecCursor] = useState<Cursor>({ id: 0 })
  const [execStack, setExecStack] = useState<Cursor[]>([])
  const execs = useQuery({
    queryKey: ['executions', execCursor.id, execCursor.at],
    queryFn: () => {
      const q = new URLSearchParams({ limit: String(EXEC_PAGE) })
      if (execCursor.id) q.set('before', String(execCursor.id))
      if (execCursor.at) q.set('before_at', execCursor.at)
      return api<ExecPage>(`/api/sync/executions?${q}`)
    },
  })
  const orgs = useQuery({ queryKey: ['orgs'], queryFn: () => api<Org[]>('/api/orgs') })

  function reload() {
    harbors.refetch()
    policies.refetch()
    execs.refetch()
  }

  async function refreshNow() {
    setBusy(true)
    try {
      await api('/api/sync/refresh', { method: 'POST' })
      setMsg({ text: t('opsversion:sync.refreshDone'), kind: 'ok' })
      reload()
    } catch (e) {
      setMsg({ text: (e as Error).message, kind: 'err' })
    } finally {
      setBusy(false)
    }
  }

  async function probe(h: Harbor) {
    setBusy(true)
    try {
      const r = await api<ProbeResult>(`/api/harbors/${h.id}/probe`, { method: 'POST' })
      // 🔴 分类显示：密码错、网络不通、权限不足三种的下一步完全不同
      setMsg({
        text: r.ok ? r.message : `${t(`opsversion:syncStatus.${r.kind ?? 'error'}`)}：${r.message}`,
        kind: r.ok ? 'ok' : 'err',
      })
    } catch (e) {
      setMsg({ text: (e as Error).message, kind: 'err' })
    } finally {
      setBusy(false)
    }
  }

  const harborCols: ColumnDef<Harbor, unknown>[] = [
    {
      id: 'name',
      header: t('opsversion:sync.harbor'),
      accessorFn: (r) => r.name,
      cell: ({ row }) => (
        <div>
          <div className="text-xs font-semibold text-foreground">{row.original.name}</div>
          <div className="font-mono text-[11px] text-muted-foreground">{row.original.endpoint}</div>
        </div>
      ),
    },
    {
      id: 'cred',
      header: t('opsversion:sync.credential'),
      accessorFn: (r) => r.has_credential,
      cell: ({ row }) => (
        <div className="text-[11px]">
          <div className="text-muted-foreground">{row.original.username || '—'}</div>
          {/* 凭据永不回显，只说配没配 */}
          <Badge tone={row.original.has_credential ? 'ok' : 'warn'}>
            {row.original.has_credential
              ? t('opsversion:sync.credSet')
              : t('opsversion:sync.credMissing')}
          </Badge>
        </div>
      ),
    },
    {
      id: 'state',
      header: t('opsversion:sync.lastPull'),
      accessorFn: (r) => r.last_sync_status,
      cell: ({ row }) => (
        <div className="text-[11px]">
          <Badge tone={SYNC_TONE[row.original.last_sync_status] ?? 'mute'}>
            {t(`opsversion:syncStatus.${row.original.last_sync_status}`)}
          </Badge>
          <div className="mt-0.5 text-muted-foreground">
            {row.original.last_sync_at ? new Date(row.original.last_sync_at).toLocaleString() : '—'}
          </div>
          {/* 失败原因必须显示出来，否则只能去翻日志 */}
          {row.original.last_sync_error && (
            <div
              className="line-clamp-2 mt-0.5 max-w-[420px] whitespace-normal text-danger"
              title={row.original.last_sync_error}
            >
              {row.original.last_sync_error}
            </div>
          )}
        </div>
      ),
    },
    {
      id: 'ops',
      header: '',
      accessorFn: () => '',
      meta: { action: true },
      cell: ({ row }) => (
        <div className="flex gap-1.5">
          <Button onClick={() => probe(row.original)} disabled={busy}>
            {t('opsversion:sync.probe')}
          </Button>
          {can(session, 'org.write') && (
            <>
              <Button onClick={() => setEditing(row.original)}>{t('common:action.edit')}</Button>
              <Button variant="ghost" onClick={() => setConfirmDel(row.original)}>
                {t('common:action.delete')}
              </Button>
            </>
          )}
        </div>
      ),
    },
  ]

  const policyCols: ColumnDef<Policy, unknown>[] = [
    {
      id: 'name',
      header: t('opsversion:sync.policy'),
      accessorFn: (r) => r.name,
      cell: ({ row }) => (
        <div>
          <div className="text-xs text-foreground">{row.original.name}</div>
          <div className="text-[11px] text-muted-foreground">
            {row.original.harbor_name} → {row.original.dest_registry || '—'}
          </div>
        </div>
      ),
    },
    {
      id: 'trigger',
      header: t('opsversion:sync.trigger'),
      accessorFn: (r) => r.trigger_type,
      cell: ({ row }) => (
        <Badge tone="mute">
          {t(`opsversion:trigger.${row.original.trigger_type || 'unknown'}`)}
        </Badge>
      ),
    },
    {
      id: 'org',
      // 🔴 这一列原来叫「绑定平台」——**没说是绑源还是绑目标**。
      //
      //    规则名形如 `sync-to-a-appA`（我方 往 A公司 推），人自然会按
      //    「这是 我方 的规则」去绑 我方。而归因查的是**接收方**：
      //    「A公司 这一列的镜像推没推到位」→ 查 A公司 的复制记录。
      //    绑成 我方 的话，A公司 那一列永远显示「同步状态未知」，
      //    而执行记录里明明一堆 Succeed —— 用户实测被这个卡住了。
      //
      //    改成「推给哪个平台」，并在下面显示目标地址做对照。
      header: t('opsversion:sync.destOrg'),
      accessorFn: (r) => r.org_name,
      cell: ({ row }) => (
        <div>
          {can(session, 'org.write') ? (
            <Select
              label={t('opsversion:sync.destOrg')}
              value={row.original.org_id ? String(row.original.org_id) : ''}
              onChange={async (v) => {
                try {
                  await api(`/api/sync/policies/${row.original.id}/org`, {
                    method: 'PUT',
                    body: JSON.stringify({ org_id: v ? Number(v) : 0 }),
                  })
                  policies.refetch()
                } catch (e) {
                  setMsg({ text: (e as Error).message, kind: 'err' })
                }
              }}
              options={[
                { value: '', label: t('opsversion:sync.unbound') },
                // ⚠️ 「我方」不该出现在这个下拉里：规则是**我方往外推**的，
                //    接收方不可能是我方自己。留着它等于摆一个必然选错的选项。
                ...(orgs.data ?? [])
                  .filter((o) => !o.is_self)
                  .map((o) => ({ value: String(o.id), label: o.name })),
              ]}
            />
          ) : (
            <span className="text-xs text-muted-foreground">
              {row.original.org_name || t('opsversion:sync.unbound')}
            </span>
          )}
        </div>
      ),
    },
  ]

  const execCols: ColumnDef<Execution, unknown>[] = [
    {
      id: 'when',
      header: t('opsversion:sync.startedAt'),
      accessorFn: (r) => r.started_at ?? '',
      cell: ({ row }) => (
        <span className="text-[11px] whitespace-nowrap text-muted-foreground">
          {row.original.started_at ? new Date(row.original.started_at).toLocaleString() : '—'}
        </span>
      ),
    },
    {
      id: 'policy',
      header: t('opsversion:sync.policy'),
      accessorFn: (r) => r.policy_name,
      cell: ({ row }) => (
        <div>
          <div className="text-xs text-foreground">{row.original.policy_name}</div>
          <div className="text-[11px] text-muted-foreground">
            {row.original.org_name || t('opsversion:sync.unbound')}
          </div>
        </div>
      ),
    },
    {
      id: 'trigger',
      header: t('opsversion:sync.trigger'),
      accessorFn: (r) => r.trigger_type,
      cell: ({ row }) => (
        <span className="text-[11px] text-muted-foreground">
          {t(`opsversion:trigger.${row.original.trigger_type || 'unknown'}`)}
        </span>
      ),
    },
    {
      id: 'status',
      header: t('opsversion:sync.status'),
      accessorFn: (r) => r.status,
      cell: ({ row }) => (
        <Badge tone={EXEC_TONE[row.original.status] ?? 'mute'}>{row.original.status}</Badge>
      ),
    },
    {
      id: 'counts',
      header: t('opsversion:sync.counts'),
      accessorFn: (r) => r.total,
      cell: ({ row }) => (
        <span className="font-mono text-[11px]">
          <span className="text-success">{row.original.succeeded}</span>
          {' / '}
          {/* 失败数只有非零时才标红：一堆 0 全是红的会让真正的失败淹没 */}
          <span className={row.original.failed > 0 ? 'font-semibold text-danger' : 'text-muted-foreground'}>
            {row.original.failed}
          </span>
          {' / '}
          <span className="text-muted-foreground">{row.original.total}</span>
        </span>
      ),
    },
  ]

  const state = fromQuery(harbors, (d) => d.length === 0, (e) => toLoadError(e, t))

  return (
    <div className="flex flex-col gap-4 p-5">
      {/* 🔴 全站统一用浮层，不用页面内联提示条。
          内联那种会跟着页面滚走，而且同一个应用里
          两套反馈机制等于让人学两遍。见 @ops/ui 的 Toast。 */}
      {msg && <Toast msg={msg.text} kind={msg.kind === 'ok' ? 'ok' : 'err'} />}

      {/* 🔴 「按服务查同步」放在**执行记录之前**。
          原来反过来 —— 理由写的是「执行记录天天看，占主位」，而那个判断是错的：

            执行记录是**流水**，看的是「最近有没有失败」，扫一眼就够；
            「按服务查同步」是**带着问题来的**（"wallet 到底推过去没有"），
            那才是主动查询。

          把一屏流水压在查询前面，等于把人真正要用的东西藏到屏幕外。 */}
      <ImageCheck />

      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-semibold text-foreground">
          {t('opsversion:sync.executions')}
        </span>
        <span className="text-[11px] text-muted-foreground">{t('opsversion:sync.execHint')}</span>
        {/* ⚠️ 空表时不显示「第 1–0 条 / 共 0 条」—— 那是废话，
            而且和下面 EmptyState 说的是同一件事，说两遍反而让人以为出了什么问题 */}
        {execs.data && execs.data.total > 0 && (
          <span className="text-[11px] text-muted-foreground">
            {/* ⚠️ from 也要夹住：总数刚好是页大小整数倍时，
                最后一页取满 10 条 → 仍给游标 → 再翻一页是空的，
                显示成「第 51–50 条」。这是游标分页的固有形态
                （取满才知道可能还有），所以在显示这一层夹一下。 */}
            {t('opsversion:audit.pageInfo', {
              from: Math.min(execStack.length * EXEC_PAGE + 1, execs.data.total),
              to: Math.min(
                execStack.length * EXEC_PAGE + (execs.data.rows ?? []).length,
                execs.data.total,
              ),
              total: execs.data.total,
            })}
          </span>
        )}
        <div className="flex-1" />
        {can(session, 'sync.trigger') && (
          <Button onClick={refreshNow} disabled={busy}>
            {busy ? t('opsversion:sync.refreshing') : t('opsversion:sync.refresh')}
          </Button>
        )}
        <Button onClick={() => setSettingsOpen(true)}>{t('opsversion:sync.settings')}</Button>
      </div>

      {/* 主体：执行记录。这是天天要看的东西，占主位 */}
      <AsyncBoundary
        state={fromQuery(execs, (d) => (d.rows ?? []).length === 0, (e) => toLoadError(e, t))}
        pending={<TableSkeleton columns={[20, 30, 15, 15, 20]} rows={5} />}
        empty={
          <EmptyState
            title={t('opsversion:sync.noExec')}
            reason={
              (harbors.data ?? []).length === 0
                ? t('opsversion:sync.emptyHint')
                : t('opsversion:sync.noExecHint')
            }
            action={
              can(session, 'org.write')
                ? { label: t('opsversion:sync.settings'), onClick: () => setSettingsOpen(true) }
                : null
            }
          />
        }
        errorTitle={t('opsversion:sync.executions')}
        retryLabel={t('common:action.retry')}
        onRetry={() => execs.refetch()}
      >
        {(data) => (
          <>
            <DataTable
              columns={execCols}
              data={data.rows ?? []}
              rowKey={(r) => String(r.id)}
              minWidth={860}
            />
            {/* 只有真的翻得动才显示分页条 —— 记录还不满一页时它是纯噪音 */}
            {(data.next_before > 0 || execStack.length > 0) && (
              <div className="flex items-center justify-end gap-2">
                <Button
                  disabled={execStack.length === 0}
                  onClick={() => {
                    const prev = [...execStack]
                    const back = prev.pop() ?? { id: 0 }
                    setExecStack(prev)
                    setExecCursor(back)
                  }}
                >
                  {t('opsversion:audit.prev')}
                </Button>
                <Button
                  disabled={!data.next_before}
                  onClick={() => {
                    setExecStack((p) => [...p, execCursor])
                    setExecCursor({ id: data.next_before, at: data.next_before_at })
                  }}
                >
                  {t('opsversion:audit.next')}
                </Button>
              </div>
            )}
          </>
        )}
      </AsyncBoundary>


      {/*
        设置抽屉：源 Harbor / 复制规则 / 通知渠道。

        🔴 这三块都是**配一次就不动**的东西，而执行记录是天天看的。
        原来五段平铺在一页里，要滚很久才看得到今天同步得怎么样。
        收进抽屉而不是单开一个菜单，是因为它们和这一页是同一件事 ——
        单开「通知设置」菜单会让人配完了想不起来它是给谁用的。
      */}
      <Dialog
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
        title={t('opsversion:sync.settings')}
        description={t('opsversion:sync.settingsHint')}
        closeLabel={t('common:action.close')}
        width={980}
        footer={
          <Button variant="primary" onClick={() => setSettingsOpen(false)}>
            {t('common:action.close')}
          </Button>
        }
      >
        <div className="flex flex-col gap-5">
          <WebhookSection />
          {/* 🔴 三块有**依赖顺序**：没配 Harbor 就拉不到规则，没绑规则就没有通知判定。
              全空时并列三个大空态，等于让人对着三块「还没有…」猜先做哪个，
              而且每个 EmptyState 都很高，滚很久还是空的。
              所以：一条龙引导取代三个空态，配好第一步后它自动消失。 */}
          {(harbors.data ?? []).length === 0 && !harbors.isPending && (
            <div className="rounded-lg border border-dashed border-border-strong px-4 py-5">
              <div className="mb-1 text-sm font-semibold text-foreground">
                {t('opsversion:sync.guideTitle')}
              </div>
              <ol className="mb-3 ml-4 list-decimal text-[11px] leading-relaxed text-muted-foreground">
                <li>{t('opsversion:sync.guide1')}</li>
                <li>{t('opsversion:sync.guide2')}</li>
                <li>{t('opsversion:sync.guide3')}</li>
              </ol>
              {can(session, 'org.write') && (
                <Button variant="primary" onClick={() => setEditing(null)}>
                  {t('opsversion:sync.add')}
                </Button>
              )}
            </div>
          )}

          <div>
            <div className="mb-1.5 flex flex-wrap items-center gap-2">
              <span className="text-sm font-semibold text-foreground">
                {t('opsversion:sync.title')}
              </span>
              <span className="text-[11px] text-muted-foreground">{t('opsversion:sync.hint')}</span>
              <div className="flex-1" />
              {can(session, 'org.write') && (
                <Button variant="primary" onClick={() => setEditing(null)}>
                  {t('opsversion:sync.add')}
                </Button>
              )}
            </div>
            <AsyncBoundary
              state={state}
              pending={<TableSkeleton columns={[30, 20, 30, 20]} rows={2} />}
              // 上面已经有一条龙引导了，这里不再重复一个大空态
              empty={
                <div className="rounded-md border border-dashed border-border-strong px-3 py-2.5 text-[11px] text-muted-foreground">
                  {t('opsversion:sync.emptyCompact')}
                </div>
              }
              errorTitle={t('opsversion:sync.title')}
              retryLabel={t('common:action.retry')}
              onRetry={() => harbors.refetch()}
            >
              {(data) => (
                <DataTable
                  columns={harborCols}
                  data={data}
                  rowKey={(r) => String(r.id)}
                  minWidth={640}
                />
              )}
            </AsyncBoundary>
          </div>

          {/* 复制规则。绑组织是归因的前提 —— 不绑就只知道「同步过」，不知道「同步给谁」 */}
          <div>
            <div className="mb-1.5 flex items-center gap-2">
              <span className="text-sm font-semibold text-foreground">
                {t('opsversion:sync.policies')}
              </span>
              <span className="text-[11px] text-muted-foreground">
                {t('opsversion:sync.policiesHint')}
              </span>
            </div>
            <AsyncBoundary
              state={fromQuery(policies, (d) => d.length === 0, (e) => toLoadError(e, t))}
              pending={<TableSkeleton columns={[40, 20, 40]} rows={2} />}
              empty={
                <div className="rounded-md border border-dashed border-border-strong px-3 py-2.5 text-[11px] text-muted-foreground">
                  {t('opsversion:sync.noPoliciesHint')}
                </div>
              }
              errorTitle={t('opsversion:sync.policies')}
              retryLabel={t('common:action.retry')}
              onRetry={() => policies.refetch()}
            >
              {(data) => (
                <DataTable
                  columns={policyCols}
                  data={data}
                  rowKey={(r) => String(r.id)}
                  minWidth={640}
                />
              )}
            </AsyncBoundary>
          </div>

          <NotifySection session={session} />
        </div>
      </Dialog>

      {editing !== undefined && (
        <HarborForm
          harbor={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined)
            reload()
          }}
          onError={(m) => setMsg({ text: m, kind: 'err' })}
        />
      )}

      {confirmDel && (
        <Dialog
          open
          onClose={() => setConfirmDel(null)}
          title={t('opsversion:sync.delete')}
          description={t('opsversion:sync.deleteHint')}
          closeLabel={t('common:action.close')}
          footer={
            <>
              <Button onClick={() => setConfirmDel(null)}>{t('common:action.cancel')}</Button>
              <Button
                variant="danger"
                onClick={async () => {
                  try {
                    await api(`/api/harbors/${confirmDel.id}`, { method: 'DELETE' })
                  } catch (e) {
                    setMsg({ text: (e as Error).message, kind: 'err' })
                  }
                  setConfirmDel(null)
                  reload()
                }}
              >
                {t('common:action.confirm')}
              </Button>
            </>
          }
        >
          <div className="text-sm text-foreground">{confirmDel.name}</div>
        </Dialog>
      )}
    </div>
  )
}
