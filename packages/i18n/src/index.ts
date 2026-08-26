/**
 * 跨产品共享的国际化。
 *
 * 覆盖面不止 UI 文案 —— 企业级 i18n 真正会翻车的地方是下面这些：
 *   · 后端消息    后端返回 error code + 参数，由前端翻译。
 *                 后端拼好中文句子发过来，英文界面就永远漏中文。
 *   · 时区        存储一律 UTC，显示按用户时区。跨时区客户看到差 8 小时的
 *                 时间戳，会直接怀疑整个系统的数据可信度。
 *   · 数字/货币   千分位、小数点、货币符号都随语言变，且货币不都是 USD。
 *   · 长度弹性    英文普遍比中文长 30–50%，按钮和表头不能写死宽度。
 */

import i18next, { type i18n as I18nInstance } from 'i18next'
import { initReactI18next } from 'react-i18next'

export const LOCALES = ['zh-CN', 'en-US'] as const
export type Locale = (typeof LOCALES)[number]

export const DEFAULT_LOCALE: Locale = 'zh-CN'
const LOCALE_KEY = 'ops.locale'
const TZ_KEY = 'ops.timezone'

export const LOCALE_LABELS: Record<Locale, string> = {
  'zh-CN': '简体中文',
  'en-US': 'English',
}

function isLocale(v: unknown): v is Locale {
  return typeof v === 'string' && (LOCALES as readonly string[]).includes(v)
}

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
    /* 隐私模式下存不下，只影响下次冷启动 */
  }
}

/** 首选语言：用户显式选过的 > 浏览器语言 > 默认。 */
export function detectLocale(): Locale {
  const saved = safeGet(LOCALE_KEY)
  if (isLocale(saved)) return saved

  const nav = typeof navigator === 'undefined' ? '' : navigator.language
  // 浏览器可能给 zh、zh-Hans、zh-TW 等等，按前缀归并，
  // 精确匹配 'zh-CN' 会让绝大多数中文用户拿到英文界面。
  if (nav.startsWith('zh')) return 'zh-CN'
  if (nav.startsWith('en')) return 'en-US'
  return DEFAULT_LOCALE
}

export type Resources = Record<Locale, Record<string, Record<string, unknown>>>

export function createI18n(resources: Resources, locale: Locale = detectLocale()): I18nInstance {
  const instance = i18next.createInstance()
  instance.use(initReactI18next).init({
    resources,
    lng: locale,
    fallbackLng: DEFAULT_LOCALE,
    defaultNS: 'common',
    interpolation: {
      // React 自己就转义，i18next 再转一次会把 & 变成 &amp;
      escapeValue: false,
    },
    // 缺 key 时返回 key 本身而不是空串 —— 空串会让界面看起来"正常但空白"，
    // 而露出的 key 一眼就能看出漏翻，符合「失败态不能退化成空态」的一贯约定。
    parseMissingKeyHandler: (key) => key,
  })
  applyLocaleToDocument(locale)
  return instance
}

/**
 * 切语言。
 *
 * ⚠️ 必须同步更新 <html lang>：CJK 行高靠 :lang(zh) 选择器生效，
 * 只切 i18next 不改 lang，英文界面会继续用中文行高，行距明显偏大。
 */
export async function setLocale(instance: I18nInstance, locale: Locale): Promise<void> {
  await instance.changeLanguage(locale)
  safeSet(LOCALE_KEY, locale)
  applyLocaleToDocument(locale)
}

function applyLocaleToDocument(locale: Locale): void {
  if (typeof document === 'undefined') return
  document.documentElement.setAttribute('lang', locale)
}

/* ==========================================================================
 * 时区
 * ======================================================================= */

/** 用户时区，缺省取浏览器时区。 */
export function getTimezone(): string {
  const saved = safeGet(TZ_KEY)
  if (saved) return saved
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

export function setTimezone(tz: string): void {
  safeSet(TZ_KEY, tz)
}

/* ==========================================================================
 * 格式化
 *
 * 全部走 Intl，不手搓。手搓的千分位在 en-US 下是逗号、de-DE 下是点，
 * 自己写必然只对一种语言。
 * ======================================================================= */

/** 整数/小数，自动千分位。 */
export function formatNumber(value: number, locale: Locale, digits = 0): string {
  return new Intl.NumberFormat(locale, {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  }).format(value)
}

/** 金额。货币可配 —— 客户不都用 USD。 */
export function formatCurrency(value: number, locale: Locale, currency = 'USD'): string {
  return new Intl.NumberFormat(locale, {
    style: 'currency',
    currency,
    maximumFractionDigits: 2,
  }).format(value)
}

export function formatPercent(value: number, locale: Locale, digits = 1): string {
  return new Intl.NumberFormat(locale, {
    style: 'percent',
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  }).format(value)
}

/**
 * 字节。用 1024 进制（KiB 语义）但显示成 KB/MB/GB ——
 * 运维场景里 kubectl、df、free 全是 1024 进制，
 * 这里若改用 1000 进制，同一块盘在我们界面上和 df 里对不上，
 * 会让人怀疑采集数据错了。
 */
export function formatBytes(bytes: number, locale: Locale, digits = 1): string {
  if (!Number.isFinite(bytes)) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const neg = bytes < 0
  let v = Math.abs(bytes)
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  const num = new Intl.NumberFormat(locale, {
    maximumFractionDigits: i === 0 ? 0 : digits,
  }).format(v)
  return `${neg ? '-' : ''}${num} ${units[i]}`
}

/** 绝对时间，按用户时区显示。 */
export function formatDateTime(
  value: Date | string | number,
  locale: Locale,
  opts: { withSeconds?: boolean; timeZone?: string } = {},
): string {
  const d = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(d.getTime())) return '—'
  return new Intl.DateTimeFormat(locale, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: opts.withSeconds ? '2-digit' : undefined,
    hour12: false,
    timeZone: opts.timeZone ?? getTimezone(),
  }).format(d)
}

/**
 * 相对时间（"2 分钟前"）。
 *
 * 列表里用相对时间更好扫，但**详情和导出必须用绝对时间** ——
 * "3 天前"在排障复盘时毫无价值，没人能从它推回具体时刻。
 */
/**
 * 时间差 → [数值, 单位]。相对时间和时长共用这一套。
 *
 * 🔴 必须共用：`/overview` 的同一行曾同时显示「12 hours ago」和「11 小时 without an
 *	update」—— 同一个事实两个数字，差 1 小时。
 *	根因是两处各算各的：一处 `Math.round`，一处 `Math.floor`。
 *	只要取整逻辑分开写，迟早会再分叉；让它们从同一个函数出数就不可能不一致。
 */
function pickTimeUnit(diffSec: number): [number, Intl.RelativeTimeFormatUnit] {
  const abs = Math.abs(diffSec)
  const steps: [number, number, Intl.RelativeTimeFormatUnit][] = [
    [60, 1, 'second'],
    [3600, 60, 'minute'],
    [86400, 3600, 'hour'],
    [604800, 86400, 'day'],
    [2592000, 604800, 'week'],
    [31536000, 2592000, 'month'],
  ]
  for (const [limit, div, unit] of steps) {
    if (abs < limit) return [Math.round(diffSec / div), unit]
  }
  return [Math.round(diffSec / 31536000), 'year']
}

export function formatRelativeTime(
  value: Date | string | number,
  locale: Locale,
  now: Date = new Date(),
): string {
  const d = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(d.getTime())) return '—'
  const [n, unit] = pickTimeUnit(Math.round((d.getTime() - now.getTime()) / 1000))
  return new Intl.RelativeTimeFormat(locale, { numeric: 'auto' }).format(n, unit)
}

/**
 * 一段**时长**（"11 hours" / "11 小时"），不是相对时间（"11 hours ago"）。
 *
 * ⚠️ 单位不能自己拼字符串：`${h} 小时` 在 EN 模式下会渲染成
 *	`11 小时 without an update` —— 同一句话里中英混排，
 *	用户只会认为是翻译漏了。
 *
 * ⚠️ 取整与 formatRelativeTime 共用 pickTimeUnit，保证同一时刻算出的
 *	「多久以前」和「多久没更新」是同一个数。
 */
export function formatDuration(ms: number, locale: Locale): string {
  if (!Number.isFinite(ms)) return '—'
  const [n, unit] = pickTimeUnit(Math.round(Math.abs(ms) / 1000))
  // Intl.RelativeTimeFormatUnit 与 NumberFormat 的 unit 名一致（second/minute/…/year）
  return new Intl.NumberFormat(locale, {
    style: 'unit',
    unit,
    unitDisplay: 'long',
  }).format(Math.abs(n))
}

export { useTranslation, Trans, I18nextProvider } from 'react-i18next'
export type { i18n as I18nInstance } from 'i18next'
/**
 * TFunction 从这里转出，应用层不要直接 import 'i18next'。
 *
 * 它是 @ops/i18n 的传递依赖，pnpm 严格布局下应用解析不到 ——
 * 本地偶尔能过（提升的缘故），进 Docker 就必然失败，
 * 而错误信息 "Cannot find module 'i18next'" 指向的是应用代码，
 * 完全看不出是"依赖没声明"。
 */
export type { TFunction } from 'i18next'

/**
 * 翻译一条结构化错误。
 *
 * 🔴 存在的理由：后端参数里的取值是**机器名**，不是给人看的词。
 *
 *	`{code:"not_found", message_key:"error.notFoundKind", params:{kind:"user"}}`
 *	直接丢给 t() 会渲染成「找不到这个 user」—— 中英混排，
 *	而这正是后端改说 code 之后最容易出现的新问题：
 *	句子翻译了，句子里的名词没翻。
 *
 *	所以凡是"机器名"性质的参数，在这里先过一遍词表。
 *	目前只有 kind（对象种类）；将来新增同类参数往 MACHINE_PARAMS 里加。
 *
 * ⚠️ 词表里没有的取值**原样保留**，不要回退成空串 ——
 *	界面上出现一个没翻译的 `webhook` 是小问题，
 *	出现「找不到这个」（宾语消失）是句子坏了。
 */
const MACHINE_PARAMS: Record<string, string> = {
  kind: 'common:kind',
}

export function tError(
  t: (key: string, params?: Record<string, unknown>) => string,
  messageKey: string,
  params?: Record<string, unknown>,
): string {
  if (!params) return t(messageKey)
  const mapped: Record<string, unknown> = { ...params }
  // 数组参数按**当前语言**的习惯连接。
  //
  // ⚠️ 直接交给 i18next 会得到 "cluster_id,namespace" —— 英文缺空格，
  //	中文该用顿号。这种小地方最能让人一眼看出"翻译是机器凑的"。
  //	Intl.ListFormat 处理连接词（a, b and c），比手写 join 稳。
  for (const [k, v] of Object.entries(params)) {
    if (!Array.isArray(v)) continue
    const items = v.map((x) => String(x))
    mapped[k] = formatList(t, items)
  }
  for (const [name, ns] of Object.entries(MACHINE_PARAMS)) {
    const raw = params[name]
    if (typeof raw !== 'string' || raw === '') continue
    const key = `${ns}.${raw}`
    // 🔴 判"词表里有没有这个取值"要比**两种形式**的 key。
    //
    //	createI18n 配了 `parseMissingKeyHandler: (key) => key`（缺 key 时露出 key
    //	而不是空白，见那里的注释）。它有两个后果：
    //	  ① `defaultValue: ''` **不生效** —— handler 优先，照样返回 key；
    //	  ② handler 拿到的是**去掉命名空间**的 key，所以查 `common:kind.Bogus`
    //	     得到的是 `kind.Bogus`。
    //	只比完整 key 的话，"没找到"会被当成"找到了"，
    //	界面上真的显示出「不支持的资源类型 kind.Bogus」（实测两次）。
    //
    //	⚠️ 这个 bug 只在**词表里没有**该取值时才暴露：
    //	user / host / cluster 都在词表里，正例全绿。
    const bare = key.slice(key.indexOf(':') + 1)
    const hit = t(key)
    mapped[name] = hit === key || hit === bare ? raw : hit
  }
  return t(messageKey, mapped)
}

/**
 * 按**当前语言**的习惯把一串词连起来。
 *
 * ⚠️ 不要写 `list.join('、')`：那个顿号在英文界面上是错的，
 *	而它**不会报任何错**，只是看着像机器凑的翻译。实测全库有 5 处这么写。
 *	`Intl.ListFormat` 还会处理连接词（a, b and c）。
 *
 * ⚠️ 用**完整** key（带命名空间）取 BCP-47 标签。写成 `locale.bcp47`
 *	依赖 common 是默认命名空间 —— 某个产品换了默认 ns 就查不到，
 *	然后静默回退到英文逗号，中文界面上出现 "a, b, c"（单测抓到过）。
 */
export function formatList(
  t: (key: string, params?: Record<string, unknown>) => string,
  items: string[],
): string {
  try {
    const tag = t('common:locale.bcp47')
    return new Intl.ListFormat(tag.includes('locale.bcp47') ? undefined : tag, {
      style: 'narrow',
      type: 'conjunction',
    }).format(items)
  } catch {
    return items.join(', ')
  }
}

/** 后端给的一个摘要片段：一个可翻译 key + 它的插值参数。 */
export interface SummarySeg {
  key: string
  params?: Record<string, unknown>
}

/**
 * 把后端给的摘要片段渲染成一句话。
 *
 * 定时任务的摘要要落库（`task_run_logs.summary`），存的是后端拼好的**中文**，
 * 于是任务列表页和巡检页在英文界面下照样显示中文。摘要跟接口响应不一样，
 * 不能临时翻——写进去就是历史记录，90 天前那条也得能用当前语言读出来。
 *
 * 🔴 后端存的是**片段数组**而不是一个 key：摘要天然由若干可选片段组成
 * （"检查 N 个集群" + 可能有的"危险 M 项" + 可能有的"K 个未接入"…）。
 * 压成一个 key 就得在词条里写条件分支，而 **i18next 只有 {{var}} 插值和复数，
 * 没有条件段**——写 `{{#critical}}…{{/critical}}` 那种 Mustache 语法
 * 会原样显示在界面上。
 *
 * @param segs 后端给的片段。空/undefined 时返回空串，调用方自己退回中文原文。
 */
export function renderSummary(
  t: (key: string, params?: Record<string, unknown>) => string,
  segs: SummarySeg[] | null | undefined,
): string {
  if (!segs || segs.length === 0) return ''
  const parts: string[] = []
  for (const s of segs) {
    if (!s?.key) continue
    // 参数里的数组要按当前语言连接，不能靠 i18next 的默认 String(array)
    // ——那会给出 "a,b,c"，在中文界面上是错的（该是 "a、b、c"）。
    const params: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(s.params ?? {})) {
      if (Array.isArray(v)) {
        params[k] = formatList(t, v.map(String))
        continue
      }
      // 嵌套片段：`{key, params}` 形状的参数递归渲染。
      //
      // 为什么需要：任务摘要里要说清「哪个源、为什么失败」，而"为什么"本身
      // 是一条带自己 key 的可翻译句子（cron:failure.*）。后端原来在服务端
      // 把它拼成中文串再塞进参数 —— 于是**外层 key 翻译了、参数还是中文**，
      // 英文界面上会看到 "2/2 data source(s) failed. 本地预演（拉取列表失败：…）"。
      //
      // ⚠️ 缺词条的判定要一路向上传播：内层渲染不出来时整句作废，
      //    否则会把生 key 拼进摘要，比露中文更糟。
      if (v && typeof v === 'object' && typeof (v as { key?: unknown }).key === 'string') {
        const inner = v as { key: string; params?: Record<string, unknown> }
        const innerText = t(inner.key, inner.params ?? {})
        if (innerText === inner.key) return ''
        params[k] = innerText
        continue
      }
      params[k] = v
    }
    const text = t(s.key, params)
    // 🔴 词条缺失时 i18next 原样返回 key。把生 key 拼进摘要，
    //	读起来就是一句夹着 `cron:summary.diskWatchPushed` 的话——
    //	比显示中文原文更糟。整句作废，让调用方退回中文原文。
    //
    //	⚠️ 判据只能是 `text === s.key`。我第一版还加了 `text.includes(':')`
    //	当保险，而英文词条 `critical (≥{{pct}}%): {{count}}` 本身就带冒号——
    //	那个"保险"会让摘要**永远**为空，且看上去只是"后端没给 key"。
    if (text === s.key) return ''
    parts.push(text)
  }
  return parts.join(t('common:summary.segSeparator'))
}
