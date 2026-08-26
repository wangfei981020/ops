package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ops-version-backend/internal/compare"
	"ops-version-backend/internal/export"
	"ops-version-backend/logx"
	"ops-version-backend/providers"
)

// exportHandler 导出 xlsx。
//
// 🔴 导出一律记审计。这份文件会**离开系统**：转发给同事、发进群、发给客户。
// 事后要能回答「这份表是谁在什么时候导的、含哪几个平台」——
// 不记的话，一份泄露出去的对账表根本查不到源头。
func (s *Server) exportHandler(w http.ResponseWriter, r *http.Request) {
	req, err := body[compareReq](r)
	if err != nil || len(req.Columns) < 2 {
		fail(w, http.StatusBadRequest, "bad_request", "至少要选两列才能导出")
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

	// 🔴 only_diff 在导出侧同样要生效，而且必须写进「数据说明」页。
	//    界面 71 行、导出 114 行、说明页只字不提 —— 收到附件的人无从判断
	//    手里这份是全量还是筛过的。
	// ⚠️ 只过滤 Rows，Summary 保持全量：它回答的是「总共多少服务」。
	filterNote := req.FilterNote
	if req.OnlyDiff {
		res.Rows = onlyDiffRows(res.Rows)
		if strings.TrimSpace(filterNote) == "" {
			// 前端会把筛选说明一并传来；这里兜住直接调 API 的客户端，
			// 保证「筛过的导出」在任何调用路径下都不会不声不响。
			filterNote = "只含有差异的行"
		}
	}

	// Pod 明细按列取。某一列取不到不该让整个导出失败 ——
	// 那一页会写明「没有明细」，而对账矩阵本身仍然是完整的
	pods := map[string][]providers.PodInfo{}
	for _, c := range plan.Columns {
		if !c.Healthy() {
			continue
		}
		list, err := s.St.ListPods(r.Context(), c.OrgID, c.ProjectID, c.Env)
		if err != nil {
			logx.Warn("export", "load_pods_failed", map[string]any{
				"col": c.Key(), "err": err.Error()})
			continue
		}
		pods[c.Key()] = list
	}

	now := time.Now()
	blob, err := export.Build(export.Input{
		Result:     res,
		Plan:       plan,
		PlanName:   orDefault(req.PlanName, "未保存的组合"),
		Pods:       pods,
		Operator:   userOf(r).Username,
		FilterNote: filterNote,
		Now:        now,
	})
	if err != nil {
		s.St.Audit(r.Context(), userOf(r).Username, "recon.export", planTarget(plan), nil, err, clientIP(r))
		fail(w, http.StatusInternalServerError, "internal", "生成 Excel 失败: "+err.Error())
		return
	}

	cols := make([]string, 0, len(plan.Columns))
	for _, c := range plan.Columns {
		cols = append(cols, c.Key())
	}
	s.St.Audit(r.Context(), userOf(r).Username, "recon.export", planTarget(plan),
		map[string]any{"columns": cols, "rows": len(res.Rows), "bytes": len(blob)}, nil, clientIP(r))

	name := fmt.Sprintf("版本比对_%s.xlsx", now.Format("20060102_150405"))
	// 🔴 文件名含中文，必须同时给 filename 和 filename*：
	//    只给 filename 时中文在部分浏览器上会变成乱码或被截断；
	//    只给 filename* 时老浏览器不认，下载下来叫 "download"。
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="export.xlsx"; filename*=UTF-8''%s`, url.PathEscape(name)))
	w.Header().Set("Content-Type",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Length", fmt.Sprint(len(blob)))
	_, _ = w.Write(blob)

	logx.Info("export", "done", map[string]any{
		"user": userOf(r).Username, "rows": len(res.Rows),
		"cols": len(plan.Columns), "kb": len(blob) / 1024})
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// exportInventory 导出**单个环境**的版本清单。
//
// 🔴 与对账表分开的入口，不是"让对账表支持一列"：
// 一列没有基准、没有落差、没有归因 —— 硬塞进对账表会导出一整套永远是空的
// 判定列，收到的人会以为"所有服务都没差异"，而实际上根本没比过。
func (s *Server) exportInventory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrgID int64  `json:"org_id"`
		Env   string `json:"env"`
		// ProjectID 🔴 必须有：一列 = 项目 × 环境。少了它，
		//    平台页上三个项目各一个「导出清单」链接，点哪个导出的都是
		//    **同一份跨项目全量** —— 视觉上是三份不同的清单，实际是一份，
		//    而收到附件的人无从分辨。
		// ⚠️ 还有个连带后果：ProjectID==0 会走「不限项目」分支，
		//    导出的清单里会冒出跨项目同名服务的 conflict 标记。
		ProjectID int64 `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrgID == 0 || req.Env == "" {
		fail(w, http.StatusBadRequest, "bad_request", "要指定平台与环境")
		return
	}
	in, err := s.St.GetOrg(r.Context(), req.OrgID)
	if err != nil {
		fail(w, http.StatusNotFound, "not_found", "平台不存在")
		return
	}
	if !userOf(r).Scope().CanSee(in.ID) {
		fail(w, http.StatusForbidden, "forbidden", "没有权限查看平台 "+in.Name)
		return
	}

	// 🔴 先确认这个环境**存在**。
	//    不校验的话，columnOf 对不存在的环境会回落到平台级状态（多半是 success），
	//    于是 Healthy() 为真、查快照查出空集、**导出一张空表** ——
	//    而空表跟"这个环境什么都没部署"长得一模一样。
	//    （环境名靠人手填，写错一个字母就撞上这条路径。）
	if _, found := envOf(in, req.Env, req.ProjectID); !found {
		fail(w, http.StatusBadRequest, "bad_request",
			"平台 "+in.Name+" 没有名为 "+req.Env+" 的环境")
		return
	}
	col := columnOf(in, req.Env)
	// 带上项目维度与它的服务过滤规则。
	// ⚠️ 只设 ProjectID 而不设 Filter 是不够的：零值 ProjectFilter 的 Matches()
	//    恒为真，service_include 不生效 —— 与 同一个坑。
	if req.ProjectID != 0 {
		col.ProjectID = req.ProjectID
		if ps, e := s.St.ListProjects(r.Context(), in.ID); e == nil {
			for _, pr := range ps {
				if pr.ID == req.ProjectID {
					col.ProjectName = pr.Name
					col.Filter = compare.ProjectFilter{Include: pr.ServiceInclude, Pins: pr.ServicePins}
					break
				}
			}
		}
	}
	// ⚠️ 采集失败的列**不导**：LoadSnapshots 对不健康的列返回 nil，
	//    真导出去就是一张空表，而空表跟"这个环境什么都没部署"分不出来。
	if !col.Healthy() {
		fail(w, http.StatusBadRequest, "stale_data",
			"这一列上次采集失败（"+col.SyncStatus+"），先刷新成功再导出 —— "+
				"否则导出的是一张空表，看起来像这个环境什么都没部署")
		return
	}
	data, err := s.St.LoadSnapshots(r.Context(), []compare.Column{col})
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	list := data[col.Key()]

	now := time.Now()
	observed := col.SyncedAt
	if len(list) > 0 && !list[0].ObservedAt.IsZero() {
		// 数据本身的时刻比"上次采集尝试"更准确
		observed = list[0].ObservedAt
	}
	// ⚠️ 平台名要带项目：一个平台三个项目导出三份清单，
	//    表内标题和文件名都只写「A公司」的话，三份东西完全分不清 ——
	//    数据修对了，交付物仍然是混的。
	who := in.Name
	if col.ProjectName != "" {
		who = in.Name + "·" + col.ProjectName
	}
	blob, err := export.BuildInventory(export.InventoryInput{
		OrgName: who, Env: req.Env, Services: list,
		ObservedAt: observed, Operator: userOf(r).Username, Now: now,
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	logx.Info("export", "inventory", map[string]any{
		"org": in.Name, "env": req.Env, "rows": len(list), "user": userOf(r).Username})
	s.St.Audit(r.Context(), userOf(r).Username, "export.inventory", col.Key(),
		map[string]any{"rows": len(list), "project_id": req.ProjectID}, nil, clientIP(r))

	// 🔴 文件名含中文，必须同时给 filename 和 filename*（理由见上面的对账表导出）
	name := fmt.Sprintf("版本清单_%s_%s_%s.xlsx", who, req.Env, now.Format("20060102_1504"))
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="inventory.xlsx"; filename*=UTF-8''%s`, url.PathEscape(name)))
	w.Header().Set("Content-Type",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Length", fmt.Sprint(len(blob)))
	_, _ = w.Write(blob)
}

// planTarget 审计里记这次比对的对象。
//
// 🔴 原来记的是基准列。没有基准之后记**参与的列**——
// 审计要能回答"谁在什么时候比了哪几个平台"，只记一列本来就不够。
func planTarget(plan compare.Plan) string {
	keys := make([]string, 0, len(plan.Columns))
	for _, c := range plan.Columns {
		keys = append(keys, c.Key())
	}
	return strings.Join(keys, " vs ")
}
