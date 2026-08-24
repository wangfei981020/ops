import { api } from '../../lib/api.js'

/** 一列的数据新鲜度。字段名与后端 store.ColumnFreshness 一一对应 */
export interface Freshness {
  org_id: number
  org_name: string
  /** 🔴 新鲜度是**按列**的，一列 = 平台 × 项目 × 环境 */
  project_id: number
  env: string
  is_self: boolean
  /** 数据**本身**的时刻。null = 从来没采到过 */
  observed_at: string | null
  /** 上次**尝试**采集的时刻 */
  last_collect_at: string | null
  last_collect_status: string
  last_collect_error: string
}

/** 一列的刷新结果 */
export interface RefreshOutcome {
  key: string
  orgName: string
  env: string
  /** 界面上显示的名字。采集是按平台×环境做的，所以这里**不带项目名** */
  label: string
  /** skipped=数据够新没采；ok=采成功；failed=采失败（旧数据仍可用） */
  state: 'skipped' | 'ok' | 'failed'
  message?: string
}

/**
 * 数据过期阈值。
 *
 * 比定时采集周期（30 分钟）短，又不至于点一次就重采一遍。
 * ⚠️ 不要设得太小：生产上一列要 5–15 秒，阈值 1 分钟等于每次都全采，
 * 用户每次点比对都要等半分钟，而且对方系统被反复打。
 */
export const STALE_MS = 10 * 60 * 1000

/**
 * 新鲜度的键。
 *
 * 🔴 必须含 projectID：同平台同环境的两个项目是**两列，各采各的**。
 * 少了它，A 项目刚采过会让 B 项目也显示成"刚刚更新"，
 * 于是「比对前自动刷新过期列」会跳过真正过期的那一列 ——
 * 而界面上写着"已刷新"。
 */
export const colKeyOf = (orgId: number, projectId: number, env: string) =>
  `${orgId}/${projectId}/${env}`

/** 这一列是否需要刷新 */
export function isStale(f: Freshness | undefined, now = Date.now()): boolean {
  if (!f) return true
  // 🔴 从来没采到过数据 → 一定要采。
  //    不采的话界面上是一列空白，跟"对方没有任何服务"分不出来。
  if (!f.observed_at) return true
  return now - new Date(f.observed_at).getTime() > STALE_MS
}

/**
 * 刷新指定的列 —— 只刷过期的，逐列并发，边跑边回报进度。
 *
 * 🔴 单列失败**不中断其余列**，也不让整次操作失败：
 * 一个客户环境不通，不该让整张对账表都看不了。
 * 失败的列会在结果里标出来，由界面显式告诉用户"这列用的是旧数据"。
 * ⚠️ 绝不能静默吞掉失败 —— 那样"已刷新"就是假的，比不刷新更危险。
 */
export async function refreshColumns(
  cols: { orgId: number; projectId: number; env: string; orgName: string }[],
  fresh: Map<string, Freshness>,
  onProgress: (done: number, total: number, current: string) => void,
): Promise<RefreshOutcome[]> {
  // 🔴 先按「平台×环境」去重再采。
  //
  //    采集的粒度是平台×环境，**不是列** —— 同一平台同一环境下的两个项目
  //    共用同一份快照。不去重的话，两个项目就把对方的系统采两遍：
  //    时间翻倍、对方日志里出现重复请求，而结果一模一样。
  //    项目越多越明显（5 个项目 = 同一个环境采 5 次）。
  const uniq = new Map<string, { orgId: number; projectId: number; env: string; orgName: string }>()
  for (const c of cols) {
    const k = colKeyOf(c.orgId, c.projectId, c.env)
    if (!uniq.has(k)) {
      uniq.set(k, { orgId: c.orgId, projectId: c.projectId, env: c.env, orgName: c.orgName })
    }
  }
  const all = [...uniq.values()]
  const mk = (c: { orgId: number; projectId: number; env: string; orgName: string }) => ({
    key: colKeyOf(c.orgId, c.projectId, c.env),
    orgName: c.orgName,
    env: c.env,
    label: `${c.orgName}·${c.env}`,
  })

  const need = all.filter((c) => isStale(fresh.get(colKeyOf(c.orgId, c.projectId, c.env))))
  const results: RefreshOutcome[] = all
    .filter((c) => !need.includes(c))
    .map((c) => ({ ...mk(c), state: 'skipped' as const }))

  if (need.length === 0) return results

  let done = 0
  // 并发上限 4：再高对方系统压力太大，而我们是来读数据的，
  // 不该因为刷新把人家的控制台拖慢
  const queue = [...need]
  const workers = Array.from({ length: Math.min(4, queue.length) }, async () => {
    for (;;) {
      const c = queue.shift()
      if (!c) return
      onProgress(done, need.length, `${c.orgName} · ${c.env}`)
      try {
        // 🔴 带上 project：一个平台的同一环境可能有多行（每个项目一行）。
        //    不带的话刷新一列会连带去打对方系统 N 次，而进度条只显示一列 ——
        //    表现是「刷新一列却等了很久」，且对方日志里出现莫名的重复请求。
        await api(
          `/api/orgs/${c.orgId}/collect?env=${encodeURIComponent(c.env)}&project=${c.projectId}`,
          { method: 'POST' },
        )
        results.push({ ...mk(c), state: 'ok' })
      } catch (e) {
        results.push({ ...mk(c), state: 'failed', message: (e as Error).message })
      }
      done++
      onProgress(done, need.length, '')
    }
  })
  await Promise.all(workers)
  return results
}

export async function loadFreshness(): Promise<Map<string, Freshness>> {
  const list = await api<Freshness[]>('/api/columns/freshness')
  return new Map(list.map((f) => [colKeyOf(f.org_id, f.project_id, f.env), f]))
}
