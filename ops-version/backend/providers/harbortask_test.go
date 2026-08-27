package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 🔴 这个测试锁住 的根因。
//
// 原来的取值是：
//
//	name := t.DstRes
//	if name == "" { name = t.SrcRes }
//
// 看着是「dst 优先、src 兜底」，实际是**非空即短路**。
// 而 Harbor 的 dst_resource 常常写成 `bizB/xxx [1 item(s) in total]` ——
// 非空、能解析出服务名、**但没有版本号**。于是真正带版本号的字段
// 永远轮不到，结果是服务名对、版本号恒为空。
//
// 版本号恰恰是这条链路存在的全部意义，所以「取到了名字」不算成功。
func TestTasksPicksFieldThatActuallyHasTag(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantTag string
	}{
		{
			// 实测过形态：dst 非空但没版本号，src 带版本号
			name: "dst没版本号_src有",
			body: `[{"id":1,"status":"Succeed",
			        "dst_resource":"bizB/svc-frontend [1 item(s) in total]",
			        "src_resource":"partner/svc-frontend:20260825093501-28"}]`,
			wantTag: "20260825093501-28",
		},
		{
			// dst 自己带版本号时就用 dst
			name: "dst有版本号",
			body: `[{"id":1,"status":"Succeed",
			        "dst_resource":"bizB/svc-a:v9",
			        "src_resource":"partner/svc-a:v1"}]`,
			wantTag: "v9",
		},
		{
			// 有些场景版本号在 resource 这个对象里
			name: "版本号在resource对象里",
			body: `[{"id":1,"status":"Succeed",
			        "dst_resource":"bizB/svc-b [1 item(s) in total]",
			        "src_resource":"partner/svc-b [1 item(s) in total]",
			        "resource":{"repository":"partner/svc-b","tag":"20260825-77"}}]`,
			wantTag: "20260825-77",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			h := &Harbor{Endpoint: srv.URL, Username: "u", Password: "p"}
			tasks, err := h.Tasks(context.Background(), 1)
			if err != nil {
				t.Fatalf("Tasks 出错: %v", err)
			}
			if len(tasks) != 1 {
				t.Fatalf("要 1 个 task，得到 %d", len(tasks))
			}
			if tasks[0].Tag != tc.wantTag {
				t.Errorf("版本号要 %q，得到 %q（服务名 %q）—— "+
					"取到名字但丢了版本号，就是 的原样复现",
					tc.wantTag, tasks[0].Tag, tasks[0].ServiceKey)
			}
		})
	}
}

// 变异测试：把修复退回原来的写法，这个测试必须失败。
// 只验证「修完是绿的」什么都不证明。
func TestTasksMutation_OldLogicWouldFail(t *testing.T) {
	body := `[{"id":1,"status":"Succeed",
	           "dst_resource":"bizB/svc [1 item(s) in total]",
	           "src_resource":"partner/svc:v123"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	h := &Harbor{Endpoint: srv.URL, Username: "u", Password: "p"}
	tasks, _ := h.Tasks(context.Background(), 1)

	// 老逻辑等价物：dst 非空就用它，不看有没有版本号
	oldName := "bizB/svc [1 item(s) in total]"
	oldRef := imageRefForTest(oldName)
	if oldRef != "" {
		t.Fatalf("前提失效：老逻辑本应拿不到版本号，却拿到 %q", oldRef)
	}
	if len(tasks) != 1 || tasks[0].Tag != "v123" {
		t.Fatalf("新逻辑应当拿到 v123，实际 %+v", tasks)
	}
}

// imageRefForTest 复现老逻辑的取值：只看一个字段，取到什么算什么。
func imageRefForTest(name string) string {
	return parseTagForTest(CleanHarborName(name))
}

func parseTagForTest(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return s[i+1:]
		}
		if s[i] == '/' {
			break
		}
	}
	return ""
}
