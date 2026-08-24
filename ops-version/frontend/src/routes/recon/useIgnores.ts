import { useCallback, useMemo, useState } from 'react'

/**
 * 人为忽略项。与后端 compare.IgnoreSet 一一对应。
 *
 * 两种粒度，语义**不能混**：
 *   services —— 整行不比（对方压根不跑这套服务）
 *   cells    —— 只有某一列不比（只有这一家不跑）
 *
 * 🔴 单元格忽略不影响同一行的其他列：
 * 「印尼不跑 wallet」不该让「马来 vs 我方 的 wallet 差异」也跟着消失。
 */
export interface IgnoreSet {
  services: string[]
  /** 服务名 → 被忽略的列（Column.StableKey()，即 orgID/projectID/env） */
  cells: Record<string, string[]>
}

export const emptyIgnores = (): IgnoreSet => ({ services: [], cells: {} })

/** 有没有任何规则 —— 界面据此决定要不要显示「已忽略 N 个」 */
export const isEmptyIgnores = (s: IgnoreSet) =>
  s.services.length === 0 && Object.keys(s.cells).length === 0

/** 被忽略的格子总数。整行忽略的不算在内（那是另一个计数） */
export const cellCount = (s: IgnoreSet) =>
  Object.values(s.cells).reduce((n, list) => n + list.length, 0)

export function useIgnores() {
  const [ignores, setIgnores] = useState<IgnoreSet>(emptyIgnores)

  const ignoreServices = useCallback((names: string[]) => {
    setIgnores((p) => ({
      ...p,
      // 去重：批量忽略时用户很可能把已经忽略过的一起选上，
      // 重复项会让「已忽略 N 个」这个数字对不上他勾了几个
      services: [...new Set([...p.services, ...names])].sort(),
    }))
  }, [])

  const unignoreService = useCallback((name: string) => {
    setIgnores((p) => ({ ...p, services: p.services.filter((s) => s !== name) }))
  }, [])

  const ignoreCell = useCallback((service: string, colKey: string) => {
    setIgnores((p) => {
      const cur = p.cells[service] ?? []
      if (cur.includes(colKey)) return p
      return { ...p, cells: { ...p.cells, [service]: [...cur, colKey] } }
    })
  }, [])

  const unignoreCell = useCallback((service: string, colKey: string) => {
    setIgnores((p) => {
      const next = (p.cells[service] ?? []).filter((c) => c !== colKey)
      const cells = { ...p.cells }
      // 🔴 空数组要**删掉键**，不能留着。
      //    留着的话 isEmptyIgnores 恒为 false —— 用户把规则全解除了，
      //    界面上却一直挂着「已忽略」的提示，而点开看是空的。
      if (next.length === 0) delete cells[service]
      else cells[service] = next
      return { ...p, cells }
    })
  }, [])

  /** 整列忽略：把当前结果里的这一列在所有服务上都标忽略 */
  const ignoreColumnFor = useCallback((services: string[], colKey: string) => {
    setIgnores((p) => {
      const cells = { ...p.cells }
      for (const s of services) {
        const cur = cells[s] ?? []
        if (!cur.includes(colKey)) cells[s] = [...cur, colKey]
      }
      return { ...p, cells }
    })
  }, [])

  const clearAll = useCallback(() => setIgnores(emptyIgnores()), [])

  const summary = useMemo(
    () => ({
      rows: ignores.services.length,
      cells: cellCount(ignores),
      empty: isEmptyIgnores(ignores),
    }),
    [ignores],
  )

  return {
    ignores,
    setIgnores,
    summary,
    ignoreServices,
    unignoreService,
    ignoreCell,
    unignoreCell,
    ignoreColumnFor,
    clearAll,
  }
}
