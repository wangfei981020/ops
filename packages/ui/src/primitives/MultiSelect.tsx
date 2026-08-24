import { Check, ChevronDown, X } from 'lucide-react'
import type { Dispatch, SetStateAction } from 'react'
import { useMemo, useState } from 'react'
import { cn } from '../lib/cn.js'
import { Popover } from './Popover.js'
import { SearchInput } from './SearchInput.js'

export interface MultiSelectOption {
  value: string
  label: string
  /** 右侧的次要数字，如制品数 */
  count?: number
}

export interface MultiSelectProps {
  label: string
  value: string[]
  /**
   * 🔴 签名是 React 的 setState 而不是 `(v: string[]) => void`。
   *
   * 用后者的话，组件只能拿闭包里的 `value` 算新集合 —— 而那是**上一次渲染**的值。
   * 同一帧内连点两个选项（快速点击、键盘连按、程序化调用）时，
   * 第二次算出来的集合基于旧值，**把第一次的选择覆盖掉**：
   * 点了两个只生效一个，而且没有任何报错。实测撞到过。
   *
   * 传 setState 后由 React 保证每次更新都看到最新值。
   * 父组件直接把 `useState` 的 setter 传进来即可。
   */
  onChange: Dispatch<SetStateAction<string[]>>
  options: MultiSelectOption[]
  /** 一个都没选时触发器上显示什么 */
  placeholder: string
  /** 已选 N 项。传函数是因为组件库不含面向用户的字符串 */
  summary: (n: number, total: number) => string
  searchPlaceholder: string
  clearLabel: string
  selectAllLabel: string
  emptyLabel: string
  /** 最多能选几个。超过后未选中的项置灰 —— 比选完再报错好 */
  max?: number
  className?: string
}

/**
 * 可搜索的多选下拉。
 *
 * 🔴 为什么不用一排复选框：选项上百个时复选框会把页面撑得很长，
 * 而人要找的通常只有两三个 —— 找的过程变成了「在一百个里用眼睛扫」。
 * 下拉 + 搜索把「找」这件事交给输入框，页面也只占一行。
 *
 * ⚠️ 与单选 `Select` 相反，选中后**不关闭浮层**：多选的下一步通常还是选，
 * 每选一个就收起来会让人点开五次。关闭交给点外部或 Esc。
 */
export function MultiSelect({
  label,
  value,
  onChange,
  options,
  placeholder,
  summary,
  searchPlaceholder,
  clearLabel,
  selectAllLabel,
  emptyLabel,
  max,
  className,
}: MultiSelectProps) {
  const [kw, setKw] = useState('')
  const picked = useMemo(() => new Set(value), [value])

  const shown = useMemo(() => {
    const q = kw.trim().toLowerCase()
    if (!q) return options
    return options.filter((o) => o.label.toLowerCase().includes(q))
  }, [options, kw])

  const atMax = max !== undefined && value.length >= max

  function toggle(v: string) {
    onChange((prev) => {
      const next = new Set(prev)
      if (next.has(v)) {
        next.delete(v)
        return [...next]
      }
      // 到上限时不再接受新的 —— 在这里挡住，比提交后报错早得多。
      // ⚠️ 上限基于 prev 判而不是闭包里的 value，理由同 onChange 的说明
      if (max !== undefined && prev.length >= max) return prev
      next.add(v)
      return [...next]
    })
  }

  return (
    <Popover
      align="start"
      panelClassName="w-[320px] p-2"
      className={className}
      trigger={(p) => (
        <button
          type="button"
          {...p}
          className={cn(
            'inline-flex h-8 max-w-[320px] cursor-pointer items-center gap-1.5 rounded-[var(--radius)] px-2.5',
            'border border-border bg-card text-[13px] whitespace-nowrap',
            'transition-colors duration-150 hover:border-border-strong hover:bg-secondary',
            p['aria-expanded'] && 'border-border-strong bg-secondary',
          )}
        >
          <span className="text-muted-foreground">{label}</span>
          <span className="truncate text-foreground">
            {value.length === 0 ? placeholder : summary(value.length, options.length)}
          </span>
          <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
        </button>
      )}
    >
      <div className="mb-1.5">
        <SearchInput
          value={kw}
          onChange={setKw}
          placeholder={searchPlaceholder}
          clearLabel={clearLabel}
        />
      </div>

      <div className="mb-1.5 flex items-center gap-2 px-1 text-[11px] text-muted-foreground">
        <span>{summary(value.length, options.length)}</span>
        <div className="flex-1" />
        {/* 全选只作用于**当前过滤结果** —— 搜了 "cmdb" 再点全选，
            意思显然是"这几个都要"，而不是把一百个全选上 */}
        {shown.length > 0 && !atMax && (
          <button
            type="button"
            className="cursor-pointer underline underline-offset-2 hover:text-foreground"
            onClick={() =>
              onChange((prev) => {
                const next = new Set(prev)
                for (const o of shown) {
                  if (max !== undefined && next.size >= max) break
                  next.add(o.value)
                }
                return [...next]
              })
            }
          >
            {selectAllLabel}
          </button>
        )}
        {value.length > 0 && (
          <button
            type="button"
            className="flex cursor-pointer items-center gap-0.5 underline underline-offset-2 hover:text-foreground"
            onClick={() => onChange([])}
          >
            <X className="size-3" />
            {clearLabel}
          </button>
        )}
      </div>

      <div className="max-h-64 overflow-y-auto">
        {shown.map((o) => {
          const on = picked.has(o.value)
          // 到上限后未选中的项置灰：让人看见"选不了了"，而不是点了没反应
          const blocked = !on && atMax
          return (
            <button
              key={o.value}
              type="button"
              onClick={() => toggle(o.value)}
              aria-pressed={on}
              disabled={blocked}
              className={cn(
                'flex w-full items-center gap-2 rounded-[var(--radius-sm)] px-2 py-1.5 text-left',
                'font-mono text-[12px] transition-colors duration-150',
                blocked
                  ? 'cursor-not-allowed text-muted-foreground/50'
                  : 'cursor-pointer hover:bg-secondary',
                on ? 'text-foreground' : 'text-muted-foreground',
              )}
            >
              <span
                className={cn(
                  'grid size-3.5 shrink-0 place-items-center rounded-[3px] border',
                  on ? 'border-brand bg-brand' : 'border-border-strong',
                )}
              >
                {on ? <Check className="size-2.5 text-primary-foreground" /> : null}
              </span>
              <span className="flex-1 truncate">{o.label}</span>
              {o.count !== undefined ? (
                <span className="tabular shrink-0 text-[11px] text-muted-foreground">{o.count}</span>
              ) : null}
            </button>
          )
        })}
        {shown.length === 0 && (
          <div className="px-2 py-3 text-center text-[11px] text-muted-foreground">{emptyLabel}</div>
        )}
      </div>
    </Popover>
  )
}
