package auth

import "testing"

func TestResolveRole(t *testing.T) {
	rules := []RoleRule{
		{"ops-admin", RoleAdmin},
		{"ops-*", RoleViewer},
		{"dev-team", RoleEditor},
		{"typo-group", "admln"}, // 故意写错的 role_code
	}
	cases := []struct {
		name     string
		groups   []string
		wantRole string
		wantHits int
	}{
		{"单条命中", []string{"dev-team"}, RoleEditor, 1},
		// 🔴 同时命中 ops-admin(admin) 与 ops-*(viewer)，必须取**高**的
		//    取第一条命中的话，结果取决于表顺序，加条映射就悄悄改别人权限
		{"多条命中取最高", []string{"ops-admin"}, RoleAdmin, 1},
		{"多个组取最高", []string{"ops-other", "dev-team"}, RoleEditor, 2},
		{"大小写不敏感", []string{"DEV-Team"}, RoleEditor, 1},
		{"一条都没命中", []string{"unknown"}, "", 0},
		{"空组", nil, "", 0},
		{"空字符串组不算命中", []string{"", "  "}, "", 0},
		// 写错的 role_code 要跳过 —— 不能静默降权成 viewer
		{"未知角色码跳过", []string{"typo-group"}, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := ResolveRole(c.groups, rules)
			role, hits := res.Role, res.Matched
			if role != c.wantRole {
				t.Errorf("角色 = %q，要 %q", role, c.wantRole)
			}
			if len(hits) != c.wantHits {
				t.Errorf("命中组 = %v（%d 个），要 %d 个", hits, len(hits), c.wantHits)
			}
		})
	}
}

// `*` 通配必须能匹配所有组 —— 这是"所有登录用户给个只读"的常见配法。
func TestResolveRoleWildcardAll(t *testing.T) {
	role := ResolveRole([]string{"anything"}, []RoleRule{{"*", RoleViewer}}).Role
	if role != RoleViewer {
		t.Errorf("通配 * 应命中，实得 %q", role)
	}
}
