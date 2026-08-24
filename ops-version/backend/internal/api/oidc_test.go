package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"ops-version-backend/internal/auth"
	"ops-version-backend/internal/store"
)

// 组 claim 各家形态不同，都要认。
//
// 🔴 只认 []any 的话，后几种会静默变成"没有任何组"，
// 然后所有人掉进默认角色 —— 而日志里显示 groups: []，
// 看起来像 IdP 没下发，其实是我们没解析。
func TestToStringSlice(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"标准数组", []any{"a", "b"}, []string{"a", "b"}},
		{"逗号分隔的单串", "a,b,c", []string{"a", "b", "c"}},
		{"空格分隔的单串", "a b", []string{"a", "b"}},
		{"只有一个组时退化成裸串", "solo", []string{"solo"}},
		{"混入非字符串元素", []any{"a", 42, "b"}, []string{"a", "b"}},
		{"带空白要 trim", []any{" a ", "b"}, []string{"a", "b"}},
		{"空数组", []any{}, []string{}},
		{"空串", "", nil},
		{"claim 不存在", nil, nil},
		{"数字类型不认", 42, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toStringSlice(c.in)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("toStringSlice(%#v) = %#v，要 %#v", c.in, got, c.want)
			}
		})
	}
}

// state 是一次性的：重放同一个必须失败。
func TestStateStoreSingleUse(t *testing.T) {
	s := newStateStore()
	v := s.issue()
	if !s.consume(v) {
		t.Fatal("刚签发的 state 应当可用")
	}
	if s.consume(v) {
		t.Error("同一个 state 被消费了两次 —— 重放攻击可行")
	}
	if s.consume("never-issued") {
		t.Error("没签发过的 state 被接受了")
	}
}

func TestFirstString(t *testing.T) {
	if got := firstString(nil, "", "  ", "x", "y"); got != "x" {
		t.Errorf("应取第一个非空串，实得 %q", got)
	}
	if got := firstString(42, nil); got != "" {
		t.Errorf("没有可用字符串时应返回空，实得 %q", got)
	}
}

// token 交换与 userinfo 的网络层。
func TestOIDCExchangeAndUserinfo(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			gotForm = r.PostForm
			_, _ = w.Write([]byte(`{"access_token":"AT","id_token":"IT","token_type":"Bearer"}`))
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer AT" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// 🔴 复刻 MXID 的形态：name / email / 组**只在 userinfo**，
			//    id_token 里没有。只解 id_token 的接入方必然拿不到角色。
			_, _ = w.Write([]byte(`{"sub":"1234567890","preferred_username":"zhangsan",
				"name":"张三","email":"z@example.com","app_roles":["ops-admin","dev-team"]}`))
		}
	}))
	defer srv.Close()

	s := &Server{}
	cfg := store.OIDCConfig{TokenURL: srv.URL + "/token", UserinfoURL: srv.URL + "/userinfo",
		ClientID: "cid", GroupsClaim: "app_roles", UsernameClaim: "preferred_username"}

	tok, err := s.oidcExchange(context.Background(), cfg, "secret", "the-code", "https://x/cb")
	if err != nil {
		t.Fatalf("换 token 失败: %v", err)
	}
	if tok != "AT" {
		t.Errorf("token = %q，要 AT", tok)
	}
	// redirect_uri 必须**一字不差**地送过去 —— 与 IdP 登记的对不上就报 mismatch
	if gotForm.Get("redirect_uri") != "https://x/cb" || gotForm.Get("grant_type") != "authorization_code" {
		t.Errorf("表单不对: %v", gotForm)
	}

	claims, err := s.oidcUserinfo(context.Background(), cfg, tok)
	if err != nil {
		t.Fatalf("userinfo 失败: %v", err)
	}
	if firstString(claims["name"]) != "张三" {
		t.Errorf("name 没取到：%v", claims["name"])
	}
	groups := toStringSlice(claims[cfg.GroupsClaim])
	if len(groups) != 2 {
		t.Fatalf("组没解析出来：%v", groups)
	}

	// 组 → 角色：命中 ops-admin(admin) 与 dev-team(editor)。
	// admin 覆盖 editor 的全部权限，所以落到 admin（不是因为"等级高"，
	// 而是因为它覆盖了并集 —— 自定义角色出现后只有覆盖关系是可靠的）
	rres := auth.ResolveRole(groups, []auth.RoleRule{
		{GroupValue: "ops-admin", RoleCode: auth.RoleAdmin},
		{GroupValue: "dev-team", RoleCode: auth.RoleEditor},
	})
	role, hits := rres.Role, rres.Matched
	if len(rres.Dropped) > 0 {
		t.Errorf("内置角色是包含关系，不该丢权限，实得 dropped=%v", rres.Dropped)
	}
	if role != auth.RoleAdmin {
		t.Errorf("角色 = %q，要 admin（两条都命中时取高的）", role)
	}
	if len(hits) != 2 {
		t.Errorf("命中组 = %v，要 2 个", hits)
	}
}

// 🔴 没配 userinfo 地址必须**明确报错**，不能悄悄退回只解 id_token。
// 那正是 MXID 上会让人查半天的形态：登录成功、但所有人都没有角色。
func TestOIDCUserinfoRequired(t *testing.T) {
	s := &Server{}
	_, err := s.oidcUserinfo(context.Background(), store.OIDCConfig{}, "AT")
	if err == nil {
		t.Fatal("没配 userinfo 地址却没报错")
	}
	if !strings.Contains(err.Error(), "userinfo") {
		t.Errorf("报错要点明缺的是 userinfo，实得：%v", err)
	}
}

// 「没匹配到授权组」的四种成因，提示必须指向**不同的**处理位置。
// 笼统一句"联系管理员"会让管理员在 IdP 和本系统两侧都翻一遍。
func TestUnmappedHint(t *testing.T) {
	cases := []struct {
		name         string
		claimPresent bool
		groups       []string
		rules        int
		wantContains string
	}{
		// IdP 压根没下发这个字段 → 去 IdP 检查应用配置
		{"claim 缺失", false, nil, 3, "没有下发"},
		// 下发了但空 → 这个人没被分配角色
		{"claim 为空", true, nil, 3, "内容为空"},
		// 有组但系统一条映射都没配 → 去加映射
		{"系统无映射", true, []string{"g1"}, 0, "还没有配置任何"},
		// 有组也有映射，就是没匹配上 → 检查组名
		{"组名对不上", true, []string{"g1"}, 3, "没有匹配到任何映射规则"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := unmappedHint("Downing", "app_roles", c.claimPresent, c.groups, c.rules)
			if !strings.Contains(got, c.wantContains) {
				t.Errorf("提示里要有 %q，实得：%s", c.wantContains, got)
			}
			// 每种都要带上用户名和 claim 名 —— 管理员是拿着截图来查的
			if !strings.Contains(got, "Downing") {
				t.Errorf("提示里没有用户名：%s", got)
			}
		})
	}
}
