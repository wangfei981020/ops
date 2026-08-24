package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 会话 cookie 的 Secure 判定。
//
// 🔴 这条有个安静的失效模式：反代没透传 X-Forwarded-Proto 时，
// 代码写了 Secure 也永远是关的 —— 看着有，实际一直没生效。
// 所以要把"透传了什么"和"结果是什么"逐一钉死。
func TestSessionCookieSecure(t *testing.T) {
	cases := []struct {
		name   string
		xfp    string
		useTLS bool
		want   bool
	}{
		{"反代告知 https", "https", false, true},
		{"反代告知 http（明文进来）", "http", false, false},
		// 多级反代会拼成 "https, http"，第一个才是最外层客户端用的协议
		{"多级反代取第一个", "https, http", false, true},
		{"大小写不敏感", "HTTPS", false, true},
		{"带空格", " https ", false, true},
		// ⚠️ 没有 XFP 时回落到本连接是否 TLS —— 直连场景（没有反代）
		{"没有 XFP 且直连 TLS", "", true, true},
		{"没有 XFP 且明文直连", "", false, false},
		// 🔴 XFP 存在时**以它为准**，不能因为本连接是明文就否掉 ——
		//    反代到后端这一跳本来就是明文，这正是常态
		{"XFP=https 但本跳明文", "https", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.xfp != "" {
				r.Header.Set("X-Forwarded-Proto", c.xfp)
			}
			if c.useTLS {
				r.TLS = &tls.ConnectionState{}
			}
			got := sessionCookieOf(r, "sid", 3600)
			if got.Secure != c.want {
				t.Errorf("Secure = %v，要 %v（XFP=%q TLS=%v）", got.Secure, c.want, c.xfp, c.useTLS)
			}
			if !got.HttpOnly {
				t.Error("HttpOnly 必须开 —— 关了 JS 就能读走登录态")
			}
		})
	}
}

// 🔴 清除与签发必须属性一致：cookie 的删除按 (name, path, domain) 匹配，
// 属性对不上浏览器可能认不出是同一个，表现为"点了退出但 cookie 还在"。
func TestClearCookieMatchesIssued(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	issued := sessionCookieOf(r, "sid", 3600)
	cleared := sessionCookieOf(r, "", -1)

	if issued.Name != cleared.Name || issued.Path != cleared.Path ||
		issued.Secure != cleared.Secure || issued.HttpOnly != cleared.HttpOnly ||
		issued.SameSite != cleared.SameSite {
		t.Errorf("清除与签发的属性不一致：\n  签发 %+v\n  清除 %+v", issued, cleared)
	}
	if cleared.MaxAge != -1 || cleared.Value != "" {
		t.Errorf("清除必须是 MaxAge=-1 且值为空，实得 MaxAge=%d value=%q", cleared.MaxAge, cleared.Value)
	}
}
