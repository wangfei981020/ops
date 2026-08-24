package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 🔴 只有 pods 权限时，必须能从 Pod 采到版本，而不是整个失败。
//
// 复刻用户的真实环境：Rancher 只读账号在 UI 里看得到 Pod，
// 但 apps/v1 的 deployments 一个都读不到（403 in the namespace）。
// 直接报错等于因为一个我们不需要的资源而完全采不到数据。
func TestRancherFallsBackToPodsWhenDeploymentsForbidden(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch {
		case strings.Contains(r.URL.Path, "/apis/apps/v1/"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"deployments.apps is forbidden: User \"u-x\" cannot list resource \"deployments\" in API group \"apps\" in the namespace \"app-uat\"","code":403}`))
		case strings.HasSuffix(r.URL.Path, "/pods"):
			_, _ = w.Write([]byte(`{"items":[
				{"metadata":{"name":"wallet-backend-7d4f8b9c6d-x2k9p","namespace":"app-uat"},
				 "spec":{"nodeName":"n1"},
				 "status":{"phase":"Running","podIP":"10.0.0.1","containerStatuses":[
				   {"name":"app","image":"reg.example.com/asia-dev/wallet-backend:20260819054132-51","ready":true}]}},
				{"metadata":{"name":"mysql-0","namespace":"app-uat"},
				 "spec":{"nodeName":"n2"},
				 "status":{"phase":"Running","podIP":"10.0.0.2","containerStatuses":[
				   {"name":"db","image":"mysql:8.0.36","ready":true}]}}]}`))
		default:
			_, _ = w.Write([]byte(`{"items":[]}`))
		}
	}))
	defer srv.Close()
	r := &Rancher{Endpoint: srv.URL, AuthType: "api_key", APIKey: "x"}

	res, err := r.ListServices(context.Background(), "local",
		Rules{NS: NSRules{Include: []string{"app-uat"}}}, false)
	if err != nil {
		t.Fatalf("只有 pods 权限时应当降级采集，却整个失败了: %v", err)
	}
	got := map[string]string{}
	for _, s := range res.Services {
		got[s.ServiceKey] = s.Tag
	}
	if got["wallet-backend"] != "20260819054132-51" {
		t.Errorf("wallet-backend 版本 = %q，要 20260819054132-51（实得 %v）", got["wallet-backend"], got)
	}
	if got["mysql"] != "8.0.36" {
		t.Errorf("mysql 版本 = %q，要 8.0.36", got["mysql"])
	}
	// workload 名必须是剥掉副本后缀的，否则每个 Pod 都成了一个服务
	for _, s := range res.Services {
		for _, w := range s.Workloads {
			if strings.Contains(w, "7d4f8b9c6d") {
				t.Errorf("workload 名没剥副本后缀：%s", w)
			}
		}
	}
}

// 🔴 Pod 也读不到时，报**原来那个 deployments 的 403**，
// 不能报"没有 Pod" —— 后者把"权限不够"伪装成"对方什么都没部署"。
func TestRancherPodsAlsoForbiddenKeepsOriginalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"forbidden","code":403}`))
	}))
	defer srv.Close()
	r := &Rancher{Endpoint: srv.URL, AuthType: "api_key", APIKey: "x"}

	_, err := r.ListServices(context.Background(), "local",
		Rules{NS: NSRules{Include: []string{"app-uat"}}}, false)
	if err == nil {
		t.Fatal("两种资源都读不到却没报错 —— 会被显示成「对方没有任何服务」")
	}
	if !strings.Contains(err.Error(), "权限") {
		t.Errorf("应当报权限问题，实得：%v", err)
	}
}
