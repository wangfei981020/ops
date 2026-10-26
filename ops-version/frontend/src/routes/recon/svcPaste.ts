/**
 * 批量粘贴服务名 → 在候选清单里认出来。
 *
 * 粘贴来源多半是 Teams/飞书里别人发的一串服务名，会带各种噪音：
 * 「（已编辑）」尾巴、行首序号/圆点、全角标点、`服务名：tag`。
 * 🔴 Tide 的批量粘贴就栽过两次（裸名不认、全角冒号不认），这里一次清干净。
 */

export interface PasteHit {
  /** 清理后的输入 */
  input: string
  /** 命中的服务，已排序 */
  matched: string[]
  /** true = 有逐字一致的服务，只勾它；false = 按「包含」匹配，命中的全勾 */
  exact: boolean
}

export interface PasteResult {
  /** 去重后要勾选的服务，按候选清单顺序 */
  picked: string[]
  hits: PasteHit[]
  /** 一个都没命中的输入 —— 必须摆给人看，不能悄悄丢掉 */
  unknown: string[]
}

/** 把粘贴的一大段拆成一个个服务名（小写） */
export function parsePaste(text: string): string[] {
  const cleaned = text
    // 括号里的一律是注释：（已编辑）、(edited)、（UAT）…
    .replace(/[（(][^）)]*[）)]/g, ' ')
    // `服务名：tag` / `服务名:tag` 只取冒号前
    .replace(/[:：]\S*/g, ' ')
  const out: string[] = []
  for (const raw of cleaned.split(/[\s,，、;；|]+/)) {
    const s = raw
      // 行首序号或项目符号：1.  2、  3)  - * • ·
      .replace(/^(\d+[.、)）]|[-*•·])+/, '')
      .replace(/[.。]+$/, '')
      .trim()
      .toLowerCase()
    // 纯数字/纯符号不是服务名
    if (!/[a-z]/.test(s)) continue
    if (!out.includes(s)) out.push(s)
  }
  return out
}

/**
 * 逐个输入去候选里找：
 * - 有逐字一致的 → 只勾它（`app-api` 不会顺带勾上 `app-api-gateway`）
 * - 没有 → 所有**包含**这段的服务全勾（`app-wallet` 勾上 `app-wallet-backend`、`app-wallet-frontend` 等）
 */
export function matchPaste(text: string, candidates: readonly string[]): PasteResult {
  const lower = candidates.map((c) => c.toLowerCase())
  const pickedSet = new Set<string>()
  const hits: PasteHit[] = []
  const unknown: string[] = []
  for (const input of parsePaste(text)) {
    const exactIdx = lower.indexOf(input)
    const matched =
      exactIdx >= 0
        ? [candidates[exactIdx] as string]
        : candidates.filter((_, i) => (lower[i] as string).includes(input)).sort()
    if (matched.length === 0) {
      unknown.push(input)
      continue
    }
    for (const m of matched) pickedSet.add(m)
    hits.push({ input, matched, exact: exactIdx >= 0 })
  }
  return { picked: candidates.filter((c) => pickedSet.has(c)), hits, unknown }
}
