package providers

import (
	"strings"
	"testing"
)

// 🔴 响应体可能回显凭据。日志会被收集、转发、长期留存 ——
// 密码进了日志就等于泄露，而且收不回来。
func TestSafeBodyRedactsCredentials(t *testing.T) {
	cases := []string{
		`{"error":"bad","password":"P@ssw0rd!"}`,
		`{"Password": "secret123"}`,
		`{"api_key":"ak_live_abcdef"}`,
		`{"apiKey":"ak_live_abcdef"}`,
		`{"token":"eyJhbGciOi"}`,
		`{"access_secret":"xyz"}`,
		`{"Authorization":"Basic dXNlcjpwYXNz"}`,
	}
	for _, in := range cases {
		got := SafeBody([]byte(in), 500)
		for _, leak := range []string{"P@ssw0rd!", "secret123", "ak_live_abcdef", "eyJhbGciOi", "xyz", "dXNlcjpwYXNz"} {
			if strings.Contains(got, leak) {
				t.Errorf("凭据泄露到日志：输入 %s → 输出 %s", in, got)
			}
		}
		if !strings.Contains(got, "***") {
			t.Errorf("没有脱敏标记：%s → %s", in, got)
		}
	}
}

// 错误页可能是一整页 HTML，全打进日志会把真正有用的那句淹掉
func TestSafeBodyTruncates(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got := SafeBody([]byte(long), 200)
	if len([]rune(got)) > 210 {
		t.Errorf("没有截断：长度 %d", len([]rune(got)))
	}
	if !strings.Contains(got, "截断") {
		t.Error("截断了却没有标记 —— 人会以为响应体本来就这么长")
	}
}

// 多行内容要压平，否则一条日志会撑成几十行
func TestSafeBodyFlattens(t *testing.T) {
	got := SafeBody([]byte("line1\n  line2\n\tline3"), 500)
	if strings.ContainsAny(got, "\n\t") {
		t.Errorf("没有压平：%q", got)
	}
}

// 空体返回空串，不要打一条没有内容的日志字段
func TestSafeBodyEmpty(t *testing.T) {
	if got := SafeBody([]byte("   \n  "), 100); got != "" {
		t.Errorf("空体应返回空串，实得 %q", got)
	}
}

// 正常的错误说明要保留 —— 脱敏不能把有用信息也抹掉
func TestSafeBodyKeepsUsefulMessage(t *testing.T) {
	in := `{"errors":[{"code":"FORBIDDEN","message":"user does not have permission to list replication policies"}]}`
	got := SafeBody([]byte(in), 500)
	if !strings.Contains(got, "replication policies") {
		t.Errorf("有用的错误说明被抹掉了：%s", got)
	}
}
