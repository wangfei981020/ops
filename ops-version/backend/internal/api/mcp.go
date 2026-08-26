package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ops-version-backend/internal/auth"
	"ops-version-backend/internal/compare"
	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
)

// MCP over HTTP（JSON-RPC 2.0）。
//
// 目标场景：让 AI 能独立回答「B平台 prod 哪些服务落后了，分别是什么原因」，
// 而不是把一堆原始数据丢给它自己拼。所以工具的返回值是**判定过的结论**，
// 不是原始快照 —— 判定逻辑留在服务端，AI 拿到的和人在界面上看到的是同一份事实。
//
// 认证走独立令牌（Authorization: Bearer <token>），不复用浏览器会话：
// 会话是给人用的、12 小时过期，AI 接入需要长期稳定的凭据且要能单独吊销。

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func rpcOK(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id, "result": result,
	})
}

func rpcErr(w http.ResponseWriter, id any, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	})
}

// toolText 按 MCP 约定把结果包成 content 数组。
func toolText(v any) map[string]any {
	b, _ := json.MarshalIndent(v, "", "  ")
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(b)}},
	}
}

func (s *Server) mcpHandler(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok = strings.TrimSpace(tok)
	if tok == "" {
		rpcErr(w, nil, -32001, "缺少 Authorization: Bearer <token>")
		return
	}
	scope, name, err := s.St.MCPScopeByToken(r.Context(), tok)
	if err != nil {
		// 🔴 过期要如实说是过期，不能笼统成「无效或已停用」——
		//    接入方拿到后者会去查是不是抄错了、是不是被吊销了，
		//    而实际只要找管理员续期。方向完全不同。
		// ⚠️ 只放行「过期」这一种细节：不存在/已吊销仍然笼统报，
		//    否则等于给试探令牌的人一个「这条存在但停用了」的确认。
		if errors.Is(err, store.ErrTokenExpired) {
			logx.Info("mcp", "token_expired", map[string]any{
				"prefix": tok[:min(13, len(tok))], "err": err.Error()})
			rpcErr(w, nil, -32001, err.Error())
			return
		}
		rpcErr(w, nil, -32001, "令牌无效或已停用")
		return
	}

	var req rpcReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		rpcErr(w, nil, -32700, "请求不是合法 JSON")
		return
	}

	switch req.Method {
	case "initialize":
		rpcOK(w, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ops-version", "version": "0.1.0"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusNoContent)
	case "tools/list":
		rpcOK(w, req.ID, map[string]any{"tools": mcpTools(scope.Role)})
	case "tools/call":
		var p struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"arguments"`
		}
		// 🔴 -32602 = Invalid params（JSON-RPC 标准码）。
		//    原来是 `_ =` 丢掉错误 —— 参数类型不对时那个字段静默变零值，
		//    调用方以为自己传了、实际没生效。
		if err := decodeArgs(req.Params, &p); err != nil {
			rpcErr(w, req.ID, -32602, err.Error())
			return
		}
		res, err := s.callTool(r.Context(), scope, p.Name, p.Args)
		logx.Info("mcp", "tool_call", map[string]any{
			"client": name, "tool": p.Name, "ok": err == nil})
		if err != nil {
			// 🔴 错误要说清是「授权不含」还是「真出错了」——
			//    AI 分不清的话会把权限问题当故障反复重试
			rpcErr(w, req.ID, -32000, err.Error())
			return
		}
		rpcOK(w, req.ID, toolText(res))
	default:
		rpcErr(w, req.ID, -32601, "不支持的方法: "+req.Method)
	}
}

// mcpTools 工具清单。
//
// 🔴 没有权限的工具**不出现在清单里**，而不是列出来再拒绝 ——
// 列出来会让 AI 反复尝试并把失败当成故障，最后给用户一个"系统有问题"的错误结论。
func mcpTools(role string) []map[string]any {
	all := []struct {
		perm auth.Perm
		def  map[string]any
	}{
		{auth.PermView, map[string]any{
			"name": "list_orgs",
			"description": "列出所有部署平台（我方 + 各客户平台），含各自的数据源类型、环境、最近一次采集的状态与时间。排查「为什么某列没数据」先看这个。" +
				// ⚠️ 结构说明必须写清楚：envs 曾经是 ["UAT","UAT","UAT"]（每个项目摊一行），
				//    AI 会读成"有 3 个 UAT 环境"。现在按环境去重并把项目列出来。
				"envs 的结构是 [{env, projects:[项目名…]}]：一个环境下可能有多个项目，" +
				"而**对账的一列 = 项目 × 环境**（列名形如 平台·项目/环境）。" +
				"其他工具的 env 参数只认环境名（如 UAT），项目要用 project 参数单独指定。",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		{auth.PermView, map[string]any{
			"name": "list_versions",
			"description": "列出某个平台某个环境下所有服务当前跑的版本。" +
				"不指定 project 时返回该平台该环境下**所有项目**的服务（跨项目全量）。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"org":     map[string]any{"type": "string", "description": "平台名，如 我方 / A平台"},
					"env":     map[string]any{"type": "string", "description": "环境，如 UAT / PROD"},
					"project": map[string]any{"type": "string", "description": "项目名。留空 = 该平台下所有项目"},
				},
				"required": []string{"org", "env"},
			},
		}},
		{auth.PermView, map[string]any{
			"name": "compare_versions",
			"description": "比对：给一组列（平台+环境的自由组合），返回每个服务的判定结果。" +
				"**没有基准列** —— 判定是横着比这几列彼此一不一样，不带方向，" +
				"说不了「谁落后谁」（跨平台是两个 Harbor、两条流水线，版本号本来就不可比）。" +
				"行结论五态：same/diff/missing/unknown/ignored，含义见返回体里的 verdict_scale。" +
				"每一格另有 state（version/missing/no_data/unversioned/conflict/ignored）说明这一格为什么能比或不能比。" +
				"默认只返回**有差异的**服务（only_diff=true）；summary 里给的是全量计数（按行），据此可知总共多少服务。" +
				"⚠️ 某列采集失败时该格 state=no_data，这表示「我们没看到」而不是「对方没部署」，不要当成缺失。" +
				"⚠️ 一个平台下可能有多个项目；不传 project 就是跨项目全量，" +
				"此时同名服务若版本不一致会标成 conflict（判不了），要精确结果请指定 project。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"columns": map[string]any{
						"type":        "array",
						"description": `参与对比的列，如 [{"org":"我方","env":"UAT"},{"org":"A平台","env":"PROD"}]`,
						"items": map[string]any{"type": "object", "properties": map[string]any{
							"org": map[string]any{"type": "string"},
							"env": map[string]any{"type": "string"},
						}},
					},
					"only_diff": map[string]any{"type": "boolean",
						"description": "只返回有差异的服务。**默认 true** —— 全量结果很大（实测 160 个服务三列 = 7 万余字符），" +
							"传 false 之前请先确认真的需要全量。"},
					"limit": map[string]any{"type": "integer",
						"description": "最多返回多少行，默认 200。超出会截断并在 truncated 字段里说明还差多少。"},
				},
				"required": []string{"columns"},
			},
		}},
		{auth.PermView, map[string]any{
			"name":        "get_service_version",
			"description": "查单个服务在所有平台所有环境上的版本横切，用于回答「这个服务各家分别是什么版本」。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"service": map[string]any{"type": "string", "description": "服务名（=镜像名最后一段）"},
				},
				"required": []string{"service"},
			},
		}},
		{auth.PermView, map[string]any{
			"name":        "list_changes",
			"description": "版本变更历史（谁在何时从哪个版本升到哪个版本，含回滚与上下线），用于回答「这个服务最近改过什么」。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"org":     map[string]any{"type": "string"},
					"service": map[string]any{"type": "string"},
					"limit":   map[string]any{"type": "integer", "description": "默认 50"},
				},
			},
		}},
	}

	out := []map[string]any{}
	for _, t := range all {
		if auth.Can(role, t.perm) {
			out = append(out, t.def)
		}
	}
	return out
}

func (s *Server) callTool(ctx context.Context, scope auth.Scope, name string, raw json.RawMessage) (any, error) {
	if !auth.Can(scope.Role, auth.PermView) {
		return nil, fmt.Errorf("该令牌的角色 %q 没有读取权限（这是授权限制，不是系统故障）", scope.Role)
	}

	insts, err := s.St.ListOrgs(ctx, false)
	if err != nil {
		return nil, err
	}
	byName := map[string]store.Org{}
	for _, in := range insts {
		if scope.CanSee(in.ID) {
			byName[in.Name] = in
		}
	}

	switch name {
	case "list_orgs":
		out := []map[string]any{}
		for _, in := range insts {
			if !scope.CanSee(in.ID) {
				continue
			}
			// 🔴 环境行的粒度是「项目 × 环境」，直接摊平会输出 ["UAT","UAT","UAT"] ——
			//    AI 会读成"这个平台有 3 个 UAT 环境"，而 list_versions /
			//    compare_versions 的 env 参数只认环境名，重复值对调用方毫无用处。
			// ⚠️ 既然项目是真实维度，就把它结构化地说出来，
			//    口径与 get_service_version 的列名（A公司·项目B/UAT）保持一致。
			projName := map[int64]string{}
			if ps, e := s.St.ListProjects(ctx, in.ID); e == nil {
				for _, pr := range ps {
					projName[pr.ID] = pr.Name
				}
			}
			seen := map[string]int{} // env → envs 里的下标
			envs := []map[string]any{}
			for _, e := range in.Envs {
				idx, ok := seen[e.Env]
				if !ok {
					seen[e.Env] = len(envs)
					envs = append(envs, map[string]any{"env": e.Env, "projects": []string{}})
					idx = len(envs) - 1
				}
				if n := projName[e.ProjectID]; n != "" {
					ps, _ := envs[idx]["projects"].([]string)
					envs[idx]["projects"] = append(ps, n)
				}
			}
			row := map[string]any{
				"name": in.Name, "provider": in.ProviderType,
				"is_self": in.IsSelf, "envs": envs,
				"sync_status": in.LastSyncStatus,
			}
			if in.LastSyncAt.Valid {
				row["last_sync_at"] = in.LastSyncAt.Time.Format(time.RFC3339)
			}
			if in.LastSyncStatus != "success" && in.LastSyncError != "" {
				row["sync_error"] = in.LastSyncError
			}
			out = append(out, row)
		}
		return out, nil

	case "list_versions":
		var p struct{ Org, Env, Project string }
		if err := decodeArgs(raw, &p); err != nil {
			return nil, err
		}
		in, okk := byName[p.Org]
		if !okk {
			return nil, fmt.Errorf("找不到平台 %q（可能不存在，或该令牌的数据范围看不到它）", p.Org)
		}
		col := columnOf(in, p.Env)
		// 🔴 项目名写错时**报错**，不能静默当成「不筛」——
		//    静默的话 AI 会拿一份跨项目全量当成某个项目的清单往下推理。
		if err := applyProjectFilter(ctx, s.St, &col, p.Project); err != nil {
			return nil, err
		}
		if !col.Healthy() {
			// 🔴 明确告诉 AI 这是「拿不到数据」而不是「没有服务」
			return map[string]any{
				"org": in.Name, "env": p.Env,
				"available": false,
				"reason":    fmt.Sprintf("该平台最近一次采集状态为 %s：%s", in.LastSyncStatus, in.LastSyncError),
				"note":      "这表示我们没能读到数据，不代表对方没有部署服务。请不要据此判断服务缺失。",
			}, nil
		}
		data, err := s.St.LoadSnapshots(ctx, []compare.Column{col})
		if err != nil {
			return nil, err
		}
		list := data[col.Key()]
		sort.Slice(list, func(i, j int) bool { return list[i].ServiceKey < list[j].ServiceKey })
		return map[string]any{"org": in.Name, "env": p.Env, "project": projectNote(p.Project),
			"available": true, "count": len(list), "services": list}, nil

	case "compare_versions":
		var p struct {
			Columns []struct{ Org, Env, Project string } `json:"columns"`
			// 🔴 baseline_org / baseline_env 已删。判定不再有基准 ——
			//    留着"收下但不读"最坏：老调用方照旧传，服务端静默丢弃，
			//    它以为自己指定了基准，而结果完全是另一套语义且不报错。
			// 🔴 用指针：要区分「没传」和「显式传了 false」。
			//    用 bool 的话零值就是 false，改不了默认值 ——
			//    而这条问题的核心正是「默认值是反的」。
			OnlyDif *bool `json:"only_diff"`
			// Limit 最多返回多少行。0 = 用默认上限。
			Limit int `json:"limit"`
		}
		if err := decodeArgs(raw, &p); err != nil {
			return nil, err
		}
		if len(p.Columns) < 2 {
			return nil, fmt.Errorf("至少要两列才能对比")
		}
		plan := compare.Plan{}
		for _, c := range p.Columns {
			in, okk := byName[c.Org]
			if !okk {
				return nil, fmt.Errorf("找不到平台 %q", c.Org)
			}
			col := columnOf(in, c.Env)
			if err := applyProjectFilter(ctx, s.St, &col, c.Project); err != nil {
				return nil, err
			}
			plan.Columns = append(plan.Columns, col)
		}
		data, err := s.St.LoadSnapshots(ctx, plan.Columns)
		if err != nil {
			return nil, err
		}
		res := compare.Compare(plan, data)

		// 🔴 默认只回有差异的。
		//
		//    这个工具是给 AI 用的，而上下文是 AI 最稀缺的资源。
		//    实测：生产三列 160 个服务，全量返回 73,025 字符 / 3,210 行，
		//    第一次按最自然的方式调用（不带可选参数）就直接超出单次结果上限。
		//    「有差异的才值得看」在这个场景下是唯一合理的默认。
		onlyDiff := true
		if p.OnlyDif != nil {
			onlyDiff = *p.OnlyDif
		}
		limit := mcpRowLimit
		if p.Limit > 0 && p.Limit < mcpRowLimit {
			limit = p.Limit
		}

		rows, truncated := mcpRows(res, onlyDiff, limit)
		summary := map[string]int{}
		for k, v := range res.Summary {
			summary[string(k)] = v
		}
		out := map[string]any{
			"summary": summary, "rows": rows,
			// 🔴 口径必须跟着结果一起给 AI。
			//
			//    判定是**无基准**的：只说"这几列彼此一不一样"，说不了"谁落后谁"。
			//    不写清楚的话，AI 会按常识把 diff 解释成"落后"并给出方向，
			//    而这张表可能是别的两个平台之间的对账，我方根本不在里面。
			"verdict_scale": map[string]string{
				"same":    "这几列的 tag 完全相同",
				"diff":    "这几列都有，但 tag 不全相同。⚠️ 不含方向 —— 跨平台是两个 Harbor、两条流水线，版本号不可比，说不了谁新谁旧",
				"missing": "至少有一列确实没有这个服务",
				"unknown": "至少有一列没法比：整列采集失败 / 非版本化 tag / 同名冲突",
				"ignored": "整行被人为忽略，主动不比",
			},
			// 🔴 把「这次筛过」写进返回体。
			//    summary 给的是**全量**计数（一致 160），rows 给的是筛后的（可能是空）——
			//    不说清楚的话，AI 看到 summary.same=160 而 rows=[] 会自己编一个解释。
			//    这也是 only_diff=true 时仍然保留 summary 的原因：
			//    没有它，「我们没采到数据」会被读成「两边完全一致」，结论正好相反。
			"only_diff": onlyDiff,
		}
		if truncated > 0 {
			out["truncated"] = truncated
			out["truncated_note"] = fmt.Sprintf(
				"结果超过 %d 行，还有 %d 行没有返回。这**不是**全部差异 —— "+
					"要看完请缩小 columns 范围，或按 summary 里的判定分类逐项查。",
				limit, truncated)
		}
		if len(res.UnhealthyColumns) > 0 {
			bad := []string{}
			for _, c := range res.UnhealthyColumns {
				bad = append(bad, c.Key()+"("+c.SyncStatus+")")
			}
			// 🔴 这条必须显眼：整列拿不到数据时，对账结论是不完整的。
			//    AI 若不知道这点，会把残缺的结果当成完整结论汇报给用户
			out["warning"] = "以下列采集失败，其判定为 no_data，本次比对结论不完整：" +
				strings.Join(bad, ", ")
		}
		return out, nil

	case "get_service_version":
		var p struct {
			Service string `json:"service"`
		}
		if err := decodeArgs(raw, &p); err != nil {
			return nil, err
		}
		// 🔴 每一列必须带上**项目**，否则同一个平台的多个项目会产出
		//    完全相同的 Key()（"A公司/UAT"），后果是双重重复：
		//      ① LoadSnapshots 按 Key 建 map，后面的查询结果覆盖前面的
		//      ② 外层遍历 cols 时，每个同名 Column 都从同一份数据里再取一遍
		//
		//    实测过（2026-08-21）：A公司 有 3 个项目、我方 有 2 个，
		//    查一个只部署在一处的服务，返回 **11 条**（A公司/UAT × 9 + 我方/UAT × 2）——
		//    AI 会以为这个服务有 11 个部署。
		var cols []compare.Column
		for _, in := range insts {
			if !scope.CanSee(in.ID) {
				continue
			}
			projName := map[int64]string{}
			// 🔴 连**服务过滤规则**一起取，不能只取名字。
			//    只设 ProjectID/ProjectName 而不设 Filter 时，零值 ProjectFilter 的
			//    Empty() 为真、Matches() 恒返回 true —— 项目的 service_include
			//    形同虚设，于是 A公司·项目B（只收 biz-*）会返回 central-frontend
			//    这种根本不属于它的服务。
			// ⚠️ 与 buildPlan 里那段是同一份规则，两处必须一致；
			//    只在一处生效的话，界面和 MCP 会对同一个问题给出不同答案。
			projFilter := map[int64]compare.ProjectFilter{}
			if ps, e := s.St.ListProjects(ctx, in.ID); e == nil {
				for _, pr := range ps {
					projName[pr.ID] = pr.Name
					projFilter[pr.ID] = compare.ProjectFilter{
						Include: pr.ServiceInclude, Pins: pr.ServicePins,
					}
				}
			}
			for _, e := range in.Envs {
				col := columnOf(in, e.Env)
				col.ProjectID = e.ProjectID
				col.ProjectName = projName[e.ProjectID]
				col.Filter = projFilter[e.ProjectID]
				cols = append(cols, col)
			}
		}
		data, err := s.St.LoadSnapshots(ctx, cols)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, c := range cols {
			if !c.Healthy() {
				out = append(out, map[string]any{
					"column": c.Key(), "available": false, "reason": c.SyncStatus})
				continue
			}
			for _, sp := range data[c.Key()] {
				if sp.ServiceKey == p.Service {
					m := map[string]any{"column": c.Key(), "available": true,
						"tag": sp.Tag, "namespace": sp.Namespace}
					if sp.RunningTag != "" && sp.RunningTag != sp.Tag {
						m["deploying"] = true
						m["running_tag"] = sp.RunningTag
					}
					if sp.HasConflict {
						m["conflict"] = true
					}
					out = append(out, m)
				}
			}
		}
		return map[string]any{"service": p.Service, "found_in": len(out), "columns": out}, nil

	case "list_changes":
		var p struct {
			Org     string `json:"org"`
			Service string `json:"service"`
			Limit   int    `json:"limit"`
		}
		if err := decodeArgs(raw, &p); err != nil {
			return nil, err
		}
		var iid int64
		if p.Org != "" {
			in, okk := byName[p.Org]
			if !okk {
				return nil, fmt.Errorf("找不到平台 %q", p.Org)
			}
			iid = in.ID
		}
		if p.Limit == 0 {
			p.Limit = 50
		}
		return s.St.ListChanges(ctx, iid, p.Service, p.Limit)

	default:
		return nil, fmt.Errorf("未知工具 %q", name)
	}
}

// columnOf 构造一列。
//
// 🔴 与 HTTP 那侧**同一套口径**：取环境级采集状态，没有才回落平台级。
// 两处各写一遍的话，AI 通过 MCP 看到的状态会和界面上的不一致 ——
// 而"AI 说没问题、界面上标着采集失败"这种矛盾最难取信于人。
func columnOf(in store.Org, env string) compare.Column {
	c := compare.Column{OrgID: in.ID, OrgName: in.Name, Env: env, IsSelf: in.IsSelf}
	if e, found := envOf(in, env, 0); found && e.LastCollectStatus != "" {
		c.SyncStatus, c.SyncError = e.LastCollectStatus, e.LastCollectError
		if e.LastCollectAt.Valid {
			c.SyncedAt = e.LastCollectAt.Time
		}
		return c
	}
	c.SyncStatus, c.SyncError = in.LastSyncStatus, in.LastSyncError
	if in.LastSyncAt.Valid {
		c.SyncedAt = in.LastSyncAt.Time
	}
	return c
}

// HashToken 令牌哈希。只存哈希，明文只在创建时返回一次。
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// createMCPToken 发一条 MCP 令牌。明文只在这里返回一次，之后任何接口都拿不到。
func (s *Server) createMCPToken(w http.ResponseWriter, r *http.Request) {
	req, err := body[struct {
		Name        string `json:"name"`
		Role        string `json:"role"`
		VisibleOrgs string `json:"visible_orgs"`
		// Days 有效期天数。
		//
		// 🔴 用指针：要区分「没传」和「显式传 0（永不过期）」。
		//    用 int 的话零值就是 0 = 永久 —— 于是所有老调用点发出来的令牌
		//    仍然是永不过期的，而这正是 本身。
		Days *int `json:"days"`
	}](r)
	if err != nil || req.Name == "" {
		fail(w, http.StatusBadRequest, "bad_request", "name 必填")
		return
	}
	if req.Role == "" {
		req.Role = auth.RoleViewer // 默认最小权限，不默认给 admin
	}
	days := store.DefaultMCPTokenDays
	if req.Days != nil {
		days = *req.Days
	}
	plain, err := s.St.CreateMCPToken(r.Context(), req.Name, req.Role,
		req.VisibleOrgs, userOf(r).Username, days)
	// 审计里绝不能出现令牌明文，只记发给了谁、什么角色、有效期多久
	s.St.Audit(r.Context(), userOf(r).Username, "mcp_token.create", req.Name,
		map[string]any{"role": req.Role, "days": days}, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ok(w, map[string]any{
		"token": plain,
		"note":  "请立刻保存：明文只显示这一次，之后无法再取回。",
	})
}

// mcpRowLimit MCP 单次返回的最大行数。
//
// 🔴 存在的理由：这个工具是给 AI 用的，而上下文是它最稀缺的资源。
// 实测生产三列 160 个服务全量返回 73,025 字符 —— 一次调用就撑爆。
// 200 行的依据：差异行通常远少于这个数；真到 200 行还没看完，
// 说明该缩小范围了，而不是该返回更多。
const mcpRowLimit = 200

// mcpRows 把比对结果组装成 MCP 返回的行。
//
// 抽成纯函数是为了能**脱离数据库**验证两件事：
//   - 默认只回有差异的
//   - 截断要数出来并报出去，不能悄悄少给几行
//
// 🔴 截断**不能悄悄发生**：AI 拿到一份不完整的清单却以为是全部，
// 会直接据此下结论 —— 而"少了几行"从结果本身完全看不出来。
func mcpRows(res compare.Result, onlyDiff bool, limit int) ([]map[string]any, int) {
	rows := []map[string]any{}
	truncated := 0
	for _, row := range res.Rows {
		if onlyDiff && !row.HasDiff {
			continue
		}
		if len(rows) >= limit {
			truncated++
			continue
		}
		cells := []map[string]any{}
		for _, c := range row.Cells {
			m := map[string]any{"column": c.Column.Key(), "state": string(c.State)}
			if c.Snap != nil {
				m["tag"] = c.Snap.Tag
			}
			if c.Note != "" {
				m["note"] = c.Note
			}
			if c.Deploying {
				m["deploying"] = true
			}
			cells = append(cells, m)
		}
		// 🔴 行结论要给出来。AI 自己按 cells 去推的话，
		//    它推的那套顺序和我们的不一样（缺失 > 不一致 > 无法判定 > 一致），
		//    而这个顺序是被真实数据推翻过两次才定下来的。
		rows = append(rows, map[string]any{
			"service": row.ServiceKey, "verdict": string(row.Verdict), "cells": cells})
	}
	return rows, truncated
}

// setMCPTokenExpiry 给一条已发出的令牌设置有效期。
//
// 🔴 之前发的令牌全是永不过期，而它们已经在外部接入方手里。
// 只让新令牌能设有效期的话，那批永久令牌会一直存在 —— 问题只解决一半。
func (s *Server) setMCPTokenExpiry(w http.ResponseWriter, r *http.Request) {
	req, err := body[struct {
		Days *int `json:"days"`
	}](r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	days := store.DefaultMCPTokenDays
	if req.Days != nil {
		days = *req.Days
	}
	if err := s.St.SetMCPTokenExpiry(r.Context(), id, days); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	s.St.Audit(r.Context(), userOf(r).Username, "mcp_token.set_expiry",
		strconv.FormatInt(id, 10), map[string]any{"days": days}, nil, clientIP(r))
	ok(w, map[string]any{"ok": true, "days": days})
}

// decodeArgs 解析工具参数，**类型不对就报错**。
//
// 🔴 原来是 `_ = json.Unmarshal(raw, &p)` —— 错误直接丢掉。
//
//	Go 的行为是：类型不匹配时那个字段保持零值、返回 error，
//	而**其余字段照常解析成功**。于是错误被吞之后，
//	调用方看到的是「大部分参数生效了，就那一个没生效」。
//
// 实测过（2026-08-21）：MCP 客户端把 limit 传成字符串 "1"，
// 服务端 p.Limit 静默变 0 → 走默认上限 200 →
// **AI 传 limit=1 拿回 71 行**，而工具描述里明明警告过
// 「全量结果 7 万余字符会撑爆上下文」。columns 和 only_diff 都好好的，
// 唯独 limit 不声不响地没了。
//
// ⚠️ 这类"参数被静默忽略"比直接报错危险得多：报错了调用方会改，
//
//	静默忽略则是它以为自己已经限制了，然后拿着全量继续往下走。
func decodeArgs(raw []byte, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		// 🔴 把**哪个参数、要什么类型**说清楚。
		//    只回 "invalid arguments" 的话，调用方得自己一个个试。
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return fmt.Errorf(
				"参数 %q 类型不对：收到 %s，需要 %s。"+
					"⚠️ 数字参数要传数字，不能传字符串（如 limit: 1，不是 limit: \"1\"）",
				te.Field, te.Value, te.Type)
		}
		return fmt.Errorf("参数解析失败：%w", err)
	}
	return nil
}
