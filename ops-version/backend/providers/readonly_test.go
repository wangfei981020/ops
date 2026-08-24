package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// countingTransport 记录真正发出去的请求 —— 判据是"有没有发出去"，
// 不是"对方返回了什么"。把安全边界交给对方的权限配置，等于没有边界。
type countingTransport struct {
	sent []string
	next http.RoundTripper
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.sent = append(c.sent, r.Method+" "+r.URL.Path)
	return c.next.RoundTrip(r)
}

func newProbe(t *testing.T) (*http.Client, *countingTransport, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	ct := &countingTransport{next: http.DefaultTransport}
	return readOnlyClient(&http.Client{Transport: ct}), ct, srv
}

func do(t *testing.T, c *http.Client, method, url string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	return err
}

// 🔴 核心：DELETE / PUT / PATCH 一律**不发出去**。
//
// 这是这一层存在的全部理由 —— 客户给的账号权限我们控制不了，
// 万一给了可写账号，代码里任何一处笔误就是真的删了对方生产环境的东西。
func TestReadOnlyBlocksWrites(t *testing.T) {
	c, ct, srv := newProbe(t)
	for _, m := range []string{http.MethodDelete, http.MethodPut, http.MethodPatch} {
		err := do(t, c, m, srv.URL+"/api/v1/applications/prod-app")
		var blocked *ErrWriteBlocked
		if !errors.As(err, &blocked) {
			t.Errorf("%s 应该被拦下，实得 err=%v", m, err)
		}
	}
	if len(ct.sent) != 0 {
		t.Fatalf("有写请求真的发出去了：%v —— 闸门必须在发出**之前**拦，"+
			"不能靠对方拒绝", ct.sent)
	}
}

// 非登录端点的 POST 同样拦死 —— ArgoCD 的 sync、Rancher 的 action 都是 POST
func TestReadOnlyBlocksNonLoginPost(t *testing.T) {
	c, ct, srv := newProbe(t)
	for _, p := range []string{
		"/api/v1/applications/prod-app/sync", // ArgoCD 触发同步
		"/v3/clusters/c-m-xxx?action=generateKubeconfig",
		"/api/auth/login/password/../../v1/deployments", // 路径穿越也要拦
	} {
		err := do(t, c, http.MethodPost, srv.URL+p)
		var blocked *ErrWriteBlocked
		if !errors.As(err, &blocked) {
			t.Errorf("POST %s 应该被拦下，实得 err=%v", p, err)
		}
	}
	if len(ct.sent) != 0 {
		t.Fatalf("有 POST 发出去了：%v", ct.sent)
	}
}

// 登录必须放行，否则整个采集都跑不起来
func TestReadOnlyAllowsLogin(t *testing.T) {
	c, ct, srv := newProbe(t)
	for _, p := range []string{
		"/api/auth/login/password", // Kite
		"/api/v1/session",          // ArgoCD
		"/v3-public/localProviders/local?action=login", // Rancher
	} {
		if err := do(t, c, http.MethodPost, srv.URL+p); err != nil {
			t.Errorf("登录端点 %s 被误拦：%v —— 采集会整个跑不起来", p, err)
		}
	}
	if len(ct.sent) != 3 {
		t.Errorf("应该发出 3 个登录请求，实得 %v", ct.sent)
	}
}

// ⚠️ Rancher 的登录路径上还有别的 action，不卡 action=login
// 等于把整个 localProviders 端点开成可写
func TestReadOnlyRancherActionMustBeLogin(t *testing.T) {
	c, _, srv := newProbe(t)
	err := do(t, c, http.MethodPost, srv.URL+"/v3-public/localProviders/local?action=refresh")
	var blocked *ErrWriteBlocked
	if !errors.As(err, &blocked) {
		t.Errorf("action=refresh 应该被拦下，实得 %v", err)
	}
}

// GET 照常
func TestReadOnlyAllowsGet(t *testing.T) {
	c, ct, srv := newProbe(t)
	if err := do(t, c, http.MethodGet, srv.URL+"/api/v1/applications"); err != nil {
		t.Fatalf("GET 被拦了：%v", err)
	}
	if len(ct.sent) != 1 {
		t.Errorf("GET 应该发出去，实得 %v", ct.sent)
	}
}

// 🔴 三个 provider 都必须套上闸门。漏一个，那个 provider 就完全没有保护。
func TestAllProvidersWrapped(t *testing.T) {
	ct := &countingTransport{next: http.DefaultTransport}
	base := &http.Client{Transport: ct}
	cases := map[string]*http.Client{
		"kite":    (&Kite{HTTP: base}).client(),
		"rancher": (&Rancher{HTTP: base}).client(),
		"argocd":  (&ArgoCD{HTTP: base}).client(),
	}
	for name, c := range cases {
		if _, ok := c.Transport.(*readOnly); !ok {
			t.Errorf("%s 的 client 没套只读闸门（Transport=%T）—— "+
				"这个 provider 对客户环境是可写的", name, c.Transport)
		}
	}
}

// 套闸门不能改坏调用方自己持有的 client
func TestReadOnlyDoesNotMutateInput(t *testing.T) {
	orig := &http.Client{Transport: http.DefaultTransport}
	_ = readOnlyClient(orig)
	if _, wrapped := orig.Transport.(*readOnly); wrapped {
		t.Error("就地改了传进来的 client —— 会影响调用方后续的用法")
	}
}
