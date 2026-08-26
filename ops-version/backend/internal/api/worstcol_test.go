package api

import (
	"testing"

	"ops-version-backend/internal/compare"
)

func row(svc string, states ...compare.CellState) compare.Row {
	r := compare.Row{ServiceKey: svc}
	for i, st := range states {
		r.Cells = append(r.Cells, compare.Cell{
			Column: compare.Column{OrgName: string(rune('A' + i)), Env: "UAT"},
			State:  st,
		})
	}
	return r
}

// 判据必须是「这一列拖累了多少行」，不是「服务名重合度」。
//
// 实测过推翻过第一版实现：我方·项目B 只有 13 个服务（全表 122 行），
// 那 13 个里有 9 个在别列也有 —— 重合度 69% 很高，
// 却在 109 行上都是 missing。重合度这个维度抓不住元凶。
func TestWorstMissingColumnNamesTheDragger(t *testing.T) {
	rows := []compare.Row{}
	// B 列在 8 行里缺 7 行；A 列全都有
	for i, svc := range []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8"} {
		if i == 0 {
			rows = append(rows, row(svc, compare.CellVersion, compare.CellVersion))
			continue
		}
		rows = append(rows, row(svc, compare.CellVersion, compare.CellMissing))
	}
	col, pct := worstMissingColumn(compare.Result{Rows: rows})
	if col != "B/UAT" {
		t.Fatalf("应当点名 B 列（它让 7/8 行判缺失），实际 %q", col)
	}
	if pct < 50 {
		t.Fatalf("比例应当 ≥50%%，实际 %d", pct)
	}
}

// 🔴 没有明显元凶时**绝不点名** —— 指错一列比不指更糟：
// 用户会取消勾选一列本该参与对账的数据。
func TestWorstMissingColumnStaysQuietWhenSpread(t *testing.T) {
	rows := []compare.Row{
		row("s1", compare.CellVersion, compare.CellMissing),
		row("s2", compare.CellMissing, compare.CellVersion),
		row("s3", compare.CellVersion, compare.CellVersion),
		row("s4", compare.CellVersion, compare.CellVersion),
	}
	if col, _ := worstMissingColumn(compare.Result{Rows: rows}); col != "" {
		t.Fatalf("缺失分散在多列时不该点名，实际点了 %q", col)
	}
}

// no_data 不算：那是采集失败/降级采集，界面另有提示条。
// 算进来会指向一列"数据没采到"的，而那时该做的是修采集，不是取消勾选。
func TestWorstMissingColumnIgnoresNoData(t *testing.T) {
	rows := []compare.Row{}
	for _, svc := range []string{"s1", "s2", "s3", "s4"} {
		rows = append(rows, row(svc, compare.CellVersion, compare.CellNoData))
	}
	if col, _ := worstMissingColumn(compare.Result{Rows: rows}); col != "" {
		t.Fatalf("整列 no_data 不该被点名（那是采集问题），实际点了 %q", col)
	}
}
