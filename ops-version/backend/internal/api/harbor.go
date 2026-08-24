package api

import (
	"net/http"
	"ops-version-backend/logx"
	"strconv"
	"strings"
	"time"

	"ops-version-backend/internal/store"
	"ops-version-backend/notify"
	"ops-version-backend/providers"
)

type harborReq struct {
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Username    string `json:"username"`
	Password    string `json:"password"` // 空 = 不改
	InsecureTLS bool   `json:"insecure_tls"`
	Enabled     bool   `json:"enabled"`
	// PolicyFilter 只拉这几条复制规则（按规则名，支持 * 通配）。留空 = 全部
	PolicyFilter []string `json:"policy_filter"`
}

func (s *Server) listHarbors(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListHarbors(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 🔴 CredentialEnc 有 json:"-"，不会外泄；这里只是再确认一遍：
	//    凭据永不回显，界面只需要知道「配没配」
	ok(w, list)
}

func (s *Server) saveHarbor(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	req, err := body[harborReq](r)
	if err != nil || req.Name == "" || req.Endpoint == "" {
		fail(w, http.StatusBadRequest, "bad_request", "名称与地址必填")
		return
	}
	enc := ""
	if req.Password != "" {
		enc, err = s.Ciph.Encrypt(req.Password)
		if err != nil {
			fail(w, http.StatusInternalServerError, "internal", "凭据加密失败")
			return
		}
	}
	newID, err := s.St.SaveHarbor(r.Context(), id, store.HarborInput{
		Name: req.Name, Endpoint: req.Endpoint, Username: req.Username,
		CredentialEnc: enc, InsecureTLS: req.InsecureTLS, Enabled: req.Enabled,
		PolicyFilter: req.PolicyFilter,
	})
	action := "harbor.create"
	if id > 0 {
		action = "harbor.update"
	}
	s.St.Audit(r.Context(), userOf(r).Username, action, req.Name, nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ok(w, map[string]any{"id": newID})
}

func (s *Server) deleteHarbor(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := s.St.DeleteHarbor(r.Context(), id)
	s.St.Audit(r.Context(), userOf(r).Username, "harbor.delete",
		strconv.FormatInt(id, 10), nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

// probeHarbor 测连通。
//
// 🔴 失败必须分类：密码错、网络不通、权限不足三种的处理方式完全不同，
// 混成一句「连接失败」等于没说 —— 人会挨个试而不知道该找谁。
func (s *Server) probeHarbor(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	list, err := s.St.ListHarbors(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	var h *store.Harbor
	for i := range list {
		if list[i].ID == id {
			h = &list[i]
		}
	}
	if h == nil {
		fail(w, http.StatusNotFound, "not_found", "Harbor 不存在")
		return
	}
	pw := ""
	if h.CredentialEnc != "" {
		if v, err := s.Ciph.Decrypt(h.CredentialEnc); err == nil {
			pw = v
		}
	}
	hb := &providers.Harbor{Endpoint: h.Endpoint, Username: h.Username,
		Password: pw, InsecureTLS: h.InsecureTLS}
	perr := hb.Probe(r.Context())
	s.St.Audit(r.Context(), userOf(r).Username, "harbor.probe", h.Name, nil, perr, clientIP(r))
	if perr != nil {
		// 同 probeOrg：业务失败返 200，访问日志记不到，必须自己打
		logx.Warn("harbor", "probe_failed", map[string]any{
			"harbor": h.Name, "endpoint": h.Endpoint,
			"kind": classifyKind(perr), "err": perr.Error()})
		ok(w, map[string]any{"ok": false, "kind": classifyKind(perr), "message": perr.Error()})
		return
	}
	// 连得上之后再报一次**能读到什么** —— Harbor 的权限是分级的，
	// 只说"连接正常"会让人以为所有功能都能用，
	// 直到几天后发现同步状态一直是空的才回头查。
	caps := hb.Capabilities(r.Context())
	ok(w, map[string]any{"ok": true, "message": "连接正常 —— " + caps.Detail, "caps": caps})
}

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListPolicies(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}

// bindPolicy 把复制规则绑到组织 —— 绑了才能做归因
func (s *Server) bindPolicy(w http.ResponseWriter, r *http.Request) {
	ref, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	req, err := body[struct {
		OrgID int64 `json:"org_id"`
	}](r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	e := s.St.BindPolicyOrg(r.Context(), ref, req.OrgID)
	s.St.Audit(r.Context(), userOf(r).Username, "harbor.bind_policy",
		strconv.FormatInt(ref, 10), map[string]any{"org_id": req.OrgID}, e, clientIP(r))
	if e != nil {
		fail(w, http.StatusInternalServerError, "internal", e.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

func (s *Server) listExecutions(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	// 复合游标：排序键是 (started_at, id)，游标就得是这两个 ——
	// 只传 id 的话切出来的不是「排在这一行之后」那一批（见 store.ListExecutions）
	beforeID, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	var beforeAt *time.Time
	if v := r.URL.Query().Get("before_at"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			beforeAt = &t
		}
	}
	page, err := s.St.ListExecutions(r.Context(), limit, beforeAt, beforeID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, page)
}

// syncHarborsNow 手动触发一次同步拉取
func (s *Server) syncHarborsNow(w http.ResponseWriter, r *http.Request) {
	s.Coll.SyncHarbors(r.Context(), true)
	s.St.Audit(r.Context(), userOf(r).Username, "harbor.sync", "", nil, nil, clientIP(r))
	ok(w, map[string]any{"ok": true})
}

// ─────────────── 通知渠道 ───────────────

type channelReq struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Webhook string `json:"webhook"` // 空 = 不改
	Enabled bool   `json:"enabled"`
	OrgID   int64  `json:"org_id"`
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListChannels(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}

func (s *Server) saveChannel(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	req, err := body[channelReq](r)
	if err != nil || req.Name == "" {
		fail(w, http.StatusBadRequest, "bad_request", "名称必填")
		return
	}
	enc := ""
	if req.Webhook != "" {
		// 🔴 webhook 里带 token，等同于凭据 —— 加密存储，接口永不回显
		enc, err = s.Ciph.Encrypt(req.Webhook)
		if err != nil {
			fail(w, http.StatusInternalServerError, "internal", "加密失败")
			return
		}
	}
	kind := req.Kind
	if kind == "" {
		kind = "lark"
	}
	newID, err := s.St.SaveChannel(r.Context(), id, store.ChannelInput{
		Name: req.Name, Kind: kind, WebhookEnc: enc, Enabled: req.Enabled, OrgID: req.OrgID,
	})
	action := "notify.channel_create"
	if id > 0 {
		action = "notify.channel_update"
	}
	s.St.Audit(r.Context(), userOf(r).Username, action, req.Name, nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ok(w, map[string]any{"id": newID})
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := s.St.DeleteChannel(r.Context(), id)
	s.St.Audit(r.Context(), userOf(r).Username, "notify.channel_delete",
		strconv.FormatInt(id, 10), nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

// testChannel 发一条测试消息。
//
// 🔴 必须真发一条，不能只校验 URL 格式。
// webhook 被重置、机器人被移出群这类失败，飞书返回的是
// **HTTP 200 + code≠0** —— 只看格式或只看状态码都会误判成"能用"。
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	list, err := s.St.ListChannels(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	var ch *store.NotifyChannel
	for i := range list {
		if list[i].ID == id {
			ch = &list[i]
		}
	}
	if ch == nil || ch.WebhookEnc == "" {
		fail(w, http.StatusBadRequest, "bad_request", "渠道不存在或未配置 webhook")
		return
	}
	hook, err := s.Ciph.Decrypt(ch.WebhookEnc)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", "webhook 解密失败")
		return
	}
	sendErr := notify.SendFeishu(hook, "【OpsVersion】测试消息：通知渠道「"+ch.Name+"」配置正常")
	s.St.Audit(r.Context(), userOf(r).Username, "notify.test", ch.Name, nil, sendErr, clientIP(r))
	if sendErr != nil {
		// 同上：通知测试失败也是 200，日志里同样会隐身
		logx.Warn("notify", "test_failed", map[string]any{
			"channel": ch.Name, "err": sendErr.Error()})
		ok(w, map[string]any{"ok": false, "message": sendErr.Error()})
		return
	}
	ok(w, map[string]any{"ok": true, "message": "已发送，去群里确认收到"})
}

func (s *Server) listNotifyRecords(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.St.ListNotifyRecords(r.Context(), limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}

// policyServices 一条复制规则推过哪些服务。
//
// 🔴 「按服务查同步」靠它去掉「先选项目」那一步 —— 项目和「这条规则推什么」
// 不是一回事，让人先猜一个项目，猜错了下拉里就没有他要的服务。
func (s *Server) policyServices(w http.ResponseWriter, r *http.Request) {
	ref, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	list, err := s.St.ServicesOfPolicy(r.Context(), ref)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}

// syncTasks 查某几个服务的推送记录。
//
// 🔴 数据全部来自我们自己的库，不打 Harbor、不需要项目 ——
// 「按服务查同步」靠它把流程从「Harbor → 项目 → 范围 → 服务」
// 缩成「规则 → 服务」。
func (s *Server) syncTasks(w http.ResponseWriter, r *http.Request) {
	policy, _ := strconv.ParseInt(r.URL.Query().Get("policy"), 10, 64)
	var services []string
	for _, v := range strings.Split(r.URL.Query().Get("services"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			services = append(services, v)
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.St.SyncTasksOf(r.Context(), policy, services, limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}
