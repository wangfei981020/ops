// Package export 把一次对账结果导出成 Excel。
//
// 为什么导出这件事值得单独一个包：这张表**会被转发**。
// 它一旦离开界面就没有上下文了 —— 收到附件的人不知道数据是什么时候采的、
// 基准是哪一列、哪些列其实没采到。所以第一个 sheet 必须是「数据说明」，
// 而不是直接甩一张对比表过去。
package export

import (
	"fmt"
	"time"

	"github.com/xuri/excelize/v2"

	"ops-version-backend/internal/compare"
	"ops-version-backend/providers"
)

// Input 导出所需的全部数据。
type Input struct {
	Result   compare.Result
	Plan     compare.Plan
	PlanName string
	// Pods 各列的 Pod 明细，key 是 Column.Key()
	Pods map[string][]providers.PodInfo
	// Operator 谁导的。导出会外发，必须能追溯
	Operator string
	// FilterNote 导出时界面上应用了什么筛选。空 = 没筛，导的是全量
	FilterNote string
	// Now 导出时刻，由调用方传入而不是包内取 —— 好让测试可重复
	Now time.Time
}

// 判定 → 中文标签。与界面用同一套词，避免「界面说落后、表里说 behind」
// verdictLabel 行结论的中文。
//
// 🔴 判定本身在 compare.RowVerdict —— 这里**只负责翻译**，不许再算一遍。
//    （原来导出有自己一套判定，界面另一套，迟早分叉且不报错。）
var verdictLabel = map[compare.Verdict]string{
	compare.VerdictSame:    "一致",
	compare.VerdictDiff:    "不一致",
	compare.VerdictMissing: "缺失",
	compare.VerdictUnknown: "无法判定",
	compare.VerdictIgnored: "已忽略",
}

// cellText 一格显示什么。
//
// 🔴 三种空态**各有各的字**，不能都写「—」：
//
//	—      这一列确实没有这个服务   → 找对方确认
//	未采集  我们没采到               → 查我们自己的采集
//	已忽略  主动决定不比             → 什么都不用做
//
// 都写「—」的话，对方 token 过期会被读成「对方把服务全下线了」——
// 处理方向正好反了。
func cellText(c compare.Cell) string {
	switch c.State {
	case compare.CellIgnored:
		return "已忽略"
	case compare.CellNoData:
		return "未采集"
	case compare.CellMissing:
		return "—"
	}
	// ⚠️ 非版本化 tag / 同名冲突**照样显示版本号** —— 它是真的，
	//    只是不能拿来判定是否同一制品。
	if t := c.Tag(); t != "" {
		return t
	}
	return "—"
}

// styleOf 行结论对应的底色。整行都用它。
func styleOf(st *styles, v compare.Verdict) int {
	switch v {
	case compare.VerdictSame:
		return st.same
	case compare.VerdictDiff:
		return st.diff
	case compare.VerdictMissing:
		return st.missing
	case compare.VerdictIgnored:
		return st.ignored
	default: // unknown
		return st.noData
	}
}

// monoStyleOf 同上，等宽 —— 版本号那几列用，位数才对得齐。
func monoStyleOf(st *styles, v compare.Verdict) int {
	switch v {
	case compare.VerdictSame:
		return st.sameMono
	case compare.VerdictDiff:
		return st.diffMono
	case compare.VerdictMissing:
		return st.missingMono
	case compare.VerdictIgnored:
		return st.ignoredMono
	default:
		return st.noDataMono
	}
}

// Build 生成 xlsx 字节流。
func Build(in Input) ([]byte, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	st, err := newStyles(f)
	if err != nil {
		return nil, err
	}

	// 🔴 说明放第一个 sheet，不是最后。转发出去的表，人只会看第一屏
	if err := writeReadme(f, in, st); err != nil {
		return nil, err
	}
	if err := writeMatrix(f, in, st); err != nil {
		return nil, err
	}
	if err := writeDiff(f, in, st); err != nil {
		return nil, err
	}
	if err := writeDetails(f, in, st); err != nil {
		return nil, err
	}

	// NewFile 会自带一个空的 Sheet1，留着会让人以为漏了内容
	if idx, _ := f.GetSheetIndex("Sheet1"); idx >= 0 {
		_ = f.DeleteSheet("Sheet1")
	}
	f.SetActiveSheet(0)

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04:05")
}

// age 把启动时刻换算成「跑了多久」。
//
// ⚠️ 基准是**导出时刻**，不是采集时刻 —— 两者可能差几分钟到几小时，
// 而人看到的「Age」是在读这张表的时候理解的。
func age(started, now time.Time) string {
	if started.IsZero() {
		return "—"
	}
	d := now.Sub(started)
	if d < 0 {
		return "—"
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%.1f 小时", d.Hours())
	default:
		return fmt.Sprintf("%.1f 天", d.Hours()/24)
	}
}
