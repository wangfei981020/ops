package compare

import "testing"

func planOf(ign IgnoreSet) Plan {
	base := Column{OrgID: 1, OrgName: "我方", Env: "UAT", SyncStatus: "success"}
	c2 := Column{OrgID: 2, OrgName: "印尼", Env: "UAT", SyncStatus: "success"}
	c3 := Column{OrgID: 3, OrgName: "马来", Env: "UAT", SyncStatus: "success"}
	return Plan{Columns: []Column{base, c2, c3}, Ignores: ign}
}

// 🔴 单元格忽略**只影响那一格**，同一行其他列照常判定。
// 这是用户明确要的语义："印尼不跑 wallet" 不该让"马来 vs 我方 的 wallet 差异"也消失。
func TestIgnoreCellDoesNotAffectOtherColumns(t *testing.T) {
	// ⚠️ 列标识是 StableKey：orgID/projectID/env。中间那段是项目 ——
	// 同一平台同一环境下的两个项目是两列，共用标识会让忽略规则串到隔壁项目去。
	p := planOf(IgnoreSet{Cells: map[string][]string{"wallet": {"2/0/UAT"}}})
	data := map[string][]Snapshot{
		"我方/UAT": {snap("wallet", "v2", 2, true)},
		"印尼/UAT": {snap("wallet", "v1", 1, true)}, // 被忽略
		"马来/UAT": {snap("wallet", "v1", 1, true)}, // 应当照常判成落后
	}
	res := Compare(p, data)
	if len(res.Rows) != 1 {
		t.Fatalf("行数 = %d，要 1", len(res.Rows))
	}
	cells := res.Rows[0].Cells
	if cells[1].State != CellIgnored {
		t.Errorf("印尼列应为 ignored，实得 %s", cells[1].State)
	}
	if cells[2].State != CellVersion {
		t.Errorf("马来列应照常参与比对，实得 %s —— 忽略串到别的列了", cells[2].State)
	}
	// 🔴 忽略的格子**不参与行结论**，但其余列的差异照常算出来。
	//    "印尼不跑 wallet" 不该让 "我方 vs 马来的 wallet 差异" 也跟着消失。
	if got := res.Rows[0].Verdict; got != VerdictDiff {
		t.Errorf("行结论 = %s，我方(v2) vs 马来(v1) 应为 diff（忽略印尼不影响它们）", got)
	}
	if !res.Rows[0].HasDiff {
		t.Error("马来和我方版本不同，这一行应当算有差异")
	}
	// 🔴 逐格忽略在 Summary（按行统计）里是看不见的 —— 这一行的结论是 diff。
	//    所以必须有独立的计数，否则"忽略了什么"就藏起来了。
	if res.IgnoredCells != 1 {
		t.Errorf("IgnoredCells = %d，要 1 —— 忽略必须看得见", res.IgnoredCells)
	}
}

// 整行忽略：不进 Rows，但名字要报出来
func TestIgnoreRowReportsNames(t *testing.T) {
	p := planOf(IgnoreSet{Services: []string{"wallet", "bi-*"}})
	data := map[string][]Snapshot{
		"我方/UAT": {snap("wallet", "v2", 2, true), snap("bi-report", "v1", 1, true), snap("order", "v3", 3, true)},
		"印尼/UAT": {snap("order", "v3", 3, true)},
		"马来/UAT": {snap("order", "v3", 3, true)},
	}
	res := Compare(p, data)
	if len(res.Rows) != 1 || res.Rows[0].ServiceKey != "order" {
		t.Fatalf("只该剩 order，实得 %d 行", len(res.Rows))
	}
	// 🔴 名字必须给出来 —— 只给数字的话人没法确认自己排除了什么
	if len(res.IgnoredRows) != 2 {
		t.Fatalf("IgnoredRows = %v，要 2 个", res.IgnoredRows)
	}
	if res.IgnoredRows[0] != "bi-report" || res.IgnoredRows[1] != "wallet" {
		t.Errorf("IgnoredRows = %v，要 [bi-report wallet]（含通配命中的）", res.IgnoredRows)
	}
}

// 忽略优先于"采集失败" —— 人为决定不比的东西，不该显示成需要处理的 no_data
func TestIgnoreBeatsNoData(t *testing.T) {
	p := planOf(IgnoreSet{Cells: map[string][]string{"wallet": {"2/0/UAT"}}})
	p.Columns[1].SyncStatus = "auth_failed" // 印尼列采集失败
	data := map[string][]Snapshot{"我方/UAT": {snap("wallet", "v2", 2, true)}, "马来/UAT": {snap("wallet", "v2", 2, true)}}
	res := Compare(p, data)
	if got := res.Rows[0].Cells[1].State; got != CellIgnored {
		t.Errorf("忽略应优先于 no_data，实得 %s —— 会让人去查一个不需要处理的采集失败", got)
	}
}

// 🔴 没有基准之后，**任何列都可以被忽略**。
//
// 原来第一列（基准）不许忽略，理由是"忽略了基准整行就没有参照物"。
// 现在判定是横着比这几列彼此，没有参照物这回事 —— 那条限制跟着删掉。
func TestAnyColumnCanBeIgnored(t *testing.T) {
	p := planOf(IgnoreSet{Cells: map[string][]string{"wallet": {"1/0/UAT"}}})
	data := map[string][]Snapshot{
		"我方/UAT": {snap("wallet", "v2", 2, true)}, "印尼/UAT": {snap("wallet", "v1", 1, true)}, "马来/UAT": {snap("wallet", "v2", 2, true)},
	}
	res := Compare(p, data)
	if got := res.Rows[0].Cells[0].State; got != CellIgnored {
		t.Errorf("第一列 = %s，要 ignored —— 没有基准了，哪一列都能忽略", got)
	}
	// 剩下 印尼(v1) vs 马来(v2) 仍要比出差异
	if got := res.Rows[0].Verdict; got != VerdictDiff {
		t.Errorf("行结论 = %s，剩余两列 tag 不同应为 diff", got)
	}
}

func TestIgnoreSetHelpers(t *testing.T) {
	s := IgnoreSet{Services: []string{"a", "bi-*"}, Cells: map[string][]string{"c": {"2/0/UAT"}}}
	if !s.IgnoredRow("bi-report") || s.IgnoredRow("other") {
		t.Error("整行通配匹配不对")
	}
	// ⚠️ 整行忽略时 IgnoredCell 返回 false —— 两者分工不同，
	//    混在一起会让"已忽略格子数"把整行的也算进去
	if s.IgnoredCell("a", "2/0/UAT") {
		t.Error("整行忽略不该同时算作单元格忽略")
	}
	if !s.IgnoredCell("c", "2/0/UAT") || s.IgnoredCell("c", "3/0/UAT") {
		t.Error("单元格匹配不对")
	}
	if (IgnoreSet{}).IsEmpty() != true {
		t.Error("空集合判定不对")
	}
}
