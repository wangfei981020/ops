import { useTranslation } from '@ops/i18n'
import { Banner, Button } from '@ops/ui'
import { ImageOff } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { type ChangeEvent, useEffect, useRef, useState } from 'react'
import { api } from '../../lib/api.js'
import { type Branding, useBranding } from '../../lib/branding.js'
import type { Session } from '../../lib/session.js'

/**
 * 与后端 validateImage 保持一致。
 *
 * ⚠️ 两边不一致的话，前端放过的会被后端拒 ——
 * 用户看到的是一句莫名其妙的 400，而不是"图太大了"。
 */
const MAX_LOGO = 256 * 1024
const MAX_FAVICON = 64 * 1024
const ACCEPT = 'image/png,image/jpeg,image/webp,image/svg+xml,image/x-icon'

export function BrandingPage({ session }: { session: Session }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const current = useBranding()
  const [f, setF] = useState<Branding | null>(null)
  const [err, setErr] = useState('')
  const [okMsg, setOkMsg] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => setF(current), [current])
  const canWrite = session.perms.includes('user.admin')

  function pick(field: 'logo_data' | 'favicon_data', max: number, label: string) {
    return (e: ChangeEvent<HTMLInputElement>) => {
      const file = e.target.files?.[0]
      if (!file) return
      // 🔴 在**读文件之前**就按原始大小拦：base64 会把体积撑大三分之一，
      //    等编码完再判会让"看着 300KB 的图"报成 400KB，对不上人的直觉
      if (file.size > max) {
        setErr(t('opsversion:branding.tooLarge', { label, kb: Math.ceil(max / 1024) }))
        e.target.value = ''
        return
      }
      const r = new FileReader()
      r.onload = () => {
        setF((p) => (p ? { ...p, [field]: String(r.result) } : p))
        setErr('')
      }
      r.readAsDataURL(file)
      e.target.value = '' // 允许重复选同一个文件
    }
  }

  async function save() {
    if (!f) return
    setBusy(true)
    setErr('')
    setOkMsg('')
    try {
      await api('/api/branding', { method: 'PUT', body: JSON.stringify(f) })
      setOkMsg(t('opsversion:branding.saved'))
      qc.invalidateQueries({ queryKey: ['branding'] })
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  if (!f) return null

  /**
   * 预览方框。
   *
   * 🔴 未设置时**不能往框里塞文字**：方框是固定尺寸（logo 40px、favicon 24px），
   *    「未设置」三个字放进去必然撑破边框、压住下面的说明文字。
   *    框里只放一个占位图标（尺寸可控），文字挪到框外面去说。
   */
  const preview = (src: string, cls: string) =>
    src ? (
      <img src={src} alt="" className={cls} />
    ) : (
      <ImageOff className="size-1/2 text-muted-foreground" aria-hidden />
    )

  return (
    <div className="max-w-2xl space-y-4">
      <div>
        <h1 className="text-base font-semibold text-foreground">{t('opsversion:branding.title')}</h1>
        <p className="mt-0.5 text-xs text-muted-foreground">{t('opsversion:branding.subtitle')}</p>
      </div>

      {err ? <Banner tone="bad">{err}</Banner> : null}
      {okMsg ? <Banner tone="info">{okMsg}</Banner> : null}

      <div className="space-y-4 rounded-lg border border-border bg-card p-4">
        <div>
          <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:branding.appName')}</div>
          <input
            value={f.app_name}
            onChange={(e) => setF({ ...f, app_name: e.target.value })}
            placeholder={t('opsversion:brand')}
            className="w-full rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground"
          />
          <div className="mt-1 text-[11px] text-muted-foreground">
            {t('opsversion:branding.appNameHint')}
          </div>
        </div>

        <div>
          <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:branding.tagline')}</div>
          <input
            value={f.tagline}
            onChange={(e) => setF({ ...f, tagline: e.target.value })}
            placeholder={t('opsversion:subtitle')}
            className="w-full rounded-md border border-input bg-background px-2.5 py-1.5 text-xs text-foreground"
          />
        </div>

        <div>
          <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:branding.logo')}</div>
          <div className="flex items-center gap-2">
            <span className="flex size-10 shrink-0 items-center justify-center rounded-lg border border-border bg-background">
              {preview(f.logo_data, 'size-full rounded-lg object-contain')}
            </span>
            {!f.logo_data && (
              <span className="text-[11px] text-muted-foreground">{t('opsversion:branding.none')}</span>
            )}
            <FilePick
              accept={ACCEPT}
              disabled={!canWrite}
              onChange={pick('logo_data', MAX_LOGO, t('opsversion:branding.logo'))}
              label={t('opsversion:branding.choose')}
            />
            {f.logo_data ? (
              <Button size="sm" variant="ghost" disabled={!canWrite} onClick={() => setF({ ...f, logo_data: '' })}>
                {t('opsversion:branding.clear')}
              </Button>
            ) : null}
          </div>
          {/* 🔴 按**实际显示尺寸**再预览一次。
              上面那个 40px 的框只是"有没有图"，而侧栏里它是 32px、
              登录页 44px —— 一张有细节的图在 32px 下会糊成一团，
              而人是保存完去侧栏才发现的。让他在这里就看见。 */}
          {f.logo_data && (
            <div className="mt-2 flex items-center gap-3 rounded-md border border-dashed border-border px-3 py-2">
              <span className="text-[11px] text-muted-foreground">
                {t('opsversion:branding.actualSize')}
              </span>
              <span className="flex items-center gap-1.5">
                <img src={f.logo_data} alt="" className="size-8 object-contain" />
                <span className="text-[11px] text-muted-foreground">32px</span>
              </span>
              <span className="flex items-center gap-1.5">
                <img src={f.logo_data} alt="" className="size-11 object-contain" />
                <span className="text-[11px] text-muted-foreground">44px</span>
              </span>
            </div>
          )}
          <div className="mt-1 text-[11px] text-muted-foreground">
            {t('opsversion:branding.logoHint', { kb: MAX_LOGO / 1024 })}
          </div>
        </div>

        <div>
          <div className="mb-1 text-[11px] text-muted-foreground">{t('opsversion:branding.favicon')}</div>
          <div className="flex items-center gap-2">
            <span className="flex size-6 shrink-0 items-center justify-center rounded border border-border bg-background">
              {preview(f.favicon_data, 'size-full object-contain')}
            </span>
            {!f.favicon_data && (
              <span className="text-[11px] text-muted-foreground">{t('opsversion:branding.none')}</span>
            )}
            <FilePick
              accept={ACCEPT}
              disabled={!canWrite}
              onChange={pick('favicon_data', MAX_FAVICON, t('opsversion:branding.favicon'))}
              label={t('opsversion:branding.choose')}
            />
            {f.favicon_data ? (
              <Button
                size="sm"
                variant="ghost"
                disabled={!canWrite}
                onClick={() => setF({ ...f, favicon_data: '' })}
              >
                {t('opsversion:branding.clear')}
              </Button>
            ) : null}
          </div>
          {/* ⚠️ 要说清它与 logo 的区别：直接拿宽幅 logo 当 favicon 会糊成一团 */}
          <div className="mt-1 text-[11px] text-muted-foreground">
            {t('opsversion:branding.faviconHint', { kb: MAX_FAVICON / 1024 })}
          </div>
        </div>

        <div className="flex items-center justify-between border-t border-border pt-3">
          <span className="text-[11px] text-muted-foreground">
            {f.updated_by ? t('opsversion:branding.updatedBy', { user: f.updated_by }) : ''}
          </span>
          <Button variant="primary" size="sm" disabled={!canWrite || busy} onClick={save}>
            {t('common:action.save')}
          </Button>
        </div>
      </div>
    </div>
  )
}

/**
 * 选文件。
 *
 * 🔴 不能直接用裸的 `<input type="file">`：
 *
 *   它的按钮是**浏览器原生控件**，不吃我们的主题变量 ——
 *   深色下按钮和背景几乎同色，界面上只剩「选择文件 未选择任何文件」
 *   一串灰字，看着像一段说明而不是一个能点的东西。
 *   实测：用户在深色模式下**根本没找到上传入口**。
 *
 * ⚠️ 隐藏 input 用 `sr-only` 而不是 `display:none` ——
 *   后者在部分浏览器里会让 `click()` 失效，也拿不到键盘焦点。
 */
function FilePick({
  accept,
  disabled,
  onChange,
  label,
}: {
  accept: string
  disabled?: boolean
  onChange: (e: ChangeEvent<HTMLInputElement>) => void
  label: string
}) {
  const ref = useRef<HTMLInputElement>(null)
  return (
    <>
      <input
        ref={ref}
        type="file"
        accept={accept}
        disabled={disabled}
        onChange={onChange}
        className="sr-only"
      />
      <Button size="sm" disabled={disabled} onClick={() => ref.current?.click()}>
        {label}
      </Button>
    </>
  )
}
