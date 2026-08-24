import { Lock } from 'lucide-react'
import { cn } from '../lib/cn.js'

export interface NoPermissionProps {
  title: string
  /** 一句话说明为什么进不来，来自语言包 */
  reason: string
  /**
   * 缺的权限码，原样显示。
   *
   * ⚠️ 必须露出来。上一代只写「无权限访问」，用户去找管理员时说不清要什么，
   * 管理员也只能在几十个权限项里猜 —— 一次沟通要来回三趟。
   * 把 `menu:cmdb_hosts` 直接摆出来，管理员照着勾就行。
   * 权限码本身不是机密：它是一个名字，不带任何数据。
   */
  code: string
  /** 「把这段发给管理员」的复制按钮文案 */
  copyLabel: string
  /** 复制成功后的提示文案 */
  copiedLabel: string
  className?: string
}

/**
 * 无权限页。
 *
 * 刻意**不做成空白页或跳回首页**：
 *   · 空白页看起来像功能坏了，用户会先怀疑系统再怀疑权限；
 *   · 自动跳首页更糟 —— 深链被人分享过来，点开却莫名回到首页，
 *     没人会意识到这是权限问题。
 */
export function NoPermission({
  title,
  reason,
  code,
  copyLabel,
  copiedLabel,
  className,
}: NoPermissionProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center gap-3 px-6 py-16 text-center',
        className,
      )}
    >
      <Lock className="size-6 text-muted-foreground" aria-hidden="true" />
      <p className="text-sm font-semibold text-foreground">{title}</p>
      <p className="max-w-[46ch] text-xs leading-relaxed text-muted-foreground">{reason}</p>
      <CopyableCode code={code} copyLabel={copyLabel} copiedLabel={copiedLabel} />
    </div>
  )
}

function CopyableCode({
  code,
  copyLabel,
  copiedLabel,
}: {
  code: string
  copyLabel: string
  copiedLabel: string
}) {
  return (
    <button
      type="button"
      onClick={() => {
        // 复制失败不弹错：权限码就在旁边摆着，手抄一遍也就几秒，
        // 为一个降级路径弹一个错误框反而更吵
        void navigator.clipboard?.writeText(code).catch(() => {})
      }}
      title={copyLabel}
      aria-label={`${copyLabel}: ${code}`}
      className={cn(
        'group cursor-pointer rounded-[var(--radius)] border border-border bg-secondary',
        'px-2.5 py-1.5 font-mono text-xs text-foreground',
        'transition-colors duration-150 hover:border-border-strong',
      )}
    >
      {code}
      <span className="ml-2 font-sans text-[11px] text-muted-foreground">
        <span className="group-active:hidden">{copyLabel}</span>
        <span className="hidden group-active:inline">{copiedLabel}</span>
      </span>
    </button>
  )
}
