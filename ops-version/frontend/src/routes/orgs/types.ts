export interface EnvRow {
  env: string
  cluster_refs: string[]
  ns_include: string[]
  ns_exclude: string[]
  /** 只抄这些 workload 回来。与方案里的「服务白名单」不是一回事：那个管这次比哪些 */
  workload_include: string[]
  workload_exclude: string[]
  compare_enabled: boolean
  /** 所属项目。0/未设 = 归入该平台的默认项目（对比表的一列 = 项目 × 环境） */
  project_id: number
  /**
   * 这个环境用哪个数据源。0 = 没选，走下一层（环境手填 → 平台手填 → 平台数据源）。
   *
   * 🔴 客户 UAT / PROD 常是两套独立的 Rancher —— 两套各建一个数据源、
   *    各环境各选各的，凭据就只配一处。手填的话同一套凭据有几个环境填几遍，
   *    改密码漏掉一处的表现是那一列「认证失败」，人会去查账号本身。
   */
  datasource_id: number
  /** 只出不进：由后端 JOIN 数据源得到，用来显示当前选的是哪个 */
  datasource_name?: string
  /** 空 = 继承平台级。一个平台两套 Rancher（UAT/PROD 各一套）时在这里各填各的 */
  /**
   * 🔴 环境级连接**整组覆盖**，不逐字段回落：填了 endpoint 就必须连
   * auth_type 和凭据一起填。逐字段回落会造出「A 的地址配 B 的密码」
   * 这种组合，排查时看每一项都对、就是连不上。
   */
  endpoint: string
  auth_type: string
  username: string
  password: string
  api_key: string
  has_credential: boolean
}

export interface Org {
  id: number
  name: string
  provider_type: string
  auth_type: string
  endpoint: string
  /**
   * 引用的数据源。0 = 不引用，这个平台自己填地址和凭据。
   *
   * 🔴 引用之后地址和凭据都由数据源提供 —— 表单里不再填一遍。
   *    两处都填的话，平台自己那份优先级更高（见后端 OrgEnv.Conn），
   *    表现是「选了数据源却没生效」。
   */
  datasource_id: number
  /** 只读：数据源的名字和地址，随平台一起返回，省一次请求 */
  datasource_name: string
  ds_provider_type: string
  ds_endpoint: string
  harbor_host: string
  harbor_project: string
  is_self: boolean
  /** 停用的平台保留配置但不参与比对/采集。可选：兼容旧后端 */
  enabled?: boolean
  /** 🔴 只说有没有，不说是什么 —— 凭据永不回显 */
  has_credential: boolean
  sync_status: string
  sync_at: string | null
  sync_error: string
  envs: EnvRow[]
  /**
   * 该平台下的项目，随平台一起返回（省一次请求）。
   * 🔴 环境行要靠它显示项目名 —— 同一平台同一环境的多个项目在界面上
   *    长得一模一样时，用户看不出它们的采集范围完全不同。
   */
  projects?: { id: number; name: string; enabled: boolean }[]
}

export interface ProbeResult {
  ok: boolean
  /** auth_failed | unreachable | forbidden | error —— 三种处理方式完全不同，不能混成"连接失败" */
  kind?: string
  message: string
}

export const ENVS = ['DEV', 'TEST', 'UAT', 'PROD'] as const
export const PROVIDERS = ['kite', 'rancher', 'argocd', 'manual_import'] as const

/**
 * 各数据源支持的认证方式。
 *
 * ⚠️ ArgoCD 用 `token`（账号 token，Bearer）而不是 `api_key` ——
 * 名字不同是因为**含义不同**：Kite 的 api_key 有自己的传法（不加 Bearer），
 * 混用一个名字迟早会让人以为两边能互相粘贴。
 */
export const AUTH_BY_PROVIDER: Record<string, readonly string[]> = {
  kite: ['password', 'api_key'],
  rancher: ['password', 'api_key'],
  argocd: ['password', 'token'],
  manual_import: [],
}
export const AUTH_TYPES = ['password', 'api_key'] as const

export function emptyEnv(): EnvRow {
  return {
    env: 'UAT',
    cluster_refs: [],
    ns_include: [],
    ns_exclude: [],
    workload_include: [],
    workload_exclude: [],
    compare_enabled: true,
    project_id: 0,
    datasource_id: 0,
    endpoint: '',
    auth_type: '',
    username: '',
    password: '',
    api_key: '',
    has_credential: false,
  }
}

/**
 * 换行分隔的文本框 ↔ 字符串数组。
 *
 * 🔴 **输入期只拆分，不做任何清理。清理放到保存时（normLines）。**
 *
 * 原来这里是 `split('\n').map(trim).filter(Boolean)`，理由写的是
 * 「多敲一个回车不该变成一条 "" 规则去匹配所有 ns」—— 意图没错，
 * 但放在受控组件的 onChange 上就成了 **用户永远敲不出第二行**：
 *
 *   敲回车 → 值变 "ops-*\n" → toLines 丢掉尾部空行 → ["ops-*"]
 *          → 重渲染 fromLines → "ops-*" —— 换行当场被吃掉
 *
 * 五个多行框（集群 / ns 包含 / ns 排除 / 服务包含 / 服务排除）全中，
 * 用户只能改用逗号分隔，而后端并不认逗号。
 * ⚠️ 这类"每次按键都规范化"的写法，在任何**会删字符**的规范化上都会出事
 *    （trim 同理：末尾空格也打不出来）。规范化的位置是提交，不是按键。
 */
export const toLines = (s: string) => s.split('\n')
export const fromLines = (a: string[] | null) => (a ?? []).join('\n')

/**
 * 保存前的清理：去空白、丢空行、去重。
 *
 * 顺带把**逗号也当分隔符**：占位符写的是换行，但换行被吃掉的那段时间里
 * 用户只能拿逗号凑合（`app-*,`），这些值已经存在了。
 * ns / 服务名 / 集群名都不含逗号，认它没有歧义。
 */
export const normLines = (a: string[] | null) => {
  const out: string[] = []
  for (const raw of a ?? []) {
    for (const part of raw.split(',')) {
      const v = part.trim()
      if (v && !out.includes(v)) out.push(v)
    }
  }
  return out
}
