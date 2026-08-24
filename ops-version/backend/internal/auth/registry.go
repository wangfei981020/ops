package auth

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

// Role 一个角色的完整定义。
type Role struct {
	Code    string
	Name    string
	Perms   map[Perm]bool
	Builtin bool
	Note    string
}

// PermList 该角色的权限清单，顺序固定（供界面展示与比对）。
func (r Role) PermList() []Perm {
	var out []Perm
	for _, p := range AllPerms() {
		if r.Perms[p] {
			out = append(out, p)
		}
	}
	return out
}

// Covers 这个角色是否覆盖另一个角色的全部权限。
//
// 🔴 自定义角色出现后，角色之间**不再是全序**：
// 「只能管通知的角色」和「只读+导出」谁大谁小无从谈起。
// 所以凡是要比较角色的地方，都只能问「谁覆盖谁」，不能问「谁的等级高」。
func (r Role) Covers(o Role) bool {
	for p := range o.Perms {
		if !r.Perms[p] {
			return false
		}
	}
	return true
}

// registry 当前生效的角色表。
//
// 用 atomic.Value 存整份快照而不是加锁改 map：
// 判权限是每个请求都要走的路径，读多写极少（只有改角色时才换一次）。
// ⚠️ 快照必须整份替换，绝不能就地改里面的 map —— 那样读的一方会看到半成品。
var registry atomic.Value // map[string]Role

func init() {
	// 启动即可用：DB 还没连上时（迁移、reset-password 子命令）也得能判权限
	registry.Store(builtinRoles())
}

// builtinRoles 内置角色。**这是唯一权威定义**，迁移里的种子必须与它一致，
// 由 VerifyBuiltins 在启动时校验。
func builtinRoles() map[string]Role {
	return map[string]Role{
		RoleViewer: {Code: RoleViewer, Name: "只读", Builtin: true,
			Perms: permSet(PermView, PermExport)},
		RoleEditor: {Code: RoleEditor, Name: "编辑", Builtin: true,
			Perms: permSet(PermView, PermExport, PermRefresh, PermPlanWrite)},
		RoleAdmin: {Code: RoleAdmin, Name: "管理员", Builtin: true,
			Perms: permSet(PermView, PermExport, PermRefresh, PermPlanWrite,
				PermOrgWrite, PermAlertWrite, PermAudit, PermSyncTrigger)},
		RoleSuperAdmin: {Code: RoleSuperAdmin, Name: "超级管理员", Builtin: true,
			Perms: permSet(PermView, PermExport, PermRefresh, PermPlanWrite,
				PermOrgWrite, PermAlertWrite, PermAudit, PermSyncTrigger, PermUserAdmin)},
	}
}

func permSet(ps ...Perm) map[Perm]bool {
	m := make(map[Perm]bool, len(ps))
	for _, p := range ps {
		m[p] = true
	}
	return m
}

// SetRoles 换上一份新的角色表（从库里读出来之后调）。
//
// 🔴 内置角色一律用代码里的定义**覆盖**库里的，不管库里存的是什么。
//
//	库可以被人手工改，而 admin 到底包含哪些权限是安全契约的一部分，
//	不能因为有人跑了一条 UPDATE 就变了。
//	（真改错了，VerifyBuiltins 会在启动时喊出来。）
func SetRoles(list []Role) {
	next := map[string]Role{}
	for _, r := range list {
		if r.Code == "" {
			continue
		}
		next[r.Code] = r
	}
	for code, r := range builtinRoles() {
		next[code] = r
	}
	registry.Store(next)
}

func roles() map[string]Role {
	m, _ := registry.Load().(map[string]Role)
	if m == nil {
		return builtinRoles()
	}
	return m
}

// VerifyBuiltins 校验库里种下的内置角色与代码里的定义一致。
//
// 🔴 对不上就该拒绝启动，不该只打一条日志。
//
//	分叉的表现是「界面上显示有这个权限、点了却 403」——
//	用户会以为是 bug，而实际是两处定义不同步。这种问题查起来极贵，
//	而在启动时挡住只要一秒。
func VerifyBuiltins(fromDB []Role) error {
	want := builtinRoles()
	seen := map[string]bool{}
	var bad []string
	for _, r := range fromDB {
		w, ok := want[r.Code]
		if !ok {
			continue // 自定义角色，不管
		}
		seen[r.Code] = true
		if !samePerms(w.Perms, r.Perms) {
			bad = append(bad, fmt.Sprintf(
				"%s：库里是 [%s]，代码里是 [%s]",
				r.Code, joinPerms(r.PermList()), joinPerms(w.PermList())))
		}
	}
	for code := range want {
		if !seen[code] {
			bad = append(bad, code+"：库里没有这个内置角色（迁移 016 没跑完？）")
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("内置角色的权限清单与代码不一致，拒绝启动：\n  %s",
			strings.Join(bad, "\n  "))
	}
	return nil
}

func samePerms(a, b map[Perm]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for p := range a {
		if !b[p] {
			return false
		}
	}
	return true
}

func joinPerms(ps []Perm) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return strings.Join(out, ",")
}

// AllPerms 全部权限码，顺序固定 —— 界面上的复选框按这个顺序排。
func AllPerms() []Perm {
	return []Perm{
		PermView, PermExport, PermRefresh, PermPlanWrite,
		PermOrgWrite, PermSyncTrigger, PermAlertWrite, PermAudit, PermUserAdmin,
	}
}

// PermValid 权限码是否合法。
// 🔴 存进自定义角色之前必须挡住：写错一个字（org.wirte）不会报错，
// 只是那项权限永远不生效，而界面上的复选框还是勾着的。
func PermValid(p Perm) bool {
	for _, x := range AllPerms() {
		if x == p {
			return true
		}
	}
	return false
}

// RoleOf 取一个角色的定义。第二个返回值为 false 表示这个角色码不存在。
func RoleOf(code string) (Role, bool) {
	r, ok := roles()[code]
	return r, ok
}

// AllRoles 全部角色，内置在前，同类按 code 排 —— 下拉里的顺序要稳定。
func AllRoles() []Role {
	m := roles()
	out := make([]Role, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Builtin != out[j].Builtin {
			return out[i].Builtin
		}
		if out[i].Builtin {
			// 内置的按权限从多到少 —— 这是人心里的顺序
			return len(out[i].Perms) > len(out[j].Perms)
		}
		return out[i].Code < out[j].Code
	})
	return out
}
