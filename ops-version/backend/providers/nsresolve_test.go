package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeRancher 造一个只在特定路径上给数据的 Rancher。
func fakeRancher(t *testing.T, h http.HandlerFunc) *Rancher {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Rancher{Endpoint: srv.URL, APIKey: "k", AuthType: "api_key", HTTP: srv.Client()}
}

func nsJSON(names ...string) string {
	type meta struct {
		Name string `json:"name"`
	}
	type item struct {
		Metadata meta `json:"metadata"`
	}
	var l struct {
		Items []item `json:"items"`
	}
	for _, n := range names {
		l.Items = append(l.Items, item{meta{n}})
	}
	b, _ := json.Marshal(l)
	return string(b)
}

// 🔴 回归：通配展开成功但**零命中**，必须报「规则没匹配上」，
// 不能回退去拉整集群 —— 那样必然 403，然后报成「权限不足」。
//
// 两者的下一步完全相反：一个去改 ns 规则，一个去放大权限。
// 用户实测被这条误导过（对方压根没有 biz-* 那些命名空间）。
func TestZeroMatchReportsRuleNotPermission(t *testing.T) {
	var clusterWide bool
	r := fakeRancher(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/api/v1/namespaces") {
			_, _ = w.Write([]byte(nsJSON("default", "kube-system", "wallet-uat")))
			return
		}
		if strings.HasSuffix(req.URL.Path, "/deployments") {
			clusterWide = true
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"deployments is forbidden at the cluster scope"}`))
			return
		}
		w.WriteHeader(404)
	})
	rules := Rules{NS: NSRules{Include: []string{"biz-*"}}}
	_, err := r.ListServices(context.Background(), "local", rules, false)
	if err == nil {
		t.Fatal("零命中应该报错")
	}
	if clusterWide {
		t.Error("零命中却去拉了整集群 —— 那必然 403，然后报成「权限不足」，方向反了")
	}
	for _, want := range []string{"没有匹配到任何命名空间", "biz-*", "wallet-uat", "不是权限问题"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息里缺 %q —— 人看不出该改什么\n实得：%v", want, err)
		}
	}
}

// ⚠️ 读不到 ns 列表（无法展开）是另一回事：那时回退整集群是对的
func TestCannotExpandStillFallsBackToClusterWide(t *testing.T) {
	var clusterWide bool
	r := fakeRancher(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "namespaces") {
			w.WriteHeader(http.StatusForbidden) // k8s 和 v3 两条路都拒
			return
		}
		if strings.HasSuffix(req.URL.Path, "/deployments") {
			clusterWide = true
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	rules := Rules{NS: NSRules{Include: []string{"biz-*"}}}
	if _, err := r.ListServices(context.Background(), "local", rules, false); err != nil {
		t.Fatalf("读不到 ns 列表时应回退整集群，实得错误：%v", err)
	}
	if !clusterWide {
		t.Error("无法展开时没有回退整集群 —— 集群级账号会因此完全采不到")
	}
}

// 🔴 Rancher v3 兜底：k8s API 被拒时，project 级账号靠它展开通配
func TestNamespaceExpandFallsBackToRancherV3(t *testing.T) {
	var usedV3 bool
	r := fakeRancher(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/api/v1/namespaces"):
			w.WriteHeader(http.StatusForbidden) // 集群级被拒（project 级账号的常态）
		case strings.Contains(req.URL.Path, "/v3/clusters/") && strings.HasSuffix(req.URL.Path, "/namespaces"):
			usedV3 = true
			_, _ = w.Write([]byte(`{"data":[{"name":"biz-uat","id":"local:biz-uat"}]}`))
		default:
			_, _ = w.Write([]byte(`{"items":[]}`))
		}
	})
	ns, expanded := r.resolveNamespaces(context.Background(), "local",
		Rules{NS: NSRules{Include: []string{"biz-*"}}})
	if !usedV3 {
		t.Fatal("k8s API 被拒后没有去试 Rancher v3 —— project 级账号用不了通配")
	}
	if !expanded || len(ns) != 1 || ns[0] != "biz-uat" {
		t.Errorf("v3 展开结果不对：ns=%v expanded=%v", ns, expanded)
	}
}

// v3 只给 id 不给 name 的版本，要能取出纯 ns 名
func TestRancherV3IDFallback(t *testing.T) {
	r := fakeRancher(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/api/v1/namespaces") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"c-m-xxx:biz-uat"}]}`))
	})
	got := r.nsFromRancherV3(context.Background(), "c-m-xxx")
	if len(got) != 1 || got[0] != "biz-uat" {
		t.Errorf("只给 id 时应取冒号后那段，实得 %v —— "+
			"用整个 id 的话后续逐 ns 查询会拼出不存在的路径，表现是「展开成功却一个服务都采不到」", got)
	}
}
