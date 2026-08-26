import { useTranslation } from '@ops/i18n'
import {
  AsyncBoundary,
  Badge,
  type BadgeTone,
  Button,
  type ColumnDef,
  DataTable,
  Dialog,
  EmptyState,
  TableSkeleton,
  fromQuery,
  Toast,
  useToast,
} from '@ops/ui'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api, download, toLoadError } from '../../lib/api.js'
import { type Session, can } from '../../lib/session.js'
import { OrgForm } from './OrgForm.js'
import type { Org, ProbeResult } from './types.js'

/** 采集状态色。🔴 `never`（从未采集）用中性灰而不是绿 ——
 *  「还没采过」和「采集正常」必须一眼分得开 */
const SYNC_TONE: Record<string, BadgeTone> = {
  success: 'ok',
  partial: 'warn',
  auth_failed: 'bad',
  unreachable: 'bad',
  forbidden: 'bad',
  error: 'bad',
  never: 'mute',
}

type TFn = (k: string) => string

/**
 * 凭据摘要 —— 必须反映**实际生效的那一层**。
 *
 * 🔴 凭据在这个系统里有三层：平台级 / 环境级 / 数据源级，生效的是后两层。
 *    只读平台级的话，生产上 A公司 会显示「password · 未配凭据」，
 *    而同一行的采集状态是「正常」、数据源页显示「api_key · 已配凭据」——
 *    同一个对象三个页面三种说法，排障时人会去补一个填了也没用的地方。
 */
function credSummary(o: Org, t: TFn): string {
  // 引用了数据源 → 地址和凭据都由数据源提供，平台级那份根本不参与
  if (o.datasource_id > 0) {
    const kind = o.ds_provider_type || o.provider_type
    return `${kind} · ${t('opsversion:org.credFromDs')}「${o.datasource_name}」`
  }
  const envs = o.envs ?? []
  const withCred = envs.filter((e) => e.has_credential)
  if (withCred.length > 0) {
    const at = withCred[0]?.auth_type || o.auth_type
    // 部分环境配了、部分没配 —— 这个差别要说出来，否则"正常"和"失败"混在一起没法解释
    const label =
      withCred.length === envs.length
        ? t('opsversion:org.credEnvLevel')
        : `${t('opsversion:org.credEnvPartial')}（${withCred.length}/${envs.length}）`
    return `${at} · ${label}`
  }
  return `${o.auth_type} · ${
    o.has_credential ? t('opsversion:org.credConfigured') : t('opsversion:org.credMissing')
  }`
}

/** 环境行上要显示的项目名。查不到（老数据 / 默认项目）返回空串，调用方据此不渲染 */
function projLabel(o: Org, projectId?: number): string {
  if (!projectId) return ''
  return (o.projects ?? []).find((p) => p.id === projectId)?.name ?? ''
}

export function OrgsPage({ session }: { session: Session }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<Org | null | undefined>(undefined)
  const [confirmDel, setConfirmDel] = useState<Org | null>(null)
  const { toast, show: showToast } = useToast()
  /**
   * 正在进行中的按钮。key 形如 `probe:3` / `collect:3` / `collectAll`。
   *
   * 🔴 没有加载态时，点了到结果返回之间界面**毫无变化** ——
   * 用户以为"功能坏了"或"后台还在跑"，然后反复点。
   * 而 probe 是秒回的，那几百毫秒里什么都不显示，人是感知不到发生过事情的。
   */
  const [busy, setBusy] = useState<Set<string>>(new Set())
  const isBusy = (k: string) => busy.has(k)
  async function withBusy(k: string, fn: () => Promise<void>) {
    setBusy((p) => new Set(p).add(k))
    try {
      await fn()
    } finally {
      // ⚠️ 必须 finally：抛异常时不解锁的话，那个按钮就永久禁用了
      setBusy((p) => {
        const n = new Set(p)
        n.delete(k)
        return n
      })
    }
  }

  const q = useQuery({ queryKey: ['orgs'], queryFn: () => api<Org[]>('/api/orgs') })
  const reload = () => qc.invalidateQueries({ queryKey: ['orgs'] })
  const say = showToast

  /** 导出单个环境的版本清单 */
  // 🔴 projectId 必须传：一列 = 项目 × 环境。不传的话三个项目各一个
  //    「导出清单」链接，点哪个下下来的都是同一份跨项目全量。
  async function exportInventory(orgId: number, orgName: string, env: string, projectId: number) {
    try {
      await download('/api/export/inventory', {
        method: 'POST',
        body: JSON.stringify({ org_id: orgId, env, project_id: projectId }),
      })
    } catch (e) {
      // ⚠️ 采集失败的环境后端会拒绝导出（导出去是张空表，
      //    跟"这个环境什么都没部署"分不出来）—— 那句话要原样带给用户
      say((e as Error).message, 'err')
    }
  }

  async function probe(i: Org) {
    await withBusy(`probe:${i.id}`, async () => {
    try {
      const r = await api<ProbeResult>(`/api/orgs/${i.id}/probe`, { method: 'POST' })
      // 🔴 认证失败 / 网络不通 / 权限不足 分别提示 —— 三者处理方式完全不同
      say(r.ok ? t('opsversion:org.probeOk') : `${r.kind}: ${r.message}`, r.ok ? 'ok' : 'err')
    } catch (e) {
      say((e as Error).message, 'err')
    }
    })
  }

  async function collect(i: Org) {
    await withBusy(`collect:${i.id}`, async () => {
      try {
        await api(`/api/orgs/${i.id}/collect`, { method: 'POST' })
        say(t('opsversion:org.collectDone'), 'ok')
      } catch (e) {
        say((e as Error).message, 'err')
      }
      reload()
    })
  }

  async function collectAll(list: Org[]) {
    await withBusy('collectAll', async () => {
    let ok = 0
    let fail = 0
    for (const i of list) {
      try {
        await api(`/api/orgs/${i.id}/collect`, { method: 'POST' })
        ok++
      } catch {
        fail++
      }
    }
    // 🔴 逐个报结果，不能只说「完成」—— 失败了不说等于骗人
    say(t('opsversion:org.collectAllDone', { ok, fail }), fail ? 'err' : 'ok')
    reload()
    })
  }

  const columns: ColumnDef<Org, unknown>[] = [
    {
      id: 'name',
      header: t('opsversion:org.title'),
      accessorFn: (r) => r.name,
      cell: ({ row }) => (
        <div>
          <span className="text-xs font-semibold text-foreground">{row.original.name}</span>
          {row.original.is_self && (
            <span className="ml-1.5">
              <Badge tone="info">{t('opsversion:org.isSelfShort')}</Badge>
            </span>
          )}
          {/* 🔴 停用必须在列表里看得见：停用的平台不参与比对，
              而"我的平台怎么不在比对列里"是没有这个标记就查不出来的问题 */}
          {row.original.enabled === false && (
            <span className="ml-1.5">
              <Badge tone="mute">{t('opsversion:org.disabled')}</Badge>
            </span>
          )}
        </div>
      ),
    },
    {
      id: 'provider',
      header: t('opsversion:org.provider'),
      accessorFn: (r) => r.provider_type,
      cell: ({ row }) => (
        <div>
          <Badge tone="info">{row.original.provider_type}</Badge>
          {/* 🔴 凭据有三层（平台 / 环境 / 数据源），实际生效的是后两层。
              只读平台级那一层的话，会出现「未配凭据」和「采集正常」并排显示 ——
              自相矛盾，而且把排障引向错误方向：人会去补平台级凭据，
              可真正生效的配置在环境级或数据源级，平台级填了也没用。
              ⚠️ 同理 auth_type：平台级那个 password 也是不生效的陈旧值。 */}
          <div className="text-[11px] text-muted-foreground">{credSummary(row.original, t)}</div>
        </div>
      ),
    },
    {
      id: 'harbor',
      header: t('opsversion:org.harbor'),
      accessorFn: (r) => r.harbor_host,
      cell: ({ row }) => (
        <span className="font-mono text-[11px] text-muted-foreground">
          {row.original.harbor_host || '—'}
          {row.original.harbor_project ? `/${row.original.harbor_project}` : ''}
        </span>
      ),
    },
    {
      id: 'envs',
      header: t('opsversion:org.envCluster'),
      accessorFn: (r) => r.envs?.length ?? 0,
      cell: ({ row }) => (
        <div className="text-[11px] text-muted-foreground">
          {/* 🔴 key 必须带 project_id：一个平台的同一环境现在有多行（每个项目一行），
              只用 e.env 时三行的 key 全是 "UAT" —— React 会按 key 复用节点，
              增删项目时渲染出的行可能对不上真实数据。 */}
          {(row.original.envs ?? []).map((e) => (
            <div key={`${e.env}/${e.project_id ?? 0}`} className="flex items-center gap-1.5">
              <span>
                {e.env} <span className="font-mono">{(e.cluster_refs ?? []).join(',')}</span>
                {/* 🔴 项目名必须显示：三行都写「UAT local」的话，
                    用户完全看不出它们是三个不同项目（采集范围、排除规则各不相同）——
                    实测过：同一平台的多行长得一模一样，只有其中一行配了 workload_exclude。 */}
                {projLabel(row.original, e.project_id) && (
                  <span className="text-brand"> · {projLabel(row.original, e.project_id)}</span>
                )}
                {!e.compare_enabled && ` (${t('opsversion:org.notCompared')})`}
              </span>
              {/* 单个环境的版本清单。
                  🔴 与「比对」是两件事：这里导的是"这套环境现在跑着什么"，
                     没有基准、没有落差 —— 所以是独立入口而不是让比对支持一列。 */}
              {can(session, 'export') ? (
                <button
                  type="button"
                  onClick={() =>
                    exportInventory(row.original.id, row.original.name, e.env, e.project_id ?? 0)
                  }
                  className="text-[11px] text-brand hover:underline"
                >
                  {t('opsversion:org.exportInventory')}
                </button>
              ) : null}
            </div>
          ))}
        </div>
      ),
    },
    {
      id: 'sync',
      header: t('opsversion:org.syncState'),
      accessorFn: (r) => r.sync_status,
      cell: ({ row }) => (
        <div>
          <Badge tone={SYNC_TONE[row.original.sync_status] ?? 'mute'}>
            {t(`opsversion:syncStatus.${row.original.sync_status}`)}
          </Badge>
          {row.original.sync_at && (
            <div className="font-mono text-[11px] text-muted-foreground">
              {new Date(row.original.sync_at).toLocaleString()}
            </div>
          )}
          {/* 🔴 错误信息**最多两行**，悬停看全文。
              整条铺出来的话，一条 500 字的错误会把整张表撑变形，
              而人真正要看的「大概什么问题」就在前半句。 */}
          {row.original.sync_error && (
            <div
              className="line-clamp-2 max-w-[420px] text-[11px] whitespace-normal text-danger"
              title={row.original.sync_error}
            >
              {row.original.sync_error}
            </div>
          )}
        </div>
      ),
    },
    {
      id: 'ops',
      header: '',
      accessorFn: () => '',
      // 🔴 操作列必须钉在最右：横向滚动时按钮不能飘走
      meta: { action: true },
      cell: ({ row }) => (
        <div className="flex gap-1.5">
          {can(session, 'org.write') && (
            <>
              <Button
                disabled={isBusy(`probe:${row.original.id}`)}
                onClick={() => probe(row.original)}
              >
                {isBusy(`probe:${row.original.id}`)
                  ? t('opsversion:org.probing')
                  : t('opsversion:org.probe')}
              </Button>
              <Button onClick={() => setEditing(row.original)}>{t('common:action.edit')}</Button>
            </>
          )}
          {can(session, 'refresh') && (
            <Button
              disabled={isBusy(`collect:${row.original.id}`)}
              onClick={() => collect(row.original)}
            >
              {isBusy(`collect:${row.original.id}`)
                ? t('opsversion:org.collecting')
                : t('opsversion:org.collect')}
            </Button>
          )}
          {can(session, 'org.write') && (
            <Button onClick={() => setConfirmDel(row.original)}>{t('common:action.delete')}</Button>
          )}
        </div>
      ),
    },
  ]

  const state = fromQuery(q, (d) => d.length === 0, (e) => toLoadError(e, t))

  return (
    <div className="flex flex-col gap-3 p-5">
      {toast && <Toast msg={toast.msg} kind={toast.kind} />}

      <div className="flex items-center gap-2">
        <span className="text-sm font-semibold text-foreground">{t('opsversion:org.title')}</span>
        <span className="text-[11px] text-muted-foreground">{t('opsversion:org.hint')}</span>
        <div className="flex-1" />
        {can(session, 'org.write') && (
          <Button variant="primary" onClick={() => setEditing(null)}>
            {t('opsversion:org.add')}
          </Button>
        )}
        {can(session, 'refresh') && (
          <Button disabled={isBusy('collectAll')} onClick={() => collectAll(q.data ?? [])}>
            {isBusy('collectAll') ? t('opsversion:org.collecting') : t('opsversion:org.collectAll')}
          </Button>
        )}
      </div>

      <AsyncBoundary
        state={state}
        pending={<TableSkeleton columns={[24, 14, 20, 18, 14, 10]} rows={5} />}
        empty={
          <EmptyState
            title={t('opsversion:org.empty')}
            reason={t('opsversion:org.hint')}
            action={
              can(session, 'org.write')
                ? { label: t('opsversion:org.add'), onClick: () => setEditing(null) }
                : null
            }
          />
        }
        errorTitle={t('opsversion:org.title')}
        retryLabel={t('common:action.retry')}
        onRetry={() => q.refetch()}
      >
        {(data) => (
          <DataTable columns={columns} data={data} rowKey={(r) => String(r.id)} minWidth={900} />
        )}
      </AsyncBoundary>

      {editing !== undefined && (
        <OrgForm
          org={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined)
            say(t('common:action.save'), 'ok')
            reload()
          }}
          onToast={say}
        />
      )}

      {confirmDel && (
        <Dialog
          open
          onClose={() => setConfirmDel(null)}
          title={t('opsversion:org.delete')}
          closeLabel={t('common:action.close')}
          footer={
            <>
              <Button onClick={() => setConfirmDel(null)}>{t('common:action.cancel')}</Button>
              <Button
                variant="primary"
                onClick={async () => {
                  try {
                    await api(`/api/orgs/${confirmDel.id}`, { method: 'DELETE' })
                    say(t('common:action.delete'), 'ok')
                  } catch (e) {
                    say((e as Error).message, 'err')
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
          <p className="text-sm text-foreground">
            {t('opsversion:org.deleteConfirm', { name: confirmDel.name })}
          </p>
        </Dialog>
      )}
    </div>
  )
}
