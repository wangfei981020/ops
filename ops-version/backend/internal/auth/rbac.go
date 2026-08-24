// Package auth 定义角色与权限判定。
//
// 设计要点见 docs/plans/opsversion-plan.md 阶段 4。这里只放判定，
// 会话与用户 CRUD 在 store 层。
package auth

// Perm 一项权限。
type Perm string

const (
	PermView       Perm = "view"        // 看对账 / 镜像同步
	PermExport     Perm = "export"      // 导出 xlsx（会外发，一律记审计）
	PermRefresh    Perm = "refresh"     // 刷新对账组（对外部系统有负载）
	PermPlanWrite  Perm = "plan.write"  // 管对比方案 / 服务别名
	PermOrgWrite   Perm = "org.write"   // 管组织（含写入凭据）
	PermAlertWrite Perm = "alert.write" // 管告警设置 / 通知渠道
	PermAudit      Perm = "audit.view"  // 看审计日志
	PermUserAdmin  Perm = "user.admin"  // 用户与角色管理

	// 🔴 PermSyncTrigger 单列一项，**不归进 PermOrgWrite 或「编辑」**。
	//    它是真的往对方公司的 Harbor 推镜像：改配置错了能改回来，推镜像推不回来。
	//    权限模型里凡是「不可逆的对外动作」都该单独一项，否则迟早被顺带授出去。
	PermSyncTrigger Perm = "sync.trigger"
)

// 角色。刻意只有四个：角色一多，"这个人该给哪个"就没人说得清了，
// 最后的结果是所有人都给 admin。
const (
	RoleSuperAdmin = "super_admin"
	RoleAdmin      = "admin"
	RoleEditor     = "editor"
	RoleViewer     = "viewer"
)

// 角色定义搬到 registry.go —— 自定义角色要在运行期加载，
// 静态 map 装不下。内置四个的权威定义在 builtinRoles()。

// Can 判断角色是否有某项权限。
//
// 🔴 注意这里**没有** "查看凭据明文" 这项权限，任何角色都拿不到 ——
// 凭据只能覆盖写入，读接口一律不回显。超管也不例外。
// 生产验证时两个 P0 都是「接口把凭据发给了不该看的人」，这条是从那儿来的。
func Can(role string, p Perm) bool {
	r, ok := RoleOf(role)
	return ok && r.Perms[p]
}

// RoleValid 角色码是否合法。
// 🔴 未知角色一律**不给任何权限**，而不是回落到 viewer ——
// 拼错一个角色码就静默降级成只读，会表现为「这个人明明配了管理员却什么都点不了」，
// 查半天查不出。让它彻底不能用，反而一眼就发现配错了。
func RoleValid(role string) bool {
	_, ok := RoleOf(role)
	return ok
}

// Roles 返回全部角色码，供前端渲染下拉。
// ⚠️ 含自定义角色 —— 别在别处另写一份内置四个的清单，那样新建的角色不出现在下拉里。
func Roles() []string {
	rs := AllRoles()
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Code
	}
	return out
}

// PermsOf 返回某角色的权限清单。
//
// 🔴 前端必须用这个接口返回的结果来决定显示什么，
// **禁止按 role_code 或 auth_source 在前端自行推导** ——
// 前后端两套判据必然分叉，同一个人在两个页面看到的权限会不一致。这个坑栽过三次。
func PermsOf(role string) []Perm {
	r, ok := RoleOf(role)
	if !ok {
		// 未知角色 = 零权限。见 RoleValid 的说明：静默降级比彻底不能用更难查。
		return nil
	}
	return r.PermList()
}

// VisibleOrgs 数据范围：这个用户能看到哪些组织。
//
// 与角色是**两个独立维度** —— 一个 viewer 可能只能看 A公司，
// 一个 admin 也可能被限定只管某几个组织。
// 返回 nil 表示不限。
type Scope struct {
	Role string
	Orgs map[int64]bool // nil = 不限
}

func (s Scope) CanSee(orgID int64) bool {
	if s.Orgs == nil {
		return true
	}
	return s.Orgs[orgID]
}

// IsKnownRole 角色码是否合法。
//
// 🔴 给组映射用：映射表里写错一个字（admln）会让那条永远不生效，
// 而界面上看着好好的。存进去之前就挡住，比事后查"他为什么没权限"便宜得多。
func IsKnownRole(code string) bool {
	_, ok := RoleOf(code)
	return ok
}
