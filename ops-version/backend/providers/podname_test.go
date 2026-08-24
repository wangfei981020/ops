package providers

import "testing"

// 从 Pod 名推 workload 名。
//
// 🔴 剥过头的代价很实在：`foo-v2` 被截成 `foo` 之后，
// 两个不同服务会合并成一条，而合并后的版本取决于遍历顺序 —— 静默错。
// 所以判据宁可保守：只剥**明显是生成的**后缀。
func TestWorkloadNameOfPod(t *testing.T) {
	cases := []struct{ pod, want string }{
		// Deployment：<name>-<rs哈希>-<随机5位>
		{"wallet-backend-7d4f8b9c6d-x2k9p", "wallet-backend"},
		{"biz-baccarat-h5-c-game-frontend-74cdc4bf6-qs2b6", "biz-baccarat-h5-c-game-frontend"},
		{"argocd-server-6b569c949c-whjcq", "argocd-server"},
		// StatefulSet：<name>-<序号>
		{"mysql-0", "mysql"},
		{"argocd-application-controller-0", "argocd-application-controller"},
		{"kafka-12", "kafka"},
		// 🔴 不该剥的：末段不是生成的后缀
		{"wallet-v2", "wallet-v2"},
		{"api-gateway", "api-gateway"},
		{"foo-bar-baz", "foo-bar-baz"},
		// 单段名字
		{"nginx", "nginx"},
		// 末段像随机串但中间那段不像哈希 → 不剥
		{"my-app-x2k9p", "my-app-x2k9p"},
	}
	for _, c := range cases {
		t.Run(c.pod, func(t *testing.T) {
			if got := workloadNameOfPod(c.pod); got != c.want {
				t.Errorf("workloadNameOfPod(%q) = %q，要 %q", c.pod, got, c.want)
			}
		})
	}
}
