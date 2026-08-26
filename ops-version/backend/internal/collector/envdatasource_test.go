package collector

import (
	"testing"

	"ops-version-backend/internal/store"
)

// 环境选了 ArgoCD 数据源时，必须用 ArgoCD 的协议 —— 不能用平台级的 rancher。
//
// 🔴 实测过过：A公司 平台类型 rancher、PROD 环境选了 argocd 数据源，
// 结果拿 rancher 的登录端点去连 ArgoCD 地址，报 i/o timeout，
// 而配置页上每一项看着都对。
func TestProviderTypeFollowsEnvDatasource(t *testing.T) {
	org := store.Org{ProviderType: "rancher", DSProviderType: "rancher"}
	env := store.OrgEnv{
		DSEndpoint: "https://argocd.example.com", DSProviderType: "argocd",
	}
	c := &Collector{}
	p, err := c.BuildProvider(org, env)
	if err != nil {
		t.Fatalf("建 provider 失败: %v", err)
	}
	if got := p.Type(); got != "argocd" {
		t.Fatalf("环境选了 argocd 数据源，却建出了 %s —— 会拿错协议去连", got)
	}
}

// 环境选了 ArgoCD 数据源时，"要不要填集群"的判据也必须跟着走。
//
// 🔴 这是同一个 bug 的第二处：上一版只改了建 provider 那处，
// 集群检查仍用平台类型，于是报「未配置集群」——而 ArgoCD 本来就不需要填，
// 人照着提示去填集群反而更错。实测撞到过过（2026-08-25）。
func TestClusterRequirementFollowsEnvDatasource(t *testing.T) {
	org := store.Org{ProviderType: "rancher"}
	env := store.OrgEnv{DSEndpoint: "https://argocd.example.com", DSProviderType: "argocd"}
	if got := providerTypeOf(org, env); got != "argocd" {
		t.Fatalf("实际类型应为 argocd，得到 %s —— 集群检查会误判成必填", got)
	}
	// 反向：环境没选数据源时，仍以平台类型为准
	if got := providerTypeOf(org, store.OrgEnv{}); got != "rancher" {
		t.Fatalf("环境没选数据源时应回落到平台类型，得到 %s", got)
	}
}
