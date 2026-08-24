import { ChevronDown, PanelLeftClose, PanelLeftOpen, Search } from 'lucide-react'
import { type ReactNode, useCallback, useEffect, useMemo, useState } from 'react'
import { cn } from '../lib/cn.js'
import { Skeleton } from '../state/Skeleton.js'
import { CommandPalette, type CommandItem } from './CommandPalette.js'
import { type NavGroup, filterNav } from './types.js'
import { type NavViewport, useNavCollapse, useNavRail, useNavViewport } from './useNavCollapse.js'

/**
 * 全产品共用的应用外壳：侧栏 + 分组折叠 + 图标条 + ⌘K 命令面板 + 顶栏。
 *
 * # 产品要做的只有三件事
 *
 *   1. 按 NavGroup[] 声明自己的菜单（各自的 layouts/nav.ts）
 *   2. 传一个 can(perm) 判据
 *   3. 传品牌名与版本号
 *
 * 侧栏本身**不要再抄一份**。原来三个产品各写各的，
 * 结果不只是长得不一样 —— 另一个产品 那份抄过去时漏了权限过滤，
 * 菜单不按权限收，用户点进去才拿到 403。
 * 这类漂移靠文档约定拦不住（约定早就写着），只有"没有第二份"才拦得住。
 */
export interface AppShellProps {
  /** 菜单数据（未过滤）。过滤在外壳内部统一做，见 filterNav */
  nav: NavGroup[]
  /**
   * 权限判据。`perm` 为空表示该项无需权限。
   *
   * ⚠️ 由产品传入而不是内置：各产品的会话结构不同，
   * 但"怎么过滤"必须是同一套，所以判据外置、过滤内置。
   */
  can: (perm?: string) => boolean
  /** 品牌名，显示在 logo 右侧 */
  brand: string
  /**
   * 自定义 logo（data URI 或 URL）。
   *
   * 🔴 **可选**：不传就用内置的那个图标。
   * 加成必填会逼着四个产品同时改，而其中三个根本没有白标需求 ——
   * 共用组件加参数时，"不传等于保持原样"是唯一不会连累别人的做法。
   *
   * ⚠️ 这里不做尺寸裁剪：调用方传什么就渲染什么。
   * 在组件里强行 object-cover 会把宽幅 logo 裁掉两头，
   * 而"我传的图和显示的不一样"最难查 —— 上传时限制比渲染时裁剪更好。
   */
  logo?: string
  /** 版本号。⚠️ 本身已带 v（v0.7.1），不要再补一个 v，否则是 vv0.7.1 */
  version: string
  activeKey: string
  onNavigate: (key: string) => void
  breadcrumb: ReactNode
  toolbar?: ReactNode
  children: ReactNode
  /**
   * 权限是否还在加载。
   * ⚠️ 与"加载失败"必须分开：加载中显示骨架，失败由内容区的 AsyncBoundary 报，
   * 侧栏不是报错的地方 —— 否则一次接口抖动会让左右两边同时报同一个错。
   */
  permsPending?: boolean
  /** 全局横幅（授权过期等）。挂在内容区**上方**、菜单右侧 */
  banner?: ReactNode
  /**
   * 菜单项上的数字角标，key → 数量。
   *
   * 分组收起时会显示该组内各项之和 —— 否则"有 3 条待处理"这个信息
   * 会随着折叠一起消失，而人恰恰是靠它决定要不要展开。
   * ⚠️ 0 不显示：一个常驻的「0」会让人对角标脱敏，真有数字时也不看了。
   */
  badges?: Record<string, number>
  /**
   * 翻译函数。
   *
   * ⚠️ 由产品传入，@ops/ui **不依赖 @ops/i18n** —— 这是本包既有的架构：
   * 展示层只收字符串，不关心它从哪来（CommandPalette 也是这么做的）。
   * 反过来让 ui 依赖 i18n，会让任何想用这套组件的地方
   * 都被迫接受一整套语言包加载机制。
   */
  t: (key: string, params?: Record<string, unknown>) => string

  /**
   * 按视口自动折叠侧栏（CONVENTIONS §2.7.9 的断点表）。
   *
   * ⚠️ **默认 false，即保持原有行为不变**。
   * 规范说「这是外壳的缺陷，修一处全好」，本该对所有产品默认开启；
   * 做成 opt-in 是因为已上线的产品需要各自验证过窄屏表现再开
   * （页头按钮换行、顶栏用户区收起这两条要产品自己配合），
   * 而不是被一次共享包升级顺带改掉布局。
   *
   * 开启后：<768 汉堡+抽屉浮层（侧栏不占位）、768~1024 强制图标条、
   * >=1024 听用户自己的收起偏好。
   */
  responsive?: boolean
}

export function AppShell({
  nav: rawNav,
  can,
  brand,
  logo,
  version,
  activeKey,
  onNavigate,
  breadcrumb,
  toolbar,
  children,
  permsPending = false,
  banner,
  badges,
  t,
  responsive = false,
}: AppShellProps) {
  const nav = filterNav(rawNav, can)

  // 当前页所在的分组。用于"当前组无条件展开"——
  // 从命令面板/深链跳进一个被收起的组时，高亮项看不见，
  // 人会以为自己不在菜单里的任何位置
  const collapse = useNavCollapse()
  const [userRail, toggleRail] = useNavRail()
  const viewport = useNavViewport()
  const [drawerOpen, setDrawerOpen] = useState(false)

  // 未开启 responsive 时 viewport 一律不参与，行为与从前完全一致
  const mode: NavViewport = responsive ? viewport : 'full'
  // 中等屏强制图标条；宽屏听用户的；抽屉模式下浮层内是完整菜单，不收成图标
  const rail = mode === 'rail' ? true : mode === 'drawer' ? false : userRail

  // 视口变宽时把抽屉收掉，否则从窄屏拉宽会留下一个悬空的浮层和遮罩
  useEffect(() => {
    if (mode !== 'drawer') setDrawerOpen(false)
  }, [mode])

  const [paletteOpen, setPaletteOpen] = useState(false)

  // 抽屉模式下跳转后必须把浮层收掉 —— 否则点完菜单，
  // 目标页面被浮层和遮罩整个盖住，看起来像"点了没反应"
  const handleNavigate = useCallback(
    (key: string) => {
      setDrawerOpen(false)
      onNavigate(key)
    },
    [onNavigate],
  )

  // ⌘K / Ctrl+K。挂在 window 上而不是某个输入框上：
  // 它要在任何位置都能用，这正是它相对菜单的价值
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // 面板里的条目：**只放当前用户看得到的**。
  // 放全量的话，没权限的人能从这里搜到并跳进去，然后拿到 403 ——
  // 等于把菜单过滤的意义抵消掉了
  const commandItems = useMemo<CommandItem[]>(
    () =>
      nav.flatMap((g) =>
        g.items
          .filter((i) => !i.planned)
          .map((i) => ({
            key: i.key,
            label: t(i.labelKey),
            group: t(g.labelKey),
            icon: i.icon,
          })),
      ),
    [nav, t],
  )

  return (
    <div className="flex h-screen bg-background text-foreground">
      {/* 抽屉模式的遮罩。点它关闭 —— 浮层没有出路时人只能刷新页面 */}
      {mode === 'drawer' && drawerOpen ? (
        <button
          type="button"
          aria-label={t('nav:sidebar.collapse')}
          onClick={() => setDrawerOpen(false)}
          className="fixed inset-0 z-40 cursor-default bg-overlay"
        />
      ) : null}

      <aside
        className={cn(
          'flex shrink-0 flex-col gap-0.5 overflow-y-auto border-r border-border bg-surface p-2.5',
          'transition-[width] duration-150',
          rail ? 'w-[52px]' : 'w-[216px]',
          // 🔴 抽屉模式下侧栏**不占据文档流**（fixed），否则 216px 依旧吃掉
          //    390px 视口的 55%，那正是要解决的问题本身。
          mode === 'drawer'
            ? cn('fixed inset-y-0 left-0 z-50 shadow-[var(--ops-shadow-modal)]', !drawerOpen && 'hidden')
            : null,
        )}
      >
        <div className="flex items-center gap-2.5 px-2 pt-1 pb-4">
          {/* 🔴 有自定义 logo 时**不画品牌渐变底**，而且要给它更大的方框。
              两点都栽过：
                ① 渐变底是**默认图标**的底（描边图标需要一个色块衬着），
                   而用户上传的 logo 自己带背景和边缘 —— 叠在渐变上，
                   两层颜色混在一起，看着就是"一个青色方块里有个看不清的东西"。
                ② 24px（size-6）对一个图标够用，对一张 logo 太小 ——
                   稍有细节就糊成一团。有 logo 时放到 32px。
              ⚠️ 不给 logo 加 rounded：圆角会把方形 logo 的四角切掉，
                 而那四个角常常正是它的一部分。 */}
          <span
            className={cn(
              'grid place-items-center',
              logo
                ? 'size-8'
                : 'size-6 rounded-[var(--radius-sm)] bg-gradient-to-br from-primary-hover to-primary-active',
            )}
          >
            {/* 描边跟着 primary-foreground 走：品牌色换成亮黄时，
                白色描边会在黄底上消失，而 primary-foreground 会自动翻成深色。 */}
            {logo ? (
              // alt 用品牌名：logo 加载失败时至少还能看到是哪个系统，
              // 而不是一个破图标
              <img src={logo} alt={brand} className="size-full object-contain" />
            ) : (
              <svg
                viewBox="0 0 24 24"
                className="size-3.5 fill-none stroke-primary-foreground stroke-[1.75]"
                aria-hidden="true"
              >
                <path d="M12 2 3 7v10l9 5 9-5V7z" />
                <path d="M12 22V12" />
                <path d="m3 7 9 5 9-5" />
              </svg>
            )}
          </span>
          {!rail ? <span className="text-sm font-semibold tracking-tight">{brand}</span> : null}
        </div>

        {/* 搜索入口。做成一个看得见的按钮而不是只留快捷键：
            ⌘K 只有知道的人会用，而不知道的人恰恰是最需要它的那批 */}
        {!rail ? (
          <button
            type="button"
            onClick={() => setPaletteOpen(true)}
            className="mb-1 flex cursor-pointer items-center gap-2 rounded-[var(--radius)] border border-border px-2 py-1.5 text-left text-xs text-muted-foreground hover:bg-secondary"
          >
            <Search className="size-3.5 shrink-0" aria-hidden="true" />
            <span className="truncate">{t('nav:search.open')}</span>
            <kbd className="ml-auto shrink-0 rounded border border-border px-1 font-mono text-[10px]">
              ⌘K
            </kbd>
          </button>
        ) : (
          <button
            type="button"
            onClick={() => setPaletteOpen(true)}
            title={t('nav:search.open')}
            aria-label={t('nav:search.open')}
            className="mb-1 grid cursor-pointer place-items-center rounded-[var(--radius)] py-1.5 text-muted-foreground hover:bg-secondary"
          >
            <Search className="size-3.5" aria-hidden="true" />
          </button>
        )}

        {/*
          权限没到手时显示骨架，**不显示菜单**。
          先渲染全量菜单再删掉没权限的那些，会有一瞬间露出用户不该看到的入口，
          而那一瞬间足够让人点进去（然后拿到 403，以为系统坏了）。

          ⚠️ 加载失败时这里同样是空的，但侧边栏不是报错的地方 ——
          真正的错误态由内容区的 AsyncBoundary 负责，
          否则一次接口抖动会让左右两边同时报同一个错。
        */}
        {permsPending ? (
          <div className="flex flex-col gap-2 px-2 pt-3.5">
            {[80, 60, 70, 55, 75, 50].map((w) => (
              <Skeleton key={w} className="h-4" style={{ width: `${w}%` }} />
            ))}
          </div>
        ) : (
          nav.map((group) => (
            <NavGroupBlock
              key={group.key}
              group={group}
              activeKey={activeKey}
              onNavigate={handleNavigate}
              t={t}
              rail={rail}
              open={collapse.isOpen(group.key)}
              onToggle={() => collapse.toggle(group.key)}
              badges={badges}
            />
          ))
        )}

        {/* 版本号区。🔴 这里曾经还渲染一批 footer 分组（「管理」被单独吊在底下），
            已删除——侧栏必须是一列，见 types.ts 里 NavGroup 上的说明。 */}
        <div className="mt-auto border-t border-border pt-2.5">
          {/* ⚠️ 不要在这里补 "v"：版本号本身就带 v（v0.7.1），
              补出来是 "vv0.7.1"。看着只是难看，实际会让人在
              问"你跑的哪一版"时报出一个搜不到的字符串。 */}
          <div className="flex items-center gap-1 px-2 pt-2">
            {!rail ? (
              <p className="font-mono text-[10px] text-muted-foreground">{version}</p>
            ) : null}
            <button
              type="button"
              onClick={toggleRail}
              title={t(rail ? 'nav:sidebar.expand' : 'nav:sidebar.collapse')}
              aria-label={t(rail ? 'nav:sidebar.expand' : 'nav:sidebar.collapse')}
              className="ml-auto cursor-pointer rounded-[var(--radius)] p-1 text-muted-foreground hover:bg-secondary hover:text-foreground"
            >
              {rail ? (
                <PanelLeftOpen className="size-3.5" aria-hidden="true" />
              ) : (
                <PanelLeftClose className="size-3.5" aria-hidden="true" />
              )}
            </button>
          </div>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center gap-3 border-b border-border bg-surface px-5 py-2.5">
          {/* 汉堡：抽屉模式下这是唯一能打开导航的入口。
              侧栏已经 fixed 出文档流了，没有它人就彻底走不到别的页面。 */}
          {mode === 'drawer' ? (
            <button
              type="button"
              onClick={() => setDrawerOpen(true)}
              title={t('nav:sidebar.expand')}
              aria-label={t('nav:sidebar.expand')}
              className="-ml-1 grid cursor-pointer place-items-center rounded-[var(--radius)] p-1.5 text-muted-foreground hover:bg-secondary"
            >
              <svg viewBox="0 0 24 24" className="size-4 fill-none stroke-current stroke-2" aria-hidden="true">
                <path d="M3 6h18M3 12h18M3 18h18" />
              </svg>
            </button>
          ) : null}
          <div className="min-w-0 truncate text-sm text-muted-foreground">{breadcrumb}</div>
          <div className="ml-auto flex items-center gap-2">{toolbar}</div>
        </header>
        {/* 横幅在 header 之下、内容之上：它是"整个系统"的状态，
            放进内容区会随页面滚走，而这条信息恰恰要一直在 */}
        {banner}
        <main className="min-h-0 flex-1 overflow-auto">{children}</main>
      </div>

      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        items={commandItems}
        onSelect={handleNavigate}
        placeholder={t('nav:search.placeholder')}
        emptyLabel={t('nav:search.empty')}
        hintLabel={t('nav:search.hint')}
      />
    </div>
  )
}

function NavGroupBlock({
  group,
  activeKey,
  onNavigate,
  t,
  rail,
  open,
  onToggle,
  badges,
}: {
  group: NavGroup
  activeKey: string
  onNavigate: (key: string) => void
  t: (k: string) => string
  /** 侧栏收成图标条：不显示分组标题，也不能折叠（没地方点） */
  rail: boolean
  open: boolean
  onToggle: () => void
  badges?: Record<string, number>
}) {
  // 收起状态下仍要能看出"这一组里有当前页"，否则收起之后
  // 用户完全失去自己在哪儿的线索
  const activeItem = group.items.find((i) => i.key === activeKey)
  const hasActive = activeItem !== undefined
  // 收起时该组内数字之和。折叠不该让"有几条待处理"这个信息消失 ——
  // 人正是靠它决定要不要展开这一组
  const groupBadge = group.items.reduce((sum, i) => sum + (badges?.[i.key] ?? 0), 0)

  if (rail) {
    return (
      <div className="flex flex-col gap-0.5 border-t border-border/60 py-1.5 first:border-0">
        {group.items.map((item) => {
          const Icon = item.icon
          const active = item.key === activeKey
          return (
            <button
              key={item.key}
              type="button"
              disabled={item.planned}
              onClick={() => onNavigate(item.key)}
              // 图标条下**必须**有 title：只剩图标时，
              // 没有文字提示的导航等于让人靠猜
              title={t(item.labelKey)}
              aria-label={t(item.labelKey)}
              className={cn(
                'grid place-items-center rounded-[var(--radius)] py-1.5',
                active ? 'bg-brand-bg text-brand-text' : 'text-foreground/70 hover:bg-secondary',
                item.planned ? 'cursor-not-allowed opacity-40' : 'cursor-pointer',
              )}
            >
              <Icon className="size-4" aria-hidden="true" />
            </button>
          )
        })}
      </div>
    )
  }

  return (
    <div>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full cursor-pointer items-center gap-1 px-2 pt-3.5 pb-1.5 text-left text-[11px] font-medium tracking-wider text-muted-foreground uppercase hover:text-foreground"
      >
        <span className="truncate">{t(group.labelKey)}</span>
        {/* 收起且当前页在这一组里时，把**当前页名**接在组名后面。
            🔴 原来这里只有一个 6px 的圆点，而且 aria-hidden ——
            那是收起状态下唯一的"我在这儿"线索，可它既没有文字也读不出来。
            用户在 /k8s/nodes 上看到的是收起的 `KUBERNETES ● >`，
            侧栏上没有任何地方写着 Nodes。
            ⚠️ 不要改成"当前组自动展开"：那会让折叠按钮在当前组上点不动，
            是上一版真实收到过的反馈，见 useNavCollapse 的注释。 */}
        {!open && activeItem ? (
          <>
            <span className="shrink-0 text-muted-foreground/60" aria-hidden="true">
              ·
            </span>
            <span className="truncate text-brand-text normal-case">{t(activeItem.labelKey)}</span>
            <span className="size-1.5 shrink-0 rounded-full bg-brand" aria-hidden="true" />
          </>
        ) : null}
        {!open && groupBadge > 0 ? (
          <span className="rounded-full bg-brand-bg px-1.5 text-[10px] font-medium text-brand-text tabular-nums">
            {groupBadge}
          </span>
        ) : null}
        <ChevronDown
          className={cn(
            'ml-auto size-3 shrink-0 transition-transform duration-150',
            open ? '' : '-rotate-90',
          )}
          aria-hidden="true"
        />
      </button>
      {!open
        ? null
        : group.items.map((item) => {
            const Icon = item.icon
            const active = item.key === activeKey
            return (
              <button
                key={item.key}
                type="button"
                // planned 的菜单置灰但**保留在列表里**：
                // 直接不渲染会让人以为功能不存在，看得见才知道在路线图上。
                disabled={item.planned}
                onClick={() => onNavigate(item.key)}
                className={cn(
                  'flex w-full items-center gap-2.5 rounded-[var(--radius)] px-2 py-1.5 text-left text-[13px]',
                  'transition-colors duration-150',
                  active
                    ? 'bg-brand-bg font-medium text-brand-text'
                    : 'text-foreground/75 hover:bg-secondary hover:text-foreground',
                  item.planned && 'cursor-not-allowed opacity-40 hover:bg-transparent',
                  !item.planned && 'cursor-pointer',
                )}
              >
                <Icon className="size-3.5 shrink-0" aria-hidden="true" />
                <span className="truncate">{t(item.labelKey)}</span>
                {/* 0 不渲染：常驻的「0」会让人对角标脱敏 */}
                {badges?.[item.key] ? (
                  <span className="ml-auto rounded-full bg-brand-bg px-1.5 text-[10px] font-medium text-brand-text tabular-nums">
                    {badges[item.key]}
                  </span>
                ) : null}
                {item.feature ? (
                  <span className="ml-auto rounded-[var(--radius-sm)] bg-secondary px-1 text-[9px] tracking-wide text-muted-foreground uppercase">
                    EE
                  </span>
                ) : null}
              </button>
            )
          })}
    </div>
  )
}
