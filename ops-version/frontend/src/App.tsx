import { RouterProvider } from '@tanstack/react-router'
import { router } from './router.js'

/**
 * 应用入口。
 *
 * 路由本体在 router.tsx —— 这里只做接线。
 *
 * ⚠️ 之前这里是 `useState` 切页：没有 URL、刷新回首页、页面发不给同事、
 * 浏览器前进后退失效。切到真路由后，**深链会绕过菜单过滤**，
 * 所以 router.tsx 里必须有 PermGate（见那里的说明）。
 */
export function App() {
  return <RouterProvider router={router} />
}
