package providers

import "testing"

// workload 规则：留空放行全部，否则按 include/exclude 判
func TestWorkloadRules(t *testing.T) {
	cases := []struct {
		name string
		r    WorkloadRules
		in   string
		want bool
	}{
		{"空规则放行", WorkloadRules{}, "wallet-backend", true},
		{"精确包含", WorkloadRules{Include: []string{"wallet-backend"}}, "wallet-backend", true},
		{"不在包含里", WorkloadRules{Include: []string{"wallet-backend"}}, "risk-backend", false},
		{"前缀包含", WorkloadRules{Include: []string{"wallet-*"}}, "wallet-client-backend", true},
		{"排除优先于包含", WorkloadRules{
			Include: []string{"wallet-*"}, Exclude: []string{"wallet-debug"},
		}, "wallet-debug", false},
		{"只配排除时其余放行", WorkloadRules{Exclude: []string{"*-canary"}}, "wallet-backend", true},
		{"只配排除时命中的挡掉", WorkloadRules{Exclude: []string{"*-canary"}}, "wallet-canary", false},
		{"空白被忽略", WorkloadRules{Include: []string{"  wallet-backend  "}}, "wallet-backend", true},
	}
	for _, c := range cases {
		if got := c.r.Match(c.in); got != c.want {
			t.Errorf("%s: Match(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// 🔴 通配写法：原来只支持 `app-*`，`*-canary` 写了却静默不生效。
// 配置类的东西最怕这个 —— 保存成功、界面上规则就在那儿，但它从不匹配。
func TestMatchPatternForms(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"app-*", "app-uat", true},
		{"app-*", "prod-appA", false},
		{"*-canary", "wallet-canary", true},
		{"*-canary", "canary-wallet", false},
		{"*wallet*", "my-wallet-backend", true},
		{"*wallet*", "risk-backend", false},
		{"*", "anything", true},
		{"wallet", "wallet", true},
		{"wallet", "wallet-backend", false},
		{"", "anything", false},
		{"  app-*  ", "app-uat", true},
	}
	for _, c := range cases {
		if got := matchPattern(c.pat, c.s); got != c.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", c.pat, c.s, got, c.want)
		}
	}
}
