import { useTranslation } from '@ops/i18n'
import {
  AsyncBoundary, Badge, Banner, Button, type ColumnDef, DataTable,
  Dialog, EmptyState, Select, TableSkeleton, fromQuery,
} from '@ops/ui'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { api, toLoadError } from '../../lib/api.js'
import type { Session } from '../../lib/session.js'

interface OidcConfig {
  enabled: boolean
  display_name: string
  issuer: string
  client_id: string
  has_secret: boolean
  authorize_url: string
  token_url: string
  userinfo_url: string
  scopes: string
  groups_claim: string
  username_claim: string
  default_role: string
  allow_unmapped: boolean
  insecure_tls: boolean
}
interface Mapping { id: number; group_value: string; role_code: string; note: string }

const ROLES = ['viewer', 'editor', 'admin', 'super_admin'] as const

export function SsoPage({ session }: { session: Session }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [err, setErr] = useState('')
  const [okMsg, setOkMsg] = useState('')
  const [editing, setEditing] = useState<Mapping | null>(null)
  const [removing, setRemoving] = useState<Mapping | null>(null)

  const cfgQ = useQuery({ queryKey: ['oidc-config'], queryFn: () => api<OidcConfig>('/api/oidc/config') })
  const mapQ = useQuery({ queryKey: ['oidc-mappings'], queryFn: () => api<Mapping[]>('/api/oidc/mappings') })

  const cfgState = fromQuery(cfgQ, () => false, (e) => toLoadError(e, t))
  const mapState = fromQuery(mapQ, (d) => d.length === 0, (e) => toLoadError(e, t))

  const [f, setF] = useState<OidcConfig | null>(null)
  const [secret, setSecret] = useState('')
  useEffect(() => { if (cfgQ.data) setF(cfgQ.data) }, [cfgQ.data])
  const set = <K extends keyof OidcConfig>(k: K, v: OidcConfig[K]) =>
    setF((p) => (p ? { ...p, [k]: v } : p))

  async function saveCfg() {
    if (!f) return
    setErr(''); setOkMsg('')
    try {
      await api('/api/oidc/config', { method: 'PUT', body: JSON.stringify({ ...f, client_secret: secret }) })
      setSecret('')
      setOkMsg(t('opsversion:sso.saved'))
      qc.invalidateQueries({ queryKey: ['oidc-config'] })
    } catch (e) { setErr((e as Error).message) }
  }

  const canWrite = session.perms.includes('user.admin')

  const cols: ColumnDef<Mapping>[] = [
    { accessorKey: 'group_value', header: t('opsversion:sso.groupValue') },
    {
      accessorKey: 'role_code',
      header: t('opsversion:sso.roleCode'),
      cell: ({ row }) => <Badge tone="mute">{t(`opsversion:role.${row.original.role_code}`)}</Badge>,
    },
    { accessorKey: 'note', header: t('opsversion:sso.note') },
    {
      id: 'ops',
      header: '',
      meta: { action: true },
      cell: ({ row }) => (
        <div className="flex gap-1">
          <Button size="sm" variant="ghost" disabled={!canWrite} onClick={() => setEditing(row.original)}>
            {t('common:action.edit')}
          </Button>
          <Button size="sm" variant="ghost" disabled={!canWrite} onClick={() => setRemoving(row.original)}>
            {t('common:action.delete')}
          </Button>
        </div>
      ),
    },
  ]

  const input = (v: string, on: (s: string) => void, ph = '', type = 'text') => (
    <input
      type={type} value={v} placeholder={ph}
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
    <div className="space-y-4">
      <div>
        <h1 className="text-base font-semibold text-foreground">{t('opsversion:sso.title')}</h1>
        <p className="mt-0.5 text-xs text-muted-foreground">{t('opsversion:sso.subtitle')}</p>
      </div>

      {err ? <Banner tone="bad">{err}</Banner> : null}
      {okMsg ? <Banner tone="info">{okMsg}</Banner> : null}

      <AsyncBoundary
        state={cfgState}
        pending={<TableSkeleton columns={[30, 30]} rows={6} />}
        empty={<EmptyState title={t('opsversion:sso.title')} reason={t('opsversion:sso.subtitle')}
          action={{ label: t('common:action.retry'), onClick: () => cfgQ.refetch() }} />}
        errorTitle={t('opsversion:sso.title')}
        retryLabel={t('common:action.retry')}
        onRetry={() => cfgQ.refetch()}
      >
        {() => (f ? (
          <div className="rounded-lg border border-border bg-card p-4">
            <div className="mb-3 flex items-center justify-between">
              <label className="flex items-center gap-1.5 text-xs font-medium text-foreground">
                <input type="checkbox" checked={f.enabled} disabled={!canWrite}
                  onChange={(e) => set('enabled', e.target.checked)} />
                {t('opsversion:sso.enable')}
              </label>
              <Button variant="primary" size="sm" disabled={!canWrite} onClick={saveCfg}>
                {t('common:action.save')}
              </Button>
            </div>

            <div className="grid gap-x-4 md:grid-cols-2">
              <div>
                {field(t('opsversion:sso.displayName'), input(f.display_name, (v) => set('display_name', v), 'SSO'),
                  t('opsversion:sso.displayNameHint'))}
                {field(t('opsversion:sso.issuer'), input(f.issuer, (v) => set('issuer', v), 'https://idp.example.com'))}
                {field(t('opsversion:sso.clientId'), input(f.client_id, (v) => set('client_id', v)))}
                {field(t('opsversion:sso.clientSecret'),
                  input(secret, setSecret, f.has_secret ? '' : t('opsversion:sso.secretNone'), 'password'),
                  f.has_secret ? t('opsversion:sso.secretKeep') : undefined)}
                {field(t('opsversion:sso.scopes'), input(f.scopes, (v) => set('scopes', v)))}
              </div>
              <div>
                {/* 🔴 三个端点必须能手填：实测 MXID 探不到 well-known，
                    只靠 issuer 自动发现会连不上，而报错只说"连接失败" */}
                {field(t('opsversion:sso.authorizeUrl'), input(f.authorize_url, (v) => set('authorize_url', v)))}
                {field(t('opsversion:sso.tokenUrl'), input(f.token_url, (v) => set('token_url', v)))}
                {field(t('opsversion:sso.userinfoUrl'), input(f.userinfo_url, (v) => set('userinfo_url', v)),
                  t('opsversion:sso.userinfoHint'))}
                {field(t('opsversion:sso.usernameClaim'), input(f.username_claim, (v) => set('username_claim', v)))}
                {field(t('opsversion:sso.groupsClaim'), input(f.groups_claim, (v) => set('groups_claim', v)),
                  t('opsversion:sso.groupsClaimHint'))}
              </div>
            </div>

            <div className="mt-2 grid gap-x-4 md:grid-cols-2">
              <div>
                <Select
                  label={t('opsversion:sso.defaultRole')}
                  value={f.default_role}
                  onChange={(v) => set('default_role', v)}
                  options={ROLES.map((r) => ({ value: r, label: t(`opsversion:role.${r}`) }))}
                />
                <div className="mt-1 text-[11px] text-muted-foreground">{t('opsversion:sso.defaultRoleHint')}</div>
              </div>
              <div className="pt-4">
                <label className="flex items-center gap-1.5 text-xs text-foreground">
                  <input type="checkbox" checked={f.allow_unmapped} disabled={!canWrite}
                    onChange={(e) => set('allow_unmapped', e.target.checked)} />
                  {t('opsversion:sso.allowUnmapped')}
                </label>
                <div className="mt-1 text-[11px] text-muted-foreground">{t('opsversion:sso.allowUnmappedHint')}</div>
                <label className="mt-2 flex items-center gap-1.5 text-xs text-foreground">
                  <input type="checkbox" checked={f.insecure_tls} disabled={!canWrite}
                    onChange={(e) => set('insecure_tls', e.target.checked)} />
                  {t('opsversion:org.insecureTls')}
                </label>
              </div>
            </div>
          </div>
        ) : null)}
      </AsyncBoundary>

      <div className="rounded-lg border border-border bg-card">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <div>
            <div className="text-xs font-semibold text-foreground">{t('opsversion:sso.mappings')}</div>
            <div className="text-[11px] text-muted-foreground">{t('opsversion:sso.mappingsHint')}</div>
          </div>
          <Button size="sm" variant="primary" disabled={!canWrite}
            onClick={() => setEditing({ id: 0, group_value: '', role_code: 'viewer', note: '' })}>
            {t('opsversion:sso.addMapping')}
          </Button>
        </div>
        <AsyncBoundary
          state={mapState}
          pending={<TableSkeleton columns={[30, 16, 30, 14]} rows={3} />}
          empty={<EmptyState title={t('opsversion:sso.noMapping')} reason={t('opsversion:sso.noMappingHint')}
            action={{ label: t('opsversion:sso.addMapping'),
              onClick: () => setEditing({ id: 0, group_value: '', role_code: 'viewer', note: '' }) }} />}
          errorTitle={t('opsversion:sso.mappings')}
          retryLabel={t('common:action.retry')}
          onRetry={() => mapQ.refetch()}
        >
          {(data) => <DataTable columns={cols} data={data} rowKey={(r) => String(r.id)} minWidth={720} />}
        </AsyncBoundary>
      </div>

      {editing ? (
        <MappingDialog
          value={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); qc.invalidateQueries({ queryKey: ['oidc-mappings'] }) }}
          onError={setErr}
        />
      ) : null}

      {removing ? (
        <Dialog open title={t('opsversion:sso.deleteTitle')} onClose={() => setRemoving(null)}
          closeLabel={t('common:action.close')}
          footer={
            <>
              <Button variant="ghost" onClick={() => setRemoving(null)}>{t('common:action.cancel')}</Button>
              <Button variant="danger" onClick={async () => {
                try {
                  await api(`/api/oidc/mappings/${removing.id}`, { method: 'DELETE' })
                  qc.invalidateQueries({ queryKey: ['oidc-mappings'] })
                } catch (e) { setErr((e as Error).message) }
                setRemoving(null)
              }}>{t('common:action.delete')}</Button>
            </>
          }>
          <p className="text-xs text-foreground">
            {t('opsversion:sso.deleteConfirm', { group: removing.group_value })}
          </p>
        </Dialog>
      ) : null}
    </div>
  )
}

function MappingDialog({ value, onClose, onSaved, onError }: {
  value: Mapping
  onClose: () => void
  onSaved: () => void
  onError: (s: string) => void
}) {
  const { t } = useTranslation()
  const [m, setM] = useState(value)
  const [busy, setBusy] = useState(false)

  async function save() {
    if (!m.group_value.trim()) { onError(t('opsversion:sso.groupRequired')); return }
    setBusy(true)
    try {
      await api(m.id ? `/api/oidc/mappings/${m.id}` : '/api/oidc/mappings',
        { method: m.id ? 'PUT' : 'POST', body: JSON.stringify(m) })
      onSaved()
    } catch (e) { onError((e as Error).message) } finally { setBusy(false) }
  }

  return (
    <Dialog open title={t('opsversion:sso.mappingTitle')} onClose={onClose}
      closeLabel={t('common:action.close')}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('common:action.cancel')}</Button>
          <Button variant="primary" disabled={busy} onClick={save}>{t('common:action.save')}</Button>
        </>
      }>
      <div className="space-y-2.5">
        <div>
          <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:sso.groupValue')}</div>
          <input
            value={m.group_value}
            onChange={(e) => setM({ ...m, group_value: e.target.value })}
            placeholder="ops-admin / ops-* / *"
            className="w-full rounded-md border border-input bg-background px-2.5 py-1.5 font-mono text-xs text-foreground"
          />
          <div className="mt-1 text-[11px] text-muted-foreground">{t('opsversion:sso.groupValueHint')}</div>
        </div>
        <Select
          label={t('opsversion:sso.roleCode')}
          value={m.role_code}
          onChange={(v) => setM({ ...m, role_code: v })}
          options={ROLES.map((r) => ({ value: r, label: t(`opsversion:role.${r}`) }))}
        />
        <div>
          <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:sso.note')}</div>
          <input
            value={m.note}
            onChange={(e) => setM({ ...m, note: e.target.value })}
            className="w-full rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground"
          />
        </div>
      </div>
    </Dialog>
  )
}
