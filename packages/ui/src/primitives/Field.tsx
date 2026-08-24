import { Eye, EyeOff } from 'lucide-react'
import { type ReactNode, useState } from 'react'
import { cn } from '../lib/cn.js'

export interface FieldProps {
  label: string
  /** 这个字段是干什么的、填错会怎样。能写就写 —— 表单最贵的是猜 */
  hint?: string
  /** 校验没过时的提示。有值时输入框描边变红 */
  error?: string
  required?: boolean
  children: ReactNode
}

export function Field({ label, hint, error, required, children }: FieldProps) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="text-[13px] font-medium text-foreground">
        {label}
        {required ? <span className="ml-0.5 text-danger">*</span> : null}
      </span>
      {children}
      {/* 错误优先于说明：出错时人只想知道怎么改 */}
      {error ? (
        <span className="text-xs text-danger">{error}</span>
      ) : hint ? (
        <span className="text-xs leading-relaxed text-muted-foreground">{hint}</span>
      ) : null}
    </label>
  )
}

const base =
  'w-full rounded-[var(--radius)] border bg-background px-2.5 py-1.5 text-[13px] text-foreground outline-none transition-colors duration-150 placeholder:text-muted-foreground'

export function TextInput({
  invalid,
  className,
  ...rest
}: React.InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }) {
  return (
    <input
      {...rest}
      className={cn(base, invalid ? 'border-danger' : 'border-border focus:border-border-strong', className)}
    />
  )
}

export function TextArea({
  invalid,
  className,
  ...rest
}: React.TextareaHTMLAttributes<HTMLTextAreaElement> & { invalid?: boolean }) {
  return (
    <textarea
      {...rest}
      className={cn(
        base,
        'resize-y font-mono text-xs',
        invalid ? 'border-danger' : 'border-border focus:border-border-strong',
        className,
      )}
    />
  )
}

/**
 * 密码输入框：带「显示密码」的眼睛按钮。
 *
 * # 为什么必须有这个按钮
 *
 * 圆点回显把「密码错了」和「密码打错了」变成同一个现象 ——
 * 而这两件事的下一步完全不同（去找管理员重置 vs 把多打的空格删掉）。
 * 实测踩过：登录失败排查了半天，最后是大小写打错一个字母。
 *
 * # 三个容易漏的细节
 *
 * - `tabIndex={-1}`：Tab 键要从密码框直接跳到登录按钮，
 *   中间夹一个眼睛按钮会让习惯键盘的人多按一次、且不知道焦点去哪了。
 * - `aria-label` 随状态变：读屏用户需要知道现在是"显示"还是"隐藏"。
 * - `type="button"`：不写的话在 form 里点一下就提交了。
 */
export function PasswordInput({
  showLabel,
  hideLabel,
  className,
  ...rest
}: Omit<React.InputHTMLAttributes<HTMLInputElement>, 'type'> & {
  showLabel: string
  hideLabel: string
}) {
  const [show, setShow] = useState(false)
  return (
    <div className="relative">
      <input {...rest} type={show ? 'text' : 'password'} className={cn(base, 'pr-9', className)} />
      <button
        type="button"
        tabIndex={-1}
        onClick={() => setShow((v) => !v)}
        aria-label={show ? hideLabel : showLabel}
        title={show ? hideLabel : showLabel}
        className="absolute top-1/2 right-2 -translate-y-1/2 cursor-pointer text-muted-foreground hover:text-foreground"
      >
        {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
      </button>
    </div>
  )
}

/**
 * 凭据输入框。
 *
 * ⚠️ 已保存的凭据**永远不会被回填**（接口本来就不返回）。所以编辑时
 * 这个框是空的，而空**不代表要清空凭据** —— 留空 = 保持原值。
 * 不写清楚的话，用户会以为凭据丢了，或者反过来以为改密码了其实没改。
 *
 * 复用 PasswordInput 的眼睛按钮：粘贴进来的密钥同样需要肉眼核对，
 * 而且这里更需要 —— 密钥比密码长得多，看不见就只能重新去源头复制。
 */
export function SecretInput({
  configured,
  placeholderConfigured,
  placeholderEmpty,
  showLabel,
  hideLabel,
  ...rest
}: Omit<React.InputHTMLAttributes<HTMLInputElement>, 'type'> & {
  configured: boolean
  placeholderConfigured: string
  placeholderEmpty: string
  showLabel: string
  hideLabel: string
}) {
  return (
    <PasswordInput
      {...rest}
      showLabel={showLabel}
      hideLabel={hideLabel}
      autoComplete="new-password"
      placeholder={configured ? placeholderConfigured : placeholderEmpty}
      className="border-border font-mono focus:border-border-strong"
    />
  )
}

export function Switch({
  checked,
  onChange,
  label,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  label: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      onClick={() => onChange(!checked)}
      className="flex cursor-pointer items-center gap-2 text-[13px] text-foreground"
    >
      <span
        className={cn(
          'relative h-4 w-7 rounded-full transition-colors duration-150',
          checked ? 'bg-primary' : 'bg-border',
        )}
      >
        <span
          className={cn(
            'absolute top-0.5 size-3 rounded-full bg-card transition-all duration-150',
            checked ? 'left-3.5' : 'left-0.5',
          )}
        />
      </span>
      {label}
    </button>
  )
}
