import { useTranslation } from '@ops/i18n'
import { useAppTitle, useBranding, useFavicon } from '../lib/branding.js'
import { AppShell as SharedShell } from '@ops/ui'
import type { ReactNode } from 'react'
import { type Session, type Perm, can } from '../lib/session.js'
import { NAV } from './nav.js'

/**
 * 本产品的外壳。**只做三件事**：接上自己的菜单、权限判据、品牌信息。
 *
 * ⚠️ 侧栏本体在 `@ops/ui` 的 AppShell 里，不要在这里重新实现。
 * 原来三个产品各写各的（cmdb 351 行 / alert 394 行 / sso 87 行），
 * 结果不只是长得不一样 —— 另一个产品 那份抄过去时漏掉了权限过滤，
 * 菜单不按权限收，用户点进去才拿到 403。
 * 守卫：tooling/scripts/check-shell-consistency.mjs
 */
export interface AppShellProps {
  activeKey: string
  onNavigate: (key: string) => void
  breadcrumb: ReactNode
  toolbar?: ReactNode
  children: ReactNode
  /** 当前会话。`undefined` = 还没拿到（加载中或失败） */
  session: Session | undefined
  /**
   * 权限是否还在加载。
   * 🔴 与「加载失败」必须分开：都当成"没权限"的话，
   * 加载中会先渲染出一个空菜单再突然长出来；而加载失败时反而该显式提示，
   * 不能让人以为自己就是没权限。
   */
  permsPending: boolean
}

export function AppShell(p: AppShellProps) {
  const { t } = useTranslation()
  const brand = useBranding()
  // favicon 与标题都只能运行期注入 —— 客户自定义的图标构建期还不知道
  useFavicon(brand.favicon_data)
  useAppTitle(brand.app_name, t('opsversion:brand'))
  return (
    <SharedShell
      nav={NAV}
      // 权限判据只有这一处，且只认后端返回的 perms
      can={(perm?: string) => (perm ? can(p.session, perm as Perm) : true)}
      // 自定义为空时回落产品内置 —— 白标是可选的，不配也要能正常用
      brand={brand.app_name.trim() || t('opsversion:brand')}
      logo={brand.logo_data || undefined}
      version={__APP_VERSION__}
      activeKey={p.activeKey}
      onNavigate={p.onNavigate}
      breadcrumb={p.breadcrumb}
      toolbar={p.toolbar}
      permsPending={p.permsPending}
      // 按视口自动折叠侧栏（CONVENTIONS §2.7.9）。
      // 共享外壳里默认关闭 —— 已上线的产品要各自验证过窄屏再开，
      // 不能被一次共享包升级顺带改掉布局。本产品是第一个开的。
      responsive
      t={t}
    >
      {p.children}
    </SharedShell>
  )
}
