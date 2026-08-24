/**
 * 首屏引导脚本 —— 必须在任何 CSS 之前、同步执行。
 *
 * 漏了这一步会有一帧白闪（深色偏好的用户先看到浅色再跳成深色），
 * 那一帧是「廉价感」最直接的来源，而且只在冷加载出现、最容易漏测。
 *
 * 同理 lang：CJK 行高靠 :lang(zh) 选择器生效，晚设一帧文字会跳行。
 *
 * 由 opsBootScript() vite 插件注入 index.html，不要手抄到 HTML 里 ——
 * 手抄会产生第二份真相，改一处忘另一处。
 */
;(() => {
  try {
    const d = document.documentElement
    // 'dark' | 'light' | 'system'（缺省即 system，交给 CSS 的 color-scheme 决定）
    const t = localStorage.getItem('ops.theme')
    if (t === 'dark' || t === 'light') d.setAttribute('data-theme', t)

    const density = localStorage.getItem('ops.density')
    if (density) d.setAttribute('data-density', density)

    const locale = localStorage.getItem('ops.locale')
    if (locale) d.setAttribute('lang', locale)

    // 白标：品牌三值。存的是 JSON {l,c,h}
    const brand = localStorage.getItem('ops.brand')
    if (brand) {
      const b = JSON.parse(brand)
      if (typeof b.c === 'number') d.style.setProperty('--ops-brand-c', String(b.c))
      if (typeof b.h === 'number') d.style.setProperty('--ops-brand-h', String(b.h))
    }
  } catch {
    // localStorage 在隐私模式/沙箱 iframe 下会抛异常。
    // 此时静默退回系统主题即可 —— 绝不能因为读不到偏好就白屏。
  }
})()
