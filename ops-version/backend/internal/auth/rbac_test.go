package auth

import "testing"

func TestSyncTriggerNotInEditor(t *testing.T) {
	// 🔴 「手动触发同步」是不可逆的对外动作，editor 及以下绝不能有
	if Can(RoleEditor, PermSyncTrigger) || Can(RoleViewer, PermSyncTrigger) {
		t.Error("editor/viewer 不该有 sync.trigger —— 它真的往对方推镜像，推不回来")
	}
	if !Can(RoleAdmin, PermSyncTrigger) || !Can(RoleSuperAdmin, PermSyncTrigger) {
		t.Error("admin 及以上应该有 sync.trigger")
	}
}

func TestUserAdminOnlySuper(t *testing.T) {
	for _, r := range []string{RoleAdmin, RoleEditor, RoleViewer} {
		if Can(r, PermUserAdmin) {
			t.Errorf("%s 不该能管用户和角色", r)
		}
	}
	if !Can(RoleSuperAdmin, PermUserAdmin) {
		t.Error("super_admin 应该能管用户")
	}
}

func TestUnknownRoleGetsNothing(t *testing.T) {
	// 拼错角色码要彻底不能用，而不是静默降级成 viewer
	for _, p := range []Perm{PermView, PermExport, PermRefresh, PermOrgWrite} {
		if Can("adminn", p) {
			t.Errorf("未知角色不该有任何权限，却有 %s", p)
		}
	}
	if RoleValid("adminn") {
		t.Error("RoleValid 应拒绝未知角色")
	}
}

func TestNoCredentialReadPermExists(t *testing.T) {
	// 确认权限清单里根本没有「读凭据」这一项 —— 没有这项权限，就没人能被授予
	for _, r := range Roles() {
		for _, p := range PermsOf(r) {
			if string(p) == "credential.view" || string(p) == "credential.read" {
				t.Errorf("%s 竟然有读凭据的权限，凭据必须永不回显", r)
			}
		}
	}
}

func TestScopeIndependentOfRole(t *testing.T) {
	// 数据范围与角色是两个维度：admin 也可以被限定只看某些组织
	s := Scope{Role: RoleAdmin, Orgs: map[int64]bool{1: true}}
	if !s.CanSee(1) || s.CanSee(2) {
		t.Error("数据范围应独立于角色生效")
	}
	if !(Scope{Role: RoleViewer}).CanSee(999) {
		t.Error("Orgs 为 nil 应表示不限")
	}
}
