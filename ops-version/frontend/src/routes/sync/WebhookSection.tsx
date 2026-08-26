import { useTranslation } from '@ops/i18n'
import { Badge, Button } from '@ops/ui'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../lib/api.js'

/**
 * Harbor webhook 接入。
 *
 * 🔴 存在的理由：轮询 Harbor 的 REST API **拿不到版本号**。
 *    `/replication/executions/{id}/tasks` 的 resource 字段在多 artifact 时
 *    写的是 `repo [N item(s) in total]` —— 实测过 143 条复制记录 tag 100% 为空，
 *    于是对账的同步归因（按 服务名+版本 查）永远匹配不上。
 *    而 webhook 事件里的 `successful_artifact[].name_tag` 带完整版本号。
 *
 * ⚠️ 令牌明文**只在创建时显示一次**：库里只存哈希，我们自己也拿不回来。
 */
interface TokenRow {
  id: number
  name: string
  created_by: string
  created_at: string
  last_used_at: string
  /**
   * 最近一次收到的事件摘要。
   *
   * 🔴 「Harbor 说推成功、令牌显示被调用过、可就是没有数据」——
   *    这个状态没有摘要就查不下去，原因只在后端日志里，而用的人多半没日志权限。
   */
  last_event: string
}
interface WebhookInfo {
  path: string
  tokens: TokenRow[]
}

export function WebhookSection() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  // 🔴 预填一个**真实值**，不要留空靠 placeholder 提示。
  //
  //    留空的话按钮是禁用态，而界面上看到的是灰色的 placeholder 文字 ——
  //    人会读成"框里已经有内容了，但按钮点不动"，然后卡在这里。
  //    同一个错在忽略规则那里已经犯过一次，这是第二次。
  //
  // ⚠️ 名字只是备注（将来判断这条令牌能不能吊销），不该拦着人生成令牌。
  const [name, setName] = useState('Harbor 复制事件')
  const [plain, setPlain] = useState('')
  const [err, setErr] = useState('')

  const q = useQuery({
    queryKey: ['webhook-tokens'],
    queryFn: () => api<WebhookInfo>('/api/webhooks/tokens'),
  })

  const create = useMutation({
    mutationFn: (n: string) =>
      api<{ token: string }>('/api/webhooks/tokens', {
        method: 'POST',
        body: JSON.stringify({ name: n }),
      }),
    onSuccess: (d) => {
      setErr('')
      setPlain(d.token)
      setName('Harbor 复制事件')
      qc.invalidateQueries({ queryKey: ['webhook-tokens'] })
    },
    onError: (e: Error) => setErr(e.message),
  })

  const del = useMutation({
    mutationFn: (id: number) => api(`/api/webhooks/tokens/${id}`, { method: 'DELETE' }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['webhook-tokens'] }),
    onError: (e: Error) => setErr(e.message),
  })

  const info = q.data
  const rows = info?.tokens ?? []

  return (
    <section>
      <div className="mb-1.5 text-xs font-semibold text-foreground">
        {t('opsversion:sync.hookSection')}
      </div>
      <p className="mb-2 text-[11px] leading-relaxed text-muted-foreground">
        {t('opsversion:sync.hookHint')}
      </p>

      {err && (
        <div className="mb-2 rounded-md border border-danger-border bg-danger-subtle px-3 py-2 text-[11px] text-danger">
          {err}
        </div>
      )}

      {/* 🔴 端点路径由后端给，前端不拼 —— Harbor 在集群内填的是 svc 地址，
          而前端只知道浏览器地址（往往是公网域名），让它拼必然拼错，
          且拼错的表现是 Harbor 那边一直失败，没人会想到是地址问题。 */}
      <div className="mb-3 rounded-md border border-border bg-background p-2.5">
        <div className="mb-1 text-[11px] text-muted-foreground">
          {t('opsversion:sync.hookUrlLabel')}
        </div>
        <code className="font-mono text-[11px] text-foreground">
          http://&lt;ops-version-backend 的 svc&gt;:8080{info?.path ?? '/api/webhooks/harbor'}
        </code>
        <div className="mt-1 text-[11px] text-muted-foreground">
          {t('opsversion:sync.hookUrlNote')}
        </div>
      </div>

      {plain && (
        <div className="mb-3 rounded-md border border-warning-border bg-warning-subtle p-2.5">
          <div className="mb-1 text-[11px] font-semibold text-warning">
            {t('opsversion:sync.hookTokenOnce')}
          </div>
          <code className="break-all font-mono text-[11px] text-foreground">{plain}</code>
        </div>
      )}

      <div className="mb-2 flex items-center gap-2">
        <input
          value={name}
          placeholder={t('opsversion:sync.hookNamePlaceholder')}
          onChange={(e) => setName(e.target.value)}
          className="h-8 flex-1 rounded-md border border-input bg-background px-2.5 text-[11px] text-foreground"
        />
        <Button
          variant="primary"
          disabled={name.trim() === '' || create.isPending}
          onClick={() => create.mutate(name)}
        >
          {t('opsversion:sync.hookCreate')}
        </Button>
      </div>

      {rows.length === 0 ? (
        <div className="rounded-md border border-dashed border-border px-3 py-3 text-center text-[11px] text-muted-foreground">
          {t('opsversion:sync.hookNone')}
        </div>
      ) : (
        <ul className="space-y-1">
          {rows.map((r) => (
            <li
              key={r.id}
              className="flex items-center gap-2 rounded-md border border-border bg-background px-2.5 py-1.5"
            >
              <span className="text-[11px] text-foreground">{r.name}</span>
              {/* 🔴 「从没被调用过」要单独标出来：配了令牌但 Harbor 那边地址填错、
                  或被网络挡住时，界面上和「一直在正常工作」长得一模一样。 */}
              {r.last_used_at ? (
                <Badge tone="ok">{t('opsversion:sync.hookUsed', { at: r.last_used_at })}</Badge>
              ) : (
                <Badge tone="warn">{t('opsversion:sync.hookNeverUsed')}</Badge>
              )}
              <div className="flex-1" />
              <span className="text-[11px] text-muted-foreground">{r.created_at}</span>
              <Button size="sm" variant="ghost" onClick={() => del.mutate(r.id)}>
                {t('opsversion:sync.hookRevoke')}
              </Button>
            </li>
          ))}
        </ul>
      )}

      {/* 🔴 最近一次收到了什么 —— 让「配好了却没数据」在界面上查得下去。
          Harbor 说推成功、令牌显示被调用过、可就是没数据，这个状态
          没有摘要就走不下去了：原因只在后端日志里，而用的人多半没日志权限。
          ⚠️ 带 ⚠ 的判断由**后端**给（策略名对不上 / 没有镜像明细），
          前端不另外推一遍 —— 判据两处写必然分叉。 */}
      {rows.some((r) => r.last_event) && (
        <div className="mt-2 rounded-md border border-border bg-background p-2.5">
          <div className="mb-1 text-[11px] text-muted-foreground">
            {t('opsversion:sync.hookLastEvent')}
          </div>
          <ul className="space-y-0.5">
            {rows
              .filter((r) => r.last_event)
              .map((r) => (
                <li
                  key={r.id}
                  className={`text-[11px] ${r.last_event.includes('⚠') ? 'text-warning' : 'text-foreground'}`}
                >
                  {r.last_event}
                </li>
              ))}
          </ul>
        </div>
      )}
    </section>
  )
}
