package providers

import (
	"context"
	"os"
	"testing"
)

// 打真实 ArgoCD：副本缩到 0 的服务**必须**被采到。
//
// 🔴 这条是这个 provider 的核心风险：
// ArgoCD 的 status.summary.images 只汇总**运行中 Pod** 的镜像，
// 副本 0 的服务在那里是空的 —— 照搬会把缩容服务显示成"对方没部署"，
// 而它明明有明确的版本定义。所以实现必须走 managed-resources 的 liveState。
func TestArgoCDIncludesScaledToZero(t *testing.T) {
	ep := os.Getenv("ARGOCD_EP")
	if ep == "" {
		t.Skip("需要 ARGOCD_EP")
	}
	a := &ArgoCD{Endpoint: ep, AuthType: "password",
		Username: os.Getenv("ARGOCD_USER"), Password: os.Getenv("ARGOCD_PASS"),
		InsecureTLS: true}
	ctx := context.Background()

	if err := a.Probe(ctx); err != nil {
		t.Fatalf("探测失败: %v", err)
	}

	res, err := a.ListServices(ctx, "", Rules{NS: NSRules{Include: []string{"app-*"}}}, false)
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if len(res.Services) == 0 {
		t.Fatal("一个服务都没采到")
	}
	// ⚠️ 不能按"服务条数"断言：本地 ArgoCD 的几个应用镜像全是 nginx:1.27，
	//    而 ServiceKey 取的是**镜像名最后一段**（跨平台对账 key 必须一致），
	//    于是它们本来就会合并成一个服务。那是测试数据的形态，不是缺陷。
	//    要验的是**那个副本 0 的 workload 有没有被采进来**。
	var workloads []string
	for _, s := range res.Services {
		workloads = append(workloads, s.Workloads...)
		t.Logf("  服务 %-12s ns=%-10s tag=%-8s workloads=%v",
			s.ServiceKey, s.Namespace, s.Tag, s.Workloads)
	}
	const scaledToZero = "app-atmosphere-client-backend" // replicas=0，summary.images 为空
	var found bool
	for _, w := range workloads {
		if w == scaledToZero {
			found = true
		}
	}
	if !found {
		t.Errorf("副本 0 的 %s 没被采到 —— 说明取的是 status.summary.images（只汇总运行中 Pod）"+
			"而不是 managed-resources 的 liveState。实采 workloads=%v", scaledToZero, workloads)
	}
}

// token 认证与账密认证走**不同**的取 token 路径，两条都要能用。
func TestArgoCDAuthTypes(t *testing.T) {
	a := &ArgoCD{AuthType: "token", APIKey: "abc"}
	got, err := a.ensureToken(context.Background())
	if err != nil || got != "abc" {
		t.Errorf("token 模式应直接用配置里的 token，实得 %q err=%v", got, err)
	}
	// 空 token 必须报错，不能拿空串去请求（那会以 401 的面目出现，被误判成凭据错）
	b := &ArgoCD{AuthType: "token"}
	if _, err := b.ensureToken(context.Background()); err == nil {
		t.Error("没配 token 却没报错")
	}
}
