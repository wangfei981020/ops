package collector

import (
	"context"
	"fmt"
	"time"

	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
	"ops-version-backend/notify"
)

// SendFailureMentions 同步失败的卡片发完之后，在 Teams 渠道追发一条 @ 提醒。
//
// 两条投递链路（webhook 实时 / 采集器兜底）共用。调用方保证：渠道是 Teams、
// 这次是失败、失败卡片至少发出去了一片（卡片都没到，提醒「请查看上一条」没有意义）。
//
// 🔴 提醒失败只记一条 failed 投递记录，不影响失败卡片本身的结果，
// 也不让「sent」判定变成假的 —— 告警已经送达，提醒是锦上添花。
func SendFailureMentions(ctx context.Context, st *store.Store, ch store.NotifyChannel,
	hook string, rep notify.Replication, rec store.NotifyRecordInput) {

	contacts, err := st.ChannelMentions(ctx, ch.ID)
	if err != nil {
		logx.Warn("notify", "list_mentions_failed", map[string]any{
			"channel": ch.Name, "err": err.Error()})
		return
	}
	if len(contacts) == 0 {
		return
	}
	ms := make([]notify.TeamsMention, 0, len(contacts))
	for _, c := range contacts {
		ms = append(ms, notify.TeamsMention{Name: c.Name, Account: c.TeamsAccount})
	}
	// 和分片之间一样留个间隔，别和最后一片挤在同一时刻
	time.Sleep(pageInterval)

	r := rec
	r.ChannelID, r.State, r.Attempts = ch.ID, "sent", 1
	r.Reason = fmt.Sprintf("同步失败 @ 提醒：%s", notify.TeamsMentionLog(ms))
	r.Content = r.Reason
	if err := notify.SendTeams(hook, notify.TeamsMentionCard(rep, ms)); err != nil {
		r.State, r.ErrMsg = "failed", err.Error()
		logx.Warn("notify", "mention_failed", map[string]any{
			"channel": ch.Name, "err": err.Error()})
	} else {
		r.Reason += "；" + notify.TeamsDeliveredNote
	}
	_ = st.SaveNotifyRecord(ctx, r)
}
