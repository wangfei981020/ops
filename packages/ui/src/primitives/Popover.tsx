import {
  type ReactNode,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from 'react'
import { createPortal } from 'react-dom'
import { cn } from '../lib/cn.js'

/**
 * 让 MenuItem 能关掉包着它的浮层。
 *
 * ⚠️ 「点了菜单项浮层还开着」是这个组件原来的行为，之前不明显 ——
 * absolute 定位时它跟着表格一起滚走了。改成 portal 之后它固定在屏幕上，
 * 打开对话框后菜单**还杵在旁边**，看着像是"点了两下"。
 */
const CloseCtx = createContext<(() => void) | null>(null)

/**
 * 拿到「关掉这个浮层」的方法。
 *
 * 面板里任何「选完就该走」的控件都要用它 —— `MenuItem` 一直在用，
 * 而 `Select` 漏了，于是单选下拉选完浮层还杵在那儿挡住下面的内容，
 * 人得再点一次别处。浮层自己的注释写的是「点一下弹出、**选完就走**」，
 * 所以那是实现没跟上契约，不是有意设计。
 */
export function usePopoverClose(): (() => void) | null {
	return useContext(CloseCtx)
}

export interface PopoverProps {
  /** 触发器。会被包一层 wrapper 以承载定位，本身保持原样渲染。 */
  trigger: (props: {
    onClick: () => void
    'aria-expanded': boolean
    'aria-haspopup': 'dialog'
    'aria-controls': string
  }) => ReactNode
  children: ReactNode
  /** 面板对齐边。右上角的菜单一律用 end，否则会溢出视口右侧。 */
  align?: 'start' | 'end'
  className?: string
  panelClassName?: string
}

/**
 * 轻量浮层。用于工具栏的偏好设置、用户菜单这类「点一下弹出、选完就走」的场景。
 *
 * 点外部关闭在这里是**对的**——它和「弹窗禁止点遮罩关闭」的约定不冲突：
 * 那条规矩针对的是 dialog（有未保存内容、误关代价高），
 * 而下拉菜单没有待提交状态，点外面收起是所有人预期的行为，
 * 强迫用户去点关闭按钮反而别扭。
 */
export function Popover({
  trigger,
  children,
  align = 'end',
  className,
  panelClassName,
}: PopoverProps) {
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLElement | null>(null)
  const panelId = useId()
  // ⚠️ 面板必须**渲染到 body**（portal + fixed），不能用 absolute。
  //
  // 原来是 `absolute` 定位在触发器旁边。在顶栏（偏好菜单、用户菜单）上没问题，
  // 但表格行里的操作菜单就废了：DataTable 的容器是 overflow-auto，
  // absolute 的面板被容器裁掉 —— 菜单**显示出来了，只是下半截没了**。
  // 实测域名页七个菜单项只看得见前四个，删除项永远够不着。
  // 没有报错、没有空白，从截图上也只是"菜单有点短"。
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null)

  const close = useCallback((returnFocus: boolean) => {
    setOpen(false)
    // 键盘关闭时把焦点还给触发器，否则焦点掉到 body，
    // 再按 Tab 会从页面顶部重新开始 —— 键盘用户会直接迷路。
    if (returnFocus) triggerRef.current?.focus()
  }, [])

  // 打开时按触发器的位置算一次，并在滚动/改窗口大小时重算。
  // 空间不够就向上翻 —— 最后一行的菜单不能开到视口外面去。
  useLayoutEffect(() => {
    if (!open) return
    const place = () => {
      const trg = wrapRef.current?.getBoundingClientRect()
      if (!trg) return
      const h = panelRef.current?.offsetHeight ?? 240
      const w = panelRef.current?.offsetWidth ?? 200
      const below = window.innerHeight - trg.bottom
      const top = below < h + 12 && trg.top > h + 12 ? trg.top - h - 6 : trg.bottom + 6
      const rawLeft = align === 'end' ? trg.right - w : trg.left
      // 夹在视口内：贴右边的菜单不能溢出屏幕
      const left = Math.max(8, Math.min(rawLeft, window.innerWidth - w - 8))
      setPos({ top, left })
    }
    place()
    // 第二遍：首帧拿不到真实高度（面板还没渲染），量到之后再摆一次
    const raf = requestAnimationFrame(place)
    window.addEventListener('scroll', place, true)
    window.addEventListener('resize', place)
    return () => {
      cancelAnimationFrame(raf)
      window.removeEventListener('scroll', place, true)
      window.removeEventListener('resize', place)
    }
  }, [open, align])

  useEffect(() => {
    if (!open) setPos(null)
  }, [open])

  useEffect(() => {
    if (!open) return

    const onPointerDown = (e: PointerEvent) => {
      // ⚠️ 面板现在挂在 body 上，不再是 wrapRef 的后代 ——
      // 只判 wrapRef 的话，点菜单项本身会先把菜单关掉
      const t = e.target as Node
      if (!wrapRef.current?.contains(t) && !panelRef.current?.contains(t)) close(false)
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        close(true)
      }
    }
    // 用 pointerdown 而不是 click：click 要等到抬起才触发，
    // 期间按下的位置若发生滚动，会出现「已经点到别处了但面板还开着」的一帧。
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open, close])

  return (
    <div
      ref={wrapRef}
      className={cn('relative', className)}
      // 捕获触发器 DOM 以便关闭时归还焦点。
      // 不用 cloneElement 塞 ref：那要求调用方必须转发 ref，很容易漏。
      onFocusCapture={(e) => {
        const el = e.target as HTMLElement
        if (el.getAttribute('aria-controls') === panelId) triggerRef.current = el
      }}
    >
      {trigger({
        onClick: () => setOpen((v) => !v),
        'aria-expanded': open,
        'aria-haspopup': 'dialog',
        'aria-controls': panelId,
      })}

      {open && pos
        ? createPortal(
            <div
              ref={panelRef}
              id={panelId}
              role="dialog"
              style={{ position: 'fixed', top: pos.top, left: pos.left }}
              className={cn(
                // ⚠️ 必须**高于** Dialog 的 z-40 —— Select 也是用这个组件实现的，
                // 对话框里的每一个下拉都靠它。我试过压到 z-30 来避免"菜单浮在对话框上面"，
                // 结果是对话框里的下拉展开后整个看不见：按钮变成展开态、面板在对话框底下。
                // 没有报错，快照里 aria-expanded 也是 true，只有截图能看出来什么都没弹。
                //
                // "菜单和对话框共存"那个问题的正解是点菜单项就关掉菜单（见 CloseCtx），
                // 不是压层级。
                'z-50 min-w-[200px]',
                'rounded-[var(--radius-lg)] border border-border-strong bg-popover',
                // 浮层是全站唯一允许用阴影的层级：卡片靠背景亮度分层，
                // 只有真正「浮」在内容之上的东西才需要阴影把它抬起来。
                'shadow-pop',
                panelClassName,
              )}
            >
              <CloseCtx.Provider value={() => close(false)}>{children}</CloseCtx.Provider>
            </div>,
            document.body,
          )
        : null}
    </div>
  )
}

/** 菜单项。用于用户菜单这类纵向列表。 */
export function MenuItem({
  icon,
  children,
  onClick,
  danger,
}: {
  icon?: ReactNode
  children: ReactNode
  onClick?: () => void
  danger?: boolean
}) {
  const close = useContext(CloseCtx)
  return (
    <button
      type="button"
      onClick={() => {
        onClick?.()
        close?.()
      }}
      className={cn(
        'flex w-full cursor-pointer items-center gap-2.5 px-3 py-2 text-left text-[13px]',
        'transition-colors duration-150',
        danger ? 'text-danger hover:bg-danger-bg' : 'text-foreground hover:bg-secondary',
      )}
    >
      {icon ? <span className="[&>svg]:size-3.5">{icon}</span> : null}
      {children}
    </button>
  )
}

export function MenuSeparator() {
  return <div className="my-1 h-px bg-border" />
}
