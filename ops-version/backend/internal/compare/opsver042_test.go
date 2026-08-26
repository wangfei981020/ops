package compare

import "testing"

// 被本列采集规则排掉的服务，绝不能显示成「该平台未部署此服务」。
// 实测过 22 个 healthy 的服务中过这一枪，而那句话会随导出的 Excel 发给对方公司。
func TestExcludedServiceIsNotReportedAsMissing(t *testing.T) {
	self := Column{
		OrgID: 1, OrgName: "我方", Env: "UAT", IsSelf: true, SyncStatus: "success",
		// 我方把这批服务排除了 —— 它们不进快照
		ExcludeRules: []string{"*-game-server-backend"},
	}
	other := Column{OrgID: 2, OrgName: "A公司", Env: "UAT", SyncStatus: "success"}

	plan := Plan{Columns: []Column{self, other}}
	// 只有对方那一列有这个服务（非对称配置，正是生产的情形）
	data := map[string][]Snapshot{
		self.Key():  {},
		other.Key(): {{ServiceKey: "maxwin24d-game-server-backend", Tag: "20260803082445-29", IsVersioned: true}},
	}

	res := Compare(plan, data)
	if len(res.Rows) != 1 {
		t.Fatalf("应当有 1 行，实际 %d", len(res.Rows))
	}
	cell := res.Rows[0].Cells[0]
	if cell.State == CellMissing {
		t.Fatalf("被规则排除的服务被判成了 missing，note=%q —— 这正是 ", cell.Note)
	}
	if cell.State != CellIgnored {
		t.Fatalf("期望 CellIgnored，实际 %q", cell.State)
	}
	if cell.Note == "该平台未部署此服务" {
		t.Fatal("文案仍然在断言「未部署」")
	}
	t.Logf("✅ state=%s note=%q", cell.State, cell.Note)
}

// 反向：没有规则时，确实缺失的服务仍要判 missing —— 别把这条修过头。
func TestGenuinelyMissingStillReportsMissing(t *testing.T) {
	self := Column{OrgID: 1, OrgName: "我方", Env: "UAT", IsSelf: true, SyncStatus: "success"}
	other := Column{OrgID: 2, OrgName: "A公司", Env: "UAT", SyncStatus: "success"}
	plan := Plan{Columns: []Column{self, other}}
	data := map[string][]Snapshot{
		self.Key():  {},
		other.Key(): {{ServiceKey: "wallet-backend", Tag: "v1", IsVersioned: true}},
	}
	res := Compare(plan, data)
	if got := res.Rows[0].Cells[0].State; got != CellMissing {
		t.Fatalf("没有排除规则时应当仍判 missing，实际 %q", got)
	}
}

// 降级采集的列上，「查不到」只能说"看不见"，不能说"对方没部署"。
// 副本缩到 0 的服务在 Pod 层没有任何 Pod —— 而降级路径正是从 Pod 反推的。
func TestDegradedColumnDowngradesMissingToNoData(t *testing.T) {
	self := Column{OrgID: 1, OrgName: "我方", Env: "UAT", IsSelf: true, SyncStatus: "success"}
	// A公司 长期处于降级状态（读不到 deployments）
	pa := Column{
		OrgID: 2, OrgName: "A公司", Env: "UAT", SyncStatus: "success",
		Degraded: true, DegradedNote: "降级采集：读不到 deployments",
	}
	plan := Plan{Columns: []Column{self, pa}}
	data := map[string][]Snapshot{
		self.Key(): {{ServiceKey: "wallet-backend", Tag: "v9", IsVersioned: true}},
		pa.Key():   {}, // 副本 0 → Pod 层看不见 → 采不到
	}
	res := Compare(plan, data)
	cell := res.Rows[0].Cells[1]
	if cell.State == CellMissing {
		t.Fatalf("降级列不该判 missing（那是在说「对方没部署」），note=%q", cell.Note)
	}
	if cell.State != CellNoData {
		t.Fatalf("期望降格为 CellNoData，实际 %q", cell.State)
	}
	// 行判定应当是"要去查"而不是"已查实缺失"
	if res.Rows[0].Verdict == VerdictMissing {
		t.Fatal("行判定仍是 missing —— 降格没有传到行这一层")
	}
	t.Logf("✅ state=%s verdict=%s note=%q", cell.State, res.Rows[0].Verdict, cell.Note)
}

// 有「被排除清单」这个事实时，必须用它，不能再拿规则反推。
//
// 生产/本地的真实形状：helm 把 release 名拼进 workload 名，于是
//
//	规则 另一个产品-*  →  匹配 workload `另一个产品-plane-backend`（真的被排掉了）
//	                 →  匹配不上 workload `opsalert-另一个产品-backend`
//
// 而两者的 ServiceKey 分别是 另一个产品-plane-backend 和 另一个产品-backend，
// **都**匹配 另一个产品-* —— 拿 ServiceKey 反推会把没被排掉的那个也算进去。
func TestExcludedKeysBeatRuleGuessing(t *testing.T) {
	col := Column{
		OrgID: 1, OrgName: "我方", Env: "UAT", SyncStatus: "success",
		ExcludeRules: []string{"demo-svc-*"},
		// 事实：只有这一个真的被排掉了（它的 workload 名匹配上了规则）
		ExcludedKeys: map[string]string{"demo-svc-plane-backend": "demo-svc-plane-backend"},
	}
	if !col.ExcludedByRule("demo-svc-plane-backend") {
		t.Fatal("真正被排掉的服务应当判为已排除")
	}
	// 🔴 关键断言：这个服务 ServiceKey 匹配规则，但它的 workload 名没匹配上，
	//    实际一直在正常采集 —— 不能因为"规则看起来匹配"就说它被排除了
	if col.ExcludedByRule("demo-svc-backend") {
		t.Fatal("🔴 没被排掉的服务被误判成已排除 —— 说明仍在拿规则反推而不是用事实")
	}
}

// 过渡路径：还没用新版采集器采过一轮的列（ExcludedKeys 为空）仍走规则反推，
// 否则升级后、重新采集前，会整个失效。
func TestFallsBackToRulesWhenNoFactsYet(t *testing.T) {
	col := Column{ExcludeRules: []string{"demo-svc-*"}} // ExcludedKeys 为 nil
	if !col.ExcludedByRule("demo-svc-backend") {
		t.Fatal("没有事实清单时应当回落到规则反推")
	}
}
