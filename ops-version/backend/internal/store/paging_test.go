package store

import "testing"

// 🔴 超上限**钳制**，不掉回默认值。
//
// 这个坏写法在四个列表接口里各写了一遍（审计 / 执行记录 / 变更历史 / 通知记录），
// 表现是「传 501 比传 500 拿得更少」，而且没有任何提示。
// 抽成公用函数之后，这条测试同时管着这四处。
func TestClampLimit(t *testing.T) {
	cases := []struct {
		in, want int
		why      string
	}{
		{0, DefaultPageLimit, "没指定 → 默认值"},
		{-1, DefaultPageLimit, "负数 → 默认值"},
		{50, 50, "范围内原样返回"},
		{MaxPageLimit, MaxPageLimit, "正好上限"},
		{MaxPageLimit + 1, MaxPageLimit, "🔴 超上限钳制到上限，不能掉回默认值"},
		{99999, MaxPageLimit, "要多少都只给上限"},
	}
	for _, c := range cases {
		if got := ClampLimit(c.in); got != c.want {
			t.Errorf("ClampLimit(%d) = %d，要 %d —— %s", c.in, got, c.want, c.why)
		}
	}
	// 回归：501 拿到的不能比 500 少
	if ClampLimit(MaxPageLimit+1) < ClampLimit(MaxPageLimit) {
		t.Error("传 501 比传 500 拿得更少 —— 这是任何人都想不到的行为")
	}
}
