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
ShouldNotify 分级规则。

规则本身很短，但每一条都有理由，改之前先看理由：

	失败              → 一律发。这是通知存在的意义。
	手动触发 + 成功    → 发。**人正等着结果**：他刚点了同步按钮，
	                    不告诉他成功了，他会去 Harbor 界面自己翻。
	自动触发 + 成功    → 只入库不发。自动同步一天几十次，全发出来的结果是
	                    群里刷屏，然后**所有人把这个群设成免打扰** ——
	                    于是真正的失败也没人看见。这条规则是在保护失败通知的可见性。
	触发方式认不出     → 🔴 **发，并记 WARN**。

🔴 最后一条是刻意的。Harbor 各版本对 trigger 的拼法不统一
（manual / event_based / event-based / scheduled / schedule / cron…），
认不出的时候有两种选择：

	当成自动 → 静默丢弃。万一它其实是手动的，人就永远等不到回音，
	          而且**没有任何痕迹**能让人发现规则漏了一种拼法。
	当成需要发 → 多发几条消息，有点吵，但那条 WARN 会告诉我们
	          「出现了没见过的 trigger 取值」，于是能把它补进规则表。

多发一条的代价是被吐槽，漏发一条的代价是没人知道 —— 所以选后者。
*/
func ShouldNotify(triggerType, status string, manualRun bool) Decision {
	if IsFailedStatus(status) {
		return Decision{Send: true, Reason: "同步失败，一律通知"}
	}

	// manualRun 是**我们这边**的手动触发（有人点了「立即拉取」），
	// 与 Harbor 里那条规则自身的 trigger_type 是两回事，两者任一为手动都算
	switch normalizeTrigger(triggerType) {
	case "manual":
		return Decision{Send: true, Reason: "手动触发且成功，有人在等结果"}
	case "event_based", "scheduled":
		if manualRun {
			return Decision{Send: true, Reason: "本次是人工发起的拉取，成功也回一条"}
		}
		return Decision{Send: false, Reason: "自动触发且成功，只入库不通知（避免刷屏淹没失败通知）"}
	default:
		return Decision{
			Send: true, Warn: true,
			Reason: fmt.Sprintf("触发方式认不出（%q），按需要通知处理 —— 宁可多发也不能静默丢弃", triggerType),
		}
	}
}

// normalizeTrigger 归一化 Harbor 的触发方式取值。
//
// ⚠️ 认不出时返回空串而不是猜一个 —— 猜错会让 ShouldNotify 走进错误的分支，
// 而返回空串会落到 default，那一支会发通知并记 WARN。
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

// Text 拼一条通知文案。
//
// 🔴 必须写明**是手动还是自动**：收到消息的人第一反应是「这是我刚才点的那次吗」，
// 不写的话他得去界面上比时间戳。
func Text(lv Level, org, policy, trigger string, total, succeeded, failed int, detail string) string {
	var b strings.Builder
	if lv == LevelFailed {
		b.WriteString("🔴 镜像同步失败\n")
	} else {
		b.WriteString("✅ 镜像同步完成\n")
	}
	if org != "" {
		b.WriteString("平台：" + org + "\n")
	}
	b.WriteString("规则：" + policy + "\n")
	b.WriteString("触发：" + triggerText(trigger) + "\n")
	b.WriteString(fmt.Sprintf("结果：成功 %d / 失败 %d / 共 %d\n", succeeded, failed, total))
	if detail != "" {
		b.WriteString("详情：" + detail)
	}
	return strings.TrimRight(b.String(), "\n")
}

func triggerText(s string) string {
	switch normalizeTrigger(s) {
	case "manual":
		return "手动"
	case "event_based":
		return "事件触发"
	case "scheduled":
		return "定时"
	default:
		// 认不出时把原值也带出来，好让人知道该补哪种拼法
		return "未识别（" + s + "）"
	}
}
