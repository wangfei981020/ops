package compare

import "testing"

// 🔴 行结论的优先级：**缺失 > 不一致 > 无法判定 > 一致**。
//
// 这个顺序被真实数据推翻过**两次**，两次都是同一个形状 ——
// 优先级高的那一档把低的那一档的事实盖掉了，而**两次单测都是绿的**
// （因为我把想错的预期也写进了断言）。
//
// 这个文件就是拿来锁死这个顺序的。改它之前先想清楚：
// 你是要让某种"确凿的事实"被另一种盖掉吗？
func TestRowVerdictPriority(t *testing.T) {
	ver := func(tag string) Cell {
		return Cell{State: CellVersion, Snap: &Snapshot{Tag: tag}}
	}
	missing := Cell{State: CellMissing}
	dead := Cell{State: CellNoData}
	unver := Cell{State: CellUnversioned, Snap: &Snapshot{Tag: "latest"}}
	conflict := Cell{State: CellConflict, Snap: &Snapshot{Tag: "v1"}}
	ign := Cell{State: CellIgnored}

	cases := []struct {
		name  string
		cells []Cell
		want  Verdict
	}{
		// ── 基本四态 ──
		{"两列全同", []Cell{ver("v1"), ver("v1")}, VerdictSame},
		{"五列全同", []Cell{ver("v1"), ver("v1"), ver("v1"), ver("v1"), ver("v1")}, VerdictSame},
		{"两列不同", []Cell{ver("v1"), ver("v2")}, VerdictDiff},
		{"三列里一列不同", []Cell{ver("v1"), ver("v1"), ver("v9")}, VerdictDiff},
		{"一列没这个服务", []Cell{ver("v1"), missing}, VerdictMissing},
		{"一列采集失败、其余一致", []Cell{ver("v1"), ver("v1"), dead}, VerdictUnknown},
		{"非版本化 tag，两边字符串相同也不能判一致", []Cell{unver, unver}, VerdictUnknown},
		{"同名冲突", []Cell{ver("v1"), conflict}, VerdictUnknown},

		// ── 第一次翻车：「无法判定」曾排最前 ──
		// 三列里一列采集失败，整张表每一行都成了「无法判定」，
		// 而好几行在另外两列之间明明差着版本。
		// 一个采不到的列不该污染整张表。
		{"不一致 + 采集失败：差异不能被盖掉", []Cell{ver("v1"), ver("v2"), dead}, VerdictDiff},
		{"缺失 + 采集失败：缺失不能被盖掉", []Cell{ver("v1"), missing, dead}, VerdictMissing},

		// ── 第二次翻车：「不一致」曾排在「缺失」前 ──
		// 真数据里判为「不一致」的 26 行全部同时含缺失格子 ——
		// 那些服务在一半平台上压根不存在，而结论只说"版本不同"。
		//
		// 判据是**误导性不对称**：说「不一致」暗示这几列都有、只是版本不同
		// （假信息）；说「缺失」不暗示版本相同（不误导）。
		{"不一致 + 缺失：缺失不会骗人，不一致会", []Cell{ver("v1"), ver("v2"), missing}, VerdictMissing},
		{"三缺一且其余互不相同", []Cell{ver("v1"), ver("v2"), ver("v3"), missing}, VerdictMissing},
		{"缺失 + 不一致 + 采集失败", []Cell{ver("v1"), ver("v2"), missing, dead}, VerdictMissing},

		// ── 忽略 ──
		{"忽略的格子不参与判定", []Cell{ver("v1"), ver("v1"), ign}, VerdictSame},
		{"被忽略的那列本来不一致，也不算数", []Cell{ver("v1"), ver("v1"), ign}, VerdictSame},
		{"整行逐格忽略光了", []Cell{ign, ign}, VerdictIgnored},
		{"忽略 + 其余仍有差异", []Cell{ver("v1"), ver("v2"), ign}, VerdictDiff},

		// ── 边界 ──
		// 只剩一个版本号可比 = 没有"不一致"可言
		{"只配了一个平台", []Cell{ver("v1")}, VerdictSame},
		{"其余列全被忽略", []Cell{ver("v1"), ign}, VerdictSame},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RowVerdict(c.cells); got != c.want {
				t.Errorf("= %q，要 %q", got, c.want)
			}
		})
	}
}

// 空输入不能 panic —— 行结论是全站判定的唯一出口，它崩了什么都出不来。
func TestRowVerdictEmpty(t *testing.T) {
	if got := RowVerdict(nil); got != VerdictUnknown {
		t.Errorf("= %q，空输入要 %q（不能兜成「一致」）", got, VerdictUnknown)
	}
}
