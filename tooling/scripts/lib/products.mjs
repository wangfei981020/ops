import { existsSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

/**
 * 自动发现 `ops/` 下的产品，**不要在守卫里手写产品清单**。
 *
 * # 为什么
 *
 * 三个守卫原本各写着一份硬编码清单：
 *
 *   check-i18n-usage.mjs   SRC_DIRS = ['某个同类产品/frontend/src', '另一个产品/frontend/src']
 *   check-hardcoded-colors.mjs  TARGETS = [..., '某个同类产品/frontend/src']
 *   check-perm-codes.mjs   nav: '某个同类产品/frontend/src/layouts/nav.ts'
 *
 * 旁边还写着注释「新产品加进来时补一行」—— 而 另一个产品 建出来之后
 * 一行都没补。结果是**这个产品完全不受任何守卫约束**，
 * 而守卫照样打印「✓ 全部通过」。
 *
 * 一个只检查部分产品的守卫，比没有守卫更危险：
 * 它的绿色会被当成"全都查过了"。
 *
 * 所以清单必须是**推导出来的**，不是维护出来的。
 */
// 🔴 仓库根 = tooling/scripts/lib 上溯**三层**。
//    ⚠️ 层数写错时不会报错，只是 readdirSync 扫到别的目录、
//    一个产品都发现不了，而守卫照样打印「✓ 全部通过」——
//    正是这个文件开头说的那种"绿色的谎"。
const ROOT = new URL('../../../', import.meta.url).pathname

/** 所有产品目录名，如 ['另一个产品', '某个同类产品', '另一个产品'] */
export function products() {
  return readdirSync(ROOT, { withFileTypes: true })
    .filter((d) => d.isDirectory() && d.name.startsWith('ops-') && d.name !== 'ops-kit')
    .map((d) => d.name)
    .sort()
}

/** 有前端的产品的 src 目录（相对 ROOT），如 ['另一个产品/frontend/src', ...] */
export function frontendDirs() {
  return products()
    .map((p) => join(p, 'frontend/src'))
    .filter((d) => existsSync(join(ROOT, d)) && statSync(join(ROOT, d)).isDirectory())
}

/** 有后端的产品的 backend 目录（相对 ROOT），如 ['某个同类产品/backend', ...] */
export function backendDirs() {
  return products()
    .map((p) => join(p, 'backend'))
    .filter((d) => existsSync(join(ROOT, d)) && statSync(join(ROOT, d)).isDirectory())
}

/** 某个产品的菜单数据文件；没有则返回 null */
export function navFile(product) {
  const p = join(product, 'frontend/src/layouts/nav.ts')
  return existsSync(join(ROOT, p)) ? p : null
}

export { ROOT }
