import type { ReactNode } from 'react'
import { ErrorState } from './ErrorState.js'
import type { LoadState } from './types.js'

export interface AsyncBoundaryProps<T> {
  state: LoadState<T>
  /** 加载中。用 TableSkeleton / 骨架卡，不要转圈。 */
  pending: ReactNode
  /**
   * 空态。**必填**，且必须是 `<EmptyState>` ——
   * 它会强制你写清楚为什么空、下一步做什么。
   */
  empty: ReactNode
  /** 错误标题，人话。比如「无法读取主机列表」而不是「Request failed」。 */
  errorTitle: string
  /** 重试按钮文案，来自语言包。组件库不含任何面向用户的字符串。 */
  retryLabel: string
  onRetry: () => void
  /** 错误态的额外出路，比如「查看凭据配置」。 */
  errorActions?: ReactNode
  /**
   * 「这个功能没在授权里」时显示什么。
   *
   * 🔴 不给的话，被门控拦下的页面会渲染成**错误态 + 重试** ——
   *	而重试一万次也不会变成已购。给一句人话就够了（见 NotLicensed）。
   *
   * ⚠️ 不给不是 bug：不是每个页面都属于付费功能。
   *	只有当 error.kind === 'license' 时才会用到它。
   */
  notLicensed?: (feature: string | undefined) => ReactNode
  children: (data: T) => ReactNode
}

/**
 * 四态强制分流。
 *
 * 这个组件的价值不在于少写几行 if，而在于**你没法只写其中两个分支**。
 * pending / empty / errorTitle / children 四个都是必填 props，
 * 漏掉任何一个都是编译错误。
 *
 * 对照过去的写法：
 *   {loading ? <Spin/> : data.length ? <Table/> : <Empty/>}
 * 这行代码里 error 分支根本不存在 —— 请求失败时 data 是空数组，
 * 于是失败被渲染成了「暂无数据」。它不报错，所以能活很久。
 */
export function AsyncBoundary<T>({
  state,
  pending,
  empty,
  errorTitle,
  retryLabel,
  onRetry,
  errorActions,
  notLicensed,
  children,
}: AsyncBoundaryProps<T>) {
  switch (state.status) {
    case 'pending':
      return <>{pending}</>
    case 'error':
      // 授权问题优先分流：它不是"加载失败"，下一步动作也不是"重试"
      if (state.error.kind === 'license' && notLicensed) {
        return <>{notLicensed(state.error.feature)}</>
      }
      return (
        <ErrorState
          title={errorTitle}
          error={state.error}
          onRetry={onRetry}
          retryLabel={retryLabel}
          actions={errorActions}
        />
      )
    case 'empty':
      return <>{empty}</>
    case 'ready':
      return <>{children(state.data)}</>
  }
}
