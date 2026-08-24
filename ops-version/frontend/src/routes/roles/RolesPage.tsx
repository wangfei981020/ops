import { useTranslation } from '@ops/i18n'
import {
  AsyncBoundary, Badge, Banner, Button, type ColumnDef, DataTable,
  Dialog, EmptyState, TableSkeleton, fromQuery,
} from '@ops/ui'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api, toLoadError } from '../../lib/api.js'
import { type Session, can } from '../../lib/session.js'

/**
 * 角色管理。
 *
 * 🔴 内置四个角色**不可改不可删**：它们包含哪些权限是安全契约的一部分，
 * 出事之后要说得清当时 admin 到底能做什么。真要一个不一样的组合，
 * 就新建一个自定义角色 —— 那样历史上每个角色是什么，一直都是清楚的。
 */
interface Role {
  id: number
  code: string
  name: string
  perms: string[] | null
  builtin: boolean
  note: string
  /** 有多少人在用。删除前唯一想知道的事 */
  in_use: number
}

interface PermRow {
  code: string
}

export function RolesPage({ session }: { session: Session }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<Role | null | undefined>(undefined)
  const [removing, setRemoving] = useState<Role | null>(null)
  const [err, setErr] = useState('')

  const q = useQuery({ queryKey: ['admin-roles'], queryFn: () => api<Role[]>('/api/admin/roles') })
  // 🔴 权限清单从后端来，前端**不维护副本**：
  //    新增一项权限时前端忘了更新，那项权限就永远没人能勾上，
  //    而后端明明已经支持了 —— 而且这种缺失完全不报错。
  const perms = useQuery({ queryKey: ['admin-perms'], queryFn: () => api<PermRow[]>('/api/admin/perms') })
  const state = fromQuery(q, (d) => d.length === 0, (e) => toLoadError(e, t))
  const reload = () => {
    qc.invalidateQueries({ queryKey: ['admin-roles'] })
    // 角色变了，用户页和各处下拉都要跟着变
    qc.invalidateQueries({ queryKey: ['roles'] })
  }
  const canWrite = can(session, 'user.admin')

  const columns: ColumnDef<Role, unknown>[] = [
    {
      id: 'role',
      header: t('opsversion:roleAdmin.role'),
      accessorFn: (r) => r.code,
      cell: ({ row }) => (
        <div>
          <div className="flex items-center gap-1.5">
            <span className="text-xs font-semibold text-foreground">{row.original.name}</span>
            {row.original.builtin && <Badge tone="info">{t('opsversion:roleAdmin.builtin')}</Badge>}
          </div>
          <div className="font-mono text-[11px] text-muted-foreground">{row.original.code}</div>
        </div>
      ),
    },
    {
      id: 'perms',
      header: t('opsversion:roleAdmin.perms'),
      accessorFn: (r) => (r.perms ?? []).length,
      cell: ({ row }) => (
        <div className="flex flex-wrap gap-1">
          {(row.original.perms ?? []).map((p) => (
            <span
              key={p}
              className="rounded border border-border px-1 py-0.5 font-mono text-[10px] text-muted-foreground"
            >
              {p}
            </span>
          ))}
        </div>
      ),
    },
    {
      id: 'use',
      header: t('opsversion:roleAdmin.inUse'),
      accessorFn: (r) => r.in_use,
      cell: ({ row }) => (
        <span className="text-xs text-foreground">
          {t('opsversion:roleAdmin.inUseN', { n: row.original.in_use })}
        </span>
      ),
    },
    {
      id: 'note',
      header: t('opsversion:roleAdmin.note'),
      accessorFn: (r) => r.note,
      cell: ({ row }) => (
        <span className="text-[11px] text-muted-foreground">{row.original.note || '—'}</span>
      ),
    },
    {
      id: 'ops',
      header: '',
      meta: { action: true },
      cell: ({ row }) => (
        <div className="flex gap-1">
          {/* ⚠️ 内置角色的按钮**禁用而不是隐藏**：隐藏的话，人会以为
              「这一行怎么没有操作按钮」，而不是「这一行不允许改」。 */}
          <Button
            size="sm"
            disabled={!canWrite || row.original.builtin}
            title={row.original.builtin ? t('opsversion:roleAdmin.builtinLocked') : ''}
            onClick={() => setEditing(row.original)}
          >
            {t('common:action.edit')}
          </Button>
          <Button
            size="sm"
            disabled={!canWrite || row.original.builtin}
            title={row.original.builtin ? t('opsversion:roleAdmin.builtinLocked') : ''}
            onClick={() => setRemoving(row.original)}
          >
            {t('common:action.delete')}
          </Button>
        </div>
      ),
    },
  ]

  return (
    <div className="flex flex-col gap-3">
      {err ? <Banner tone="bad">{err}</Banner> : null}

      <div className="flex items-center gap-2">
        <div>
          <h1 className="text-base font-semibold text-foreground">{t('opsversion:roleAdmin.title')}</h1>
          <p className="mt-0.5 text-xs text-muted-foreground">{t('opsversion:roleAdmin.subtitle')}</p>
        </div>
        <div className="flex-1" />
        <Button variant="primary" disabled={!canWrite} onClick={() => setEditing(null)}>
          {t('opsversion:roleAdmin.add')}
        </Button>
      </div>

      <AsyncBoundary
        state={state}
        pending={<TableSkeleton columns={[22, 38, 12, 20, 12]} rows={4} />}
        empty={<EmptyState title={t('opsversion:roleAdmin.title')} reason="" action={null} />}
        errorTitle={t('opsversion:roleAdmin.title')}
        retryLabel={t('common:action.retry')}
        onRetry={() => q.refetch()}
      >
        {(data) => <DataTable columns={columns} data={data} rowKey={(r) => String(r.id)} minWidth={900} />}
      </AsyncBoundary>

      {editing !== undefined && (
        <RoleForm
          role={editing}
          allPerms={(perms.data ?? []).map((p) => p.code)}
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
          title={t('opsversion:roleAdmin.deleteTitle')}
          onClose={() => setRemoving(null)}
          closeLabel={t('common:action.close')}
          footer={
            <>
              <Button onClick={() => setRemoving(null)}>{t('common:action.cancel')}</Button>
              <Button
                variant="primary"
                onClick={async () => {
                  try {
                    await api(`/api/admin/roles/${removing.id}`, { method: 'DELETE' })
                    reload()
                  } catch (e) {
                    // 后端对「还有人在用」回 409 并给出可执行的下一步，原样带给用户
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
            {t('opsversion:roleAdmin.deleteConfirm', { name: removing.name })}
          </p>
          {removing.in_use > 0 && (
            <p className="mt-2 text-xs text-danger">
              {t('opsversion:roleAdmin.deleteInUse', { n: removing.in_use })}
            </p>
          )}
        </Dialog>
      )}
    </div>
  )
}

function RoleForm({
  role,
  allPerms,
  onClose,
  onSaved,
  onError,
}: {
  role: Role | null
  allPerms: string[]
  onClose: () => void
  onSaved: () => void
  onError: (s: string) => void
}) {
  const { t } = useTranslation()
  const isEdit = !!role
  const [f, setF] = useState({
    code: role?.code ?? '',
    name: role?.name ?? '',
    note: role?.note ?? '',
    perms: new Set(role?.perms ?? []),
  })
  const [busy, setBusy] = useState(false)

  async function save() {
    setBusy(true)
    try {
      await api(isEdit ? `/api/admin/roles/${role.id}` : '/api/admin/roles', {
        method: isEdit ? 'PUT' : 'POST',
        body: JSON.stringify({
          code: f.code,
          name: f.name,
          note: f.note,
          perms: [...f.perms],
        }),
      })
      onSaved()
    } catch (e) {
      onError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog
      open
      title={isEdit ? t('opsversion:roleAdmin.editTitle') : t('opsversion:roleAdmin.add')}
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
      <div className="mb-2.5">
        <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:roleAdmin.code')}</div>
        <input
          value={f.code}
          // 🔴 建好之后不许改 code：users.role_code 存的是它，
          //    改了之后那些人当场变成「未知角色 = 零权限」，而界面上看不出异常。
          disabled={isEdit}
          onChange={(e) => setF((p) => ({ ...p, code: e.target.value }))}
          placeholder="notify_only"
          className="w-full rounded-md border border-input bg-background px-2.5 py-1.5 font-mono text-xs text-foreground disabled:opacity-60"
        />
        <div className="mt-1 text-[11px] text-muted-foreground">
          {isEdit ? t('opsversion:roleAdmin.codeLocked') : t('opsversion:roleAdmin.codeHint')}
        </div>
      </div>

      <div className="mb-2.5">
        <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:roleAdmin.name')}</div>
        <input
          value={f.name}
          onChange={(e) => setF((p) => ({ ...p, name: e.target.value }))}
          placeholder={t('opsversion:roleAdmin.namePh')}
          className="w-full rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground"
        />
      </div>

      <div className="mb-2.5">
        <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:roleAdmin.perms')}</div>
        <div className="space-y-1 rounded-md border border-border p-2">
          {allPerms.map((p) => (
            <label key={p} className="flex items-start gap-2 text-xs text-foreground">
              <input
                type="checkbox"
                className="mt-0.5"
                checked={f.perms.has(p)}
                onChange={(e) => {
                  setF((prev) => {
                    const next = new Set(prev.perms)
                    if (e.target.checked) next.add(p)
                    else next.delete(p)
                    return { ...prev, perms: next }
                  })
                }}
              />
              <span>
                <span className="font-mono text-[11px]">{p}</span>
                <span className="ml-1.5 text-[11px] text-muted-foreground">
                  {t(`opsversion:permDesc.${p}`)}
                </span>
              </span>
            </label>
          ))}
        </div>
        {/* ⚠️ 说清楚 sync.trigger 的分量：它是真的往对方公司的 Harbor 推镜像，
            改配置错了能改回来，推镜像推不回来。 */}
        <div className="mt-1 text-[11px] text-muted-foreground">{t('opsversion:roleAdmin.permsHint')}</div>
      </div>

      <div>
        <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:roleAdmin.note')}</div>
        <input
          value={f.note}
          onChange={(e) => setF((p) => ({ ...p, note: e.target.value }))}
          placeholder={t('opsversion:roleAdmin.notePh')}
          className="w-full rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground"
        />
      </div>
    </Dialog>
  )
}
