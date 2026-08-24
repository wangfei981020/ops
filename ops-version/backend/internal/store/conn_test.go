package store

import "testing"

// 连接信息三层优先级：环境级 > 平台级 > 数据源。
//
// 🔴 优先级搞反的代价：改了数据源的密码却不生效，
// 而界面上数据源那份明明是新的 —— 人会反复改一个根本没被读到的字段。
// ConnSource() 就是为了让这种情况能一眼看出来。
func TestConnPriority(t *testing.T) {
	ds := Org{
		DSEndpoint: "https://ds", DSAuthType: "token", DSCredentialEnc: "DS",
	}
	orgOwn := ds
	orgOwn.Endpoint, orgOwn.AuthType, orgOwn.CredentialEnc = "https://org", "api_key", "ORG"

	cases := []struct {
		name       string
		env        OrgEnv
		org        Org
		wantEP     string
		wantCred   string
		wantSource string
	}{
		{"三层都有 → 环境级赢",
			OrgEnv{Endpoint: "https://env", AuthType: "password", CredentialEnc: "ENV"},
			orgOwn, "https://env", "ENV", "env"},
		{"环境级空 → 平台级",
			OrgEnv{}, orgOwn, "https://org", "ORG", "org"},
		{"环境级与平台级都空 → 数据源",
			OrgEnv{}, ds, "https://ds", "DS", "datasource"},
		// ⚠️ 迁移期：平台自己还留着老连接信息，同时也绑了数据源。
		//    此时必须**以平台自己的为准**，否则升级瞬间所有平台的连接都变了
		{"迁移期两者都有 → 平台级（行为与升级前一致）",
			OrgEnv{}, orgOwn, "https://org", "ORG", "org"},
		{"什么都没有", OrgEnv{}, Org{}, "", "", "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ep, _, cred := c.env.Conn(c.org)
			if ep != c.wantEP {
				t.Errorf("endpoint = %q，要 %q", ep, c.wantEP)
			}
			if cred != c.wantCred {
				t.Errorf("凭据 = %q，要 %q —— 用错了一层", cred, c.wantCred)
			}
			if got := c.env.ConnSource(c.org); got != c.wantSource {
				t.Errorf("来源 = %q，要 %q", got, c.wantSource)
			}
		})
	}
}

// 🔴 环境级是**整组覆盖**：填了地址就必须连同认证方式和凭据一起用，
// 不能逐字段回落 —— 那会造出「A 的地址配 B 的密码」这种必坏组合。
func TestEnvConnIsAllOrNothing(t *testing.T) {
	org := Org{Endpoint: "https://org", AuthType: "api_key", CredentialEnc: "ORG"}
	env := OrgEnv{Endpoint: "https://env"} // 只填地址，认证方式和凭据都空
	ep, at, cred := env.Conn(org)
	if ep != "https://env" {
		t.Fatalf("endpoint = %q", ep)
	}
	if at != "" || cred != "" {
		t.Errorf("认证方式=%q 凭据=%q —— 回落到平台级了，会造出「A 的地址配 B 的密码」", at, cred)
	}
}
