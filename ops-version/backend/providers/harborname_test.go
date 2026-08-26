package providers

import "testing"

// TestCleanHarborNameShapes Harbor 各版本 / 各复制粒度写 resource 的形态都不一样。
//
// 🔴 这些形态直接决定「镜像同步」能不能归因：解析出的 tag 要和对账用的真实 tag
// 逐字相同，差一个方括号就永远匹配不上，而表现和「Harbor 根本没记 tag」
// 一模一样 —— 都是归因失效，成因却完全不同，排查方向也完全不同。
func TestCleanHarborNameShapes(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// 单个 artifact：Harbor 把 tag 包在方括号里
		{"appA/bi-central-backend:[20260824083840-15f85908-223]", "appA/bi-central-backend:20260824083840-15f85908-223"},
		// 多个 artifact：只写数量，不写 tag（生产 143 条复制记录全是这种）
		{"appA/bi-central-backend [1 item(s) in total]", "appA/bi-central-backend"},
		// 普通镜像串
		{"appA/bi-central-backend:20260824083840-15f85908-223", "appA/bi-central-backend:20260824083840-15f85908-223"},
		{"appA/bi-central-backend", "appA/bi-central-backend"},
		// ⚠️ 多 tag 时剥出来是没意义的串，整段丢掉 —— 宁可"不知道"也不编一个
		{"appA/bi-central-backend:[v1 ... ]", "appA/bi-central-backend"},
		{"appA/bi-central-backend:[v1,v2]", "appA/bi-central-backend"},
	} {
		if got := CleanHarborName(c.in); got != c.want {
			t.Errorf("CleanHarborName(%q)\n = %q\n 要 %q", c.in, got, c.want)
		}
	}
}
