import type { ReactNode } from 'react'
import { Banner } from './Banner.js'

/**
 * 写操作失败时的错误条。
 *
 * 🔴 存在的理由：这段渲染逐处手写时，一半的地方会写反。
 *
 *	正确的是「主文案 + 小字技术描述」两行。写成 `detail || t(messageKey)` 就永远
 *	只剩技术描述 —— 因为 detail **总是有值**（buildDetail 会生成
 *	"POST /api/environments → 409" 这种串）。于是后端精心给的
 *	"环境「PROD」已存在" / "还有 18 条在用" 全被吃掉，
 *	用户看到的只有一个 HTTP 状态码。
 *
 *	实测：同一个文件里删除路径写对了、创建路径写反了 —— 知道这个坑的人
 *	只修了当时踩到的那一处。所以这段不能靠自觉，得收口成一个组件。
 *
 * 用法：<MutationError error={mut.error} t={t} /> —— t 由调用方传，
 * 因为 @ops/ui 不依赖 i18n（白牌客户可能换整套文案）。
 */
export interface MutationErrorProps {
  /** react-query 的 mutation.error，或任何能被 toErrorInfo 归一的东西 */
  error: unknown
  /** 归一函数，由产品侧传入（避免 @ops/ui 依赖 @ops/api） */
  toInfo: (e: unknown) => { messageKey: string; params?: Record<string, unknown>; detail?: string }
  /** 翻译函数 */
  t: (key: string, params?: Record<string, unknown>) => string
  /**
   * messageKey 没有对应文案时的兜底 key。
   * ⚠️ 不给的话会退化成显示生的 key（如 "error.duplicateName"）。
   */
  fallbackKey?: string
  action?: ReactNode
}

export function MutationError({ error, toInfo, t, fallbackKey, action }: MutationErrorProps) {
  const n = toInfo(error)
  const translated = t(n.messageKey, n.params)
  // t 找不到 key 时多数 i18n 实现会原样返回 key —— 那不能给用户看
  const main = translated === n.messageKey ? (fallbackKey ? t(fallbackKey) : n.detail || '') : translated
  const showDetail = n.detail && n.detail !== main
  return (
    <Banner tone="bad" action={action}>
      <span>{main}</span>
      {showDetail ? <span className="mt-0.5 block text-xs opacity-80">{n.detail}</span> : null}
    </Banner>
  )
}
