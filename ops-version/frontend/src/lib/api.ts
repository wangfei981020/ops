import type { LoadError } from '@ops/ui'

/** i18next 的 t，只用到取值这一个能力，不绑死具体实现 */
type TFunc = (key: string) => string

/**
 * 后端 API 客户端。
 *
 * 统一在这里做两件事，别在各个路由里各写一遍：
 *   1. 把后端的 `{data}` / `{error:{code,message}}` 拆开
 *   2. 把错误归一成 `LoadError`，让 `<AsyncBoundary>` 能直接消费
 */

export interface ApiError extends Error {
  /** 可判别的错误码，前端据此决定跳登录还是提示。
   *  🔴 不要靠 message 文案做判断 —— 改文案就失效了 */
  code: string
  status: number
}

function makeError(status: number, code: string, message: string): ApiError {
  const e = new Error(message) as ApiError
  e.code = code
  e.status = status
  return e
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  let resp: Response
  try {
    resp = await fetch(path, {
      ...init,
      headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
    })
  } catch (e) {
    // 网络层失败：和后端返回的业务错误必须分开 ——
    // 前者重试可能有用，后者重试一定还是同样的结果
    throw makeError(0, 'network', e instanceof Error ? e.message : String(e))
  }

  const text = await resp.text()
  let body: unknown
  try {
    body = text ? JSON.parse(text) : {}
  } catch {
    throw makeError(resp.status, 'bad_response', `响应不是合法 JSON（HTTP ${resp.status}）`)
  }

  if (!resp.ok) {
    const err = (body as { error?: { code?: string; message?: string } }).error
    throw makeError(resp.status, err?.code ?? 'unknown', err?.message ?? resp.statusText)
  }
  return (body as { data: T }).data
}

/**
 * toLoadError 把异常转成 AsyncBoundary 认的错误态。
 *
 * 🔴 `cause` 必须是**已翻译**的人话 —— `@ops/ui` 不含语言包，
 * 组件库里一旦出现中文字面量，英文界面就会零星漏中文，
 * 而且只在错误路径上出现，正常测试根本走不到。所以翻译在这里做。
 *
 * 🔴 `retryable` 要如实：鉴权失败、权限不足重试一万次也是同样结果，
 * 给个重试按钮只会让人反复点。
 */
export function toLoadError(e: unknown, t: TFunc): LoadError {
  const err = e as ApiError
  const code = err?.code ?? 'unknown'
  const detail = err?.status ? `HTTP ${err.status} · ${code}` : code

  switch (code) {
    case 'unauthenticated':
      return { cause: t('common:error.unauthenticated'), detail, retryable: false }
    case 'forbidden':
      return { cause: t('common:error.forbidden'), detail, retryable: false }
    case 'network':
      return { cause: t('common:error.network'), detail: err?.message, retryable: true }
    default:
      // 后端返回的 message 已经是中文人话（见 handlers 里的 fail 调用），直接用；
      // 拿不到才回落到通用文案
      return { cause: err?.message || t('common:error.unknown'), detail, retryable: true }
  }
}

/**
 * 下载二进制附件（导出用）。
 *
 * 🔴 不能复用 `api()`：它无条件 `resp.text()` 再 `JSON.parse`，
 * 拿 xlsx 的字节去解析 JSON 必然抛「响应不是合法 JSON」——
 * 而那时文件其实已经生成好了，只是被前端自己扔掉。
 *
 * ⚠️ 失败时后端返回的**仍然是 JSON**（权限不足、参数错）。
 * 所以要按 Content-Type 分流：错误走 JSON 解析拿到 code，
 * 否则用户只会看到一句「下载失败」，不知道是没权限还是没选够列。
 */
export async function download(path: string, init?: RequestInit): Promise<void> {
  let resp: Response
  try {
    resp = await fetch(path, {
      ...init,
      headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
    })
  } catch (e) {
    throw makeError(0, 'network', e instanceof Error ? e.message : String(e))
  }

  if (!resp.ok) {
    const text = await resp.text()
    try {
      const err = (JSON.parse(text) as { error?: { code?: string; message?: string } }).error
      throw makeError(resp.status, err?.code ?? 'unknown', err?.message ?? resp.statusText)
    } catch (e) {
      if (e && typeof e === 'object' && 'code' in e) throw e
      throw makeError(resp.status, 'bad_response', `导出失败（HTTP ${resp.status}）`)
    }
  }

  // 文件名优先取后端给的 filename*（含中文，已 URL 编码）
  const cd = resp.headers.get('Content-Disposition') ?? ''
  let name = 'export.xlsx'
  const star = cd.match(/filename\*=UTF-8''([^;]+)/i)
  if (star?.[1]) {
    try {
      name = decodeURIComponent(star[1])
    } catch {
      /* 解不开就用默认名，不要因为文件名把整个下载搞失败 */
    }
  }

  const blob = await resp.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  // 立刻 revoke 在部分浏览器上会让下载中断，挪到下一个事件循环
  setTimeout(() => URL.revokeObjectURL(url), 0)
}
