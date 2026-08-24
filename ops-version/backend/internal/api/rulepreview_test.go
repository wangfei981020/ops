package api

import "testing"

// 用 的真实案例钉住：`*--game-server-backend` 多打一个连字符，
// 语法合法但永远不命中 —— 这正是「命中数」这条反馈存在的理由。
func TestCheckPattern_TypoIsSyntacticallyValid(t *testing.T) {
	if got := checkPattern("*--game-server-backend"); got != "" {
		t.Fatalf("这个笔误在语法上是合法的，checkPattern 不该报错，却返回 %q\n"+
			"—— 它只能靠「命中 0 个」发现，语法检查拦不住", got)
	}
	if checkPattern("a*b") == "" {
		t.Fatal("* 在中间应当被判为语法错误")
	}
	if checkPattern("*") != "" {
		t.Fatal("纯 * 是合法的（匹配全部）")
	}
	if checkPattern("  ") == "" {
		t.Fatal("空规则应当被指出")
	}
}
