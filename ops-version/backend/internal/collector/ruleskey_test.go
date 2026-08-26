package collector

import (
	"testing"

	"ops-version-backend/providers"
)

// 规则逐字相同才复用拉取结果。
// 实测过 A公司 的 项目B 与 项目C 规则完全一致（ns 都是 app-uat、无 workload 规则），
// 每轮却各拉一次，对方 Rancher 被打三遍、其中两遍结果一模一样。
func TestRulesKeyReusesOnlyWhenIdentical(t *testing.T) {
	inbar := providers.Rules{NS: providers.NSRules{Include: []string{"app-uat"}}}
	dtw := providers.Rules{NS: providers.NSRules{Include: []string{"app-uat"}}}
	if rulesKey(inbar) != rulesKey(dtw) {
		t.Fatal("规则完全相同的两个项目应当复用同一次拉取")
	}

	// 项目A 有排除规则 —— 结果集不同，绝不能复用
	appA := providers.Rules{
		NS:       providers.NSRules{Include: []string{"app-uat"}},
		Workload: providers.WorkloadRules{Exclude: []string{"*-game-server-backend"}},
	}
	if rulesKey(inbar) == rulesKey(appA) {
		t.Fatal("🔴 规则不同却算出同一个 key —— 会把一个项目的服务集合写进另一个项目的快照")
	}
}

// 每一个规则维度都必须进 key，漏一维就是静默的数据错乱
func TestRulesKeyCoversEveryDimension(t *testing.T) {
	base := providers.Rules{}
	cases := map[string]providers.Rules{
		"ns.include":       {NS: providers.NSRules{Include: []string{"a"}}},
		"ns.exclude":       {NS: providers.NSRules{Exclude: []string{"a"}}},
		"workload.include": {Workload: providers.WorkloadRules{Include: []string{"a"}}},
		"workload.exclude": {Workload: providers.WorkloadRules{Exclude: []string{"a"}}},
	}
	for name, r := range cases {
		if rulesKey(base) == rulesKey(r) {
			t.Fatalf("%s 没有进 key —— 两套不同规则会被当成同一套而复用结果", name)
		}
	}
}

// 不同维度之间不能互相"串味"：ns.include=[a] 与 workload.include=[a] 必须不同
func TestRulesKeyDoesNotConfuseDimensions(t *testing.T) {
	a := providers.Rules{NS: providers.NSRules{Include: []string{"x"}}}
	b := providers.Rules{Workload: providers.WorkloadRules{Include: []string{"x"}}}
	if rulesKey(a) == rulesKey(b) {
		t.Fatal("不同维度的同一个值算出了同一个 key")
	}
}
