package notify

import "testing"

/*
🔴 关掉通知的规则，**连失败都不发**。

这是整套通知里唯一一处会主动丢掉失败信号的地方，也最容易被后来的人
"好心"改回去（"失败总该发一条吧"）。用户 2026-09-17 明确要的就是这个语义：
通知与否只看规则开关，白名单之外一条都不要。

要改这条之前先去问人，别改测试。
*/
func TestDisabledPolicyStaysSilentEvenOnFailure(t *testing.T) {
	for _, trig := range []string{"manual", "event_based", "scheduled", "", "某种没见过的写法"} {
		d := ShouldNotify(false, trig)
		if d.Send {
			t.Errorf("trigger=%q 规则没开通知就一条都不该发，实得发送（%s）", trig, d.Reason)
		}
		if d.Reason == "" {
			t.Errorf("trigger=%q 不发也要写明原因，否则「这次怎么没通知我」答不上来", trig)
		}
	}
}

// 开了通知的规则：成功也发。
//
// 🔴 原来「自动触发且成功」是不发的，那套分级已经被规则白名单取代 ——
// 防刷屏由「只勾几条规则」承担，不再靠少发。
func TestEnabledPolicyNotifiesOnEveryTrigger(t *testing.T) {
	for _, trig := range []string{"manual", "event_based", "event-based", "scheduled", "cron", "schedule"} {
		d := ShouldNotify(true, trig)
		if !d.Send {
			t.Errorf("trigger=%q 规则开了通知就该发，实得不发（%s）", trig, d.Reason)
		}
	}
}

/*
触发方式**认不出时照发，并且记 WARN**。

Harbor 各版本拼法不统一。认不出不该影响发不发（那是规则开关的事），
但必须留痕：否则文案里那行「触发方式」会一直显示「未识别」而没人知道要补哪种拼法。
*/
func TestUnknownTriggerStillNotifiesAndWarns(t *testing.T) {
	for _, trig := range []string{"", "EVENT_BASED_V2", "手动", "unknown-thing"} {
		d := ShouldNotify(true, trig)
		if !d.Send {
			t.Errorf("trigger=%q 认不出不该影响发不发", trig)
		}
		if !d.Warn {
			t.Errorf("trigger=%q 认不出必须记 WARN，否则永远发现不了漏了一种拼法", trig)
		}
		t.Logf("  %-16q → %s", trig, d.Reason)
	}
}

// 已知取值不该误报 WARN，否则日志里全是狼来了
func TestKnownTriggersDoNotWarn(t *testing.T) {
	for _, trig := range []string{"manual", "event_based", "event-based", "scheduled", "cron"} {
		if d := ShouldNotify(true, trig); d.Warn {
			t.Errorf("trigger=%q 是已知取值，不该记 WARN", trig)
		}
	}
}

// 关掉的规则不必为 trigger 记 WARN —— 压根不会发，那条 WARN 只是噪音
func TestDisabledPolicyDoesNotWarn(t *testing.T) {
	if d := ShouldNotify(false, "没见过的写法"); d.Warn {
		t.Error("规则没开通知时不该因为 trigger 认不出而记 WARN")
	}
}

func TestIsFailedStatus(t *testing.T) {
	for _, s := range []string{"Failed", "failure", "Error", "Stopped", " failed "} {
		if !IsFailedStatus(s) {
			t.Errorf("%q 应判为失败", s)
		}
	}
	for _, s := range []string{"Succeeded", "Succeed", "InProgress", ""} {
		if IsFailedStatus(s) {
			t.Errorf("%q 不该判为失败", s)
		}
	}
}
