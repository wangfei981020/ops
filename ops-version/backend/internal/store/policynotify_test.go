package store

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"ops-version-backend/providers"
)

func notifyTestDB(t *testing.T) (*Store, context.Context, int64) {
	t.Helper()
	dsn := os.Getenv("TEST_MIG_DSN")
	if dsn == "" {
		t.Skip("需要 TEST_MIG_DSN")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Store{db: db}
	ctx := context.Background()

	res, err := db.ExecContext(ctx, `
		INSERT INTO harbors (name, endpoint, username, enabled)
		VALUES ('zz-通知测试-harbor', 'https://registry.example.com', 'robot$x', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	harborID, _ := res.LastInsertId()
	t.Cleanup(func() {
		db.Exec(`DELETE FROM notify_records WHERE policy_ref IN
			(SELECT id FROM sync_policies WHERE harbor_id=?)`, harborID)
		db.Exec(`DELETE FROM sync_policies WHERE harbor_id=?`, harborID)
		db.Exec(`DELETE FROM harbors WHERE id=?`, harborID)
	})
	return s, ctx, harborID
}

/*
🔴🔴 采集器每轮都会把 Harbor 上的规则 upsert 回本站（SavePolicies）。
人勾的通知开关**绝不能**被那次 upsert 覆盖。

覆盖了的表现极其隐蔽：不报错、日志里也没有，只是"我明明开了通知，
过半小时又变回关了"，而中间那段时间的同步一条通知都没发。
org_id（绑定平台）当初就是因为同一个原因才被排除在 UPDATE 子句之外。
*/
func TestSavePoliciesDoesNotClobberNotifyFlag(t *testing.T) {
	s, ctx, harborID := notifyTestDB(t)
	ps := []providers.SyncPolicy{{
		PolicyID: 999101, Name: "zz-sync-a", DestRegistry: "https://registry.example.com",
		SrcProject: "appA", TriggerType: "event_based", Enabled: true,
	}}
	if err := s.SavePolicies(ctx, harborID, ps); err != nil {
		t.Fatal(err)
	}
	ref, err := s.PolicyRefOf(ctx, harborID, 999101)
	if err != nil {
		t.Fatal(err)
	}
	// 人在界面上打开通知
	if err := s.SetPolicyNotify(ctx, ref, true); err != nil {
		t.Fatal(err)
	}
	// 采集器又跑了一轮（规则名变了、Harbor 那边禁用了，都要照常同步回来）
	ps[0].Name = "zz-sync-a-改名"
	ps[0].Enabled = false
	if err := s.SavePolicies(ctx, harborID, ps); err != nil {
		t.Fatal(err)
	}
	p, err := s.PolicyByRef(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !p.NotifyEnabled {
		t.Error("采集器的 upsert 把人勾的通知开关覆盖掉了 —— 表现是「开了通知过一会儿又没了」，且无任何报错")
	}
	if p.Name != "zz-sync-a-改名" || p.Enabled {
		t.Errorf("Harbor 那边的字段该被同步回来，实得 name=%q enabled=%v", p.Name, p.Enabled)
	}
}

/*
🔴 去重：webhook 实时发过的那次 execution，采集器轮询到时不能再发一遍。

「成功也发」之后两条链路会同时命中同一次复制。以前不撞车是巧合 ——
那时采集器对「自动触发且成功」不发。
*/
func TestAlreadyNotifiedDedup(t *testing.T) {
	s, ctx, harborID := notifyTestDB(t)
	if err := s.SavePolicies(ctx, harborID, []providers.SyncPolicy{{
		PolicyID: 999102, Name: "zz-sync-b", DestRegistry: "https://registry.example.com",
		TriggerType: "event_based", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	ref, err := s.PolicyRefOf(ctx, harborID, 999102)
	if err != nil {
		t.Fatal(err)
	}

	if done, err := s.AlreadyNotified(ctx, ref, 22199); err != nil || done {
		t.Fatalf("还没发过就说发过了：done=%v err=%v", done, err)
	}
	// webhook 实时发成功
	if err := s.SaveNotifyRecord(ctx, NotifyRecordInput{
		PolicyRef: ref, HarborExecID: 22199, Level: "ok", Trigger: "event_based",
		State: "sent", Reason: "该规则已开启通知，成功与失败都发", Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if done, err := s.AlreadyNotified(ctx, ref, 22199); err != nil || !done {
		t.Errorf("webhook 发过之后应判为已通知：done=%v err=%v", done, err)
	}
	// 另一次 execution 不受影响
	if done, _ := s.AlreadyNotified(ctx, ref, 22200); done {
		t.Error("不同 execution 不该被去重掉")
	}
}

/*
🔴 只有 state='sent' 才算发过。

failed（该发但没送出去）算成"已通知"的话，webhook 投递失败后采集器就不补发了 ——
而 webhook 发失败恰恰是采集器这条兜底路径存在的全部理由。
skipped 同理：那是"按规则不该发"，不是"已经发了"。
*/
func TestOnlySentCountsAsNotified(t *testing.T) {
	s, ctx, harborID := notifyTestDB(t)
	if err := s.SavePolicies(ctx, harborID, []providers.SyncPolicy{{
		PolicyID: 999103, Name: "zz-sync-c", DestRegistry: "https://registry.example.com",
		TriggerType: "manual", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	ref, _ := s.PolicyRefOf(ctx, harborID, 999103)

	for _, st := range []string{"failed", "skipped"} {
		if err := s.SaveNotifyRecord(ctx, NotifyRecordInput{
			PolicyRef: ref, HarborExecID: 22300, Level: "failed", State: st,
			Reason: "测试", ErrMsg: "飞书拒绝了这条消息",
		}); err != nil {
			t.Fatal(err)
		}
		done, err := s.AlreadyNotified(ctx, ref, 22300)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			t.Errorf("state=%q 不该算作已通知，否则兜底补发会被抑制", st)
		}
	}
}

// execID 拿不到（0）时一律返回 false —— 0 不是真实的 execution id，
// 拿它当键会把所有"拿不到 id"的通知互相去重掉，表现是**随机丢通知**。
func TestZeroExecIDNeverDedups(t *testing.T) {
	s, ctx, harborID := notifyTestDB(t)
	if err := s.SavePolicies(ctx, harborID, []providers.SyncPolicy{{
		PolicyID: 999104, Name: "zz-sync-d", DestRegistry: "https://registry.example.com",
		TriggerType: "manual", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	ref, _ := s.PolicyRefOf(ctx, harborID, 999104)
	if err := s.SaveNotifyRecord(ctx, NotifyRecordInput{
		PolicyRef: ref, HarborExecID: 0, Level: "ok", State: "sent",
	}); err != nil {
		t.Fatal(err)
	}
	if done, _ := s.AlreadyNotified(ctx, ref, 0); done {
		t.Error("execution id 为 0 时不能去重")
	}
}

/*
🔴🔴 一次执行只允许一个人发卡 —— 而且必须扛得住**并发**。

Harbor 每推一个镜像发一次 webhook。execution 转终态的那一刻，
排队中的剩余事件会同时看到"已终态"，于是同时去发。
"先查 AlreadyNotified 再发"在这里必然漏：两个请求都查到"没发过"。

这条测试用 20 个 goroutine 同时抢，断言**恰好一个**抢到。
*/
func TestClaimNotifyIsAtomicUnderConcurrency(t *testing.T) {
	s, ctx, harborID := notifyTestDB(t)
	if err := s.SavePolicies(ctx, harborID, []providers.SyncPolicy{{
		PolicyID: 999105, Name: "zz-sync-claim", DestRegistry: "https://registry.example.com",
		TriggerType: "manual", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	ref, _ := s.PolicyRefOf(ctx, harborID, 999105)
	t.Cleanup(func() { _ = s.ReleaseNotifyClaim(ctx, ref, 22204) })

	const n = 20
	var wg sync.WaitGroup
	var won int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := s.ClaimNotify(ctx, ref, 22204, "webhook"); err == nil && got {
				atomic.AddInt64(&won, 1)
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Errorf("%d 个并发只该有 1 个抢到通知权，实得 %d —— 意味着一次同步会发 %d 张卡", n, won, won)
	}
}

/*
🔴 发送失败要把占位还回去，否则采集器那条兜底路径永远补不了这次执行 ——
而"webhook 发失败"恰恰是兜底存在的全部理由。
*/
func TestReleasedClaimCanBeRetaken(t *testing.T) {
	s, ctx, harborID := notifyTestDB(t)
	if err := s.SavePolicies(ctx, harborID, []providers.SyncPolicy{{
		PolicyID: 999106, Name: "zz-sync-release", DestRegistry: "https://registry.example.com",
		TriggerType: "manual", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	ref, _ := s.PolicyRefOf(ctx, harborID, 999106)
	t.Cleanup(func() { _ = s.ReleaseNotifyClaim(ctx, ref, 22205) })

	if got, _ := s.ClaimNotify(ctx, ref, 22205, "webhook"); !got {
		t.Fatal("第一次应该抢到")
	}
	if got, _ := s.ClaimNotify(ctx, ref, 22205, "collector"); got {
		t.Fatal("没释放之前别人不该抢到")
	}
	// webhook 一条都没发出去 → 归还
	if err := s.ReleaseNotifyClaim(ctx, ref, 22205); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ClaimNotify(ctx, ref, 22205, "collector"); !got {
		t.Error("归还之后采集器必须能补发，否则这次同步永远没通知")
	}
}

// execID 为 0（Harbor 没给）时不占位也不去重 —— 拿 0 当键会把所有
// "拿不到 id"的通知互相挡掉，表现是随机丢通知
func TestZeroExecIDAlwaysClaims(t *testing.T) {
	s, ctx, harborID := notifyTestDB(t)
	if err := s.SavePolicies(ctx, harborID, []providers.SyncPolicy{{
		PolicyID: 999107, Name: "zz-sync-zero", DestRegistry: "https://registry.example.com",
		TriggerType: "manual", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	ref, _ := s.PolicyRefOf(ctx, harborID, 999107)
	for i := 0; i < 3; i++ {
		if got, err := s.ClaimNotify(ctx, ref, 0, "webhook"); err != nil || !got {
			t.Errorf("execID=0 时每次都该放行（不去重），第 %d 次 got=%v err=%v", i+1, got, err)
		}
	}
}
