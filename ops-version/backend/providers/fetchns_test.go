package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 逐 ns 拉取的容错分支：单个 ns 失败要继续，全失败必须报错。
//
// 🔴 这两条在真实环境里跑不到 —— Kite 对不存在的 ns 返回 200 空列表，
// 只有权限不足/网络错误才会走进去。而「没跑过的路径」正是最容易写错的地方，
// 所以用假服务端把它们逼出来。
func TestKiteFetchByNamespacesPartialFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只有 good 这个 ns 能读，bad 返回 403
		if strings.Contains(r.URL.Path, "/bad") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"no permission"}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"svc","namespace":"good"},` +
			`"spec":{"nodeName":"node-1"},"status":{"phase":"Running","podIP":"10.0.0.1"}}]}`))
	}))
	defer srv.Close()
	k := &Kite{Endpoint: srv.URL, AuthType: "api_key", APIKey: "x"}

	t.Run("部分失败要继续", func(t *testing.T) {
		data, err := k.fetchByNamespaces(context.Background(), "deployments", "c", []string{"bad", "good"})
		if err != nil {
			t.Fatalf("一个 ns 失败不该让整体失败: %v", err)
		}
		var l struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(data, &l) != nil || len(l.Items) != 1 {
			t.Fatalf("应保留 good 的 1 条，实得 %s", data)
		}
		// 🔴 字段保真：nodeName/podIP 只存在于原始 JSON，
		//    一旦合并时解析成 k8sList 再序列化就会静默消失
		if !strings.Contains(string(data), "node-1") || !strings.Contains(string(data), "10.0.0.1") {
			t.Errorf("Pod 字段被丢弃了: %s", data)
		}
	})

	t.Run("全失败必须报错", func(t *testing.T) {
		_, err := k.fetchByNamespaces(context.Background(), "deployments", "c", []string{"bad", "bad"})
		if err == nil {
			t.Fatal("全部 ns 都失败却返回了 nil —— 界面上会显示成「这个平台没有任何服务」，" +
				"跟「对方真的下线了所有服务」分不出来")
		}
	})
}

// 403 提示必须分清两种成因：全量请求是我们要多了，逐 ns 请求才是角色真缺权限。
func TestForbiddenHint(t *testing.T) {
	full := forbiddenHint("/api/v1/_clusters/uat-01/deployments")
	if !strings.Contains(full, "全命名空间") || !strings.Contains(full, "uat-01") {
		t.Errorf("全量请求的提示要点明「需要全命名空间权限」且带集群名，实得：%s", full)
	}
	if strings.Contains(full, "补上") {
		t.Errorf("全量请求不该引导用户去放大权限，实得：%s", full)
	}
	perNS := forbiddenHint("/api/v1/_clusters/uat-01/deployments/app-uat")
	if !strings.Contains(perNS, "app-uat") || !strings.Contains(perNS, "uat-01") {
		t.Errorf("逐 ns 的提示要指名集群与命名空间，实得：%s", perNS)
	}
}
