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
  MultiSelect,
  Select,
  TableSkeleton,
  fromQuery,
} from '@ops/ui'
import { type UseQueryResult, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api, toLoadError } from '../../lib/api.js'
import { type Session, can } from '../../lib/session.js'
import type { Org } from '../orgs/types.js'
import type { Channel, Contact, NotifyRecord } from './types.js'

/**
 * 投递结果 → 语气。
 *
 * 🔴 `skipped` 用 mute（灰）而不是 warn：按规则不发是**正常行为**
 * （自动同步成功一天几十次，全发出来会让群被设成免打扰，
 * 那才是真正的危险）。把它标黄会让人以为漏发了，然后来问。
 */
const STATE_TONE: Record<string, BadgeTone> = {
  sent: 'ok',
  skipped: 'mute',
  failed: 'bad',
}

export function NotifySection({ session }: { session: Session }) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState<Channel | null | undefined>(undefined)
  const [confirmDel, setConfirmDel] = useState<Channel | null>(null)
  const [msg, setMsg] = useState<string>('')
  const [busy, setBusy] = useState(false)

  const chans = useQuery({
    queryKey: ['channels'],
    queryFn: () => api<Channel[]>('/api/notify/channels'),
  })
  const records = useQuery({
    queryKey: ['notifyRecords'],
    queryFn: () => api<NotifyRecord[]>('/api/notify/records?limit=100'),
  })
  const orgs = useQuery({ queryKey: ['orgs'], queryFn: () => api<Org[]>('/api/orgs') })
  const contacts = useQuery({
    queryKey: ['notifyContacts'],
    queryFn: () => api<Contact[]>('/api/notify/contacts'),
  })
  const contactName = new Map((contacts.data ?? []).map((c) => [c.id, c]))

  async function test(c: Channel) {
    setBusy(true)
    try {
      const r = await api<{ ok: boolean; message: string }>(
        `/api/notify/channels/${c.id}/test`,
        { method: 'POST' },
      )
      setMsg(r.message)
    } catch (e) {
      setMsg((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const chanCols: ColumnDef<Channel, unknown>[] = [
    {
      id: 'name',
      header: t('opsversion:notify.channel'),
      accessorFn: (r) => r.name,
      cell: ({ row }) => (
        <div>
          <div className="text-xs font-semibold text-foreground">
            {row.original.name}{' '}
            <Badge tone="mute">
              {row.original.kind === 'teams' ? t('opsversion:notify.kindTeams') : t('opsversion:notify.kindLark')}
            </Badge>
          </div>
          <div className="text-[11px] text-muted-foreground">
            {row.original.org_name || t('opsversion:notify.allOrgs')}
          </div>
        </div>
      ),
    },
    {
      id: 'webhook',
      header: 'Webhook',
      accessorFn: (r) => r.has_webhook,
      cell: ({ row }) => (
        <Badge tone={row.original.has_webhook ? 'ok' : 'warn'}>
          {row.original.has_webhook
            ? t('opsversion:notify.webhookSet')
            : t('opsversion:notify.webhookMissing')}
        </Badge>
      ),
    },
    {
      id: 'mentions',
      header: t('opsversion:notify.mentionsCol'),
      accessorFn: (r) => (r.mention_ids ?? []).length,
      cell: ({ row }) => {
        // 飞书渠道不 @，显示「-」而不是「不 @」，免得以为是漏配
        if (row.original.kind !== 'teams') return <span className="text-[11px] text-muted-foreground">-</span>
        const people = (row.original.mention_ids ?? [])
          .map((id) => contactName.get(id))
          .filter((c): c is Contact => !!c && c.enabled)
        return people.length === 0 ? (
          <span className="text-[11px] text-muted-foreground">{t('opsversion:notify.mentionsNone')}</span>
        ) : (
          <span className="text-[11px] text-foreground" title={people.map((c) => c.teams_account).join('\n')}>
            {people.map((c) => c.name).join('、')}
          </span>
        )
      },
    },
    {
      id: 'enabled',
      header: t('opsversion:notify.state'),
      accessorFn: (r) => r.enabled,
      cell: ({ row }) => (
        <Badge tone={row.original.enabled ? 'ok' : 'mute'}>
          {row.original.enabled ? t('opsversion:notify.on') : t('opsversion:notify.off')}
        </Badge>
      ),
    },
    {
      id: 'ops',
      header: '',
      accessorFn: () => '',
      meta: { action: true },
      cell: ({ row }) =>
        can(session, 'alert.write') ? (
          <div className="flex gap-1.5">
            <Button onClick={() => test(row.original)} disabled={busy}>
              {t('opsversion:notify.test')}
            </Button>
            <Button onClick={() => setEditing(row.original)}>{t('common:action.edit')}</Button>
            <Button variant="ghost" onClick={() => setConfirmDel(row.original)}>
              {t('common:action.delete')}
            </Button>
          </div>
        ) : null,
    },
  ]

  const recCols: ColumnDef<NotifyRecord, unknown>[] = [
    {
      id: 'when',
      header: t('opsversion:notify.time'),
      accessorFn: (r) => r.created_at,
      cell: ({ row }) => (
        <span className="text-[11px] whitespace-nowrap text-muted-foreground">
          {new Date(row.original.created_at).toLocaleString()}
        </span>
      ),
    },
    {
      id: 'state',
      header: t('opsversion:notify.result'),
      accessorFn: (r) => r.state,
      cell: ({ row }) => (
        <div>
          <Badge tone={STATE_TONE[row.original.state] ?? 'mute'}>
            {t(`opsversion:notifyState.${row.original.state}`)}
          </Badge>
          {row.original.attempts > 1 && (
            <div className="text-[10.5px] text-muted-foreground">
              {t('opsversion:notify.attempts', { n: row.original.attempts })}
            </div>
          )}
        </div>
      ),
    },
    {
      id: 'reason',
      header: t('opsversion:notify.reason'),
      accessorFn: (r) => r.reason,
      cell: ({ row }) => (
        <div className="max-w-[420px]">
          {/* 🔴 「为什么没发」直接显示出来 —— 这是人来问时唯一想知道的事 */}
          <div className="text-[11px] text-muted-foreground">{row.original.reason}</div>
          {row.original.err_msg && (
            <div className="text-[11px] text-danger">{row.original.err_msg}</div>
          )}
        </div>
      ),
    },
    {
      id: 'level',
      header: t('opsversion:notify.level'),
      accessorFn: (r) => r.level,
      cell: ({ row }) => (
        <Badge tone={row.original.level === 'failed' ? 'bad' : 'mute'}>
          {t(`opsversion:notifyLevel.${row.original.level || 'ok'}`)}
        </Badge>
      ),
    },
  ]

  return (
    <div className="flex flex-col gap-4">
      {msg && (
        <div className="rounded-md border border-border bg-card px-3 py-2 text-xs text-foreground">
          {msg}
          <button
            type="button"
            className="ml-2 cursor-pointer text-muted-foreground underline"
            onClick={() => setMsg('')}
          >
            {t('common:action.close')}
          </button>
        </div>
      )}

      <div>
        <div className="mb-1.5 flex flex-wrap items-center gap-2">
          <span className="text-sm font-semibold text-foreground">
            {t('opsversion:notify.title')}
          </span>
          <span className="text-[11px] text-muted-foreground">{t('opsversion:notify.hint')}</span>
          <div className="flex-1" />
          {can(session, 'alert.write') && (
            <Button variant="primary" onClick={() => setEditing(null)}>
              {t('opsversion:notify.add')}
            </Button>
          )}
        </div>
        <AsyncBoundary
          state={fromQuery(chans, (d) => d.length === 0, (e) => toLoadError(e, t))}
          pending={<TableSkeleton columns={[40, 20, 20, 20]} rows={2} />}
          // 紧凑空态：这一块在设置弹窗里，和另外两块并列 ——
          // 三个大空态叠起来要滚很久，而内容全是「还没有…」
          empty={
            <div className="rounded-md border border-dashed border-border-strong px-3 py-2.5 text-[11px] text-muted-foreground">
              {t('opsversion:notify.emptyHint')}
            </div>
          }
          errorTitle={t('opsversion:notify.title')}
          retryLabel={t('common:action.retry')}
          onRetry={() => chans.refetch()}
        >
          {(data) => (
            <DataTable columns={chanCols} data={data} rowKey={(r) => String(r.id)} minWidth={720} />
          )}
        </AsyncBoundary>
      </div>

      <ContactsSection
        session={session}
        contacts={contacts}
        onError={setMsg}
        onChanged={() => {
          contacts.refetch()
          chans.refetch()
        }}
      />

      <div>
        <div className="mb-1.5 flex items-center gap-2">
          <span className="text-sm font-semibold text-foreground">
            {t('opsversion:notify.records')}
          </span>
          <span className="text-[11px] text-muted-foreground">
            {t('opsversion:notify.recordsHint')}
          </span>
        </div>
        <AsyncBoundary
          state={fromQuery(records, (d) => d.length === 0, (e) => toLoadError(e, t))}
          pending={<TableSkeleton columns={[20, 15, 45, 15]} rows={3} />}
          empty={
            <div className="rounded-md border border-dashed border-border-strong px-3 py-2.5 text-[11px] text-muted-foreground">
              {t('opsversion:notify.noRecordsHint')}
            </div>
          }
          errorTitle={t('opsversion:notify.records')}
          retryLabel={t('common:action.retry')}
          onRetry={() => records.refetch()}
        >
          {(data) => (
            <DataTable columns={recCols} data={data} rowKey={(r) => String(r.id)} minWidth={720} />
          )}
        </AsyncBoundary>
      </div>

      {editing !== undefined && (
        <ChannelForm
          channel={editing}
          orgs={orgs.data ?? []}
          contacts={contacts.data ?? []}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined)
            chans.refetch()
          }}
          onError={setMsg}
        />
      )}

      {confirmDel && (
        <Dialog
          open
          onClose={() => setConfirmDel(null)}
          title={t('opsversion:notify.delete')}
          description={t('opsversion:notify.deleteHint')}
          closeLabel={t('common:action.close')}
          footer={
            <>
              <Button onClick={() => setConfirmDel(null)}>{t('common:action.cancel')}</Button>
              <Button
                variant="danger"
                onClick={async () => {
                  try {
                    await api(`/api/notify/channels/${confirmDel.id}`, { method: 'DELETE' })
                  } catch (e) {
                    setMsg((e as Error).message)
                  }
                  setConfirmDel(null)
                  chans.refetch()
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

function ChannelForm({
  channel,
  orgs,
  contacts,
  onClose,
  onSaved,
  onError,
}: {
  channel: Channel | null
  orgs: Org[]
  contacts: Contact[]
  onClose: () => void
  onSaved: () => void
  onError: (m: string) => void
}) {
  const { t } = useTranslation()
  const isEdit = channel !== null
  const [f, setF] = useState({
    name: channel?.name ?? '',
    kind: channel?.kind ?? 'lark',
    webhook: '',
    enabled: channel?.enabled ?? true,
    org_id: channel?.org_id ?? 0,
  })
  // 单独一个 state：MultiSelect 要的是 setState（见组件注释）
  const [mentions, setMentions] = useState<string[]>(
    (channel?.mention_ids ?? []).map(String),
  )
  const [busy, setBusy] = useState(false)

  async function save() {
    if (!f.name.trim()) {
      onError(t('opsversion:notify.nameRequired'))
      return
    }
    setBusy(true)
    try {
      await api(isEdit ? `/api/notify/channels/${channel.id}` : '/api/notify/channels', {
        method: isEdit ? 'PUT' : 'POST',
        body: JSON.stringify({
          ...f,
          // 飞书渠道不 @，后端也会清掉；这里照实发空
          mention_ids: f.kind === 'teams' ? mentions.map(Number) : [],
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
      onClose={onClose}
      title={isEdit ? `${t('common:action.edit')} · ${channel.name}` : t('opsversion:notify.add')}
      closeLabel={t('common:action.close')}
      footer={
        <>
          <Button onClick={onClose}>{t('common:action.cancel')}</Button>
          <Button variant="primary" onClick={save} disabled={busy}>
            {t('common:action.save')}
          </Button>
        </>
      }
    >
      <label className="mb-2.5 flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">{t('opsversion:notify.name')}</span>
        <input
          value={f.name}
          onChange={(e) => setF((p) => ({ ...p, name: e.target.value }))}
          className="w-full rounded-md border border-border bg-background px-2 py-1.5 text-sm text-foreground"
        />
      </label>
      <div className="mb-2.5">
        <Select
          label={t('opsversion:notify.kind')}
          value={f.kind}
          onChange={(v) => setF((p) => ({ ...p, kind: v || 'lark' }))}
          options={[
            { value: 'lark', label: t('opsversion:notify.kindLark') },
            { value: 'teams', label: t('opsversion:notify.kindTeams') },
          ]}
        />
      </div>
      <label className="mb-2.5 flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">Webhook</span>
        <input
          type="password"
          value={f.webhook}
          placeholder={
            f.kind === 'teams'
              ? 'https://….environment.api.powerplatform.com:443/powerautomate/…'
              : 'https://open.feishu.cn/open-apis/bot/v2/hook/…'
          }
          onChange={(e) => setF((p) => ({ ...p, webhook: e.target.value }))}
          className="w-full rounded-md border border-border bg-background px-2 py-1.5 text-sm text-foreground"
        />
        {/* 🔴 webhook 等同于凭据：它一旦泄露，任何人都能往群里发消息 */}
        <span className="text-[11px] text-muted-foreground">
          {isEdit ? t('opsversion:notify.webhookKeep') : f.kind === 'teams' ? t('opsversion:notify.teamsHint') : t('opsversion:notify.webhookHint')}
          {isEdit && f.kind !== channel.kind ? ` · ${t('opsversion:notify.kindChangeHint')}` : ''}
        </span>
      </label>
      <div className="mb-2.5">
        <Select
          label={t('opsversion:notify.scope')}
          value={f.org_id ? String(f.org_id) : ''}
          onChange={(v) => setF((p) => ({ ...p, org_id: v ? Number(v) : 0 }))}
          options={[
            { value: '', label: t('opsversion:notify.allOrgs') },
            ...orgs.map((o) => ({ value: String(o.id), label: o.name })),
          ]}
        />
      </div>
      {f.kind === 'teams' && (
        <div className="mb-2.5 flex flex-col gap-1">
          <MultiSelect
            label={t('opsversion:notify.mentions')}
            value={mentions}
            onChange={setMentions}
            options={contacts.map((c) => ({
              value: String(c.id),
              label: `${c.name}（${c.teams_account}）${c.enabled ? '' : ` · ${t('opsversion:notify.off')}`}`,
            }))}
            placeholder={t('opsversion:notify.mentionsPick')}
            summary={(n) => t('opsversion:notify.mentionsPicked', { n })}
            searchPlaceholder={t('opsversion:notify.mentionsSearch')}
            clearLabel={t('opsversion:notify.mentionsClear')}
            selectAllLabel={t('opsversion:notify.mentionsAll')}
            emptyLabel={t('opsversion:notify.mentionsEmpty')}
          />
          <span className="text-[11px] text-muted-foreground">{t('opsversion:notify.mentionsHint')}</span>
        </div>
      )}
      <label className="flex items-center gap-2 text-xs text-foreground">
        <input
          type="checkbox"
          checked={f.enabled}
          onChange={(e) => setF((p) => ({ ...p, enabled: e.target.checked }))}
        />
        {t('opsversion:notify.on')}
      </label>
    </Dialog>
  )
}

function ContactsSection({
  session,
  contacts,
  onError,
  onChanged,
}: {
  session: Session
  contacts: UseQueryResult<Contact[]>
  onError: (m: string) => void
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState<Contact | null | undefined>(undefined)
  const [confirmDel, setConfirmDel] = useState<Contact | null>(null)

  const cols: ColumnDef<Contact, unknown>[] = [
    {
      id: 'name',
      header: t('opsversion:notify.contactName'),
      accessorFn: (r) => r.name,
      cell: ({ row }) => (
        <div>
          <div className="text-xs font-semibold text-foreground">{row.original.name}</div>
          {row.original.note && (
            <div className="text-[11px] text-muted-foreground">{row.original.note}</div>
          )}
        </div>
      ),
    },
    {
      id: 'account',
      header: t('opsversion:notify.contactAccount'),
      accessorFn: (r) => r.teams_account,
      cell: ({ row }) => (
        <div>
          <div className="text-xs text-foreground">{row.original.teams_account}</div>
          <div className="text-[11px] text-muted-foreground">
            {t('opsversion:notify.contactChannels', { n: row.original.channels })}
          </div>
        </div>
      ),
    },
    {
      id: 'enabled',
      header: t('opsversion:notify.state'),
      accessorFn: (r) => r.enabled,
      cell: ({ row }) => (
        <Badge tone={row.original.enabled ? 'ok' : 'mute'}>
          {row.original.enabled ? t('opsversion:notify.on') : t('opsversion:notify.off')}
        </Badge>
      ),
    },
    {
      id: 'ops',
      header: '',
      accessorFn: () => '',
      meta: { action: true },
      cell: ({ row }) =>
        can(session, 'alert.write') ? (
          <div className="flex gap-1.5">
            <Button onClick={() => setEditing(row.original)}>{t('common:action.edit')}</Button>
            <Button variant="ghost" onClick={() => setConfirmDel(row.original)}>
              {t('common:action.delete')}
            </Button>
          </div>
        ) : null,
    },
  ]

  return (
    <div>
      <div className="mb-1.5 flex flex-wrap items-center gap-2">
        <span className="text-sm font-semibold text-foreground">
          {t('opsversion:notify.contacts')}
        </span>
        <span className="text-[11px] text-muted-foreground">{t('opsversion:notify.contactsHint')}</span>
        <div className="flex-1" />
        {can(session, 'alert.write') && (
          <Button variant="primary" onClick={() => setEditing(null)}>
            {t('opsversion:notify.contactAdd')}
          </Button>
        )}
      </div>
      <AsyncBoundary
        state={fromQuery(contacts, (d) => d.length === 0, (e) => toLoadError(e, t))}
        pending={<TableSkeleton columns={[30, 40, 15, 15]} rows={2} />}
        empty={
          <div className="rounded-md border border-dashed border-border-strong px-3 py-2.5 text-[11px] text-muted-foreground">
            {t('opsversion:notify.contactsEmptyHint')}
          </div>
        }
        errorTitle={t('opsversion:notify.contacts')}
        retryLabel={t('common:action.retry')}
        onRetry={() => contacts.refetch()}
      >
        {(data) => <DataTable columns={cols} data={data} rowKey={(r) => String(r.id)} minWidth={640} />}
      </AsyncBoundary>

      {editing !== undefined && (
        <ContactForm
          contact={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined)
            onChanged()
          }}
          onError={onError}
        />
      )}

      {confirmDel && (
        <Dialog
          open
          onClose={() => setConfirmDel(null)}
          title={t('opsversion:notify.contactDelete')}
          description={t('opsversion:notify.contactDeleteHint')}
          closeLabel={t('common:action.close')}
          footer={
            <>
              <Button onClick={() => setConfirmDel(null)}>{t('common:action.cancel')}</Button>
              <Button
                variant="danger"
                onClick={async () => {
                  try {
                    await api(`/api/notify/contacts/${confirmDel.id}`, { method: 'DELETE' })
                  } catch (e) {
                    onError((e as Error).message)
                  }
                  setConfirmDel(null)
                  onChanged()
                }}
              >
                {t('common:action.confirm')}
              </Button>
            </>
          }
        >
          <div className="text-sm text-foreground">
            {confirmDel.name}（{confirmDel.teams_account}）
            {confirmDel.channels > 0 && (
              <div className="text-[11px] text-muted-foreground">
                {t('opsversion:notify.contactChannels', { n: confirmDel.channels })}
              </div>
            )}
          </div>
        </Dialog>
      )}
    </div>
  )
}

function ContactForm({
  contact,
  onClose,
  onSaved,
  onError,
}: {
  contact: Contact | null
  onClose: () => void
  onSaved: () => void
  onError: (m: string) => void
}) {
  const { t } = useTranslation()
  const isEdit = contact !== null
  const [f, setF] = useState({
    name: contact?.name ?? '',
    teams_account: contact?.teams_account ?? '',
    note: contact?.note ?? '',
    enabled: contact?.enabled ?? true,
  })
  const [busy, setBusy] = useState(false)

  async function save() {
    if (!f.name.trim() || !f.teams_account.trim()) {
      onError(t('opsversion:notify.contactNameRequired'))
      return
    }
    setBusy(true)
    try {
      await api(isEdit ? `/api/notify/contacts/${contact.id}` : '/api/notify/contacts', {
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

  const input =
    'w-full rounded-md border border-border bg-background px-2 py-1.5 text-sm text-foreground'
  return (
    <Dialog
      open
      onClose={onClose}
      title={isEdit ? `${t('common:action.edit')} · ${contact.name}` : t('opsversion:notify.contactAdd')}
      closeLabel={t('common:action.close')}
      footer={
        <>
          <Button onClick={onClose}>{t('common:action.cancel')}</Button>
          <Button variant="primary" onClick={save} disabled={busy}>
            {t('common:action.save')}
          </Button>
        </>
      }
    >
      <label className="mb-2.5 flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">{t('opsversion:notify.contactName')}</span>
        <input
          value={f.name}
          maxLength={64}
          onChange={(e) => setF((p) => ({ ...p, name: e.target.value }))}
          className={input}
        />
        <span className="text-[11px] text-muted-foreground">{t('opsversion:notify.contactNameHint')}</span>
      </label>
      <label className="mb-2.5 flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">{t('opsversion:notify.contactAccount')}</span>
        <input
          value={f.teams_account}
          placeholder="name@example.com"
          autoComplete="off"
          onChange={(e) => setF((p) => ({ ...p, teams_account: e.target.value }))}
          className={input}
        />
        <span className="text-[11px] text-muted-foreground">{t('opsversion:notify.contactAccountHint')}</span>
      </label>
      <label className="mb-2.5 flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">{t('opsversion:notify.contactNote')}</span>
        <input
          value={f.note}
          maxLength={255}
          onChange={(e) => setF((p) => ({ ...p, note: e.target.value }))}
          className={input}
        />
      </label>
      <label className="flex items-center gap-2 text-xs text-foreground">
        <input
          type="checkbox"
          checked={f.enabled}
          onChange={(e) => setF((p) => ({ ...p, enabled: e.target.checked }))}
        />
        {t('opsversion:notify.on')}
      </label>
    </Dialog>
  )
}
