package notify

import "testing"

// 🔴 失败一律发 —— 这是通知存在的意义
func TestFailedAlwaysNotifies(t *testing.T) {
	for _, trig := range []string{"manual", "event_based", "scheduled", "", "某种没见过的写法"} {
		for _, st := range []string{"Failed", "failure", "Error", "Stopped"} {
			d := ShouldNotify(trig, st, false)
			if !d.Send {
				t.Errorf("trigger=%q status=%q 失败必须通知，实得不发（%s）", trig, st, d.Reason)
			}
		}
	}
}

// 手动触发且成功要发：人刚点了按钮，正等着结果
func TestManualSuccessNotifies(t *testing.T) {
	d := ShouldNotify("manual", "Succeeded", false)
	if !d.Send {
		t.Errorf("手动成功应通知，实得不发：%s", d.Reason)
	}
}

// 🔴 自动触发且成功**不发** —— 一天几十次全发出来，
// 结果是群被设成免打扰，然后真正的失败也没人看见。
// 这条规则是在保护失败通知的可见性。
func TestAutoSuccessStaysQuiet(t *testing.T) {
	for _, trig := range []string{"event_based", "event-based", "scheduled", "cron", "schedule"} {
		d := ShouldNotify(trig, "Succeeded", false)
		if d.Send {
			t.Errorf("trigger=%q 自动成功不该通知，实得发送（%s）", trig, d.Reason)
		}
	}
}

// 我们这边有人点了「立即拉取」，即使规则本身是自动的也回一条
func TestManualRunOverridesQuiet(t *testing.T) {
	d := ShouldNotify("scheduled", "Succeeded", true)
	if !d.Send {
		t.Errorf("人工发起的拉取应回一条，实得不发：%s", d.Reason)
	}
}

/*
🔴 最重要的一条：触发方式**认不出时要发，并且记 WARN**。

Harbor 各版本的拼法不统一，认不出时有两个选择：

	当成自动 → 静默丢弃。万一它其实是手动的，人永远等不到回音，
	          而且没有任何痕迹能让人发现规则漏了一种拼法。
	当成要发 → 多几条消息，但那条 WARN 会告诉我们「出现了没见过的取值」。

多发一条被吐槽，漏发一条没人知道 —— 所以选后者。
*/
func TestUnknownTriggerNotifiesAndWarns(t *testing.T) {
	for _, trig := range []string{"", "EVENT_BASED_V2", "手动", "unknown-thing"} {
		d := ShouldNotify(trig, "Succeeded", false)
		if !d.Send {
			t.Errorf("trigger=%q 认不出时必须发，实得静默丢弃", trig)
		}
		if !d.Warn {
			t.Errorf("trigger=%q 认不出时必须记 WARN，否则永远发现不了规则漏了一种拼法", trig)
		}
		t.Logf("  %-16q → %s", trig, d.Reason)
	}
}

// 已知取值不该误报 WARN，否则日志里全是狼来了
func TestKnownTriggersDoNotWarn(t *testing.T) {
	for _, trig := range []string{"manual", "event_based", "event-based", "scheduled", "cron"} {
		if d := ShouldNotify(trig, "Succeeded", false); d.Warn {
			t.Errorf("trigger=%q 是已知取值，不该记 WARN", trig)
		}
	}
}

// 🔴 文案必须写明是手动还是自动：收到的人第一反应是
// 「这是我刚才点的那次吗」，不写他得去界面比时间戳
func TestTextSaysTrigger(t *testing.T) {
	got := Text(LevelFailed, "A公司", "推给A公司", "manual", 10, 8, 2, "wallet:t-114 推送被拒")
	for _, want := range []string{"手动", "A公司", "推给A公司", "失败 2", "wallet:t-114"} {
		if !contains(got, want) {
			t.Errorf("文案缺少 %q：\n%s", want, got)
		}
	}
	// 认不出的取值要把原值带出来，好让人知道该补哪种拼法
	got2 := Text(LevelOK, "", "x", "WEIRD_TRIGGER", 1, 1, 0, "")
	if !contains(got2, "WEIRD_TRIGGER") {
		t.Errorf("未识别的 trigger 必须把原值带出来：\n%s", got2)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
