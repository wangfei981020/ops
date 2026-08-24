import { useTranslation } from '@ops/i18n'
import { Button } from '@ops/ui'
import { type FormEvent, useEffect, useRef, useState } from 'react'
import { api } from '../../lib/api.js'
import { Preferences } from '../../layouts/Preferences.js'
import { useAppTitle, useBranding, useFavicon } from '../../lib/branding.js'

/**
 * 登录页。
 *
 * 🔴 本地账号登录在接了 SSO 之后**必须保留** —— SSO 挂掉时它是唯一的逃生通道。
 * 别的产品上吃过亏：OIDC 没调通就把密码登录关了，超管也进不去，只能改数据库救。
 *
 * 版式是左右分栏：左侧品牌与定位，右侧表单。
 * ⚠️ 左栏不是装饰 —— 第一次看到这个系统的人需要知道它解决什么问题，
 * 而一个孤零零的登录框回答不了这件事。窄屏下左栏收成一行，否则手机要滚两屏。
 */
export function LoginPage({ onSignedIn }: { onSignedIn: () => void }) {
  const { t } = useTranslation()
  const brand = useBranding()
  useFavicon(brand.favicon_data)
  useAppTitle(brand.app_name, t('opsversion:brand'))

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [showPw, setShowPw] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [sso, setSso] = useState<{ enabled: boolean; display_name?: string } | null>(null)
  const userRef = useRef<HTMLInputElement>(null)
  const pwRef = useRef<HTMLInputElement>(null)

  const appName = brand.app_name.trim() || t('opsversion:brand')
  const tagline = brand.tagline.trim() || t('opsversion:subtitle')

  // SSO 是否开启由后端说了算。拿不到就当没开，不要挡住本地登录
  useEffect(() => {
    api<{ enabled: boolean; display_name?: string }>('/api/auth/oidc/status')
      .then(setSso)
      .catch(() => setSso({ enabled: false }))
  }, [])

  // 回调失败时后端把原因放在 ?sso_error= 里跳回来。
  // 🔴 不显示的话，用户点了 SSO 转一圈又回到登录页，完全不知道发生了什么
  useEffect(() => {
    const m = new URLSearchParams(window.location.search).get('sso_error')
    if (m) {
      setErr(m)
      window.history.replaceState({}, '', window.location.pathname)
    }
  }, [])

  async function submit(e: FormEvent) {
    e.preventDefault()
    // 🔴 空表单不发请求。
    //
    //    发出去只会拿回 401 →「用户名或密码错误」，
    //    而用户明明一个字都没输 —— 他会去回想自己密码是不是记错了。
    //    该说的是「请输入用户名和密码」。
    // ⚠️ 顺便把焦点放到缺的那一栏：只报错不给去处，人还得自己找。
    if (!username.trim()) {
      setErr(t('opsversion:login.needUsername'))
      userRef.current?.focus()
      return
    }
    if (!password) {
      setErr(t('opsversion:login.needPassword'))
      pwRef.current?.focus()
      return
    }
    setBusy(true)
    setErr('')
    try {
      await api('/api/auth/login', {
        method: 'POST',
        body: JSON.stringify({ username, password }),
      })
      onSignedIn()
    } catch (e) {
      // 后端对「用户不存在」和「密码错」返回同一句话，避免变成账号枚举器
      setErr((e as Error).message || t('opsversion:login.failed'))
    } finally {
      setBusy(false)
    }
  }

  /**
   * 切换密码可见。
   *
   * ⚠️ 必须保住焦点与光标位置：直接改 type 会让部分浏览器把光标弹到开头，
   * 于是"看一眼密码"之后再打字就插到了最前面 —— 而人不会想到是这个按钮干的。
   */
  function togglePw() {
    const el = pwRef.current
    const pos = el?.selectionStart ?? null
    setShowPw((v) => !v)
    requestAnimationFrame(() => {
      if (!el) return
      el.focus()
      if (pos !== null) el.setSelectionRange(pos, pos)
    })
  }

  return (
    <div className="flex min-h-screen bg-background">
      {/* 左栏：品牌与定位。窄屏隐藏（下面有紧凑版） */}
      <aside className="hidden w-[42%] max-w-lg flex-col justify-between bg-brand-bg p-10 md:flex">
        <div>
          <div className="flex items-center gap-2.5">
            {/* 🔴 有自定义 logo 时不画品牌底色、也不加圆角，并且给更大的方框 ——
                底色会和 logo 自己的背景混在一起，圆角会切掉方形 logo 的四角，
                而 32px 对一张 logo 才勉强够（见 AppShell 里的同一条）。 */}
            <span
              className={
                brand.logo_data
                  ? 'flex size-11 items-center justify-center'
                  : 'flex size-8 items-center justify-center rounded-lg bg-brand'
              }
            >
              {brand.logo_data ? (
                <img src={brand.logo_data} alt={appName} className="size-full object-contain" />
              ) : (
                <svg viewBox="0 0 24 24" className="size-4 fill-none stroke-primary-foreground stroke-[1.75]" aria-hidden="true">
                  <path d="M12 2 3 7v10l9 5 9-5V7z" />
                  <path d="M12 22V12" />
                  <path d="m3 7 9 5 9-5" />
                </svg>
              )}
            </span>
            <div>
              <div className="text-base font-semibold text-foreground">{appName}</div>
              <div className="text-xs text-muted-foreground">{tagline}</div>
            </div>
          </div>

          <p className="mt-10 max-w-sm text-sm leading-relaxed text-foreground/80">
            {t('opsversion:login.pitch')}
          </p>

          <ul className="mt-6 space-y-2.5">
            {(['compare', 'attribute', 'export'] as const).map((k) => (
              <li key={k} className="flex gap-2.5 text-xs">
                <span className="mt-1.5 size-1.5 shrink-0 rounded-full bg-brand" aria-hidden="true" />
                <span>
                  <span className="font-medium text-foreground">{t(`opsversion:login.feat.${k}.name`)}</span>
                  <span className="ml-2 text-muted-foreground">{t(`opsversion:login.feat.${k}.desc`)}</span>
                </span>
              </li>
            ))}
          </ul>
        </div>
        <div className="text-[11px] text-muted-foreground">{__APP_VERSION__}</div>
      </aside>

      {/* 右栏：表单 */}
      <main className="relative flex flex-1 items-center justify-center p-6">
        {/* 🔴 登录页也要能切语言 —— 看不懂中文的人**连登录框都读不了**，
            等他登进去再切已经晚了。主题同理：夜里打开一屏白光是劝退的。
            ⚠️ 放右上角绝对定位，不占表单的位置，也不影响窄屏（左栏隐藏时它还在）。 */}
        <div className="absolute top-4 right-4">
          <Preferences />
        </div>
        <form onSubmit={submit} className="w-full max-w-sm">
          {/* 窄屏下的紧凑品牌行 —— 左栏隐藏时它顶上 */}
          <div className="mb-6 flex items-center gap-2 md:hidden">
            {brand.logo_data ? (
              // 同上：不加圆角（会切掉方形 logo 的四角）
              <img src={brand.logo_data} alt={appName} className="size-9 object-contain" />
            ) : null}
            <div>
              <div className="text-sm font-semibold text-foreground">{appName}</div>
              <div className="text-[11px] text-muted-foreground">{tagline}</div>
            </div>
          </div>

          <h1 className="mb-5 text-lg font-semibold text-foreground">{t('opsversion:login.title')}</h1>

          {err && (
            <p className="mb-4 rounded-md border border-danger/40 bg-danger-bg px-3 py-2 text-xs text-danger">
              {err}
            </p>
          )}

          <label className="mb-3 block">
            <span className="mb-1 block text-xs text-muted-foreground">{t('opsversion:login.username')}</span>
            <input
              ref={userRef}
              className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm text-foreground"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              // 登录页是全站唯一入口，打开就该能直接打字 ——
              // 原来 activeElement 是 BODY，每个人每次都要先点一下输入框
              // biome-ignore lint/a11y/noAutofocus: 登录页只有这一个可填字段，自动聚焦是预期行为
              autoFocus
              required
            />
          </label>

          <label className="mb-5 block">
            <span className="mb-1 block text-xs text-muted-foreground">{t('opsversion:login.password')}</span>
            <div className="relative">
              <input
                ref={pwRef}
                type={showPw ? 'text' : 'password'}
                className="w-full rounded-md border border-input bg-background py-2 pl-3 pr-10 text-sm text-foreground"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                required
              />
              {/* 图标在框**内部**右侧，不额外占一行 */}
              <button
                type="button"
                onClick={togglePw}
                aria-label={showPw ? t('opsversion:login.hidePw') : t('opsversion:login.showPw')}
                title={showPw ? t('opsversion:login.hidePw') : t('opsversion:login.showPw')}
                className="absolute inset-y-0 right-0 flex w-10 items-center justify-center
                           text-muted-foreground hover:text-foreground"
              >
                {showPw ? <EyeOff /> : <Eye />}
              </button>
            </div>
          </label>

          <Button type="submit" variant="primary" className="w-full" disabled={busy}>
            {t('opsversion:login.submit')}
          </Button>

          {sso?.enabled ? (
            <>
              <div className="my-4 flex items-center gap-2 text-[11px] text-muted-foreground">
                <span className="h-px flex-1 bg-border" />
                {t('opsversion:login.or')}
                <span className="h-px flex-1 bg-border" />
              </div>
              {/* 🔴 用 <a> 整页跳转，不能用 fetch：
                  OIDC 授权是**浏览器重定向**流程，XHR 会被 CORS 拦下，
                  而且用户需要在 IdP 页面上真正操作。 */}
              <a
                href="/api/auth/oidc/login"
                className="flex w-full items-center justify-center rounded-md border border-input
                           bg-background px-3 py-2 text-sm text-foreground hover:bg-muted"
              >
                {t('opsversion:sso.loginWith', { name: sso.display_name || 'SSO' })}
              </a>
            </>
          ) : null}
        </form>
      </main>
    </div>
  )
}

/* 图标内联而不是引 lucide：登录页是**未登录**就要加载的第一屏，
   为两个图标拉进一个图标库不值得。 */
function Eye() {
  return (
    <svg viewBox="0 0 24 24" className="size-4 fill-none stroke-current stroke-[1.75]" aria-hidden="true">
      <path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  )
}
function EyeOff() {
  return (
    <svg viewBox="0 0 24 24" className="size-4 fill-none stroke-current stroke-[1.75]" aria-hidden="true">
      <path d="M10.6 5.1A10.9 10.9 0 0 1 12 5c6.5 0 10 7 10 7a18 18 0 0 1-2.5 3.4M6.6 6.6A18 18 0 0 0 2 12s3.5 7 10 7a10.7 10.7 0 0 0 4.4-.9" />
      <path d="M9.9 9.9a3 3 0 0 0 4.2 4.2" />
      <path d="m2 2 20 20" />
    </svg>
  )
}
