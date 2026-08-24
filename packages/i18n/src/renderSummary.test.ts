import { describe, expect, it } from 'vitest'
import { renderSummary, type SummarySeg } from './index.js'

/**
 * 假 t：只认词表里有的 key，其余按 i18next 的真实行为**原样返回 key**。
 * 这一点是判据的全部依据，不能简化成"返回空串"。
 */
const DICT: Record<string, string> = {
  'common:summary.segSeparator': '. ',
  'common:locale.bcp47': 'en-US',
  'cron:summary.diskWatchChecked': 'Checked {{count}} clusters',
  // ⚠️ 故意保留冒号：真实词条就长这样
  'cron:summary.diskWatchCritical': 'critical (≥{{pct}}%): {{count}} — {{top}}',
  'cron:summary.diskWatchSkipped': '{{count}} clusters skipped ({{clusters}})',
}
const t = (k: string, p?: Record<string, unknown>) => {
  const tpl = DICT[k]
  if (tpl === undefined) return k
  return tpl.replace(/\{\{(\w+)\}\}/g, (_, n) => String(p?.[n] ?? ''))
}

describe('renderSummary', () => {
  it('没有片段时返回空串，让调用方退回中文原文', () => {
    expect(renderSummary(t, null)).toBe('')
    expect(renderSummary(t, [])).toBe('')
  })

  it('多个片段按语言的分隔符拼起来', () => {
    const segs: SummarySeg[] = [
      { key: 'cron:summary.diskWatchChecked', params: { count: 3 } },
      { key: 'cron:summary.diskWatchCritical', params: { pct: 85, count: 2, top: 'node17' } },
    ]
    expect(renderSummary(t, segs)).toBe('Checked 3 clusters. critical (≥85%): 2 — node17')
  })

  // 🔴 这条守的是我自己写出来的 bug：判据一度是
  //	`text === s.key || text.includes(':')`，而真实词条本身就带冒号，
  //	于是摘要**永远**为空，界面上表现为"后端没给 key"——一个安静的假象。
  it('词条里带冒号是正常的，不能因此判成缺词条', () => {
    const segs = [{ key: 'cron:summary.diskWatchCritical', params: { pct: 85, count: 2, top: 'x' } }]
    expect(renderSummary(t, segs)).not.toBe('')
    expect(renderSummary(t, segs)).toContain(':')
  })

  // 缺词条时**整句作废**，不能把生 key 拼进去。
  // 半句英文夹一个 `cron:summary.xxx`，比直接显示中文原文更糟。
  it('任一片段缺词条则整句作废', () => {
    const segs = [
      { key: 'cron:summary.diskWatchChecked', params: { count: 3 } },
      { key: 'cron:summary.notInDict', params: {} },
    ]
    expect(renderSummary(t, segs)).toBe('')
  })

  it('参数里的数组按当前语言连接，不是 String(array)', () => {
    const segs = [
      { key: 'cron:summary.diskWatchSkipped', params: { count: 2, clusters: ['uat', 'dev'] } },
    ]
    const out = renderSummary(t, segs)
    expect(out).not.toContain('uat,dev') // String(array) 的样子
    expect(out).toContain('uat')
    expect(out).toContain('dev')
  })
})
