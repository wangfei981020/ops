/**
 * 白标：客户只填一个品牌色，五档由 CSS 的 calc 自动推导（见 tokens.css）。
 *
 * 本模块负责推导之外的另一半 —— **校验**。
 * 客户挑的品牌色未必在两套主题下都够对比度，尤其是很亮的黄绿和很深的藏蓝。
 * 不校验就发出去，结果是主按钮上的白字在浅色主题里糊成一片，
 * 而这种问题客户只会说「你们界面看不清」，不会告诉你是哪个色。
 */

export interface BrandColor {
  /** 亮度 0–1。留空则用默认：浅色 0.54 / 深色 0.62 */
  l?: number
  /** 彩度，典型 0.10–0.22。超过 0.25 在多数色相上会溢出 sRGB 色域 */
  c: number
  /** 色相角 0–360 */
  h: number
}

/** 主按钮上该压白字还是近黑字。 */
export type Foreground = 'light' | 'dark'

export interface ContrastReport {
  ok: boolean
  /** 主按钮文字该用哪种 —— 取对比度更高的那个，不是永远白字 */
  foreground: Foreground
  /** 主按钮：品牌底 × 选中的前景色 */
  primaryOnForeground: number
  /** 品牌色当文字：品牌字 × 页面底 */
  brandOnBackground: number
  /** 不达标时给出的建议亮度；已达标为 null */
  suggestedL: number | null
  messages: string[]
}

/* -------------------------------------------------------------------------
 * OKLCH → sRGB
 * 走 OKLab 标准矩阵。数值来自 Björn Ottosson 的 oklab 定义。
 * ---------------------------------------------------------------------- */

function oklchToLinearSrgb(L: number, C: number, H: number): [number, number, number] {
  const hRad = (H * Math.PI) / 180
  const a = C * Math.cos(hRad)
  const b = C * Math.sin(hRad)

  const l_ = L + 0.3963377774 * a + 0.2158037573 * b
  const m_ = L - 0.1055613458 * a - 0.0638541728 * b
  const s_ = L - 0.0894841775 * a - 1.291485548 * b

  const l = l_ * l_ * l_
  const m = m_ * m_ * m_
  const s = s_ * s_ * s_

  return [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ]
}

/** 线性分量是否落在 sRGB 色域内（留极小容差，避免边界抖动误判）。 */
export function inSrgbGamut(brand: BrandColor, l: number): boolean {
  const rgb = oklchToLinearSrgb(l, brand.c, brand.h)
  return rgb.every((v) => v >= -0.001 && v <= 1.001)
}

/**
 * WCAG 相对亮度。注意用的是**线性** RGB，不是 gamma 编码后的值 ——
 * 这里错了对比度会整体偏高，看起来"都达标"其实没有。
 */
function relativeLuminance(linear: [number, number, number]): number {
  const clamp = (v: number) => Math.min(1, Math.max(0, v))
  const [r, g, b] = linear.map(clamp) as [number, number, number]
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(lum1: number, lum2: number): number {
  const hi = Math.max(lum1, lum2)
  const lo = Math.min(lum1, lum2)
  return (hi + 0.05) / (lo + 0.05)
}

function luminanceOfOklch(L: number, C: number, H: number): number {
  return relativeLuminance(oklchToLinearSrgb(L, C, H))
}

/* 与 tokens.css 保持一致的页面底色亮度 */
const BG_L = { light: 0.985, dark: 0.145 } as const
const BG_C = 0.005
const BG_H = 265

/** 正文最低 4.5:1；主按钮上的文字同样按正文要求，它就是正文。 */
const MIN_TEXT = 4.5
/** 大字/图形元素 3:1 */
const MIN_UI = 3

/** 默认亮度 —— 与 tokens.css 的 --ops-brand-l 必须一致。 */
export const DEFAULT_L = { light: 0.54, dark: 0.56 } as const

/** 前景候选：纯白 与 近黑（近黑取 n900 的浅色档，不用纯黑，纯黑在彩色底上发死）。 */
const FG_LUM = {
  light: luminanceOfOklch(0.99, 0, 0),
  dark: luminanceOfOklch(0.18, 0.01, 265),
} as const

/**
 * 主按钮该配白字还是黑字 —— 取对比度更高的那个。
 *
 * 写死白字是白标最常见的翻车点：客户品牌色若是亮黄，白字只有 1.4:1，
 * 而黑字有 13:1。按钮上的字是不是能看清，比"看起来像个品牌按钮"重要得多。
 */
export function pickForeground(brand: BrandColor, theme: 'light' | 'dark'): Foreground {
  const L = brand.l ?? DEFAULT_L[theme]
  const brandLum = luminanceOfOklch(L, brand.c, brand.h)
  return contrast(brandLum, FG_LUM.light) >= contrast(brandLum, FG_LUM.dark) ? 'light' : 'dark'
}

/**
 * 校验品牌色在指定主题下是否可用。
 *
 * 检查两件事：
 *   1. 主按钮 —— 品牌色当底，压上**自动选出的**前景色
 *   2. 品牌色当文字（链接、选中态）压在页面底色上
 */
export function checkBrand(brand: BrandColor, theme: 'light' | 'dark'): ContrastReport {
  const L = brand.l ?? DEFAULT_L[theme]
  const messages: string[] = []

  const brandLum = luminanceOfOklch(L, brand.c, brand.h)
  const bgLum = luminanceOfOklch(BG_L[theme], BG_C, BG_H)

  const foreground = pickForeground({ ...brand, l: L }, theme)
  const primaryOnForeground = contrast(brandLum, FG_LUM[foreground])
  const brandOnBackground = contrast(brandLum, bgLum)

  if (!inSrgbGamut(brand, L)) {
    messages.push(
      `彩度 ${brand.c} 在色相 ${brand.h}° 上超出 sRGB 色域，实际显示会被裁切成另一个颜色。建议降到 0.20 以下。`,
    )
  }
  if (primaryOnForeground < MIN_TEXT) {
    const fgName = foreground === 'light' ? '白字' : '深色字'
    messages.push(
      `主按钮${fgName}对比度 ${primaryOnForeground.toFixed(2)}:1，低于 ${MIN_TEXT}:1。按钮文字会糊。`,
    )
  }
  if (brandOnBackground < MIN_UI) {
    messages.push(
      `品牌色作文字时对比度 ${brandOnBackground.toFixed(2)}:1，低于 ${MIN_UI}:1。链接和选中态会看不清。`,
    )
  }

  return {
    ok: messages.length === 0,
    foreground,
    primaryOnForeground,
    brandOnBackground,
    suggestedL: messages.length === 0 ? null : suggestL(brand, theme),
    messages,
  }
}

/**
 * 在保持色相与彩度不变的前提下，搜一个能同时满足两项对比度的亮度。
 *
 * 刻意不动色相 —— 客户给的是品牌色，擅自改色相等于换了他们的品牌。
 * 亮度可以调，那在客户看来仍是"同一个颜色深一点/浅一点"。
 * 实在找不到就返回 null，让调用方明确告诉客户这个色用不了。
 */
function suggestL(brand: BrandColor, theme: 'light' | 'dark'): number | null {
  const bgLum = luminanceOfOklch(BG_L[theme], BG_C, BG_H)
  const start = brand.l ?? DEFAULT_L[theme]

  // 从原始亮度向两侧对称搜索，优先取离它最近的解 ——
  // 客户认得出"深了一点点"，认不出"换了个色"。
  for (let step = 0; step <= 70; step++) {
    for (const dir of step === 0 ? [0] : [-1, 1]) {
      const L = start + dir * step * 0.01
      if (L < 0.2 || L > 0.95) continue
      if (!inSrgbGamut(brand, L)) continue
      const lum = luminanceOfOklch(L, brand.c, brand.h)
      // 前景色跟着亮度一起变，所以每一步都要重新选，不能沿用外层的判断
      const fg = contrast(lum, FG_LUM.light) >= contrast(lum, FG_LUM.dark) ? 'light' : 'dark'
      if (contrast(lum, FG_LUM[fg]) >= MIN_TEXT && contrast(lum, bgLum) >= MIN_UI) {
        return Math.round(L * 1000) / 1000
      }
    }
  }
  return null
}

/**
 * 应用品牌色到当前文档。
 *
 * ⚠️ 只写 --ops-brand-c / --ops-brand-h，**不写 --ops-brand-l**。
 * 亮度必须留给 tokens.css 里的 light-dark() 决定，否则浅色主题会沿用
 * 深色主题的亮度，按钮在白底上直接糊掉 —— 这是白标最容易踩的一脚。
 * 需要覆盖亮度时用 applyBrandLightness()，两套主题分别给值。
 */
export function applyBrand(brand: BrandColor): void {
  const root = document.documentElement
  root.style.setProperty('--ops-brand-c', String(brand.c))
  root.style.setProperty('--ops-brand-h', String(brand.h))

  // 主按钮前景色按实际品牌色现算。CSS 算不了对比度，只能在这里定。
  // 两套主题分别选，因为品牌色的亮度本身就随主题变。
  const fgLight = pickForeground(brand, 'light')
  const fgDark = pickForeground(brand, 'dark')
  const toColor = (f: Foreground) => (f === 'light' ? 'oklch(0.99 0 0)' : 'oklch(0.18 0.01 265)')
  root.style.setProperty(
    '--ops-primary-foreground',
    fgLight === fgDark ? toColor(fgLight) : `light-dark(${toColor(fgLight)}, ${toColor(fgDark)})`,
  )

  try {
    localStorage.setItem('ops.brand', JSON.stringify({ c: brand.c, h: brand.h }))
  } catch {
    /* 存不下只影响下次冷启动的首屏，本次仍然生效 */
  }
}

/**
 * 覆盖品牌色亮度，两套主题分别给值。
 *
 * ⚠️ 写成两个独立数值，**不要**试图用 `light-dark(a, b)` 合成一个变量：
 * light-dark() 只接受颜色参数，塞进 oklch() 的亮度位会让整个颜色变成
 * 无效值并静默算成 transparent —— 品牌色全站消失且毫无报错。
 */
export function applyBrandLightness(light: number, dark: number): void {
  const root = document.documentElement
  root.style.setProperty('--ops-brand-l-light', String(light))
  root.style.setProperty('--ops-brand-l-dark', String(dark))
}

/** 恢复出厂配色。 */
export function resetBrand(): void {
  const root = document.documentElement
  root.style.removeProperty('--ops-brand-c')
  root.style.removeProperty('--ops-brand-h')
  root.style.removeProperty('--ops-brand-l-light')
  root.style.removeProperty('--ops-brand-l-dark')
  root.style.removeProperty('--ops-primary-foreground')
  try {
    localStorage.removeItem('ops.brand')
  } catch {
    /* 同上 */
  }
}
