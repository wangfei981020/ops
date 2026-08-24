package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// 通配规则必须被展开成确切 ns，而不是退回整集群拉。
func TestKiteWildcardExpandsToExactNS(t *testing.T) {
	ep := os.Getenv("KITE_EP")
	if ep == "" {
		t.Skip("需要 KITE_EP")
	}
	k := &Kite{Endpoint: ep, AuthType: "password",
		Username: os.Getenv("KITE_USER"), Password: os.Getenv("KITE_PASS")}
	ctx := context.Background()

	// 混合规则：一个通配 + 一个确切名 —— 正是用户的配法（app-* + biz-uat）
	rules := Rules{NS: NSRules{Include: []string{"ops-*", "argocd"}}}
	got, _ := k.resolveNamespaces(ctx, "docker-desktop", rules)
	if len(got) == 0 {
		t.Fatal("通配没被展开，退回了整集群拉 —— 这正是 403 的成因")
	}
	var hasWildcardMatch, hasExact bool
	for _, n := range got {
		if n == "argocd" {
			hasExact = true
		}
		if len(n) > 4 && n[:4] == "ops-" {
			hasWildcardMatch = true
		}
	}
	t.Logf("展开结果 (%d 个): %v", len(got), got)
	if !hasWildcardMatch {
		t.Error("通配 ops-* 一个都没展开出来")
	}
	if !hasExact {
		t.Error("确切名 argocd 被通配拖没了 —— 这就是 biz-uat 丢失的形态")
	}

	// 展开后拉到的数据，要和"全量拉再本地过滤"一致
	perNS, err := k.ListServices(ctx, "docker-desktop", rules, true)
	if err != nil {
		t.Fatalf("展开后采集失败: %v", err)
	}
	full, err := k.ListServices(ctx, "docker-desktop",
		Rules{NS: NSRules{Include: []string{"*"}}}, true)
	if err != nil {
		t.Fatalf("全量失败: %v", err)
	}
	want := map[string]bool{}
	for _, s := range full.Services {
		if rules.NS.Match(s.Namespace) {
			want[s.ServiceKey] = true
		}
	}
	t.Logf("展开后采到 %d 个服务；全量过滤得 %d 个", len(perNS.Services), len(want))
	if len(perNS.Services) == 0 {
		t.Fatal("展开后一个服务都没采到")
	}
}

// 🔴 复刻用户的真实场景：账号**没有** namespaces 读权限，
// 只能靠 /api/auth/user 里角色声明的 ns 列表来展开通配。
//
// 这条路径在本地用 admin 测不出来（admin 能列 namespaces，走的是路径①；
// 且它的角色 namespaces 是 `*`，展不出确切名）——只能用假服务端逼出来。
func TestKiteExpandsFromRoleWhenNamespacesForbidden(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch {
		// 用户角色的 resources 只有 deployments/statefulsets/daemonsets，
		// 列 namespaces 会被拒
		//
		// ⚠️ 路径判断必须跟 clusterPath 的格式一致（`/api/v1/_clusters/<c>/<res>[/<ns>]`）。
		//    改成路径段集群后这里一度还在匹配旧的 `/api/v1/namespaces`，
		//    请求落进 default 返回空 items，测试**照样 PASS** ——
		//    但"namespaces 被拒"这个前提已经不成立，测的东西变了。
		case strings.HasSuffix(r.URL.Path, "/namespaces"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"no permission"}`))
		case r.URL.Path == "/api/auth/user":
			_, _ = w.Write([]byte(`{"user":{"username":"qa-read","roles":[
				{"name":"version-reader","clusters":["uat-cluster-01","app-prod-cluster"],
				 "namespaces":["app-uat","app-pay","biz-uat","other-ns"],
				 "resources":["deployments"],"verbs":["get"]}]}}`))
		// 整集群 list 必须**不被调用** —— 调了就说明没展开成功
		case strings.HasSuffix(r.URL.Path, "/deployments") || strings.HasSuffix(r.URL.Path, "/statefulsets"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"namespace All"}`))
		default:
			_, _ = w.Write([]byte(`{"items":[]}`))
		}
	}))
	defer srv.Close()
	k := &Kite{Endpoint: srv.URL, AuthType: "api_key", APIKey: "x"}

	// 用户的配法：一个通配 + 一个确切名
	rules := Rules{NS: NSRules{Include: []string{"app-*", "biz-uat"}}}
	got, _ := k.resolveNamespaces(context.Background(), "uat-cluster-01", rules)

	want := map[string]bool{"app-uat": true, "app-pay": true, "biz-uat": true}
	if len(got) != len(want) {
		t.Fatalf("展开结果 %v，要 %v —— other-ns 不该进来，biz-uat 不该丢", got, want)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("多展开了 %q", n)
		}
	}

	// 🔴 集群必须出现在**路径段**里，不能是 `?cluster=` ——
	//    Kite 的鉴权中间件只认路径，用查询参数会让它拿默认集群去比对角色，
	//    表现是"报错里的集群名跟请求的对不上"。
	var sawClusterInPath bool
	for _, p := range asked {
		if strings.HasPrefix(p, "/api/v1/_clusters/uat-cluster-01/") {
			sawClusterInPath = true
		}
	}
	if !sawClusterInPath {
		t.Errorf("请求路径里没有 _clusters/<集群>，实际请求：%v", asked)
	}

	// 角色管不到的集群，不能拿它的 ns 来展开
	// ⚠️ 断言 expanded=false 而不只是 ns==nil：
	//    "展开不了"和"展开了但零命中"现在是两条完全不同的路 ——
	//    前者回退整集群，后者报「规则没匹配上」。这里要的是前者。
	if ns, expanded := k.resolveNamespaces(context.Background(), "dev-k8s-cluster-01", rules); ns != nil || expanded {
		t.Errorf("集群 dev-k8s-cluster-01 不在角色授权内，应判为「展不开」回退整集群，实得 ns=%v expanded=%v", ns, expanded)
	}
}

// 角色 namespaces 写的是通配时展不开，必须回退整集群拉（而不是展成空集合）。
// ⚠️ 展成空集合会让采集"成功但零结果"—— 那跟"对方下线了所有服务"分不出来。
func TestKiteRoleWildcardFallsBackToFullScan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/namespaces") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"user":{"roles":[{"clusters":["*"],"namespaces":["*"]}]}}`))
	}))
	defer srv.Close()
	k := &Kite{Endpoint: srv.URL, AuthType: "api_key", APIKey: "x"}
	ns, expanded := k.resolveNamespaces(context.Background(),
		"c", Rules{NS: NSRules{Include: []string{"app-*"}}})
	if ns != nil || expanded {
		t.Fatalf("角色是 `*` 时展不出确切名，应判为「展不开」回退全量，实得 ns=%v expanded=%v", ns, expanded)
	}
}
