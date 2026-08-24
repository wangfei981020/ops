package providers

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const onePod = `{"items":[{
  "metadata":{"name":"wallet-backend-7d9f8c-abcde","namespace":"biz-uat"},
  "spec":{"nodeName":"node-1"},
  "status":{"phase":"Running","podIP":"10.1.2.3",
    "containerStatuses":[{"name":"app","ready":true,"restartCount":2,
      "image":"harbor.x.com/p/wallet-backend:v1.2.3",
      "imageID":"harbor.x.com/p/wallet-backend@sha256:abc"}]}}]}`

// 🔴 回归：只有 pods 权限时（deployments 被拒），走 listFromPods 反推版本 ——
// 那份 Pod 数据**已经在手里**，必须一并带出去做明细。
//
// 原来只填了 Services，Pods 字段空着：表现是「版本比对有 164 行，
// 而导出的 Pod 明细页整页空，只有一句『采集成功，但没有 Pod 明细』」——
// 而这条路径正是**最需要明细**的那种账号（连 Deployment 都看不到）。
func TestListFromPodsAlsoReturnsPodDetails(t *testing.T) {
	r := fakeRancher(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.Contains(req.URL.Path, "/deployments"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"deployments is forbidden at the cluster scope"}`))
		case strings.HasSuffix(req.URL.Path, "/pods"):
			_, _ = w.Write([]byte(onePod))
		default:
			_, _ = w.Write([]byte(`{"items":[]}`))
		}
	})
	res, err := r.ListServices(context.Background(), "local",
		Rules{NS: NSRules{Include: []string{"biz-uat"}}}, true)
	if err != nil {
		t.Fatalf("应从 Pod 反推成功：%v", err)
	}
	if len(res.Services) == 0 {
		t.Fatal("没反推出服务")
	}
	if len(res.Pods) == 0 {
		t.Fatal("Pod 明细为空 —— 数据已经拿到手里却被扔了，" +
			"导出的明细页会整页空白")
	}
	p := res.Pods[0]
	// 明细里最容易被中途丢掉的几项（mergeItems 那次就是这几个）
	if p.Node != "node-1" || p.PodIP != "10.1.2.3" {
		t.Errorf("节点/IP 丢了：node=%q ip=%q", p.Node, p.PodIP)
	}
	if p.Restarts != 2 || !p.Ready {
		t.Errorf("重启次数/就绪状态丢了：restarts=%d ready=%v", p.Restarts, p.Ready)
	}
	if p.Namespace != "biz-uat" {
		t.Errorf("命名空间丢了：%q", p.Namespace)
	}
}

// 🔴 整集群拉 Pod 被拒时，用刚采到的服务所在 ns 逐个再拉。
//
// 那些 ns 的读权限一定有（它们的 Deployment 刚读到）。
// 不兜的话，project 级账号永远拿不到 Pod 明细 ——
// 而"能拿到多少就给多少"本来完全做得到。
func TestPodsFallBackToSnapshotNamespaces(t *testing.T) {
	var perNS bool
	r := fakeRancher(t, func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		switch {
		// 整集群 deployments 放行（账号能读 workload，但读不了整集群 pods）
		case strings.HasSuffix(p, "/apis/apps/v1/deployments"):
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"wallet-backend","namespace":"biz-uat"},
			  "spec":{"template":{"spec":{"containers":[{"image":"h/p/wallet-backend:v1"}]}}}}]}`))
		case strings.HasSuffix(p, "/api/v1/pods"):
			w.WriteHeader(http.StatusForbidden) // 整集群 pods 被拒
		case strings.Contains(p, "/namespaces/biz-uat/pods"):
			perNS = true
			_, _ = w.Write([]byte(onePod))
		default:
			_, _ = w.Write([]byte(`{"items":[]}`))
		}
	})
	// ns 规则留空 → 走整集群路径
	res, err := r.ListServices(context.Background(), "local", Rules{}, true)
	if err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	if !perNS {
		t.Fatal("整集群 pods 被拒后没有按 ns 兜底 —— project 级账号永远拿不到明细")
	}
	if len(res.Pods) == 0 {
		t.Error("兜底拉到了却没带出来")
	}
}
