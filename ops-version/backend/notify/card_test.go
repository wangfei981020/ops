package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sample() Replication {
	return Replication{
		Level: LevelOK, Policy: "sync-to-a-appA", OrgName: "A平台",
		DestRegistry: "https://registry.example.com/", Trigger: "event_based",
		ExecID: 22199, Total: 1, Succeeded: 1,
		Duration: 4 * time.Second,
		SyncedAt: time.Date(2026, 9, 17, 16, 49, 36, 0, time.UTC),
		Realtime: true,
		OK:       []Image{{Service: "appA-wallet-backend", Tag: "20260917084842-6"}},
	}
}

// 卡片必须是飞书认的形状：msg_type=interactive + header.template 决定颜色
func TestCardShape(t *testing.T) {
	c := Card(sample())
	if c["msg_type"] != "interactive" {
		t.Fatalf("msg_type 应为 interactive，实得 %v", c["msg_type"])
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("卡片序列化失败：%v", err)
	}
	if !strings.Contains(string(raw), `"template":"green"`) {
		t.Errorf("成功卡片的标题栏应为绿色：%s", raw)
	}
	bad := sample()
	bad.Level = LevelFailed
	raw2, _ := json.Marshal(Card(bad))
	if !strings.Contains(string(raw2), `"template":"red"`) {
		t.Errorf("失败卡片的标题栏应为红色：%s", raw2)
	}
}

// 🔴 版本号是收通知的人唯一关心的东西，必须出现在文案里
func TestTextCarriesTagAndPolicy(t *testing.T) {
	got := Text(sample())
	for _, want := range []string{"sync-to-a-appA", "A平台", "registry.example.com",
		"appA-wallet-backend", "20260917084842-6", "4 秒", "Execution 22199", "实时通知"} {
		if !strings.Contains(got, want) {
			t.Errorf("文案缺少 %q：\n%s", want, got)
		}
	}
}

/*
🔴 耗时查不到时**整行不显示**，绝不显示「0 秒」。

「0 秒」是一个事实陈述（这次复制瞬间完成），「没查到」是数据缺失，
两者不能长成一个样 —— 老脚本的三个失败分支都渲染成「同步 0 个」，
结果"没拿到结果"看起来像"这次没事"，正是要避免的。
*/
func TestUnknownDurationIsOmittedNotZero(t *testing.T) {
	r := sample()
	r.Duration = 0
	got := Text(r)
	if strings.Contains(got, "0 秒") {
		t.Errorf("耗时未知时不能显示「0 秒」：\n%s", got)
	}
	if !strings.Contains(got, "触发") {
		t.Errorf("耗时未知时仍要说明触发方式：\n%s", got)
	}
}

// 🔴 取不到版本号要显式写出来 —— 少一行会被读成「这个镜像没推过去」
func TestMissingTagIsSpelledOut(t *testing.T) {
	r := sample()
	r.OK = []Image{{Service: "appA-bi-frontend"}}
	got := Text(r)
	if !strings.Contains(got, "未取到版本号") {
		t.Errorf("没有版本号时必须显式说明：\n%s", got)
	}
}

// 失败原因要带进卡片：老脚本只报「失败 N 个」，收到的人还得自己去 Harbor 翻
func TestFailureReasonIsIncluded(t *testing.T) {
	r := sample()
	r.Level = LevelFailed
	r.Failed = 1
	r.Bad = []Image{{Service: "appA-wallet-backend", Tag: "t-8",
		Reason: "unauthorized to access repository\n  detail: robot has no push permission"}}
	got := Text(r)
	if !strings.Contains(got, "unauthorized to access repository") {
		t.Errorf("失败原因必须出现在文案里：\n%s", got)
	}
	// 多行日志要压平，否则一条通知撑成一屏
	if strings.Contains(got, "\n  detail:") {
		t.Errorf("失败原因里的换行必须压平：\n%s", got)
	}
}

// 失败列表排在成功列表前面：17 成功 2 失败时，要看的是那 2 条
func TestFailuresListedFirst(t *testing.T) {
	r := sample()
	r.Level = LevelFailed
	r.OK = []Image{{Service: "ok-svc", Tag: "1"}}
	r.Bad = []Image{{Service: "bad-svc", Tag: "2"}}
	got := Text(r)
	if strings.Index(got, "bad-svc") > strings.Index(got, "ok-svc") {
		t.Errorf("失败镜像应排在成功镜像前面：\n%s", got)
	}
}

/*
截断要说清「共几个、还剩几个没列」。

老脚本恒写「（前10个）」：1 个镜像时看着莫名其妙，19 个时又看不出漏了多少 ——
而"少推的那一个"正是对账要问的。
*/
func TestTruncationStatesTotals(t *testing.T) {
	r := sample()
	r.OK = nil
	for i := 0; i < 19; i++ {
		r.OK = append(r.OK, Image{Service: "svc", Tag: "t"})
	}
	r.Succeeded, r.Total = 19, 19
	got := Text(r)
	if !strings.Contains(got, "共 19 个") || !strings.Contains(got, "前 10 个") {
		t.Errorf("截断时要说明总数和列出条数：\n%s", got)
	}
	if !strings.Contains(got, "其余 9 个未列出") {
		t.Errorf("要说明还剩多少没列：\n%s", got)
	}
	// 没超过上限时不该出现「前 10 个」这种将就说法
	small := sample()
	if s := Text(small); strings.Contains(s, "前 10 个") {
		t.Errorf("只有 1 个镜像时不该写「前 10 个」：\n%s", s)
	}
}

/*
🔴 兜底补发必须**自己写同步时间**并标明来源。

采集器最晚晚 30 分钟才发现，而飞书上那个时间戳是消息送达时间 ——
不标的话，一条半小时前的同步看起来像刚刚发生。
*/
func TestBackfillIsLabeled(t *testing.T) {
	r := sample()
	r.Realtime = false
	got := Text(r)
	if !strings.Contains(got, "兜底补发") {
		t.Errorf("补发的通知必须标明，否则时间会骗人：\n%s", got)
	}
	if !strings.Contains(got, "2026-09-17 16:49:36") {
		t.Errorf("必须写明同步发生的时刻：\n%s", got)
	}
}

// 规则没绑平台时如实说「未绑定平台」，不留空白
func TestUnboundOrgIsExplicit(t *testing.T) {
	r := sample()
	r.OrgName = ""
	if got := Text(r); !strings.Contains(got, "未绑定平台") {
		t.Errorf("没绑平台要如实写出来：\n%s", got)
	}
}

/*
🔴 终态卡片**必须有耗时**。

用户 2026-09-17 的原话：「原来的都有耗时，不可能改版本后，没有发送耗时」。
Harbor 在 execution 还没收尾时 end_time 是空的 —— 那时不该发卡（等终态），
而万一终态了 end_time 仍为空，就用"现在"估一个并标 ≈，而不是整行消失。
*/
func TestDurationFallback(t *testing.T) {
	start := time.Date(2026, 9, 17, 17, 2, 0, 0, time.UTC)
	end := start.Add(36 * time.Second)
	now := start.Add(50 * time.Second)

	// ① 有结束时间 → 权威值，不标 ≈
	if d, approx := DurationOf(start, end, now, true); d != 36*time.Second || approx {
		t.Errorf("有 end_time 时应取 end-start 且不标估算，实得 %v approx=%v", d, approx)
	}
	// ② 终态但没有结束时间 → 用 now 估，标 ≈
	if d, approx := DurationOf(start, time.Time{}, now, true); d != 50*time.Second || !approx {
		t.Errorf("终态缺 end_time 应估算并标 ≈，实得 %v approx=%v", d, approx)
	}
	// ③ 还在进行中 → 不算。算出来的是"已经跑了多久"，不是耗时
	if d, _ := DurationOf(start, time.Time{}, now, false); d != 0 {
		t.Errorf("进行中不该给耗时（那是已用时长，不是结果），实得 %v", d)
	}
	// ④ 连开始时间都没有 → 0，卡片整行不显示
	if d, _ := DurationOf(time.Time{}, time.Time{}, now, true); d != 0 {
		t.Errorf("没有开始时间时不能编一个耗时出来，实得 %v", d)
	}
}

// ≈ 要出现在文案里，别让人拿估算值去对 SLA
func TestApproxDurationIsMarked(t *testing.T) {
	r := sample()
	r.Duration, r.Approx = 36*time.Second, true
	got := Text(r)
	if !strings.Contains(got, "≈ 36 秒") {
		t.Errorf("估算的耗时要标 ≈：\n%s", got)
	}
}

/*
一次同步 100 个镜像 → **一张卡**，列 10 条，并说清还有 90 条没列。

Harbor 是每推一个镜像发一次 webhook，真按事件发就是 100 张卡，
而飞书自定义机器人限频 100 次/分钟、5 次/秒 —— 必然有发不出去的。
*/
func TestHundredImagesStayOneCard(t *testing.T) {
	r := sample()
	r.OK = nil
	for i := 0; i < 100; i++ {
		r.OK = append(r.OK, Image{Service: "svc-" + string(rune('a'+i%26)), Tag: "20260917-1"})
	}
	r.Total, r.Succeeded = 100, 100
	got := Text(r)
	if !strings.Contains(got, "共 100 个") || !strings.Contains(got, "其余 90 个未列出") {
		t.Errorf("100 个镜像要汇总成一张卡并说清截断：\n%s", got)
	}
	if n := strings.Count(got, "20260917-1"); n != 10 {
		t.Errorf("应只列 10 条版本号，实得 %d 条", n)
	}
}
