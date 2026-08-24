package providers

import (
	"strings"
	"testing"
)

// 403 提示必须分清两种成因 —— 它们该做的事**相反**。
func TestRancherForbiddenHint(t *testing.T) {
	// 集群级 list 被拒：是我们要多了，不该引导去放大权限
	cluster := rancherForbiddenHint(
		"/k8s/clusters/local/apis/apps/v1/deployments",
		`{"message":"deployments.apps is forbidden: User \"u-xxxxx\" cannot list resource \"deployments\" in API group \"apps\" at the cluster scope"}`)
	if !strings.Contains(cluster, "ns 包含") {
		t.Errorf("集群级被拒时要引导填 ns 包含，实得：%s", cluster)
	}
	if !strings.Contains(cluster, "at the cluster scope") {
		t.Errorf("Rancher 原话里有这句时要点出来（它是判据），实得：%s", cluster)
	}
	// 🔴 不能出现"去绑集群只读角色"这类引导 —— 那会让人为了让工具跑起来而放弃最小权限
	if strings.Contains(cluster, "绑定集群") {
		t.Errorf("集群级被拒时不该引导放大权限，实得：%s", cluster)
	}

	// 单个 ns 被拒：这才是账号确实缺授权
	single := rancherForbiddenHint(
		"/k8s/clusters/local/apis/apps/v1/namespaces/app-uat/deployments", "")
	if !strings.Contains(single, "app-uat") {
		t.Errorf("逐 ns 被拒时要指名是哪个 ns，实得：%s", single)
	}
	if strings.Contains(single, "ns 包含里填上") {
		t.Errorf("逐 ns 被拒时不该再让人填 ns（已经填了），实得：%s", single)
	}
}
