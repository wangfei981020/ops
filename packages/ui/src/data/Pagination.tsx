import { ChevronLeft, ChevronRight } from 'lucide-react'
import { Button } from '../primitives/Button.js'
import { Select } from '../primitives/Select.js'

export interface PaginationProps {
  page: number
  size: number
  total: number
  onPage: (page: number) => void
  onSize: (size: number) => void
  /** 「第 1–50 条，共 61 条」 */
  rangeLabel: (from: number, to: number, total: number) => string
  /** 「共 61 条」——总数不超过一页时用它 */
  totalLabel: (total: number) => string
  perPageLabel: string
  prevLabel: string
  nextLabel: string
  sizes?: number[]
}

/**
 * 列表分页。
 *
 * # 为什么每个列表页都必须有它
 *
 * 不是"省得滑动"——**是防止静默截断**。
 *
 * 后端拿到 `size=50` 就只返回 50 条，而页脚写着"共 61 条"。
 * 界面上没有任何迹象表明还有 11 条没显示：既不报错，也不留白，
 * 排障的人就照着这 50 条下结论了。
 * 实测撞到过：服务页 61 条只画了 50 条，Pod 页 51 条只画了 50 条。
 *
 * # 为什么总数没超过一页时也要显示条数
 *
 * 「共 12 条」这句话本身是个断言：**你看到的就是全部**。
 * 没有它，用户没法区分"只有 12 条"和"被截断到 12 条"。
 *
 * # 为什么吸底
 *
 * 放在内容流末尾的话，要看"共几条 / 翻下一页"就得先滚到底 ——
 * 50 行表格滚三屏才够到那一排按钮，而它恰恰是**用来避免滚动**的。
 * 与表头 sticky 是同一个道理：导航性的东西不该跟着内容滚走。
 */
export function Pagination({
  page,
  size,
  total,
  onPage,
  onSize,
  rangeLabel,
  totalLabel,
  perPageLabel,
  prevLabel,
  nextLabel,
  sizes = [20, 50, 100, 200],
}: PaginationProps) {
  const lastPage = Math.max(1, Math.ceil(total / size))
  const from = total === 0 ? 0 : (page - 1) * size + 1
  const to = Math.min(page * size, total)

  // 一页装得下：只声明"这就是全部"，不摆一堆点不动的按钮
  if (total <= size) {
    return (
      <div className="sticky bottom-0 z-10 flex items-center justify-end border-t border-border bg-card px-4 py-2.5">
        <span className="text-xs text-muted-foreground">{totalLabel(total)}</span>
      </div>
    )
  }

  return (
    // bg-card 不能省：透明背景下表格行会从它下面透出来，数字和行文字叠在一起
    <div className="sticky bottom-0 z-10 flex flex-wrap items-center gap-3 border-t border-border bg-card px-4 py-2.5">
      <span className="text-xs text-muted-foreground">{rangeLabel(from, to, total)}</span>

      <Select<string>
        label={perPageLabel}
        value={String(size)}
        onChange={(v) => {
          // 改每页条数后回第一页：停在第 5 页而新的页数只有 2 页，
          // 用户会看到空列表并以为数据没了
          onSize(Number(v))
          onPage(1)
        }}
        options={sizes.map((n) => ({ value: String(n), label: String(n) }))}
      />

      <div className="ml-auto flex items-center gap-2">
        <Button
          variant="ghost"
          size="sm"
          disabled={page <= 1}
          onClick={() => onPage(page - 1)}
          aria-label={prevLabel}
        >
          <ChevronLeft className="size-3.5" />
        </Button>
        <span className="tabular text-xs text-muted-foreground">
          {page} / {lastPage}
        </span>
        <Button
          variant="ghost"
          size="sm"
          disabled={page >= lastPage}
          onClick={() => onPage(page + 1)}
          aria-label={nextLabel}
        >
          <ChevronRight className="size-3.5" />
        </Button>
      </div>
    </div>
  )
}
