package api

import (
	"testing"

	"ops-version-backend/internal/store"
)

// ⚠️ 顺序照抄 ListProjects 的排序（sort_order, name）：
// 「默认」**不在第一个**。真实数据里就是这样 —— 后建的项目按名字排到了前面，
// 而这正是下面那条回归测试要防的 bug。
func projs() []store.Project {
	return []store.Project{
		{ID: 8, Name: "平台组", Enabled: true},
		{ID: 7, Name: "默认", Enabled: true},
		{ID: 9, Name: "已停", Enabled: false},
	}
}

// 🔴 回归：老方案里没有 project_id，必须回落到**这个环境自己挂的项目**。
//
// 实测栽过：回落写成「列表第一个」，于是一个 17 个服务的老方案打开后只剩 4 个 ——
// 被排在最前面的「平台组」的通配规则筛过了，而界面上没有任何异常提示。
func TestResolveProjectLegacyPlanUsesEnvProject(t *testing.T) {
	p, ok := resolveProject(projs(), 0, 7)
	if !ok || p.ID != 7 {
		t.Fatalf("want=0 时应取环境挂的项目 7，实得 %+v ok=%v —— 老方案会被别的项目的规则筛过", p, ok)
	}
}

// 环境也没挂项目（更老的数据）：只能取第一个启用的，但不能报错
func TestResolveProjectNoEnvProject(t *testing.T) {
	if p, ok := resolveProject(projs(), 0, 0); !ok || p.ID != 8 {
		t.Errorf("都没有时取第一个启用的，实得 %+v ok=%v", p, ok)
	}
}

// 请求显式指定的优先级最高，压过环境的归属
func TestResolveProjectRequestBeatsEnv(t *testing.T) {
	if p, _ := resolveProject(projs(), 8, 7); p.ID != 8 {
		t.Errorf("请求指定 8 应压过环境的 7，实得 %+v", p)
	}
}

// 方案引用了一个已删/已停的项目：回落，不是让整次比对失败
func TestResolveProjectMissingFallsBack(t *testing.T) {
	if p, ok := resolveProject(projs(), 999, 7); !ok || p.ID != 7 {
		t.Errorf("引用不存在的项目应回落到环境挂的那个，实得 %+v ok=%v", p, ok)
	}
	if p, ok := resolveProject(projs(), 9, 7); !ok || p.ID != 7 {
		t.Errorf("引用已停用的项目应回落到环境挂的那个，实得 %+v ok=%v", p, ok)
	}
}

func TestResolveProjectExplicit(t *testing.T) {
	if p, _ := resolveProject(projs(), 8, 0); p.ID != 8 || p.Name != "平台组" {
		t.Errorf("显式指定的项目要原样返回，实得 %+v", p)
	}
}

// 一个项目都没有的平台（迁移前建的、还没建默认项目）不能崩，也不能假装有
func TestResolveProjectNone(t *testing.T) {
	if _, ok := resolveProject(nil, 0, 0); ok {
		t.Error("没有任何项目时应返回 false，让调用方按「不分项目」处理")
	}
}

// 单项目平台不显示项目名 —— 「A平台·默认/UAT」是纯噪音
func TestCountEnabledProjects(t *testing.T) {
	if n := countEnabledProjects(projs()); n != 2 {
		t.Errorf("启用项目数 = %d，要 2（停用的不算）", n)
	}
	if n := countEnabledProjects([]store.Project{{ID: 1, Enabled: true}}); n != 1 {
		t.Errorf("单项目 = %d，要 1", n)
	}
}
