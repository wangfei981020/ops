package collector

import (
	"context"
	"time"

	"ops-version-backend/internal/metrics"
	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
	"ops-version-backend/notify"
	"ops-version-backend/providers"
)

// notifyExecution 按规则开关决定要不要为这条执行发通知，并落一条记录。
//
// 🔴 **无论发不发都要落记录**（三态 sent/skipped/failed）。
// 只在发送时记录的话，人来问「这次同步怎么没通知我」时无从回答 ——
// 而「这条规则没开通知」「已经由 webhook 实时发过了」「该发但失败了」
// 是三件完全不同的事。
//
// ⚠️ 这条路是**兜底**：正常情况下 webhook 会在复制结束的当下就发出去，
// 轮询到这里时已经发过了，于是记一条 skipped。只有 webhook 没送达
// （服务重启、网络断、令牌过期）才由这里补发，卡片上会标明是补发的。
func (c *Collector) notifyExecution(ctx context.Context, execRef int64, p store.PolicyRow,
	e providers.SyncExecution, tasks []providers.SyncTask,
) {
	d := notify.ShouldNotify(p.NotifyEnabled, e.TriggerType)
	if d.Warn {
		// 🔴 认不出的 trigger 必须留痕，否则永远发现不了 Harbor 换了一种拼法
		logx.Warn("notify", "unknown_trigger", map[string]any{
			"trigger": e.TriggerType, "policy": p.Name, "reason": d.Reason})
	}

	level := notify.LevelOK
	if notify.IsFailedStatus(e.Status) {
		level = notify.LevelFailed
	}
	rec := store.NotifyRecordInput{
		ExecRef: execRef, PolicyRef: p.Ref, HarborExecID: e.ExecID,
		Level: string(level), Trigger: e.TriggerType, Reason: d.Reason,
	}

	if !d.Send {
		rec.State = "skipped"
		_ = c.st.SaveNotifyRecord(ctx, rec)
		metrics.NotifyTotal.WithLabelValues(string(level), "skipped").Inc()
		return
	}

	// 🔴 去重：webhook 已经实时发过这条 execution 就不再发。
	//
	//    「成功也发」之后两条路会同时命中同一次复制 —— webhook 收到事件发一张，
	//    30 分钟后轮询看到这条 execution 是新的又发一张，内容一模一样。
	//    以前不撞车纯属巧合：那时采集器对「自动触发且成功」不发。
	//
	// ⚠️ 查询失败时**照发不误**：漏发一条失败通知的代价，比多发一条重复的大得多。
	done, err := c.st.AlreadyNotified(ctx, p.Ref, e.ExecID)
	if err != nil {
		logx.Warn("notify", "dedup_check_failed", map[string]any{
			"policy": p.Name, "exec": e.ExecID, "err": err.Error(),
			"note": "查不到是否已通知，按未通知处理 —— 宁可重复也不能漏"})
	}
	if done {
		rec.State = "skipped"
		rec.Reason = "webhook 已实时通知过这次执行，不重复发送"
		_ = c.st.SaveNotifyRecord(ctx, rec)
		metrics.NotifyTotal.WithLabelValues(string(level), "skipped").Inc()
		return
	}

	rep := notify.Replication{
		Level: level, Policy: p.Name, OrgName: p.OrgName, DestRegistry: p.DestRegistry,
		Trigger: e.TriggerType, ExecID: e.ExecID,
		Total: e.Total, Succeeded: e.Succeeded, Failed: e.Failed,
		SyncedAt: e.EndedAt,
		// 🔴 走到这里就说明 webhook 那条路没送达，如实标成补发。
		//    轮询最晚晚 30 分钟，而飞书上那个时间戳是**消息送达时间** ——
		//    不说明的话，人会以为刚刚才同步过。
		Realtime: false,
	}
	if rep.SyncedAt.IsZero() {
		rep.SyncedAt = e.StartedAt
	}
	if !e.StartedAt.IsZero() && !e.EndedAt.IsZero() && e.EndedAt.After(e.StartedAt) {
		rep.Duration = e.EndedAt.Sub(e.StartedAt)
	}
	for _, t := range tasks {
		im := notify.Image{Service: t.ServiceKey, Tag: t.Tag}
		if providers.IsFailed(t.Status) {
			im.Reason = t.ErrMsg
			rep.Bad = append(rep.Bad, im)
		} else {
			rep.OK = append(rep.OK, im)
		}
	}
	text := notify.Text(rep)
	card := notify.Card(rep)
	rec.Content = text

	chans, err := c.st.ListChannels(ctx)
	if err != nil {
		logx.Error("notify", "list_channels_failed", map[string]any{"err": err.Error()})
		return
	}
	sent := false
	for _, ch := range chans {
		if !ch.Enabled || ch.WebhookEnc == "" {
			continue
		}
		// 渠道绑了平台就只发那个平台的。
		// ⚠️ 与规则开关是「与」关系：规则开了通知，还要这个渠道收得到它。
		if ch.OrgID != nil && (p.OrgID == nil || *ch.OrgID != *p.OrgID) {
			continue
		}
		r := rec
		r.ChannelID = ch.ID
		hook, err := c.dec.Decrypt(ch.WebhookEnc)
		if err != nil {
			r.State, r.ErrMsg = "failed", "webhook 解密失败："+err.Error()
			_ = c.st.SaveNotifyRecord(ctx, r)
			metrics.NotifyTotal.WithLabelValues(string(level), "failed").Inc()
			continue
		}
		attempts, sendErr := deliver(ctx, hook, card)
		r.State, r.Attempts = "sent", attempts
		if sendErr != nil {
			r.State, r.ErrMsg = "failed", sendErr.Error()
			logx.Warn("notify", "deliver_failed", map[string]any{
				"channel": ch.Name, "attempts": attempts, "err": r.ErrMsg})
		} else {
			sent = true
		}
		_ = c.st.SaveNotifyRecord(ctx, r)
		metrics.NotifyTotal.WithLabelValues(string(level), r.State).Inc()
	}

	// 🔴 该发但一个渠道都没有 —— 这不是"发成功了"，也不是"不用发"。
	//    不记的话，界面上看不出「配置缺失导致失败通知从来没送出去过」，
	//    而那恰恰是最危险的静默失效。
	if !sent && len(chans) == 0 {
		rec.State = "failed"
		rec.ErrMsg = "没有配置任何通知渠道，消息未送出"
		_ = c.st.SaveNotifyRecord(ctx, rec)
		metrics.NotifyTotal.WithLabelValues(string(level), "failed").Inc()
	}
}

// deliver 投递，失败重试。返回实际尝试次数。
//
// ⚠️ 只重试 3 次、间隔很短：通知的价值随时间衰减，
// 一条迟到十分钟的失败告警意义已经不大，而卡在这里会拖住整个采集循环。
// webhook 被重置、机器人被移出群这类错误，重试多少次都是同样结果。
func deliver(ctx context.Context, webhook string, card map[string]any) (int, error) {
	var last error
	for i := 1; i <= 3; i++ {
		if err := notify.SendFeishuCard(webhook, card); err == nil {
			return i, nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return i, ctx.Err()
		case <-time.After(time.Duration(i) * 2 * time.Second):
		}
	}
	return 3, last
}
