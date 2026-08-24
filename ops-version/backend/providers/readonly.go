package providers

import (
	"fmt"
	"net/http"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// 只读闸门
// ─────────────────────────────────────────────────────────────
//
// 🔴 本系统对**别人家的** Rancher / Kite / ArgoCD 只做读取，永不写入。
//
// 光靠"代码里现在只写了 GET"是不够的：
//
//	客户给的账号权限由客户决定，我们控制不了。多数时候他们给的是只读账号，
//	那种账号即使我们请求了 DELETE 也会被对方拒掉。但只要**有一家**给了
//	可写账号，代码里任何一处笔误、任何一次"顺手加个接口"，
//	就是真的把对方生产环境的东西删掉了 —— 而且删完才发现。
//
// 所以把它变成结构上不可能：所有出站请求过这一层，
// **GET/HEAD 之外一律拒绝**，例外只有登录端点，且必须逐条写死在下面。
//
// ⚠️ 这一层拦的是"我方主动发起的写"。它不负责也拦不住对方账号本身的权限 ——
//
//	那是对方的事。我们能保证的是：无论对方给什么权限，我们都不会去写。
//
// ⚠️ Harbor 不走这一层：镜像同步是**有意为之的写**（往对方 Harbor 推镜像），
//
//	它有独立的权限码 sync.trigger 且一律记审计。把它混进来会让这条规则
//	变成"有例外的规则"，而有例外的安全规则等于没有规则。
//	（现状：harbor.go 目前也只有 GET，推送走的是 Harbor 自己的复制策略。）

// loginPaths 允许 POST 的登录端点。
//
// 🔴 用**完整路径精确匹配**，不是前缀、不是包含。
//
//	前缀匹配的话，`/api/v1/session` 会连 `/api/v1/session/../applications/x` 一起放过。
//
// 新增一条之前先问：这真的是登录吗？除了换 token，没有第二种写是必要的。
var loginPaths = map[string]bool{
	// Kite
	"/api/auth/login/password": true,
	// ArgoCD
	"/api/v1/session": true,
	// Rancher（登录动作在 query 里，见 allowedLogin）
	"/v3-public/localProviders/local": true,
}

// ErrWriteBlocked 被只读闸门拦下的写请求。
//
// 🔴 这个错误出现 = 代码里有 bug，不是配置问题。
// 文案要直说"这是程序问题"，否则运维会去翻对方账号的权限，翻一天也找不到。
type ErrWriteBlocked struct {
	Method string
	URL    string
}

func (e *ErrWriteBlocked) Error() string {
	return fmt.Sprintf(
		"只读闸门拦下了一个写请求：%s %s。"+
			"本系统对别人家的 Rancher/Kite/ArgoCD 只读，不写。"+
			"看到这条说明代码里有 bug（不是权限配置问题）—— "+
			"要么这个请求写错了方法，要么它确实需要写而没有走审批过的路径。",
		e.Method, e.URL)
}

// readOnly 只读闸门。包在任何 http.RoundTripper 外面。
type readOnly struct{ next http.RoundTripper }

func (t *readOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if !allowed(req) {
		// 🔴 直接返回错误，**不发出去**。
		//    发出去再看对方拒不拒，等于把安全边界交给了对方的权限配置。
		return nil, &ErrWriteBlocked{Method: req.Method, URL: req.URL.String()}
	}
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}

func allowed(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead:
		return true
	case http.MethodPost:
		return allowedLogin(req)
	default:
		// PUT / DELETE / PATCH 一律不放。没有任何读取场景需要它们。
		return false
	}
}

// allowedLogin 这个 POST 是不是已登记的登录端点。
func allowedLogin(req *http.Request) bool {
	if req.URL == nil {
		return false
	}
	// ⚠️ 用 URL.Path 而不是原始串：原始串里可能带 `..` 或编码过的斜杠，
	//    而 net/url 解析出来的 Path 已经是规范化的。
	p := strings.TrimRight(req.URL.Path, "/")
	if p == "" {
		p = "/"
	}
	if !loginPaths[p] {
		return false
	}
	// Rancher 的登录是 `?action=login`。同一个路径上还有别的 action，
	// 不卡这一下等于把整个 localProviders 端点开成可写。
	if p == "/v3-public/localProviders/local" {
		return req.URL.Query().Get("action") == "login"
	}
	return true
}

// readOnlyClient 给客户端套上只读闸门。
//
// 所有 provider 的 client() 都必须经过它 —— 漏一个，那个 provider 就没有保护。
// check-readonly-providers.mjs 会核对。
func readOnlyClient(c *http.Client) *http.Client {
	if c == nil {
		return nil
	}
	// ⚠️ 复制一份再改：传进来的可能是调用方（测试）自己持有的 client，
	//    就地改它的 Transport 会影响到调用方后续的用法。
	out := *c
	out.Transport = &readOnly{next: c.Transport}
	return &out
}
