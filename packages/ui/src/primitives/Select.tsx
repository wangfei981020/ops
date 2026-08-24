import { Check, ChevronDown } from 'lucide-react'
import { cn } from '../lib/cn.js'
import { Popover, usePopoverClose } from './Popover.js'

export interface SelectOption<T extends string> {
  value: T
  label: string
  /** 可选计数。显示在选项右侧，用于「全部 412 / 运行中 347」这类筛选器。 */
  count?: number
}

export interface SelectProps<T extends string> {
  /** 前缀标签，如「状态」。收起时和值一起显示，让人不点开也知道这个下拉管什么。 */
  label: string
  value: T
  options: SelectOption<T>[]
  onChange: (v: T) => void
  className?: string
  /**
   * 计数的单位词，如「条」「个」。
   *
   * # 🔴 为什么收起态默认**不显示**计数
   *
   * 收起时长这样：`区域 全部 19`。
   * 而实际只有 3 个区域，19 是**总行数** —— 这一串被读成"有 19 个区域"
   * 。
   *
   * 展开后不会有这个歧义：`全部 19 / europe-west3 12 / asia-east2 5`，
   * 兄弟选项自己就把口径说清楚了。歧义只出在收起态那一行。
   *
   * 而且总数右上角本来就有一份（「共 19 个子网」），
   * 收起态再挂一个同样的数字属于重复。
   *
   * ⚠️ 所以默认收起态不显示计数。确实需要显示的，传一个单位词
   * （`countUnit="条"` → `区域 全部 19 条`）—— 有单位词就没有歧义了。
   */
  countUnit?: string
  /**
   * 禁用这个筛选器，并说明**为什么**。
   *
   * # 什么时候该禁用
   *
   * 只在「一条数据都没有」时禁用 —— 没有数据可筛，还能点开就会让人
   * 以为是自己筛错了才没结果。
   *
   * ⚠️ **不要**在「筛出 0 条」时禁用：那时候改筛选条件正是唯一的出路，
   * 禁掉等于把人锁死在空结果里。这两种空必须分开判。
   *
   * ⚠️ 也不要让它**消失**。空态把整条工具条连同同步按钮一起吞掉是
   * 另一个极端，同样错（这一条在另一个产品 上栽过）。
   * 规则是：筛选器该禁用不该消失，动作按钮该保留不该吞掉。
   *
   * 传值即禁用，值就是 tooltip 里给出的理由 —— 强制给理由，
   * 因为一个点不动又不说为什么的控件比可点的更让人困惑。
   */
  disabledReason?: string
}

/**
 * 筛选下拉。
 *
 * 取代平铺的分段控件：分段控件每多一个选项就多占一截横向空间，
 * 筛选维度一多（状态 + 集群 + 机型 + 搜索框）一行就放不下，
 * 换行后工具栏高度会随筛选条件数量跳变。下拉是定宽的，加多少个都不挤。
 *
 * 收起态显示「标签：值 计数」而不是只显示值 ——
 * 只显示「全部」的话，一排下拉看过去根本分不清哪个是哪个维度。
 */
export function Select<T extends string>({
  label,
  value,
  options,
  onChange,
  className,
  countUnit,
  disabledReason,
}: SelectProps<T>) {
  const current = options.find((o) => o.value === value)

  // 禁用态不挂 Popover：点不开的触发器就该是个静态元素，
  // 而不是一个"能点开但里面什么都做不了"的空壳
  if (disabledReason) {
    return (
      <span
        title={disabledReason}
        className={cn(
          'inline-flex h-8 items-center gap-1.5 rounded-[var(--radius)] px-2.5',
          'cursor-not-allowed border border-border bg-card text-[13px] whitespace-nowrap opacity-50',
          className,
        )}
      >
        <span className="text-muted-foreground">{label}</span>
        <span className="text-foreground">{current?.label ?? '—'}</span>
        <ChevronDown className="size-3.5 text-muted-foreground" />
      </span>
    )
  }

  return (
    <Popover
      align="start"
      panelClassName="min-w-[180px] p-1"
      className={className}
      trigger={(p) => (
        <button
          type="button"
          {...p}
          className={cn(
            'inline-flex h-8 cursor-pointer items-center gap-1.5 rounded-[var(--radius)] px-2.5',
            'border border-border bg-card text-[13px] whitespace-nowrap',
            'transition-colors duration-150 hover:border-border-strong hover:bg-secondary',
            p['aria-expanded'] && 'border-border-strong bg-secondary',
          )}
        >
          <span className="text-muted-foreground">{label}</span>
          <span className="text-foreground">{current?.label ?? '—'}</span>
          {current?.count !== undefined && countUnit ? (
            <span className="tabular text-muted-foreground">
              {current.count}
              {countUnit}
            </span>
          ) : null}
          <ChevronDown className="size-3.5 text-muted-foreground" />
        </button>
      )}
    >
      {options.map((opt) => (
        <Option
          key={opt.value}
          opt={opt}
          active={opt.value === value}
          countUnit={countUnit}
          onPick={onChange}
        />
      ))}
    </Popover>
  )
}

/**
 * 一个选项。抽成组件是为了能调 `usePopoverClose` ——
 * hook 只能在组件函数里调，直接在 map 里返回 <button> 拿不到 context。
 */
function Option<T extends string>({
  opt,
  active,
  countUnit,
  onPick,
}: {
  opt: SelectOption<T>
  active: boolean
  countUnit?: string
  onPick: (v: T) => void
}) {
  const close = usePopoverClose()
  return (
    <button
      type="button"
      onClick={() => {
        onPick(opt.value)
        // 🔴 单选下拉选完就该收起。不收的话浮层杵在那儿挡住下面的内容，
        // 人得再点一次别处才能看到自己刚选出来的结果。
        // Popover 自己的注释写的就是「点一下弹出、选完就走」——
        // MenuItem 一直这么做，这里以前漏了。
        close?.()
      }}
      aria-pressed={active}
      className={cn(
        'flex w-full cursor-pointer items-center gap-2 rounded-[var(--radius-sm)] px-2.5 py-1.5',
        'text-left text-[13px] transition-colors duration-150 hover:bg-secondary',
        active ? 'text-foreground' : 'text-muted-foreground',
      )}
    >
      <span className="flex-1">{opt.label}</span>
      {opt.count !== undefined ? (
        <span className="tabular text-xs text-muted-foreground">
          {opt.count}
          {countUnit ?? ''}
        </span>
      ) : null}
      {active ? <Check className="size-3.5 text-brand" /> : null}
    </button>
  )
}
