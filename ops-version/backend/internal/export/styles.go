package export

import "github.com/xuri/excelize/v2"

// styles 全表共用的样式。
//
// 配色沿用原来那套 Excel 脚本的约定（红=有差异 / 绿=一致 / 黄=缺失），
// 因为看表的人已经习惯了这三个颜色的含义 —— 换一套只会让人重新学一遍。
//
// 🔴 但比原脚本多一档：**灰色的「数据不可用」**。
// 原脚本把「对方没有」和「我们没采到」都涂成黄色 MISSING，
// 于是对方 token 过期时整列变黄，看起来像「对方把服务全下线了」。
// 这两件事的处理方式完全不同（一个找对方确认、一个是我们自己的采集坏了），
// 颜色必须分开。
type styles struct {
	head     int
	same     int
	diff     int
	missing  int
	noData   int
	conflict int
	// ignored 人为忽略：白底灰字斜体，视觉上**退到背景里** ——
	// 它是唯一一个"看到了也不用做任何事"的态。
	ignored int
	// ── 上面几个的等宽版，版本号那几列用 ──
	// 🔴 版本号必须等宽，否则 `20260811070909-177d814-116` 和
	//    `20260807095832-135` 上下一比对不齐，人得一位一位数。
	sameMono    int
	diffMono    int
	missingMono int
	noDataMono  int
	ignoredMono int
	mono        int
	wrap        int
	title       int
}

func newStyles(f *excelize.File) (*styles, error) {
	mk := func(bg, fg string, bold bool) (int, error) {
		return mkFont(f, bg, fg, bold, false)
	}
	// 等宽版：同样的底色，字体换成 Consolas
	mkMono := func(bg, fg string) (int, error) {
		return mkFont(f, bg, fg, false, true)
	}
	var st styles
	var err error
	if st.head, err = mk("F2F2F2", "404040", true); err != nil {
		return nil, err
	}
	if st.same, err = mk("C6EFCE", "006100", false); err != nil {
		return nil, err
	}
	if st.diff, err = mk("FFC7CE", "9C0006", false); err != nil {
		return nil, err
	}
	if st.missing, err = mk("FFEB9C", "9C6500", false); err != nil {
		return nil, err
	}
	// 灰：不知道。刻意不用红也不用黄 —— 它既不是故障也不是缺失
	if st.noData, err = mk("EDEDED", "808080", false); err != nil {
		return nil, err
	}
	if st.conflict, err = mk("FFC7CE", "9C0006", true); err != nil {
		return nil, err
	}
	// 白底灰字：忽略的格子不该抢注意力，它是唯一"不用做任何事"的态
	if st.ignored, err = mk("FFFFFF", "A6A6A6", false); err != nil {
		return nil, err
	}
	// 各色的等宽版
	for _, x := range []struct {
		dst    *int
		bg, fg string
	}{
		{&st.sameMono, "C6EFCE", "006100"},
		{&st.diffMono, "FFC7CE", "9C0006"},
		{&st.missingMono, "FFEB9C", "9C6500"},
		{&st.noDataMono, "EDEDED", "808080"},
		{&st.ignoredMono, "FFFFFF", "A6A6A6"},
	} {
		if *x.dst, err = mkMono(x.bg, x.fg); err != nil {
			return nil, err
		}
	}
	if st.mono, err = f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Family: "Consolas", Size: 10},
	}); err != nil {
		return nil, err
	}
	if st.wrap, err = f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"},
		Font:      &excelize.Font{Size: 10},
	}); err != nil {
		return nil, err
	}
	if st.title, err = f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Size: 13},
	}); err != nil {
		return nil, err
	}
	return &st, nil
}

// mkFont 建一个带底色和边框的单元格样式。
//
// mono=true 时用 Consolas —— 版本号列专用，位数才对得齐。
func mkFont(f *excelize.File, bg, fg string, bold, mono bool) (int, error) {
	font := &excelize.Font{Bold: bold, Color: fg, Size: 10}
	if mono {
		font.Family = "Consolas"
	}
	s := &excelize.Style{
		Font:      font,
		Alignment: &excelize.Alignment{Vertical: "center"},
		Border: []excelize.Border{
			{Type: "left", Color: "D9D9D9", Style: 1},
			{Type: "right", Color: "D9D9D9", Style: 1},
			{Type: "top", Color: "D9D9D9", Style: 1},
			{Type: "bottom", Color: "D9D9D9", Style: 1},
		},
	}
	if bg != "" {
		s.Fill = excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{bg}}
	}
	return f.NewStyle(s)
}
