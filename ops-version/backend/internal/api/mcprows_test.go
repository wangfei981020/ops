package api

import (
	"testing"

	"ops-version-backend/internal/compare"
)

func rowsOf(n int, diff bool) compare.Result {
	r := compare.Result{}
	for i := 0; i < n; i++ {
		r.Rows = append(r.Rows, compare.Row{
			ServiceKey: string(rune('a'+i%26)) + string(rune('0'+i/26)),
			HasDiff:    diff,
			Cells:      []compare.Cell{{State: compare.CellVersion}},
		})
	}
	return r
}

// 🔴 默认只回有差异的。
//
// 全量返回实测 73,025 字符 —— 给 AI 用的工具，第一次按最自然的方式调用
// （不带可选参数）就撑爆上下文。
func TestMCPRowsOnlyDiffFiltersSame(t *testing.T) {
	rows, tr := mcpRows(rowsOf(160, false), true, mcpRowLimit)
	if len(rows) != 0 {
		t.Errorf("全部一致时 only_diff 应该一行不回，实得 %d 行", len(rows))
	}
	if tr != 0 {
		t.Errorf("被 only_diff 滤掉的不算截断（那是筛选不是丢失），实得 truncated=%d", tr)
	}
}

func TestMCPRowsOnlyDiffFalseReturnsAll(t *testing.T) {
	rows, _ := mcpRows(rowsOf(10, false), false, mcpRowLimit)
	if len(rows) != 10 {
		t.Errorf("显式 only_diff=false 要给全量，实得 %d 行", len(rows))
	}
}

// 🔴 截断必须**数出来**。悄悄少给几行的话，AI 拿到不完整清单却以为是全部。
func TestMCPRowsTruncationIsReported(t *testing.T) {
	rows, tr := mcpRows(rowsOf(250, true), true, 200)
	if len(rows) != 200 {
		t.Errorf("应截到 200 行，实得 %d", len(rows))
	}
	if tr != 50 {
		t.Errorf("截掉的行数要如实报出来：应为 50，实得 %d —— "+
			"报 0 的话调用方以为拿到了全部", tr)
	}
}

// 没超上限时不该报截断 —— 平白喊狼来了会让真的截断被忽略
func TestMCPRowsNoFalseTruncation(t *testing.T) {
	if _, tr := mcpRows(rowsOf(5, true), true, 200); tr != 0 {
		t.Errorf("没超上限却报了 truncated=%d", tr)
	}
}
