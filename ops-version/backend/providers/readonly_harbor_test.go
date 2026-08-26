package providers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestReadOnlyGateBlocksWrites 只读闸门的**变异测试**：
// 模拟"某天有人顺手加了个写接口"，闸门必须拦住。
//
// 🔴 光看"代码里现在只写了 GET"不能证明任何事 —— 那只是当下的事实，
// 而客户给的账号权限由客户决定：只要有一家给了可写账号，
// 一处笔误就是真的删掉对方生产环境的东西，且删完才发现。
func TestReadOnlyGateBlocksWrites(t *testing.T) {
	c := readOnlyClient(&http.Client{})
	for _, tc := range []struct{ method, url, what string }{
		{http.MethodDelete, "https://rancher.example.com/v3/clusters/c-abc", "Rancher 删集群"},
		{http.MethodPut, "https://rancher.example.com/v3/clusters/c-abc", "Rancher 改集群"},
		{http.MethodPatch, "https://kite.example.com/api/v1/deployments/x", "Kite 改工作负载"},
		{http.MethodDelete, "https://kite.example.com/api/v1/pods/x", "Kite 删 Pod"},
		{http.MethodPost, "https://argocd.example.com/api/v1/applications/wallet/sync", "ArgoCD sync"},
		{http.MethodDelete, "https://argocd.example.com/api/v1/applications/wallet", "ArgoCD 删应用"},
		// ⚠️ 路径穿越：白名单是精确匹配，不是前缀 —— 前缀匹配会把这条放过去
		{http.MethodPost, "https://argocd.example.com/api/v1/session/../applications/wallet/sync", "穿越白名单"},
		// Harbor 也在闸门内（2026-08 起）：它上面放着全部镜像
		{http.MethodDelete, "https://harbor.example.com/api/v2.0/projects/appA/repositories/x", "Harbor 删仓库"},
		{http.MethodPost, "https://harbor.example.com/api/v2.0/replication/executions", "Harbor 触发复制"},
	} {
		req, _ := http.NewRequestWithContext(context.Background(), tc.method, tc.url, strings.NewReader("{}"))
		var blocked *ErrWriteBlocked
		if _, err := c.Do(req); !errors.As(err, &blocked) {
			t.Errorf("%s（%s %s）没有被拦下：err=%v", tc.what, tc.method, tc.url, err)
		}
	}

	// 反向：登录端点必须放行，否则整个采集都跑不起来
	for _, u := range []string{
		"https://argocd.example.com/api/v1/session",
		"https://kite.example.com/api/auth/login/password",
		"https://rancher.example.com/v3-public/localProviders/local?action=login",
	} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, u, strings.NewReader("{}"))
		var blocked *ErrWriteBlocked
		if _, err := c.Do(req); errors.As(err, &blocked) {
			t.Errorf("登录端点被误拦：%s", u)
		}
	}
}
