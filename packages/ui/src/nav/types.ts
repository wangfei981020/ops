import type { LucideIcon } from 'lucide-react'

/**
 * 菜单数据的**共享类型**。
 *
 * 每个产品在自己的 `layouts/nav.ts` 里按这套类型声明菜单，
 * 外壳（AppShell）由 @ops/ui 统一提供 —— 产品不再各写一份侧栏。
 *
 * # 为什么必须共享
 *
 * 原来三个产品各写各的外壳：某个同类产品 351 行、另一个产品 394 行、另一个产品 87 行。
 * 结果是同一个产品线的三套菜单长得不一样，而且**能力也不一样**：
 * 另一个产品 抄过去之后漏掉了权限过滤，菜单不按权限收 —— 点进去 403。
 *
 * 光靠文档约定拦不住这件事：约定早就写在 CONVENTIONS §2.7.1，
 * 而复制粘贴出来的第二份代码从复制那一刻起就开始漂移，没人会回头对照文档。
 * 只有"根本没有第二份"才拦得住。
 */
export interface NavItem {
  key: string
  /** i18n key，命名空间 nav。外壳里用 t(item.labelKey) 取 */
  labelKey: string
  icon: LucideIcon
  /**
   * 路由路径。**外壳不用它**——导航一律走 onNavigate(item.key)。
   * 产品自己需要（生成路由表、深链）就填，不需要可以不填。
   */
  path?: string
  /**
   * 需要的权限码，必须与后端权限表里那一条**同名**。
   *
   * ⚠️ 前端这份只决定"显不显示"，**拦截全在后端**。
   * 少写一个只会让菜单多露一个入口，点进去照样 403；
   * 但如果把它当拦截手段，改一行前端就能绕过去。
   *
   * ⚠️ 写错码名不会报错，只会让菜单对所有非管理员静默消失。
   * `check-perm-codes.mjs` 会拿后端源码里的码表核对。
   */
  perm?: string
  /** 路线图上但还没做。置灰保留，不要不渲染——不渲染会让人以为功能不存在 */
  planned?: boolean
  /** 企业版功能，菜单上打 EE 角标 */
  feature?: string
  /**
   * 未授权时**完全不显示**这一项（而不是显示 EE 角标）。
   *
   * 两种表现是**商业决定**，不要按直觉统一：
   *   打角标   希望对方知道有这个功能并考虑购买（多数 EE 功能）
   *   不显示   不希望对方知道有这个功能
   *
   * ⚠️ 隐藏只是界面行为，**拦截必须同时在后端**。
   * 只藏菜单的话，知道路径的人直接输 hash 就进去了 ——
   * 而那种"藏起来了所以安全"的错觉比不藏更危险。
   */
  hideWhenLocked?: boolean
}

export interface NavGroup {
  key: string
  labelKey: string
  /** 分组图标。当前外壳不渲染它（分组只显示文字标题），产品可以填着备用 */
  icon?: LucideIcon
  items: NavItem[]
}

// 🔴 这里曾经有个 `footer?: boolean`，让「管理」类分组脱离主列表、
// 单独吊在侧栏最底下。已删除，不要加回来。
//
// 侧栏必须是**一列**：分组从上到下顺序排列，没有例外区。
// 把某一组挪到底部会产生两个问题——
//   1. 它和其它分组的距离取决于上面有多少组，位置不稳定，肌肉记忆失效
//   2. 用户按"从上往下扫一遍"找菜单时，底部那组在视觉上不属于同一个序列，会被跳过
// 需要弱化某组的存在感，就把它排到最后一个，而不是移出列表。

/**
 * 按权限过滤菜单。
 *
 * ⚠️ 过滤只在这一个地方做。菜单在前端硬编码、权限在后端另有一份，
 * 两边判据不一致的问题已经栽过三次。
 *
 * ⚠️ 整组都没权限时**连组标题一起去掉**：只剩一个标题的空分组
 * 看起来像功能加载失败。
 */
export function filterNav(
  nav: NavGroup[],
  can: (perm?: string) => boolean,
  /**
   * 功能是否已授权。不传 = 不按授权过滤（打角标的项照常显示）。
   *
   * ⚠️ 可选参数是刻意的：已有产品（某个同类产品）调的是两参数版本，
   * 改成必填会让它们在编译期就断，而它们并不需要这个能力。
   */
  hasFeature?: (feature: string) => boolean,
): NavGroup[] {
  return nav
    .map((g) => ({
      ...g,
      items: g.items.filter((i) => {
        if (!can(i.perm)) return false
        // ⚠️ hasFeature 没传时**不过滤**，而不是当成"未授权"。
        // 当成未授权的话，没接授权的产品会突然少掉一批菜单，
        // 而那看起来像是权限配错了。
        if (i.hideWhenLocked && i.feature && hasFeature && !hasFeature(i.feature)) return false
        return true
      }),
    }))
    .filter((g) => g.items.length > 0)
}
