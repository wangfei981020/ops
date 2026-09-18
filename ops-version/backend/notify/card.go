package notify

import (
	"fmt"
	"strings"
	"time"
)

const (
	// imagesPerCard 一张卡最多列几个成功镜像，超出就分片再发一张。
	//
	// 🔴 **不再截断**。原来写死列 10 个，一次同步 19 个镜像就少看 9 个 ——
	// 而收通知的人要的恰恰是那份完整清单（"哪个服务推到哪个版本了"）。
	//
	// 25 的依据：一个镜像两行，25 个 = 50 行，接近飞书折叠消息的界线；
	// 100 个镜像正好 4 张，比每片 20 个少一条消息。
	imagesPerCard = 25

	// maxCards 一次执行最多发几张卡。超出的不再列，指向界面。
	//
	// 🔴 必须有上限。真出现一次同步 500 个镜像时，20 张卡群里没人看，
	// 而且会连撞飞书的 5 次/秒 限频。8 张 = 200 个镜像，够覆盖绝大多数同步。
	maxCards = 8

	// maxBadList 失败镜像最多列几条。
	//
	// 失败镜像**永远全量列在第一张卡上**、不参与分片 —— 失败是要人处理的，
	// 不能被"列不下了"吃掉。但也要有个天花板，否则 300 个全失败时第一张卡直接超限。
	maxBadList = 50

	// cardByteBudget 单张卡序列化后的字节上限。
	//
	// 🔴 飞书自定义机器人的硬限制是**请求体 20KB**，超了整条发不出去。
	// 按条数估算不可靠（服务名长度差很多），所以渲染完还要按真实字节兜一道：
	// 超预算就砍这一片的条数重渲染。留 2KB 余量给 JSON 转义。
	cardByteBudget = 18 << 10

	// cardOverhead 卡片除镜像列表以外的固定开销（标题/头部三行/小字/JSON 结构）
	cardOverhead = 1200
)

// Image 一个镜像的同步结果。
type Image struct {
	// Service 镜像名最后一段（与对账用的是同一套 key）
	Service string
	// Tag 版本号。🔴 **空不等于没有** —— 空表示我们没取到，
	// 文案里必须显式写出来而不是省略：收通知的人唯一关心的就是版本号，
	// 静悄悄少一行会被读成"这个镜像没推"。
	Tag string
	// Reason 失败原因（Harbor task 日志，已截断）。只有失败的镜像才有。
	Reason string
}

// Replication 一次复制的通知内容。
//
// 两条链路（webhook 实时 / 采集器兜底）共用这一个结构，保证同一次复制
// 无论从哪条路发出来，人看到的东西是一样的。
type Replication struct {
	Level        Level
	Policy       string // 规则名，如 sl-rsync-pa-g32
	OrgName      string // 推给哪个平台；空 = 这条规则还没绑平台
	DestRegistry string // 目标 Harbor 地址
	Trigger      string // Harbor 的 trigger 原值
	ExecID       int64  // Harbor 的 execution id；0 = 拿不到
	Total        int
	Succeeded    int
	Failed       int
	// Duration 这次复制耗时。
	//
	// 🔴 **终态的卡片必须有耗时**（用户 2026-09-17 明确要求：老服务每条都有，
	// 换了新版反而没有说不过去）。取值见 DurationOf 的兜底链。
	//
	// 0 = 连开始时间都没拿到，那时整行不显示 —— 绝不显示「0 秒」：
	// 「0 秒」是事实陈述（瞬间完成），「没查到」是数据缺失，两者不能长成一样。
	Duration time.Duration
	// Approx 耗时是估算的（Harbor 没给结束时间，用"收到事件的时刻"减开始时间），
	// 文案里会写成「≈ 36 秒」。
	Approx bool
	// SyncedAt 复制发生的时刻 —— 不是发通知的时刻。
	//
	// 🔴 兜底补发最晚会晚 30 分钟，而飞书卡片上那个时间戳是**消息送达时间**。
	// 不自己写一行同步时间的话，补发的卡片会让人以为刚刚才同步过。
	SyncedAt time.Time
	// Realtime true=webhook 实时发的；false=采集器轮询发现后补发的
	Realtime bool
	OK       []Image
	Bad      []Image

	// ---- 分片（由 Split 填，调用方不用自己算）----

	// Page 第几片（从 1 开始）。Pages<=1 时标题里不显示页码。
	Page int
	// Pages 总片数
	Pages int
	// OKFrom 这一片列的是第几个成功镜像（从 1 开始），用于「第 26–50 个」
	OKFrom int
	// OKTotal 这次执行成功镜像的总数（不是这一片的条数）
	OKTotal int
	// OKOmitted 超过 maxCards 之后被舍弃的条数；>0 时最后一片要说明去哪儿看
	OKOmitted int
	// BadOmitted 失败镜像超过 maxBadList 被舍弃的条数
	BadOmitted int
}

// Split 把一次执行的通知切成若干张卡。
//
// 🔴 切片规则：
//
//	第 1 片  完整头部（平台/结果/耗时）+ **失败镜像全量** + 成功镜像第 1 片
//	第 2 片起 只有标题（带页码）+ 成功镜像 + 小字，不重复头部
//
// 失败镜像不参与分片：要处理的东西必须在第一眼能看到的地方。
//
// ⚠️ 返回的每一片都是独立的 Replication，调用方逐片 Card()/Text() 并**逐片投递**，
// 片间要留间隔（飞书 5 次/秒）。分片投递不是原子的 —— 中间一片失败就缺一页，
// 所以每片都要各自记一条投递记录，否则事后查不出缺的是哪一页。
func Split(r Replication) []Replication {
	bad := r.Bad
	omittedBad := 0
	if len(bad) > maxBadList {
		omittedBad = len(bad) - maxBadList
		bad = bad[:maxBadList]
	}

	// 🔴 装箱同时看**两个**上限：条数（可读性）和字节（飞书 20KB 硬限制）。
	//    只按条数切不够：服务名长度差很多，25 个长名字也可能撑爆一张卡，
	//    而那时飞书整条拒收 —— 表现是"这一页没发出来"，比多发一页糟得多。
	groups := [][]Image{}
	cur := []Image{}
	curBytes := 0
	for _, im := range r.OK {
		b := imageBytes(im)
		if len(cur) > 0 && (len(cur) >= imagesPerCard || curBytes+b > cardByteBudget-cardOverhead) {
			groups = append(groups, cur)
			cur, curBytes = []Image{}, 0
		}
		cur = append(cur, im)
		curBytes += b
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}

	// 没有成功镜像（纯失败）也要发一张
	if len(groups) == 0 {
		one := r
		one.Bad, one.BadOmitted = bad, omittedBad
		one.Page, one.Pages, one.OKFrom, one.OKTotal = 1, 1, 0, 0
		return []Replication{one}
	}

	omitted := 0
	if len(groups) > maxCards {
		for _, g := range groups[maxCards:] {
			omitted += len(g)
		}
		groups = groups[:maxCards]
	}

	out := make([]Replication, 0, len(groups))
	from := 1
	for i, g := range groups {
		p := r
		p.Page, p.Pages = i+1, len(groups)
		p.OK, p.OKFrom, p.OKTotal = g, from, len(r.OK)
		from += len(g)
		if i == 0 {
			p.Bad, p.BadOmitted = bad, omittedBad
		} else {
			// 后续片不重复失败清单，也不重复头部（头部由 Page>1 决定不渲染）
			p.Bad, p.BadOmitted = nil, 0
		}
		if i == len(groups)-1 {
			p.OKOmitted = omitted
		}
		out = append(out, p)
	}
	return out
}

// imageBytes 一个镜像在卡片里大约占多少字节（服务名 + 版本号 + 两行缩进符号）。
// 宁可估多不估少：估少了会撑爆 20KB 让飞书整条拒收。
func imageBytes(im Image) int {
	n := len(im.Service) + len(im.Tag) + 24 // 24 ≈ 换行 + 「　└ 」+ 反引号
	if im.Reason != "" {
		n += len(im.Reason) + 16
		if n > 400 {
			n = 400 // Reason 渲染时会截到 160 个字符
		}
	}
	return n
}

// Card 飞书交互卡片。
//
// 结构：彩色标题栏（一眼分成败）→ 平台/结果/耗时三行 → 镜像列表 → 小字元信息。
// 元信息（execution id、来源、同步时刻）放最后的 note 区：排障要用，
// 但它不该和版本号抢视线。
func Card(r Replication) map[string]any {
	failed := r.Level == LevelFailed
	tone, icon, what := "green", "✅", "镜像同步完成"
	if failed {
		tone, icon, what = "red", "❌", "镜像同步失败"
	}
	title := fmt.Sprintf("%s %s · %s%s", icon, what, r.Policy, pageSuffix(r))

	elements := []map[string]any{}
	// 平台/结果/耗时只在第一片出现：后面几片是同一次执行的续页，
	// 每片都重复一遍头部，真正要看的镜像清单会被挤下去。
	if r.Page <= 1 {
		elements = append(elements, map[string]any{"tag": "div", "text": md(summaryLines(r))})
	}

	// 🔴 失败列表排在成功列表**前面**，而且只在第一片 —— 要处理的东西
	//    必须在第一眼能看到的地方，不能被翻页藏起来。
	if len(r.Bad) > 0 {
		if r.Page <= 1 {
			elements = append(elements, map[string]any{"tag": "hr"})
		}
		elements = append(elements,
			map[string]any{"tag": "div", "text": md(badBlock(r, true))})
	}
	if len(r.OK) > 0 {
		if len(elements) > 0 {
			elements = append(elements, map[string]any{"tag": "hr"})
		}
		elements = append(elements,
			map[string]any{"tag": "div", "text": md(okBlock(r, true))})
	}
	elements = append(elements, map[string]any{
		"tag":      "note",
		"elements": []map[string]any{{"tag": "plain_text", "content": meta(r)}},
	})

	return map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"config": map[string]any{"wide_screen_mode": true},
			"header": map[string]any{
				"template": tone,
				"title":    map[string]any{"tag": "plain_text", "content": title},
			},
			"elements": elements,
		},
	}
}

// Text 同一份内容的纯文本版。
//
// 🔴 存进 notify_records.content 的是这一份，不是卡片 JSON：
// 「实际发出去的是什么」要人能一眼读懂，而一坨 JSON 没人愿意读。
func Text(r Replication) string {
	icon, what := "✅", "镜像同步完成"
	if r.Level == LevelFailed {
		icon, what = "❌", "镜像同步失败"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s · %s%s\n", icon, what, r.Policy, pageSuffix(r))
	if r.Page <= 1 {
		b.WriteString(stripMD(summaryLines(r)) + "\n")
	}
	if len(r.Bad) > 0 {
		b.WriteString("\n" + stripMD(badBlock(r, false)) + "\n")
	}
	if len(r.OK) > 0 {
		b.WriteString("\n" + stripMD(okBlock(r, false)) + "\n")
	}
	b.WriteString("\n" + meta(r))
	return b.String()
}

func summaryLines(r Replication) string {
	var b strings.Builder
	// 平台 + 目标地址。老脚本只有规则名 —— `sl-rsync-pa-g32` 这种名字
	// 对不熟的人不说明推给了谁，而这正是收通知的人要判断的第一件事。
	where := r.OrgName
	if where == "" {
		where = "未绑定平台"
	}
	if r.DestRegistry != "" {
		where += " · " + shortHost(r.DestRegistry)
	}
	fmt.Fprintf(&b, "**平台**　%s\n", where)

	if r.Failed > 0 {
		fmt.Fprintf(&b, "**结果**　成功 %d · **失败 %d**\n", r.Succeeded, r.Failed)
	} else {
		fmt.Fprintf(&b, "**结果**　成功 %d · 失败 0\n", r.Succeeded)
	}

	// 耗时连开始时间都没有时才整行不写 —— 写「0 秒」会被当成事实
	if r.Duration > 0 {
		d := humanDuration(r.Duration)
		if r.Approx {
			// Harbor 没给结束时间，这是用"收到事件的时刻"估的。标出来，
			// 别让人拿它去对 SLA —— 但也不能不显示，老服务每条都有耗时。
			d = "≈ " + d
		}
		fmt.Fprintf(&b, "**耗时**　%s · %s", d, triggerText(r.Trigger))
	} else {
		fmt.Fprintf(&b, "**触发**　%s", triggerText(r.Trigger))
	}
	return b.String()
}

// DurationOf 算这次复制的耗时，两条路都用它 —— 口径不能有两套。
//
// 🔴 兜底链（用户要求终态卡片一定要有耗时）：
//
//	① end_time − start_time      权威值
//	② now − start_time           Harbor 还没写回 end_time 时的估算，标 ≈
//	③ 0                          连 start_time 都没有，整行不显示
//
// ⚠️ ② 只在**执行已到终态**时才用。执行还在跑时算出来的是"已经跑了多久"，
// 那不是耗时，写进卡片会被当成最终结果。
func DurationOf(started, ended, now time.Time, terminal bool) (time.Duration, bool) {
	if started.IsZero() {
		return 0, false
	}
	if !ended.IsZero() && ended.After(started) {
		return ended.Sub(started), false
	}
	if terminal && now.After(started) {
		return now.Sub(started), true
	}
	return 0, false
}

// pageSuffix 标题上的页码。单片时不显示 —— 「(1/1)」是噪音。
func pageSuffix(r Replication) string {
	if r.Pages > 1 {
		return fmt.Sprintf(" (%d/%d)", r.Page, r.Pages)
	}
	return ""
}

// okBlock 成功镜像清单。
//
// 🔴 **给多少列多少，不再截断**：这一片的清单本来就是 Split 按上限切好的，
// 这里再截一次就会静默丢内容 —— 而收通知的人要的正是完整清单。
func okBlock(r Replication, markdown bool) string {
	title := "成功镜像"
	if markdown {
		title = "**成功镜像**"
	}
	var b strings.Builder
	switch {
	case r.Pages > 1:
		// 分片时写明这一片是第几到第几个，方便人对齐几条消息
		fmt.Fprintf(&b, "%s（共 %d 个 · 第 %d–%d 个）\n",
			title, r.OKTotal, r.OKFrom, r.OKFrom+len(r.OK)-1)
	default:
		fmt.Fprintf(&b, "%s（%d 个）\n", title, len(r.OK))
	}
	writeImages(&b, r.OK)
	if r.OKOmitted > 0 {
		// 超过总上限时必须说清还剩多少、去哪儿看 —— 不能让人以为就这么多
		fmt.Fprintf(&b, "…（其余 %d 个未列出，去「镜像同步」页按 Execution %d 查看）\n",
			r.OKOmitted, r.ExecID)
	}
	return strings.TrimRight(b.String(), "\n")
}

// badBlock 失败镜像清单。永远在第一片，永远全量（上限 maxBadList）。
func badBlock(r Replication, markdown bool) string {
	title := "失败镜像"
	if markdown {
		title = "**失败镜像**"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s（%d 个）\n", title, r.Failed)
	writeImages(&b, r.Bad)
	if r.BadOmitted > 0 {
		fmt.Fprintf(&b, "…（其余 %d 个失败未列出，去「镜像同步」页按 Execution %d 查看）\n",
			r.BadOmitted, r.ExecID)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeImages(b *strings.Builder, list []Image) {
	for _, im := range list {
		b.WriteString(im.Service + "\n")
		if strings.TrimSpace(im.Tag) == "" {
			// 🔴 显式写出来。少一行会被读成"这个镜像没推过去"
			b.WriteString("　└ ⚠️ 未取到版本号\n")
		} else {
			// 服务名与版本号分两行：`eeze-baccarat-multiplay-bp-game-frontend:20260917084842-6`
			// 这种长度在手机上必然折行，折在哪儿不受控，版本号会被拦腰断开
			fmt.Fprintf(b, "　└ `%s`\n", im.Tag)
		}
		if s := strings.TrimSpace(im.Reason); s != "" {
			fmt.Fprintf(b, "　└ 原因：%s\n", oneLine(s, 160))
		}
	}
}

func meta(r Replication) string {
	parts := []string{}
	if r.ExecID > 0 {
		parts = append(parts, fmt.Sprintf("Execution %d", r.ExecID))
	}
	if r.Realtime {
		parts = append(parts, "实时通知")
	} else {
		parts = append(parts, "兜底补发（轮询发现，webhook 未送达）")
	}
	if !r.SyncedAt.IsZero() {
		parts = append(parts, "同步于 "+r.SyncedAt.Format("2006-01-02 15:04:05"))
	}
	return strings.Join(parts, " · ")
}

func md(s string) map[string]any {
	return map[string]any{"tag": "lark_md", "content": s}
}

// stripMD 去掉 lark_md 的标记，用于纯文本版。
func stripMD(s string) string {
	return strings.NewReplacer("**", "", "`", "").Replace(s)
}

// oneLine 把多行的错误日志压平并截断 —— Harbor 的 task 日志是多行的，
// 原样塞进卡片会把一条通知撑成一屏。
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	rs := []rune(s)
	if len(rs) > max {
		return string(rs[:max]) + "…"
	}
	return s
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
	}
}

// shortHost 从 registry 地址里取主机名 —— 卡片里放整串 https://… 太长。
func shortHost(endpoint string) string {
	v := strings.TrimSpace(endpoint)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://")
	if i := strings.IndexAny(v, "/:"); i > 0 {
		v = v[:i]
	}
	return v
}
