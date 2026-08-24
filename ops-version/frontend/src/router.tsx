import { useTranslation } from '@ops/i18n'
import { Badge, Button, ErrorBoundary, ErrorState, NoPermission } from '@ops/ui'
import {
  Outlet,
  createRootRoute,
  createRoute,
  createRouter,
  useNavigate,
  useRouterState,
} from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { AppShell } from './layouts/AppShell.js'
import { Preferences } from './layouts/Preferences.js'
import { NAV } from './layouts/nav.js'
import { api, toLoadError } from './lib/api.js'
import { type Perm, type Session, can } from './lib/session.js'
import { AuditPage } from './routes/audit/AuditPage.js'
import { LoginPage } from './routes/login/LoginPage.js'
import { OrgsPage } from './routes/orgs/OrgsPage.js'
import { ReconPage } from './routes/recon/ReconPage.js'
import { SyncPage } from './routes/sync/SyncPage.js'
import { TokensPage } from './routes/tokens/TokensPage.js'
import { BrandingPage } from './routes/branding/BrandingPage.js'
import { DatasourcesPage } from './routes/datasources/DatasourcesPage.js'
import { SsoPage } from './routes/sso/SsoPage.js'
import { RolesPage } from './routes/roles/RolesPage.js'
import { UsersPage } from './routes/users/UsersPage.js'

/** 后端 /api/me 的返回。字段名与后端 mePayload 一一对应 */
interface MeResp {
  /**
   * 🔴 登没登录看这个字段，**不看状态码**。
   *
   * /api/me 未登录时返回 200 + authenticated:false（它是探测，不是受保护资源）——
   * 401 那条路会把「没登录」和「网络抖了一下」混在一起，
   * 而且每次打开登录页都在 console 里留一条红色 error。
   */
  authenticated: boolean
  username: string
  display_name: string
  role: string
  auth_source: string
  perms: string[]
  scoped: boolean
}

/**
 * 菜单 key → URL。
 *
 * 🔴 单独一张表而不是拿 key 直接拼 `/${key}`：
 * 菜单 key 是给 AppShell 高亮用的内部标识，URL 是**对外契约**
 * （会被收藏、被分享、被写进文档）。两者绑死的话，
 * 哪天想把菜单 key 改得更清楚，所有人存的链接就全断了。
 */
const PATHS: Record<string, string> = {
  recon: '/',
  orgs: '/orgs',
  datasources: '/datasources',
  sync: '/sync',
  users: '/users',
  roles: '/roles',
  sso: '/sso',
  branding: '/branding',
  tokens: '/tokens',
  audit: '/audit',
}

/** URL → 菜单项（含权限码）。用于高亮和深链权限判断 */
function itemOf(pathname: string) {
  for (const g of NAV) {
    for (const it of g.items) {
      if (PATHS[it.key] === pathname) return it
    }
  }
  return undefined
}

const rootRoute = createRootRoute({ component: RootLayout })

function RootLayout() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (s) => s.location.pathname })

  const me = useQuery({
    queryKey: ['me'],
    queryFn: () => api<MeResp>('/api/me'),
    // 未登录会 401 —— 这是预期路径不是故障，不要重试
    retry: false,
    // 🔴 必须给 staleTime。同一个 queryKey 被 RootLayout 和页面组件各用一次，
    //    而默认 staleTime=0 意味着「第二个组件挂载时数据已经过期」——
    //    react-query 于是又打一遍。每次切页都翻倍。
    // ⚠️ 30 秒是权衡：会话信息几乎不变（改角色要重新登录才生效），
    //    但也不能设成 Infinity —— 退出登录后要能及时察觉。
    staleTime: 30_000,
  })

  // 🔴 三态**真的**分开：加载中 ≠ 未登录 ≠ 加载失败。
  //
  //    原来 isError 和「未登录」都跳登录页 —— 注释写着要分开，代码没做到。
  //    后端挂了的时候把人送到一个点了也没反应的登录页，
  //    比直接说「连不上服务端」更让人摸不着头脑。
  if (me.isPending) return null
  if (me.isError) {
    return (
      <div className="flex min-h-dvh items-center justify-center p-6">
        <div className="max-w-md rounded-lg border border-danger bg-danger-bg px-4 py-3 text-sm text-danger">
          <div className="font-semibold">{t('opsversion:boot.failTitle')}</div>
          <div className="mt-1 text-xs">{(me.error as Error)?.message}</div>
          <div className="mt-2">
            <Button onClick={() => me.refetch()}>{t('common:action.retry')}</Button>
          </div>
        </div>
      </div>
    )
  }
  if (!me.data.authenticated) {
    return <LoginPage onSignedIn={() => qc.invalidateQueries({ queryKey: ['me'] })} />
  }

  const session: Session = {
    username: me.data.username,
    displayName: me.data.display_name,
    role: me.data.role,
    authSource: me.data.auth_source,
    perms: me.data.perms ?? [],
    scoped: me.data.scoped,
  }

  const item = itemOf(pathname)
  const activeKey = item?.key ?? 'recon'

  /**
   * 退出登录。
   *
   * 🔴 **本地清理必须无条件执行**，不能挂在接口成功上。
   *
   * 原来是裸 `await api(...)`：`api()` 在非 2xx 时会 throw，
   * 于是清缓存和跳登录页两步全被跳过 —— 界面纹丝不动，用户看到的是「点了没反应」。
   * 实测过 `POST /api/auth/logout` 返回过 401，正好撞上这条路径。
   *
   * ⚠️ 即使接口一直正常，这个写法也只是"碰巧能用"：网络抖一下、会话刚好过期、
   * 后端重启，任何一种都会让退出变成死键。而**退出恰恰是出问题时的最后一根救命稻草**
   * （权限不对、身份不对时第一反应就是退出重登），它最不能依赖"一切正常"。
   */
  async function signOut() {
    try {
      await api('/api/auth/logout', { method: 'POST' })
    } catch {
      // 401 / 网络失败都不影响**本地**登出：
      // 服务端会话最多是没删掉，而把人留在登录态里是更糟的结果
    } finally {
      // 🔴 **整页跳转**，不靠 react-query 的状态联动。
      //
      // 原来是 `qc.clear()` + `invalidateQueries(['me'])`，指望 me 请求拿到 401、
      // 进而由 `me.isError` 切到登录页。实测**不成立**：
      // `clear()` 把 query 移除后，活跃的 observer 并不会自动重新请求，
      // 于是界面停在原地、右上角还是原来那个人 —— 与修复前的表现一模一样。
      // （这一层是修 时实测才发现的：档案给的"包 try/finally"
      //  只解决了异常吞掉的问题，没解决"清了缓存但界面不动"。）
      //
      // ⚠️ 退出是**安全动作**，最不能"部分生效"。整页重载让内存里的一切
      // （query 缓存、组件 state、别人的数据）全部归零，代价只是一次加载。
      qc.clear()
      window.location.assign('/')
    }
  }

  return (
    <AppShell
      activeKey={activeKey}
      onNavigate={(key) => navigate({ to: PATHS[key] ?? '/' })}
      breadcrumb={t(`opsversion:nav.${activeKey}`)}
      toolbar={
        <div className="flex items-center gap-2">
          {/* 🔴 窄屏只留退出按钮。CONVENTIONS §2.7.1.2：顶栏右侧用户区是
              窄屏溢出的常见元凶，用户名/角色要收起来。
              lg 以下隐藏，用 title 兜住信息（悬停仍能看到是谁） */}
          <span
            className="hidden text-xs text-muted-foreground lg:inline"
            title={`${session.username} · ${session.role}`}
          >
            {session.displayName || session.username}
          </span>
          <span className="hidden lg:inline">
            <Badge tone={session.role === 'super_admin' ? 'bad' : 'info'}>
              {t(`opsversion:role.${session.role}`)}
            </Badge>
          </span>
          {/* 数据范围被限定时要让人知道 —— 否则「怎么少了几个组织」会被当成故障。
              这条即使窄屏也保留：它影响的是"看到的数据全不全" */}
          {session.scoped && <Badge tone="warn">{t('opsversion:user.scoped')}</Badge>}
          {/* 主题 / 语言 / 密度。
              ⚠️ 窄屏**不隐藏**：用户名和角色徽章窄屏可以收起（它们是"我是谁"，
                 悬停就能看到），但切语言是"我看不看得懂这个界面"——
                 恰恰是窄屏上更需要的。三个图标按钮总共 96px，收起省不下什么。 */}
          <Preferences />
          <Button onClick={signOut}>{t('opsversion:login.signOut')}</Button>
        </div>
      }
      session={session}
      permsPending={false}
    >
      <PermGate session={session} perm={item?.perm}>
        {/* 渲染期异常兜底放在 Outlet 外面而不是最外层，是为了**保住菜单和顶栏**：
            一个页面炸了，用户还能点去别的地方，而不是对着一片白屏只能刷新 */}
        <ErrorBoundary
          key={pathname}
          resetKey={pathname}
          fallback={(err, reset) => (
            <ErrorState
              title={t('opsversion:crash.title')}
              error={{ cause: t('opsversion:crash.cause'), detail: err.message, retryable: true }}
              retryLabel={t('common:action.retry')}
              onRetry={reset}
            />
          )}
        >
          <Outlet />
        </ErrorBoundary>
      </PermGate>
    </AppShell>
  )
}

/**
 * 页面级权限闸。
 *
 * 🔴 存在的理由是**深链**：菜单过滤只挡住了「点击」这条路径，
 * 而分享出来的链接、收藏夹、浏览器地址栏补全都能直接落到页面上。
 * 没有这一层的话，把 /users 发给一个只读用户，他打开就能看到用户管理界面
 * （虽然接口会 403，但界面已经渲染出来了）。
 *
 * ⚠️ 这里挡的只是界面。真正的拦截在后端 —— 前端这层被绕过也拿不到数据。
 */
function PermGate({
  session,
  perm,
  children,
}: {
  session: Session
  perm?: string
  children: ReactNode
}) {
  const { t } = useTranslation()
  // 没有对应菜单项的路径（notFound）不做权限判断，交给下游渲染
  if (!perm || can(session, perm as Perm)) return <>{children}</>
  return (
    <NoPermission
      title={t('opsversion:perm.title')}
      reason={t('opsversion:perm.reason')}
      code={perm}
      copyLabel={t('opsversion:perm.copy')}
      copiedLabel={t('opsversion:perm.copied')}
    />
  )
}

/** 会话由 RootLayout 保证已就绪，页面路由直接取用 */
function useSessionFromRoot(): Session {
  // ⚠️ staleTime 要和 RootLayout 里那处**一致** —— 不一致的话，
  //    以短的那个为准，长的那份形同虚设，重复请求照旧。
  const { data } = useQuery({
    queryKey: ['me'],
    queryFn: () => api<MeResp>('/api/me'),
    retry: false,
    staleTime: 30_000,
  })
  return {
    username: data?.username ?? '',
    displayName: data?.display_name ?? '',
    role: data?.role ?? '',
    authSource: data?.auth_source ?? '',
    perms: data?.perms ?? [],
    scoped: data?.scoped ?? false,
  }
}

function page(path: string, Comp: (p: { session: Session }) => ReactNode) {
  return createRoute({
    getParentRoute: () => rootRoute,
    path,
    component: () => Comp({ session: useSessionFromRoot() }),
  })
}

const routes = [
  page('/', ReconPage),
  page('/orgs', OrgsPage),
  page('/datasources', DatasourcesPage),
  page('/sync', SyncPage),
  page('/users', UsersPage),
  page('/roles', RolesPage),
  page('/sso', SsoPage),
  page('/branding', BrandingPage),
  page('/tokens', TokensPage),
  page('/audit', AuditPage),
]

const notFoundRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '$',
  component: NotFound,
})

function NotFound() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  return (
    <div className="flex flex-col items-center gap-3 py-16">
      <p className="text-sm font-semibold text-foreground">{t('opsversion:notFound.title')}</p>
      <p className="font-mono text-xs text-muted-foreground">{pathname}</p>
      <Button variant="primary" onClick={() => navigate({ to: '/' })}>
        {t('opsversion:notFound.back')}
      </Button>
    </div>
  )
}

const routeTree = rootRoute.addChildren([...routes, notFoundRoute])

export const router = createRouter({
  routeTree,
  // 页面组件自己抛的异常交给这里，与 RootLayout 那层 ErrorBoundary 分工：
  // 那层保住外壳，这层负责单个页面
  defaultErrorComponent: PageCrash,
})

function PageCrash({ error, reset }: { error: Error; reset: () => void }) {
  const { t } = useTranslation()
  const info = toLoadError(error, t)
  return (
    <ErrorState
      title={t('opsversion:crash.title')}
      error={info}
      retryLabel={t('common:action.retry')}
      onRetry={reset}
    />
  )
}

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
