/**
 * 会话与权限判据。
 *
 * 🔴 **权限一律以后端 `/api/me` 返回的 `perms` 为准，
 * 禁止前端按 role 自行推导。** 前后端两套判据必然分叉，
 * 同一个人在两个页面看到的权限会不一致 —— 这个坑在别的产品上栽过三次。
 */
export interface Session {
  username: string
  displayName: string
  role: string
  authSource: string
  /** 后端算好的权限清单，前端只做 includes 判断 */
  perms: string[]
  /** 是否被限定了可见组织范围（数据范围，独立于角色） */
  scoped: boolean
}

export type Perm =
  | 'view' | 'export' | 'refresh' | 'plan.write'
  | 'org.write' | 'sync.trigger' | 'alert.write'
  | 'audit.view' | 'user.admin'

/**
 * can 判断当前会话是否有某项权限。
 *
 * session 为 undefined（还没拿到 / 拿失败）时一律返回 false ——
 * 「还不知道」必须按「没有」处理，否则会先渲染出按钮再消失，
 * 或者更糟：让用户点了才拿到 403。
 */
export function can(session: Session | undefined, perm: Perm): boolean {
  return !!session?.perms?.includes(perm)
}
