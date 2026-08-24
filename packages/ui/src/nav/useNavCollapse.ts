import { useCallback, useEffect, useState } from 'react'

/**
 * 侧边栏分组的折叠状态。
 *
 * # 为什么需要折叠
 *
 * 菜单是按权限过滤的，所以"长"这件事对不同人完全不一样：
 * 成本分析角色只看到 4 项，而管理员看到 40+ 项。
 * 40 项一次全铺出来，侧栏必然要滚动 —— 而**滚动的侧栏等于没有导航**：
 * 你得先滚动去找，才谈得上点。
 *
 * # 为什么要记住状态
 *
 * 每次进来都重置成默认展开，等于每次都要重新收一遍。
 * 存 localStorage，跨会话保持。
 *
 * # 🔴 当前分组不做任何"强制展开"
 *
 * 这里曾经写成：
 *
 *	isOpen = (g) => g === activeGroupKey || !collapsed.has(g)
 *
 * 意图是：用户收起了某组后又从别处跳进该组的页面（面包屑、命令面板、深链），
 * 侧栏里当前页所在的组是收起的，高亮项看不见，人会以为自己不在菜单里的任何位置。
 *
 * 但这条规则让**当前页所在的组永远收不起来**——点折叠按钮没有任何反应，
 * 是个看起来能点、实际不工作的死控件（用户在「节点」页反馈「集群」收不起来）。
 *
 * 而它想解决的问题**本来就已经解决了**：AppShell 在组收起且含当前页时会渲染
 * 一个定位圆点（`!open && hasActive`），顶部还有「集群 / 节点」的面包屑。
 * 位置信息一直都在，不需要靠强行展开来传达。
 *
 * ⚠️ 所以不要再加回任何形式的"当前组自动展开"，包括"只展开一次"那种：
 * 折叠状态存在 localStorage 里，任何自动展开都会在刷新后把用户的选择顶掉，
 * 表现成"我收起了它，刷新又回来了"。
 */

const KEY = 'ops.nav.collapsed'

function read(): Set<string> {
  try {
    const raw = localStorage.getItem(KEY)
    return new Set(raw ? (JSON.parse(raw) as string[]) : [])
  } catch {
    // 隐私模式 / 存储被禁：全部展开，功能照常，只是不记忆
    return new Set()
  }
}

export interface NavCollapse {
  /** 这一组当前是不是展开的。只看用户的选择，没有任何隐藏规则 */
  isOpen: (groupKey: string) => boolean
  toggle: (groupKey: string) => void
  /** 全部展开 / 全部收起，给"一眼看全"和"清爽模式"两种用法 */
  setAll: (collapsed: boolean, allKeys: string[]) => void
}

// 不再需要知道"当前在哪一组"——折叠状态完全由用户的选择决定。
// 参数是刻意去掉的：留着它就还会有人想"顺手"再加回自动展开。
export function useNavCollapse(): NavCollapse {
  const [collapsed, setCollapsed] = useState<Set<string>>(read)

  useEffect(() => {
    try {
      localStorage.setItem(KEY, JSON.stringify([...collapsed]))
    } catch {
      /* 存不下就算了，不影响使用 */
    }
  }, [collapsed])

  const isOpen = useCallback((groupKey: string) => !collapsed.has(groupKey), [collapsed])

  const toggle = useCallback((groupKey: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(groupKey)) next.delete(groupKey)
      else next.add(groupKey)
      return next
    })
  }, [])

  const setAll = useCallback((c: boolean, allKeys: string[]) => {
    setCollapsed(c ? new Set(allKeys) : new Set())
  }, [])

  return { isOpen, toggle, setAll }
}

/**
 * 侧边栏整体收起成图标条。
 *
 * 看宽表格（Pod、主机）时能多出 200 多像素。与分组折叠是两件事：
 * 折叠是"我不关心这几组"，整体收起是"我现在要看内容，不看导航"。
 */
const RAIL_KEY = 'ops.nav.rail'

/**
 * 视口档位。断点表见 CONVENTIONS §2.7.9（某个同类产品 v0.82.0 实测定出来的）：
 *
 *   < 768px     抽屉  —— 216px 侧栏在 390px 视口下吃掉 55% 宽度，那一屏没法用
 *   768~1024px  图标条 —— 已无溢出，但侧栏仍占 28%
 *   >= 1024px   完整  —— 听用户自己的偏好
 *
 * 🔴 目标不是「别溢出」而是「折叠侧栏」：把最小内容宽度从 480 压到 390，
 * 只是把「被截断」换成「极窄到没法用」。
 */
export type NavViewport = 'drawer' | 'rail' | 'full'

/** 只读视口档位。SSR / 无 matchMedia 环境一律按 full，不猜。 */
export function useNavViewport(): NavViewport {
  const [vp, setVp] = useState<NavViewport>(() => readViewport())
  useEffect(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return
    const mqs = [window.matchMedia('(max-width: 767.98px)'), window.matchMedia('(max-width: 1023.98px)')]
    const onChange = () => setVp(readViewport())
    for (const mq of mqs) mq.addEventListener('change', onChange)
    // 挂载后再读一次：首帧的 useState 初值可能来自 SSR 或水合前
    onChange()
    return () => {
      for (const mq of mqs) mq.removeEventListener('change', onChange)
    }
  }, [])
  return vp
}

function readViewport(): NavViewport {
  if (typeof window === 'undefined' || !window.matchMedia) return 'full'
  if (window.matchMedia('(max-width: 767.98px)').matches) return 'drawer'
  if (window.matchMedia('(max-width: 1023.98px)').matches) return 'rail'
  return 'full'
}

export function useNavRail(): [boolean, () => void] {
  const [rail, setRail] = useState(() => {
    try {
      return localStorage.getItem(RAIL_KEY) === '1'
    } catch {
      return false
    }
  })
  const toggle = useCallback(() => {
    setRail((v) => {
      try {
        localStorage.setItem(RAIL_KEY, v ? '0' : '1')
      } catch {
        /* 同上 */
      }
      return !v
    })
  }, [])
  return [rail, toggle]
}
