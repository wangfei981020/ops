import { useTranslation } from '@ops/i18n'
import {
  AsyncBoundary, Badge, Banner, Button, type ColumnDef, DataTable,
  Dialog, EmptyState, Select, TableSkeleton, fromQuery,
} from '@ops/ui'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api, toLoadError } from '../../lib/api.js'
import type { Session } from '../../lib/session.js'

interface Token {
  id: number; name: string; prefix: string; role_code: string
  visible_orgs: string; enabled: boolean
  /** null = 永不过期。🔴 这是发给外部接入方和 AI 的长期凭据，必须看得见什么时候失效 */
  expires_at: string | null
  /** ⚠️ 由后端判定，前端不拿时间跟本地时钟比 */
  expired: boolean
  last_used_at: string | null; created_by: string; created_at: string
}
interface Role { code: string; name: string; builtin: boolean; perms: string[] | null }

export function TokensPage({ session }: { session: Session }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [creating, setCreating] = useState(false)
  const [issued, setIssued] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<Token | null>(null)
  const [expiring, setExpiring] = useState<Token | null>(null)
  const [err, setErr] = useState('')
  const [copied, setCopied] = useState(false)

  const q = useQuery({ queryKey: ['mcp-tokens'], queryFn: () => api<Token[]>('/api/mcp-tokens') })
  const roles = useQuery({ queryKey: ['roles'], queryFn: () => api<Role[]>('/api/roles') })
  const reload = () => qc.invalidateQueries({ queryKey: ['mcp-tokens'] })

  const columns: ColumnDef<Token, unknown>[] = [
    { id: 'name', header: t('opsversion:token.title'), accessorFn: (r) => r.name,
      cell: ({ row }) => <span className="text-xs font-semibold text-foreground">{row.original.name}</span> },
    { id: 'prefix', header: t('opsversion:token.prefix'), accessorFn: (r) => r.prefix,
      // 🔴 列表里只有前缀。完整令牌在创建时显示一次，之后任何接口都拿不到 ——
      //    能被读出来的令牌等于没有令牌
      cell: ({ row }) => <span className="font-mono text-[11px] text-muted-foreground">{row.original.prefix}…</span> },
    { id: 'role', header: t('opsversion:user.roleCol'), accessorFn: (r) => r.role_code,
      // 自定义角色没有 i18n 文案，只能用后端给的名字（内置的走 i18n）
      cell: ({ row }) => {
        const r = (roles.data ?? []).find((x) => x.code === row.original.role_code)
        return <Badge tone="info">
          {r && !r.builtin ? r.name : t(`opsversion:role.${row.original.role_code}`)}
        </Badge>
      } },
    { id: 'vis', header: t('opsversion:user.visible'), accessorFn: (r) => r.visible_orgs,
      cell: ({ row }) => <span className="text-[11px] text-muted-foreground">
        {row.original.visible_orgs || t('opsversion:user.all')}</span> },
    { id: 'state', header: t('opsversion:user.state'), accessorFn: (r) => r.enabled,
      // 🔴 三态，不是两态：吊销 / 已过期 / 有效。
      //    过期的令牌 enabled 仍是 1，只显示「有效」的话，
      //    对方报「用不了」时你看着界面说「明明是有效的」—— 而接口早就在拒了。
      cell: ({ row }) => row.original.expired
        ? <Badge tone="bad">{t('opsversion:token.expired')}</Badge>
        : <Badge tone={row.original.enabled ? 'ok' : 'mute'}>
            {row.original.enabled ? t('opsversion:token.valid') : t('opsversion:token.revoked')}</Badge> },
    { id: 'exp', header: t('opsversion:token.expiresAt'), accessorFn: (r) => r.expires_at ?? '',
      cell: ({ row }) => row.original.expires_at
        ? <span className="font-mono text-[11px] text-muted-foreground">
            {new Date(row.original.expires_at).toLocaleDateString()}</span>
        // ⚠️ 「永不过期」要标出来而不是显示「—」：那是一个需要有人决定的状态，
        //    不是「没填」。之前发的令牌全是这种。
        : <Badge tone="warn">{t('opsversion:token.never')}</Badge> },
    { id: 'used', header: t('opsversion:token.lastUsed'), accessorFn: (r) => r.last_used_at ?? '',
      cell: ({ row }) => <span className="font-mono text-[11px] text-muted-foreground">
        {row.original.last_used_at ? new Date(row.original.last_used_at).toLocaleString() : t('opsversion:user.never')}</span> },
    { id: 'ops', header: '', accessorFn: () => '', meta: { action: true },
      cell: ({ row }) => row.original.enabled
        ? <div className="flex gap-1">
            <Button onClick={() => setExpiring(row.original)}>{t('opsversion:token.setExpiry')}</Button>
            <Button onClick={() => setRevoking(row.original)}>{t('opsversion:token.revoke')}</Button>
          </div>
        : <span className="text-[11px] text-muted-foreground">—</span> },
  ]

  const state = fromQuery(q, (d) => d.length === 0, (e) => toLoadError(e, t))

  return (
    <div className="flex flex-col gap-3 p-5">
      {err && <div className="rounded-md border border-danger bg-danger-bg px-3 py-2 text-xs text-danger">{err}</div>}
      <div className="flex items-center gap-2">
        <span className="text-sm font-semibold text-foreground">{t('opsversion:token.title')}</span>
        <span className="text-[11px] text-muted-foreground">{t('opsversion:token.hint')}</span>
        <div className="flex-1" />
        <Button variant="primary" onClick={() => setCreating(true)}>{t('opsversion:token.add')}</Button>
      </div>

      <AsyncBoundary
        state={state}
        pending={<TableSkeleton columns={[20,14,12,14,10,18,10]} rows={3} />}
        empty={<EmptyState title={t('opsversion:token.empty')} reason={t('opsversion:token.hint')}
          action={{ label: t('opsversion:token.add'), onClick: () => setCreating(true) }} />}
        errorTitle={t('opsversion:token.title')}
        retryLabel={t('common:action.retry')}
        onRetry={() => q.refetch()}
      >
        {(data) => <DataTable columns={columns} data={data} rowKey={(r) => String(r.id)} minWidth={880} />}
      </AsyncBoundary>

      <div className="rounded-lg border border-border bg-card p-3 text-[11px] text-muted-foreground">
        {t('opsversion:token.usage')}
      </div>

      {creating && <IssueForm roles={roles.data ?? []} onClose={() => setCreating(false)}
        onIssued={(tk) => { setCreating(false); setIssued(tk); reload() }} onError={setErr} />}

      {issued && (
        <Dialog open onClose={() => setIssued(null)} title={t('opsversion:token.issued')}
          closeLabel={t('common:action.close')}
          footer={<Button variant="primary" onClick={() => setIssued(null)}>{t('opsversion:token.saved')}</Button>}>
          <Banner tone="bad">{t('opsversion:token.onceOnly')}</Banner>
          {/* 🔴 「只显示一次」配上「只能手工选中复制」是最容易丢东西的组合：
              选漏一个字符、或者手滑关掉弹窗，令牌就永久拿不回来了，只能重发一个。
              一键复制不是锦上添花，是这个弹窗的必备件。 */}
          <div className="mt-3 flex items-start gap-2">
            <div className="flex-1 break-all rounded-md border border-border bg-background p-3 font-mono text-sm text-foreground">
              {issued}
            </div>
            <Button
              variant="primary"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(issued)
                  setCopied(true)
                  setTimeout(() => setCopied(false), 2000)
                } catch {
                  // 非 HTTPS 或浏览器拒绝时会抛 —— 不能假装成功，
                  // 否则人以为复制到了、关掉弹窗才发现剪贴板是空的
                  setCopied(false)
                  setErr(t('opsversion:token.copyFailed'))
                }
              }}
            >
              {copied ? t('opsversion:token.copied') : t('opsversion:token.copy')}
            </Button>
          </div>
        </Dialog>)}

      {expiring && (
        <ExpiryDialog
          token={expiring}
          onClose={() => setExpiring(null)}
          onDone={() => {
            setExpiring(null)
            reload()
          }}
          onError={setErr}
        />
      )}

      {revoking && (
        <Dialog open onClose={() => setRevoking(null)} title={t('opsversion:token.revoke')}
          closeLabel={t('common:action.close')}
          footer={<>
            <Button onClick={() => setRevoking(null)}>{t('common:action.cancel')}</Button>
            <Button variant="primary" onClick={async () => {
              try { await api(`/api/mcp-tokens/${revoking.id}`, { method: 'DELETE' }) }
              catch (e) { setErr((e as Error).message) }
              setRevoking(null); reload()
            }}>{t('common:action.confirm')}</Button>
          </>}>
          <p className="text-sm text-foreground">{t('opsversion:token.revokeConfirm', { name: revoking.name })}</p>
        </Dialog>)}
    </div>
  )
}

function IssueForm({ roles, onClose, onIssued, onError }: {
  roles: Role[]; onClose: () => void; onIssued: (t: string) => void; onError: (s: string) => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  // 默认最小权限。没权限的工具**不会出现在 tools/list 里** ——
  // 列出来再拒绝会让 AI 反复重试并把授权问题当成故障
  const [role, setRole] = useState('viewer')
  const [vis, setVis] = useState('')
  // 🔴 有效期**显式预填 90 天**，而不是让后端悄悄兜底。
  //    的成因正是「写入侧不支持设置」→ 全部令牌永不过期。
  //    替人选一个复核周期而不告诉他，等于他在不知情的情况下接受了它。
  const [days, setDays] = useState('90')

  return (
    <Dialog open onClose={onClose} title={t('opsversion:token.add')} closeLabel={t('common:action.close')}
      footer={<>
        <Button onClick={onClose}>{t('common:action.cancel')}</Button>
        <Button variant="primary" onClick={async () => {
          if (!name.trim()) { onError(t('opsversion:token.nameRequired')); return }
          try {
            const r = await api<{ token: string }>('/api/mcp-tokens',
              { method: 'POST', body: JSON.stringify({ name, role, visible_orgs: vis, days: Number(days) }) })
            onIssued(r.token)
          } catch (e) { onError((e as Error).message) }
        }}>{t('common:action.confirm')}</Button>
      </>}>
      <div className="mb-3 flex items-start gap-2">
        <span className="w-20 shrink-0 pt-1.5 text-right text-xs text-muted-foreground">{t('opsversion:token.name')}</span>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('opsversion:token.namePh')}
          className="min-w-0 flex-1 rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground" />
      </div>
      <div className="mb-3 flex items-start gap-2">
        <span className="w-20 shrink-0 pt-1.5 text-right text-xs text-muted-foreground">{t('opsversion:user.roleCol')}</span>
        <Select label={t('opsversion:user.roleCol')} value={role} onChange={setRole}
          options={roles.map((r) => ({
            value: r.code,
            // 自定义角色没有 i18n 文案，只能用后端给的名字
            label: r.builtin === false ? r.name : t(`opsversion:role.${r.code}`),
          }))} />
      </div>
      <div className="mb-1 flex items-start gap-2">
        <span className="w-20 shrink-0 pt-1.5 text-right text-xs text-muted-foreground">{t('opsversion:user.visible')}</span>
        <input value={vis} onChange={(e) => setVis(e.target.value)} placeholder={t('opsversion:user.visiblePh')}
          className="min-w-0 flex-1 rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground" />
      </div>
      <div className="mb-1 mt-3 flex items-start gap-2">
        <span className="w-20 shrink-0 pt-1.5 text-right text-xs text-muted-foreground">
          {t('opsversion:token.expiryLabel')}
        </span>
        <div className="min-w-0 flex-1">
          <input type="number" min={0} max={365} value={days}
            onChange={(e) => setDays(e.target.value)}
            className="w-32 rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground" />
          {/* ⚠️ 填 0 = 永不过期。允许，但要是个**看得见的选择** ——
              所以这里当场标出来，而不是等发完了才在列表里显示 */}
          <div className="mt-1 text-[11px] text-muted-foreground">
            {Number(days) === 0 ? t('opsversion:token.expiryNeverWarn') : t('opsversion:token.daysHint')}
          </div>
        </div>
      </div>
      <p className="mt-2 text-[11px] text-muted-foreground">{t('opsversion:token.minPerm')}</p>
    </Dialog>
  )
}

/**
 * 给一条**已发出**的令牌设置有效期。
 *
 * 🔴 存在的理由：之前发的令牌 expires_at 全是 NULL（永不过期），
 * 而它们已经在外部接入方和 AI 的配置里。只让新令牌能设期限的话，
 * 那批永久令牌会一直存在 —— 问题只解决一半。
 */
function ExpiryDialog({
  token,
  onClose,
  onDone,
  onError,
}: {
  token: { id: number; name: string; expires_at: string | null }
  onClose: () => void
  onDone: () => void
  onError: (s: string) => void
}) {
  const { t } = useTranslation()
  // ⚠️ 默认 90 天**显式填进输入框**，不靠后端悄悄兜底 ——
  //    替人选一个复核周期而不告诉他，等于他在不知情的情况下接受了它。
  const [days, setDays] = useState('90')
  const [busy, setBusy] = useState(false)

  async function submit(d: number) {
    setBusy(true)
    try {
      await api(`/api/mcp-tokens/${token.id}/expiry`, {
        method: 'PUT',
        body: JSON.stringify({ days: d }),
      })
      onDone()
    } catch (e) {
      onError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title={t('opsversion:token.setExpiry')}
      closeLabel={t('common:action.close')}
      footer={
        <>
          <Button onClick={onClose}>{t('common:action.cancel')}</Button>
          {/* 「永不过期」允许，但必须是一个**显式动作** ——
              不能是默认值悄悄变成的。这正是 的成因。 */}
          <Button disabled={busy} onClick={() => submit(0)}>
            {t('opsversion:token.setNever')}
          </Button>
          <Button variant="primary" disabled={busy} onClick={() => submit(Number(days))}>
            {t('common:action.confirm')}
          </Button>
        </>
      }
    >
      <p className="mb-3 text-xs text-muted-foreground">
        {t('opsversion:token.expiryHint', { name: token.name })}
      </p>
      <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:token.days')}</div>
      <input
        type="number"
        min={1}
        max={365}
        value={days}
        onChange={(e) => setDays(e.target.value)}
        className="w-32 rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground"
      />
      <div className="mt-1 text-[11px] text-muted-foreground">{t('opsversion:token.daysHint')}</div>
    </Dialog>
  )
}
