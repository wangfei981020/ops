// Package compare 是对账引擎：给一组列和一个基准，算出每个服务在每列上的判定。
//
// 纯函数，不碰数据库不发请求 —— 判定逻辑是这个系统唯一"想错了就全错"的地方，
// 必须能脱离环境反复测。
package compare

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Column 一个对比列 = (组织, 环境)。
//
// 🔴 **不能假设「同环境对同环境」**：有些项目我方只在 UAT 部署、没有 PROD，
// 需要拿我方 UAT 去比对方的 UAT 和 PROD。列是自由组合，基准也是选出来的其中一列。
// 一个模型覆盖三种用法：跨公司对账 / 内部晋级检查 / 组织×环境全展开。
type Column struct {
	OrgID   int64
	OrgName string
	Env     string

	// ─── 项目 ───
	//
	// 一列 = 项目 × 环境。一家公司下常有多个项目，各自要独立的一列。
	//
	// ⚠️ ProjectName 留空表示**不必在表头显示项目名** —— 该平台只有一个项目时，
	//    显示成「A公司·默认/UAT」纯属噪音。是否填由 API 层决定，
	//    引擎只负责「填了就显示」。
	ProjectID   int64
	ProjectName string

	// Filter 这个项目吃哪些服务。取数时用它从该「平台×环境」的全量快照里筛出本列的部分。
	Filter ProjectFilter

	// IsSelf 这一列是不是**我方**。
	//
	// 🔴 归因（"镜像推没推给对方"）的源只能是我方 —— 只有我方的镜像
	//    才是我们推出去的。原来这个角色由"基准列"兼任，默认基准就是我方；
	//    没有基准之后必须显式标出来。
	// ⚠️ 参与列里一个 IsSelf 都没有时（别的两家公司之间对比），
	//    归因不成立，要显式说"无法判断"而不是"未同步"。
	IsSelf bool

	// 该组织最近一次采集的结果。
	// 🔴 采集失败时整列都是 NoData，**不能让它退化成「这些服务没部署」** ——
	//    那会把「我们没看到」显示成「对方没有」，是最会骗人的一种错。
	SyncStatus string // success | auth_failed | unreachable | forbidden | partial | never | error
	SyncedAt   time.Time
	SyncError  string
}

func (c Column) Key() string {
	if c.ProjectName == "" {
		return c.OrgName + "/" + c.Env
	}
	return c.OrgName + "·" + c.ProjectName + "/" + c.Env
}

// StableKey 不随平台改名变化的列标识。
//
// 🔴 凡是要**存下来**的东西（忽略规则、方案里的列引用）都必须用它，不能用 Key()。
// Key() 里含 OrgName —— 平台一改名，存下来的规则就全部对不上了，
// 而那时界面上不会报错，只是"我明明忽略过的服务又冒出来了"。
// Key() 只适合做同一次比对内部的临时索引（data map 的 key 之类）。
//
// ⚠️ 含 ProjectID：同一平台同一环境下的两个项目是**两列**，
// 共用一个标识的话，给其中一列加的忽略规则会连带作用到另一列。
func (c Column) StableKey() string {
	return strconv.FormatInt(c.OrgID, 10) + "/" + strconv.FormatInt(c.ProjectID, 10) + "/" + c.Env
}

// Healthy 该列的数据是否可信。
func (c Column) Healthy() bool { return c.SyncStatus == "success" || c.SyncStatus == "partial" }

// Snapshot 某服务在某列上的状态，由 providers 层采集后落库再读出来。
type Snapshot struct {
	ServiceKey  string
	Tag         string
	RunningTag  string // 实际在跑的（来自 pod imageID）；与 Tag 不一致 = 发布中
	Digest      string
	Namespace   string
	Workloads   []string
	BuildNo     *int
	IsVersioned bool
	HasConflict bool
	ObservedAt  time.Time
}

// Verdict **一行**的结论。
//
// 🔴 没有基准，所以判定**没有方向**：只能说"这几列彼此一不一样"，
// 说不了"谁落后谁"。原来那套（behind / ahead / missing_base）全是
// 相对基准的，而这张表可能是别的两家公司之间的对账，我方根本不在里面 ——
// 那时"落后 8 个版本"这句话没有主语。
//
// ⚠️ 与 CellState 分工：Verdict 描述**一行**，CellState 描述**一格**。
// 合成一个的话，"这一格没采到"和"这一行没法判定"会共用一个值，
// 而它们一个是原因、一个是结论。
type Verdict string

const (
	VerdictSame    Verdict = "same"    // 这几列的 tag 完全相同
	VerdictDiff    Verdict = "diff"    // 都有，但 tag 不全相同
	VerdictMissing Verdict = "missing" // 至少一列确实没有这个服务
	// VerdictUnknown 至少有一列没法拿来比：整列采集失败 / 非版本化 tag / 同名冲突。
	//
	// 🔴 只在其余列都一致时才会出现 —— 已经查实的缺失或差异不会被它盖掉。
	//    （这条被真实数据推翻过一次：优先级写反时，一列采集失败
	//    就让每一行都成了"无法判定"，而好几行明明比得出差异。）
	VerdictUnknown Verdict = "unknown"
	// VerdictIgnored 整行的格子全被人为忽略。
	//
	// ⚠️ 必须和 VerdictUnknown 分开：忽略是"不用管"，无法判定是"要去查"。
	VerdictIgnored Verdict = "ignored"
)

// CellState **一格**的状态：这一格显示什么、能不能拿来比。
type CellState string

const (
	// CellVersion 有版本号，参与比对
	CellVersion CellState = "version"
	// CellMissing 这一列确实没有这个服务（该列采集是成功的）
	CellMissing CellState = "missing"
	// CellNoData 我们没采到这一列。
	//
	// 🔴 与 CellMissing 严格区分：前者是"我们没看到"，后者是"对方确实没有"。
	//    混成一个的话，对方 token 过期会显示成"对方把服务全下线了"——
	//    处理方向正好相反（查我们自己 vs 找对方确认）。
	CellNoData CellState = "no_data"
	// CellUnversioned 非版本化 tag（latest / stable / v3）。
	// 有版本号、要显示，但两边字符串相同也不代表是同一个镜像。
	CellUnversioned CellState = "unversioned"
	// CellConflict 同名冲突：命中多个 workload 且版本不一致，拒绝判定
	CellConflict CellState = "conflict"
	// CellIgnored 人为忽略，主动不比
	CellIgnored CellState = "ignored"
)

// Comparable 这一格的版本号能不能拿去和别的列比。
func (s CellState) Comparable() bool { return s == CellVersion }

// SyncAttr 差异的**归因**：这个版本的镜像到底推没推到对方那边。
//
// 🔴 这是整个 Harbor 同步模块存在的理由。
// 同样是「对方落后 4 个版本」，两种情况的下一步完全相反：
//
//	镜像推过去了 → 对方还没发版，是对方的节奏，我们催一下就行
//	镜像没推过去 → 是我们的锅，对方想发都发不了
//
// 没有这一层，两者在对账表上长得一模一样。
type SyncAttr string

const (
	// SyncAttrSynced 镜像已同步到位 —— 差异的原因在对方（没发版）
	SyncAttrSynced SyncAttr = "synced"
	// SyncAttrFailed 同步任务失败了 —— 原因在我们这边，且有具体报错
	SyncAttrFailed SyncAttr = "sync_failed"
	// SyncAttrNotSynced 确实没推过去（该组织的复制记录里找不到这个 tag）
	SyncAttrNotSynced SyncAttr = "not_synced"
	// SyncAttrUnknown 🔴 **无法归因**，与 NotSynced 严格分开。
	//
	// 三种情况都会落到这里：复制规则没绑组织、Harbor 压根没配、还没拉取过。
	// 混进 NotSynced 的话，一个「忘了绑定」会被显示成「镜像没推过去」——
	// 全站一致的原则：「我们不知道」永远不能显示成「事实是否定的」。
	SyncAttrUnknown SyncAttr = "unknown"
)

// SyncFact 某个 (服务, tag) 在某组织上的同步结果。
// 由调用方从库里查好传进来 —— compare 是纯函数包，不碰数据库。
type SyncFact struct {
	Status     string
	ErrMsg     string
	FinishedAt time.Time
}

// Cell 一个格子。
type Cell struct {
	Column Column
	State  CellState
	Snap   *Snapshot // Missing / NoData / Ignored 时为 nil

	// Deploying 声明的 tag 与实际在跑的不一致 = 正在滚动更新，或滚动卡住了。
	// 这是**附加标记**不是主判定：一个服务可以既"一致"又"发布中"。
	Deploying bool

	// Note 给人看的一句话解释，UI 直接显示，不要在前端重新拼
	Note string

	// Sync 差异归因。仅对**非一致**的格子有意义（一致就没什么可归因的）
	Sync SyncAttr
	// SyncNote 归因的一句话说明，含失败原因
	SyncNote string
}

// Tag 这一格的版本号；没有则空串。
func (c Cell) Tag() string {
	if c.Snap == nil {
		return ""
	}
	return c.Snap.Tag
}

// Row 一个服务在所有列上的横切。
type Row struct {
	ServiceKey string
	Cells      []Cell

	// Verdict 这一行的结论。**判定的唯一出口** ——
	// 界面、导出、MCP 全都读它，不许各自再算一遍。
	Verdict Verdict

	// HasDiff 这一行需不需要人去看（结论不是"一致"也不是"已忽略"）。
	// 「只看差异」筛的就是它。
	HasDiff bool
}

// Plan 对账方案。
type Plan struct {
	Columns []Column
	// Aliases 各组织的服务名别名：orgID → (该组织上的名字 → 标准名)
	Aliases map[int64]map[string]string

	// SyncGaps 某个平台**为什么**没有复制记录：orgID → 给人看的一句话。
	//
	// 🔴 由调用方填 —— 只有它知道是「规则没绑」「还没拉过」还是「拉取失败」。
	//    compare 是纯函数包，看不到这些。
	SyncGaps map[int64]string

	// Ignores 人为忽略项。两种粒度：
	//   整行忽略  —— 这个服务所有列都不比（对方压根不跑这套服务）
	//   单元格忽略 —— 只有某一列不比（只有这一家不跑）
	//
	// 🔴 单元格忽略**不影响同一行的其他列**：
	//   "印尼不跑 wallet" 不该让"马来 vs 我方 的 wallet 差异"也跟着消失。
	//   这是用户明确要的语义。
	Ignores IgnoreSet

	// ServiceInclude 这次只比这些服务（镜像名最后一段），支持 * 通配。留空 = 全部。
	//
	// 🔴 与采集层的 workload 规则**不是一回事**：
	//   workload 规则管「抄什么回来」——各平台各配各的，改了要重新采集
	//   这个管「这次比哪些」——一份配置对所有平台生效，随时可改、不动数据
	// 按**服务名**而不是 deployment 名：服务名是各平台唯一对得齐的东西。
	ServiceInclude []string

	// SyncFacts 各组织的镜像同步记录：orgID → (service_key\x00tag → 结果)。
	//
	// 🔴 **key 在不在，本身就是信息**：
	//   map 里没有这个 orgID = 该组织没绑复制规则 / Harbor 没配 / 还没拉过
	//                        → 归因为 unknown，而不是「没同步」
	//   有 orgID 但没有那个 (服务,tag) = 确实没推过去 → not_synced
	// 把两者混成一个，「忘了绑定」会被显示成「镜像没推过去」，
	// 人会跑去查 Harbor 的复制规则，而真正的问题是这边少配了一行。
	SyncFacts map[int64]map[string]SyncFact
}

// Result 对账结果。
type Result struct {
	Rows    []Row
	Summary map[Verdict]int
	// UnhealthyColumns 采集失败的列。
	// 🔴 必须单独返回并在 UI 顶部显著提示：整列 NoData 时，
	//    表面上只是几个灰格子，但结论已经不完整了。
	UnhealthyColumns []Column

	// IgnoredCells 被**逐格忽略**的格子数。
	//
	// 🔴 必须单独给。Summary 现在按**行**统计，而一行只有全部格子
	//    都被忽略时结论才是 ignored —— 只忽略了某一列的格子在 Summary 里
	//    完全看不见。而「忽略必须看得见」是硬要求：藏起来的话，
	//    几个月后没人说得清某个格子为什么是空的。
	IgnoredCells int

	// IgnoredRows 被**整行忽略**的服务名。
	// 🔴 必须返回：这些服务不在 Rows 里，如果连名字都不给，
	//    界面上就只能显示一个数字，人没法确认"我到底排除了什么"。
	//    单元格忽略不在这里 —— 那些行还在 Rows 里，格子上有 ignored 判定。
	IgnoredRows []string
}

// Compare 执行对账。
//
// data 的 key 是 Column.Key()。某列缺失或采集失败时，该列所有格子为 NoData。
func Compare(plan Plan, data map[string][]Snapshot) Result {
	res := Result{Summary: map[Verdict]int{}}

	for _, c := range plan.Columns {
		if !c.Healthy() {
			res.UnhealthyColumns = append(res.UnhealthyColumns, c)
		}
	}

	// 按 service_key 归拢，别名在这一步统一
	byCol := map[string]map[string]Snapshot{}
	allKeys := map[string]bool{}
	for _, c := range plan.Columns {
		m := map[string]Snapshot{}
		if c.Healthy() {
			for _, s := range data[c.Key()] {
				key := s.ServiceKey
				if al := plan.Aliases[c.OrgID]; al != nil {
					if canon, ok := al[key]; ok {
						key = canon
					}
				}
				m[key] = s
				allKeys[key] = true
			}
		}
		byCol[c.Key()] = m
	}

	keys := make([]string, 0, len(allKeys))
	for k := range allKeys {
		if !includeService(plan.ServiceInclude, k) {
			continue
		}
		// 🔴 整行忽略的服务**不进结果**，但要计数报出去。
		//    只是不显示的话，几个月后没人说得清某个服务为什么不在表里 ——
		//    这和"筛选后导出"必须写明筛了什么是同一条原则。
		if plan.Ignores.IgnoredRow(k) {
			res.IgnoredRows = append(res.IgnoredRows, k)
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sort.Strings(res.IgnoredRows)

	// 🔴 我方那一列 —— 归因（"镜像推没推过去"）的源。
	//
	//    原来归因用的是**基准列**的 tag，默认基准就是我方。没有基准之后
	//    只能挂 is_self：只有我方的镜像才是我们推出去的。
	//    ⚠️ 参与列里没有我方时（就是"别的两家公司之间对比"），
	//       归因根本不成立 —— 那时显式说"无法判断"，不能退化成"未同步"。
	selfCols := map[string]bool{}
	for _, c := range plan.Columns {
		if c.IsSelf {
			selfCols[c.Key()] = true
		}
	}

	for _, key := range keys {
		row := Row{ServiceKey: key}

		for _, c := range plan.Columns {
			cell := Cell{Column: c}
			switch {
			case plan.Ignores.IgnoredCell(key, c.StableKey()):
				// 🔴 判在最前面：忽略是**人为决定**，优先于任何数据状态。
				//    放在 Healthy 之后的话，采集失败的列会显示成"数据不可用"
				//    而不是"已忽略" —— 而后者根本不需要人去处理。
				cell.State = CellIgnored
				cell.Note = "已忽略：这一列不参与比对"

			case !c.Healthy():
				// 🔴 先判这个。采集失败的列不能进入任何版本比较分支，
				//    否则会拿空数据算出"该列没有这个服务"。
				cell.State = CellNoData
				cell.Note = syncNote(c)

			default:
				s, has := byCol[c.Key()][key]
				if !has {
					// 🔴 这里**绝不能 continue**。
					//
					// 曾经写成「当前列没有且基准列也没有 → continue」，
					// 结果那一行少一格，前端按列顺序渲染时**整行错位** ——
					// 把别的列的版本显示在了这一列下面。
					// 数据看着完全正常，只是对应错了列，是最难发现的一类错。
					//
					// 每一列都必须产出一个 cell，行与列严格对齐。
					cell.State = CellMissing
					cell.Note = "该组织未部署此服务"
				} else {
					cell.Snap = &s
					cell.State, cell.Note = classify(&s)
				}
				if cell.Snap != nil && cell.Snap.RunningTag != "" && cell.Snap.RunningTag != cell.Snap.Tag {
					cell.Deploying = true
					if cell.Note != "" {
						cell.Note += "；"
					}
					cell.Note += "发布中：声明 " + cell.Snap.Tag + "，实跑 " + cell.Snap.RunningTag
				}
			}
			if cell.State == CellIgnored {
				res.IgnoredCells++
			}
			row.Cells = append(row.Cells, cell)
		}

		// 🔴 行结论**统一在这里算一次**，界面/导出/MCP 都读它。
		//    各自再算一遍必然分叉，而分叉时没有任何报错。
		row.Verdict = RowVerdict(row.Cells)
		row.HasDiff = row.Verdict != VerdictSame && row.Verdict != VerdictIgnored
		res.Summary[row.Verdict]++

		// 归因：只对**需要人处理**的行做。一致的没什么可归因的，
		// 已忽略的我们主动不比。
		if row.HasDiff {
			attributeRow(plan, &row, selfCols)
		}

		res.Rows = append(res.Rows, row)
	}
	return res
}

// RowVerdict 一行的结论 —— **无基准，横着看这几列彼此一不一样**。
//
// 顺序：**缺失 > 不一致 > 无法判定 > 一致**。
//
// 🔴 这个顺序被真实数据推翻过两次，两次都是同一个形状 ——
// **优先级高的那一档把低的那一档的事实盖掉了**：
//
//	① 「无法判定」曾排最前。三列里一列采集失败，整张表每一行都成了
//	   「无法判定」，而好几行在另外两列之间明明差着版本。
//	   → 一个采不到的列不该污染整张表。
//
//	② 「不一致」曾排在「缺失」前。真数据一跑：判为「不一致」的 26 行
//	   **全部**同时含缺失格子 —— 这些服务在一半平台上压根不存在，
//	   而结论只说"版本不同"。
//
// ⚠️ ② 的判据不是"谁更重要"（两个都是行动项），是**误导性不对称**：
//
//	说「不一致」暗示这几列都有、只是版本不同 —— 那是假信息；
//	说「缺失」不暗示版本相同，人会去看具体格子 —— 不误导。
//	两害相权，选不会骗人的那个。
//
// ⚠️ 两次都是跑真实数据才发现的，两次单测都是绿的。
func RowVerdict(cells []Cell) Verdict {
	var tags []string
	var unjudgeable, missing bool
	ignored, total := 0, 0

	for _, c := range cells {
		total++
		switch c.State {
		case CellIgnored:
			ignored++
		case CellNoData, CellUnversioned, CellConflict:
			unjudgeable = true
		case CellMissing:
			missing = true
		case CellVersion:
			tags = append(tags, c.Tag())
		}
	}

	if total > 0 && ignored == total {
		return VerdictIgnored
	}
	if missing {
		return VerdictMissing
	}
	if hasDiff(tags) {
		return VerdictDiff
	}
	if unjudgeable {
		// 其余列都一致，但有一列不知道 —— 不能说"一致"，
		// 那等于替一个没查到的列打包票。
		return VerdictUnknown
	}
	if len(tags) == 0 {
		// 一个可比的版本都没有，且没被忽略、没缺失、没有不可判定的 ——
		// 理论上到不了这里，兜底成"无法判定"而不是"一致"。
		return VerdictUnknown
	}
	return VerdictSame
}

// hasDiff 参与比对的版本号是否不全相同。
//
// ⚠️ 少于两个时恒为 false：只配了一个平台、或其余列全被忽略，
// 都没有"不一致"可言 —— 那不是"一致"也不是"不一致"，是无从比较，
// 交给后面几档去定。
func hasDiff(tags []string) bool {
	for i := 1; i < len(tags); i++ {
		if tags[i] != tags[0] {
			return true
		}
	}
	return false
}

// classify 一格有快照时，它是什么状态。
func classify(s *Snapshot) (CellState, string) {
	if s.HasConflict {
		// 🔴 冲突优先于一切：同一个镜像名命中多个 workload 且版本不同，
		//    通常是 ns 规则误抓。此时任何版本判定都是猜的，必须拒绝。
		return CellConflict, "同名冲突：命中多个 workload 且版本不一致，拒绝判定"
	}
	if !s.IsVersioned {
		// 非版本化 tag（latest/stable/...）指向的内容随时会变，
		// 🔴 两边字符串相同**不代表跑的是同一个镜像**，不能判绿
		return CellUnversioned, "非版本化 tag，无法判定是否同一制品"
	}
	return CellVersion, ""
}

// attributeRow 给一行里需要处理的格子做归因。
//
// 🔴 源是**我方**（is_self）那一列的版本，不是"基准"——
// 只有我方的镜像才是我们推出去的。
func attributeRow(plan Plan, row *Row, selfCols map[string]bool) {
	// 我方在这次比对里跑的是哪个版本
	selfTag := ""
	for _, c := range row.Cells {
		if selfCols[c.Column.Key()] && c.State.Comparable() {
			selfTag = c.Tag()
			break
		}
	}
	for i := range row.Cells {
		c := &row.Cells[i]
		if selfCols[c.Column.Key()] || c.State == CellIgnored || c.State == CellNoData {
			continue
		}
		c.Sync, c.SyncNote = attribute(plan, c.Column.OrgID, row.ServiceKey, selfTag, len(selfCols) > 0)
	}
}

// attribute 归因：**我方**那个版本的镜像，推到这个组织了没有。
//
// 🔴 判的是**我方的 tag**（我们要交付的那个版本），不是对方当前跑的 tag。
// 判对方当前 tag 是错的：对方跑着旧版本，那个旧版本当然同步成功过 ——
// 那样每一行都会显示「已同步」，这个功能就完全失去意义。
// 要问的是「我方那个版本，推过去了吗」。
//
// ⚠️ hasSelf=false 表示这次比对里**根本没有我方**（别的两家公司之间对比）。
// 那时归因不成立，必须显式说出来 —— 退化成「未同步」的话，
// 人会跑去查我们的复制规则，而我们压根不是这次比对的一方。
func attribute(plan Plan, orgID int64, serviceKey, selfTag string, hasSelf bool) (SyncAttr, string) {
	if !hasSelf {
		return SyncAttrUnknown,
			"这次比对里没有我方的列 —— 镜像同步是「我方推给对方」，" +
				"两家外部平台之间推没推过，我们无从知道"
	}
	// 我方本来就没有这个服务（或它的 tag 不可比），无所谓「推没推过去」
	if selfTag == "" {
		return SyncAttrUnknown, "我方没有此服务（或版本不可比），无法判断同步状态"
	}

	facts, ok := plan.SyncFacts[orgID]
	if !ok {
		// 🔴 与「没同步」严格分开。这里是**我们不知道**。
		//
		//    ⚠️ 而「不知道」本身有三种成因，处理方式完全不同：
		//      规则没绑     → 去镜像同步页把规则绑上
		//      还没拉取     → 点「立即拉取」
		//      拉取失败     → 去看 Harbor 权限 / 连通性
		//    原来三种混成一句「未绑定复制规则」—— 用户按它去查绑定，
		//    而绑定明明是对的，真因是覆盖面不够。实测把用户和我都引偏了。
		//    成因由调用方填进 SyncGaps（只有它知道），这里只负责说出来。
		if why := plan.SyncGaps[orgID]; why != "" {
			return SyncAttrUnknown, why
		}
		return SyncAttrUnknown, "没有这个平台的复制记录，无法判断镜像是否已同步"
	}

	f, hit := facts[serviceKey+"\x00"+selfTag]
	if !hit {
		// 🔴 再分一层：这个**服务**在复制记录里出现过吗？
		//
		//    出现过 → 说明它在某条规则的范围内，只是这个版本没推 → not_synced（是事实）
		//    没出现 → 它可能压根不在任何规则的范围内 → **unknown**，不是「没推」
		//
		//    不分的话，一个"不在复制范围内"的服务会被显示成「镜像未同步」，
		//    人会去查为什么没推，而真相是它本来就不该被推。
		if !serviceSeen(facts, serviceKey) {
			return SyncAttrUnknown,
				"复制记录里没有这个服务 —— 它可能不在任何复制规则的范围内，" +
					"不代表镜像没推过去"
		}
		return SyncAttrNotSynced,
			"镜像未同步：这个服务推过别的版本，但复制记录里没有当前这个版本 —— 对方拿不到，想发也发不了"
	}
	switch {
	case isSyncFailed(f.Status):
		note := "镜像同步失败"
		if f.ErrMsg != "" {
			note += "：" + strings.TrimSpace(f.ErrMsg)
		}
		return SyncAttrFailed, note
	case isSyncOK(f.Status):
		if !f.FinishedAt.IsZero() {
			return SyncAttrSynced, "镜像已于 " + f.FinishedAt.Format("2006-01-02 15:04") + " 同步，对方尚未发版"
		}
		return SyncAttrSynced, "镜像已同步，对方尚未发版"
	default:
		// 进行中 / 认不出的状态。既不能归成功也不能归失败
		return SyncAttrUnknown, "同步进行中或状态未知（" + f.Status + "）"
	}
}

// Harbor 各版本的取值拼法不统一。
// ⚠️ 认不出时落到 unknown 而不是 not_synced —— 认不出的状态不代表没同步。
func isSyncOK(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "succeed" || s == "succeeded" || s == "success"
}

func isSyncFailed(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "failed" || s == "failure" || s == "error"
}

// includeService 服务白名单判定。留空 = 全放行。
//
// ⚠️ 在**归拢之后**过滤而不是采集时过滤：别名映射要先跑完，
// 否则「对方叫 openapi-svc、我方叫 openapi-backend」时，
// 白名单写我方的名字会把对方那条漏掉。
func includeService(patterns []string, key string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if matchService(strings.TrimSpace(p), key) {
			return true
		}
	}
	return false
}
// matchService 与采集层用同一套通配语义：app-* / *-canary / *mid* / 全等。
// 两处语义不一致的话，人在两个输入框里写同样的东西会得到不同结果。
func matchService(pat, s string) bool {
	if pat == "" {
		return false
	}
	if pat == "*" {
		return true
	}
	pre := strings.HasPrefix(pat, "*")
	suf := strings.HasSuffix(pat, "*")
	switch {
	case pre && suf:
		return strings.Contains(s, strings.Trim(pat, "*"))
	case suf:
		return strings.HasPrefix(s, strings.TrimSuffix(pat, "*"))
	case pre:
		return strings.HasSuffix(s, strings.TrimPrefix(pat, "*"))
	default:
		return pat == s
	}
}

// syncNote 把采集失败翻译成人话。
// 🔴 「连接失败」这种笼统说法等于没说 —— 密码错、网络不通、权限不足
// 三种的处理方式完全不同，必须让看表的人一眼知道该找谁。
func syncNote(c Column) string {
	switch c.SyncStatus {
	case "auth_failed":
		return "数据不可用：认证失败（密码或 token 已失效）"
	case "unreachable":
		return "数据不可用：网络不可达"
	case "forbidden":
		return "数据不可用：账号权限不足，读不到工作负载"
	case "never":
		return "数据不可用：从未成功采集过"
	default:
		s := "数据不可用：采集失败"
		if c.SyncError != "" {
			s += "（" + strings.TrimSpace(c.SyncError) + "）"
		}
		return s
	}
}

// IgnoreSet 忽略规则。
//
// 🔴 存进方案而不是全局：不同客户用不同方案，排除项也不同。
// ⚠️ 必须可解除 —— 对方以后可能上线这个服务，那时不该逼人重建方案。
type IgnoreSet struct {
	// Services 整行忽略的服务名（镜像名最后一段），支持 * 通配
	Services []string `json:"services"`
	// Cells 单元格忽略：服务名 → 该服务被忽略的列
	// （**Column.StableKey()**，即 `orgID/projectID/env`）
	//
	// ⚠️ 用 StableKey 而不是 Key：Key 含平台名和项目名，改个名规则就全失效了，
	//    而且失效时不报错 —— 只是"我明明忽略过的服务又冒出来了"。
	Cells map[string][]string `json:"cells"`
}

// IgnoredRow 整行是否被忽略。
func (s IgnoreSet) IgnoredRow(service string) bool {
	for _, p := range s.Services {
		if matchService(strings.TrimSpace(p), service) {
			return true
		}
	}
	return false
}

// IgnoredCell 某服务在某列是否被忽略。
//
// ⚠️ 整行忽略时**这里返回 false** —— 整行的事由 IgnoredRow 判，
// 两个混在一起会让"已忽略格子数"把整行忽略的也算进去，数字对不上。
func (s IgnoreSet) IgnoredCell(service, colKey string) bool {
	for _, c := range s.Cells[service] {
		if c == colKey {
			return true
		}
	}
	return false
}

// IsEmpty 有没有任何忽略规则 —— 界面据此决定要不要显示"已忽略 N 个"。
func (s IgnoreSet) IsEmpty() bool { return len(s.Services) == 0 && len(s.Cells) == 0 }

// serviceSeen 复制记录里有没有出现过这个服务（不论哪个版本）。
//
// 用来区分「这个版本没推」和「这个服务压根不在复制范围内」——
// 前者是事实（可以去补推），后者是我们不知道（可能它本来就不该被推）。
func serviceSeen(facts map[string]SyncFact, serviceKey string) bool {
	prefix := serviceKey + "\x00"
	for k := range facts {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}
