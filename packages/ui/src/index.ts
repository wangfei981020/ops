export { cn } from './lib/cn.js'

export { Button, type ButtonProps, type ButtonVariant, type ButtonSize } from './primitives/Button.js'
export { Badge, type BadgeProps, type BadgeTone } from './primitives/Badge.js'
export { EnumBadge, type EnumBadgeProps, type EnumCase } from './primitives/EnumBadge.js'
export { Popover, MenuItem, MenuSeparator, type PopoverProps } from './primitives/Popover.js'
export { Dialog, type DialogProps } from './primitives/Dialog.js'
export {
  Field,
  TextInput,
  TextArea,
  SecretInput,
  PasswordInput,
  Switch,
  type FieldProps,
} from './primitives/Field.js'
export {
  MultiSelect,
  type MultiSelectOption,
  type MultiSelectProps,
} from './primitives/MultiSelect.js'
export { Select, type SelectProps, type SelectOption } from './primitives/Select.js'
export { SearchInput, type SearchInputProps } from './primitives/SearchInput.js'
export {
  Drawer,
  DrawerSection,
  DrawerField,
  type DrawerProps,
} from './primitives/Drawer.js'
export {
  ThemeToggle,
  LocaleToggle,
  PreferencesMenu,
  type ThemeToggleProps,
  type LocaleToggleProps,
  type PreferencesMenuProps,
} from './primitives/PreferenceControls.js'

export { AsyncBoundary, type AsyncBoundaryProps } from './state/AsyncBoundary.js'
export { Banner, type BannerProps, type BannerTone } from './state/Banner.js'
export { ErrorBoundary, type ErrorBoundaryProps } from './state/ErrorBoundary.js'
export { EmptyState, type EmptyStateProps, type EmptyStateAction } from './state/EmptyState.js'
export { ErrorState, type ErrorStateProps } from './state/ErrorState.js'
export { NoPermission, type NoPermissionProps } from './state/NoPermission.js'
// 页面级"没采到"≠"确实没有"。字段级是 NoValue，页面级是这个
export { NotIngested, type NotIngestedProps } from './state/NotIngested.js'
export { Skeleton, TableSkeleton, type TableSkeletonProps } from './state/Skeleton.js'
export { fromQuery, type LoadState, type LoadError } from './state/types.js'

export { DataTable, type DataTableProps } from './data/DataTable.js'

// 导航：分组折叠状态与命令面板。做成共享的，其他 ops/ 系统直接继承同一套行为，
// 也就不会各自实现出不一样的快捷键和记忆规则
export { useNavCollapse, useNavRail, type NavCollapse } from './nav/useNavCollapse.js'
export { CommandPalette, type CommandPaletteProps, type CommandItem } from './nav/CommandPalette.js'
// 全产品共用的应用外壳。⚠️ 产品不要再各写一份侧栏，见 nav/AppShell.tsx 顶部注释
export { AppShell, type AppShellProps } from './nav/AppShell.js'
export { type NavItem, type NavGroup, filterNav } from './nav/types.js'
export { Pagination, type PaginationProps } from './data/Pagination.js'
/**
 * 列定义类型从这里转出，应用层不要直接依赖 @tanstack/react-table。
 * 让表格库只出现在 ui 包的依赖图里，将来换实现不用改每个页面的 import。
 */
export type { ColumnDef } from '@tanstack/react-table'
export { NoValue, type NoValueProps, type NoValueKind } from './data/NoValue.js'
export { MutationError, type MutationErrorProps } from './state/MutationError.js'
export { NotLicensed, type NotLicensedProps } from './state/NotLicensed.js'
export { Toast, useToast, type ToastKind } from './state/Toast.js'
