import { useTranslation } from '@ops/i18n'
import { Button, Dialog } from '@ops/ui'
import { useState } from 'react'
import { api } from '../../lib/api.js'
import type { Harbor } from './types.js'

/**
 * Harbor 配置表单。
 *
 * 🔴 密码栏**不回显**。编辑时留空 = 不改，不是清空 ——
 * 表单打开时那一栏本来就是空的，当成清空的话「改个名字」会把凭据抹掉，
 * 下次采集报认证失败，而人完全想不到是自己刚才改名字造成的。
 */
export function HarborForm({
  harbor,
  onClose,
  onSaved,
  onError,
}: {
  harbor: Harbor | null
  onClose: () => void
  onSaved: () => void
  onError: (msg: string) => void
}) {
  const { t } = useTranslation()
  const isEdit = harbor !== null
  const [f, setF] = useState({
    name: harbor?.name ?? '',
    endpoint: harbor?.endpoint ?? '',
    username: harbor?.username ?? '',
    password: '',
    insecure_tls: harbor?.insecure_tls ?? false,
    policy_filter: harbor?.policy_filter ?? [],
    enabled: harbor?.enabled ?? true,
  })
  const [busy, setBusy] = useState(false)

  function set<K extends keyof typeof f>(k: K, v: (typeof f)[K]) {
    setF((p) => ({ ...p, [k]: v }))
  }

  async function save() {
    if (!f.name.trim() || !f.endpoint.trim()) {
      onError(t('opsversion:sync.nameRequired'))
      return
    }
    setBusy(true)
    try {
      await api(isEdit ? `/api/harbors/${harbor.id}` : '/api/harbors', {
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
      className="w-full rounded-md border border-border bg-background px-2 py-1.5 text-sm text-foreground"
    />
  )
  const field = (label: string, node: React.ReactNode, hint?: string) => (
    <label className="mb-2.5 flex flex-col gap-1">
      <span className="text-xs text-muted-foreground">{label}</span>
      {node}
      {hint && <span className="text-[11px] text-muted-foreground">{hint}</span>}
    </label>
  )

  return (
    <Dialog
      open
      onClose={onClose}
      title={isEdit ? `${t('opsversion:sync.edit')} · ${harbor.name}` : t('opsversion:sync.add')}
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
      {field(t('opsversion:sync.name'), input(f.name, (v) => set('name', v), 'prod-harbor'))}
      {field(
        t('opsversion:sync.endpoint'),
        input(f.endpoint, (v) => set('endpoint', v), 'https://harbor.example.com'),
      )}
      {field(t('opsversion:sync.username'), input(f.username, (v) => set('username', v), 'robot$sync'))}
      {field(
        t('opsversion:sync.password'),
        input(f.password, (v) => set('password', v), '', 'password'),
        isEdit ? t('opsversion:sync.passwordKeep') : undefined,
      )}
      {/* 🔴 只拉关心的那几条规则。一个 Harbor 上可能有几十条，
          而每条要拉 20 次执行、每次执行还要拉 tasks —— 全量是上千次请求，
          每轮采集都打一遍会把 Harbor 拖慢。 */}
      <label className="mb-2.5 flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">{t('opsversion:sync.policyFilter')}</span>
        <textarea
          rows={3}
          value={(f.policy_filter ?? []).join('\n')}
          placeholder={t('opsversion:sync.policyFilterPh')}
          onChange={(e) =>
            set(
              'policy_filter',
              e.target.value
                .split('\n')
                .map((x) => x.trim())
                .filter(Boolean),
            )
          }
          className="w-full rounded-md border border-border bg-background px-2 py-1.5 font-mono text-xs text-foreground"
        />
        <span className="text-[11px] text-muted-foreground">
          {t('opsversion:sync.policyFilterHint')}
        </span>
      </label>
      <label className="mb-2 flex items-center gap-2 text-xs text-foreground">
        <input
          type="checkbox"
          checked={f.enabled}
          onChange={(e) => set('enabled', e.target.checked)}
        />
        {t('opsversion:sync.enabled')}
      </label>
      <label className="flex items-center gap-2 text-xs text-foreground">
        <input
          type="checkbox"
          checked={f.insecure_tls}
          onChange={(e) => set('insecure_tls', e.target.checked)}
        />
        {t('opsversion:sync.insecureTLS')}
      </label>
      <p className="mt-1 text-[11px] text-muted-foreground">{t('opsversion:sync.insecureHint')}</p>
    </Dialog>
  )
}
