// Package api 是 HTTP 层：路由、认证、权限、响应封装。
//
// 业务判定一律不在这里 —— handler 只做「取参 → 调 store/compare → 写响应」。
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"ops-version-backend/config"
	"ops-version-backend/crypto"
	"ops-version-backend/internal/auth"
	"ops-version-backend/internal/collector"
	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
)

const sessionCookie = "opsv_session"
const sessionTTL = 12 * time.Hour

type Server struct {
	St   *store.Store
	Cfg  *config.Config
	Ciph *crypto.Cipher
	Coll *collector.Collector

	// states OIDC 的 state（防 CSRF）。见 oidc.go 里关于"多副本会失败"的说明。
	states *stateStore
}

type ctxKey string

const ctxUser ctxKey = "user"

// ---------- 响应封装 ----------

// ok 写成功响应。统一包一层 {data:...}，前端不用为每个接口猜结构。
func ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// fail 写失败响应。
//
// 🔴 code 必须是可判别的字符串而不是只有一句人话 ——
// 前端要能区分「认证失败」和「权限不足」来决定是跳登录还是提示，
// 靠 message 文案做判断迟早因为改文案而失效。
func fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

func body[T any](r *http.Request) (T, error) {
	var v T
	err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&v)
	return v, err
}

func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	return h
}

func userOf(r *http.Request) store.User {
	u, _ := r.Context().Value(ctxUser).(store.User)
	return u
}

// ---------- 中间件 ----------

// authed 要求已登录。
func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			// 🔴 401 必须能分辨**没带 cookie**和**会话失效**，两者的排查方向完全不同：
			//    前者是 cookie 根本没发过来（Secure/SameSite/域名/协议不匹配），
			//    后者是会话过期或被删。
			//    只写一句"请先登录"的话，"登录成功却立刻 401"这种问题
			//    在日志里看不出任何线索 —— 实测因此卡过一轮。
			// ⚠️ 用 Info 级：未登录访问是**预期路径**（登录页自己就会拉一次 /api/me），
			//    打成 Warn 会把正常流量刷成一片警告，真问题反而被淹掉。
			logx.Info("auth", "no_cookie", map[string]any{
				"path": r.URL.Path,
				// 带上这三个才能判断 cookie 为什么没发过来
				"xfp":     r.Header.Get("X-Forwarded-Proto"),
				"host":    r.Host,
				"has_any": r.Header.Get("Cookie") != "", // 有别的 cookie 但没有我们这个
			})
			fail(w, http.StatusUnauthorized, "unauthenticated", "请先登录")
			return
		}
		u, err := s.St.UserBySession(r.Context(), c.Value)
		if err != nil {
			// cookie 带过来了但会话查不到 —— 过期、被删、或者会话是**别的副本**建的
			logx.Info("auth", "session_invalid", map[string]any{
				"path": r.URL.Path, "sid_prefix": safePrefix(c.Value), "err": err.Error()})
			fail(w, http.StatusUnauthorized, "unauthenticated", "会话已失效，请重新登录")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxUser, u)))
	}
}

// currentUser 取当前会话的用户。
//
// 与 authed 的分工：authed 是**中间件**（不通过就 401 并终止），
// 这个是**查询**（不通过就返回错误，由调用方决定怎么办）。
// /api/me 需要后者 —— 未登录对它来说是正常回答，不是拒绝。
func (s *Server) currentUser(r *http.Request) (store.User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return store.User{}, errNoSession
	}
	return s.St.UserBySession(r.Context(), c.Value)
}

var errNoSession = errors.New("no session cookie")

// requires 要求某项权限。
//
// 🔴 **接口级校验，不能只靠前端隐藏按钮** ——
// 按钮藏了接口还开着，直接调就绕过去了。前端的隐藏只是体验，这里才是防线。
func (s *Server) requires(p auth.Perm, next http.HandlerFunc) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request) {
		u := userOf(r)
		if !auth.Can(u.RoleCode, p) {
			logx.Warn("rbac", "denied", map[string]any{
				"user": u.Username, "role": u.RoleCode, "perm": string(p), "path": r.URL.Path})
			fail(w, http.StatusForbidden, "forbidden", "当前角色没有此操作的权限")
			return
		}
		next(w, r)
	})
}

// ---------- 路由 ----------

func (s *Server) Routes() http.Handler {
	// ⚠️ 在这里初始化而不是让 main 传进来：state 存储是 OIDC 的内部细节，
	//    暴露给 main 只会多一个"忘了初始化就 panic"的机会。
	if s.states == nil {
		s.states = newStateStore()
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("POST /api/auth/login", s.login)

	// ─── 单点登录 ───
	// ⚠️ 这三个**不能要求登录**：登录页要用 status，另外两个是浏览器跳转过来的。
	//    但 status 只回"开没开"和按钮文字，绝不回配置细节。
	mux.HandleFunc("GET /api/auth/oidc/status", s.oidcStatus)

	// 品牌配置：**读不要求登录**（登录页与 favicon 都要用，那时人还没登录），
	// 写要最高权限（改的是所有人看到的东西）
	// ─── 数据源（多平台共用的连接信息）与项目 ───
	// 写操作要 org.write：改数据源等于改一批平台的连接方式
	mux.HandleFunc("GET /api/datasources", s.requires(auth.PermView, s.listDatasources))
	mux.HandleFunc("POST /api/datasources", s.requires(auth.PermOrgWrite, s.saveDatasource))
	mux.HandleFunc("PUT /api/datasources/{id}", s.requires(auth.PermOrgWrite, s.saveDatasource))
	mux.HandleFunc("DELETE /api/datasources/{id}", s.requires(auth.PermOrgWrite, s.deleteDatasource))
	mux.HandleFunc("GET /api/projects", s.requires(auth.PermView, s.listProjects))
	mux.HandleFunc("POST /api/projects", s.requires(auth.PermOrgWrite, s.saveProject))
	mux.HandleFunc("PUT /api/projects/{id}", s.requires(auth.PermOrgWrite, s.saveProject))
	mux.HandleFunc("DELETE /api/projects/{id}", s.requires(auth.PermOrgWrite, s.deleteProject))

	mux.HandleFunc("GET /api/branding", s.getBranding)
	mux.HandleFunc("PUT /api/branding", s.requires(auth.PermUserAdmin, s.saveBranding))
	mux.HandleFunc("GET /api/auth/oidc/login", s.oidcLogin)
	mux.HandleFunc("GET /api/auth/oidc/callback", s.oidcCallback)

	// 配置与映射管理：改这些等于改"谁能进来、进来是什么角色"，
	// 所以要最高权限（PermUserAdmin），与用户管理同级。
	mux.HandleFunc("GET /api/oidc/config", s.requires(auth.PermUserAdmin, s.getOIDCConfig))
	mux.HandleFunc("PUT /api/oidc/config", s.requires(auth.PermUserAdmin, s.saveOIDCConfig))
	mux.HandleFunc("GET /api/oidc/mappings", s.requires(auth.PermUserAdmin, s.listRoleMappings))
	mux.HandleFunc("POST /api/oidc/mappings", s.requires(auth.PermUserAdmin, s.saveRoleMapping))
	mux.HandleFunc("PUT /api/oidc/mappings/{id}", s.requires(auth.PermUserAdmin, s.saveRoleMapping))
	mux.HandleFunc("DELETE /api/oidc/mappings/{id}", s.requires(auth.PermUserAdmin, s.deleteRoleMapping))
	// 🔴 退出**不要求认证**。
	//
	// 挂 authed 的话，会话已失效时直接 401 —— handler 进不去，
	// 于是既不清 cookie、也不留任何记录。而那恰恰是最需要记录的一种：
	// 用户点了退出、界面没反应，
	// 事后想查"他到底点没点"，日志里一片空白。
	//
	// 安全上也没损失：这个接口只删调用方自己带来的那个会话
	// （sid 从 cookie 取，不接受任何参数），带不出别人的会话。
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	// 🔴 /api/me **不套 authed**：它是「我是谁」的探测，不是受保护资源。
	//
	//    套了的话未登录会 401 —— 而那是**预期路径**（每次打开登录页都会走），
	//    浏览器却会在 console 里留一条红色 error。真出故障时这条噪音会干扰排查。
	//    改成 200 + authenticated:false，噪音消失，三态也从"猜状态码"
	//    变成显式的字段。
	mux.HandleFunc("GET /api/me", s.me)

	mux.HandleFunc("GET /api/orgs", s.requires(auth.PermView, s.listOrgs))
	mux.HandleFunc("POST /api/orgs", s.requires(auth.PermOrgWrite, s.createOrg))
	mux.HandleFunc("PUT /api/orgs/{id}", s.requires(auth.PermOrgWrite, s.updateOrg))
	mux.HandleFunc("DELETE /api/orgs/{id}", s.requires(auth.PermOrgWrite, s.deleteOrg))
	mux.HandleFunc("POST /api/orgs/{id}/probe", s.requires(auth.PermOrgWrite, s.probeOrg))
	mux.HandleFunc("GET /api/orgs/{id}/clusters", s.requires(auth.PermOrgWrite, s.orgClusters))
	// 规则预检：保存前拿真实服务名跑一遍，回答「这条规则现在命中几个」
	mux.HandleFunc("POST /api/orgs/{id}/rules/preview", s.requires(auth.PermOrgWrite, s.previewRules))
	mux.HandleFunc("POST /api/orgs/{id}/collect", s.requires(auth.PermRefresh, s.collectOrg))

	mux.HandleFunc("POST /api/compare", s.requires(auth.PermView, s.compareHandler))
	// 🔴 导出单独一个权限：它产出的文件会离开系统，
	//    「能看」和「能把整张表带走」不是一回事
	mux.HandleFunc("POST /api/export", s.requires(auth.PermExport, s.exportHandler))
	// 单列版本清单：与对账表是**两件事**，所以是两个入口（见 exportInventory 的说明）
	mux.HandleFunc("POST /api/export/inventory", s.requires(auth.PermExport, s.exportInventory))
	mux.HandleFunc("GET /api/changes", s.requires(auth.PermView, s.listChanges))
	mux.HandleFunc("GET /api/audit", s.requires(auth.PermAudit, s.listAudit))

	// 用户与角色。只有超管能管
	mux.HandleFunc("GET /api/users", s.requires(auth.PermUserAdmin, s.listUsers))
	mux.HandleFunc("POST /api/users", s.requires(auth.PermUserAdmin, s.saveUser))
	mux.HandleFunc("PUT /api/users/{id}", s.requires(auth.PermUserAdmin, s.saveUser))
	mux.HandleFunc("DELETE /api/users/{id}", s.requires(auth.PermUserAdmin, s.deleteUser))
	mux.HandleFunc("GET /api/roles", s.requires(auth.PermView, s.listRoles))

	// 角色管理。⚠️ 与上面那条分开：这些含"多少人在用"、能改能删，要 user.admin
	mux.HandleFunc("GET /api/admin/roles", s.requires(auth.PermUserAdmin, s.listRolesFull))
	mux.HandleFunc("GET /api/admin/perms", s.requires(auth.PermUserAdmin, s.listPerms))
	mux.HandleFunc("POST /api/admin/roles", s.requires(auth.PermUserAdmin, s.saveRole))
	mux.HandleFunc("PUT /api/admin/roles/{id}", s.requires(auth.PermUserAdmin, s.saveRole))
	mux.HandleFunc("DELETE /api/admin/roles/{id}", s.requires(auth.PermUserAdmin, s.deleteRole))
	// 角色锁定：锁着的用户 SSO 登录不按组覆盖角色
	mux.HandleFunc("PUT /api/users/{id}/role-lock", s.requires(auth.PermUserAdmin, s.setRoleLock))
	// 快到期/已过期的角色锁。⚠️ 路径排在 {id} 那条之前无所谓 ——
	// Go 1.22 的 ServeMux 按**具体度**匹配，静态段优先于通配段。
	// 某平台采集到的服务清单。项目的「指定服务」靠它做勾选 ——
	// 手打服务名拼错不报错，只是那个服务永远不出现在这个项目下。
	mux.HandleFunc("GET /api/orgs/{id}/services", s.requires(auth.PermView, s.listOrgServices))
	mux.HandleFunc("GET /api/users/role-locks/expiring",
		s.requires(auth.PermUserAdmin, s.listExpiringLocks))

	// 对比方案
	mux.HandleFunc("GET /api/plans", s.requires(auth.PermView, s.listPlans))
	mux.HandleFunc("POST /api/plans", s.requires(auth.PermPlanWrite, s.savePlan))
	mux.HandleFunc("PUT /api/plans/{id}", s.requires(auth.PermPlanWrite, s.savePlan))
	mux.HandleFunc("DELETE /api/plans/{id}", s.requires(auth.PermPlanWrite, s.deletePlan))

	// MCP 令牌管理。只有超管能发令牌 —— 令牌等于一份长期有效的读权限
	mux.HandleFunc("GET /api/mcp-tokens", s.requires(auth.PermUserAdmin, s.listMCPTokens))
	mux.HandleFunc("POST /api/mcp-tokens", s.requires(auth.PermUserAdmin, s.createMCPToken))
	mux.HandleFunc("PUT /api/mcp-tokens/{id}/expiry", s.requires(auth.PermUserAdmin, s.setMCPTokenExpiry))
	mux.HandleFunc("DELETE /api/mcp-tokens/{id}", s.requires(auth.PermUserAdmin, s.revokeMCPToken))

	// Harbor 镜像同步。这一层的产出是**归因**：
	// 「对方版本落后」到底是镜像没推过去，还是推过去了对方没发版
	mux.HandleFunc("GET /api/harbors", s.requires(auth.PermView, s.listHarbors))
	mux.HandleFunc("POST /api/harbors", s.requires(auth.PermOrgWrite, s.saveHarbor))
	mux.HandleFunc("PUT /api/harbors/{id}", s.requires(auth.PermOrgWrite, s.saveHarbor))
	mux.HandleFunc("DELETE /api/harbors/{id}", s.requires(auth.PermOrgWrite, s.deleteHarbor))
	mux.HandleFunc("POST /api/harbors/{id}/probe", s.requires(auth.PermOrgWrite, s.probeHarbor))
	mux.HandleFunc("GET /api/sync/policies", s.requires(auth.PermView, s.listPolicies))
	mux.HandleFunc("PUT /api/sync/policies/{id}/org", s.requires(auth.PermOrgWrite, s.bindPolicy))
	mux.HandleFunc("GET /api/sync/executions", s.requires(auth.PermView, s.listExecutions))
	mux.HandleFunc("GET /api/columns/freshness", s.requires(auth.PermView, s.columnFreshness))
	mux.HandleFunc("POST /api/sync/refresh", s.requires(auth.PermSyncTrigger, s.syncHarborsNow))
	// Harbor webhook 接收端。
	// 🔴 **不挂 requires**：Harbor 不会带 cookie，它只会带我们让它带的 Auth Header，
	//    认证在 handler 里自己做（常量时间比较令牌哈希）。
	mux.HandleFunc("POST /api/webhooks/harbor", s.harborHook)
	mux.HandleFunc("GET /api/webhooks/tokens", s.requires(auth.PermOrgWrite, s.webhookInfo))
	mux.HandleFunc("POST /api/webhooks/tokens", s.requires(auth.PermOrgWrite, s.createWebhookToken))
	mux.HandleFunc("DELETE /api/webhooks/tokens/{id}", s.requires(auth.PermOrgWrite, s.deleteWebhookToken))

	// 通知渠道。webhook 等同于凭据，写操作要 alert.write
	mux.HandleFunc("GET /api/notify/channels", s.requires(auth.PermView, s.listChannels))
	mux.HandleFunc("POST /api/notify/channels", s.requires(auth.PermAlertWrite, s.saveChannel))
	mux.HandleFunc("PUT /api/notify/channels/{id}", s.requires(auth.PermAlertWrite, s.saveChannel))
	mux.HandleFunc("DELETE /api/notify/channels/{id}", s.requires(auth.PermAlertWrite, s.deleteChannel))
	mux.HandleFunc("POST /api/notify/channels/{id}/test", s.requires(auth.PermAlertWrite, s.testChannel))
	mux.HandleFunc("GET /api/notify/records", s.requires(auth.PermView, s.listNotifyRecords))

	// 镜像查询：从我方 Harbor 拿服务清单与版本，逐个判定推没推过去
	// 🔴 /api/harbors/{id}/projects 与 /repositories 已删。
	//
	//    它们是「按服务查同步」旧流程里「先选项目、再选服务」那两步用的。
	//    现在项目由后端从我方快照的 image_repo 推出来（ProjectOfService），
	//    服务清单来自复制记录（ServicesOfPolicy）—— 前端不再需要它们。
	//
	// ⚠️ providers.Harbor 的 Projects()/Repositories() **保留**：能力还在，
	//    只是没有对外路由了。
	//
	// 🔴 /api/images/check（「和我方 Harbor 逐版本比对」）也已删。
	//
	//    它做的是：拉我方 Harbor 里该服务最近 N 个 tag，逐个去复制记录里
	//    查有没有对应的成功任务。名字叫「比对」但比的不是两边 Harbor ——
	//    我们没有对方的账号，比不了。用户的原话是
	//    「同步就同步了，怎么还和我方 Harbor 对比呢，本来就是从我们的 Harbor
	//    同步过去的」「这个对比，也没看到怎么对比的」。
	//
	//    真正要回答的问题是「同步过去的是哪个版本」，那由 sync_tasks 直接回答，
	//    不需要再打一次 Harbor。
	// 一条复制规则推过哪些服务 —— 「按服务查同步」用它代替「先选项目」
	mux.HandleFunc("GET /api/sync/policies/{id}/services", s.requires(auth.PermView, s.policyServices))
	// 某几个服务的推送记录（来自我们自己的库，不打 Harbor）
	mux.HandleFunc("GET /api/sync/tasks", s.requires(auth.PermView, s.syncTasks))

	// MCP：给 AI 用的只读工具集。走独立令牌，不复用浏览器会话
	mux.HandleFunc("POST /api/mcp", s.mcpHandler)

	return withLog(mux)
}

func withLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		// 只记非 2xx 和慢请求，避免日志被健康检查刷屏
		if sw.code >= 400 || time.Since(start) > 2*time.Second || logx.Enabled(logx.LevelDebug) {
			lv := logx.Info
			if sw.code >= 500 {
				lv = logx.Error
			} else if sw.code < 400 {
				lv = logx.Debug
			}
			lv("http", "request", map[string]any{
				"method": r.Method, "path": r.URL.Path,
				"status": sw.code, "ms": time.Since(start).Milliseconds()})
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

// ---------- 认证 ----------

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	req, err := body[struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}](r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}

	u, hash, err := s.St.UserByName(r.Context(), req.Username)
	// 🔴 用户不存在和密码错返回同一句话：区分开会变成账号枚举器，
	//    让人能试出哪些用户名是存在的。
	if err != nil || !u.Enabled || hash == "" || !s.St.CheckPassword(hash, req.Password) {
		s.St.Audit(r.Context(), req.Username, "auth.login", "", nil,
			errors.New("用户名或密码错误"), clientIP(r))
		fail(w, http.StatusUnauthorized, "bad_credentials", "用户名或密码错误")
		return
	}

	if !s.issueSession(w, r, u) {
		return
	}
	s.St.Audit(r.Context(), u.Username, "auth.login", "", nil, nil, clientIP(r))
	ok(w, mePayload(u))
}

// issueSession 建会话并下发 cookie。本地登录与 SSO **共用同一套** ——
// 两处各写一遍，改 TTL 或 SameSite 时必然漏掉一处。
// 返回 false 表示已经写过错误响应，调用方直接 return。
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, u store.User) bool {
	sid, err := s.St.CreateSession(r.Context(), u.ID, sessionTTL)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", "创建会话失败")
		return false
	}
	ck := sessionCookieOf(r, sid, int(sessionTTL.Seconds()))
	http.SetCookie(w, ck)
	// 会话下发成功。⚠️ 记下 secure 与 xfp：
	// "登录成功但下一个请求就 401" 最常见的成因就是 cookie 属性与访问协议对不上
	// （https 回调下发了 Secure cookie，而用户随后用 http 访问 → 浏览器不发送）。
	// 没有这条日志的话，只能看到一个孤零零的 401。
	logx.Info("auth", "session_issued", map[string]any{
		"user": u.Username, "role": u.RoleCode,
		"secure": ck.Secure, "xfp": r.Header.Get("X-Forwarded-Proto"),
		"host": r.Host, "ttl_s": int(sessionTTL.Seconds()),
		"sid_prefix": safePrefix(sid),
	})
	return true
}

// safePrefix 取会话 id 的前 8 位，用于把日志里的多条记录串起来。
// ⚠️ **只取前缀**：完整 sid 等于登录凭据，进了日志就等于泄露 ——
// 而日志会被转发、归档、给排障的人看。
func safePrefix(sid string) string {
	if len(sid) <= 8 {
		return "********"
	}
	return sid[:8] + "…"
}

// sessionCookieOf 造会话 cookie。签发与清除**共用这一处**。
//
// 🔴 两处各写一遍的话，属性迟早分叉 —— 而 cookie 的删除是按
// (name, path, domain) 匹配的，属性对不上浏览器可能认不出是同一个，
// 表现为"点了退出但 cookie 还在"。
//
// Secure 按 X-Forwarded-Proto 判：生产入口是 https（Istio 终止 TLS），
// 但 Gateway 的 80 端口没开强制跳转，http 能直接进来 ——
// 没有 Secure 的 cookie 会在任何一次误走 http 的请求里被明文发出去。
// ⚠️ 这条依赖前端 nginx **正确透传** X-Forwarded-Proto。
//
//	透传坏掉时（比如写成 $scheme，而 Pod 自己永远是 http）会得到
//	"代码写了 Secure 但永远不加"的假象 —— 看着有，实际一直是关的。
func sessionCookieOf(r *http.Request, value string, maxAge int) *http.Cookie {
	secure := r.TLS != nil
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		secure = strings.EqualFold(strings.TrimSpace(strings.Split(v, ",")[0]), "https")
	}
	return &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: secure, MaxAge: maxAge,
	}
}

// logout 退出登录。
//
// 🔴 **必须留下日志和审计**。登录有 `oidc/login` + `session_issued` + 审计三处记录，
// 而退出原本一处都没有 —— 于是"这个人到底点没点退出"、"那串 401 是退出后的正常状态
// 还是登录坏了"，在日志里完全分不出来。
// 实测因此误判过一次：把用户主动退出后的 401 读成了"登录后 cookie 带不回来"，
// 差点去查一个不存在的 cookie 问题。
//
// ⚠️ 身份事件的日志要**成对**：只记登录不记退出，时间线就是断的。
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	// 谁退出的 —— 从会话反查，而不是信任前端传的任何东西
	username, sidPrefix := "", ""
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		sidPrefix = safePrefix(c.Value)
		if u, err := s.St.UserBySession(r.Context(), c.Value); err == nil {
			username = u.Username
		}
		_ = s.St.DeleteSession(r.Context(), c.Value)
	}
	// ⚠️ 清除时属性必须与签发时**一致**（尤其 Secure / Path），
	//    否则浏览器可能认不出是同一个 cookie，删不掉
	http.SetCookie(w, sessionCookieOf(r, "", -1))

	logx.Info("auth", "logout", map[string]any{
		"user": username, "sid_prefix": sidPrefix,
		// ⚠️ 会话已失效时也走到这里（前端拿 401 后仍会清本地状态）。
		//    这种情况 username 是空 —— 区分开才知道是"正常退出"还是"会话早没了"
		"had_session": username != "",
	})
	if username != "" {
		s.St.Audit(r.Context(), username, "auth.logout", "", nil, nil, clientIP(r))
	}
	ok(w, map[string]any{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u, err := s.currentUser(r)
	if err != nil {
		// ⚠️ 200 而不是 401：未登录是这个接口的**正常回答之一**。
		//    前端据 authenticated 判断，而不是据状态码 ——
		//    状态码那条路还会把「网络抖了一下」和「没登录」混在一起。
		ok(w, map[string]any{"authenticated": false})
		return
	}
	p := mePayload(u)
	p["authenticated"] = true
	ok(w, p)
}

// mePayload 前端渲染权限时**只认这里返回的 perms**。
//
// 🔴 禁止前端按 role_code 或 auth_source 自行推导权限 ——
// 前后端两套判据必然分叉，同一个人在两个页面看到的权限会不一致。这个坑栽过三次。
func mePayload(u store.User) map[string]any {
	perms := make([]string, 0, 8)
	for _, p := range auth.PermsOf(u.RoleCode) {
		perms = append(perms, string(p))
	}
	return map[string]any{
		"username":     u.Username,
		"display_name": u.DisplayName,
		"role":         u.RoleCode,
		"auth_source":  u.AuthSource,
		"perms":        perms,
		"scoped":       strings.TrimSpace(u.VisibleOrgs) != "",
	}
}
