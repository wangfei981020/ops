import {
  type Density,
  type ResolvedTheme,
  getDensity,
  getThemeMode,
  onThemeChange,
  resolveTheme,
  setDensity,
  setThemeMode,
} from '@ops/design'
import { Check, Monitor, Moon, Rows3, Sun } from 'lucide-react'
import { type ReactNode, useEffect, useState } from 'react'
import { cn } from '../lib/cn.js'
import { Popover } from './Popover.js'

/**
 * 主题 / 语言 / 密度控件。
 *
 * # 为什么文案全走 props
 *
 * 本包**不依赖 @ops/i18n**，与 EmptyState / ErrorState 一致 ——
 * 让 `packages/ui → packages/design` 这条依赖严格成立（CONVENTIONS §1），
 * 也让组件不绑死在某一个 i18n 库上。
 * 每个产品写十来行的薄封装，把自己的 t() 结果传进来。
 *
 * # 为什么主题和语言是直接 toggle，密度在弹层里
 *
 * 主题和语言只有两三个值，**点一下即切比"点开弹层再选"快一倍**，
 * 而且图标本身就表达了当前态。密度是"设一次不再碰"的偏好，
 * 放进常驻工具栏只会和高频操作抢位置。
 */

const iconButton = (active?: boolean) =>
  cn(
    'inline-flex size-8 cursor-pointer items-center justify-center rounded-[var(--radius)]',
    'text-muted-foreground transition-colors duration-150',
    'hover:bg-secondary hover:text-foreground',
    active && 'bg-secondary text-foreground',
  )

/** useResolvedTheme 当前实际生效的明暗（system 会被解析成具体值）。 */
function useResolvedTheme(): ResolvedTheme {
  const [theme, setTheme] = useState<ResolvedTheme>(() => resolveTheme(getThemeMode()))
  useEffect(() => onThemeChange(() => setTheme(resolveTheme(getThemeMode()))), [])
  return theme
}

export interface ThemeToggleProps {
  /** 无障碍标签，随当前态变化，如「切换到深色」。 */
  label: string
}

/**
 * 一键切明暗。
 *
 * # 为什么不在这里循环三态
 *
 * 三态（system / light / dark）用一个按钮轮转，点下去之前没人能预测会到哪个态。
 * 所以这里只做「切到相反的那个」：当前解析成浅色就切深色，反之亦然 ——
 * 无论原本是不是 system，结果都可预测。
 * 「跟随系统」放在偏好弹层里，那是个设置，不是高频开关。
 */
export function ThemeToggle({ label }: ThemeToggleProps) {
  const theme = useResolvedTheme()
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      className={iconButton()}
      onClick={() => setThemeMode(theme === 'dark' ? 'light' : 'dark')}
    >
      {/* 显示**当前**是什么，不是点了会变成什么 ——
          图标当状态指示比当动作预告更容易读懂 */}
      {theme === 'dark' ? <Moon className="size-4" /> : <Sun className="size-4" />}
    </button>
  )
}

export interface LocaleToggleProps {
  /** 当前语言的短标识，直接显示在按钮上，如 `中` / `EN`。 */
  current: string
  label: string
  onToggle: () => void
  /**
   * 当前语言的**已知局限**。有值时按钮上出现一个小圆点，hover 能读到这句话。
   *
   * 🔴 为什么需要它：某个同类产品 的后端会直接产出成句的诊断说明
   *	（"尚未接入告警系统，请到…添加一个接入点"），那些句子目前只有中文。
   *	英文界面下框架是英文、诊断说明是中文 —— 这是**架构选择的后果**，
   *	不是漏翻了几句。
   *
   *	不说出来的话，英文用户会把它当成 bug 反复报；说出来它就是一个已知的中间态。
   *	⚠️ 这不能代替真正的修复，只是让局限可见。
   */
  hint?: string
}

/**
 * 一键切语言。
 *
 * 用**文字**而不是地球图标：地球只说明"这里能改语言"，
 * 说不出现在是哪一种；而两个字符既是状态又是入口。
 */
export function LocaleToggle({ current, label, onToggle, hint }: LocaleToggleProps) {
  return (
    <button
      type="button"
      // hint 接在 label 后面而不是替换它：按钮首先得说清自己是干什么的
      title={hint ? `${label}\n\n${hint}` : label}
      aria-label={hint ? `${label} — ${hint}` : label}
      className={cn(iconButton(), 'relative text-[12px] font-medium')}
      onClick={onToggle}
    >
      {current}
      {/* 小圆点：不喧宾夺主，但让人知道这里有话说。
          用 warning 而不是 danger —— 这是已知局限，不是故障 */}
      {hint ? (
        <span
          aria-hidden
          className="absolute top-0.5 right-0.5 size-1.5 rounded-full bg-warning"
        />
      ) : null}
    </button>
  )
}

export interface PreferencesMenuProps {
  labels: {
    /** 触发按钮的无障碍标签 */
    preferences: string
    theme: string
    themeSystem: string
    themeLight: string
    themeDark: string
    density: string
    densityCompact: string
    densityDefault: string
    densityComfortable: string
  }
  /** 追加在弹层底部的自定义内容，如时区、版本号。 */
  children?: ReactNode
}

/**
 * 偏好弹层：放"设一次不再碰"的东西。
 *
 * 主题在这里仍然出现，是为了「跟随系统」——那一档在顶栏的两态开关里表达不了。
 */
export function PreferencesMenu({ labels, children }: PreferencesMenuProps) {
  const [mode, setMode] = useState(() => getThemeMode())
  const [density, setDensityState] = useState<Density>(() => getDensity())

  // ⚠️ 必须订阅：主题有**两个**入口（顶栏的 ThemeToggle 和这里）。
  // 只在挂载时读一次的话，用顶栏切了深色之后再打开这个弹层，
  // 上面还打勾在"跟随系统"——两个控件对同一个设置各说各话，
  // 而且不报错，只是显示错。实际撞到过。
  //
  // setThemeMode 每次都会 notify，所以这里能捕获全部变更，
  // 包括"解析结果没变但模式变了"（如 dark → system 而系统正好是深色）。
  useEffect(() => onThemeChange(() => setMode(getThemeMode())), [])

  return (
    <Popover
      align="end"
      panelClassName="w-[220px] p-1.5"
      trigger={(p) => (
        <button
          type="button"
          {...p}
          title={labels.preferences}
          aria-label={labels.preferences}
          className={iconButton(p['aria-expanded'])}
        >
          <Rows3 className="size-4" />
        </button>
      )}
    >
      <Group icon={<Sun />} label={labels.theme}>
        {(
          [
            ['system', labels.themeSystem, <Monitor key="s" />],
            ['light', labels.themeLight, <Sun key="l" />],
            ['dark', labels.themeDark, <Moon key="d" />],
          ] as const
        ).map(([value, text, icon]) => (
          <Row
            key={value}
            active={mode === value}
            icon={icon}
            label={text}
            onClick={() => {
              setMode(value)
              setThemeMode(value)
            }}
          />
        ))}
      </Group>

      <Group icon={<Rows3 />} label={labels.density}>
        {(
          [
            ['compact', labels.densityCompact],
            ['default', labels.densityDefault],
            ['comfortable', labels.densityComfortable],
          ] as const
        ).map(([value, text]) => (
          <Row
            key={value}
            active={density === value}
            label={text}
            onClick={() => {
              setDensityState(value)
              setDensity(value)
            }}
          />
        ))}
      </Group>

      {children}
    </Popover>
  )
}

function Group({ icon, label, children }: { icon: ReactNode; label: string; children: ReactNode }) {
  return (
    <div className="border-b border-border py-1 last:border-b-0">
      <div className="flex items-center gap-2 px-2.5 pt-1 pb-1.5 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        <span className="[&>svg]:size-3">{icon}</span>
        {label}
      </div>
      {children}
    </div>
  )
}

function Row({
  active,
  icon,
  label,
  onClick,
}: {
  active: boolean
  icon?: ReactNode
  label: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      // 用 √ 标记当前项而不是整行高亮：弹层里同时有两组选项，
      // 两块高亮底会让面板看起来很花，而勾选标记扫一眼就知道每组选的是哪个。
      className={cn(
        'flex w-full cursor-pointer items-center gap-2 rounded-[var(--radius-sm)] px-2.5 py-1.5',
        'text-left text-[13px] transition-colors duration-150 hover:bg-secondary',
        active ? 'text-foreground' : 'text-muted-foreground',
      )}
    >
      {icon ? <span className="[&>svg]:size-3.5">{icon}</span> : null}
      <span className="flex-1">{label}</span>
      {active ? <Check className="size-3.5 text-brand" /> : null}
    </button>
  )
}
