package providers

import (
	"context"
	"os"
	"testing"
)

// 逐 ns 路径与全量路径必须产出一致的结果（含 Pod 明细字段）。
// 打真实本地 Kite —— 这类"参数被静默忽略""字段被静默丢弃"的问题
// 只有比对真实数据才能发现，mock 一定是绿的。
func TestKitePerNamespaceMatchesFullScan(t *testing.T) {
	ep := os.Getenv("KITE_EP")
	if ep == "" {
		t.Skip("需要 KITE_EP")
	}
	k := &Kite{Endpoint: ep, AuthType: "password",
		Username: os.Getenv("KITE_USER"), Password: os.Getenv("KITE_PASS")}
	ctx := context.Background()
	const cluster = "docker-desktop"
	nss := []string{"platform", "ops-version", "argocd"}

	// 🔴 对照组必须用**等价的 NS 规则**，只让请求路径不同。
	//
	// 第一版我用 `Include: ["*"]` 拉全量、再按 snapshot.Namespace 手工过滤，
	// 结果差了一个 mysql —— 因为全量时同名服务是**跨 ns 合并**成一个 snapshot 的，
	// 合并后 Namespace 只剩其中一个，手工过滤自然漏掉。
	// 那是我对照组设错，不是采集有问题。
	//
	// `argocd*` 带通配 → exactNamespaces 返回 nil → 走集群级请求，
	// 而规则本身与下面那组等价（argocd* 只匹配到 argocd 一个 ns）。
	wildcardNS := []string{"platform", "ops-version", "argocd*"}
	full, err := k.ListServices(ctx, cluster, Rules{NS: NSRules{Include: wildcardNS}}, true)
	if err != nil {
		t.Fatalf("全量失败: %v", err)
	}
	type svc struct {
		tag      string
		conflict bool
	}
	wantSvc := map[string]svc{}
	for _, s := range full.Services {
		wantSvc[s.ServiceKey] = svc{s.Tag, len(s.Conflicts) > 0}
	}

	perNS, err := k.ListServices(ctx, cluster, Rules{NS: NSRules{Include: nss}}, true)
	if err != nil {
		t.Fatalf("逐 ns 失败: %v", err)
	}
	gotSvc := map[string]svc{}
	for _, s := range perNS.Services {
		gotSvc[s.ServiceKey] = svc{s.Tag, len(s.Conflicts) > 0}
	}

	if len(gotSvc) != len(wantSvc) {
		for k2 := range gotSvc {
			if _, ok := wantSvc[k2]; !ok {
				t.Errorf("只在逐ns出现: %s", k2)
			}
		}
		for k2 := range wantSvc {
			if _, ok := gotSvc[k2]; !ok {
				t.Errorf("只在全量出现: %s", k2)
			}
		}
		t.Fatalf("服务数不一致: 逐ns=%d 全量过滤=%d", len(gotSvc), len(wantSvc))
	}
	for k2, w := range wantSvc {
		g := gotSvc[k2]
		if g.conflict != w.conflict {
			t.Errorf("%s 冲突标记不一致: 逐ns=%v 全量=%v", k2, g.conflict, w.conflict)
			continue
		}
		// ⚠️ 冲突项不比 tag：同一个 service_key 在多个 ns 上版本不同时，
		//    tag 取哪个取决于遍历顺序（逐 ns 按规则顺序、全量按 API 返回顺序）。
		//    这不是缺陷 —— 有冲突时判定本来就被拒绝，显示哪个 tag 不影响结论。
		//    本地实测撞到的是 platform/redis(7-alpine) 与
		//    argocd/argocd-redis(8.2.3-alpine)：workload 名不同但镜像仓库同名，
		//    于是推导出同一个 service_key。正是冲突机制该拦的形态。
		if w.conflict {
			continue
		}
		if g.tag != w.tag {
			t.Errorf("%s tag 不一致: 逐ns=%q 全量=%q", k2, g.tag, w.tag)
		}
	}

	// 🔴 Pod 字段是重点：mergeItems 若解析成结构再序列化，这些会静默变空
	if len(perNS.Pods) == 0 {
		t.Fatal("逐 ns 没拿到任何 Pod")
	}
	var withNode, withIP, withStart int
	for _, p := range perNS.Pods {
		if p.Node != "" {
			withNode++
		}
		if p.PodIP != "" {
			withIP++
		}
		if !p.StartedAt.IsZero() {
			withStart++
		}
	}
	t.Logf("逐ns: 服务 %d, Pod %d (node=%d ip=%d started=%d)",
		len(gotSvc), len(perNS.Pods), withNode, withIP, withStart)
	// 这三个字段都只存在于 Pod 的 spec/status 里，k8sList 结构里没有——
	// 一旦合并时解析成结构再序列化，它们会全部静默变空。
	if withNode == 0 || withIP == 0 || withStart == 0 {
		t.Errorf("Pod 字段被丢弃: node=%d ip=%d started=%d（应当都 >0）", withNode, withIP, withStart)
	}

	// 全量与逐 ns 的 Pod 数也应一致（同样过滤到那三个 ns）
	if len(full.Pods) != len(perNS.Pods) {
		t.Errorf("Pod 数不一致: 逐ns=%d 全量=%d", len(perNS.Pods), len(full.Pods))
	}
}

func TestExactNamespaces(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want int // -1 表示期望 nil（必须回退到全量）
	}{
		{"确切名字", []string{"a", "b"}, 2},
		{"空规则回退全量", nil, -1},
		{"含通配必须回退全量", []string{"a", "app-*"}, -1},
		{"纯通配回退全量", []string{"*"}, -1},
		{"忽略空白项", []string{"a", "  ", "b"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := exactNamespaces(NSRules{Include: c.in})
			if c.want < 0 {
				if got != nil {
					t.Fatalf("应回退全量，却拿到 %v —— 逐 ns 请求会漏掉通配本该覆盖的 ns，且是静默漏", got)
				}
				return
			}
			if len(got) != c.want {
				t.Fatalf("要 %d 个，拿到 %v", c.want, got)
			}
		})
	}
}
