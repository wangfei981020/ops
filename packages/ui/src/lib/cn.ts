import { type ClassValue, clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'

/**
 * 合并类名，后面的同类工具类覆盖前面的。
 *
 * 直接拼字符串会让 `px-3` 和调用方传的 `px-6` 同时存在，
 * 最终哪个生效取决于 CSS 里的先后顺序 —— 那是构建产物的顺序，不可控。
 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs))
}
