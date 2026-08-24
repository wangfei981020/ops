package collector

import (
	"context"
	"time"

	"ops-version-backend/internal/metrics"
	"ops-version-backend/logx"
	"ops-version-backend/notify"
	"ops-version-backend/providers"
)

// notifyExecution 按分级规则决定要不要为这条执行发通知，并落一条记录。
//
// 🔴 **无论发不发都要落记录**（三态 sent/skipped/failed）。
// 只在发送时记录的话，人来问「这次同步怎么没通知我」时无从回答 ——
// 而「按规则不该发」和「该发但失败了」是完全不同的两件事。
func (c *Collector) notifyExecution(ctx context.Context, execRef int64, orgID int64,
	orgName, policyName string, e providers.SyncExecution, manualRun bool,
) {
	d := notify.ShouldNotify(e.TriggerType, e.Status, manualRun)
	if d.Warn {
		// 🔴 认不出的 trigger 必须留痕，否则永远发现不了规则漏了一种拼法
		logx.Warn("notify", "unknown_trigger", map[string]any{
			"trigger": e.TriggerType, "policy": policyName, "reason": d.Reason})
	}

	level := notify.LevelOK
	if notify.IsFailedStatus(e.Status) {
		level = notify.LevelFailed
	}

	if !d.Send {
		_ = c.st.SaveNotifyRecord(ctx, 0, execRef, string(level), e.TriggerType,
			"skipped", d.Reason, "", "", 0)
		metrics.NotifyTotal.WithLabelValues(string(level), "skipped").Inc()
		return
	}

	detail := ""
	if e.Failed > 0 {
		detail = "有镜像没推过去，去「镜像同步」页看执行记录"
	}
	text := notify.Text(level, orgName, policyName, e.TriggerType,
		e.Total, e.Succeeded, e.Failed, detail)

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
		// 渠道绑了组织就只发那个组织的
		if ch.OrgID != nil && *ch.OrgID != orgID {
			continue
		}
		hook, err := c.dec.Decrypt(ch.WebhookEnc)
		if err != nil {
			_ = c.st.SaveNotifyRecord(ctx, ch.ID, execRef, string(level), e.TriggerType,
				"failed", d.Reason, "webhook 解密失败："+err.Error(), text, 0)
			metrics.NotifyTotal.WithLabelValues(string(level), "failed").Inc()
			continue
		}
		attempts, sendErr := deliver(ctx, hook, text)
		state, errMsg := "sent", ""
		if sendErr != nil {
			state, errMsg = "failed", sendErr.Error()
			logx.Warn("notify", "deliver_failed", map[string]any{
				"channel": ch.Name, "attempts": attempts, "err": errMsg})
		} else {
			sent = true
		}
		_ = c.st.SaveNotifyRecord(ctx, ch.ID, execRef, string(level), e.TriggerType,
			state, d.Reason, errMsg, text, attempts)
		metrics.NotifyTotal.WithLabelValues(string(level), state).Inc()
	}

	// 🔴 该发但一个渠道都没有 —— 这不是"发成功了"，也不是"不用发"。
	//    不记的话，界面上看不出「配置缺失导致失败通知从来没送出去过」，
	//    而那恰恰是最危险的静默失效。
	if !sent && len(chans) == 0 {
		_ = c.st.SaveNotifyRecord(ctx, 0, execRef, string(level), e.TriggerType,
			"failed", d.Reason, "没有配置任何通知渠道，消息未送出", text, 0)
		metrics.NotifyTotal.WithLabelValues(string(level), "failed").Inc()
	}
}

// deliver 投递，失败重试。返回实际尝试次数。
//
// ⚠️ 只重试 3 次、间隔很短：通知的价值随时间衰减，
// 一条迟到十分钟的失败告警意义已经不大，而卡在这里会拖住整个采集循环。
// webhook 被重置、机器人被移出群这类错误，重试多少次都是同样结果。
func deliver(ctx context.Context, webhook, text string) (int, error) {
	var last error
	for i := 1; i <= 3; i++ {
		if err := notify.SendFeishu(webhook, text); err == nil {
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
