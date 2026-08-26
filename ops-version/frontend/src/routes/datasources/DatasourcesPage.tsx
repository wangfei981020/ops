import { useTranslation } from '@ops/i18n'
import {
  AsyncBoundary, Badge, Banner, Button, type ColumnDef, DataTable,
  Dialog, EmptyState, Select, TableSkeleton, fromQuery,
} from '@ops/ui'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api, toLoadError } from '../../lib/api.js'
import { type Session, can } from '../../lib/session.js'

/**
 * 数据源 = 连接信息（地址 + 凭据），可被多个平台共用。
 *
 * 🔴 独立出来的理由：同一个 Rancher/ArgoCD/Kite 常被 N 个平台共用。
 * 之前要把地址和凭据重复配 N 遍 —— 改一次密码要改 N 处，
 * 漏一处就是一个平台悄悄采集失败，而失败原因显示成"认证失败"，
 * 没人会想到是"另外那处忘了改"。
 */
interface Datasource {
  id: number
  name: string
  provider_type: string
  endpoint: string
  auth_type: string
  has_credential: boolean
  insecure_tls: boolean
  enabled: boolean
  /** 有几个平台在用 —— 删除前要看这个 */
  used_by: number
}

const PROVIDERS = ['kite', 'rancher', 'argocd'] as const

/** 各数据源支持的认证方式。与平台表单同一套口径，两处不一致会让人以为填错了 */
const AUTH_BY_PROVIDER: Record<string, readonly string[]> = {
  kite: ['password', 'api_key'],
  rancher: ['password', 'api_key'],
  argocd: ['password', 'token'],
}

export function DatasourcesPage({ session }: { session: Session }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<Datasource | null | undefined>(undefined)
  const [removing, setRemoving] = useState<Datasource | null>(null)
  const [err, setErr] = useState('')

  const q = useQuery({ queryKey: ['datasources'], queryFn: () => api<Datasource[]>('/api/datasources') })
  const state = fromQuery(q, (d) => d.length === 0, (e) => toLoadError(e, t))
  const reload = () => qc.invalidateQueries({ queryKey: ['datasources'] })
  const canWrite = can(session, 'org.write')

  const columns: ColumnDef<Datasource, unknown>[] = [
    {
      id: 'name',
      header: t('opsversion:ds.name'),
      accessorFn: (r) => r.name,
      cell: ({ row }) => (
        <div>
          <span className="text-xs font-semibold text-foreground">{row.original.name}</span>
          {!row.original.enabled && (
            <span className="ml-1.5">
              <Badge tone="mute">{t('opsversion:org.disabled')}</Badge>
            </span>
          )}
        </div>
      ),
    },
    {
      id: 'type',
      header: t('opsversion:ds.type'),
      accessorFn: (r) => r.provider_type,
      cell: ({ row }) => (
        <div className="text-[11px]">
          <Badge tone="info">{row.original.provider_type}</Badge>
          <div className="mt-0.5 text-muted-foreground">
            {row.original.auth_type} ·{' '}
            {row.original.has_credential
              ? t('opsversion:ds.credSet')
              : t('opsversion:ds.credNone')}
          </div>
        </div>
      ),
    },
    {
      id: 'endpoint',
      header: t('opsversion:ds.endpoint'),
      accessorFn: (r) => r.endpoint,
      cell: ({ row }) => (
        <span className="font-mono text-[11px] text-muted-foreground">
          {row.original.endpoint || '—'}
        </span>
      ),
    },
    {
      id: 'used',
      header: t('opsversion:ds.usedBy'),
      accessorFn: (r) => r.used_by,
      cell: ({ row }) => (
        <span className="text-xs text-foreground">
          {t('opsversion:ds.usedByN', { n: row.original.used_by })}
        </span>
      ),
    },
    {
      id: 'ops',
      header: '',
      meta: { action: true },
      cell: ({ row }) => (
        <div className="flex gap-1">
          <Button size="sm" disabled={!canWrite} onClick={() => setEditing(row.original)}>
            {t('common:action.edit')}
          </Button>
          <Button size="sm" disabled={!canWrite} onClick={() => setRemoving(row.original)}>
            {t('common:action.delete')}
          </Button>
        </div>
      ),
    },
  ]

  return (
    <div className="flex flex-col gap-3 p-5">
      {err ? <Banner tone="bad">{err}</Banner> : null}

      <div className="flex items-center gap-2">
        <span className="text-sm font-semibold text-foreground">{t('opsversion:ds.title')}</span>
        <span className="text-[11px] text-muted-foreground">{t('opsversion:ds.hint')}</span>
        <div className="flex-1" />
        <Button variant="primary" disabled={!canWrite} onClick={() => setEditing(null)}>
          {t('opsversion:ds.add')}
        </Button>
      </div>

      <AsyncBoundary
        state={state}
        pending={<TableSkeleton columns={[22, 16, 34, 12, 16]} rows={3} />}
        empty={
          <EmptyState
            title={t('opsversion:ds.empty')}
            reason={t('opsversion:ds.emptyHint')}
            action={{ label: t('opsversion:ds.add'), onClick: () => setEditing(null) }}
          />
        }
        errorTitle={t('opsversion:ds.title')}
        retryLabel={t('common:action.retry')}
        onRetry={() => q.refetch()}
      >
        {(data) => <DataTable columns={columns} data={data} rowKey={(r) => String(r.id)} minWidth={900} />}
      </AsyncBoundary>

      {editing !== undefined && (
        <DatasourceForm
          ds={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined)
            reload()
          }}
          onError={setErr}
        />
      )}

      {removing && (
        <Dialog
          open
          title={t('opsversion:ds.deleteTitle')}
          onClose={() => setRemoving(null)}
          closeLabel={t('common:action.close')}
          footer={
            <>
              <Button onClick={() => setRemoving(null)}>{t('common:action.cancel')}</Button>
              <Button
                variant="primary"
                onClick={async () => {
                  try {
                    await api(`/api/datasources/${removing.id}`, { method: 'DELETE' })
                    reload()
                  } catch (e) {
                    // ⚠️ 后端对"还有平台在用"回 409 并给出可执行的话，原样带给用户
                    setErr((e as Error).message)
                  }
                  setRemoving(null)
                }}
              >
                {t('common:action.confirm')}
              </Button>
            </>
          }
        >
          <p className="text-sm text-foreground">
            {t('opsversion:ds.deleteConfirm', { name: removing.name })}
          </p>
          {removing.used_by > 0 && (
            <p className="mt-2 text-xs text-danger">
              {t('opsversion:ds.deleteInUse', { n: removing.used_by })}
            </p>
          )}
        </Dialog>
      )}
    </div>
  )
}

function DatasourceForm({
  ds,
  onClose,
  onSaved,
  onError,
}: {
  ds: Datasource | null
  onClose: () => void
  onSaved: () => void
  onError: (s: string) => void
}) {
  const { t } = useTranslation()
  const isEdit = !!ds
  const [f, setF] = useState({
    name: ds?.name ?? '',
    provider_type: ds?.provider_type ?? 'rancher',
    endpoint: ds?.endpoint ?? '',
    auth_type: ds?.auth_type ?? 'api_key',
    username: '',
    password: '',
    api_key: '',
    insecure_tls: ds?.insecure_tls ?? false,
    enabled: ds?.enabled ?? true,
  })
  const [busy, setBusy] = useState(false)
  const set = <K extends keyof typeof f>(k: K, v: (typeof f)[K]) => setF((p) => ({ ...p, [k]: v }))

  /** 单串凭据（api_key / token）与用户名密码走不同输入框 */
  const isKeyAuth = (a: string) => a === 'api_key' || a === 'token'

  async function save() {
    if (!f.name.trim()) {
      onError(t('opsversion:ds.nameRequired'))
      return
    }
    setBusy(true)
    try {
      await api(isEdit ? `/api/datasources/${ds.id}` : '/api/datasources', {
        method: isEdit ? 'PUT' : 'POST',
        body: JSON.stringify(f),
      })
      onSaved()
    } catch (e) {
      onError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const input = (v: string, on: (s: string) => void, ph = '', type = 'text') => (
    <input
      type={type}
      value={v}
      placeholder={ph}
      onChange={(e) => on(e.target.value)}
      className="w-full rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground"
    />
  )
  const field = (label: string, node: React.ReactNode, hint?: string) => (
    <div className="mb-2.5">
      <div className="mb-1 text-[11px] text-muted-foreground">{label}</div>
      {node}
      {hint ? <div className="mt-1 text-[11px] text-muted-foreground">{hint}</div> : null}
    </div>
  )

  return (
    <Dialog
      open
      title={isEdit ? t('opsversion:ds.editTitle') : t('opsversion:ds.add')}
      onClose={onClose}
      closeLabel={t('common:action.close')}
      footer={
        <>
          <Button onClick={onClose}>{t('common:action.cancel')}</Button>
          <Button variant="primary" disabled={busy} onClick={save}>
            {t('common:action.save')}
          </Button>
        </>
      }
    >
      {field(t('opsversion:ds.name'), input(f.name, (v) => set('name', v), 'asia-dev-rancher'),
        t('opsversion:ds.nameHint'))}
      <div className="mb-2.5 flex gap-2">
        <Select
          label={t('opsversion:ds.type')}
          value={f.provider_type}
          onChange={(v) => {
            // 换类型时把认证方式也切到该类型支持的第一种 ——
            // 否则会留下一个该类型根本不支持的认证方式（如 kite + token）
            const allowed = AUTH_BY_PROVIDER[v] ?? []
            setF((p) => ({
              ...p,
              provider_type: v,
              auth_type: allowed.includes(p.auth_type) ? p.auth_type : (allowed[0] ?? ''),
            }))
          }}
          options={PROVIDERS.map((p) => ({ value: p, label: p }))}
        />
        <Select
          label={t('opsversion:org.authType')}
          value={f.auth_type}
          onChange={(v) => {
            // 切换认证方式时清掉另一种已填的值：两种凭据同时送到后端，
            // 用哪个取决于实现细节，而人以为自己只配了一种
            setF((p) => ({
              ...p,
              auth_type: v,
              username: v === 'password' ? p.username : '',
              password: v === 'password' ? p.password : '',
              api_key: isKeyAuth(v) ? p.api_key : '',
            }))
          }}
          options={(AUTH_BY_PROVIDER[f.provider_type] ?? []).map((a) => ({
            value: a,
            label: t(`opsversion:authType.${a}`),
          }))}
        />
      </div>
      {field(t('opsversion:ds.endpoint'), input(f.endpoint, (v) => set('endpoint', v), 'https://rancher.example.com'))}
      {field(
        t('opsversion:org.credential'),
        isKeyAuth(f.auth_type) ? (
          input(f.api_key, (v) => set('api_key', v), 'API Key', 'password')
        ) : (
          <div className="flex gap-2">
            {input(f.username, (v) => set('username', v), t('opsversion:login.username'))}
            {input(f.password, (v) => set('password', v), t('opsversion:login.password'), 'password')}
          </div>
        ),
        // ⚠️ 与平台/环境同一条规矩：留空 = 不改动。凭据永不回显，
        //    所以"框里是空的"是正常表现，不是"没配过"
        ds?.has_credential ? t('opsversion:org.credKeep') : t('opsversion:org.credNone'),
      )}
      <label className="mb-1.5 flex items-center gap-1.5 text-xs text-foreground">
        <input
          type="checkbox"
          checked={f.insecure_tls}
          onChange={(e) => set('insecure_tls', e.target.checked)}
        />
        {t('opsversion:org.insecureTls')}
      </label>
      <label className="flex items-center gap-1.5 text-xs text-foreground">
        <input type="checkbox" checked={f.enabled} onChange={(e) => set('enabled', e.target.checked)} />
        {t('opsversion:ds.enabled')}
      </label>
    </Dialog>
  )
}
