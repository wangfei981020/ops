import { describe, expect, it } from 'vitest'
import { DEFAULT_L, checkBrand, inSrgbGamut, pickForeground } from './brand.js'

describe('sRGB 色域判定', () => {
  it('中性灰在任意亮度都在色域内', () => {
    expect(inSrgbGamut({ c: 0, h: 0 }, 0.5)).toBe(true)
    expect(inSrgbGamut({ c: 0, h: 0 }, 0.02)).toBe(true)
    expect(inSrgbGamut({ c: 0, h: 0 }, 0.98)).toBe(true)
  })

  it('高彩度 + 高亮度会溢出色域', () => {
    // 亮度 0.9 配彩度 0.3 是物理上做不出来的颜色，
    // 浏览器会静默裁切成另一个色 —— 必须在设置时就拦住。
    expect(inSrgbGamut({ c: 0.3, h: 250 }, 0.9)).toBe(false)
  })

  it('默认品牌色（靛蓝紫）在两套主题的亮度下都在色域内', () => {
    expect(inSrgbGamut({ c: 0.19, h: 278 }, DEFAULT_L.dark)).toBe(true)
    expect(inSrgbGamut({ c: 0.19, h: 278 }, DEFAULT_L.light)).toBe(true)
  })
})

describe('主按钮前景色自动选择', () => {
  it('深靛蓝配白字', () => {
    expect(pickForeground({ c: 0.19, h: 278 }, 'dark')).toBe('light')
  })

  it('亮黄配深色字 —— 写死白字会只剩 1.4:1', () => {
    const brand = { l: 0.88, c: 0.16, h: 95 }
    expect(pickForeground(brand, 'light')).toBe('dark')

    const r = checkBrand(brand, 'light')
    expect(r.foreground).toBe('dark')
    // 换成深色字之后这个品牌色是完全可用的，不该被拒绝
    expect(r.primaryOnForeground).toBeGreaterThan(10)
  })
})

describe('对比度校验', () => {
  it('默认品牌色在深浅两套主题下都达标', () => {
    for (const theme of ['light', 'dark'] as const) {
      const r = checkBrand({ c: 0.19, h: 278 }, theme)
      expect(r.messages, `${theme}: ${r.messages.join(' / ')}`).toEqual([])
      expect(r.ok).toBe(true)
      expect(r.primaryOnForeground).toBeGreaterThanOrEqual(4.5)
      expect(r.brandOnBackground).toBeGreaterThanOrEqual(3)
    }
  })

  it('默认亮度下主按钮必须是白字 —— 这是选 0.56 而不是 0.62 的真正原因', () => {
    // L≥0.60 同样能达标，但达标方式是把按钮文字翻成近黑色。
    // 深紫底压近黑字在企业产品里很怪。这条断言锁的是设计意图，不是无障碍下限：
    // 有人"觉得暗"把 L 调到 0.62，按钮文字会静默变黑，没有任何东西会报错。
    expect(checkBrand({ c: 0.19, h: 278 }, 'dark').foreground).toBe('light')
    expect(checkBrand({ c: 0.19, h: 278 }, 'light').foreground).toBe('light')
    expect(checkBrand({ l: 0.62, c: 0.19, h: 278 }, 'dark').foreground).toBe('dark')
  })

  it('L=0.58 是死区 —— 白字和深色字都不够，这是默认值必须避开的那一档', () => {
    const r = checkBrand({ l: 0.58, c: 0.19, h: 278 }, 'dark')
    expect(r.ok).toBe(false)
    expect(r.primaryOnForeground).toBeLessThan(4.5)
  })

  it('不达标时给出的建议亮度必须真的能通过校验', () => {
    const brand = { c: 0.19, h: 278 }
    const bad = checkBrand({ ...brand, l: 0.58 }, 'dark')
    expect(bad.ok).toBe(false)
    expect(bad.suggestedL).not.toBeNull()

    // 建议值不能只是"看起来合理"，得真的复验通过，
    // 否则等于把问题从我们这里推给了客户。
    const fixed = checkBrand({ ...brand, l: bad.suggestedL as number }, 'dark')
    expect(fixed.ok, `建议 L=${bad.suggestedL} 仍不达标：${fixed.messages.join(' / ')}`).toBe(true)
  })

  it('深色主题下极暗的品牌色作文字会被判不达标', () => {
    const r = checkBrand({ l: 0.25, c: 0.1, h: 265 }, 'dark')
    expect(r.ok).toBe(false)
    expect(r.brandOnBackground).toBeLessThan(3)
  })

  it('超色域的品牌色会被明确告知，而不是静默裁切', () => {
    const r = checkBrand({ l: 0.9, c: 0.3, h: 250 }, 'dark')
    expect(r.ok).toBe(false)
    expect(r.messages.some((m) => m.includes('色域'))).toBe(true)
  })
})
