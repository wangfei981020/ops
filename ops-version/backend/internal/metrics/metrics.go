// Package metrics 暴露 Prometheus 指标。
//
// 只放**能驱动告警**的指标。为了好看堆一堆没人配告警的 gauge，
// 只会让抓取变贵、面板变乱，出事时反而找不到关键那条。
//
// 🔴 标签叫 org 而**不能叫 instance**：instance 是 Prometheus 的保留标签，
// 抓取时会被自动注入为 `instance="<pod_ip>:<port>"`。业务指标自带同名标签时，
// Prometheus 在 relabel 阶段把我们这份改名成 exported_instance —— 于是
// `sum by (instance) (...)` 分组分出来的是 Pod IP 而不是平台名，
// 告警文案里显示的也是 IP。这类冲突不报错、不掉数据，只是**悄悄换掉了含义**。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// CollectTotal 采集次数，按结果分类。
	// status 与 orgs.last_sync_status 用同一套取值，
	// 好让「界面上看到的失败」和「告警里看到的失败」是同一个口径。
	CollectTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "opsversion_collect_total",
		Help: "采集执行次数，按平台/环境/结果分类",
	}, []string{"org", "env", "status"})

	CollectDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "opsversion_collect_duration_seconds",
		Help:    "单次采集耗时",
		Buckets: []float64{1, 5, 15, 30, 60, 120, 300},
	}, []string{"org", "env"})

	// LastSuccessTimestamp 最近一次**成功**采集的 Unix 时间戳。
	//
	// 🔴 这是本系统最该配告警的一条：
	//     time() - opsversion_last_success_timestamp_seconds > 3600
	//
	// 对账最危险的失效方式不是「报错」，而是**某个平台悄悄停止更新**——
	// 界面上那一列会变成 no_data，但没人一直盯着界面。
	//
	// 用「最后成功时间」而不是「失败计数」做告警，是因为采集根本没跑起来时
	// （cron 挂了、Pod 一直 CrashLoop、平台被误删）**失败计数也不会增长**，
	// 那种情况下失败数是 0，看起来一切正常 —— 而这恰恰是最该被发现的故障。
	LastSuccessTimestamp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "opsversion_last_success_timestamp_seconds",
		Help: "最近一次成功采集的 Unix 时间戳（配 time()-x > 阈值 做数据陈旧告警）",
	}, []string{"org", "env"})

	// ServicesTotal 采到的服务数。
	// 突然掉到 0 通常不是「对方下线了所有服务」，而是 ns 规则被改坏了
	ServicesTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "opsversion_services_total",
		Help: "该平台该环境采到的服务数量",
	}, []string{"org", "env"})

	// NotifyTotal 通知投递计数，按级别与结果分类。
	//
	// 🔴 state=failed 是最该配告警的一条：
	//     increase(opsversion_notify_total{state="failed"}[1h]) > 0
	// 「通知发不出去」本身不会有人来告诉你 —— 因为通知就是用来告诉你的那个东西。
	// ⚠️ skipped 不是故障（自动+成功按规则就该静默），别把它算进告警。
	NotifyTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "opsversion_notify_total",
		Help: "通知投递次数，按级别（failed/ok）与结果（sent/skipped/failed）分类",
	}, []string{"level", "state"})
)

// EnsureNotify 预置通知指标的全部标签组合。
//
// 与 Ensure 同理：一条通知都没发过时 *Vec 不输出任何样本，
// 于是「通知从来没成功过」这种最严重的状态在监控上完全静默。
func EnsureNotify() {
	for _, lv := range []string{"failed", "ok"} {
		for _, st := range []string{"sent", "skipped", "failed"} {
			NotifyTotal.WithLabelValues(lv, st)
		}
	}
}

// Ensure 为一个 (平台, 环境) 预置全部指标。
//
// 🔴 **必须在采集之前调用**，否则告警是失效的。
//
// Prometheus 的 *Vec 在没有任何 label 组合被使用过时**不输出任何样本**。
// 于是一个从未成功采集过的平台（token 一开始就错、网络从头不通），
// opsversion_last_success_timestamp_seconds 压根不存在，
// PromQL 的 `time() - x > 3600` 返回空集 —— **告警永远不会触发**。
//
// 而「从未成功过」恰恰是最该被发现的状态：它比「曾经成功、现在坏了」更严重，
// 却因为指标缺席而完全静默。
//
// 预置成 0 之后，time() - 0 是个巨大的数，告警会立刻 fire，符合预期。
func Ensure(org, env string) {
	// Gauge 预置 0：0 表示「从未成功」，而不是「刚刚成功」——
	// 时间戳语义下 0 是 1970 年，任何陈旧阈值都会判它过期，这正是我们要的
	LastSuccessTimestamp.WithLabelValues(org, env)
	ServicesTotal.WithLabelValues(org, env)
	// Counter 预置 0：让「失败次数一直是 0」和「指标不存在」在监控上分得开
	for _, st := range []string{"success", "auth_failed", "unreachable", "forbidden", "error"} {
		CollectTotal.WithLabelValues(org, env, st)
	}
}
