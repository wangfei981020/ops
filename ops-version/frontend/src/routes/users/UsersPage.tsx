import { useTranslation } from '@ops/i18n'
import {
  AsyncBoundary, Badge, type BadgeTone, Banner, Button, type ColumnDef, DataTable,
  Dialog, EmptyState, Select, TableSkeleton, fromQuery,
} from '@ops/ui'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api, toLoadError } from '../../lib/api.js'
import type { Session } from '../../lib/session.js'

interface User {
  id: number; username: string; display_name: string; email: string
  auth_source: string; role_code: string; visible_orgs: string
  enabled: boolean; last_login_at?: string
  /** 🔴 锁没锁**以后端为准**。前端不许拿 until 跟本地时间比 ——
   *  浏览器时钟和服务端差几分钟，两处显示就会不一致 */
  role_locked: boolean
  role_locked_until: string | null
  role_lock_reason: string
}
interface Role { code: string; name: string; builtin: boolean; perms: string[] | null }

/** 快到期或已过期的角色锁 */
interface ExpiringLock {
  id: number
  username: string
  display_name: string
  role_code: string
  reason: string
  locked_until?: string
  /** ⚠️ 由后端判定，前端不拿时间自己比 —— 浏览器时钟偏几分钟就会两处不一致 */
  expired: boolean
}

/**
 * 角色的显示名。
 *
 * 🔴 内置角色走 i18n（要跟界面语言走），**自定义角色只能用后端给的名字** ——
 *    它没有 i18n 文案，硬套 t() 会渲染成 `opsversion:role.notify_only` 这种原始 key。
 *
 * ⚠️ 收口成一个函数：这条规则要用在徽章、权限矩阵表头、编辑表单的下拉三处，
 *    各写一遍必然漏掉某一处 —— 实测就漏了后两处，界面上直接露出了 key。
 */
function roleLabelOf(
  roles: Role[] | undefined,
  code: string,
  t: (k: string) => string,
): string {
  const r = (roles ?? []).find((x) => x.code === code)
  if (r && !r.builtin) return r.name
  return t(`opsversion:role.${code}`)
}

const ROLE_TONE: Record<string, BadgeTone> = {
  super_admin: 'bad', admin: 'info', editor: 'ok', viewer: 'mute',
}
/**
 * 权限矩阵的行。顺序 = 权限从轻到重。
 *
 * ⚠️ 这只是**排序依据**，不是权限清单本身 —— 实际渲染的行取自
 * 各角色 perms 的并集（见 permRows），所以新增一项权限时这里忘了加，
 * 它照样会出现在矩阵里（排在最后），而不是凭空消失。
 */
const PERM_ORDER = ['view','export','refresh','plan.write','org.write',
                    'sync.trigger','alert.write','audit.view','user.admin'] as const

export function UsersPage({ session }: { session: Session }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<User | null | undefined>(undefined)
  const [err, setErr] = useState('')

  const q = useQuery({ queryKey: ['users'], queryFn: () => api<User[]>('/api/users') })
  // 🔴 角色锁到期提醒。这是「锁有期限」这件事**唯一被看见的途径** ——
  //    没有它，到期那天用户的角色静默变回组映射的值，
  //    管理员只会收到一句「他的权限怎么自己变了」。
  const expiring = useQuery({
    queryKey: ['role-locks-expiring'],
    queryFn: () => api<ExpiringLock[]>('/api/users/role-locks/expiring?days=30'),
  })
  const roles = useQuery({ queryKey: ['roles'], queryFn: () => api<Role[]>('/api/roles') })
  const reload = () => {
    qc.invalidateQueries({ queryKey: ['users'] })
    // 续期/解除之后提醒要跟着变，否则处理完了横幅还挂着
    qc.invalidateQueries({ queryKey: ['role-locks-expiring'] })
  }

  const [confirmDel, setConfirmDel] = useState<User | null>(null)
  const [locking, setLocking] = useState<User | null>(null)

  /** 矩阵要显示哪些权限行：所有角色 perms 的并集，按 PERM_ORDER 排，没排到的放最后 */
  const permRows = (() => {
    const all = new Set<string>()
    for (const r of roles.data ?? []) for (const p of r.perms ?? []) all.add(p)
    const idx = (p: string) => {
      const i = (PERM_ORDER as readonly string[]).indexOf(p)
      return i < 0 ? PERM_ORDER.length : i
    }
    return [...all].sort((a, b) => idx(a) - idx(b) || a.localeCompare(b))
  })()

  const roleLabel = (code: string) => roleLabelOf(roles.data, code, t)

  const columns: ColumnDef<User, unknown>[] = [
    { id: 'user', header: t('opsversion:user.title'), accessorFn: (r) => r.username,
      cell: ({ row }) => (
        <div>
          <span className="text-xs font-semibold text-foreground">{row.original.username}</span>
          <div className="text-[11px] text-muted-foreground">
            {row.original.display_name} {row.original.email}
          </div>
        </div>) },
    { id: 'src', header: t('opsversion:user.source'), accessorFn: (r) => r.auth_source,
      cell: ({ row }) => <Badge tone={row.original.auth_source === 'sso' ? 'info' : 'mute'}>
        {row.original.auth_source}</Badge> },
    { id: 'role', header: t('opsversion:user.roleCol'), accessorFn: (r) => r.role_code,
      cell: ({ row }) => (
        <div className="flex flex-wrap items-center gap-1">
          <Badge tone={ROLE_TONE[row.original.role_code] ?? 'mute'}>
            {roleLabel(row.original.role_code)}
          </Badge>
          {/* 🔴 锁定状态必须能看见。看不到的话，「这个人的角色为什么
              登录一次又变回去了」完全没有线索 —— 那正是加锁定之前的症状。 */}
          {row.original.role_locked && (
            <span
              className="cursor-help rounded border border-border px-1 py-0.5 text-[10px] text-muted-foreground"
              title={`${row.original.role_lock_reason}（${t('opsversion:user.lockUntil', {
                d: row.original.role_locked_until
                  ? new Date(row.original.role_locked_until).toLocaleDateString()
                  : '',
              })}）`}
            >
                {row.original.auth_source === 'sso'
                ? t('opsversion:user.locked')
                : t('opsversion:user.lockedInert')}
            </span>
          )}
        </div>
      ) },
    { id: 'vis', header: t('opsversion:user.visible'), accessorFn: (r) => r.visible_orgs,
      cell: ({ row }) => <span className="text-[11px] text-muted-foreground">
        {row.original.visible_orgs || t('opsversion:user.all')}</span> },
    { id: 'state', header: t('opsversion:user.state'), accessorFn: (r) => r.enabled,
      cell: ({ row }) => <Badge tone={row.original.enabled ? 'ok' : 'mute'}>
        {row.original.enabled ? t('opsversion:user.enabled') : t('opsversion:user.disabled')}</Badge> },
    { id: 'login', header: t('opsversion:user.lastLogin'), accessorFn: (r) => r.last_login_at ?? '',
      cell: ({ row }) => <span className="font-mono text-[11px] text-muted-foreground">
        {row.original.last_login_at
          ? new Date(row.original.last_login_at).toLocaleString() : t('opsversion:user.never')}</span> },
    { id: 'ops', header: '', accessorFn: () => '', meta: { action: true },
      cell: ({ row }) => (
        <div className="flex gap-1.5">
          <Button onClick={() => setEditing(row.original)}>{t('common:action.edit')}</Button>
          {/* 锁定只对 SSO 用户有意义（本地账号的角色没人会覆盖它），
              所以本地账号平时不显示这个按钮 —— 显示了只会让人以为自己漏了什么设置。

              🔴 但**已经锁着的一律要显示**，哪怕是本地账号。
                 只按 auth_source 判的话，一个本地账号上留着的锁会变成
                 「看得见、解不掉」——界面上挂着「角色已锁定」，却没有任何入口。
                 （账号从 SSO 转本地、或锁是通过接口设的，都会落到这个状态。）
                 凡是能进入的状态，都必须有出去的路。 */}
          {(row.original.auth_source === 'sso' || row.original.role_locked) && (
            <Button variant="ghost" onClick={() => setLocking(row.original)}>
              {row.original.role_locked
                ? t('opsversion:user.unlockRole')
                : t('opsversion:user.lockRole')}
            </Button>
          )}
          {/* 🔴 删除是硬删：不做「禁用即删除」的近似 ——
              禁用的账号还占着用户名和可见范围，两者语义不同，混在一起
              会让人以为删干净了，其实名字还被占着，重建时报「已存在」 */}
          <Button variant="ghost" onClick={() => setConfirmDel(row.original)}>
            {t('common:action.delete')}
          </Button>
        </div>) },
  ]

  const state = fromQuery(q, (d) => d.length === 0, (e) => toLoadError(e, t))

  return (
    <div className="flex flex-col gap-3 p-5">
      {err && <div className="rounded-md border border-danger bg-danger-bg px-3 py-2 text-xs text-danger">{err}</div>}
      {/* 🔴 已过期的用 bad、将到期的用 warn：
          已过期意味着**那个人的角色已经被改回去了**，是既成事实；
          将到期还只是提醒。两者混成一种颜色，等于把最要紧的那条淹掉。 */}
      {(expiring.data ?? []).length > 0 && (
        <Banner tone={(expiring.data ?? []).some((x) => x.expired) ? 'bad' : 'warn'}>
          <div className="space-y-1">
            <div>{t('opsversion:user.lockExpiringTitle', { n: (expiring.data ?? []).length })}</div>
            {(expiring.data ?? []).map((x) => (
              <div key={x.id} className="flex flex-wrap items-center gap-1.5 text-[11px]">
                <span className="font-semibold">{x.username}</span>
                <span>{roleLabel(x.role_code)}</span>
                <span className="text-muted-foreground">
                  {x.expired
                    ? t('opsversion:user.lockExpired')
                    : t('opsversion:user.lockExpiresOn', {
                        d: x.locked_until ? new Date(x.locked_until).toLocaleDateString() : '',
                      })}
                </span>
                {/* 理由要显示出来 —— 复核时看的就是这句话，
                    还得回列表里翻一遍的话，没人会去复核 */}
                {x.reason && <span className="text-muted-foreground">「{x.reason}」</span>}
                <Button
                  variant="ghost"
                  onClick={() => {
                    const u = (q.data ?? []).find((y) => y.id === x.id)
                    if (u) setLocking(u)
                  }}
                >
                  {t('opsversion:user.lockHandle')}
                </Button>
              </div>
            ))}
          </div>
        </Banner>
      )}

      <div className="flex items-center gap-2">
        <span className="text-sm font-semibold text-foreground">{t('opsversion:user.title')}</span>
        <span className="text-[11px] text-muted-foreground">{t('opsversion:user.hint')}</span>
        <div className="flex-1" />
        <Button variant="primary" onClick={() => setEditing(null)}>{t('opsversion:user.add')}</Button>
      </div>

      <AsyncBoundary
        state={state}
        pending={<TableSkeleton columns={[22,10,12,14,10,16,10]} rows={4} />}
        empty={<EmptyState title={t('opsversion:user.title')} reason={t('opsversion:user.hint')} action={null} />}
        errorTitle={t('opsversion:user.title')}
        retryLabel={t('common:action.retry')}
        onRetry={() => q.refetch()}
      >
        {(data) => <DataTable columns={columns} data={data} rowKey={(r) => String(r.id)} minWidth={860} />}
      </AsyncBoundary>

      {locking && (
        <RoleLockDialog
          user={locking}
          onClose={() => setLocking(null)}
          onDone={() => {
            setLocking(null)
            reload()
          }}
          onError={setErr}
        />
      )}

      {confirmDel && (
        <Dialog
          open
          onClose={() => setConfirmDel(null)}
          title={t('opsversion:user.delete')}
          description={t('opsversion:user.deleteHint')}
          closeLabel={t('common:action.close')}
          footer={
            <>
              <Button onClick={() => setConfirmDel(null)}>{t('common:action.cancel')}</Button>
              <Button
                variant="danger"
                onClick={async () => {
                  try {
                    await api(`/api/users/${confirmDel.id}`, { method: 'DELETE' })
                    setErr('')
                  } catch (e) {
                    setErr((e as Error).message)
                  }
                  setConfirmDel(null)
                  q.refetch()
                }}
              >
                {t('common:action.confirm')}
              </Button>
            </>
          }
        >
          <div className="text-sm text-foreground">{confirmDel.username}</div>
        </Dialog>
      )}

      {/* 角色权限矩阵。🔴 最后一行「查看凭据明文」四个角色全是 ✗ ——
          凭据只能覆盖写入，任何角色任何接口都不回显 */}
      <div className="rounded-lg border border-border bg-card p-3">
        <div className="mb-2 text-sm font-semibold text-foreground">{t('opsversion:user.permMatrix')}</div>
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="border-b border-border text-muted-foreground">
                <th className="py-1.5 text-left font-medium">{t('opsversion:user.permCol')}</th>
                {(roles.data ?? []).map((r) => (
                  <th key={r.code} className="py-1.5 text-left font-medium">{roleLabel(r.code)}</th>))}
              </tr>
            </thead>
            <tbody>
              {permRows.map((p) => (
                <tr key={p} className="border-b border-border/60">
                  <td className="py-1.5 font-mono text-foreground">
                    {p}
                    {p === 'sync.trigger' && (
                      <span className="ml-1.5"><Badge tone="bad">{t('opsversion:user.irreversible')}</Badge></span>)}
                  </td>
                  {(roles.data ?? []).map((r) => (
                    <td key={r.code} className="py-1.5">
                      {(r.perms ?? []).includes(p)
                        ? <span className="text-success">✓</span>
                        : <span className="text-muted-foreground">✗</span>}
                    </td>))}
                </tr>))}
              <tr className="bg-secondary/40">
                <td className="py-1.5 font-semibold text-foreground">{t('opsversion:user.credNever')}</td>
                {(roles.data ?? []).map((r) => (
                  <td key={r.code} className="py-1.5 font-semibold text-muted-foreground">✗</td>))}
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      {editing !== undefined && (
        <UserForm
          user={editing}
          roles={roles.data ?? []}
          onClose={() => setEditing(undefined)}
          onSaved={() => { setEditing(undefined); setErr(''); reload() }}
          onError={setErr}
        />)}
    </div>
  )
}

function UserForm({ user, roles, onClose, onSaved, onError }: {
  user: User | null; roles: Role[]
  onClose: () => void; onSaved: () => void; onError: (s: string) => void
}) {
  const { t } = useTranslation()
  const isEdit = !!user
  const [f, setF] = useState({
    username: user?.username ?? '', display_name: user?.display_name ?? '',
    email: user?.email ?? '', password: '',
    role_code: user?.role_code ?? 'viewer',
    visible_orgs: user?.visible_orgs ?? '',
    enabled: user?.enabled ?? true,
  })
  const set = (k: keyof typeof f, v: unknown) => setF((p) => ({ ...p, [k]: v }))
  const row = (label: string, node: React.ReactNode) => (
    <div className="mb-3 flex items-start gap-2">
      <span className="w-20 shrink-0 pt-1.5 text-right text-xs text-muted-foreground">{label}</span>
      <div className="min-w-0 flex-1">{node}</div>
    </div>)
  const input = (v: string, on: (s: string) => void, ph = '', type = 'text', disabled = false) => (
    <input type={type} value={v} placeholder={ph} disabled={disabled}
      onChange={(e) => on(e.target.value)}
      className="w-full rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground" />)

  return (
    <Dialog open onClose={onClose}
      title={isEdit ? `${t('common:action.edit')} · ${user.username}` : t('opsversion:user.add')}
      closeLabel={t('common:action.close')}
      footer={<>
        <Button onClick={onClose}>{t('common:action.cancel')}</Button>
        <Button variant="primary" onClick={async () => {
          try {
            await api(isEdit ? `/api/users/${user.id}` : '/api/users',
              { method: isEdit ? 'PUT' : 'POST', body: JSON.stringify(f) })
            onSaved()
          } catch (e) { onError((e as Error).message) }
        }}>{t('common:action.save')}</Button>
      </>}>
      {row(t('opsversion:login.username'), input(f.username, (v) => set('username', v), '', 'text', isEdit))}
      {row(t('opsversion:user.displayName'), input(f.display_name, (v) => set('display_name', v)))}
      {row(t('opsversion:user.email'), input(f.email, (v) => set('email', v)))}
      {row(t('opsversion:login.password'), input(f.password, (v) => set('password', v),
        isEdit ? t('opsversion:user.pwKeep') : '', 'password'))}
      {row(t('opsversion:user.roleCol'),
        <Select label={t('opsversion:user.roleCol')} value={f.role_code} onChange={(v) => set('role_code', v)}
          options={roles.map((r) => ({ value: r.code, label: roleLabelOf(roles, r.code, t) }))} />)}
      {row(t('opsversion:user.visible'), input(f.visible_orgs, (v) => set('visible_orgs', v), t('opsversion:user.visiblePh')))}
      {row('', <label className="flex items-center gap-1.5 text-xs text-foreground">
        <input type="checkbox" checked={f.enabled} onChange={(e) => set('enabled', e.target.checked)} />
        {t('opsversion:user.enabled')}</label>)}
      {/* 🔴 改角色会立刻踢掉该用户的全部会话 —— 角色在会话里，
          不踢的话降权看着像没生效，权限实际还留着 */}
      <p className="mt-2 text-[11px] text-muted-foreground">{t('opsversion:user.kickHint')}</p>
    </Dialog>
  )
}

/**
 * 角色锁定。
 *
 * 🔴 为什么锁：SSO 用户每次登录都按组重新映射角色。手工改的角色，
 * 下一次登录就被覆盖回去 —— 表现是「我明明改了，他登录一次又变回来了」，
 * 而且毫无提示。
 *
 * 🔴 为什么必须有期限：永久锁会**悄悄和 IdP 脱节**。人调岗了、从组里
 * 被移出去了，锁着的角色还在，而没有任何东西提醒你。到期日等于强制
 * 每隔一段时间重新确认「这个例外还成不成立」。
 */
function RoleLockDialog({
  user,
  onClose,
  onDone,
  onError,
}: {
  user: { id: number; username: string; role_code: string; role_locked: boolean; role_lock_reason: string }
  onClose: () => void
  onDone: () => void
  onError: (s: string) => void
}) {
  const { t } = useTranslation()
  // ⚠️ 默认 90 天**显式填进输入框**，不靠后端悄悄兜底 ——
  //    替人选一个复核周期而不告诉他，等于他在不知情的情况下接受了这个周期。
  const [days, setDays] = useState('90')
  const [reason, setReason] = useState(user.role_lock_reason)
  const [busy, setBusy] = useState(false)
  // 🔴 当前锁着 → 两个动作都要给：**续期**和**解除**。
  //    只给「解除」的话，从到期提醒点进来的人做不到最常见的那件事（续期），
  //    只能先解除再重新锁一遍 —— 中间那一下他的角色是跟着组走的。
  const locked = user.role_locked

  async function submit(action: 'lock' | 'unlock') {
    setBusy(true)
    try {
      await api(`/api/users/${user.id}/role-lock`, {
        method: 'PUT',
        body: JSON.stringify(
          action === 'unlock' ? { days: 0, reason: '' } : { days: Number(days), reason },
        ),
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
      title={locked ? t('opsversion:user.renewTitle') : t('opsversion:user.lockRole')}
      closeLabel={t('common:action.close')}
      footer={
        <>
          <Button onClick={onClose}>{t('common:action.cancel')}</Button>
          {locked && (
            <Button disabled={busy} onClick={() => submit('unlock')}>
              {t('opsversion:user.unlockRole')}
            </Button>
          )}
          <Button variant="primary" disabled={busy} onClick={() => submit('lock')}>
            {locked ? t('opsversion:user.renewAction') : t('common:action.confirm')}
          </Button>
        </>
      }
    >
      {
        <>
          <p className="mb-3 text-xs text-muted-foreground">
            {locked
              ? t('opsversion:user.renewHint', { user: user.username, role: user.role_code })
              : t('opsversion:user.lockHint', { user: user.username, role: user.role_code })}
          </p>
          <div className="mb-2.5">
            <div className="mb-1 text-[11px] text-muted-foreground">
              {t('opsversion:user.lockDays')}
            </div>
            <input
              type="number"
              min={1}
              max={365}
              value={days}
              onChange={(e) => setDays(e.target.value)}
              className="w-32 rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground"
            />
            <div className="mt-1 text-[11px] text-muted-foreground">
              {t('opsversion:user.lockDaysHint')}
            </div>
          </div>
          <div>
            <div className="mb-1 text-[11px] text-muted-foreground">
              {t('opsversion:user.lockReason')}
            </div>
            <input
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder={t('opsversion:user.lockReasonPh')}
              className="w-full rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground"
            />
            {/* 🔴 理由后端强制必填：到期复核时看到一条没有理由的锁，
                唯一能做的决定是「续期吧，万一有用呢」—— 那这个机制就废了 */}
            <div className="mt-1 text-[11px] text-muted-foreground">
              {t('opsversion:user.lockReasonHint')}
            </div>
          </div>
        </>
      }
    </Dialog>
  )
}
