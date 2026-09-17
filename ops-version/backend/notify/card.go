package notify

import (
	"fmt"
	"strings"
	"time"
)

// maxImageList 镜像列表最多列几条。
//
// 🔴 超出时必须说明**总共多少、还剩多少没列**。老的 harbor-replication 脚本
// 恒写「（前10个）」，1 个镜像时看着莫名其妙，19 个时又看不出被截断了多少。
const maxImageList = 10

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
	title := fmt.Sprintf("%s %s · %s", icon, what, r.Policy)

	elements := []map[string]any{{"tag": "div", "text": md(summaryLines(r))}}

	// 🔴 失败列表排在成功列表**前面**。一次复制 17 成功 2 失败时，
	//    要看的是那 2 条，而不是先滚过 10 条成功的。
	if len(r.Bad) > 0 {
		elements = append(elements,
			map[string]any{"tag": "hr"},
			map[string]any{"tag": "div", "text": md(imageBlock("**失败镜像**", r.Bad, len(r.Bad)))})
	}
	if len(r.OK) > 0 {
		elements = append(elements,
			map[string]any{"tag": "hr"},
			map[string]any{"tag": "div", "text": md(imageBlock("**成功镜像**", r.OK, len(r.OK)))})
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
	fmt.Fprintf(&b, "%s %s · %s\n", icon, what, r.Policy)
	b.WriteString(stripMD(summaryLines(r)) + "\n")
	if len(r.Bad) > 0 {
		b.WriteString("\n" + stripMD(imageBlock("失败镜像", r.Bad, len(r.Bad))) + "\n")
	}
	if len(r.OK) > 0 {
		b.WriteString("\n" + stripMD(imageBlock("成功镜像", r.OK, len(r.OK))) + "\n")
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

func imageBlock(title string, list []Image, total int) string {
	var b strings.Builder
	if total > maxImageList {
		fmt.Fprintf(&b, "%s（共 %d 个，列出前 %d 个）\n", title, total, maxImageList)
	} else {
		fmt.Fprintf(&b, "%s（%d 个）\n", title, total)
	}
	for i, im := range list {
		if i >= maxImageList {
			fmt.Fprintf(&b, "…（其余 %d 个未列出）\n", total-maxImageList)
			break
		}
		b.WriteString(im.Service + "\n")
		if strings.TrimSpace(im.Tag) == "" {
			// 🔴 显式写出来。少一行会被读成"这个镜像没推过去"
			b.WriteString("　└ ⚠️ 未取到版本号\n")
		} else {
			// 服务名与版本号分两行：`eeze-baccarat-multiplay-bp-game-frontend:20260917084842-6`
			// 这种长度在手机上必然折行，折在哪儿不受控，版本号会被拦腰断开
			fmt.Fprintf(&b, "　└ `%s`\n", im.Tag)
		}
		if s := strings.TrimSpace(im.Reason); s != "" {
			fmt.Fprintf(&b, "　└ 原因：%s\n", oneLine(s, 160))
		}
	}
	return strings.TrimRight(b.String(), "\n")
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
