package export

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"ops-version-backend/internal/compare"
)

// writeReadme 数据说明。**必须是第一个 sheet。**
//
// 🔴 这张表会被转发。收到附件的人不在界面上，他不知道：
// 数据是什么时候采的、基准是哪一列、哪些列压根没采到。
// 少了这一页，一张「对方落后 12 个版本」的表可能是拿三天前的数据算的，
// 而看表的人会拿它去开会。
func writeReadme(f *excelize.File, in Input, st *styles) error {
	const sh = "数据说明"
	if _, err := f.NewSheet(sh); err != nil {
		return err
	}
	_ = f.SetColWidth(sh, "A", "A", 22)
	_ = f.SetColWidth(sh, "B", "B", 96)

	rows := [][2]string{
		{"比对方案", in.PlanName},
		{"导出时间", fmtTime(in.Now)},
		{"导出人", in.Operator},
	}
	// 🔴 筛过的导出必须在第一页说清楚。不说的话，收到附件的人
	//    会把一份「只含落后服务」的清单当成全量，然后得出「其余都一致」的结论。
	if in.FilterNote != "" {
		rows = append(rows, [2]string{"⚠️ 本次为筛选后导出",
			in.FilterNote + " —— 未列出的服务不代表没有差异，只是这次没导"})
	}

	// 🔴 被忽略的东西必须在这里点名。
	//
	//    忽略掉的行**不出现在表里**，不写出来的话，几个月后没人说得清
	//    某个服务为什么不在这份清单上 —— 而这份表是拿去跟客户对账的。
	//    「不比」和「比过了没差异」是两件完全不同的事，收到附件的人有权知道是哪一种。
	if len(in.Result.IgnoredRows) > 0 {
		rows = append(rows, [2]string{
			fmt.Sprintf("⚠️ 已忽略 %d 个服务（整行不比）", len(in.Result.IgnoredRows)),
			strings.Join(in.Result.IgnoredRows, "、") +
				" —— 这些服务本次未参与比对，不代表它们没有差异",
		})
	}
	if n := ignoredCellCount(in.Result); n > 0 {
		rows = append(rows, [2]string{
			fmt.Sprintf("⚠️ 已忽略 %d 个单元格", n),
			"表中标为「已忽略」的格子，是人为决定不比这一列，不是数据缺失",
		})
	}

	// 各列的数据时点 —— 逐列列出，因为它们是**分别采集**的，可能相差很远
	for _, c := range in.Plan.Columns {
		v := fmtTime(c.SyncedAt)
		if !c.Healthy() {
			v += fmt.Sprintf("  ⚠️ 采集失败（%s）：本列全部判为「数据不可用」", c.SyncStatus)
			if c.SyncError != "" {
				v += "，" + strings.TrimSpace(c.SyncError)
			}
		}
		rows = append(rows, [2]string{"数据时点 · " + c.Key(), v})
	}

	rows = append(rows, [][2]string{
		{"", ""},
		{"比对 key", "镜像名的最后一段。registry 地址、Harbor 项目名、namespace、workload 名四者双方都可能不一致，全部剥掉不参与比对"},
		{"", ""},
		// 🔴 只有**一套**口径 —— 比对矩阵和差异明细现在用同一套判定。
		//
		//    改之前是两套（矩阵无基准、明细相对基准），说明页得分两段写。
		//    基准整个删掉之后两页统一了，这里也跟着合并 ——
		//    留着"判定口径 · 差异明细"那一段会解释一堆**已经不存在的词**
		//    （落后 / 基准没有），看表的人会去表里找而找不到。
		{"判定口径", ""},
		{"一致", "这几列的 tag 完全相同"},
		{"缺失", "至少有一列确实没有这个服务（该列采集是成功的）。⚠️ 这一档排在「不一致」前面：这种行里往往两者都有，而说「不一致」会让人以为这几列都有、只是版本不同"},
		{"不一致", "这几列都有这个服务，但 tag 不全相同。⚠️ 不说谁新谁旧 —— 没有基准列，而跨公司是两个 Harbor、两条流水线，版本号本来就不可比"},
		{"无法判定", "至少有一列没法拿来比：整列采集失败、非版本化 tag（latest / v3 这类，指向的内容随时会变）、或同名冲突。⚠️ 排在最后：它说的是「不知道」，不该盖掉已经查实的缺失或差异"},
		{"已忽略", "人为决定不比这一格，不是数据缺失。忽略的格子不参与该行的结论"},
		{"", ""},
		{"格子里的字", ""},
		{"—", "这一列确实没有这个服务 → 找对方确认"},
		{"未采集", "我们没采到这一列 → 查我们自己的采集。⚠️ 与「—」是两回事，处理方向相反"},
		{"已忽略", "主动决定不比 → 什么都不用做"},
		{"", ""},
		{"发布中", "声明的 tag 与实际在跑的 tag 不一致 = 正在滚动更新，或滚动卡住了。这是附加标记，一个服务可以既「一致」又「发布中」。⚠️ 只在差异明细里体现，比对矩阵不显示它——它几分钟就自愈，标出来会制造假的待办"},
		{"", ""},
		{"归因口径", ""},
		{"镜像已同步", "我方那个版本的镜像已经推到对方仓库了 —— 差异的原因在对方（还没发版），不是我们没给"},
		{"镜像未同步", "复制记录里找不到我方这个版本 —— 对方拿不到，想发也发不了。这是我们这边要处理的"},
		{"同步失败", "复制任务跑了但失败了，具体原因见「归因说明」列"},
		{"同步状态未知", "复制规则没绑到这个组织 / 没配 Harbor / 还没拉取过记录。不等于没同步 —— 是我们不知道"},
		{"一致率分母", "排除「数据不可用」的列。拿不到数据不算不一致 —— 否则一个组织采集失败会让整张表看起来差异激增，掩盖真正的差异"},
	}...)

	_ = f.SetCellValue(sh, "A1", "版本比对 · 数据说明")
	_ = f.SetCellStyle(sh, "A1", "A1", st.title)
	for i, kv := range rows {
		r := i + 3
		_ = f.SetCellValue(sh, cell("A", r), kv[0])
		_ = f.SetCellValue(sh, cell("B", r), kv[1])
		if kv[0] != "" && kv[1] == "" {
			_ = f.SetCellStyle(sh, cell("A", r), cell("A", r), st.title)
		}
		_ = f.SetCellStyle(sh, cell("B", r), cell("B", r), st.wrap)
	}
	return nil
}

// writeMatrix 对账矩阵 —— 收到这张表的人，九成时间只看它。
//
// 🔴 一个平台**一列**，格子里**只有版本号**，最后一列结论。没别的。
//
//	原来一个平台占两列（版本 + 状态），表头还标着「（基准）」，
//	状态写的是「落后 8」「基准没有」「该列没有」。
//	用户的原话是"其他人看不懂" —— 而问题不在措辞：
//	**这张表可能是别的两家公司之间的对账，我方根本不在里面**，
//	那时"落后 8 个版本"这句话没有主语。
//
// 去掉基准之后只剩一个问法：这几列彼此一不一样。判定见 matrixverdict.go。
//
// ⚠️ 版本号那一格保持**纯版本号**，一个字都不加 ——
//
//	拿到表的人最常做的动作是复制它去搜索、去填工单，
//	混进「[落后 2]」会被一起复制走（用户实测反馈）。
//	所有判定信息都收进结论列，那一列本来就不是拿来复制的。
func writeMatrix(f *excelize.File, in Input, st *styles) error {
	const sh = "比对矩阵"
	if _, err := f.NewSheet(sh); err != nil {
		return err
	}
	_ = f.SetColWidth(sh, "A", "A", 42)

	// 表头：服务 + 每个平台一列 + 结论
	_ = f.SetCellValue(sh, "A1", "服务")
	for i, c := range in.Plan.Columns {
		col := colName(i + 1)
		title := c.Key()
		// ⚠️ 「⚠️采集失败」保留。这一列连不上时整列都是「—」，
		//    和「对方确实没有这些服务」长得一模一样 ——
		//    不标的话人会把"我们没查到"读成"对方没有"，判反。
		//    行结论虽然也会写「无法判定」，但表头标一次才看得出
		//    这是**整列**的问题，不是零散几个服务的问题。
		if !c.Healthy() {
			title += " ⚠️采集失败"
		}
		_ = f.SetCellValue(sh, cell(col, 1), title)
		_ = f.SetColWidth(sh, col, col, 30)
	}
	last := colName(len(in.Plan.Columns) + 1)
	_ = f.SetCellValue(sh, cell(last, 1), "结论")
	_ = f.SetColWidth(sh, last, last, 14)
	_ = f.SetCellStyle(sh, "A1", cell(last, 1), st.head)

	for ri, row := range in.Result.Rows {
		r := ri + 2

		// 先把整行翻成矩阵格子，再算结论 ——
		// 结论要看全行，所以不能边写边判。
		// 🔴 结论直接读 row.Verdict —— compare 已经算好了。
		//    导出这边**不许再算一遍**：两套判定必然分叉，而分叉时不报错。
		conc := verdictLabel[row.Verdict]

		// 服务名列不上色（和收表人已经习惯的那两版表一致），只用等宽
		_ = f.SetCellValue(sh, cell("A", r), row.ServiceKey)
		_ = f.SetCellStyle(sh, cell("A", r), cell("A", r), st.mono)

		// 整行同色，颜色由行结论定 —— 不再是每格各自一个颜色。
		// 没有基准就没有"这一格相对谁如何"，颜色只能表达行的结论。
		for ci, c := range row.Cells {
			col := colName(ci + 1)
			_ = f.SetCellValue(sh, cell(col, r), cellText(c))
			_ = f.SetCellStyle(sh, cell(col, r), cell(col, r), monoStyleOf(st, row.Verdict))
		}
		_ = f.SetCellValue(sh, cell(last, r), conc)
		_ = f.SetCellStyle(sh, cell(last, r), cell(last, r), styleOf(st, row.Verdict))
	}

	// 冻结首行 + 首列：横向有 N 列、纵向上百行，不冻结就没法看
	_ = f.SetPanes(sh, &excelize.Panes{
		Freeze: true, Split: false, XSplit: 1, YSplit: 1,
		TopLeftCell: "B2", ActivePane: "bottomRight",
	})
	_ = f.AutoFilter(sh, "A1:"+cell(last, 1), []excelize.AutoFilterOptions{})
	return nil
}

// writeDiff 只列需要人处理的行，附带每列的版本和归因。
//
// 矩阵一屏放不下上百行，而真正要处理的通常只有十几行。
//
// 🔴 原来这一页有「基准版本」「差几个版本」两列 —— 已删。
// 没有基准就没有"落后几个版本"，而跨公司两边是两个 Harbor、两条流水线，
// 版本号本来就不可比，那个数字在跨公司场景里是假的。
func writeDiff(f *excelize.File, in Input, st *styles) error {
	const sh = "差异明细"
	if _, err := f.NewSheet(sh); err != nil {
		return err
	}
	// 每列一栏版本号 + 结论 + 归因
	heads := []string{"服务"}
	widths := []float64{40}
	for _, c := range in.Plan.Columns {
		heads = append(heads, c.Key())
		widths = append(widths, 30)
	}
	heads = append(heads, "结论", "归因", "归因说明", "说明")
	widths = append(widths, 12, 14, 52, 52)
	for i, h := range heads {
		c := colName(i)
		_ = f.SetCellValue(sh, cell(c, 1), h)
		_ = f.SetColWidth(sh, c, c, widths[i])
	}
	_ = f.SetCellStyle(sh, "A1", cell(colName(len(heads)-1), 1), st.head)

	r := 2
	for _, row := range in.Result.Rows {
		// 一致的和已忽略的不列 —— 这一页是"要处理的清单"
		if !row.HasDiff {
			continue
		}
		_ = f.SetCellValue(sh, cell("A", r), row.ServiceKey)
		_ = f.SetCellStyle(sh, cell("A", r), cell("A", r), st.mono)
		for ci, c := range row.Cells {
			col := colName(ci + 1)
			_ = f.SetCellValue(sh, cell(col, r), cellText(c))
			_ = f.SetCellStyle(sh, cell(col, r), cell(col, r), monoStyleOf(st, row.Verdict))
		}
		vc := colName(len(in.Plan.Columns) + 1)
		_ = f.SetCellValue(sh, cell(vc, r), verdictLabel[row.Verdict])
		_ = f.SetCellStyle(sh, cell(vc, r), cell(vc, r), styleOf(st, row.Verdict))

		// 归因取这一行里**最该被看到**的那一条。
		// ⚠️ 一行可能有多列各有各的归因，但这一页一行只有一格 ——
		//    取"我们这边要处理的"那条（not_synced / failed 优先于 unknown），
		//    否则一个 unknown 会把真正要处理的那条挤掉。
		sync, note, detail := pickSync(row.Cells)
		_ = f.SetCellValue(sh, cell(colName(len(in.Plan.Columns)+2), r), syncLabel[sync])
		if sync != "" {
			_ = f.SetCellStyle(sh, cell(colName(len(in.Plan.Columns)+2), r),
				cell(colName(len(in.Plan.Columns)+2), r), syncStyleOf(st, sync))
		}
		_ = f.SetCellValue(sh, cell(colName(len(in.Plan.Columns)+3), r), note)
		_ = f.SetCellValue(sh, cell(colName(len(in.Plan.Columns)+4), r), detail)
		_ = f.SetCellStyle(sh, cell(colName(len(in.Plan.Columns)+3), r),
			cell(colName(len(in.Plan.Columns)+4), r), st.wrap)
		r++
	}
	if r == 2 {
		_ = f.SetCellValue(sh, "A2", "本次比对没有需要处理的行")
	}
	_ = f.SetPanes(sh, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	return nil
}

// pickSync 一行里最该被看到的那条归因。
//
// 🔴 "我们这边要处理的"优先：not_synced / failed 排在 unknown 前面。
// 不排的话，一个 unknown 会把真正的行动项挤掉 ——
// 而 unknown 恰恰是最常见的那个（没绑规则的平台每一行都是它）。
func pickSync(cells []compare.Cell) (compare.SyncAttr, string, string) {
	best := -1
	rankOf := func(a compare.SyncAttr) int {
		switch a {
		case compare.SyncAttrFailed:
			return 3
		case compare.SyncAttrNotSynced:
			return 2
		case compare.SyncAttrSynced:
			return 1
		case compare.SyncAttrUnknown:
			return 0
		}
		return -1
	}
	var pick compare.Cell
	for _, c := range cells {
		if rk := rankOf(c.Sync); rk > best {
			best, pick = rk, c
		}
	}
	if best < 0 {
		// 🔴 一格归因都没有，通常是**这次比对的列全是我方**
		//    （我方 UAT vs 我方 PROD 这种），那时"推给对方"无从谈起。
		//    ⚠️ 留空白的话，看表的人会以为归因功能坏了 ——
		//       说清楚"不适用"和留空白是两回事。
		return "", "不适用：镜像同步说的是「我方推给对方」，这次比对的列里没有外部平台", ""
	}
	return pick.Sync, pick.SyncNote, pick.Note
}

// writeDetails 每列一个 Pod 级明细 sheet —— 对应原脚本的 RANCHER_XX sheet。
func writeDetails(f *excelize.File, in Input, st *styles) error {
	heads := []string{"命名空间", "Pod", "容器", "服务", "镜像", "版本", "状态", "就绪", "重启", "IP", "节点", "已运行"}
	widths := []float64{18, 46, 26, 30, 70, 30, 12, 8, 8, 16, 24, 12}

	// 🔴 页签名要去重。Excel 的页签名上限 31 字符，而列标识加上项目名之后
	//    很容易超 —— 两个项目的列截断后会撞成同一个名字，
	//    结果是第二列的明细**盖掉**第一列的，而导出照样成功、文件照样能打开。
	//    收到文件的人看到两个页签、内容却是同一份，只会以为自己看错了。
	used := map[string]bool{}
	for _, c := range in.Plan.Columns {
		sh := uniqueSheetName(sheetName(c.Key()), used)
		if _, err := f.NewSheet(sh); err != nil {
			return err
		}
		for i, h := range heads {
			col := colName(i)
			_ = f.SetCellValue(sh, cell(col, 1), h)
			_ = f.SetColWidth(sh, col, col, widths[i])
		}
		_ = f.SetCellStyle(sh, "A1", cell(colName(len(heads)-1), 1), st.head)

		// 🔴 采集失败的列必须写清楚，不能留一张空表 ——
		//    空表和「这个组织一个 Pod 都没有」长得一模一样
		if !c.Healthy() {
			_ = f.SetCellValue(sh, "A2", fmt.Sprintf(
				"⚠️ 本列采集失败（%s），没有明细数据。这不代表对方没有部署 —— 我们没读到。%s",
				c.SyncStatus, c.SyncError))
			_ = f.SetCellStyle(sh, "A2", "A2", st.noData)
			continue
		}

		pods := in.Pods[c.Key()]
		if len(pods) == 0 {
			_ = f.SetCellValue(sh, "A2", "采集成功，但没有 Pod 明细 —— 可能是 pod 接口读取失败（不影响版本比对），或该列确实没有运行中的 Pod")
			_ = f.SetCellStyle(sh, "A2", "A2", st.noData)
			continue
		}
		for i, p := range pods {
			r := i + 2
			vals := []any{p.Namespace, p.PodName, p.Container, p.ServiceKey, p.ImageRepo, p.Tag,
				p.Phase, readyText(p.Ready), p.Restarts, p.PodIP, p.Node, age(p.StartedAt, in.Now)}
			for ci, v := range vals {
				_ = f.SetCellValue(sh, cell(colName(ci), r), v)
			}
			// Running 但没就绪是最常见的故障态（探针一直不过），标出来
			if !p.Ready {
				_ = f.SetCellStyle(sh, cell("G", r), cell("H", r), st.diff)
			}
			// 重启次数高的单独标：它不影响版本判定，但往往是真问题
			if p.Restarts >= 5 {
				_ = f.SetCellStyle(sh, cell("I", r), cell("I", r), st.missing)
			}
		}
		_ = f.SetPanes(sh, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
		_ = f.AutoFilter(sh, "A1:"+cell(colName(len(heads)-1), 1), []excelize.AutoFilterOptions{})
	}
	return nil
}

func readyText(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

// syncLabel 归因 → 中文。与界面用同一套词。
var syncLabel = map[compare.SyncAttr]string{
	compare.SyncAttrSynced:    "镜像已同步",
	compare.SyncAttrFailed:    "同步失败",
	compare.SyncAttrNotSynced: "镜像未同步",
	compare.SyncAttrUnknown:   "同步状态未知",
}

// syncStyleOf 归因配色。
//
// 🔴 与判定色分工：判定色说「有没有差异」，归因色说「该找谁」。
//
//	未同步 / 同步失败 → 红：**是我们的锅**，对方想发都发不了
//	已同步            → 灰：镜像到位了，差异的原因在对方
//	未知              → 灰：我们不知道，去把复制规则绑上组织
//
// ⚠️ 已同步刻意**不用绿**：绿会被读成「这一格没问题」，
// 而它仍然是一个差异，只是责任不在我们。
func syncStyleOf(st *styles, a compare.SyncAttr) int {
	switch a {
	case compare.SyncAttrNotSynced, compare.SyncAttrFailed:
		return st.diff
	default:
		return st.noData
	}
}
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func cell(col string, row int) string { return col + strconv.Itoa(row) }

func colName(i int) string {
	n, _ := excelize.ColumnNumberToName(i + 1)
	return n
}

// sheetName Excel 的 sheet 名有硬限制：≤31 字符，且不能含 : \ / ? * [ ]
// 超了或带非法字符时 NewSheet 会失败，而那时整个导出都出不来。
// uniqueSheetName 保证页签名不重复：撞了就在**末尾**换成 ~2、~3。
//
// ⚠️ 后缀要挤掉尾部字符而不是往后追加 —— 追加会再次超过 31 字符上限，
// excelize 那时会直接报错，整份导出失败。
func uniqueSheetName(base string, used map[string]bool) string {
	name := base
	for n := 2; used[name]; n++ {
		suf := fmt.Sprintf("~%d", n)
		r := []rune(base)
		if len(r)+len(suf) > 31 {
			r = r[:31-len([]rune(suf))]
		}
		name = string(r) + suf
	}
	used[name] = true
	return name
}

func sheetName(s string) string {
	r := strings.NewReplacer(":", "-", "\\", "-", "/", "-", "?", "", "*", "", "[", "(", "]", ")")
	s = r.Replace(s)
	if len([]rune(s)) > 31 {
		s = string([]rune(s)[:31])
	}
	return s
}

// ignoredCellCount 数一下有多少格被人为忽略。
//
// ⚠️ 从结果里数，不从 Plan.Ignores 里数：规则可能写了通配、也可能指向
// 这次没参与比对的列 —— 「配了几条规则」和「实际忽略了几个格子」是两个数，
// 写进导出的必须是后者。
func ignoredCellCount(r compare.Result) int {
	n := 0
	for _, row := range r.Rows {
		for _, c := range row.Cells {
			if c.State == compare.CellIgnored {
				n++
			}
		}
	}
	return n
}
