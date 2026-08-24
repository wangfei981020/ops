import {
  type ColumnDef,
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  type SortingState,
  useReactTable,
} from '@tanstack/react-table'
import { useVirtualizer } from '@tanstack/react-virtual'
import { ChevronDown, ChevronsUpDown, ChevronUp } from 'lucide-react'
import { type CSSProperties, type RefObject, useEffect, useRef, useState } from 'react'
import { cn } from '../lib/cn.js'

export interface DataTableProps<T> {
  columns: ColumnDef<T, unknown>[]
  data: T[]
  rowKey: (row: T) => string
  onRowClick?: (row: T) => void
  selectedKey?: string | null
  /**
   * 每行额外的样式，用来做「整行底色」这类按行结论上色的表达。
   *
   * ⚠️ 请用**半透明**底色。实色会盖掉 hover 和选中的反馈，
   *    而那两个是交互表格里最基本的两个信号。
   */
  rowClassName?: (row: T) => string | undefined
  /**
   * 超过这个行数就开虚拟滚动。
   *
   * 50 是经验阈值：再少的话虚拟化的开销和闪烁反而不划算，
   * 再多则 DOM 节点数开始拖慢滚动。
   */
  /**
   * 超过多少行才启用虚拟滚动。
   *
   * 🔴 400 而不是 50。理由是**行高不一致**：
   *
   *   本产品的表格里，一行可能只有一个版本号（40px），
   *   也可能带判定 + 说明 + 同步状态（130px 以上）。
   *   虚拟化靠 estimateSize 估总高，估不准就会在滚动中不停纠正 ——
   *   滚动条长度和位置一路变，肉眼看就是「滑动时一直动、闪屏」。
   *   实测 179 行时滚动高度出现过 8225 → 8537 → 8693 → 8732 四个值。
   *
   * ⚠️ 加 measureElement（实测行高）**解决不了**这个问题：
   *   没被测量过的行仍按 estimateSize 算，于是从「一次性估错」
   *   变成「边滚边纠正」，抖动次数反而更多（实测 12 个不同高度）。
   *
   * 几百行的表直接全渲染完全没问题（400 行 × 5 列 = 2000 个格子），
   * 而虚拟化省下的那点渲染时间，换来的是全程抖动 —— 不划算。
   * 真正上千行时才有必要，那时抖动的代价才低于卡顿。
   */
  virtualizeThreshold?: number
  /** 虚拟化用的行高估值。必须和当前密度档一致，否则滚动条长度会漂。 */
  rowHeight?: number
  /** 虚拟滚动容器高度。开虚拟化时必填 —— 没有固定高度就没有滚动容器。 */
  maxHeight?: number | string
  className?: string
  /** 表格最小宽度，窄屏时横向滚动而不是压扁列。 */
  minWidth?: number
}

/**
 * 操作列的标记：`meta: { action: true }`。
 *
 * # 为什么要标记，而不是"最后一列就当操作列"
 *
 * 不是每张表的最后一列都是操作列（告警页 7 列全是数据）。
 * 按位置猜的话，会把一列真数据钉死在右边并加上阴影 —— 看起来像坏了。
 *
 * # 🔴 标记之后它必须**真的在最后**
 *
 * 实测撞到：节点页的操作列是第 12 列，后面还挂着「实时用量」——
 * 那一列是在页面里 `...nodeColumns()` **之后追加**的，绕过了列定义文件。
 * 于是 13 列的表横向溢出 147px 时，最后那列整个在屏幕外，
 * 而被钉住的"操作列"其实钉在了倒数第二个位置。
 *
 * 这条由 `check-action-column.mjs` 守，靠约定拦不住（已经发生过）。
 */
export interface ActionColumnMeta {
  action?: true
}

export function DataTable<T>({
  columns,
  data,
  rowKey,
  onRowClick,
  selectedKey,
  rowClassName,
  virtualizeThreshold = 400,
  rowHeight = 40,
  maxHeight = 560,
  className,
  minWidth = 900,
}: DataTableProps<T>) {
  /**
   * 操作列固定在最右侧。
   *
   * # 为什么统一做，而不是"只给会横向滚的表"
   *
   * 一张表会不会滚，取决于窗口宽度、列内容长度、字号档位 —— 全是会变的。
   * 按"现在滚不滚"决定要不要钉，等于让这件事随环境漂移：
   * 同一张表在窄屏上操作列不见了，而写代码的人在宽屏上看不到这个问题。
   *
   * 旧版 CMDB 就是统一钉的（`fixed="right"`，几乎每个列表页都写了）。
   *
   * ⚠️ sticky 单元格**必须有不透明背景**，否则滚动时下层内容会透上来。
   * 背景色走令牌（bg-card / group-hover 跟随行态），不能写死 ——
   * 这个应用有明暗两套主题，写死的那个在另一套下就是错的。
   */
  const actionIndex = columns.findIndex(
    (c) => (c as { meta?: ActionColumnMeta }).meta?.action === true,
  )
  const isPinnable = actionIndex >= 0 && actionIndex === columns.length - 1
  // 🔴 窄屏**不钉住**操作列。
  //
  //    钉住的前提是"表格比视口宽，但操作按钮要一直够得着"。
  //    而视口本身就很窄时（手机），操作列会把中间的数据列整个盖住 ——
  //    实测 390px 下平台页只看得到「平台名 + 四个按钮」，
  //    数据源 / Harbor / 环境 / 采集状态全部不可见，等于这一页没法用。
  //
  // ⚠️ 判据用视口宽而不是表格宽：表格总是比视口宽（所以才有横向滚动），
  //    拿它判永远为真。真正决定"钉住是帮忙还是添乱"的是视口有多宽。
  const narrow = useNarrowViewport()
  const scrollRef = useRef<HTMLDivElement>(null)
  const worthPinning = usePinWorthwhile(scrollRef, isPinnable && !narrow)
  const pinned = isPinnable && !narrow && worthPinning
  const [sorting, setSorting] = useState<SortingState>([])

  const table = useReactTable({
    data,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  })

  const rows = table.getRowModel().rows
  const virtualize = rows.length > virtualizeThreshold

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    overscan: 12,
    enabled: virtualize,
    // 🔴 必须实测行高，不能只靠 estimateSize。
    //
    //    estimateSize 是**固定值**，而很多表的行高是变的 ——
    //    对账表里一行可能只有一个版本号（40px），也可能带判定+说明+同步状态
    //    （130px 以上）。按固定值算出来的总高和每行偏移都是错的，
    //    滚动时虚拟化不停地拿真实高度去纠正 → 滚动条来回跳、画面抖。
    //    用户报的「滑动滚动条会一直动、闪屏」就是这个。
    //
    // ⚠️ 行高一致的表不受影响：measureElement 量到的就是 estimateSize，
    //    不会有额外开销上的意外。
    measureElement: (el) => el.getBoundingClientRect().height,
  })

  const virtualRows = virtualize ? virtualizer.getVirtualItems() : null
  const totalSize = virtualize ? virtualizer.getTotalSize() : 0
  const paddingTop = virtualRows?.[0]?.start ?? 0
  const paddingBottom = virtualRows?.length
    ? totalSize - (virtualRows[virtualRows.length - 1]?.end ?? 0)
    : 0

  const renderRow = (row: (typeof rows)[number], style?: CSSProperties, vIndex?: number) => {
    const key = rowKey(row.original)
    const selected = selectedKey != null && key === selectedKey
    return (
      <tr
        key={key}
        style={style}
        // 虚拟化时把行交给 virtualizer 实测。
        // ⚠️ data-index 必不可少 —— measureElement 靠它知道量的是第几行，
        //    少了它测量结果会全部记到同一行上，反而更抖。
        ref={vIndex != null ? virtualizer.measureElement : undefined}
        data-index={vIndex}
        onClick={onRowClick ? () => onRowClick(row.original) : undefined}
        className={cn(
          'group/row border-b border-border transition-colors duration-150',
          onRowClick && 'cursor-pointer',
          // 选中行用左侧色条 + 淡底 —— 选中是**交互状态**，
          // 它要盖过 rowClassName 给的那层"数据状态"底色。
          selected ? 'bg-brand-bg' : 'hover:bg-secondary',
          // 数据状态底色（按行结论上色）排在最后：优先级最低，
          // 半透明且 hover/选中时自己让位，见产品侧的 .ops-row-*
          !selected && rowClassName?.(row.original),
        )}
        aria-selected={selected || undefined}
      >
        {row.getVisibleCells().map((cell, i) => (
          <td
            key={cell.id}
            className={cn(
              'whitespace-nowrap px-[var(--ops-cell-px)] text-foreground',
              selected && i === 0 && 'shadow-[inset_2px_0_0_var(--ops-accent-500)]',
              // ⚠️ 背景必须跟着行态走（选中/hover/普通），不能固定一个色：
              // 固定色会让被钉住的那一格在选中行里显得"没被选中"，
              // 而它和左边那些格子明明是同一行
              pinned &&
                i === row.getVisibleCells().length - 1 &&
                cn(
                  'sticky right-0 shadow-[inset_1px_0_0_var(--ops-border)]',
                  selected ? 'bg-brand-bg' : 'bg-card group-hover/row:bg-secondary',
                ),
            )}
            style={{ height: 'var(--ops-row-h)' }}
          >
            {flexRender(cell.column.columnDef.cell, cell.getContext())}
          </td>
        ))}
      </tr>
    )
  }

  return (
    <div
      ref={scrollRef}
      // 🔴 容器必须有**不透明底色**。
      //
      //    没有的话，普通行透出的是页面底色，而被钉住的操作列为了遮住
      //    下层内容必须自带背景 —— 两者对不上，深色下就是一条竖向色块断层
      //    （实测亮了 0.06）。给容器一个底色，sticky 列用同一个，才对得齐。
      className={cn('overflow-auto bg-card', className)}
      style={virtualize ? { maxHeight } : undefined}
    >
      <table className="w-full border-collapse text-sm" style={{ minWidth }}>
        <thead>
          {table.getHeaderGroups().map((hg) => (
            <tr key={hg.id}>
              {hg.headers.map((header, i) => {
                const sortable = header.column.getCanSort()
                const dir = header.column.getIsSorted()
                return (
                  <th
                    key={header.id}
                    // sticky 表头：滚动时不丢列名。
                    // 长表格里丢了列名，用户只能滚回顶部核对，等于每次都重读一遍表。
                    className={cn(
                      'sticky top-0 z-10 whitespace-nowrap bg-secondary px-[var(--ops-cell-px)] py-2',
                      // 操作列表头要同时贴顶和贴右，z 比普通表头高一层，
                      // 否则横向滚动时会被后面的表头盖住
                      pinned &&
                        i === hg.headers.length - 1 &&
                        'right-0 z-20 shadow-[inset_1px_0_0_var(--ops-border)]',
                      'border-b border-border text-left text-[11.5px] font-medium uppercase tracking-wide',
                      'text-muted-foreground',
                      sortable && 'cursor-pointer select-none hover:text-foreground',
                    )}
                    onClick={sortable ? header.column.getToggleSortingHandler() : undefined}
                    aria-sort={
                      dir === 'asc' ? 'ascending' : dir === 'desc' ? 'descending' : undefined
                    }
                  >
                    <span className="inline-flex items-center gap-1">
                      {flexRender(header.column.columnDef.header, header.getContext())}
                      {sortable ? (
                        dir === 'asc' ? (
                          <ChevronUp className="size-3" aria-hidden="true" />
                        ) : dir === 'desc' ? (
                          <ChevronDown className="size-3" aria-hidden="true" />
                        ) : (
                          <ChevronsUpDown className="size-3 opacity-40" aria-hidden="true" />
                        )
                      ) : null}
                    </span>
                  </th>
                )
              })}
            </tr>
          ))}
        </thead>
        <tbody>
          {virtualRows ? (
            <>
              {paddingTop > 0 ? (
                <tr aria-hidden="true">
                  <td style={{ height: paddingTop }} colSpan={columns.length} />
                </tr>
              ) : null}
              {virtualRows.map((vr) => {
                const row = rows[vr.index]
                return row ? renderRow(row, undefined, vr.index) : null
              })}
              {paddingBottom > 0 ? (
                <tr aria-hidden="true">
                  <td style={{ height: paddingBottom }} colSpan={columns.length} />
                </tr>
              ) : null}
            </>
          ) : (
            rows.map((row) => renderRow(row))
          )}
        </tbody>
      </table>
    </div>
  )
}

/**
 * 视口是不是窄到"钉住操作列会盖住数据"的程度。
 *
 * 640px = Tailwind 的 sm 断点。低于它基本就是手机竖屏，
 * 而操作列本身要占 200~250px —— 钉住等于遮掉大半个可视区。
 */
function useNarrowViewport(): boolean {
  const [narrow, setNarrow] = useState(
    () => typeof window !== 'undefined' && window.innerWidth < 640,
  )
  useEffect(() => {
    const mq = window.matchMedia('(max-width: 639px)')
    const on = () => setNarrow(mq.matches)
    on()
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  return narrow
}

/**
 * 钉住操作列**是否划算**。
 *
 * 🔴 只看视口宽是不够的（那是 useNarrowViewport 的活）。真正决定钉住是帮忙还是
 *    添乱的，是「横向要滚多少」和「操作列吃掉多宽」的对比：
 *
 *      表格宽 921，可视宽 813  →  只需横滚 108px 就能看全
 *      而操作列本身 250px      →  钉住等于永久遮掉 107px 的数据
 *
 *    这种情况下不钉，用户滚一下就看全了；钉了反而有一块内容**在任何滚动位置
 *    都看不到**。实测 900px 视口下 5 个页面全中，平台页的采集时间被砍掉一半
 *    （只剩「正常 2026/8/21 22...」）—— 。
 *
 * ⚠️ 不会来回抖动：sticky **不脱离文档流**，钉与不钉时 scrollWidth 和列宽完全相同，
 *    所以测量结果不受本 hook 自身的输出影响。
 */
function usePinWorthwhile(ref: RefObject<HTMLDivElement | null>, enabled: boolean): boolean {
  const [worth, setWorth] = useState(true)
  useEffect(() => {
    const el = ref.current
    if (!el || !enabled) {
      setWorth(true)
      return
    }
    const measure = () => {
      const overflow = el.scrollWidth - el.clientWidth
      const lastTh = el.querySelector('thead th:last-child') as HTMLElement | null
      const actionW = lastTh?.offsetWidth ?? 0
      // 溢出量还不够操作列自己宽 → 钉住是净损失
      setWorth(overflow > actionW)
    }
    measure()
    // 容器缩放、列宽变化（数据换了一批）都要重算
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    const inner = el.querySelector('table')
    if (inner) ro.observe(inner)
    return () => ro.disconnect()
  }, [ref, enabled])
  return worth
}
