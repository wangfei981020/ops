import { describe, expect, it } from 'vitest'
import { formatDuration, formatRelativeTime } from './index.js'

describe('formatDuration', () => {
  // 🔴 ①：EN 模式下这一句渲染成 `11 小时 without an update`，
  // 同一句话里中英混排 —— 用户只会认为是翻译漏了。
  it('EN 下不出现中文单位', () => {
    for (const ms of [30_000, 90_000, 11.6 * 3600_000, 5 * 86400_000, 40 * 86400_000]) {
      const s = formatDuration(ms, 'en-US')
      expect(s, `${ms}ms → ${s}`).not.toMatch(/[一-鿿]/)
    }
  })

  it('中文 locale 下给中文单位', () => {
    expect(formatDuration(11.6 * 3600_000, 'zh-CN')).toMatch(/[一-鿿]/)
  })

  // 🔴 ②：同一行同时显示「12 hours ago」和「11 小时 without an update」，
  // 同一个事实两个数字。根因是两处取整方式不同（round vs floor）。
  // 这条测试锁的是：只要来自同一时刻，两个函数给出的数值必须相同。
  it('与 formatRelativeTime 的取整一致（同一时刻不出两个数）', () => {
    const now = new Date('2026-08-21T12:00:00Z')
    // 11.6 小时前：round → 12，floor → 11，正是当初分叉的那个值
    const cases = [11.6, 0.9, 5.4, 47.9, 100.5]
    for (const h of cases) {
      const at = new Date(now.getTime() - h * 3600_000)
      const rel = formatRelativeTime(at, 'en-US', now)
      const dur = formatDuration(now.getTime() - at.getTime(), 'en-US')
      const relNum = rel.match(/\d+/)?.[0]
      const durNum = dur.match(/\d+/)?.[0]
      // numeric:'auto' 会把 1 天前说成 "yesterday"（没有数字），那不算矛盾
      if (relNum !== undefined && durNum !== undefined) {
        expect(durNum, `${h}h: "${rel}" vs "${dur}"`).toBe(relNum)
      }
    }
  })

  it('非有限值给「—」而不是 NaN', () => {
    expect(formatDuration(Number.NaN, 'en-US')).toBe('—')
    expect(formatDuration(Number.POSITIVE_INFINITY, 'zh-CN')).toBe('—')
  })
})
