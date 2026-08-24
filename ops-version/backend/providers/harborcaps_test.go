package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Harbor 权限分级：项目级可用 / replication 不可用，两者必须分别判定。
func TestHarborCapabilities(t *testing.T) {
	cases := []struct {
		name                  string
		projectsOK, replicaOK bool
		wantDetail            string
	}{
		{"系统级 robot", true, true, "全部可用"},
		// 用户的实际情形：robot$check_haproxy 是项目级的
		{"项目级 robot", true, false, "可查版本"},
		{"凭据有效但没授权", false, false, "既读不到项目也读不到复制记录"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// /statistics 是**凭据探针**（Probe），与两级能力无关：
				// 这三个用例都假定凭据本身有效，只是授权范围不同。
				// ⚠️ 假服务端必须把探针也模拟出来，否则 Capabilities 会在
				//    第一步就返回，三个用例测的都是同一条路径。
				if strings.HasSuffix(r.URL.Path, "/statistics") {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				okThis := c.projectsOK
				if strings.Contains(r.URL.Path, "/replication/") {
					okThis = c.replicaOK
				}
				if !okThis {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"errors":[{"code":"FORBIDDEN"}]}`))
					return
				}
				_, _ = w.Write([]byte(`[]`))
			}))
			defer srv.Close()
			h := &Harbor{Endpoint: srv.URL, Username: "robot$x", Password: "y"}

			caps := h.Capabilities(context.Background())
			if caps.Projects != c.projectsOK || caps.Replication != c.replicaOK {
				t.Fatalf("探测结果 projects=%v replication=%v，要 %v/%v",
					caps.Projects, caps.Replication, c.projectsOK, c.replicaOK)
			}
			if !strings.Contains(caps.Detail, c.wantDetail) {
				t.Errorf("说明里要有 %q，实得：%s", c.wantDetail, caps.Detail)
			}

			// 🔴 Probe 只验凭据，不验授权范围：
			//    这三个用例凭据都有效，所以都该判成连通 ——
			//    读不到 replication（一个**可选**能力）不该说成"连不上"。
			if err := h.Probe(context.Background()); err != nil {
				t.Errorf("凭据有效却判成连不上: %v", err)
			}
		})
	}
}

// 🔴 打**真实** Harbor：错误凭据必须探测失败。
//
// 这条只有真实 Harbor 能测出来 —— 假服务端按我写的规则返回，
// 而真实 Harbor 有"部分接口对匿名开放"这条规矩：
// 用完全错误的凭据打 /api/v2.0/projects 会返回 200（只是内容只有公开项目）。
// 拿它当探针，密码打错也会显示"连接正常"。
func TestHarborProbeRejectsBadCredentialOnRealServer(t *testing.T) {
	ep := os.Getenv("HARBOR_EP")
	if ep == "" {
		t.Skip("需要 HARBOR_EP")
	}
	ctx := context.Background()

	bad := &Harbor{Endpoint: ep, Username: "robot$nonexistent", Password: "wrong", InsecureTLS: true}
	if err := bad.Probe(ctx); err == nil {
		t.Fatal("错误凭据却探测成功 —— 探针打的接口对匿名开放，" +
			"这样密码打错、robot 过期都会显示「连接正常」")
	}
	if caps := bad.Capabilities(ctx); caps.Projects || caps.Replication {
		t.Errorf("错误凭据却报出能力 projects=%v replication=%v —— "+
			"会把认证失败伪装成权限分级问题", caps.Projects, caps.Replication)
	}

	// 对照：正确凭据必须探测成功
	u, pw := os.Getenv("HARBOR_USER"), os.Getenv("HARBOR_PASS")
	if u == "" {
		return
	}
	good := &Harbor{Endpoint: ep, Username: u, Password: pw, InsecureTLS: true}
	if err := good.Probe(ctx); err != nil {
		t.Fatalf("正确凭据却探测失败: %v", err)
	}
	caps := good.Capabilities(ctx)
	t.Logf("正确凭据能力: projects=%v replication=%v", caps.Projects, caps.Replication)
	if !caps.Projects {
		t.Error("管理员凭据应当能读项目")
	}
}
