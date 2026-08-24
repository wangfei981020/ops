package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestNSRules ns 匹配是纯逻辑，必须先对 —— 配错的表现是
// 「某个服务从对账里凭空消失」，比报错更难发现。
func TestNSRules(t *testing.T) {
	cases := []struct {
		name string
		r    NSRules
		ns   string
		want bool
	}{
		{"空规则=全收", NSRules{}, "app-wallet", true},
		{"前缀匹配", NSRules{Include: []string{"app-*"}}, "app-wallet", true},
		{"前缀不匹配", NSRules{Include: []string{"app-*"}}, "xyz-core", false},
		{"精确匹配", NSRules{Include: []string{"app-test"}}, "app-test", true},
		{"精确不匹配前缀", NSRules{Include: []string{"app-test"}}, "app-test2", false},
		// 🔴 这条是重点：app-* 会把 app-uat 一起抓进来 → 同名冲突。exclude 是唯一的挡法
		{"exclude 优先于 include", NSRules{Include: []string{"app-*"}, Exclude: []string{"app-uat"}}, "app-uat", false},
		{"exclude 也支持前缀", NSRules{Include: []string{"*"}, Exclude: []string{"kube-*"}}, "kube-system", false},
		{"多个 include 任一命中", NSRules{Include: []string{"a-*", "b-*"}}, "b-svc", true},
	}
	for _, c := range cases {
		if got := c.r.Match(c.ns); got != c.want {
			t.Errorf("%s: Match(%q) = %v, want %v", c.name, c.ns, got, c.want)
		}
	}
}

// TestBuildSnapshots 归并逻辑：一个镜像被多个 workload 共用是常态，
// 版本不一致时必须报冲突而不是静默取第一个。
func TestBuildSnapshots(t *testing.T) {
	// 真实形态：go-archive-server-backend 被 api/consumer/worker 三个 Deployment 共用
	rows := []rawWorkload{
		{"app-base", "go-archive-server-backend-api", "deployments", "h/appA/go-archive-server-backend:20260813081348-14"},
		{"app-base", "go-archive-server-backend-consumer", "deployments", "h/appA/go-archive-server-backend:20260813081348-14"},
		{"app-base", "go-archive-server-backend-worker", "deployments", "h/appA/go-archive-server-backend:20260813081348-14"},
		{"app-wallet", "wallet-client-backend", "deployments", "h/appA/wallet-client-backend:20260519082034-58ac8c3-114"},
	}
	snaps := buildSnapshots(rows, nil)
	if len(snaps) != 2 {
		t.Fatalf("三个 workload 共用一个镜像应归并成 1 条，加上 wallet 共 2 条，实得 %d", len(snaps))
	}
	if snaps[0].ServiceKey != "go-archive-server-backend" || len(snaps[0].Workloads) != 3 {
		t.Errorf("归并结果不对: key=%s workloads=%v", snaps[0].ServiceKey, snaps[0].Workloads)
	}
	if len(snaps[0].Conflicts) != 0 {
		t.Errorf("版本一致时不该报冲突, 实得 %v", snaps[0].Conflicts)
	}

	// worker 忘了升级 —— 用 workload 名当 key 发现不了，按镜像名归并立刻暴露
	rows[2].Image = "h/appA/go-archive-server-backend:20260701000000-9"
	snaps = buildSnapshots(rows, nil)
	if len(snaps[0].Conflicts) != 2 {
		t.Fatalf("版本不一致应报冲突并列出两边, 实得 %d 条: %+v", len(snaps[0].Conflicts), snaps[0].Conflicts)
	}
	t.Logf("冲突明细正确列出双方: %+v", snaps[0].Conflicts)

	// registry 白名单：公共镜像不参与对账
	rows = append(rows, rawWorkload{"app-base", "nginx", "deployments", "docker.io/library/nginx:stable"})
	got := buildSnapshots(rows, []string{"h"})
	for _, s := range got {
		if s.ServiceKey == "nginx" {
			t.Error("白名单外的公共镜像不该进对账")
		}
	}
}

// TestKiteLive 实测连本地 Kite。默认跳过，给了环境变量才跑：
//
//	KITE_ENDPOINT=http://localhost:30836 KITE_USER=admin KITE_PASS=xxx \
//	  go test ./providers/ -run TestKiteLive -v
func TestKiteLive(t *testing.T) {
	ep := os.Getenv("KITE_ENDPOINT")
	if ep == "" {
		t.Skip("未设置 KITE_ENDPOINT，跳过实测")
	}
	k := &Kite{
		Endpoint: ep,
		AuthType: "password",
		Username: os.Getenv("KITE_USER"),
		Password: os.Getenv("KITE_PASS"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("Probe", func(t *testing.T) {
		if err := k.Probe(ctx); err != nil {
			t.Fatalf("连不上: %v", err)
		}
		t.Log("✓ 密码登录成功，token 有效期至", k.tokenExp.Format(time.RFC3339))
	})

	var clusters []string
	t.Run("Clusters", func(t *testing.T) {
		var err error
		clusters, err = k.Clusters(ctx)
		if err != nil {
			t.Fatalf("列集群失败: %v", err)
		}
		t.Logf("✓ 集群: %v", clusters)
	})

	t.Run("ListServices", func(t *testing.T) {
		if len(clusters) == 0 {
			t.Skip("没有集群")
		}
		res, err := k.ListServices(ctx, clusters[0], Rules{NS: NSRules{Exclude: []string{"kube-*"}}}, true)
		if err != nil {
			t.Fatalf("拉服务失败: %v", err)
		}
		snaps := res.Services
		t.Logf("✓ 采到 %d 个服务、%d 条 Pod 明细", len(snaps), len(res.Pods))
		// Pod 明细与服务快照来自同一次 pod 请求，服务非空时不该一条 Pod 都没有
		if len(snaps) > 0 && len(res.Pods) == 0 {
			t.Log("⚠️ 采到了服务但 Pod 明细为空 —— pod 接口可能失败了（不致命，但导出会缺明细）")
		}
		n := 0
		for _, s := range snaps {
			if n >= 6 {
				break
			}
			flag := ""
			if !s.IsVersioned {
				flag = "  [非版本化 tag → 无法判定]"
			}
			if s.RunningTag != "" && s.RunningTag != s.Tag {
				flag += "  [发布中: 声明 " + s.Tag + " 实跑 " + s.RunningTag + "]"
			}
			if len(s.Conflicts) > 0 {
				flag += "  [同名冲突]"
			}
			t.Logf("  %-28s tag=%-16s ns=%-14s workloads=%d%s",
				s.ServiceKey, s.Tag, s.Namespace, len(s.Workloads), flag)
			n++
		}
	})

	t.Run("错误分类", func(t *testing.T) {
		bad := &Kite{Endpoint: ep, AuthType: "password", Username: "admin", Password: "definitely-wrong"}
		err := bad.Probe(ctx)
		if !errors.Is(err, ErrAuth) {
			t.Errorf("密码错应归类为 ErrAuth，实得: %v", err)
		} else {
			t.Logf("✓ 密码错 → ErrAuth: %v", err)
		}

		gone := &Kite{Endpoint: "http://127.0.0.1:1", AuthType: "password", Username: "a", Password: "b"}
		err = gone.Probe(ctx)
		if !errors.Is(err, ErrUnreachable) {
			t.Errorf("连不上应归类为 ErrUnreachable，实得: %v", err)
		} else {
			t.Log("✓ 端口不通 → ErrUnreachable")
		}
	})
}

// 🔴 Kite 的两种认证走**完全不同**的位置：
//
//	password → Cookie: auth_token=<jwt>
//	api_key  → Authorization: <key>（不加 Bearer）
//
// 共用一条路径的话，其中一种必然恒 401 —— 而 Kite 对所有认证失败
// 都返回同一句 "Invalid or expired token"，连「传错位置」都分不出来。
func TestKiteAuthHeaderByAuthType(t *testing.T) {
	cases := []struct {
		authType   string
		wantHeader string
		wantValue  string
		notHeader  string
	}{
		{"api_key", "Authorization", "kite12-secret", "Cookie"},
		{"password", "Cookie", "auth_token=jwt-token", "Authorization"},
	}
	for _, c := range cases {
		var got *http.Request
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/auth/login/password" {
				http.SetCookie(w, &http.Cookie{Name: "auth_token", Value: "jwt-token"})
				w.WriteHeader(http.StatusNoContent)
				return
			}
			got = r.Clone(r.Context())
			_, _ = w.Write([]byte(`{"items":[]}`))
		}))
		defer srv.Close()

		k := &Kite{Endpoint: srv.URL, AuthType: c.authType,
			Username: "u", Password: "p", APIKey: "kite12-secret"}
		if _, err := k.do(context.Background(), "/api/v1/clusters"); err != nil {
			t.Fatalf("%s: %v", c.authType, err)
		}
		if v := got.Header.Get(c.wantHeader); v != c.wantValue {
			t.Errorf("%s: %s = %q, want %q", c.authType, c.wantHeader, v, c.wantValue)
		}
		// 🔴 另一个头必须**不带** —— 两个都发的话，服务端用哪个取决于它的实现，
		//    今天能跑不代表明天升级后还能跑
		if v := got.Header.Get(c.notHeader); v != "" {
			t.Errorf("%s: 不该带 %s，实得 %q", c.authType, c.notHeader, v)
		}
		// Bearer 前缀是最容易顺手加上的，文档明确说不要
		if c.authType == "api_key" && strings.HasPrefix(got.Header.Get("Authorization"), "Bearer") {
			t.Error("api_key 不能加 Bearer 前缀（Kite 文档：Do not prepend Bearer）")
		}
	}
}
