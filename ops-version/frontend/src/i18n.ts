import { createI18n, type Resources } from '@ops/i18n'
import enCommon from '@ops/i18n/locales/en-US/common.json'
import enNav from '@ops/i18n/locales/en-US/nav.json'
import enOpsversion from '@ops/i18n/locales/en-US/opsversion.json'
import zhCommon from '@ops/i18n/locales/zh-CN/common.json'
import zhNav from '@ops/i18n/locales/zh-CN/nav.json'
import zhOpsversion from '@ops/i18n/locales/zh-CN/opsversion.json'

/**
 * 本产品的语言资源。
 *
 * ⚠️ 两边 key 必须完全一致，`check-i18n` 会比对 —— 缺一个 key 的表现是
 * 界面上直接显示 `opsversion.xxx.yyy` 这种原始路径，而不是报错。
 */
const resources: Resources = {
  // 🔴 `nav` 是 @ops/ui 的 AppShell 自己要用的（搜索框、侧栏折叠等）。
  // 不注册的话组件取不到 key 会**把 key 本身当文案渲染** ——
  // 界面上出现一个写着 "search.open" 的搜索框，不报错。
  // 守卫 check-shell-i18n 就是拦这个的。
  'zh-CN': { common: zhCommon, nav: zhNav, opsversion: zhOpsversion },
  'en-US': { common: enCommon, nav: enNav, opsversion: enOpsversion },
}

// 第二个参数是语言（默认自动探测），不是选项对象。
// defaultNS 固定为 'common'，所以本产品的 key 通过 useTranslation() 取。
export const i18n = createI18n(resources)
