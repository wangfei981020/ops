export interface Harbor {
  id: number
  name: string
  endpoint: string
  username: string
  /** 🔴 只说有没有，不说是什么 —— 凭据永不回显 */
  has_credential: boolean
  insecure_tls: boolean
  /**
   * 只拉这几条复制规则（按规则名，支持 * 通配）。留空 = 全部。
   * 🔴 与「绑定平台」不是一回事：那个说这条规则推给谁，这个说我们只关心哪几条。
   */
  policy_filter: string[] | null
  enabled: boolean
  last_sync_at: string | null
  last_sync_status: string
  last_sync_error: string
}

export interface Policy {
  id: number
  harbor_id: number
  harbor_name: string
  policy_id: number
  name: string
  dest_registry: string
  /** 绑了平台才能做归因：这条规则把镜像推给谁 */
  org_id: number | null
  org_name: string
  trigger_type: string
  enabled: boolean
}

/** 执行记录的一页 */
export interface ExecPage {
  rows: Execution[] | null
  /** 全表总条数。🔴 不给的话人不知道自己看到的是不是全部 */
  total: number
  /** 下一页游标（id）。0 = 没有下一页 */
  next_before: number
  /**
   * 下一页游标的时间部分。
   *
   * 🔴 排序键是 (started_at, id)，游标就得是这两个 ——
   * 只用 id 的话切出来的不是「排在这一行之后」那一批，会重复也会漏。
   */
  next_before_at?: string
}

export interface Execution {
  id: number
  policy_name: string
  harbor_name: string
  org_name: string
  exec_id: number
  trigger_type: string
  status: string
  total: number
  succeeded: number
  failed: number
  started_at: string | null
  ended_at: string | null
}

export interface ProbeResult {
  ok: boolean
  /** auth_failed | unreachable | forbidden —— 三种处理方式完全不同，不能混成「连接失败」 */
  kind?: string
  message: string
}

export interface Channel {
  id: number
  name: string
  kind: string
  /** 🔴 只说配没配 —— webhook 里带 token，等同于凭据，永不回显 */
  has_webhook: boolean
  enabled: boolean
  org_id: number | null
  org_name: string
}

export interface NotifyRecord {
  id: number
  channel: string
  level: string
  trigger_type: string
  /**
   * 🔴 三态，不是布尔：
   *   sent    发出去了
   *   skipped 规则判定不该发（自动+成功）—— 这是**正常**的，不是故障
   *   failed  该发但没发出去 —— 这才是要查的
   */
  state: string
  /** 为什么是这个 state。直接来自后端的分级判定，不在前端重拼 */
  reason: string
  err_msg: string
  attempts: number
  content: string
  created_at: string
}

/** 一条复制规则推过的一个服务 */
export interface PolicyService {
  service_key: string
  /** 推过多少个版本 */
  tags: number
  /** 最近一次推它的时刻（RFC3339，**带时区偏移**）；没推过时为 null */
  last_at: string | null
  /** 其中失败了多少次 —— 非零时这个服务要优先看 */
  failed: number
}

/** 一次「把某个版本推给某方」的记录。全部来自我们自己的库，不打 Harbor */
export interface SyncTaskRow {
  policy_name: string
  org_name: string
  service_key: string
  tag: string
  status: string
  err_msg: string
  /**
   * 推送完成时刻（RFC3339，**带时区偏移**）；未完成时为 null。
   *
   * ⚠️ 后端一度用 `DATE_FORMAT(..., '...Z')` 手拼这个串，把本地时间
   * 贴上 UTC 标签，`new Date()` 再按 UTC 转一次，界面整整差 8 小时。
   * 现在由后端序列化 time.Time 产出，偏移是真的，直接 new Date 即可。
   */
  finished_at: string | null
}
