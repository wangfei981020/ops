import { Lock } from 'lucide-react'
import { cn } from '../lib/cn.js'

export interface NotLicensedProps {
  title: string
  /** 一句话说明这是授权问题，不是故障 */
  reason: string
  /**
   * 缺的功能名，原样显示。
   *
   * ⚠️ 必须露出来，理由和 NoPermission 露权限码一样：
   * 用户去问加购时说得清要什么，销售也不用在九个 feature 里猜。
   * 功能名不是机密，它只是一个名字。
   */
  feature?: string
  className?: string
}

/**
 * 「这个功能没在你的授权里」。
 *
 * 🔴 与 ErrorState 的区别是**下一步动作完全不同**：
 *	ErrorState 给「重试」，而授权问题重试一万次也不会变成已购。
 *	渲染成错误态的后果实测过 —— 用户反复刷新，以为是服务端出了问题。
 *
 * 🔴 与 NoPermission 的区别是**找谁**：
 *	权限找管理员（他勾一下就有），授权找采购/销售（要花钱）。
 *	两者界面相似但不能合并：合并之后管理员会去权限页找一个根本不存在的开关。
 */
export function NotLicensed({ title, reason, feature, className }: NotLicensedProps) {
  return (
    <div className={cn('flex flex-col items-center gap-2 px-6 py-12 text-center', className)}>
      <Lock className="size-7 text-muted-foreground" aria-hidden />
      <p className="text-[15px] font-medium text-foreground">{title}</p>
      <p className="max-w-[46ch] text-[13px] text-muted-foreground">{reason}</p>
      {feature ? (
        <code className="rounded-[var(--radius)] bg-secondary px-2 py-1 font-mono text-xs text-foreground">
          {feature}
        </code>
      ) : null}
    </div>
  )
}
