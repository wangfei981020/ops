package api

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ops-version-backend/internal/auth"
	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
	"ops-version-backend/providers"
)

// ─────────────────── state 存储 ───────────────────
//
// state 防 CSRF：授权请求带出去，回调时必须原样带回来。
//
// ⚠️ 放内存而不是数据库：它只活几分钟，进库反而要考虑清理。
// 代价是**多副本时会失败**（授权请求打到 A 副本、回调打到 B 副本）——
// 所以 replicaCount 必须保持 1，与 P3-10 选主未做是同一个约束。
type stateStore struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newStateStore() *stateStore { return &stateStore{m: map[string]time.Time{}} }

func (s *stateStore) issue() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	v := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	// 顺手清过期的，避免长期运行累积
	now := time.Now()
	for k, exp := range s.m {
		if now.After(exp) {
			delete(s.m, k)
		}
	}
	s.m[v] = now.Add(10 * time.Minute)
	return v
}

func (s *stateStore) consume(v string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[v]
	if !ok {
		return false
	}
	delete(s.m, v) // 一次性：重放同一个 state 必须失败
	return time.Now().Before(exp)
}

// ─────────────────── 配置读写 ───────────────────

func (s *Server) getOIDCConfig(w http.ResponseWriter, r *http.Request) {
	c, err := s.St.GetOIDCConfig(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	c.ClientSecretEnc = "" // 永不回显
	ok(w, c)
}

func (s *Server) saveOIDCConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		store.OIDCConfig
		ClientSecret string `json:"client_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	enc := ""
	if strings.TrimSpace(req.ClientSecret) != "" {
		v, err := s.Ciph.Encrypt(req.ClientSecret)
		if err != nil {
			fail(w, http.StatusInternalServerError, "internal", "密钥加密失败")
			return
		}
		enc = v
	}
	// 🔴 启用前必须校验。原来是解析→加密→直接存，一点校验都没有 ——
	//    于是可以存出一个「已启用但地址全空」的必坏配置，
	//    而它坏的是**登录页**，所有人都进不来。
	// ⚠️ hasSecret 要把「这次没传但库里已有」算进去，
	//    否则改个显示名就会被要求重填密钥。
	hasSecret := strings.TrimSpace(req.ClientSecret) != ""
	if !hasSecret {
		if cur, err := s.St.GetOIDCConfig(r.Context()); err == nil && cur.HasSecret {
			hasSecret = true
		}
	}
	if err := store.ValidateOIDC(req.OIDCConfig, hasSecret); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	err := s.St.SaveOIDCConfig(r.Context(), req.OIDCConfig, enc)
	s.St.Audit(r.Context(), userOf(r).Username, "oidc.config", "oidc", nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

func (s *Server) listRoleMappings(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListRoleMappings(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}

func (s *Server) saveRoleMapping(w http.ResponseWriter, r *http.Request) {
	var m store.RoleMapping
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	if strings.TrimSpace(m.GroupValue) == "" {
		fail(w, http.StatusBadRequest, "bad_request", "组名不能为空")
		return
	}
	// 🔴 校验 role_code：写错一个字（admln）会让这条映射永远不生效，
	//    而界面上看着好好的 —— 存进去之前就挡住。
	if !auth.IsKnownRole(m.RoleCode) {
		fail(w, http.StatusBadRequest, "bad_request",
			"角色不存在："+m.RoleCode+"（只能是 super_admin / admin / editor / viewer）")
		return
	}
	id, err := s.St.SaveRoleMapping(r.Context(), m)
	s.St.Audit(r.Context(), userOf(r).Username, "oidc.mapping.save", m.GroupValue, nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"id": id})
}

func (s *Server) deleteRoleMapping(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := s.St.DeleteRoleMapping(r.Context(), id)
	s.St.Audit(r.Context(), userOf(r).Username, "oidc.mapping.delete", fmt.Sprint(id), nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

// ─────────────────── 登录流程 ───────────────────

// oidcStatus 登录页用：要不要显示 SSO 按钮、按钮上写什么。
//
// ⚠️ 这个接口**不需要登录**（登录页要用），所以绝不能回任何配置细节 ——
// 只回"开没开"和"按钮文字"。
func (s *Server) oidcStatus(w http.ResponseWriter, r *http.Request) {
	c, err := s.St.GetOIDCConfig(r.Context())
	if err != nil || !c.Enabled {
		ok(w, map[string]any{"enabled": false})
		return
	}
	ok(w, map[string]any{"enabled": true, "display_name": c.DisplayName})
}

func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	c, err := s.St.GetOIDCConfig(r.Context())
	if err != nil || !c.Enabled {
		fail(w, http.StatusBadRequest, "bad_request", "未启用单点登录")
		return
	}
	if c.AuthorizeURL == "" || c.ClientID == "" {
		fail(w, http.StatusBadRequest, "bad_request", "单点登录配置不完整：缺授权地址或 Client ID")
		return
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", s.oidcRedirectURI(r))
	q.Set("scope", c.Scopes)
	q.Set("state", s.states.issue())

	sep := "?"
	if strings.Contains(c.AuthorizeURL, "?") {
		sep = "&"
	}
	http.Redirect(w, r, c.AuthorizeURL+sep+q.Encode(), http.StatusFound)
}

// oidcRedirectURI 回调地址。
//
// 🔴 必须用 X-Forwarded-* / Host 拼出**外部可见**的地址。
// 用 r.Host 在反代后面拿到的可能是内部地址，而 redirect_uri 是要
// 一字不差地和 IdP 里登记的对上的 —— 差一个端口就报 redirect_uri_mismatch。
// ⚠️ 反代那一侧必须用 $http_host 而不是 $host（后者不含端口）。
func (s *Server) oidcRedirectURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = strings.Split(v, ",")[0]
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = strings.Split(v, ",")[0]
	}
	return scheme + "://" + strings.TrimSpace(host) + "/api/auth/oidc/callback"
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		// IdP 侧拒绝（用户取消、应用未授权…）。原文进日志，给人看的只留一句
		logx.Warn("oidc", "idp_error", map[string]any{
			"error": e, "desc": q.Get("error_description")})
		s.oidcFailRedirect(w, r, "身份提供方拒绝了这次登录")
		return
	}
	code := q.Get("code")
	if code == "" {
		s.oidcFailRedirect(w, r, "回调里没有授权码")
		return
	}
	if !s.states.consume(q.Get("state")) {
		// state 对不上有两种可能：CSRF，或者后端重启过（state 在内存里）
		s.oidcFailRedirect(w, r, "登录状态已失效，请重新点一次登录")
		return
	}

	c, err := s.St.GetOIDCConfig(r.Context())
	if err != nil || !c.Enabled {
		s.oidcFailRedirect(w, r, "未启用单点登录")
		return
	}
	secret := ""
	if c.ClientSecretEnc != "" {
		if v, err := s.Ciph.Decrypt(c.ClientSecretEnc); err == nil {
			secret = v
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	tok, err := s.oidcExchange(ctx, c, secret, code, s.oidcRedirectURI(r))
	if err != nil {
		logx.Warn("oidc", "token_exchange_failed", map[string]any{"err": err.Error()})
		s.oidcFailRedirect(w, r, "换取令牌失败，详情见服务端日志")
		return
	}

	claims, err := s.oidcUserinfo(ctx, c, tok)
	if err != nil {
		logx.Warn("oidc", "userinfo_failed", map[string]any{"err": err.Error()})
		s.oidcFailRedirect(w, r, "读取用户信息失败，详情见服务端日志")
		return
	}

	username := firstString(claims[c.UsernameClaim], claims["preferred_username"], claims["sub"])
	if username == "" {
		logx.Warn("oidc", "no_username", map[string]any{
			"claim": c.UsernameClaim, "available": claimKeys(claims)})
		s.oidcFailRedirect(w, r, "身份信息里没有用户名，检查「用户名 claim」配置")
		return
	}
	groups := toStringSlice(claims[c.GroupsClaim])

	rules, err := s.St.ListRoleMappings(ctx)
	if err != nil {
		s.oidcFailRedirect(w, r, "读取角色映射失败")
		return
	}
	rr := make([]auth.RoleRule, 0, len(rules))
	for _, m := range rules {
		rr = append(rr, auth.RoleRule{GroupValue: m.GroupValue, RoleCode: m.RoleCode})
	}
	rres := auth.ResolveRole(groups, rr)
	role, hits := rres.Role, rres.Matched

	// 🔴 组映射配出了「单一角色表达不了的权限组合」。
	//    登录照常，但必须喊出来 —— 否则就是悄悄少给权限，
	//    表现是某个人某天突然点不动某个按钮，而谁也想不到是组映射的问题。
	if len(rres.Dropped) > 0 {
		dropped := make([]string, len(rres.Dropped))
		for i, p := range rres.Dropped {
			dropped[i] = string(p)
		}
		logx.Warn("oidc", "role_union_not_expressible", map[string]any{
			"user": username, "groups": groups,
			"candidates": rres.Candidates, "picked": role,
			"dropped": dropped,
			"hint": "这个人同时命中了多个角色，而没有任何一个角色覆盖它们的并集。" +
				"已按覆盖最多的那个授予，上面 dropped 里的权限没有给。" +
				"要么给他单独建一个包含这些权限的自定义角色，要么改组映射。",
		})
	}

	// 🔴 一条都没命中时的处置由配置定，且必须**记日志**：
	//    "他为什么是 viewer" 是这套东西上线后最高频的问题，
	//    没有这条日志只能去翻 IdP。
	if role == "" {
		if !c.AllowUnmapped {
			// 🔴 "没匹配到组"有**两种成因**，处理方式完全不同：
			//   a. IdP 压根没下发这个 claim（claim_keys 里找不到）
			//      → 去 IdP 侧检查：这个应用有没有配置下发该 claim、
			//        或者这个人有没有被分配应用角色
			//   b. IdP 下发了组，但系统里没有对应的映射
			//      → 去「单点登录 → 组→角色映射」加一条
			// 只说"没匹配到任何授权组"的话，管理员两边都得翻一遍。
			hasClaim := false
			for _, k := range claimKeys(claims) {
				if k == c.GroupsClaim {
					hasClaim = true
					break
				}
			}
			logx.Warn("oidc", "unmapped_rejected", map[string]any{
				"user": username, "groups": groups,
				"groups_claim": c.GroupsClaim, "claim_present": hasClaim,
				// ⚠️ 只记 key 不记 value：userinfo 里可能有手机号、工号
				"claim_keys": claimKeys(claims),
				"rules":      len(rr),
			})
			s.oidcFailRedirect(w, r, unmappedHint(username, c.GroupsClaim, hasClaim, groups, len(rr)))
			return
		}
		role = c.DefaultRole
	}
	logx.Info("oidc", "login", map[string]any{
		"user": username, "groups": groups, "matched": hits, "role": role,
		"candidates":   rres.Candidates,
		"groups_claim": c.GroupsClaim,
		// ⚠️ 带上 IdP 实际给了哪些 claim（只记 key）：
		//    "组匹配不上"最常见的成因是 claim 名字填错，而那时 groups 是空数组，
		//    光看 groups:[] 分不清是"IdP 没下发"还是"我们取错了字段"
		"claim_keys": claimKeys(claims)})

	u, err := s.St.UpsertOIDCUser(ctx, store.OIDCUser{
		Username:    username,
		DisplayName: firstString(claims["name"], claims["display_name"], username),
		Email:       firstString(claims["email"]),
		Subject:     firstString(claims["sub"]),
		RoleCode:    role,
		Groups:      strings.Join(groups, ","),
	})
	if err != nil {
		logx.Warn("oidc", "upsert_failed", map[string]any{"user": username, "err": err.Error()})
		s.oidcFailRedirect(w, r, "创建或更新账号失败，详情见服务端日志")
		return
	}

	// 🔴 组映射算出来的角色被锁定挡下了 —— 必须记。
	//    不记的话，「组里明明配了 admin，他登录后还是 viewer」查不出原因，
	//    而这正是锁定功能本身造成的、且完全符合预期的行为。
	if u.RoleLocked() && u.RoleCode != role {
		logx.Info("oidc", "role_kept_by_lock", map[string]any{
			"user": username, "from_groups": role, "kept": u.RoleCode,
			"locked_until": u.RoleLockedUntil.Time,
			"reason":       u.RoleLockReason,
			"hint":         "这个人的角色被锁定，本次登录没有按组映射覆盖。到期后会自动恢复成跟着组走。",
		})
	}

	if !s.issueSession(w, r, u) {
		return
	}
	s.St.Audit(ctx, username, "auth.login.sso", "", map[string]any{
		"groups": groups, "role": role}, nil, clientIP(r))

	// 🔴 回调最后一步：302 回首页。
	//    这条日志是"登录成功"与"随后 /api/me 401"之间唯一的连接点 ——
	//    没有它就分不清是回调没走完，还是走完了但 cookie 没带回来。
	logx.Info("oidc", "callback_done", map[string]any{
		"user": username, "role": role,
		"redirect": "/", "xfp": r.Header.Get("X-Forwarded-Proto"), "host": r.Host})
	http.Redirect(w, r, "/", http.StatusFound)
}

// oidcExchange 用授权码换 access_token。
func (s *Server) oidcExchange(ctx context.Context, c store.OIDCConfig, secret, code, redirect string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", secret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := oidcClient(c.InsecureTLS).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("token 端点返回 HTTP %d：%s", resp.StatusCode, providers.SafeBody(raw, 400))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(raw, &out) != nil || out.AccessToken == "" {
		return "", fmt.Errorf("token 响应里没有 access_token：%s", providers.SafeBody(raw, 300))
	}
	return out.AccessToken, nil
}

// oidcUserinfo 取用户信息。
//
// 🔴 **必须打 userinfo 端点，不能只解 id_token。**
//
// 同事那套 MXID 的 claim 是**分两处下发**的：
//   - id_token 里只有 sub 等基本字段
//   - name / email / 组信息**只在 userinfo**
//
// 只读 id_token 的接入方（Atlassian 那类）必然报 "Claim not found"，
// 而 IdP 侧看什么都正常 —— 这个坑已经让 Jira/Confluence 接入卡过。
func (s *Server) oidcUserinfo(ctx context.Context, c store.OIDCConfig, token string) (map[string]any, error) {
	if strings.TrimSpace(c.UserinfoURL) == "" {
		return nil, fmt.Errorf("没有配置 userinfo 地址 —— " +
			"组信息通常只在 userinfo 里下发，缺了它拿不到角色")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.UserinfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := oidcClient(c.InsecureTLS).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("userinfo 返回 HTTP %d：%s", resp.StatusCode, providers.SafeBody(raw, 400))
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, fmt.Errorf("userinfo 不是合法 JSON：%s", providers.SafeBody(raw, 200))
	}
	return claims, nil
}

func oidcClient(insecure bool) *http.Client {
	c := &http.Client{Timeout: 20 * time.Second}
	if insecure {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return c
}

// oidcFailRedirect 失败时回登录页并带一句人话。
//
// ⚠️ 不能只返回 JSON：这是**浏览器跳转**过来的，返回 JSON 会让用户
// 盯着一页原始文本，不知道该点哪里回去。
func (s *Server) oidcFailRedirect(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?sso_error="+url.QueryEscape(msg), http.StatusFound)
}

// ─────────────────── claim 取值 ───────────────────

func firstString(vs ...any) string {
	for _, v := range vs {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// toStringSlice 把组 claim 转成字符串切片。
//
// ⚠️ 各家 IdP 下发的形态不同，必须都认：
//   - []any{"a","b"}  最常见
//   - "a,b" / "a b"   有的 IdP 下发逗号或空格分隔的**单个字符串**
//   - "a"             只有一个组时退化成裸字符串
//
// 只认第一种的话，后两种会静默变成"没有任何组"，然后所有人掉进默认角色 ——
// 而日志里看到的是"groups: []"，像 IdP 没下发，其实是我们没解析。
func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case []string:
		return t
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil
		}
		sep := ","
		if !strings.Contains(s, ",") && strings.Contains(s, " ") {
			sep = " "
		}
		var out []string
		for _, p := range strings.Split(s, sep) {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// claimKeys 只用于排障日志：列出 IdP 到底给了哪些字段。
// ⚠️ 只记 key 不记 value —— userinfo 里可能有手机号、工号之类。
func claimKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// unmappedHint 「没匹配到授权组」的可执行说明。
//
// 这句话是给**管理员**看的（用户会把它截图发过去），
// 所以必须直接指出该去哪一侧改，而不是笼统说"联系管理员"。
func unmappedHint(user, claim string, claimPresent bool, groups []string, ruleCount int) string {
	switch {
	case !claimPresent:
		return "账号 " + user + " 登录成功，但身份提供方没有下发 “" + claim + "” 这个字段 —— " +
			"请在 IdP 侧确认：该应用是否配置了下发此 claim，以及这个人是否被分配了应用角色。" +
			"（当前系统按 “" + claim + "” 判定权限，可在「单点登录」页更改。）"
	case len(groups) == 0:
		return "账号 " + user + " 登录成功，身份提供方下发了 “" + claim + "” 但内容为空 —— " +
			"通常是这个人还没有被分配任何应用角色，请在 IdP 侧给他分配。"
	case ruleCount == 0:
		return "账号 " + user + " 带着组 " + strings.Join(groups, "、") +
			" 登录，但系统里还没有配置任何「组→角色映射」—— 请先在「单点登录」页加一条。"
	default:
		return "账号 " + user + " 的组 " + strings.Join(groups, "、") +
			" 没有匹配到任何映射规则 —— 请在「单点登录 → 组→角色映射」里为它加一条，" +
			"或确认已有规则的组名是否写对（大小写不敏感，支持 * 通配）。"
	}
}
