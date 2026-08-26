/** 后端 /api/compare 的返回。字段名与 Go 结构体一一对应（注意是大驼峰） */

export interface CompareColumn {
  OrgID: number
  OrgName: string
  Env: string
  ProjectID: number
  /** 单项目平台为空 —— 后端决定要不要显示，前端只管「有就显示」 */
  ProjectName: string
  SyncStatus: string
  SyncError: string
  /**
   * 这一列的版本是从 Pod 反推的（读不到 deployments）。
   *
   * ⚠️ **界面上不显示这个** —— 客户普遍只给 Pod 读权限，所以它是常态不是故障，
   *    每列常驻一个警告等于没有警告。字段保留是因为它仍然有用：
   *    采集日志里的 version_from 靠它，事后要查「这一列当时怎么采的」只有这一条路。
   *
   * 🔴 它带来的**真实后果**已经写在格子上了：副本为 0 的服务在 Pod 层没有痕迹，
   *    那些格子写的是「没有运行中的实例」而不是「未部署」——
   *    后者会把"我们看不见"讲成"事实是否定的"。
   */
  Degraded: boolean
  DegradedNote: string
  /**
   * 这一列被采集规则实际排掉的服务：ServiceKey → 规则匹配到的 workload 名。
   *
   * 🔴 数量必须在表头显示出来。生产上 我方 的 UAT 列排掉了 67 个服务
   *    （`workload_exclude: *-game-frontend` 等），而对面 A公司 只有一个项目配了
   *    同样的规则 —— 于是「两个平台服务数差一大截」在界面上看不出任何原因，
   *    人的第一反应是「采漏了」或「对方没部署」。
   *
   * ⚠️ 只给数量和名字，不给版本：这些服务没有参与对账的资格。
   */
  ExcludedKeys?: Record<string, string>
}

/**
 * 返回体里一列的标识。
 *
 * 🔴 必须与后端 `Column.Key()` **逐字一致**：
 * `result.baseline`、`result.data` 的键都是后端用 Key() 拼的，
 * 这边少拼一段「·项目名」，基准列的标记就再也匹配不上 ——
 * 而界面上不报错，只是那颗「基准」标记消失了，没人会注意到。
 * 所以只留这一个函数，别在各处手拼。
 */
export const respColKey = (c: CompareColumn) =>
  c.ProjectName ? `${c.OrgName}·${c.ProjectName}/${c.Env}` : `${c.OrgName}/${c.Env}`

/**
 * 一列的**稳定**标识，与后端 `Column.StableKey()` 逐字一致。
 *
 * 🔴 忽略规则存的是这个，不是 respColKey：
 * respColKey 含平台名和项目名，改个名规则就全失效了 ——
 * 而失效时不报错，只是「我明明忽略过的服务又冒出来了」。
 */
export const stableOf = (c: CompareColumn) => `${c.OrgID}/${c.ProjectID}/${c.Env}`

export interface Snapshot {
  ServiceKey: string
  Tag: string
  RunningTag: string
  Digest: string
  Namespace: string
  Workloads: string[] | null
  BuildNo: number | null
  IsVersioned: boolean
  HasConflict: boolean
}

/**
 * 一行的结论。**五态，一个都不能少。**
 *
 * 🔴 没有基准，判定**没有方向** —— 只能说"这几列彼此一不一样"，
 * 说不了"谁落后谁"。这张表可能是别的两个平台之间的对账，
 * 我方根本不在里面，那时"落后 8 个版本"这句话没有主语。
 *
 * ⚠️ 判定由**后端**算（compare.RowVerdict），前端不许再算一遍。
 */
export type Verdict = 'same' | 'diff' | 'missing' | 'unknown' | 'ignored'

/**
 * 一格的状态。
 *
 * 🔴 `missing`（对方确实没部署）与 `no_data`（我们没读到）**必须分开**：
 * 混成一个的话，对方 token 过期会显示成"对方把服务全下线了"——
 * 处理方向正好反了（查我们自己 vs 找对方确认）。
 */
export type CellState =
  | 'version' | 'missing' | 'no_data'
  | 'unversioned' | 'conflict' | 'ignored'

/**
 * 差异归因：这个版本的镜像推没推到对方那边。
 *
 * 🔴 `unknown` 与 `not_synced` 严格分开：
 * 前者是「我们不知道」（没绑复制规则 / Harbor 没配 / 还没拉过），
 * 后者是「确实没推过去」。混成一个的话，一个「忘了绑定」会被显示成
 * 「镜像没同步」，人会跑去查 Harbor 而真正的问题是这边少配了一行。
 */
export type SyncAttr = 'synced' | 'sync_failed' | 'not_synced' | 'unknown'

export interface Cell {
  Column: CompareColumn
  State: CellState
  Snap: Snapshot | null
  /** 声明的 tag 与实际在跑的不一致 = 正在滚动更新。附加标记，不是主判定 */
  Deploying: boolean
  Note: string
  /** 差异归因。仅非一致的格子有值 */
  Sync?: SyncAttr
  SyncNote?: string
}

export interface Row {
  ServiceKey: string
  Cells: Cell[]
  /** 这一行的结论。**判定的唯一出口** —— 前端只翻译成颜色，不再自己算 */
  Verdict: Verdict
  /** 需不需要人去看（结论不是"一致"也不是"已忽略"）。「只看差异」筛的就是它 */
  HasDiff: boolean
}

export interface CompareResult {
  columns: CompareColumn[]
  rows: Row[]
  /** 按**行**统计（一行一个结论），不是按格子 */
  summary: Partial<Record<Verdict, number>>
  /**
   * 被**逐格忽略**的格子数。
   * 🔴 单独给：summary 按行统计，只忽略了某一列的格子在里面完全看不见，
   *    而「忽略必须看得见」是硬要求。
   */
  ignored_cells?: number
  /** 采集失败的列。🔴 必须在页面顶部显著提示 —— 整列 no_data 时
   *  表面只是几个灰格子，但结论已经不完整了 */
  unhealthy_columns: CompareColumn[] | null
  /** 结果几乎全是「没有」—— 多半是列选错了，不是两边真的都没部署 */
  mostly_missing?: boolean
  /**
   * 与其余列服务名重合度最低的那一列，以及重合百分比。
   *
   * 🔴 「多半是列选错了」这句警告本身是对的，但只说"你去检查一下"
   *    等于把问题原样还给用户。指名道姓才是可执行的下一步。
   * ⚠️ 空串 = 没有明显异类，此时不要瞎指一列 —— 指错了会让用户
   *    取消勾选本该参与对账的数据。
   */
  worst_overlap_col?: string
  worst_overlap_pct?: number
  /**
   * 被**整行忽略**的服务名。
   * 🔴 这些服务不在 rows 里 —— 必须把名字显示出来，
   *    否则通配规则（`bi-*`）命中了哪些完全看不见。
   */
  ignored_rows: string[] | null
}

export interface OrgEnv {
  env: string
  cluster_refs: string[] | null
  compare_enabled: boolean
  /** 所属项目。0/未设 = 归入该平台的默认项目 */
  project_id?: number
}

export interface Org {
  id: number
  name: string
  provider_type: string
  auth_type: string
  endpoint: string
  harbor_host: string
  harbor_project: string
  is_self: boolean
  /** 停用的平台仍在列表里（可编辑/重新启用），但不参与比对列。可选：兼容旧后端 */
  enabled?: boolean
  has_credential: boolean
  sync_status: string
  sync_at: string | null
  sync_error: string
  envs: OrgEnv[]
  /** 该平台下的项目。可选：兼容旧后端（那时一个平台就是一列） */
  projects?: ProjectRef[]
}

export interface ProjectRef {
  id: number
  name: string
  enabled: boolean
}

/** 一个可选的对比列 = (平台, 项目, 环境) */
export interface ColumnChoice {
  orgId: number
  orgName: string
  env: string
  projectId: number
  /** 表头上要不要显示项目名。单项目平台留空 —— 「A平台·默认/UAT」是纯噪音 */
  projectName: string
}

/**
 * 可参与比对的列 = 启用的平台 × 它的各个环境。
 *
 * 🔴 必须过滤停用平台。不过滤的话：
 *   - 列表里挂着一堆早就停用的平台，每次比对都要手动取消勾选
 *   - 「比对前自动刷新」会一并去采它们，然后**全部失败**，
 *     把一屏红色的"采集失败"糊在真正的差异上面
 * ⚠️ `enabled` 可能是 undefined（后端老版本没这个字段），
 *    只在**明确为 false** 时才排除 —— 否则升级过程中会一列都不剩。
 */
/**
 * 「整个平台在该环境上的全部项目」这一列的 projectId。
 *
 * 🔴 与后端 projectAll 常量必须一致（-1）。
 *    用 -1 而不是 0：0 已经是「回落到默认项目」的语义，改它会动到方案里存的老列。
 */
export const PROJECT_ALL = -1

/**
 * 平台级列：一个平台在一个环境上算**一列**，把它下面所有项目滚在一起。
 *
 * 🔴 为什么需要这一层：一列 = 项目 × 环境，而同一个服务在两个平台的
 *    **不同项目**里跑是常态 —— 逐项目比必然大面积「缺失」。
 *    实测过：5 列逐项目比 = 122 行全判缺失、0 条有效结论；
 *    平台级汇总后是「42 一致 / 22 版本不同 / 8 一方整体未部署」。
 *
 * ⚠️ 环境**不合并**：跨环境比版本没有意义（UAT 跑 v9 而 PROD 跑 v8 是正常的，
 *    不是"差异"）。所以一个平台有几个环境就有几列。
 */
export function orgColumnsOf(orgs: Org[]): ColumnChoice[] {
  return orgs
    .filter((i) => i.enabled !== false)
    .flatMap((i) => {
      // 同一平台同一环境可能挂着多行（每个项目一行），这里要按环境去重
      const envs = [...new Set((i.envs ?? []).map((e) => e.env))]
      return envs.map((env) => ({
        orgId: i.id,
        orgName: i.name,
        env,
        projectId: PROJECT_ALL,
        projectName: '全部项目',
      }))
    })
}

/**
 * 建平台时自动生成的占位项目名。
 *
 * ⚠️ 只认这几个确切写法，不做模糊匹配 —— 客户真把项目命名成「默认线路」时
 * 那是有意义的名字，不能替人家藏起来。与后端 isPlaceholderProject 一一对应。
 */
function isPlaceholderProject(name?: string): boolean {
  return ['默认', 'default', 'Default', 'DEFAULT'].includes((name ?? '').trim())
}

export function columnsOf(orgs: Org[]): ColumnChoice[] {
  return orgs
    .filter((i) => i.enabled !== false)
    .flatMap((i) => {
      const projs = (i.projects ?? []).filter((p) => p.enabled)
      // ⚠️ 显示项目名的判据必须与后端**逐条一致**（countEnabledProjects + isPlaceholderProject）。
      //    两边判据不一样的话，表头写着「A平台/UAT」而导出的文件里是「A平台·项目B/UAT」，
      //    收到附件的人对不上是同一列。
      const multi = projs.length > 1
      return (i.envs ?? []).map((e) => {
        // 🔴 环境自己挂的项目**就是**归属，先按它找；找不到（项目被删/停用）才回落到第一个。
        //    别把回落写成无条件 projs[0]：项目列表按名字排序，「默认」很可能不在第一位，
        //    于是这一列会被另一个项目的通配规则筛过 —— 表格看着正常，服务少了一多半。
        //    （后端 resolveProject 同一条规则，两边必须一致。）
        const pr = projs.find((p) => p.id === e.project_id) ?? projs[0]
        return {
          orgId: i.id,
          orgName: i.name,
          env: e.env,
          projectId: pr?.id ?? 0,
          // 🔴 「默认」是建平台时自动生成的占位名，不占表头位置：
          //    「A公司/UAT」比「A公司·默认/UAT」清楚，而且不会被读成"有个叫默认的项目"。
          //    有真实名字的项目（项目B 等）照常显示，两者并排自然就区分开了。
          projectName: multi && !isPlaceholderProject(pr?.name) ? (pr?.name ?? '') : '',
        }
      })
    })
}

/**
 * 列标识。
 *
 * 🔴 必须含 projectId：同一平台同一环境下的两个项目是**两列**，
 * 少了它两列的 key 完全一样 —— 勾选状态、基准选择、忽略规则全部串在一起，
 * 而 React 的 key 重复只在控制台警告一句，界面上看不出来。
 */
export const colKey = (c: ColumnChoice) => `${c.orgId}/${c.projectId}/${c.env}`

/** 列在界面上的名字。与后端 Column.Key() 同一套拼法 */
export const colLabel = (c: ColumnChoice) =>
  c.projectName ? `${c.orgName}·${c.projectName}/${c.env}` : `${c.orgName}/${c.env}`

/**
 * 这一列被采集规则排掉了几个服务。
 *
 * ⚠️ 单独一个函数而不是内联 `Object.keys(...).length`：
 * 表头和提示文案两处都要用同一个数，两处各写一次迟早分叉。
 */
export const excludedCount = (c: CompareColumn) => Object.keys(c.ExcludedKeys ?? {}).length
