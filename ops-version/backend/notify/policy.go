package notify

import (
	"fmt"
	"strings"
)

// Decision 一条同步执行要不要发通知。
type Decision struct {
	Send bool
	// Reason 为什么发/不发。写进日志，方便回答「为什么这条没发」
	Reason string
	// Warn 需要额外记一条 WARN —— 目前只有「触发方式认不出」会置位
	Warn bool
}

// Level 消息的紧要程度，决定文案前缀。
type Level string

const (
	LevelFailed Level = "failed"
	LevelOK     Level = "ok"
)

/*
ShouldNotify 判定要不要发。

**只看一件事：这条复制规则有没有开通知。**

	开   → 成功、失败，手动、事件、定时，一律发
	关   → 一条都不发（失败也不发）

# 为什么不再按触发方式分级

原来这里有一套分级：失败一律发、手动成功发、自动成功不发。它是用**少发**
来防刷屏的，而代价是两头不讨好 —— 你不关心的规则（别人手动点一次同步）
照样发到你群里，你关心的规则自动触发成功时反而不发。

现在防刷屏由白名单承担：只有人勾过的规则才发。白名单比按触发方式猜精确得多，
于是「成功也发」不再等于刷屏。

🔴 **关掉的规则连失败都不发**，这是用户明确要的取舍（2026-09-17），不是疏漏。
改之前先问清楚 —— 这是整套通知里唯一会**主动丢掉失败信号**的地方。

⚠️ 触发方式认不出时仍然记 WARN（Harbor 各版本拼法不统一：manual /
event_based / event-based / scheduled / schedule / cron…）。它不再影响发不发，
但"出现了没见过的取值"必须留痕，否则文案里那行「触发方式」会一直显示成
「未识别」而没人知道该补哪一种拼法。
*/
func ShouldNotify(notifyEnabled bool, triggerType string) Decision {
	if !notifyEnabled {
		return Decision{Send: false, Reason: "该规则未开启通知（在「镜像同步」页逐条勾选）"}
	}
	if normalizeTrigger(triggerType) == "" {
		return Decision{
			Send: true, Warn: true,
			Reason: fmt.Sprintf("该规则已开启通知；触发方式认不出（%q），文案里会显示成「未识别」", triggerType),
		}
	}
	return Decision{Send: true, Reason: "该规则已开启通知，成功与失败都发"}
}

// normalizeTrigger 归一化 Harbor 的触发方式取值。
//
// ⚠️ 认不出时返回空串而不是猜一个 —— 猜一个会让文案显示成一个**确定但错误**的
// 触发方式，而返回空串会让它显示「未识别（原值）」，人一眼看得出是我们没认出来。
func normalizeTrigger(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "manual":
		return "manual"
	case "event", "event_based", "event-based", "eventbased":
		return "event_based"
	case "scheduled", "schedule", "cron", "timed":
		return "scheduled"
	default:
		return ""
	}
}

// IsFailedStatus Harbor 各版本的失败取值。
func IsFailedStatus(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "failed", "failure", "error", "stopped":
		return true
	}
	return false
}

func triggerText(s string) string {
	switch normalizeTrigger(s) {
	case "manual":
		return "手动触发"
	case "event_based":
		// 「（自动）」是照搬老 harbor-replication 的措辞 —— 收通知的人第一反应是
		// 「这是我刚点的那次吗」，写「事件触发」还要想一下，写「（自动）」一眼就否掉了
		return "事件触发（自动）"
	case "scheduled":
		return "定时触发"
	default:
		// 认不出时把原值也带出来，好让人知道该补哪种拼法
		return "未识别（" + s + "）"
	}
}
