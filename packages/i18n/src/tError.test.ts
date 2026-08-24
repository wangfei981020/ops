import { describe, expect, it } from 'vitest'
import { tError } from './index.js'

/**
 * 一个假的 t：只认词表里有的 key，其余按 i18next 的真实行为返回
 * **去掉命名空间**的 key（这正是 bug 的来源）。
 */
const DICT: Record<string, string> = {
  'common:kind.user': '用户',
  'common:kind.host': '主机',
  'common:locale.bcp47': 'zh-CN',
  'error.notFoundKind': '找不到这个{{kind}}。',
  'error.unsupportedKind': '不支持的资源类型 {{kind}}。支持的是：{{supported}}。',
  'error.requiredAll': '缺少必填参数：{{fields}}。',
}
const t = (key: string, params?: Record<string, unknown>): string => {
  const hit = DICT[key]
  if (hit === undefined) {
    // 🔴 完全照搬真实行为：createI18n 配了 parseMissingKeyHandler，
    //	缺 key 时返回**去掉命名空间**的 key，**且 defaultValue 不生效**。
    //	假实现只要比真实现宽松一点（比如这里支持 defaultValue），
    //	测试就会通过而线上照样错 —— 上一版正是这么放过去的。
    return key.includes(':') ? key.slice(key.indexOf(':') + 1) : key
  }
  return hit.replace(/\{\{(\w+)\}\}/g, (_, k) => String(params?.[k] ?? ''))
}

describe('tError', () => {
  it('词表里有的机器名翻成人话', () => {
    expect(tError(t, 'error.notFoundKind', { kind: 'user' })).toBe('找不到这个用户。')
  })

  /**
   * 🔴 这一条是防回退的关键。
   *
   * 原来的判据是 `t(key) === key ? raw : hit` —— 而 i18next 找不到时
   * 返回的是**去掉命名空间**的 key（`kind.Bogus` ≠ `common:kind.Bogus`），
   * 于是"没找到"被当成"找到了"，界面上真的显示出「不支持的资源类型 kind.Bogus」。
   *
   * ⚠️ 只测 user/host 这些**词表里有**的值永远发现不了这个 bug。
   */
  it('词表里没有的取值原样保留，不能漏出 key', () => {
    const out = tError(t, 'error.unsupportedKind', { kind: 'Bogus', supported: ['A', 'B'] })
    expect(out).toContain('Bogus')
    expect(out).not.toContain('kind.Bogus')
    expect(out).not.toContain('common:')
  })

  it('数组参数按语言连接（中文顿号）', () => {
    expect(tError(t, 'error.requiredAll', { fields: ['a', 'b', 'c'] })).toBe('缺少必填参数：a、b、c。')
  })

  it('没有 params 时不炸', () => {
    expect(() => tError(t, 'error.notFoundKind')).not.toThrow()
    expect(tError(t, 'error.notFoundKind')).not.toBe('')
  })
})
