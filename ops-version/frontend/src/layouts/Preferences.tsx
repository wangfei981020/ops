import {
  DEFAULT_LOCALE,
  LOCALES,
  LOCALE_LABELS,
  type Locale,
  setLocale,
  useTranslation,
} from '@ops/i18n'
import { LocaleToggle, PreferencesMenu, ThemeToggle } from '@ops/ui'

/**
 * 把 @ops/ui 的无文案控件接上本产品的语言包。
 *
 * ui 包刻意不依赖 i18n（与 EmptyState 一致），所以每个产品都要这么一层。
 * 十来行的代价，换来 ui 不绑死在某个 i18n 库上、依赖方向也保持单向。
 *
 * 🔴 这三个控件在 @ops/ui 里躺了很久，本产品一直没接 ——
 * 底层能力（@ops/design 的 setThemeMode、@ops/i18n 的两份完整语言包）
 * 全都是好的，缺的只有这个开关本身。
 *
 * ⚠️ 「共享包有能力、产品没入口」和「后端有接口、前端没入口」是同一个形状，
 * 但 check-read-coverage 只查后者 —— 它够不着这一层。
 */
export function Preferences() {
  const { t, i18n } = useTranslation()
  const locale = i18n.language as Locale

  // 两种语言时就是"切到另一种"；将来多于两种，按顺序轮转。
  //
  // ⚠️ 用 ?? DEFAULT_LOCALE 兜底而不是断言非空：i18n.language 可能是
  // 浏览器给的 'zh'、'en-GB' 这类我们没登记的值，indexOf 会返回 -1。
  // 断言的话运行时会拿到 undefined，界面上表现为语言按钮点了没反应。
  const idx = LOCALES.indexOf(locale)
  const next: Locale = LOCALES[(idx + 1) % LOCALES.length] ?? DEFAULT_LOCALE

  return (
    <div className="flex items-center gap-0.5">
      <ThemeToggle label={t('theme.label')} />
      <LocaleToggle
        current={next === 'en-US' ? '中' : 'EN'}
        label={`${t('locale.label')}：${LOCALE_LABELS[next]}`}
        onToggle={() => void setLocale(i18n, next)}
      />
      <PreferencesMenu
        labels={{
          preferences: t('preferences'),
          theme: t('theme.label'),
          themeSystem: t('theme.system'),
          themeLight: t('theme.light'),
          themeDark: t('theme.dark'),
          density: t('density.label'),
          densityCompact: t('density.compact'),
          densityDefault: t('density.default'),
          densityComfortable: t('density.comfortable'),
        }}
      />
    </div>
  )
}
