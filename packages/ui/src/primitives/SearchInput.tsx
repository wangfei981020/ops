import { Search, X } from 'lucide-react'
import { cn } from '../lib/cn.js'

export interface SearchInputProps {
  value: string
  onChange: (v: string) => void
  placeholder: string
  /** 清空按钮的无障碍名，来自语言包。 */
  clearLabel: string
  className?: string
}

export function SearchInput({
  value,
  onChange,
  placeholder,
  clearLabel,
  className,
}: SearchInputProps) {
  return (
    <div
      className={cn(
        'inline-flex h-8 items-center gap-1.5 rounded-[var(--radius)] px-2.5',
        'border border-border bg-card',
        'focus-within:border-primary focus-within:shadow-[0_0_0_2px_var(--ops-accent-ring)]',
        className,
      )}
    >
      <Search className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
      <input
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        aria-label={placeholder}
        // 去掉浏览器给 type=search 的原生清除按钮：各浏览器长得不一样，
        // 和我们自己的清除按钮并排出现会变成两个 X
        className={cn(
          'w-full min-w-0 bg-transparent text-[13px] text-foreground outline-none',
          'placeholder:text-muted-foreground',
          '[&::-webkit-search-cancel-button]:appearance-none',
        )}
      />
      {value ? (
        <button
          type="button"
          onClick={() => onChange('')}
          aria-label={clearLabel}
          className="cursor-pointer text-muted-foreground transition-colors duration-150 hover:text-foreground"
        >
          <X className="size-3.5" />
        </button>
      ) : null}
    </div>
  )
}
