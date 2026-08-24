import { Component, type ErrorInfo, type ReactNode } from 'react'

export interface ErrorBoundaryProps {
  children: ReactNode
  /** 渲染兜底界面。拿到的是原始异常，调用方决定怎么展示 */
  fallback: (error: Error, reset: () => void) => ReactNode
  /** 上报钩子。不填就只打 console —— 但生产上应该接监控 */
  onError?: (error: Error, info: ErrorInfo) => void

  /**
   * 这个值变了就自动复位。
   *
   * 典型用法是传"当前页"：某一页崩了之后切到别的页，应当直接恢复正常，
   * 而不是继续显示上一页的错误界面 —— 那会让人以为整个应用都坏了。
   * 没有它的话唯一的出路是刷新，而刷新往往又回到崩掉的那一页。
   */
  resetKey?: unknown
}

interface State {
  error: Error | null
}

/**
 * 渲染期异常兜底。
 *
 * ⚠️ `AsyncBoundary` 管的是**请求**失败，管不了**渲染**时抛的异常
 * （字段形状与预期不符、漏了 import、undefined.length）。
 * 没有这一层的话，任何一个页面的一处笔误都会白屏整个应用 ——
 * 连左边的菜单都没了，用户完全不知道发生了什么，只能刷新。
 *
 * 实际撞到过：一个接口返回裸数组而代码按 `{ items }` 读，
 * 整站白屏，控制台里只有一句 `Cannot read properties of undefined`。
 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, State> {
  override state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  override componentDidUpdate(prev: ErrorBoundaryProps) {
    // 只在**已经处于错误态**时才比较：正常渲染时每次 update 都比一次是白费，
    // 而且会把 fallback 里的重试按钮变成多余的。
    if (this.state.error && prev.resetKey !== this.props.resetKey) {
      this.setState({ error: null })
    }
  }

  override componentDidCatch(error: Error, info: ErrorInfo) {
    // 默认也要打出来：吞掉异常等于让这个 bug 再也查不到
    if (this.props.onError) this.props.onError(error, info)
    else console.error('[ErrorBoundary]', error, info.componentStack)
  }

  override render() {
    if (this.state.error) {
      return this.props.fallback(this.state.error, () => this.setState({ error: null }))
    }
    return this.props.children
  }
}
