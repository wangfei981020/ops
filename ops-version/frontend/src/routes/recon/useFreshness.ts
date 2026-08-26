import { api } from '../../lib/api.js'
import { PROJECT_ALL } from './types.js'

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
  // 按「平台×**项目**×环境」去重再采 —— 这与 colKeyOf 的粒度一致。
  //
  // ⚠️ 这段注释原来写的是「按平台×环境去重」，与实现不符：colKeyOf 一直带着
  //    projectId。而实现才是对的 —— 每个项目有各自的采集规则、各自落一份快照，
  //    真按「平台×环境」去重会让后两个项目的快照永远不更新。
  //
  // 🔴 「同一环境被打 N 遍」这个真实问题不在这里解决，
  //    而在后端：collector 对「集群 + 完整规则」都相同的拉取做同轮复用，
  //    规则不同的项目仍各拉各的。放在前端去重是解决不了的 ——
  //    定时采集根本不经过这里。
  // 🔴 平台级列（projectId = PROJECT_ALL）只是**视图**，底层仍是一个个项目：
  //    新鲜度按真实 project_id 记、采集也按真实 project_id 做。
  //
  //    不展开的话 colKeyOf(org, -1, env) 在 fresh 里永远查不到 →
  //    isStale(undefined) 恒为 true → 每次比对都触发一次注定失败的采集
  //    （project=-1 找不到环境）→ 界面报「这些列刷新失败，用的是上次采到的数据」，
  //    而平台页明明显示「采集正常」。两处自相矛盾，且每次比对都白打对方系统一次。
  //
  // ⚠️ 展开只用于**执行**，结果最后要聚合回调用方给的那些列 ——
  //    否则一个平台级列会产出 N 条同名 outcome（"我方·UAT、我方·UAT"）。
  const expand = (c: { orgId: number; projectId: number; env: string; orgName: string }) => {
    if (c.projectId !== PROJECT_ALL) return [c]
    const rows = [...fresh.values()].filter((f) => f.org_id === c.orgId && f.env === c.env)
    // 兜底：一条新鲜度记录都没有（从没采过）时原样保留，让它走正常的失败路径
    const real = rows.filter((f) => f.project_id !== 0)
    return real.length ? real.map((f) => ({ ...c, projectId: f.project_id })) : [c]
  }

  // 展开后的子列 key → 它属于调用方的哪一列（用于把结果聚合回去）
  const ownerOf = new Map<string, string>()
  const uniq = new Map<string, { orgId: number; projectId: number; env: string; orgName: string }>()
  for (const c of cols) {
    const owner = colKeyOf(c.orgId, c.projectId, c.env)
    for (const sub of expand(c)) {
      const k = colKeyOf(sub.orgId, sub.projectId, sub.env)
      ownerOf.set(k, owner)
      if (!uniq.has(k)) {
        uniq.set(k, { orgId: sub.orgId, projectId: sub.projectId, env: sub.env, orgName: sub.orgName })
      }
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

  // 🔴 把展开后的子列结果**聚合回调用方给的那些列**。
  //    一个平台级列展开成 N 个项目，不聚合的话界面会显示
  //    「我方·UAT、我方·UAT 刷新失败」这种重复项。
  // ⚠️ 聚合取最"坏"的结果：任一项目失败 → 整列算失败。
  //    这一列的判定用到了它下面每一个项目的数据，
  //    有一个没刷上就不能说"这一列是最新的"。
  const byOwner = new Map<string, RefreshOutcome>()
  for (const r of results) {
    const owner = ownerOf.get(r.key) ?? r.key
    const prev = byOwner.get(owner)
    const rank = (s: RefreshOutcome['state']) => (s === 'failed' ? 2 : s === 'ok' ? 1 : 0)
    if (!prev || rank(r.state) > rank(prev.state)) {
      byOwner.set(owner, { ...r, key: owner })
    }
  }
  return [...byOwner.values()]
}

export async function loadFreshness(): Promise<Map<string, Freshness>> {
  const list = await api<Freshness[]>('/api/columns/freshness')
  return new Map(list.map((f) => [colKeyOf(f.org_id, f.project_id, f.env), f]))
}
