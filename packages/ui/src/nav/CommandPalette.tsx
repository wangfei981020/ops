import { Search } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { cn } from '../lib/cn.js'

export interface CommandItem {
  key: string
  /** 已翻译好的显示文案。组件库不含任何面向用户的字符串 */
  label: string
  /** 分组名，同样是翻译好的。用来在结果里标出"这一项属于哪一组" */
  group: string
  icon?: React.ComponentType<{ className?: string }>
}

export interface CommandPaletteProps {
  open: boolean
  onClose: () => void
  items: CommandItem[]
  onSelect: (key: string) => void
  placeholder: string
  emptyLabel: string
  /** 底部快捷键提示，如「↑↓ 选择 · ↵ 打开 · esc 关闭」 */
  hintLabel: string
}

/**
 * 命令面板（⌘K / Ctrl+K）。
 *
 * # 为什么菜单折叠还不够
 *
 * 折叠只是让菜单**变短**，没让它**变好找**。管理员有 40 多个页面，
 * 知道自己要去哪的人根本不该用菜单去找 —— 他要的是"输两个字直接到"。
 *
 * 这也是唯一能覆盖"我记得有个页面能看防火墙，但不记得它在哪一组"的手段：
 * 按名字搜，不需要先猜对分组。
 *
 * # 匹配规则
 *
 * 子序列匹配（输入的字符按顺序出现即可），不要求连续 ——
 * 输 "fw" 能命中 "firewalls"，输 "ip" 能命中 "IP 地址"。
 * 要求连续的话，中文用户几乎只能靠完整输入，等于退化成一个笨的下拉。
 */
export function CommandPalette({
  open,
  onClose,
  items,
  onSelect,
  placeholder,
  emptyLabel,
  hintLabel,
}: CommandPaletteProps) {
  const [q, setQ] = useState('')
  const [cursor, setCursor] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  const results = useMemo(() => filter(items, q), [items, q])

  // 每次打开都从干净状态开始：留着上次的搜索词，
  // 打开看到的是一份和当下无关的结果，还得先清空
  useEffect(() => {
    if (open) {
      setQ('')
      setCursor(0)
      // 等 DOM 挂上再聚焦，否则 autoFocus 在条件渲染下不生效
      requestAnimationFrame(() => inputRef.current?.focus())
    }
  }, [open])

  useEffect(() => {
    setCursor(0)
  }, [])

  // 选中项滚进视野。不做的话，按住 ↓ 到第 12 项时光标已经在可视区外，
  // 看起来像"按了没反应"
  useEffect(() => {
    listRef.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [])

  if (!open) return null

  const commit = (key: string) => {
    onSelect(key)
    onClose()
  }

  return (
    // 点遮罩关闭在这里是**允许**的：命令面板不是表单，关掉不会丢任何输入。
    // （禁止点遮罩关闭那条约定针对的是会丢数据的对话框。）
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-overlay pt-[12vh]"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        className="w-full max-w-[560px] overflow-hidden rounded-[var(--radius-lg)] border border-border bg-card shadow-2xl"
      >
        <div className="flex items-center gap-2.5 border-b border-border px-3.5 py-3">
          <Search className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <input
            ref={inputRef}
            value={q}
            onChange={(e) => {
              setQ(e.target.value)
              setCursor(0)
            }}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') {
                e.preventDefault()
                setCursor((c) => Math.min(c + 1, results.length - 1))
              } else if (e.key === 'ArrowUp') {
                e.preventDefault()
                setCursor((c) => Math.max(c - 1, 0))
              } else if (e.key === 'Enter') {
                e.preventDefault()
                const hit = results[cursor]
                if (hit) commit(hit.key)
              } else if (e.key === 'Escape') {
                e.preventDefault()
                onClose()
              }
            }}
            placeholder={placeholder}
            aria-label={placeholder}
            className="w-full bg-transparent text-[13px] text-foreground outline-none placeholder:text-muted-foreground"
          />
        </div>

        <div ref={listRef} className="max-h-[52vh] overflow-y-auto p-1.5">
          {results.length === 0 ? (
            <p className="px-2.5 py-6 text-center text-xs text-muted-foreground">{emptyLabel}</p>
          ) : (
            results.map((item, i) => {
              const Icon = item.icon
              return (
                <button
                  key={item.key}
                  type="button"
                  data-active={i === cursor}
                  // 鼠标移上去就跟着移光标：键盘和鼠标各有一个"当前项"的话，
                  // 回车会打开一个和高亮不同的页面
                  onMouseMove={() => setCursor(i)}
                  onClick={() => commit(item.key)}
                  className={cn(
                    'flex w-full cursor-pointer items-center gap-2.5 rounded-[var(--radius)] px-2.5 py-2 text-left text-[13px]',
                    i === cursor ? 'bg-secondary text-foreground' : 'text-foreground/80',
                  )}
                >
                  {Icon ? <Icon className="size-3.5 shrink-0" /> : null}
                  <span className="truncate">{item.label}</span>
                  <span className="ml-auto shrink-0 text-[11px] text-muted-foreground">
                    {item.group}
                  </span>
                </button>
              )
            })
          )}
        </div>

        <div className="border-t border-border px-3.5 py-2 text-[11px] text-muted-foreground">
          {hintLabel}
        </div>
      </div>
    </div>
  )
}

/**
 * 子序列匹配 + 排序。
 *
 * 排序规则：前缀命中 > 连续子串命中 > 分散命中。
 * 不排序的话，输 "ip" 第一个出来的可能是 "Pod 生命周期"（p...i...）
 * 而不是 "IP 地址" —— 能搜到但要往下找，等于没搜到。
 */
function filter(items: CommandItem[], q: string): CommandItem[] {
  const needle = q.trim().toLowerCase()
  if (!needle) return items
  const scored: { item: CommandItem; score: number }[] = []
  for (const item of items) {
    const hay = item.label.toLowerCase()
    if (hay.startsWith(needle)) {
      scored.push({ item, score: 0 })
    } else if (hay.includes(needle)) {
      scored.push({ item, score: 1 })
    } else if (isSubsequence(needle, hay)) {
      scored.push({ item, score: 2 })
    } else if (item.group.toLowerCase().includes(needle)) {
      // 分组名也能搜：记得"在域名那一组"但想不起页面叫什么，是很常见的状态
      scored.push({ item, score: 3 })
    }
  }
  return scored.sort((a, b) => a.score - b.score).map((s) => s.item)
}

function isSubsequence(needle: string, hay: string): boolean {
  let i = 0
  for (const ch of hay) {
    if (ch === needle[i]) i++
    if (i === needle.length) return true
  }
  return i === needle.length
}
