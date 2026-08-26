import { useTranslation } from '@ops/i18n'
import { Badge, Button } from '@ops/ui'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../lib/api.js'
import { fromLines, normLines, toLines } from './types.js'

/**
 * 一个平台下的项目。对比表的一列 = 项目 × 环境。
 *
 * 🔴 同一个 ns 里区分多项目的三档（按优先级，够用即止）：
 *   ① ns 隔离      → 环境的 ns 规则就够，这里留空
 *   ② 名字有规律   → 服务名通配，如 biz-*
 *   ③ 无规律       → 从**已采集的服务列表**勾选
 *
 * ⚠️ 第 ③ 档绝不让人手打服务名：168 个服务没人会填，填了会拼错，
 * 而拼错时不报错，只是那个服务永远不出现在这个项目下。
 */
export interface Project {
  id: number
  org_id: number
  name: string
  // ⚠️ 如实写成可空：后端的空列表历史上是 JSON null，不是 []。
  //    类型写成 string[] 而实际收到 null，`.length` 会当场把整个弹窗打白。
  service_include: string[] | null
  service_pins: string[] | null
  sort_order: number
  enabled: boolean
  env_count: number
}

/** 项目列表。环境卡片的「所属项目」下拉与项目区共用同一份数据和缓存键。 */
export function useProjects(orgId: number) {
  return useQuery({
    queryKey: ['projects', orgId],
    queryFn: () => api<Project[]>(`/api/projects?org_id=${orgId}`),
    enabled: orgId > 0,
  })
}

export function ProjectsSection({ orgId }: { orgId: number }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<Project | null | undefined>(undefined)
  const [err, setErr] = useState('')

  // orgId=0 表示平台还没保存 —— 项目要挂在平台下，先存平台再配项目
  const q = useProjects(orgId)
  const reload = () => qc.invalidateQueries({ queryKey: ['projects', orgId] })

  if (orgId === 0) {
    return (
      <div className="rounded-md border border-dashed border-border p-3 text-[11px] text-muted-foreground">
        {t('opsversion:proj.saveFirst')}
      </div>
    )
  }

  const list = q.data ?? []

  return (
    <div className="space-y-2">
      {err ? <div className="rounded-md bg-danger-bg px-2.5 py-1.5 text-[11px] text-danger">{err}</div> : null}

      {list.map((p) => (
        <div key={p.id} className="rounded-md border border-border bg-background p-2.5">
          <div className="flex items-center gap-2">
            <span className="text-xs font-semibold text-foreground">{p.name}</span>
            {!p.enabled && <Badge tone="mute">{t('opsversion:org.disabled')}</Badge>}
            <span className="text-[11px] text-muted-foreground">
              {t('opsversion:proj.envCount', { n: p.env_count })}
            </span>
            <div className="flex-1" />
            <Button size="sm" variant="ghost" onClick={() => setEditing(p)}>
              {t('common:action.edit')}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={async () => {
                try {
                  await api(`/api/projects/${p.id}`, { method: 'DELETE' })
                  reload()
                } catch (e) {
                  // 后端对"还有环境挂着"回 409 并给出可执行的话，原样带给用户
                  setErr((e as Error).message)
                }
              }}
            >
              {t('common:action.delete')}
            </Button>
          </div>
          {/* 显示这个项目**靠什么区分服务** —— 三档都留空时要说明它吃全部 */}
          <div className="mt-1 text-[11px] text-muted-foreground">
            {ruleSummary(p, t)}
          </div>
        </div>
      ))}

      <Button size="sm" onClick={() => setEditing(null)}>
        {t('opsversion:proj.add')}
      </Button>

      {editing !== undefined && (
        <ProjectForm
          orgId={orgId}
          proj={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined)
            reload()
          }}
          onError={setErr}
        />
      )}
    </div>
  )
}

/** 这个项目靠什么区分服务。三档都留空时要说清它吃全部，别显示成空白 */
function ruleSummary(p: Project, t: (k: string, o?: Record<string, unknown>) => string) {
  const inc = p.service_include ?? []
  const pins = p.service_pins ?? []
  if (inc.length === 0 && pins.length === 0) return t('opsversion:proj.noRule')
  return (
    <>
      {inc.length > 0 && <span className="mr-2 font-mono">{inc.join(' ')}</span>}
      {pins.length > 0 && <span>{t('opsversion:proj.pinnedN', { n: pins.length })}</span>}
    </>
  )
}

/**
 * 从平台已采集到的服务里勾选。
 *
 * ⚠️ 已经勾上但**当前采不到**的服务要单独列出来，不能悄悄丢掉 ——
 *    对方可能只是暂时下线了，直接从选中列表里抹掉等于替人做了决定。
 */
function ServicePicker({
  orgId,
  picked,
  onChange,
}: {
  orgId: number
  picked: string[]
  onChange: (v: string[]) => void
}) {
  const { t } = useTranslation()
  const [kw, setKw] = useState('')
  const q = useQuery({
    queryKey: ['org-services', orgId],
    queryFn: () => api<{ service_key: string; envs: string[] }[]>(`/api/orgs/${orgId}/services`),
    enabled: orgId > 0,
  })
  const all = q.data ?? []
  const known = new Set(all.map((x) => x.service_key))
  // 勾了但采不到的：单独一组放最前面，标出来
  const missing = picked.filter((p) => !known.has(p))
  const shown = all.filter((x) => !kw || x.service_key.includes(kw))
  const toggle = (k: string) =>
    onChange(picked.includes(k) ? picked.filter((x) => x !== k) : [...picked, k])

  if (orgId === 0) {
    return (
      <div className="rounded-md border border-dashed border-border p-2.5 text-[11px] text-muted-foreground">
        {t('opsversion:proj.saveFirst')}
      </div>
    )
  }

  return (
    <div className="rounded-md border border-border">
      <input
        value={kw}
        onChange={(e) => setKw(e.target.value)}
        placeholder={t('opsversion:proj.pickSearch')}
        className="w-full rounded-t-md border-b border-border bg-background px-2.5 py-1.5 text-[11px] text-foreground"
      />
      <div className="max-h-52 overflow-y-auto p-1.5">
        {missing.map((k) => (
          <label key={k} className="flex items-center gap-1.5 px-1 py-0.5 text-[11px] text-warning">
            <input type="checkbox" checked onChange={() => toggle(k)} />
            <span className="font-mono">{k}</span>
            <span>{t('opsversion:proj.pickMissing')}</span>
          </label>
        ))}
        {all.length === 0 && (
          <div className="px-1 py-3 text-center text-[11px] text-muted-foreground">
            {/* 「还没采过」和「采了但没有服务」对使用者是两件事，但下一步一样：先去采一次 */}
            {t('opsversion:proj.pickEmpty')}
          </div>
        )}
        {shown.map((x) => (
          <label
            key={x.service_key}
            className="flex items-center gap-1.5 px-1 py-0.5 text-[11px] text-foreground"
          >
            <input
              type="checkbox"
              checked={picked.includes(x.service_key)}
              onChange={() => toggle(x.service_key)}
            />
            <span className="font-mono">{x.service_key}</span>
            <span className="text-muted-foreground">{x.envs.join(' ')}</span>
          </label>
        ))}
      </div>
      <div className="border-t border-border px-2.5 py-1 text-[11px] text-muted-foreground">
        {t('opsversion:proj.pickCount', { n: picked.length, total: all.length })}
      </div>
    </div>
  )
}

function ProjectForm({
  orgId,
  proj,
  onClose,
  onSaved,
  onError,
}: {
  orgId: number
  proj: Project | null
  onClose: () => void
  onSaved: () => void
  onError: (s: string) => void
}) {
  const { t } = useTranslation()
  const [f, setF] = useState({
    name: proj?.name ?? '',
    service_include: proj?.service_include ?? [],
    service_pins: proj?.service_pins ?? [],
    enabled: proj?.enabled ?? true,
  })
  const [busy, setBusy] = useState(false)

  async function save() {
    if (!f.name.trim()) {
      onError(t('opsversion:proj.nameRequired'))
      return
    }
    setBusy(true)
    try {
      await api(proj ? `/api/projects/${proj.id}` : '/api/projects', {
        method: proj ? 'PUT' : 'POST',
        body: JSON.stringify({
          org_id: orgId,
          name: f.name,
          // 与平台表单同一条规矩：输入期无损，保存时才清理（空行会变成匹配所有的空规则）
          service_include: normLines(f.service_include),
          service_pins: normLines(f.service_pins),
          enabled: f.enabled,
          sort_order: proj?.sort_order ?? 0,
        }),
      })
      onSaved()
    } catch (e) {
      onError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const area = (v: string, on: (s: string) => void, ph: string) => (
    <textarea
      rows={Math.min(8, Math.max(2, v.split('\n').length))}
      value={v}
      placeholder={ph}
      onChange={(e) => on(e.target.value)}
      className="w-full resize-y rounded-md border border-input bg-background px-2.5 py-1.5 font-mono text-[11px] text-foreground"
    />
  )

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-overlay p-4">
      <div className="w-full max-w-lg rounded-lg border border-border bg-card p-4 shadow-lg">
        <div className="mb-3 text-sm font-semibold text-foreground">
          {proj ? t('opsversion:proj.editTitle') : t('opsversion:proj.add')}
        </div>

        <div className="mb-2.5">
          <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:proj.name')}</div>
          <input
            value={f.name}
            onChange={(e) => setF((p) => ({ ...p, name: e.target.value }))}
            placeholder="项目B"
            className="w-full rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground"
          />
        </div>

        <div className="mb-2.5">
          <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:proj.include')}</div>
          {area(
            fromLines(f.service_include),
            (v) => setF((p) => ({ ...p, service_include: toLines(v) })),
            'biz-*',
          )}
          <div className="mt-1 text-[11px] text-muted-foreground">{t('opsversion:proj.includeHint')}</div>
        </div>

        <div className="mb-2.5">
          <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:proj.pins')}</div>
          {/* 🔴 从**已采集到的**服务里勾，不让人手打。
              168 个服务没人会手填，填了会拼错 —— 而拼错**不报错**，
              只是那个服务永远不出现在这个项目下，配置页上看着完全正常。 */}
          <ServicePicker
            orgId={orgId}
            picked={f.service_pins ?? []}
            onChange={(v) => setF((p) => ({ ...p, service_pins: v }))}
          />
          <div className="mt-1 text-[11px] text-muted-foreground">{t('opsversion:proj.pinsHint')}</div>
        </div>

        <label className="flex items-center gap-1.5 text-xs text-foreground">
          <input
            type="checkbox"
            checked={f.enabled}
            onChange={(e) => setF((p) => ({ ...p, enabled: e.target.checked }))}
          />
          {t('opsversion:proj.enabled')}
        </label>

        <div className="mt-4 flex justify-end gap-2">
          <Button onClick={onClose}>{t('common:action.cancel')}</Button>
          <Button variant="primary" disabled={busy} onClick={save}>
            {t('common:action.save')}
          </Button>
        </div>
      </div>
    </div>
  )
}
