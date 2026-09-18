package notify

import (
	"encoding/json"
	"fmt"
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
🔴 不再截断：一次同步 19 个镜像就要列出 19 个。

用户 2026-09-18 的要求：「全部同步的你要全部发出来，不然查询版本号不好查」。
原来写死列 10 个，19 个的时候少看 9 个 —— 而那份完整清单正是通知的用处。
*/
func TestSinglePageListsEverything(t *testing.T) {
	r := sample()
	r.OK = nil
	for i := 0; i < 19; i++ {
		r.OK = append(r.OK, Image{Service: fmt.Sprintf("svc-%02d", i), Tag: fmt.Sprintf("t-%02d", i)})
	}
	r.Succeeded, r.Total = 19, 19
	pages := Split(r)
	if len(pages) != 1 {
		t.Fatalf("19 个镜像应该一张卡装下，实得 %d 张", len(pages))
	}
	got := Text(pages[0])
	if strings.Contains(got, "未列出") || strings.Contains(got, "前 10 个") {
		t.Errorf("不该再有截断字样：\n%s", got)
	}
	for i := 0; i < 19; i++ {
		if !strings.Contains(got, fmt.Sprintf("t-%02d", i)) {
			t.Errorf("缺第 %d 个镜像的版本号：\n%s", i, got)
		}
	}
	// 单片不显示「(1/1)」，那是噪音
	if strings.Contains(got, "(1/1)") {
		t.Errorf("单片不该显示页码：\n%s", got)
	}
}

/*
100 个镜像 → 4 片，每片 25 个，合起来一个不少；页码和序号区间都要对。
*/
func TestHundredImagesSplitIntoPages(t *testing.T) {
	r := sample()
	r.OK = nil
	for i := 0; i < 100; i++ {
		r.OK = append(r.OK, Image{Service: fmt.Sprintf("svc-%03d", i), Tag: fmt.Sprintf("t-%03d", i)})
	}
	r.Succeeded, r.Total = 100, 100
	pages := Split(r)
	if len(pages) != 4 {
		t.Fatalf("100 个镜像按每片 25 个应切成 4 片，实得 %d", len(pages))
	}
	seen := 0
	for i, p := range pages {
		txt := Text(p)
		if !strings.Contains(txt, fmt.Sprintf("(%d/4)", i+1)) {
			t.Errorf("第 %d 片缺页码：\n%s", i+1, txt)
		}
		// 头部只在第一片
		if i == 0 && !strings.Contains(txt, "平台") {
			t.Errorf("第一片必须有头部：\n%s", txt)
		}
		if i > 0 && strings.Contains(txt, "耗时") {
			t.Errorf("续页不该重复头部：\n%s", txt)
		}
		if !strings.Contains(txt, fmt.Sprintf("第 %d–%d 个", i*25+1, i*25+25)) {
			t.Errorf("第 %d 片的序号区间不对：\n%s", i+1, txt)
		}
		seen += len(p.OK)
	}
	if seen != 100 {
		t.Errorf("分片后镜像总数应为 100，实得 %d —— 有内容被丢了", seen)
	}
}

/*
🔴 超过总上限（8 片 = 200 个）时，最后一片必须说清还剩多少、去哪儿看 ——
不能让人以为"就这么多"。
*/
func TestOverMaxCardsIsSpelledOut(t *testing.T) {
	r := sample()
	r.OK = nil
	for i := 0; i < 250; i++ {
		r.OK = append(r.OK, Image{Service: fmt.Sprintf("svc-%03d", i), Tag: "t"})
	}
	r.Succeeded, r.Total = 250, 250
	pages := Split(r)
	if len(pages) != 8 {
		t.Fatalf("250 个镜像应封顶在 8 片，实得 %d", len(pages))
	}
	last := Text(pages[7])
	if !strings.Contains(last, "其余 50 个未列出") || !strings.Contains(last, "Execution") {
		t.Errorf("最后一片要说明剩多少、去哪儿看：\n%s", last)
	}
}

/*
🔴 失败镜像永远在第一片、永远全量，不参与分片 ——
要处理的东西不能被翻页藏起来。
*/
func TestFailuresAlwaysOnFirstPage(t *testing.T) {
	r := sample()
	r.Level = LevelFailed
	r.OK = nil
	for i := 0; i < 60; i++ {
		r.OK = append(r.OK, Image{Service: fmt.Sprintf("ok-%02d", i), Tag: "t"})
	}
	r.Bad = []Image{
		{Service: "bad-wallet", Tag: "t-8", Reason: "manifest unknown"},
		{Service: "bad-bi", Tag: "t-3", Reason: "unauthorized"},
	}
	r.Succeeded, r.Failed, r.Total = 60, 2, 62
	pages := Split(r)
	if len(pages) < 2 {
		t.Fatalf("60 个成功镜像该分片，实得 %d", len(pages))
	}
	if !strings.Contains(Text(pages[0]), "bad-wallet") {
		t.Error("失败镜像必须在第一片")
	}
	for i := 1; i < len(pages); i++ {
		if strings.Contains(Text(pages[i]), "bad-wallet") {
			t.Errorf("第 %d 片重复了失败清单", i+1)
		}
	}
}

/*
🔴 单张卡序列化后必须小于飞书的 20KB 硬限制 —— 超了整条拒收，
表现是"这一页没发出来"，比多发一页糟得多。服务名特别长时尤其危险。
*/
func TestNoPageExceedsFeishuLimit(t *testing.T) {
	r := sample()
	r.OK = nil
	long := strings.Repeat("very-long-service-name-segment-", 8) // ~250 字节
	for i := 0; i < 200; i++ {
		r.OK = append(r.OK, Image{Service: fmt.Sprintf("%s-%03d", long, i), Tag: "20260918021500-40"})
	}
	r.Succeeded, r.Total = 200, 200
	for i, p := range Split(r) {
		raw, err := json.Marshal(Card(p))
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 20<<10 {
			t.Errorf("第 %d 片 %d 字节，超过飞书 20KB 上限", i+1, len(raw))
		}
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
