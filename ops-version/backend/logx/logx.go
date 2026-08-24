// Package logx 结构化 JSON 日志：一行一个 JSON，便于 grep / 接日志系统。
// 用独立无前缀 logger 写 stdout（k8s 采集），ts 放进 JSON，不影响其它 log.Printf。
package logx

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"
)

var l = log.New(os.Stdout, "", 0)

// ctxKey 请求 id 在 context 里的键（避免碰撞用独立类型）。
type ctxKey struct{}

// WithRequestID 把 request_id 放进 context，供下游（含 dnsource）日志携带。
func WithRequestID(ctx context.Context, rid string) context.Context {
	return context.WithValue(ctx, ctxKey{}, rid)
}

// RequestID 从 context 取 request_id（无则空串）。
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// Line 把一条文本日志包成 JSON（{tag,event:"log",msg}）。用于把零散的 log.Printf 统一成 JSON 格式，
// 不必逐条拆成结构化字段；关键操作日志仍用 J/JCtx 的富字段形式。
func Line(tag, msg string) {
	J(tag, "log", map[string]any{"msg": msg})
}

// JCtx 带 request_id 的 JSON 日志（从 ctx 取 request_id 自动加进字段）。
func JCtx(ctx context.Context, tag, event string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	if rid := RequestID(ctx); rid != "" {
		fields["request_id"] = rid
	}
	J(tag, event, fields)
}

// J 输出一条 JSON 日志。tag 标类别（dns_write / godaddy_write / domain_renew / db …），
// event 标事件（create_start / success / fail …），其余字段随传。
func J(tag, event string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["ts"] = time.Now().Format("2006-01-02T15:04:05Z07:00")
	fields["tag"] = tag
	fields["event"] = event
	b, err := json.Marshal(fields)
	if err != nil {
		l.Printf(`{"tag":%q,"event":%q,"log_err":%q}`, tag, event, err.Error())
		return
	}
	l.Println(string(b))
}

// ─────────────────────────── 分级 ───────────────────────────
//
// 由 LOG_LEVEL 环境变量控制：debug | info | warn | error（默认 info）。
//
// 为什么要分级：接新数据源（Kite/Rancher）时需要看到每一次请求的 URL、状态码、
// 匹配到的 ns、解析出的 key —— 这些量很大，跑顺了必须能关掉，否则一天几十万行。
// 但**不能靠删代码来关**：删了下次排查又得重新加，而重新加的时候
// 往往漏掉当初最关键的那一条。

// Level 日志级别。
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var levelNames = map[Level]string{
	LevelDebug: "debug", LevelInfo: "info", LevelWarn: "warn", LevelError: "error",
}

var curLevel = LevelInfo

// SetLevel 设置级别。无法识别的值回落到 info，并**打一条 warn 说明**——
// 静默回落会让人以为 LOG_LEVEL=DEUBG（拼错）生效了，然后对着没有 debug 日志的输出发懵。
func SetLevel(s string) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		curLevel = LevelDebug
	case "info", "":
		curLevel = LevelInfo
	case "warn", "warning":
		curLevel = LevelWarn
	case "error":
		curLevel = LevelError
	default:
		curLevel = LevelInfo
		J("logx", "unknown_level", map[string]any{
			"level": "warn", "given": s, "fallback": "info",
			"msg": "LOG_LEVEL 取值无法识别，已回落到 info。可选：debug|info|warn|error",
		})
	}
	J("logx", "level_set", map[string]any{"level": "info", "value": levelNames[curLevel]})
}

// Enabled 供调用方在**构造昂贵字段之前**先判断，避免白白序列化。
func Enabled(lv Level) bool { return lv >= curLevel }

func emit(lv Level, tag, event string, fields map[string]any) {
	if lv < curLevel {
		return
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["level"] = levelNames[lv]
	J(tag, event, fields)
}

// Debug 排查用的细节：每次外部请求、每条判定的输入与依据。
// 🔴 判定类日志必须把「输入 + 依据 + 结论」三样都打出来 ——
// 只打结论的话，出错时仍然要重新加日志才能查，等于没打。
func Debug(tag, event string, f map[string]any) { emit(LevelDebug, tag, event, f) }

// Info 状态变化：启动、采集完成、配置变更。
func Info(tag, event string, f map[string]any) { emit(LevelInfo, tag, event, f) }

// Warn 不影响流程但需要被看见的：未识别的枚举值、回落到默认值、数据可疑。
// 🔴 未识别的枚举一律 WARN，不能静默归到「其他」——
// 新增一种取值时必须有人发现，否则界面上永远显示一句没用的「未知」。
func Warn(tag, event string, f map[string]any) { emit(LevelWarn, tag, event, f) }

// Error 操作失败。
func Error(tag, event string, f map[string]any) { emit(LevelError, tag, event, f) }
