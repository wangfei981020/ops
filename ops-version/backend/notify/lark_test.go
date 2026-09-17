package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeLark 一个假的飞书接收端：记下收到的报文，按 want 回应。
func fakeLark(t *testing.T, status int, body string, got *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if got != nil {
			_ = json.Unmarshal(raw, got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// 卡片真的以 interactive 形态发出去，且标题栏颜色与成败一致
func TestSendFeishuCardDelivers(t *testing.T) {
	var got map[string]any
	srv := fakeLark(t, 200, `{"code":0,"msg":"success","data":{}}`, &got)
	defer srv.Close()

	r := Replication{
		Level: LevelFailed, Policy: "sync-to-a-appA", OrgName: "A平台",
		DestRegistry: "https://registry.example.com", Trigger: "manual",
		ExecID: 22213, Total: 2, Succeeded: 1, Failed: 1,
		Duration: 36 * time.Second, SyncedAt: time.Now(), Realtime: true,
		OK:  []Image{{Service: "appA-bi-frontend", Tag: "t-3"}},
		Bad: []Image{{Service: "appA-wallet-backend", Tag: "t-8", Reason: "manifest unknown"}},
	}
	if err := SendFeishuCard(srv.URL, Card(r)); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if got["msg_type"] != "interactive" {
		t.Errorf("飞书收到的不是交互卡片：%v", got["msg_type"])
	}
	raw, _ := json.Marshal(got)
	for _, want := range []string{`"template":"red"`, "appA-wallet-backend", "manifest unknown", "22213"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("卡片里缺少 %q：%s", want, raw)
		}
	}
}

/*
🔴 HTTP 200 也可能是失败 —— 这条判定对卡片和文本消息一样适用。

webhook 被重置、机器人被移出群时，飞书返回 **200 + code≠0**。
只看网络错误的话，所有投递失败都会被记成"已送达"，
而界面上写着已送达就没人会去查。
*/
func TestCardRejectedByFeishuIsAnError(t *testing.T) {
	srv := fakeLark(t, 200, `{"code":19001,"msg":"param invalid"}`, nil)
	defer srv.Close()
	err := SendFeishuCard(srv.URL, Card(sample()))
	if err == nil {
		t.Fatal("飞书 code≠0 必须当成失败，否则投递失败会被记成已送达")
	}
	if !strings.Contains(err.Error(), "19001") {
		t.Errorf("错误里要带上飞书给的 code，方便排查：%v", err)
	}
}

// 响应不是飞书的 JSON（webhook 填成了别的地址）也不能算成功
func TestCardNonJSONResponseIsAnError(t *testing.T) {
	srv := fakeLark(t, 200, `<html>登录页</html>`, nil)
	defer srv.Close()
	if err := SendFeishuCard(srv.URL, Card(sample())); err == nil {
		t.Fatal("响应不是飞书 JSON 时不能断言成功")
	}
}

// 没配 webhook 不是"发成功了" —— 返回 nil 会让记录写成 sent
func TestEmptyWebhookIsAnError(t *testing.T) {
	if err := SendFeishuCard("", Card(sample())); err == nil {
		t.Fatal("没有配置 webhook 时必须报错，不能假装发成功")
	}
}
