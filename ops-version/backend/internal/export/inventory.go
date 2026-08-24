package export

import (
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
	"ops-version-backend/internal/compare"
)

// InventoryInput 单列版本清单的入参。
//
// 🔴 与对账表（Build）刻意分成两个入口，而不是"让对账表支持一列"：
// 一列没有"比对"可言 —— 没有基准、没有落差、没有归因。
// 硬塞进对账表的话，导出的文件里会有一整套永远是空的判定列，
// 收到的人会以为"所有服务都没差异"，而实际上根本没比过。
//
// 这张表回答的是另一个问题：**这一套环境现在跑着哪些服务、什么版本**。
type InventoryInput struct {
	OrgName  string
	Env      string
	Services []compare.Snapshot
	// ObservedAt 数据时点。⚠️ 与导出时刻分开：
	// 清单会被转发，几天后打开必须看得出数据是什么时候的
	ObservedAt time.Time
	Operator   string
	Now        time.Time
}

// BuildInventory 生成单列版本清单。
func BuildInventory(in InventoryInput) ([]byte, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	st, err := newStyles(f)
	if err != nil {
		return nil, err
	}

	const sh = "版本清单"
	if _, err := f.NewSheet(sh); err != nil {
		return nil, err
	}
	f.DeleteSheet("Sheet1")

	// 表头之上先写清楚这是什么、数据什么时候的 ——
	// 与对账表的「数据说明」页同一条理由：这张表会脱离上下文流传
	meta := [][2]string{
		{"平台 / 环境", in.OrgName + " · " + in.Env},
		{"数据时点", fmtTime(in.ObservedAt)},
		{"导出时间", fmtTime(in.Now)},
		{"导出人", in.Operator},
		{"说明", "这是单个环境的**版本清单**，不是比对结果 —— 没有基准列，因此不存在「落后 / 超前」的判定"},
	}
	for i, m := range meta {
		_ = f.SetCellValue(sh, cell("A", i+1), m[0])
		_ = f.SetCellValue(sh, cell("B", i+1), m[1])
		_ = f.SetCellStyle(sh, cell("A", i+1), cell("A", i+1), st.title)
	}
	_ = f.SetColWidth(sh, "A", "A", 26)
	_ = f.SetColWidth(sh, "B", "B", 34)
	_ = f.SetColWidth(sh, "C", "D", 20)
	_ = f.SetColWidth(sh, "E", "E", 46)

	head := len(meta) + 2
	cols := []string{"服务", "命名空间", "版本", "在跑版本", "工作负载"}
	for i, h := range cols {
		c := cell(string(rune('A'+i)), head)
		_ = f.SetCellValue(sh, c, h)
		_ = f.SetCellStyle(sh, c, c, st.head)
	}

	for i, s := range in.Services {
		r := head + 1 + i
		_ = f.SetCellValue(sh, cell("A", r), s.ServiceKey)
		_ = f.SetCellValue(sh, cell("B", r), s.Namespace)
		_ = f.SetCellValue(sh, cell("C", r), s.Tag)
		// ⚠️ 在跑版本与声明版本不同 = 正在滚动更新，或者滚动卡住了。
		//    只导声明版本会让"卡在半路"的服务看起来一切正常。
		running := s.RunningTag
		if running == "" || running == s.Tag {
			running = "—"
		}
		_ = f.SetCellValue(sh, cell("D", r), running)
		_ = f.SetCellValue(sh, cell("E", r), strings.Join(s.Workloads, ", "))
	}

	if err := f.SetPanes(sh, &excelize.Panes{
		Freeze: true, Split: false, XSplit: 0, YSplit: head, TopLeftCell: cell("A", head+1),
		ActivePane: "bottomLeft",
	}); err != nil {
		return nil, err
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
