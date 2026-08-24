package providers

import (
	"regexp"
	"strings"
)

// SafeBody 把失败响应体整理成可以进日志的形态。
//
// 🔴 为什么必须打响应体：非 2xx 时**真正的原因全在体里**。
// 只打状态码的话，403 只能告诉你「没权限」，而人要知道的是「缺哪个权限」——
// 于是每次都得去问对方要账号信息，或者干脆猜。
// 实测就吃过这个亏：Harbor 返回 403，日志里只有 status=403，
// 排查只能靠人去 Harbor 界面上翻 robot 账号的权限范围。
//
// 三件事必须同时做，少一件这个函数就不能用：
//  1. **脱敏**：响应体里可能回显请求里的凭据（Harbor 的某些错误就会带上账号名），
//     日志会被收集、转发、长期留存 —— 密码进了日志就等于泄露。
//  2. **截断**：错误页可能是一整页 HTML（几十 KB），
//     全打进去会把日志刷爆，真正有用的那句反而被淹没。
//  3. **压平**：多行 HTML/JSON 在行式日志里会把一条记录撑成几十行。
func SafeBody(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	s = credPattern.ReplaceAllString(s, `$1"***"`)
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…(截断)"
	}
	return s
}

// 常见的凭据字段名。响应体回显请求内容时会带上它们。
// ⚠️ 宁可多脱一个也不能漏 —— 日志一旦收集走就收不回来了。
var credPattern = regexp.MustCompile(
	`(?i)("(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?secret|authorization)"\s*:\s*)"[^"]*"`)
