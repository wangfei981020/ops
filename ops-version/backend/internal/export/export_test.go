package export

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"ops-version-backend/internal/compare"
	"ops-version-backend/providers"
)

func col(id int64, name, env, status string) compare.Column {
	return compare.Column{OrgID: id, OrgName: name, Env: env, SyncStatus: status,
		SyncedAt: time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)}
}

func sampleInput() Input {
	base := col(1, "我方", "UAT", "success")
	other := col(2, "A平台", "PROD", "success")
	dead := col(3, "B平台", "PROD", "auth_failed")
	plan := compare.Plan{Columns: []compare.Column{base, other, dead}}

	b := 114
	o := 110
	data := map[string][]compare.Snapshot{
		base.Key():  {{ServiceKey: "wallet", Tag: "t-114", BuildNo: &b, IsVersioned: true}},
		other.Key(): {{ServiceKey: "wallet", Tag: "t-110", BuildNo: &o, IsVersioned: true}},
		dead.Key():  {},
	}
	return Input{
		Result:   compare.Compare(plan, data),
		Plan:     plan,
		PlanName: "测试方案",
		Operator: "tester",
		Now:      time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
		Pods: map[string][]providers.PodInfo{
			base.Key(): {{
				Namespace: "app-uat", PodName: "wallet-abc", Container: "wallet",
				ServiceKey: "wallet", ImageRepo: "harbor/x/wallet:t-114", Tag: "t-114",
				Phase: "Running", Ready: true, Restarts: 0, PodIP: "10.0.0.1", Node: "node-1",
				StartedAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
			}},
		},
	}
}

func open(t *testing.T, blob []byte) *excelize.File {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("生成的文件打不开: %v", err)
	}
	return f
}

// 🔴 说明页必须是第一个 sheet。
// 这张表会被转发，收到附件的人只会看第一屏 —— 数据时点和判定口径
// 若排在最后一页，等于没写。
func TestReadmeIsFirstSheet(t *testing.T) {
	blob, err := Build(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	f := open(t, blob)
	names := f.GetSheetList()
	if len(names) == 0 || names[0] != "数据说明" {
		t.Errorf("第一个 sheet 应是「数据说明」，实际顺序: %v", names)
	}
	// 空的 Sheet1 必须删掉，留着会让人以为漏了内容
	for _, n := range names {
		if n == "Sheet1" {
			t.Error("默认的空 Sheet1 没有删掉")
		}
	}
}

// 🔴 采集失败的列，明细页不能是一张空表 ——
// 空表和「这个平台一个 Pod 都没有」长得一模一样。
func TestFailedColumnSaysWhy(t *testing.T) {
	in := sampleInput()
	blob, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	f := open(t, blob)
	v, err := f.GetCellValue("B平台-PROD", "A2")
	if err != nil {
		t.Fatalf("找不到失败列的明细页: %v", err)
	}
	if !strings.Contains(v, "采集失败") || !strings.Contains(v, "不代表对方没有部署") {
		t.Errorf("失败列必须说清是我们没读到，而不是留白。实际: %q", v)
	}
}

// 说明页要逐列写出数据时点，并标出哪一列不可信
func TestReadmeListsPerColumnFreshness(t *testing.T) {
	blob, _ := Build(sampleInput())
	f := open(t, blob)
	rows, _ := f.GetRows("数据说明")
	var joined string
	for _, r := range rows {
		joined += strings.Join(r, " | ") + "\n"
	}
	for _, want := range []string{"我方/UAT", "A平台/PROD", "B平台/PROD", "采集失败", "比对 key"} {
		if !strings.Contains(joined, want) {
			t.Errorf("说明页缺少 %q", want)
		}
	}
}

// 🔴 矩阵页**不能再出现任何相对基准的词**。
//
// 这张表可能是别的两个平台之间的对账，我方根本不在里面 ——
// 那时「落后 8」「基准没有」这种话没有主语，收到表的人看不懂。
func TestMatrixHasNoBaselineWording(t *testing.T) {
	blob, _ := Build(sampleInput())
	f := open(t, blob)
	rows, err := f.GetRows("比对矩阵")
	if err != nil || len(rows) < 2 {
		t.Fatalf("矩阵页读不到内容: %v", err)
	}
	all := ""
	for _, r := range rows {
		all += strings.Join(r, " | ") + "\n"
	}
	for _, banned := range []string{"落后", "超前", "基准", "该列没有"} {
		if strings.Contains(all, banned) {
			t.Errorf("矩阵页不该出现 %q（相对基准的说法），实际:\n%s", banned, all)
		}
	}
}

// sampleInput 里 B平台 那一列采集失败（auth_failed），
// 而 wallet 在另外两列之间是 t-114 vs t-110 —— 确凿的不一致。
//
// 🔴 一个采不到的列**不许污染整张表**。
//
//	这条是样本导出时抓到的真 bug：判定优先级写反，把「无法判定」
//	排在最前，于是一列连不上就让**每一行**都成了「无法判定」，
//	而其中好几行明明比得出差异。那条差异被藏在"不知道"后面，
//	就没人会去处理它了。
func TestDeadColumnDoesNotMaskRealDiff(t *testing.T) {
	blob, _ := Build(sampleInput())
	f := open(t, blob)
	rows, _ := f.GetRows("比对矩阵")
	if len(rows) < 2 {
		t.Fatal("矩阵页没有数据行")
	}
	if last := rows[1][len(rows[1])-1]; last != "不一致" {
		t.Errorf("行结论应是「不一致」（另两列确实差着版本），实际 %q（整行: %v）", last, rows[1])
	}
	// ⚠️ 但采集失败这件事不能就此消失：表头必须标出是**整列**的问题，
	//    否则那一列满屏的「—」会被读成「对方把服务全下线了」。
	if !strings.Contains(strings.Join(rows[0], " "), "采集失败") {
		t.Errorf("表头应标出该列采集失败，实际: %v", rows[0])
	}
	// 🔴 采集失败的格子写「未采集」，**不能**和「这列确实没有」的「—」同形：
	//    两者的处理方向相反（查我们自己 vs 找对方确认）
	if rows[1][3] != "未采集" {
		t.Errorf("采集失败的格子 = %q，要「未采集」——和「—」同形就分不出是谁的问题", rows[1][3])
	}
}

// sheet 名有硬限制：>31 字符或含 : \ / ? * [ ] 会让 NewSheet 失败，
// 那时**整个导出都出不来** —— 而平台名是用户自己填的，什么都可能有
func TestSheetNameSanitized(t *testing.T) {
	long := strings.Repeat("组织", 20) + "/PROD"
	got := sheetName(long + ":x[1]")
	if len([]rune(got)) > 31 {
		t.Errorf("sheet 名超长未截断: %d 字符", len([]rune(got)))
	}
	for _, bad := range []string{":", "\\", "/", "?", "*", "[", "]"} {
		if strings.Contains(got, bad) {
			t.Errorf("sheet 名仍含非法字符 %q: %s", bad, got)
		}
	}
}

// Age 以**导出时刻**为基准，且解析不出时不能拿 now 兜底
// （那会让一个跑了半年的 Pod 显示成「刚启动」）
func TestAgeBaseline(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if got := age(now.Add(-25*time.Hour), now); !strings.Contains(got, "天") {
		t.Errorf("25 小时应显示为天，实际 %q", got)
	}
	if got := age(time.Time{}, now); got != "—" {
		t.Errorf("没有启动时刻必须显示 —，实际 %q", got)
	}
}

// 🔴 筛选后导出必须在说明页写明白。
// 不写的话，收到附件的人会把一份「只含落后服务」的清单当成全量，
// 然后得出「其余都一致」这个完全相反的结论。
func TestFilteredExportSaysSo(t *testing.T) {
	in := sampleInput()
	in.FilterNote = "判定为「落后」"
	blob, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	f := open(t, blob)
	rows, _ := f.GetRows("数据说明")
	var joined string
	for _, r := range rows {
		joined += strings.Join(r, " | ") + "\n"
	}
	if !strings.Contains(joined, "筛选后导出") {
		t.Error("说明页没写明这是筛选后的导出")
	}
	if !strings.Contains(joined, "不代表没有差异") {
		t.Error("必须说清「未列出的服务不代表没有差异」—— 否则会被读成全量结论")
	}

	// 没筛选时不该出现这一行，否则每份导出都带个无意义的警告
	plain, _ := Build(sampleInput())
	f2 := open(t, plain)
	rows2, _ := f2.GetRows("数据说明")
	for _, r := range rows2 {
		if strings.Contains(strings.Join(r, ""), "筛选后导出") {
			t.Error("没筛选时不该出现筛选提示")
		}
	}
}

// 🔴 版本栏必须**只有版本号** —— 这一栏是拿来复制的。
// 原来把 `[落后 2]` 拼在版本号后面，复制去比对/搜索/填工单时会一起带走。
func TestMatrixVersionCellHasNoVerdict(t *testing.T) {
	base := compare.Column{OrgID: 1, OrgName: "我方", Env: "UAT", SyncStatus: "success"}
	c2 := compare.Column{OrgID: 2, OrgName: "印尼", Env: "UAT", SyncStatus: "success"}
	cells := []compare.Cell{
		{Column: base, State: compare.CellVersion,
			Snap: &compare.Snapshot{Tag: "20260819174536-53"}},
		{Column: c2, State: compare.CellVersion,
			Snap: &compare.Snapshot{Tag: "20260819054132-51"}},
	}
	res := compare.Result{
		Summary: map[compare.Verdict]int{},
		Rows: []compare.Row{{
			ServiceKey: "biz-frontend",
			Cells:      cells,
			// ⚠️ 结论由 compare 算，这里照它算一次而不是硬写 ——
			//    硬写的话这个测试就锁不住"导出读的是 row.Verdict"。
			Verdict: compare.RowVerdict(cells),
		}},
	}
	blob, err := Build(Input{
		Result: res, Plan: compare.Plan{Columns: []compare.Column{base, c2}},
		PlanName: "t", Operator: "tester", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	rows, err := f.GetRows("比对矩阵")
	if err != nil {
		t.Fatalf("找不到矩阵页: %v", err)
	}
	if len(rows) < 2 {
		t.Fatal("矩阵页没有数据行")
	}
	data := rows[1]
	// 🔴 一个平台**一列**：A=服务 B=我方 C=印尼 D=结论。
	//    原来每列拆成"版本 + 状态"两栏，用户反馈看不懂。
	if len(data) != 4 {
		t.Fatalf("列数 = %d，要 4（服务 + 2 个平台 + 结论）：%v", len(data), data)
	}
	// ⚠️ 版本格是拿来**复制**的，一个字都不能多
	if data[2] != "20260819054132-51" {
		t.Errorf("版本格 = %q，要纯版本号 —— 混了判定就没法直接复制", data[2])
	}
	if data[3] != "不一致" {
		t.Errorf("结论 = %q，两列版本不同应是「不一致」", data[3])
	}
}
