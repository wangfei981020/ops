package export

import (
	"strings"
	"testing"
	"time"

	"ops-version-backend/internal/compare"
)

// notesText 把「数据说明」页拍平成一段文本，方便断言里面写没写某句话。
func notesText(t *testing.T, in Input) string {
	t.Helper()
	blob, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	f := open(t, blob)
	rows, err := f.GetRows("数据说明")
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, r := range rows {
		sb.WriteString(strings.Join(r, " "))
		sb.WriteString("\n")
	}
	return sb.String()
}

// 🔴 被忽略的服务必须在「数据说明」页点名。
//
// 忽略掉的行不出现在表里 —— 不写出来的话，收到附件的人无从分辨
// 「这个服务比过了没差异」和「这个服务压根没比」。
func TestNotesListsIgnoredRows(t *testing.T) {
	base := compare.Column{OrgID: 1, OrgName: "我方", Env: "UAT", SyncStatus: "success"}
	other := compare.Column{OrgID: 2, OrgName: "印尼", Env: "UAT", SyncStatus: "success"}
	in := Input{
		Plan:   compare.Plan{Columns: []compare.Column{base, other}},
		Result: compare.Result{IgnoredRows: []string{"bi-report", "wallet"}},
		Now:    time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
	}
	got := notesText(t, in)
	for _, want := range []string{"已忽略 2 个服务", "bi-report", "wallet"} {
		if !strings.Contains(got, want) {
			t.Errorf("数据说明里缺 %q —— 收到附件的人看不出这些服务没参与比对\n实得：%s", want, got)
		}
	}
}

// 没有忽略时不要平白多一行 —— 空提示会让人以为出了什么事
//
// ⚠️ 拦的是那两条**动态提示**（都带 ⚠️ 前缀），不是判定口径表里
// 常驻的「已忽略」词条 —— 那一条无论这次有没有忽略都该在，
// 收表的人得知道这个词是什么意思。
func TestNotesNoIgnoreLineWhenEmpty(t *testing.T) {
	base := compare.Column{OrgID: 1, OrgName: "我方", Env: "UAT", SyncStatus: "success"}
	in := Input{
		Plan:   compare.Plan{Columns: []compare.Column{base}},
		Result: compare.Result{},
		Now:    time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
	}
	if got := notesText(t, in); strings.Contains(got, "⚠️ 已忽略") {
		t.Errorf("没有忽略却写了「⚠️ 已忽略」的提示行：%s", got)
	}
}

// ignoredCellCount 数的是**实际忽略了几个格子**，不是配了几条规则
func TestIgnoredCellCountFromResult(t *testing.T) {
	r := compare.Result{Rows: []compare.Row{
		{Cells: []compare.Cell{{State: compare.CellVersion}, {State: compare.CellIgnored}}},
		{Cells: []compare.Cell{{State: compare.CellIgnored}, {State: compare.CellIgnored}}},
	}}
	if n := ignoredCellCount(r); n != 3 {
		t.Errorf("忽略格子数 = %d，要 3", n)
	}
}
