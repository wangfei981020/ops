/**
 * 主题与密度的运行时控制。
 *
 * 三态而非两态：dark / light / **system**（默认）。
 * 只给 dark/light 两个选项是常见错误 —— 大多数人真正想要的是跟随系统。
 */

export type ThemeMode = 'dark' | 'light' | 'system'
export type ResolvedTheme = 'dark' | 'light'
export type Density = 'compact' | 'default' | 'comfortable'

const THEME_KEY = 'ops.theme'
const DENSITY_KEY = 'ops.density'

const listeners = new Set<(t: ResolvedTheme) => void>()

/** localStorage 在隐私模式/沙箱 iframe 下会抛异常，一律降级为「读不到」而非崩溃。 */
function safeGet(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function safeSet(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    /* 存不下就只在本次会话生效，不影响功能 */
  }
}

export function getThemeMode(): ThemeMode {
  const v = safeGet(THEME_KEY)
  return v === 'dark' || v === 'light' ? v : 'system'
}

/**
 * 当前**实际生效**的主题。
 *
 * ECharts / Canvas 这类不吃 CSS 变量的东西必须问这个函数，
 * 而不是问 getThemeMode() —— 后者返回 'system' 时它们不知道该画哪套色。
 */
export function resolveTheme(mode: ThemeMode = getThemeMode()): ResolvedTheme {
  if (mode !== 'system') return mode
  if (typeof window === 'undefined' || !window.matchMedia) return 'dark'
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export function setThemeMode(mode: ThemeMode): void {
  const root = document.documentElement
  if (mode === 'system') {
    root.removeAttribute('data-theme')
  } else {
    root.setAttribute('data-theme', mode)
  }
  safeSet(THEME_KEY, mode)
  notify()
}

export function getDensity(): Density {
  const v = safeGet(DENSITY_KEY)
  return v === 'compact' || v === 'comfortable' ? v : 'default'
}

export function setDensity(d: Density): void {
  document.documentElement.setAttribute('data-density', d)
  safeSet(DENSITY_KEY, d)
}

function notify(): void {
  const t = resolveTheme()
  for (const fn of listeners) fn(t)
}

/**
 * 订阅「实际生效主题」的变化，返回取消订阅函数。
 *
 * 两个触发源都要覆盖：用户手动切换、以及 mode=system 时操作系统自己变了
 * （macOS 的日出日落自动切换）。只监听前者会让图表在傍晚配色错乱。
 */
export function onThemeChange(fn: (t: ResolvedTheme) => void): () => void {
  listeners.add(fn)

  const mq = window.matchMedia?.('(prefers-color-scheme: dark)')
  const onSystem = () => {
    if (getThemeMode() === 'system') notify()
  }
  mq?.addEventListener('change', onSystem)

  return () => {
    listeners.delete(fn)
    mq?.removeEventListener('change', onSystem)
  }
}

/**
 * 从 CSS 读出当前主题下某个 token 的实际色值。
 *
 * 给 ECharts 用：它需要具体色串，不认 var()。
 * 必须在主题切换**之后**再读，否则拿到的是上一套配色。
 */
export function readToken(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

/** 图表 8 色，顺序与 --ops-chart-1..8 一致。 */
export function chartPalette(): string[] {
  return Array.from({ length: 8 }, (_, i) => readToken(`--ops-chart-${i + 1}`))
}
