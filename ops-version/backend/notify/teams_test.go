package notify

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const goodTeams = "https://example-env.environment.api.powerplatform.com:443/powerautomate/automations/direct/workflows/0a1b2c/triggers/manual/paths/invoke?api-version=1&sp=%2Ftriggers%2Fmanual%2Frun&sv=1.0&sig=AbC-123_x"

func TestValidateTeamsWebhook(t *testing.T) {
	if err := ValidateTeamsWebhook(goodTeams); err != nil {
		t.Fatalf("真实格式的地址被拒: %v", err)
	}
	dirty := strings.Replace(goodTeams, "/triggers/", "/trig​gers /", 1)
	if err := ValidateTeamsWebhook(NormalizeTeamsWebhook(dirty)); err != nil {
		t.Errorf("清洗后应通过: %v", err)
	}
	for name, bad := range map[string]string{
		"http":   strings.Replace(goodTeams, "https://", "http://", 1),
		"其他主机":   strings.Replace(goodTeams, "environment.api.powerplatform.com", "evil.com", 1),
		"后缀伪造":   strings.Replace(goodTeams, ".powerplatform.com:443", ".powerplatform.com.evil.com:443", 1),
		"非443端口": strings.Replace(goodTeams, ":443", ":8443", 1),
		"缺sig":   strings.Replace(goodTeams, "&sig=AbC-123_x", "", 1),
		"路径不对":   strings.Replace(goodTeams, "/powerautomate/", "/x/", 1),
		"路径..":   strings.Replace(goodTeams, "/0a1b2c/", "/../x/", 1),
		"飞书地址":   "https://open.feishu.cn/open-apis/bot/v2/hook/abc",
	} {
		if ValidateTeamsWebhook(bad) == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
}

func sampleReplication() Replication {
	return Replication{
		Level: LevelFailed, Policy: "sync-a-to-b", OrgName: "PA", DestRegistry: "https://harbor.example.com",
		Succeeded: 2, Failed: 1, Duration: 75 * time.Second, Trigger: "SCHEDULE", ExecID: 4242, Realtime: true,
		OK:   []Image{{Service: "game-frontend", Tag: "20260921-1"}, {Service: "lobby", Tag: ""}},
		Bad:  []Image{{Service: "wallet", Tag: "20260921-2", Reason: "pull timeout"}},
		Page: 1, Pages: 1, OKTotal: 2, OKFrom: 1,
	}
}

func TestTeamsCardContent(t *testing.T) {
	b := TeamsCard(sampleReplication())
	var env map[string]any
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("不是合法 JSON: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"type":"message"`, `"contentType":"application/vnd.microsoft.card.adaptive"`,
		`"style":"attention"`, "❌ 镜像同步失败 · sync-a-to-b", "**平台**　PA · harbor.example.com",
		"**失败镜像**（1 个）", "　└ 原因：pull timeout", "**成功镜像**（2 个）", "　└ 20260921-1", "未取到版本号",
		"Execution 4242 · 实时通知"} {
		if !strings.Contains(s, want) {
			t.Errorf("卡片缺少 %q", want)
		}
	}
	if strings.Contains(s, "`") {
		t.Error("Teams 不认行内代码，反引号应去掉")
	}
	if strings.Index(s, "失败镜像") > strings.Index(s, "成功镜像") {
		t.Error("失败清单必须排在成功清单前面")
	}
	r := sampleReplication()
	r.Level, r.Bad, r.Failed = LevelOK, nil, 0
	if !strings.Contains(string(TeamsCard(r)), `"style":"good"`) {
		t.Error("成功应为绿色")
	}
}

// 超预算从尾部砍行并写明，失败清单保留
func TestTeamsCardBudget(t *testing.T) {
	r := sampleReplication()
	r.OK = nil
	for i := 0; i < 400; i++ {
		r.OK = append(r.OK, Image{Service: fmt.Sprintf("app-demo-game-frontend-%03d", i), Tag: "20260917084842-6"})
	}
	b := TeamsCard(r)
	if len(b) > teamsByteBudget {
		t.Fatalf("卡片 %d 字节超过预算 %d", len(b), teamsByteBudget)
	}
	s := string(b)
	if !strings.Contains(s, "卡片过长，其余未列出") || !strings.Contains(s, "pull timeout") {
		t.Error("应写明截断，且失败清单要保留")
	}
}

func TestPostTeamsStatus(t *testing.T) {
	for code, wantOK := range map[int]bool{200: true, 202: true, 400: false, 401: false, 404: false, 429: false, 500: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(code)
		}))
		err := postTeams(srv.URL, []byte(`{}`))
		srv.Close()
		if (err == nil) != wantOK {
			t.Errorf("HTTP %d：期望成功=%v，实际 err=%v", code, wantOK, err)
		}
	}
}

func TestTeamsErrorRedacted(t *testing.T) {
	u := "http://127.0.0.1:1/powerautomate/x?sig=SecretSig_999"
	err := postTeams(u, []byte(`{}`))
	if err == nil || strings.Contains(err.Error(), "SecretSig_999") {
		t.Errorf("连接错误里不应出现 sig: %v", err)
	}
}

// 按类型分派：Teams 渠道填了飞书地址会被发送前的校验拦下，不会真发出去
func TestDeliverByKind(t *testing.T) {
	err := Deliver(KindTeams, "https://open.feishu.cn/open-apis/bot/v2/hook/abc", sampleReplication())
	if err == nil || !strings.Contains(err.Error(), "powerplatform") {
		t.Errorf("Teams 渠道配飞书地址应被拒: %v", err)
	}
	if err := SendTest(KindLark, "", "x"); err == nil {
		t.Error("飞书空地址不能返回成功")
	}
	if !ValidKind("lark") || !ValidKind("teams") || ValidKind("slack") {
		t.Error("ValidKind 不对")
	}
}
