import type { NavGroup } from '@ops/ui'
import { ArrowLeftRight, KeyRound, Palette, Plug, RefreshCw, ScrollText, Server, ShieldCheck, UserCog, Users } from 'lucide-react'

/**
 * 本产品的菜单。
 *
 * 类型来自 `@ops/ui` —— **不要在这里重新声明一套**，
 * 那正是「第二份」的开始（外壳漂移就是这么发生的）。
 *
 * 🔴 `perm` 只决定菜单显不显示，**拦截全在后端**。
 * 写错码名不会报错，只会让菜单对所有非管理员静默消失；
 * `check-perm-codes.mjs` 会拿后端源码里的码表核对。
 * 这里用的码与后端 internal/auth/rbac.go 的 Perm 常量同名。
 */
export const NAV: NavGroup[] = [
  {
    key: 'recon',
    labelKey: 'opsversion:nav.groupRecon',
    items: [
      { key: 'recon', labelKey: 'opsversion:nav.recon', icon: ArrowLeftRight },
      { key: 'orgs', labelKey: 'opsversion:nav.orgs', icon: Server },
      { key: 'datasources', labelKey: 'opsversion:nav.datasources', icon: Plug },
      { key: 'sync', labelKey: 'opsversion:nav.sync', icon: RefreshCw },
    ],
  },
  {
    key: 'system',
    labelKey: 'opsversion:nav.groupSystem',
    items: [
      { key: 'users', labelKey: 'opsversion:nav.users', icon: Users, perm: 'user.admin' },
      { key: 'roles', labelKey: 'opsversion:nav.roles', icon: UserCog, perm: 'user.admin' },
      { key: 'sso', labelKey: 'opsversion:nav.sso', icon: ShieldCheck, perm: 'user.admin' },
      { key: 'branding', labelKey: 'opsversion:nav.branding', icon: Palette, perm: 'user.admin' },
      { key: 'tokens', labelKey: 'opsversion:nav.tokens', icon: KeyRound, perm: 'user.admin' },
      { key: 'audit', labelKey: 'opsversion:nav.audit', icon: ScrollText, perm: 'audit.view' },
    ],
  },
]
