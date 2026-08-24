package auth

import "testing"

// 造两个**互不包含**的自定义角色 —— 这正是内置四个角色没有的形态
func withCustomRoles(t *testing.T) {
	t.Helper()
	SetRoles([]Role{
		{Code: "notify-only", Name: "只管通知",
			Perms: permSet(PermView, PermAlertWrite)},
		{Code: "exporter", Name: "只读导出",
			Perms: permSet(PermView, PermExport)},
	})
	t.Cleanup(func() { SetRoles(nil) })
}

// 🔴 回归：一个人同时命中两个互不包含的角色时，**必须报出丢了什么**。
//
// 老实现是「按等级取最高」，而自定义角色之间没有等级 ——
// 静默取一个的结果是悄悄少给权限，表现为「他某天突然点不动某个按钮」。
func TestResolveRoleReportsDroppedPerms(t *testing.T) {
	withCustomRoles(t)
	res := ResolveRole([]string{"g1", "g2"}, []RoleRule{
		{GroupValue: "g1", RoleCode: "notify-only"},
		{GroupValue: "g2", RoleCode: "exporter"},
	})
	if len(res.Candidates) != 2 {
		t.Fatalf("候选角色 = %v，要两个", res.Candidates)
	}
	if len(res.Dropped) == 0 {
		t.Fatal("两个互不包含的角色，必然有权限表达不出来 —— Dropped 不能为空，" +
			"否则就是悄悄少给权限")
	}
	// 选中的那个之外的权限必须被点名
	r, _ := RoleOf(res.Role)
	for _, p := range res.Dropped {
		if r.Perms[p] {
			t.Errorf("%s 已经在选中角色里了，不该报成 dropped", p)
		}
	}
}

// 包含关系时不该报 dropped —— 内置四个角色走的就是这条，不能平白喊狼来了
func TestResolveRoleNoDropWhenCovered(t *testing.T) {
	res := ResolveRole([]string{"a", "b"}, []RoleRule{
		{GroupValue: "a", RoleCode: RoleAdmin},
		{GroupValue: "b", RoleCode: RoleViewer},
	})
	if res.Role != RoleAdmin {
		t.Errorf("角色 = %q，要 admin（它覆盖 viewer）", res.Role)
	}
	if len(res.Dropped) > 0 {
		t.Errorf("admin 覆盖 viewer，不该有 dropped，实得 %v", res.Dropped)
	}
}

// 结果必须可复现 —— 不能取决于 map 遍历顺序，
// 否则同一个人每次登录拿到的角色可能不同，而这种 bug 几乎无法复现
func TestResolveRoleDeterministic(t *testing.T) {
	withCustomRoles(t)
	rules := []RoleRule{
		{GroupValue: "g1", RoleCode: "notify-only"},
		{GroupValue: "g2", RoleCode: "exporter"},
	}
	first := ResolveRole([]string{"g1", "g2"}, rules).Role
	for i := 0; i < 20; i++ {
		if got := ResolveRole([]string{"g1", "g2"}, rules).Role; got != first {
			t.Fatalf("第 %d 次得到 %q，第一次是 %q —— 结果不可复现", i, got, first)
		}
	}
}

// 内置角色的权限**不受库里数据影响** —— 安全契约不能被一条 UPDATE 改掉
func TestBuiltinRolesCannotBeOverridden(t *testing.T) {
	SetRoles([]Role{{Code: RoleViewer, Name: "被人改过的", Perms: permSet(PermUserAdmin)}})
	t.Cleanup(func() { SetRoles(nil) })
	if Can(RoleViewer, PermUserAdmin) {
		t.Error("库里把 viewer 改成有 user.admin 了，代码必须覆盖它 —— 否则改库即提权")
	}
	if !Can(RoleViewer, PermView) {
		t.Error("viewer 的原有权限丢了")
	}
}

// 自定义角色不存在时 = 零权限，不能回落成 viewer
func TestUnknownRoleHasNoPerms(t *testing.T) {
	if RoleValid("nope") {
		t.Error("不存在的角色码不该合法")
	}
	if len(PermsOf("nope")) != 0 {
		t.Error("不存在的角色必须零权限 —— 静默降级成只读会让人查半天")
	}
}

// 启动校验：库里的内置角色与代码对不上就该拒绝启动
func TestVerifyBuiltinsCatchesDrift(t *testing.T) {
	drifted := []Role{
		{Code: RoleViewer, Perms: permSet(PermView)}, // 少了 export
		{Code: RoleEditor, Perms: permSet(PermView, PermExport, PermRefresh, PermPlanWrite)},
		{Code: RoleAdmin, Perms: permSet(PermView, PermExport, PermRefresh, PermPlanWrite,
			PermOrgWrite, PermAlertWrite, PermAudit, PermSyncTrigger)},
		{Code: RoleSuperAdmin, Perms: permSet(PermView, PermExport, PermRefresh, PermPlanWrite,
			PermOrgWrite, PermAlertWrite, PermAudit, PermSyncTrigger, PermUserAdmin)},
	}
	if err := VerifyBuiltins(drifted); err == nil {
		t.Error("viewer 少了一项权限，必须报错拒绝启动 —— " +
			"否则表现是「界面显示有这个权限、点了却 403」")
	}
}

func TestVerifyBuiltinsPassesWhenAligned(t *testing.T) {
	var list []Role
	for _, r := range builtinRoles() {
		list = append(list, r)
	}
	if err := VerifyBuiltins(list); err != nil {
		t.Errorf("一致却报错了：%v", err)
	}
}
