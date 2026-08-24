import { useTranslation } from '@ops/i18n'
import {
  AsyncBoundary,
  Badge,
  Button,
  type ColumnDef,
  DataTable,
  EmptyState,
  fromQuery,
  TableSkeleton,
} from '@ops/ui'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api, toLoadError } from '../../lib/api.js'
import type { Session } from '../../lib/session.js'

interface AuditRow {
  /** 游标分页要用它 */
  id: number
  actor: string
  action: string
  target: string
  detail: string
  result: string
  error_msg: string
  ip: string
  created_at: string
}

interface AuditPageResp {
  rows: AuditRow[] | null
  /** 全表总条数。🔴 必须显示——否则人不知道自己看到的是不是全部 */
  total: number
  /** 下一页的游标。0 = 没有下一页 */
  next_before: number
}

const PAGE_SIZE = 100

export function AuditPage({ session }: { session: Session }) {
  const { t } = useTranslation()
  // 🔴 游标分页，不是 offset：审计表一直在插入，
  //    offset 会让同一条在两页里都出现、或者被整个跳过。
  // ⚠️ 记住走过的游标栈才能「上一页」—— 游标分页天生只能往前，
  //    不记的话点了下一页就回不去了。
  const [cursor, setCursor] = useState(0)
  const [stack, setStack] = useState<number[]>([])
  const q = useQuery({
    queryKey: ['audit', cursor],
    queryFn: () =>
      api<AuditPageResp>(`/api/audit?limit=${PAGE_SIZE}${cursor ? `&before=${cursor}` : ''}`),
  })

  const columns: ColumnDef<AuditRow, unknown>[] = [
    {
      id: 'time',
      header: t('opsversion:audit.time'),
      accessorFn: (r) => r.created_at,
      cell: ({ row }) => (
        <span className="font-mono text-[11px] text-muted-foreground">
          {new Date(row.original.created_at).toLocaleString()}
        </span>
      ),
    },
    {
      id: 'actor',
      header: t('opsversion:audit.actor'),
      accessorFn: (r) => r.actor,
      cell: ({ row }) => <span className="text-xs text-foreground">{row.original.actor}</span>,
    },
    {
      id: 'action',
      header: t('opsversion:audit.action'),
      accessorFn: (r) => r.action,
      cell: ({ row }) => (
        <span className="font-mono text-xs text-foreground">{row.original.action}</span>
      ),
    },
    {
      id: 'target',
      header: t('opsversion:audit.target'),
      accessorFn: (r) => r.target,
      cell: ({ row }) => (
        <span className="text-[11px] text-muted-foreground">{row.original.target || '—'}</span>
      ),
    },
    // 🔴 结果必须显示。只记「发起了操作」不记成败的话，
    //    一次 401 在审计里跟成功长得一模一样
    {
      id: 'result',
      header: t('opsversion:audit.result'),
      accessorFn: (r) => r.result,
      cell: ({ row }) => (
        <Badge tone={row.original.result === 'success' ? 'ok' : 'bad'}>
          {row.original.result === 'success'
            ? t('opsversion:audit.success')
            : t('opsversion:audit.failed')}
        </Badge>
      ),
    },
    {
      id: 'detail',
      header: t('opsversion:audit.detail'),
      accessorFn: (r) => r.detail,
      // 🔴 必须 truncate 而不是 break-all：detail 存的是 JSON，
      //    多列比对的导出记录能到 100+ 字符（实测一格横向溢出 302px，
      //    和右边的 IP 列文字叠在一起，两段字重合着看不清，）。
      //    break-all 只管"能不能断字"，管不住"这一格有多宽" —— 表格是 auto 布局，
      //    长内容照样把格子撑出去。truncate（overflow-hidden + nowrap + 省略号）
      //    才是真正把宽度钉死的那个，全文交给 title 兜住。
      cell: ({ row }) => (
        <div className="max-w-80 overflow-hidden">
          {/* detail 已在后端脱敏（password/api_key/token 一律 ***） */}
          <div
            className="truncate font-mono text-[11px] text-muted-foreground"
            title={row.original.detail}
          >
            {row.original.detail}
          </div>
          {row.original.error_msg && (
            <div className="truncate text-[11px] text-danger" title={row.original.error_msg}>
              {row.original.error_msg}
            </div>
          )}
        </div>
      ),
    },
    {
      id: 'ip',
      header: 'IP',
      accessorFn: (r) => r.ip,
      cell: ({ row }) => (
        <span className="font-mono text-[11px] text-muted-foreground">{row.original.ip}</span>
      ),
    },
  ]

  const state = fromQuery(
    q,
    (d) => (d.rows ?? []).length === 0,
    (e) => toLoadError(e, t),
  )
  const page = q.data

  return (
    <div className="flex flex-col gap-3 p-5">
      <div className="flex items-center gap-2">
        <span className="text-sm font-semibold text-foreground">{t('opsversion:audit.title')}</span>
        <span className="text-[11px] text-muted-foreground">{t('opsversion:audit.hint')}</span>
        <div className="flex-1" />
        {/* 🔴 「共 N 条」必须常驻：查审计时唯一在意的就是
            「我看到的是不是全部」，不给总数等于这个问题永远没答案 */}
        {page && (
          <span className="text-[11px] text-muted-foreground">
            {/* ⚠️ 夹住上界：总数刚好是页大小整数倍时最后一页仍会给游标，
                不夹的话会显示成「第 101–100 条」（同 SyncPage） */}
            {t('opsversion:audit.pageInfo', {
              from: Math.min(stack.length * PAGE_SIZE + 1, page.total),
              to: Math.min(stack.length * PAGE_SIZE + (page.rows ?? []).length, page.total),
              total: page.total,
            })}
          </span>
        )}
      </div>
      <AsyncBoundary
        state={state}
        pending={<TableSkeleton columns={[16, 10, 14, 14, 8, 26, 10]} rows={6} />}
        empty={
          <EmptyState
            title={t('opsversion:audit.empty')}
            reason={t('opsversion:audit.hint')}
            action={null}
          />
        }
        errorTitle={t('opsversion:audit.title')}
        retryLabel={t('common:action.retry')}
        onRetry={() => q.refetch()}
      >
        {(data) => (
          <>
            <DataTable
              columns={columns}
              data={data.rows ?? []}
              // id 是唯一的；原来用「时间+人+动作」拼 key，同一秒内的两条同类操作会撞
              rowKey={(r) => String(r.id)}
              minWidth={1000}
            />
            <div className="flex items-center justify-end gap-2">
              <Button
                disabled={stack.length === 0}
                onClick={() => {
                  const prev = [...stack]
                  const back = prev.pop() ?? 0
                  setStack(prev)
                  setCursor(back)
                }}
              >
                {t('opsversion:audit.prev')}
              </Button>
              <Button
                // ⚠️ 只有后端给了游标才让点。最后一页仍可点的话，
                //    点进去是空表，人会以为数据丢了
                disabled={!data.next_before}
                onClick={() => {
                  setStack((p) => [...p, cursor])
                  setCursor(data.next_before)
                }}
              >
                {t('opsversion:audit.next')}
              </Button>
            </div>
          </>
        )}
      </AsyncBoundary>
    </div>
  )
}
