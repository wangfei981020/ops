package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ops-version-backend/internal/collector"
	"ops-version-backend/internal/compare"
	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
	"ops-version-backend/providers"
)

// ---------- 组织 ----------

// orgDTO 对外的组织形态。
//
// 🔴 这里**没有** credential 字段，任何角色任何接口都拿不到凭据明文。
// 前端编辑时凭据框永远是空的，留空提交 = 不改动（见 store.SaveOrg）。
type orgDTO struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	ProviderType string `json:"provider_type"`
	AuthType     string `json:"auth_type"`
	Endpoint     string `json:"endpoint"`
	// ─── 引用的数据源 ───
	//
	// 🔴 引用之后，地址和凭据都由数据源提供，平台这边**不该再填一遍** ——
	//    填两遍就有两份真相，改密码时改一处漏一处，
	//    表现是某个平台悄悄采集失败而报错写着「认证失败」。
	// ⚠️ 后三个只出不进：它们是数据源的属性，要改得去数据源页面改。
	DatasourceID   int64  `json:"datasource_id"`
	DatasourceName string `json:"datasource_name"`
	DSProviderType string `json:"ds_provider_type"`
	DSEndpoint     string `json:"ds_endpoint"`
	HarborHost     string `json:"harbor_host"`
	HarborProject  string `json:"harbor_project"`
	IsSelf         bool   `json:"is_self"`
	// Enabled 停用的平台仍出现在列表里（要能编辑/重新启用），
	// 但**不参与比对列** —— 前端据此过滤，见 columnsOf
	Enabled       bool       `json:"enabled"`
	HasCredential bool       `json:"has_credential"` // 只说有没有，不说是什么
	SyncStatus    string     `json:"sync_status"`
	SyncAt        *time.Time `json:"sync_at"`
	SyncError     string     `json:"sync_error"`
	Envs          []envDTO   `json:"envs"`
	// Projects 该平台下的项目。**随平台一起返回**，不让前端为每个平台再发一次请求 ——
	// 比对页要拿它给列打标签，N 个平台就是 N 次请求，页面会一列一列地闪出来。
	Projects []projDTO `json:"projects"`
}

type projDTO struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type envDTO struct {
	Env             string   `json:"env"`
	ClusterRefs     []string `json:"cluster_refs"`
	NSInclude       []string `json:"ns_include"`
	NSExclude       []string `json:"ns_exclude"`
	WorkloadInclude []string `json:"workload_include"`
	WorkloadExclude []string `json:"workload_exclude"`
	CompareEnabled  bool     `json:"compare_enabled"`
	// ProjectID 所属项目。对比表的一列 = 项目 × 环境。0 = 归入该平台的默认项目。
	ProjectID int64 `json:"project_id"`
	// 环境引用的数据源。0 = 没引用，走下一层（见 store.OrgEnv.Conn）
	DatasourceID   int64  `json:"datasource_id"`
	DatasourceName string `json:"datasource_name"`
	Endpoint       string `json:"endpoint"`  // 空 = 继承平台级
	AuthType       string `json:"auth_type"` // 空 = 继承平台级
	HasCredential bool   `json:"has_credential"`

	// ─── 只读：这一列上次采集的结果 ───
	//
	// ⚠️ 只出不进（envReq 里**没有**这三个）：它们是采集器写的事实，
	// 不是用户填的配置。放进请求体等于允许前端伪造"采集成功"。
	LastCollectAt     *time.Time `json:"last_collect_at"`
	LastCollectStatus string     `json:"last_collect_status"`
	LastCollectError  string     `json:"last_collect_error"`
	// LastCollectDegraded 上次采集走了降级路径（读不到 deployments，从 Pod 反推）。
	// 🔴 同样**只出不进**：这是采集器写的事实，不是用户填的配置。
	//    进了 Req 就等于允许前端伪造"这一列不是降级采的"，
	//    而降级恰恰意味着副本为 0 的服务看不见。
	LastCollectDegraded     bool   `json:"last_collect_degraded"`
	LastCollectDegradedNote string `json:"last_collect_degraded_note"`
}

func toDTO(in store.Org, projs []store.Project) orgDTO {
	d := orgDTO{
		ID: in.ID, Name: in.Name,
		ProviderType: in.ProviderType, AuthType: in.AuthType, Endpoint: in.Endpoint,
		DatasourceID: in.DatasourceID, DatasourceName: in.DatasourceName,
		DSProviderType: in.DSProviderType, DSEndpoint: in.DSEndpoint,
		HarborHost: in.HarborHost, HarborProject: in.HarborProject, IsSelf: in.IsSelf,
		Enabled:       in.Enabled,
		HasCredential: in.CredentialEnc != "",
		SyncStatus:    in.LastSyncStatus, SyncError: in.LastSyncError,
		Envs: []envDTO{},
	}
	if in.LastSyncAt.Valid {
		t := in.LastSyncAt.Time
		d.SyncAt = &t
	}
	for _, e := range in.Envs {
		ed := envDTO{
			Env: e.Env, ClusterRefs: e.ClusterRefs,
			NSInclude: e.NSInclude, NSExclude: e.NSExclude,
			WorkloadInclude: e.WorkloadInclude, WorkloadExclude: e.WorkloadExclude,
			CompareEnabled: e.CompareEnabled,
			ProjectID:      e.ProjectID,
			DatasourceID:   e.DatasourceID, DatasourceName: e.DSName,
			Endpoint:       e.Endpoint, AuthType: e.AuthType,
			HasCredential:     e.CredentialEnc != "",
			LastCollectStatus: e.LastCollectStatus, LastCollectError: e.LastCollectError,
			LastCollectDegraded: e.LastCollectDegraded, LastCollectDegradedNote: e.LastCollectDegradedNote,
		}
		if e.LastCollectAt.Valid {
			t := e.LastCollectAt.Time
			ed.LastCollectAt = &t
		}
		d.Envs = append(d.Envs, ed)
	}
	for _, p := range projs {
		d.Projects = append(d.Projects, projDTO{ID: p.ID, Name: p.Name, Enabled: p.Enabled})
	}
	return d
}

func (s *Server) listOrgs(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListOrgs(r.Context(), false)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 一次全量取回，按平台分组。
	// ⚠️ 查失败**不能**降级成「没有项目」：那会让所有列悄悄退回不分项目的老行为，
	//    多项目平台的表格看着正常，实际每一列都混了别的项目的服务。
	allProjs, err := s.St.ListProjects(r.Context(), 0)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	byOrg := map[int64][]store.Project{}
	for _, p := range allProjs {
		byOrg[p.OrgID] = append(byOrg[p.OrgID], p)
	}

	scope := userOf(r).Scope()
	out := []orgDTO{}
	for _, in := range list {
		// 数据范围过滤：与角色是两个独立维度
		if !scope.CanSee(in.ID) {
			continue
		}
		out = append(out, toDTO(in, byOrg[in.ID]))
	}
	ok(w, out)
}

type orgReq struct {
	Name          string `json:"name"`
	ProviderType  string `json:"provider_type"`
	AuthType      string `json:"auth_type"`
	Endpoint      string `json:"endpoint"`
	HarborHost    string `json:"harbor_host"`
	HarborProject string `json:"harbor_project"`
	IsSelf        bool   `json:"is_self"`
	// 🔴 曾经漏在这里：DTO（响应）有 Enabled，请求结构却没有 ——
	//    前端提交 enabled:true 被 json.Unmarshal **静默丢弃**，
	//    接口照样返回 {"ok":true}，于是"停用的平台永远启用不回来"。
	// ⚠️ 用指针：不传 = 不改动（新建时默认启用）。
	//    用 bool 的话，任何一次不带该字段的保存都会把平台停用掉 —— 零值即停用是灾难。
	Enabled *bool `json:"enabled"`
	// DatasourceID 引用哪个数据源。0 = 不引用，这个平台自己填地址和凭据。
	DatasourceID int64    `json:"datasource_id"`
	Username     string   `json:"username"`
	Password     string   `json:"password"`
	APIKey       string   `json:"api_key"`
	InsecureTLS  bool     `json:"insecure_tls"`
	Envs         []envReq `json:"envs"`
}

type envReq struct {
	Env         string   `json:"env"`
	ClusterRefs []string `json:"cluster_refs"`
	NSInclude   []string `json:"ns_include"`
	NSExclude   []string `json:"ns_exclude"`
	// 🔴 这两个曾经漏在这里：迁移、store、providers、collector、前端表单全改了，
	//    唯独 API 层的请求结构没加 —— JSON 解析时被**静默丢弃**，
	//    表现是「服务包含填了没反应」，不报错、日志里也看不出来。
	//    字段要新增就得把「模型→API→前端」三层一起走一遍，漏一层就是静默失效。
	WorkloadInclude []string `json:"workload_include"`
	WorkloadExclude []string `json:"workload_exclude"`
	CompareEnabled  bool     `json:"compare_enabled"`
	ProjectID       int64    `json:"project_id"`
	// 🔴 环境引用的数据源。客户 UAT / PROD 各一套 Rancher 时选它，
	//    凭据就只配一处 —— 不必在每个环境行重填一遍账号密码。
	//    （上面那段注释说的「模型→API→前端 三层一起走」，这个字段就是照着走的。）
	DatasourceID int64  `json:"datasource_id"`
	Endpoint     string `json:"endpoint"`
	AuthType        string   `json:"auth_type"`
	Username        string   `json:"username"`
	Password        string   `json:"password"`
	APIKey          string   `json:"api_key"`
	InsecureTLS     bool     `json:"insecure_tls"`
}

// encCred 把明文凭据加密。三者全空返回 ""，表示「不改动已有凭据」。
func (s *Server) encCred(user, pass, key string, insecure bool) (string, error) {
	if user == "" && pass == "" && key == "" {
		return "", nil
	}
	b, _ := json.Marshal(collector.Credential{
		Username: user, Password: pass, APIKey: key, InsecureTLS: insecure,
	})
	return s.Ciph.Encrypt(string(b))
}

func (s *Server) toInput(req orgReq) (store.OrgInput, error) {
	cred, err := s.encCred(req.Username, req.Password, req.APIKey, req.InsecureTLS)
	if err != nil {
		return store.OrgInput{}, err
	}
	in := store.OrgInput{
		Name: req.Name, ProviderType: req.ProviderType,
		AuthType: req.AuthType, Endpoint: req.Endpoint, CredentialEnc: cred,
		HarborHost: req.HarborHost, HarborProject: req.HarborProject, IsSelf: req.IsSelf,
		Enabled: req.Enabled, DatasourceID: req.DatasourceID,
	}
	for _, e := range req.Envs {
		ec, err := s.encCred(e.Username, e.Password, e.APIKey, e.InsecureTLS)
		if err != nil {
			return in, err
		}
		in.Envs = append(in.Envs, store.OrgEnv{
			Env: e.Env, ClusterRefs: e.ClusterRefs,
			NSInclude: e.NSInclude, NSExclude: e.NSExclude,
			WorkloadInclude: e.WorkloadInclude, WorkloadExclude: e.WorkloadExclude,
			CompareEnabled: e.CompareEnabled,
			ProjectID:      e.ProjectID,
			DatasourceID:   e.DatasourceID,
			Endpoint:       e.Endpoint, AuthType: e.AuthType, CredentialEnc: ec,
		})
	}
	return in, nil
}

func (s *Server) createOrg(w http.ResponseWriter, r *http.Request) {
	req, err := body[orgReq](r)
	if err != nil || req.Name == "" || req.ProviderType == "" {
		fail(w, http.StatusBadRequest, "bad_request", "name 和 provider_type 必填")
		return
	}
	in, err := s.toInput(req)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", "凭据加密失败")
		return
	}
	id, err := s.St.SaveOrg(r.Context(), 0, in)
	s.St.Audit(r.Context(), userOf(r).Username, "org.create", req.Name,
		map[string]any{"provider": req.ProviderType, "endpoint": req.Endpoint}, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"id": id})
}

func (s *Server) updateOrg(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	req, err := body[orgReq](r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	in, err := s.toInput(req)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", "凭据加密失败")
		return
	}
	// 🔴 改之前先把旧配置读出来，用于算差异。
	//    workload_exclude 是这个系统里**杀伤力最大的配置**：改一个字符就能让
	//    22 个在线服务在对账表上变成「该平台未部署此服务」，
	//    并往变更历史灌一批假下线。
	//    而当时的审计只记了 {id, endpoint} —— 那条因果链最后是靠
	//    「时间戳 + 服务端日志的 services 计数落差 + CMDB 交叉验证」三方拼出来的，
	//    审计本身给不出答案，而这恰恰是审计该回答的问题。
	before, beforeErr := s.St.GetOrg(r.Context(), id)
	_, err = s.St.SaveOrg(r.Context(), id, in)
	detail := map[string]any{"id": id, "endpoint": req.Endpoint}
	if beforeErr == nil {
		if d := ruleDiff(before, req); len(d) > 0 {
			detail["rule_changes"] = d
		}
	}
	s.St.Audit(r.Context(), userOf(r).Username, "org.update", req.Name,
		detail, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

func (s *Server) deleteOrg(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	// 删之前留一份快照：原来 target 只有一个数字 id、详情是 nil ——
	// 平台一删，审计里连"删掉的是哪个平台"都答不出来。
	// ⚠️ 只记配置，**绝不记凭据**（envSnapshot 里没有 credential）。
	gone, goneErr := s.St.GetOrg(r.Context(), id)
	target := strconv.FormatInt(id, 10)
	detail := map[string]any{"id": id}
	if goneErr == nil {
		target = gone.Name
		detail["envs"] = envSnapshot(gone)
	}
	err := s.St.DeleteOrg(r.Context(), id)
	s.St.Audit(r.Context(), userOf(r).Username, "org.delete", target,
		detail, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

// probeOrg 测试连通。
//
// 🔴 必须能分辨认证失败 / 网络不通 / 权限不足 —— 三者的处理方式完全不同：
// 密码错去找对方管理员、网络不通去查防火墙、403 去要权限。
// 笼统一句「连接失败」等于没说，用户只能瞎试。
func (s *Server) probeOrg(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	in, err := s.St.GetOrg(r.Context(), id)
	if err != nil {
		fail(w, http.StatusNotFound, "not_found", "平台不存在")
		return
	}

	env := r.URL.Query().Get("env")
	target, found := store.OrgEnv{}, false
	for _, e := range in.Envs {
		if env == "" || e.Env == env {
			target, found = e, true
			break
		}
	}
	if !found {
		fail(w, http.StatusBadRequest, "no_env", "该平台还没有配置任何环境")
		return
	}

	p, err := s.Coll.BuildProvider(in, target)
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		err = p.Probe(ctx)
		cancel()
	}
	s.St.Audit(r.Context(), userOf(r).Username, "org.probe", in.Name,
		map[string]any{"env": target.Env}, err, clientIP(r))

	if err != nil {
		// 🔴 业务失败但返回 200 时，**必须自己打日志**。
		//    访问日志只记非 2xx 和慢请求（api.go 的中间件），
		//    于是"探测失败 = 200 + 秒回"在日志里彻底隐身 ——
		//    排障时无法回答"用户到底点没点、失败在哪一步"。
		// ⚠️ 这不只是少条日志：排查 时因为查不到 probe 请求，
		//    一度判成"请求根本没发出去"，差点去改前端事件绑定。
		//    看不见的失败会主动把排障引向错误分支，比没有日志更糟。
		logx.Warn("probe", "failed", map[string]any{
			"org": in.Name, "env": target.Env,
			"kind": classifyKind(err), "err": err.Error(),
		})
		ok(w, map[string]any{
			"ok": false, "kind": classifyKind(err), "message": err.Error(),
		})
		return
	}
	ok(w, map[string]any{"ok": true, "message": "连接正常"})
}

func classifyKind(err error) string {
	switch {
	case errors.Is(err, providers.ErrAuth):
		return "auth_failed"
	case errors.Is(err, providers.ErrUnreachable):
		return "unreachable"
	case errors.Is(err, providers.ErrForbidden):
		return "forbidden"
	default:
		return "error"
	}
}

// orgClusters 拉这个平台能看到的集群列表，配置环境映射时给用户下拉选。
// 不用手抄 clusterId（Rancher 的 c-m-xxxx 抄错一个字符就查不到任何东西）。
func (s *Server) orgClusters(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	in, err := s.St.GetOrg(r.Context(), id)
	if err != nil {
		fail(w, http.StatusNotFound, "not_found", "平台不存在")
		return
	}
	env := store.OrgEnv{Env: r.URL.Query().Get("env")}
	for _, e := range in.Envs {
		if e.Env == env.Env {
			env = e
			break
		}
	}
	p, err := s.Coll.BuildProvider(in, env)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_config", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	switch pv := p.(type) {
	case *providers.Kite:
		names, err := pv.Clusters(ctx)
		if err != nil {
			fail(w, http.StatusBadGateway, classifyKind(err), err.Error())
			return
		}
		out := []map[string]string{}
		for _, n := range names {
			out = append(out, map[string]string{"id": n, "name": n})
		}
		ok(w, out)
	case *providers.Rancher:
		m, err := pv.Clusters(ctx)
		if err != nil {
			fail(w, http.StatusBadGateway, classifyKind(err), err.Error())
			return
		}
		out := []map[string]string{}
		for id, name := range m {
			out = append(out, map[string]string{"id": id, "name": name})
		}
		ok(w, out)
	case *providers.ArgoCD:
		// ⚠️ ArgoCD 的"集群"是 destination（name 或 server URL），
		//    与 Kite/Rancher 的集群名不是同一套命名。让人手打必然填错，
		//    而填错的表现是**零结果** —— 跟"对方没有服务"长得一样。
		names, err := pv.Clusters(ctx)
		if err != nil {
			fail(w, http.StatusBadGateway, classifyKind(err), err.Error())
			return
		}
		out := []map[string]string{}
		for _, n := range names {
			out = append(out, map[string]string{"id": n, "name": n})
		}
		ok(w, out)
	default:
		ok(w, []map[string]string{})
	}
}

func (s *Server) collectOrg(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	in, err := s.St.GetOrg(r.Context(), id)
	if err != nil {
		fail(w, http.StatusNotFound, "not_found", "平台不存在")
		return
	}
	// 同步执行：采集通常几秒到几十秒，同步返回能让用户立刻看到结果与错误分类。
	// 异步的话失败信息只能去日志里翻，"点了没反应"是最难排查的一类问题。
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	// env 非空时只采那一个环境 —— 界面上「刷新数据」是逐列做的，
	// 一列一个请求才能显示进度（见 CollectOrgEnv 的说明）
	onlyEnv := strings.TrimSpace(r.URL.Query().Get("env"))
	// ⚠️ project 不传 = 该环境下所有项目都采（老调用点、以及「全部采集」）。
	//    界面上逐列刷新时会带上它。
	onlyProject, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	// 🔴 -1 是对账页**平台级列**的标识，语义就是「该环境下的所有项目」，与不传等价。
	//    不认它的话，下面的过滤 `env.ProjectID != onlyProject` 会把每一个环境都跳过 ——
	//    采集空转、一条数据都不刷，而调用方拿到的却可能是「成功」。
	//    前端已经会把平台级列展开成真实项目再刷（见 useFreshness.refreshColumns），
	//    这里是纵深防御：直接调 API 的客户端不该踩这个坑。
	if onlyProject == projectAll {
		onlyProject = 0
	}
	err = s.Coll.CollectOrgEnvProject(ctx, in, onlyEnv, onlyProject)
	s.St.Audit(r.Context(), userOf(r).Username, "org.collect", in.Name,
		map[string]any{"env": onlyEnv, "project": onlyProject}, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusBadGateway, classifyKind(err), err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

// ---------- 对账 ----------

// planCol 请求里的一列 = 平台 × 项目 × 环境。
//
// 🔴 ProjectID 缺省（0）必须解释成「该平台的默认项目」，不能解释成「没有项目」。
//
//	方案是**存下来**的：加项目层之前存的方案里根本没有这个字段，
//	解释成「没有项目」的话，所有老方案打开就是空表 ——
//	而界面上看不出任何异常，只会以为数据没采到。
type planCol struct {
	OrgID     int64  `json:"org_id"`
	ProjectID int64  `json:"project_id"`
	Env       string `json:"env"`
}

type compareReq struct {
	// 列自由组合，不假设「同环境对同环境」
	Columns []planCol `json:"columns"`
	// 🔴 baseline / baseline_pin 已删。
	//
	//    判定改成横着比这几列彼此，没有参照物这回事。
	//    ⚠️ 留着这两个字段"收下但不读"是最坏的选项：
	//       老前端或外部脚本照旧传，服务端静默丢弃 ——
	//       调用方以为自己指定了基准，而结果完全是另一套语义，
	//       且没有任何报错。字段删掉之后，多传的键被 JSON 解码忽略，
	//       但至少代码里不存在"看起来会用"的入口。
	// PlanName 仅用于导出时写进「数据说明」页，让收到附件的人知道这是哪个方案
	PlanName string `json:"plan_name"`
	// ServiceInclude 只比这些服务（镜像名最后一段），支持 * 通配。留空 = 全部
	ServiceInclude []string `json:"service_include"`
	// Ignores 人为忽略项。随请求带 —— 界面上勾一下就要立刻看到效果，
	// 不必先存方案。存方案时再把同一份规则写进去。
	Ignores compare.IgnoreSet `json:"ignores"`
	// FilterNote 界面上应用了什么筛选。仅用于写进导出文件的「数据说明」页 ——
	// 🔴 那张表会被转发，收到的人必须知道手里这份是**筛过的**，
	//    否则他会拿一份残缺的清单当全量去开会。
	FilterNote string `json:"filter_note"`
	OnlyDiff   bool   `json:"only_diff"`
}

// onlyDiffRows 只留下需要人去看的行（结论不是「一致」也不是「已忽略」）。
//
// 🔴 对账与导出**必须共用这一份**。两处各写一遍的后果实测过：
//
//	对账那边过滤了、导出那边没有，于是界面 71 行、导出的 Excel 114 行，
//	而「数据说明」页里一个字都没提 —— 收到附件的人无从判断哪份才算数。
//
// ⚠️ 只过滤 Rows，**不动 Summary**：Summary 是全量计数，
//
//	它回答的是「总共多少服务」，筛选不该改变这个分母。
func onlyDiffRows(rows []compare.Row) []compare.Row {
	out := rows[:0:0]
	for _, row := range rows {
		if row.HasDiff {
			out = append(out, row)
		}
	}
	return out
}

// buildPlan 把请求里的列组合成 compare.Plan，并做数据范围校验。
//
// 对账与导出**必须共用这一份**：两处各写一遍的话，
// 有一天给对账加了范围校验而忘了导出，就等于开了一个后门 ——
// 界面上看不到的组织，导出一次全拿到了。
func (s *Server) buildPlan(r *http.Request, req compareReq) (compare.Plan, int, string) {
	insts, err := s.St.ListOrgs(r.Context(), false)
	if err != nil {
		return compare.Plan{}, http.StatusInternalServerError, err.Error()
	}
	byID := map[int64]store.Org{}
	for _, in := range insts {
		byID[in.ID] = in
	}

	// 项目：按平台分组，供列构造时解析归属与筛选规则。
	// 一次比对最多几个平台，逐个查即可，不值得为此加个批量接口。
	projByOrg := map[int64][]store.Project{}
	for _, c := range req.Columns {
		if _, done := projByOrg[c.OrgID]; done {
			continue
		}
		ps, err := s.St.ListProjects(r.Context(), c.OrgID)
		if err != nil {
			return compare.Plan{}, http.StatusInternalServerError, err.Error()
		}
		projByOrg[c.OrgID] = ps
	}

	scope := userOf(r).Scope()
	plan := compare.Plan{
		ServiceInclude: req.ServiceInclude,
		Ignores:        req.Ignores,
	}
	for _, c := range req.Columns {
		in, okk := byID[c.OrgID]
		if !okk {
			return compare.Plan{}, http.StatusBadRequest, "平台不存在"
		}
		if !scope.CanSee(in.ID) {
			// 数据范围外的组织直接拒绝，而不是静默跳过 ——
			// 静默跳过会让用户以为"对比结果就是这样"，实际少了一整列
			return compare.Plan{}, http.StatusForbidden, "没有权限查看平台 " + in.Name
		}
		// 🔴 列的采集状态必须取**这个环境**的，不能用平台级的。
		//    平台级只能记一个值 —— UAT 采成功、PROD 采失败时，
		//    两列会显示同一个状态，其中一列的"数据不可用"就此消失，
		//    然后被当成真实数据参与判定，一致率也跟着失真。
		//    ⚠️ 老数据（012 迁移前采的）没有环境级记录，回落到平台级，
		//    否则升级后所有列都会显示成"从未采集"。
		col := compare.Column{OrgID: in.ID, OrgName: in.Name, Env: c.Env, IsSelf: in.IsSelf}

		// ─── 平台级汇总列 ───
		//
		// 🔴 project_id = -1 表示「这一列是整个平台在该环境上的全部项目」。
		//
		//    为什么需要这一层：一列 = 项目 × 环境，而同一个服务在两个平台的
		//    **不同项目**里跑是常态 —— 逐项目比必然大面积「缺失」。
		//    实测过：5 列逐项目比 = 122 行全判缺失、0 条有效结论；
		//    换成平台级汇总后是「42 一致 / 22 版本不同 / 8 一方整体未部署」，
		//    真正要处理的从 122 降到 22，该忽略的从 130 个格子降到 8 个。
		//
		// ⚠️ 用 -1 而不是复用 0：0 已经有「按环境回落到默认项目」的语义
		//    （resolveProject），改它会动到所有既有调用方。
		// ⚠️ 不调 resolveProject、不设 Filter：跨项目了，某一个项目的
		//    service_include 不能拿来筛整个平台。
		if c.ProjectID == projectAll {
			col.ProjectID = 0 // store 层：0 = 不限项目，查该平台该环境全部
			col.ProjectName = allProjectsLabel
			col.Aggregated = true

			col.SyncStatus, col.SyncError, col.SyncedAt = rollUpEnvStatus(in, c.Env)
			col.Degraded, col.DegradedNote = rollUpDegraded(in, c.Env)
			if ex, e := s.St.ListExcludedKeys(r.Context(), c.OrgID, 0, c.Env); e == nil {
				col.ExcludedKeys = ex
			}
			plan.Columns = append(plan.Columns, col)
			continue
		}

		envProj := int64(0)
		if env, found := envOf(in, c.Env, c.ProjectID); found {
			envProj = env.ProjectID
		}
		if pr, found := resolveProject(projByOrg[c.OrgID], c.ProjectID, envProj); found {
			col.ProjectID = pr.ID
			col.Filter = compare.ProjectFilter{Include: pr.ServiceInclude, Pins: pr.ServicePins}
			// ⚠️ 只有该平台**确实有多个项目**时才把项目名放进表头。
			//    单项目平台显示成「A平台·默认/UAT」是纯噪音，
			//    而多项目时不显示则是两列同名 —— 分不清哪列是哪个项目。
			//
			// 🔴 「默认」是建平台时自动生成的占位名，不带任何业务信息 ——
			//    即使平台下有多个项目，它也不该占表头的位置。
			//    实际形态是「A公司/UAT」和「A公司·项目B/PROD」并排，
			//    有名字的那个自然就区分开了；而「A公司·默认/UAT」只是让人多读两个字，
			//    还会被误读成"有个叫默认的项目"。
			if countEnabledProjects(projByOrg[c.OrgID]) > 1 && !isPlaceholderProject(pr.Name) {
				col.ProjectName = pr.Name
			}
		}
		if env, found := envOf(in, c.Env, col.ProjectID); found && env.LastCollectStatus != "" {
			col.SyncStatus, col.SyncError = env.LastCollectStatus, env.LastCollectError
			if env.LastCollectAt.Valid {
				col.SyncedAt = env.LastCollectAt.Time
			}
		} else {
			col.SyncStatus, col.SyncError = in.LastSyncStatus, in.LastSyncError
			if in.LastSyncAt.Valid {
				col.SyncedAt = in.LastSyncAt.Time
			}
		}
		// 🔴 把这一列**采集期**的排除规则带给对账引擎。
		//    不带的话，我方主动不采的服务会被判成「该平台未部署此服务」——
		//    实测过 22 个 healthy 的服务中过这一枪。
		// ⚠️ 与上面那段分开取：那段带了 LastCollectStatus != "" 的条件，
		//    而规则是配置、与这一轮采没采成功无关。
		if env, found := envOf(in, c.Env, col.ProjectID); found {
			col.ExcludeRules = env.WorkloadExclude
			// 降级采集的列，「查不到」只能说"看不见"，不能说"没部署"
			col.Degraded = env.LastCollectDegraded
			col.DegradedNote = env.LastCollectDegradedNote
		}
		// 🔴 被规则排掉的服务清单 —— 采集时记下的**事实**，优先于拿规则反推。
		//    查失败不阻断整次对账：那只会让这一列退回反推路径。
		if ex, e := s.St.ListExcludedKeys(r.Context(), c.OrgID, col.ProjectID, c.Env); e == nil {
			col.ExcludedKeys = ex
		} else {
			logx.Warn("compare", "load_excluded_failed", map[string]any{
				"col": col.Key(), "err": e.Error()})
		}
		plan.Columns = append(plan.Columns, col)
	}

	// 归因数据：这些组织的镜像同步记录。
	//
	// 🔴 **只放真正查到的组织**。map 里没有某个 orgID，compare 会归因为
	// 「未绑定复制规则，无法判断」而不是「镜像没同步」——
	// 这两件事的下一步完全不同（一个去绑规则，一个去查 Harbor）。
	// 所以查失败时**不能塞一个空 map 进去**，那等于宣称「查过了，确实没同步」。
	facts := map[int64]map[string]compare.SyncFact{}
	// gaps：某个平台**为什么**没有复制记录。三种成因的下一步完全不同
	gaps := map[int64]string{}
	seen := map[int64]bool{}
	for _, c := range plan.Columns {
		if seen[c.OrgID] {
			continue
		}
		seen[c.OrgID] = true
		m, err := s.St.SyncFactsOf(r.Context(), c.OrgID)
		if err != nil {
			logx.Warn("compare", "sync_facts_failed", map[string]any{
				"org": c.OrgName, "err": err.Error()})
			// 🔴 查失败要**说出来**，不能和「没绑规则」混成一句话
			gaps[c.OrgID] = s.St.SyncGapReason(r.Context(), c.OrgID, c.OrgName, err)
			continue
		}
		if len(m) == 0 {
			// 一条记录都没有。⚠️ 成因有三种（没绑 / 没拉 / 规则没跑过），
			//    在这里查清楚再告诉人 —— compare 包看不到这些
			gaps[c.OrgID] = s.St.SyncGapReason(r.Context(), c.OrgID, c.OrgName, nil)
			continue
		}
		conv := make(map[string]compare.SyncFact, len(m))
		for k, v := range m {
			conv[k] = compare.SyncFact{Status: v.Status, ErrMsg: v.ErrMsg}
			if v.FinishedAt != nil {
				f := conv[k]
				f.FinishedAt = *v.FinishedAt
				conv[k] = f
			}
		}
		facts[c.OrgID] = conv
	}
	plan.SyncFacts = facts
	plan.SyncGaps = gaps

	return plan, 0, ""
}

func (s *Server) compareHandler(w http.ResponseWriter, r *http.Request) {
	req, err := body[compareReq](r)
	if err != nil || len(req.Columns) < 2 {
		fail(w, http.StatusBadRequest, "bad_request", "至少要选两列才能对比")
		return
	}
	plan, code, msg := s.buildPlan(r, req)
	if code != 0 {
		fail(w, code, "bad_request", msg)
		return
	}

	data, err := s.St.LoadSnapshots(r.Context(), plan.Columns)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	res := compare.Compare(plan, data)

	rows := res.Rows
	if req.OnlyDiff {
		rows = onlyDiffRows(rows)
	}

	summary := map[string]int{}
	for k, v := range res.Summary {
		summary[string(k)] = v
	}
	// ⚠️ 只在确实「绝大多数是缺失」时才去找异类列：结果正常时点名一列
	//    会误导用户取消勾选本该参与对账的数据。
	worstCol, worstPct := "", 0
	if mostlyMissing(res) {
		worstCol, worstPct = worstMissingColumn(res)
	}
	ok(w, map[string]any{
		"columns": plan.Columns,
		"rows":    rows,
		"summary": summary,
		// 🔴 结果几乎全是「没有」时给一句提示。
		//    这种结果绝大多数不是真的两边都没部署，而是**列选错了**
		//    （比如把两批毫不相干的平台放进同一次比对）。
		//    不提示的话，人对着一屏灰格子只能自己琢磨，
		//    而「全是灰的」和「确实没差异」在视觉上很接近。
		"mostly_missing": mostlyMissing(res),
		// 指名道姓说出是哪一列拉低了重合度 —— 光说「多半是列选错了」
		// 只是把问题原样还给用户
		"worst_overlap_col": worstCol,
		"worst_overlap_pct": worstPct,
		// 🔴 单独返回并要求前端显著提示：整列 NoData 时表面上只是几个灰格子，
		//    但结论已经不完整了 —— 不提示的话用户会拿一份残缺的对账当结论
		"unhealthy_columns": res.UnhealthyColumns,
		// 🔴 整行忽略的服务名必须给出来。这些服务**不在 rows 里** ——
		//    只给一个数字的话，人没法确认自己到底排除了什么，
		//    而通配规则（`bi-*`）命中了哪些更是完全看不见。
		//    ⚠️ 兜成 []：nil 序列化成 null，前端 .length 会炸。
		"ignored_rows": orEmptyStrings(res.IgnoredRows),
		// 🔴 逐格忽略的数量必须单独给。summary 按**行**统计，
		//    只忽略了某一列的格子在里面完全看不见 ——
		//    而「忽略必须看得见」是硬要求：藏起来的话，几个月后
		//    没人说得清某个格子为什么是空的。
		"ignored_cells": res.IgnoredCells,
	})
}

// mostlyMissing 判断这次比对是不是「几乎全是没有」。
//
// 判据：结论为「缺失」的**行**占了八成以上，且总量够大
// （少量服务时本来就容易全是 missing，提示反而是噪音）。
//
// ⚠️ Summary 现在按**行**统计（一行一个结论），不再按格子。
func mostlyMissing(res compare.Result) bool {
	total, missing := 0, 0
	for k, v := range res.Summary {
		total += v
		if k == compare.VerdictMissing {
			missing += v
		}
	}
	if total < 10 {
		return false
	}
	return float64(missing)/float64(total) >= 0.8
}

func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) {
	iid, _ := strconv.ParseInt(r.URL.Query().Get("org_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.St.ListChanges(r.Context(), iid, r.URL.Query().Get("service_key"), limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}

// listAudit 读一页审计。
//
// 🔴 游标分页（`before`），不是 offset：审计表一直在插入，
// offset 会让同一条在两页里都出现、或者被整个跳过 ——
// 而审计恰恰是最不该漏记录的地方。
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	page, err := s.St.ListAudit(r.Context(), limit, before)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, page)
}

// columnFreshness 各列的数据新鲜度。
//
// 界面用它决定"点比对时要刷新哪几列" —— 只刷过期的，新鲜的跳过。
// ⚠️ 这个接口只读，不触发采集：把"看有多旧"和"去刷新"分开，
// 才可能做出"我就想看现在这份数据"的选择（对方系统挂着时唯一能做的事）。
func (s *Server) columnFreshness(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListColumnFreshness(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}

// envOf 取平台下某个项目在某环境上的配置。
//
// 🔴 必须带 projectID：同一个平台的同一个环境**现在可能有多行**
//
//	（每个项目一行）。只按 env 找会拿到"第一个碰上的"，
//	于是 A 项目的采集状态被显示到 B 项目的列上 ——
//	B 明明采成功，界面上却写着采集失败，反过来也一样。
//
// ⚠️ projectID=0 表示不限（老调用点、或确实只关心"这个环境有没有配过"）。
func envOf(in store.Org, env string, projectID int64) (store.OrgEnv, bool) {
	for _, e := range in.Envs {
		if e.Env != env {
			continue
		}
		if projectID == 0 || e.ProjectID == projectID {
			return e, true
		}
	}
	return store.OrgEnv{}, false
}

// orEmptyStrings 出参的切片一律给 []，不给 null。
// Go 的 nil 切片序列化成 JSON null，而前端类型写的是 string[] ——
// `.length` 当场抛异常，整棵子树白屏，接口却是 200。
func orEmptyStrings(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}

// ---------- 服务包含 / 排除规则的保存前预检 ----------

type rulePreviewItem struct {
	Pattern string   `json:"pattern"`
	Matched int      `json:"matched"`
	Samples []string `json:"samples"`
	// Invalid 语法问题的人话说明。空 = 语法没问题。
	Invalid string `json:"invalid"`
	// Active 这条规则**已经存在于已保存的配置里**（即：它可能正在生效）。
	//
	// 🔴 这个字段是为了打破一个自证循环：
	//    排除规则一旦生效，被它排掉的服务就不再进快照，而预检的样本正是快照 ——
	//    于是一条**完全正确、正在排除 22 个服务**的规则，预检会报「命中 0」。
	//    若据此提示"几乎一定写错了"，用户就会把正确规则改回错的，
	//    改回去之后服务重新进快照、预检显示"命中 22"，看起来反而"修好了"。
	//    这会让人在正确与错误配置之间来回摇摆，且每次都得到"看起来正确"的反馈。
	//
	// ⚠️ 所以：Active 的规则命中 0 是**正常现象**，不能报警；
	//    只有**新写的/改过的**规则命中 0 才值得提醒。
	Active bool `json:"active"`
}

type rulePreviewResp struct {
	// Total 该环境上次采集到的服务总数。0 = 从没采过，此时命中数全是 0 且不说明任何问题。
	Total int `json:"total"`
	// Kept 按这套规则过一遍后还剩几个。用 providers.WorkloadRules.Match 算 ——
	// 必须和采集器用同一个判据，否则预检说的和实际抄回来的不是一回事。
	Kept    int               `json:"kept"`
	Include []rulePreviewItem `json:"include"`
	Exclude []rulePreviewItem `json:"exclude"`
}

// checkPattern 只查**语法**。
//
// ⚠️ 这一层拦不住 那个笔误：`*--game-server-backend` 的 `*` 在首位，
// 语法完全合法，只是多了一个连字符所以永远不命中。
// 真正能拦住它的是「命中 0 个」那条反馈 —— 两层缺一不可。
func checkPattern(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "空规则"
	}
	if strings.Trim(p, "*") == "" {
		return "" // 纯 "*" 是合法的：匹配全部
	}
	if strings.Contains(strings.Trim(p, "*"), "*") {
		return "* 只能放在开头或结尾"
	}
	return ""
}

// previewRules 拿该环境**最近一次采集到的服务名**跑一遍规则，回答"这条规则现在命中几个"。
//
// 🔴 为什么必须有这个接口：规则是人手填的自由文本，写错一个字符就永远不命中，
// 而保存成功、界面正常、比对照跑 —— 那条规则只是**静默失效**。
// 命中 0 个几乎一定是写错了，这是唯一能在保存那一刻就发现笔误的信号。
//
// ⚠️ 判定必须走 providers 那一份实现（MatchPattern / WorkloadRules.Match），
// 前端另写一份必然与采集器漂移 —— 那会变成"预检说命中、实际没抄回来"，比没有预检更糟。
func (s *Server) previewRules(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	in, err := s.St.GetOrg(r.Context(), id)
	if err != nil {
		fail(w, http.StatusNotFound, "not_found", "平台不存在")
		return
	}
	if !userOf(r).Scope().CanSee(in.ID) {
		fail(w, http.StatusForbidden, "forbidden", "没有权限查看平台 "+in.Name)
		return
	}
	req, err := body[struct {
		Env       string   `json:"env"`
		ProjectID int64    `json:"project_id"`
		Include   []string `json:"workload_include"`
		Exclude   []string `json:"workload_exclude"`
	}](r)
	if err != nil || strings.TrimSpace(req.Env) == "" {
		fail(w, http.StatusBadRequest, "bad_request", "要指定环境")
		return
	}

	// 🔴 样本是「服务名 → 它的 workload 名列表」，匹配时用 **workload 名**。
	//
	//    规则是采集期规则，采集器按 workload 名过滤 —— 预检必须用同一个判据。
	//    拿 ServiceKey 去比规则，在 helm 环境下报的命中与实际排除的**没有交集**：
	//    实测规则 另一个产品-* 真正排掉的是 另一个产品-plane-*（workload 名匹配），
	//    而按 ServiceKey 比会报成 另一个产品-backend（它的 workload 是
	//    opsalert-另一个产品-backend，根本没匹配上，一直在正常采集）——。
	//
	// ⚠️ 样本里已并入 excluded_services：被排掉的服务不在快照里，
	//    不并进来的话已生效的规则永远显示"命中 0"。
	sample, err := s.St.ListServiceWorkloads(r.Context(), id, req.ProjectID, req.Env)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 服务名排序后再用，让 samples 的输出稳定 —— 每次点「检查」看到不同的样例
	// 会让人以为规则变了
	keys := make([]string, 0, len(sample))
	for k := range sample {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// matchesRule 这个服务的**任一** workload 名命中规则，就算这条规则会排掉它 ——
	// 与 providers.WorkloadRules.Match 的语义一致
	matchesRule := func(pat, svc string) bool {
		for _, wl := range sample[svc] {
			if providers.MatchPattern(pat, wl) {
				return true
			}
		}
		return false
	}

	// 已保存的规则集合 —— 用来区分「这条已经在生效」和「这条是刚写的」。
	// ⚠️ 样本已包含被排掉的服务，所以已生效的规则**能**算出真实命中数了；
	//    Active 现在只用于把"命中 0"的原因说得更准（已保存的规则命中 0
	//    多半是环境变了，新写的则多半是拼错了）。
	savedInc, savedExc := map[string]bool{}, map[string]bool{}
	if env, found := envOf(in, req.Env, req.ProjectID); found {
		for _, p := range env.WorkloadInclude {
			savedInc[strings.TrimSpace(p)] = true
		}
		for _, p := range env.WorkloadExclude {
			savedExc[strings.TrimSpace(p)] = true
		}
	}

	count := func(pats []string, saved map[string]bool) []rulePreviewItem {
		out := []rulePreviewItem{}
		for _, p := range pats {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			it := rulePreviewItem{
				Pattern: p, Samples: []string{}, Invalid: checkPattern(p),
				Active: saved[p],
			}
			for _, k := range keys {
				if matchesRule(p, k) {
					it.Matched++
					if len(it.Samples) < 3 {
						it.Samples = append(it.Samples, k)
					}
				}
			}
			out = append(out, it)
		}
		return out
	}

	// kept 同样按 workload 名算 —— 与采集器逐字同源
	rules := providers.WorkloadRules{Include: req.Include, Exclude: req.Exclude}
	kept := 0
	for _, k := range keys {
		for _, wl := range sample[k] {
			if rules.Match(wl) {
				kept++
				break
			}
		}
	}

	ok(w, rulePreviewResp{
		// Total 含被规则排掉的那些 —— 它们同样是"这个环境上有的服务"
		Total:   len(keys),
		Kept:    kept,
		Include: count(req.Include, savedInc),
		Exclude: count(req.Exclude, savedExc),
	})
}

// ---------- 平台配置变更的审计详情----------

// envSnapshot 环境级配置的快照，用于审计。
//
// 🔴 **只记配置，绝不记凭据**：credential_enc / username / password / api_key
// 一个都不能进审计。审计日志会被导出、会被更多人看到 ——
// 全站两个 P0 都是「接口把凭据发给了不该看的人」，这里是同一类风险面。
func envSnapshot(in store.Org) []map[string]any {
	out := make([]map[string]any, 0, len(in.Envs))
	for _, e := range in.Envs {
		out = append(out, map[string]any{
			"env": e.Env, "project_id": e.ProjectID,
			"ns_include": e.NSInclude, "ns_exclude": e.NSExclude,
			"workload_include": e.WorkloadInclude, "workload_exclude": e.WorkloadExclude,
			"compare_enabled": e.CompareEnabled,
		})
	}
	return out
}

// ruleDiff 算出**采集规则**的前后差异，只返回真正变了的那些。
//
// 🔴 为什么记差异而不是只记新值：排查时要回答的是「谁把它改成这样的」，
// 而只有新值的话，看到 `workload_exclude: [*-game-server-backend]` 也无从判断
// 这次改动到底动了什么 —— 那正是 /043 排查时缺的那块。
//
// ⚠️ 按 (env, project_id) 配对，不按下标：环境行的顺序不保证稳定，
// 按下标比会在增删环境时把两个不相干的环境比在一起，diff 全是噪音。
func ruleDiff(before store.Org, req orgReq) []map[string]any {
	type ruleSet struct {
		nsInc, nsExc, wlInc, wlExc []string
		compare                    bool
	}
	oldBy := map[string]ruleSet{}
	for _, e := range before.Envs {
		k := e.Env + "\x00" + strconv.FormatInt(e.ProjectID, 10)
		oldBy[k] = ruleSet{e.NSInclude, e.NSExclude, e.WorkloadInclude, e.WorkloadExclude, e.CompareEnabled}
	}

	changes := []map[string]any{}
	for _, e := range req.Envs {
		k := e.Env + "\x00" + strconv.FormatInt(e.ProjectID, 10)
		o, existed := oldBy[k]
		if !existed {
			changes = append(changes, map[string]any{
				"env": e.Env, "project_id": e.ProjectID, "added": true,
				"ns_include": e.NSInclude, "ns_exclude": e.NSExclude,
				"workload_include": e.WorkloadInclude, "workload_exclude": e.WorkloadExclude,
			})
			continue
		}
		delete(oldBy, k)
		one := map[string]any{"env": e.Env, "project_id": e.ProjectID}
		add := func(name string, oldV, newV []string) {
			if !sameStrings(oldV, newV) {
				one[name] = map[string]any{"from": oldV, "to": newV}
			}
		}
		add("ns_include", o.nsInc, e.NSInclude)
		add("ns_exclude", o.nsExc, e.NSExclude)
		add("workload_include", o.wlInc, e.WorkloadInclude)
		add("workload_exclude", o.wlExc, e.WorkloadExclude)
		if o.compare != e.CompareEnabled {
			one["compare_enabled"] = map[string]any{"from": o.compare, "to": e.CompareEnabled}
		}
		// 只有 env/project_id 两个键 = 这个环境没动，不记
		if len(one) > 2 {
			changes = append(changes, one)
		}
	}
	// 剩下的是被删掉的环境行 —— 同样要记，否则"整个环境被删了"在审计里看不见
	for k, o := range oldBy {
		env, _, _ := strings.Cut(k, "\x00")
		changes = append(changes, map[string]any{
			"env": env, "removed": true,
			"workload_exclude": o.wlExc, "ns_include": o.nsInc,
		})
	}
	return changes
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}

// worstMissingColumn 找出**造成最多「缺失」判定**的那一列。
//
// 🔴 为什么需要它：对账页默认全选所有列，而各列的采集范围常常差得很远。
// 实测过：5 列全选 → 122 行 **100% 判缺失**，一条有效结论都没有 ——
// 这是新用户打开这个产品看到的第一眼。
// 系统已经会弹「多半是列选错了」，判据也对，但它只说"你去检查一下"。
// 指名道姓地说出是哪一列、它让多少行变成缺失，才是**可执行**的下一步。
//
// ⚠️ 判据用「这一列贡献了多少 missing」，不是「服务名重合度」。
//
//	第一版写的是重合度，实测过证明那个维度抓不住元凶：
//	我方·项目B 只有 13 个服务（全表 122 行），它那 13 个里有 9 个在别列也有 ——
//	**重合度 69% 很高，可它在 109 行上都是 missing**。
//	重合度回答的是"这列的服务像不像别人"，而我们要问的是"这列拖累了多少行"。
//
// ⚠️ 只数 CellMissing，不数 CellNoData：后者是采集失败或降级采集
//
//	（界面另有专门的提示条），把它算进来会指向一列"数据没采到"的，
//	而那时该做的是修采集，不是取消勾选。
func worstMissingColumn(res compare.Result) (string, int) {
	total := len(res.Rows)
	if total == 0 {
		return "", 0
	}
	missByCol := map[string]int{}
	for _, row := range res.Rows {
		for _, c := range row.Cells {
			if c.State == compare.CellMissing {
				missByCol[c.Column.Key()]++
			}
		}
	}
	worstKey, worstN := "", 0
	for k, n := range missByCol {
		if n > worstN {
			worstKey, worstN = k, n
		}
	}
	// 只有当这一列拖累了**过半**的行时才点名。
	// ⚠️ 阈值不能低：指错一列比不指更糟 —— 用户会取消勾选一列本该参与对账的数据。
	if worstKey == "" || worstN*100/total < 50 {
		return "", 0
	}
	return worstKey, worstN * 100 / total
}
