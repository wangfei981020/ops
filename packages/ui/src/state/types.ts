/**
 * 四态模型。
 *
 * 这套类型存在的唯一目的，是让「失败被渲染成空态」在编译期就写不出来。
 *
 * 历史事故：接口 500 了，页面显示「暂无数据」，看的人以为真的没数据，
 * 于是照着一个空列表做了决策。这类 bug 不报错、不白屏、监控也看不见，
 * 只能靠类型把四条分支全逼出来。
 *
 * empty 单独成一态而不是 `ready` + 空数组：空态需要说明**为什么空**，
 * 而那个原因只有调用方知道（没配数据源？筛选太窄？确实没有？）。
 */

export type LoadState<T> =
  | { status: 'pending' }
  | { status: 'error'; error: LoadError }
  | { status: 'empty' }
  | { status: 'ready'; data: T }

/**
 * 错误的结构化描述。
 *
 * 刻意不接受裸 Error —— 裸 Error 的 message 通常是给开发看的英文栈信息，
 * 直接甩给用户既看不懂也不可行动。这里强制拆成「人话原因」和「技术细节」。
 */
export interface LoadError {
  /**
   * 人话：出了什么问题、可能是什么原因。
   *
   * ⚠️ 必须是**已翻译**的文本。本包不含任何语言包，也不该含 ——
   * 组件库里一旦出现中文字面量，英文界面就会零星漏中文，
   * 而且只在错误路径上出现，正常测试根本走不到。
   */
  cause: string
  /** 可定位的技术细节：请求路径、状态码、request id。给运维排查用 */
  detail?: string
  /** 是否值得重试。鉴权失败、参数错误这类重试一万次也没用 */
  retryable: boolean
  /**
   * 这个失败属于哪一类。目前只区分出 `license` 一档，其余留空。
   *
   * 🔴 为什么单拎出授权：**「没买这个功能」不是错误**。
   *	把它渲染成"加载失败 + 重试"，用户会一直重试，
   *	而重试一万次也不会变成已购 —— 他需要的是知道去加购什么。
   *	
   *
   * ⚠️ 留空**不代表**不是授权问题，只代表调用方没有分辨 ——
   *	所以 AsyncBoundary 在没有 licensed 渲染时仍按普通错误显示，不做猜测。
   */
  kind?: 'license'
  /** 缺的功能名（如 `exposure`）。原样显示，让人拿着它去问加购 */
  feature?: string
}

/**
 * 「请求成功但没拿到数据」的标记，传给调用方的 toError 去生成文案。
 * 用唯一的 symbol-like 常量而不是字符串，避免和真实的 Error 混淆。
 */
export const MALFORMED_RESPONSE = { __opsLoadError: 'malformed-response' } as const

interface QueryLike<T> {
  isPending: boolean
  isError: boolean
  error: unknown
  data: T | undefined
}

/**
 * 把 TanStack Query 的结果收敛成四态。
 *
 * 刻意用结构化类型而不是 import UseQueryResult ——
 * ui 包不该依赖数据层，否则组件库就绑死在一个请求库上了。
 *
 * @param isEmpty 判空规则由调用方给：有的页面空数组算空，
 *                有的页面 `{items: []}` 才算空，没有通用答案。
 */
export function fromQuery<T>(
  query: QueryLike<T>,
  isEmpty: (data: T) => boolean,
  toError: (e: unknown) => LoadError,
): LoadState<T> {
  if (query.isPending) return { status: 'pending' }
  if (query.isError) return { status: 'error', error: toError(query.error) }
  if (query.data === undefined) {
    // 不 pending、不 error、又没数据 —— 这是请求库层面的异常，
    // 绝不能悄悄当成空态放过去。
    //
    // 文案同样交给调用方的 toError 生成（传一个可辨识的标记），
    // 本包不产出任何面向用户的字符串。
    return { status: 'error', error: toError(MALFORMED_RESPONSE) }
  }
  return isEmpty(query.data) ? { status: 'empty' } : { status: 'ready', data: query.data }
}
