package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
	"ops-version-backend/providers"
)

// harborOf 按 id 取一个 Harbor 客户端（含解密后的凭据）。
func (s *Server) harborOf(r *http.Request, id int64) (*providers.Harbor, *store.Harbor, error) {
	list, err := s.St.ListHarbors(r.Context())
	if err != nil {
		return nil, nil, err
	}
	for i := range list {
		if list[i].ID != id {
			continue
		}
		h := list[i]
		pw := ""
		if h.CredentialEnc != "" {
			if v, e := s.Ciph.Decrypt(h.CredentialEnc); e == nil {
				pw = v
			}
		}
		return &providers.Harbor{
			Endpoint: h.Endpoint, Username: h.Username, Password: pw, InsecureTLS: h.InsecureTLS,
		}, &h, nil
	}
	return nil, nil, nil
}

// checkReq 「和我方 Harbor 逐版本比对」的入参。
type checkReq struct {
	HarborID int64 `json:"harbor_id"`
	// Project 我方 Harbor 里的项目名。**可选** —— 空着时从
	// service_versions.image_repo 推（见 ProjectOfService）。
	Project string `json:"project"`
	// OrgID 只看往这个平台的复制记录。0 = 看全部平台的。
	OrgID    int64    `json:"org_id"`
	Services []string `json:"services"`
	// TagLimit 每个服务最多看几个版本（Harbor 按推送时间倒序）。
	TagLimit int `json:"tag_limit"`
}

// TagCheck 一个版本推没推过去。
type TagCheck struct {
	Tag      string `json:"tag"`
	PushedAt string `json:"pushed_at"`
	// State: synced / not_synced / sync_failed
	State string `json:"state"`
	Note  string `json:"note"`
	// ErrMsg 同步失败时对方给的原话。空串表示没有失败。
	ErrMsg string `json:"err_msg"`
}

// ServiceCheck 一个服务的比对结果。
type ServiceCheck struct {
	ServiceKey string `json:"service_key"`
	FullName   string `json:"full_name"`
	// 🔴 Tags 必须初始化成空切片，不能留 nil ——
	//    nil 序列化成 JSON null，前端 `.tags.map` 当场炸整个弹窗。
	//    这个形状栽过两次，check-nullable-arrays.mjs 会核对。
	Tags []TagCheck `json:"tags"`
	// 🔴 Err 与「Tags 为空」是两回事：
	//    前者是「我们没查成 / 这个服务不存在」，后者是「确实没有版本」。
	//    合成一个的话，名字打错会显示成「这个服务从没构建过」。
	Err string `json:"err"`
}

func (s *Server) checkImages(w http.ResponseWriter, r *http.Request) {
	req, err := body[checkReq](r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	// 🔴 project 不再必填 —— 能从我方快照的 image_repo 推出来就别问人。
	//
	//    原来必须先选项目：一个 Harbor 十几个项目、每个几百个仓库，
	//    人不知道自己要查的服务在哪个下面，选错了下拉里就没有它。
	//    而 image_repo 形如 `appA/wallet-backend`，答案本来就在库里。
	//
	// ⚠️ 推不出来时**说清是哪个服务推不出来**，而不是笼统一句「project 必填」——
	//    前者能直接照做（去看那个服务采到没有），后者只能去猜。
	if strings.TrimSpace(req.Project) == "" {
		if len(req.Services) == 0 {
			fail(w, http.StatusBadRequest, "bad_request", "至少要选一个服务")
			return
		}
		p, e := s.St.ProjectOfService(r.Context(), req.Services[0])
		if e != nil || p == "" {
			fail(w, http.StatusBadRequest, "bad_request",
				"推断不出服务 "+req.Services[0]+" 在我方 Harbor 的哪个项目下 —— "+
					"我方平台的快照里没有它（可能还没采集过，或它只在对方那边有）。"+
					"可以在请求里显式指定 project。")
			return
		}
		req.Project = p
	}
	// 🔴 不允许「不选服务就查全部」：一个项目可能有几百个仓库，
	//    每个都要单独打一次 artifacts 接口 —— 会把 Harbor 打爆，
	//    而人真正关心的从来只有那么几个。宁可逼他选。
	if len(req.Services) == 0 {
		fail(w, http.StatusBadRequest, "bad_request", "至少要选一个服务")
		return
	}
	if len(req.Services) > 20 {
		fail(w, http.StatusBadRequest, "bad_request", "一次最多查 20 个服务")
		return
	}

	// 🔴 harbor_id 同样可选 —— 从这个服务的复制记录推。
	//    界面上常选「全部规则」，那时前端手上根本没有 harbor_id；
	//    逼它先选一个 Harbor 等于把刚去掉的前置步骤又加回来。
	if req.HarborID == 0 {
		hid, e := s.St.HarborOfService(r.Context(), req.Services[0])
		if e != nil || hid == 0 {
			fail(w, http.StatusBadRequest, "bad_request",
				"推断不出该用哪个 Harbor 查服务 "+req.Services[0]+" —— "+
					"复制记录里没有它（可能还没拉取过复制记录）。"+
					"可以在请求里显式指定 harbor_id。")
			return
		}
		req.HarborID = hid
	}
	hb, _, err := s.harborOf(r, req.HarborID)
	if err != nil || hb == nil {
		fail(w, http.StatusNotFound, "not_found",
			"Harbor 不存在（id="+strconv.FormatInt(req.HarborID, 10)+"）")
		return
	}

	// 复制记录：service_key\x00tag → 结果
	facts := map[string]store.SyncFact{}
	if req.OrgID > 0 {
		m, e := s.St.SyncFactsOf(r.Context(), req.OrgID)
		if e != nil {
			logx.Warn("imagecheck", "sync_facts_failed", map[string]any{"err": e.Error()})
		} else {
			facts = m
		}
	} else {
		m, e := s.St.AllSyncFacts(r.Context())
		if e != nil {
			logx.Warn("imagecheck", "all_sync_facts_failed", map[string]any{"err": e.Error()})
		} else {
			facts = m
		}
	}

	// 🔴 先取仓库清单来校验服务名是否真的存在。
	//
	// 因为 **Harbor 对不存在的仓库返回 HTTP 200 + 空数组**，不是 404 ——
	// 不校验的话，「服务名打错了」和「这个服务确实没有任何版本」
	// 在界面上长得一模一样（都是 0 个版本），人会以为这个服务从没构建过。
	// 这是全站「查不到不能显示成事实是否定的」在这里的那一份。
	known := map[string]bool{}
	repos, rerr := hb.Repositories(r.Context(), req.Project)
	if rerr != nil {
		// 校验用的清单都取不到时**不能假装校验过了**，
		// 后面统一标注「无法确认该服务是否存在」
		logx.Warn("imagecheck", "repos_failed", map[string]any{
			"project": req.Project, "err": rerr.Error()})
	}
	for _, rp := range repos {
		known[rp.ServiceKey] = true
	}

	out := make([]ServiceCheck, 0, len(req.Services))
	for _, svc := range req.Services {
		svc = strings.TrimSpace(svc)
		if svc == "" {
			continue
		}
		sc := ServiceCheck{ServiceKey: svc, FullName: req.Project + "/" + svc, Tags: []TagCheck{}}
		if rerr != nil {
			sc.Err = "无法确认该服务是否存在（取仓库清单失败：" + rerr.Error() + "）"
			out = append(out, sc)
			continue
		}
		if !known[svc] {
			sc.Err = "该项目里没有这个服务 —— 名字可能打错了，或者它在别的项目下"
			out = append(out, sc)
			continue
		}
		tags, e := hb.Tags(r.Context(), req.Project, svc, req.TagLimit)
		if e != nil {
			// 一个服务查失败不该让整次请求失败 —— 其余服务的结果仍然有价值
			sc.Err = e.Error()
			out = append(out, sc)
			continue
		}
		if len(tags) == 0 {
			// 仓库存在但没有带 tag 的制品。这是**事实**（比如制品都被覆盖过），
			// 与上面那个「服务不存在」是两回事，措辞要能分开
			sc.Err = "仓库存在，但没有任何带 tag 的版本"
		}
		for _, t := range tags {
			tc := TagCheck{Tag: t.Tag, State: "not_synced",
				Note: "复制记录里没有这个版本"}
			if !t.PushedAt.IsZero() {
				tc.PushedAt = t.PushedAt.Format("2006-01-02 15:04")
			}
			if f, hit := facts[svc+"\x00"+t.Tag]; hit {
				switch {
				case isFailedStatus(f.Status):
					tc.State, tc.Note, tc.ErrMsg = "sync_failed", "同步任务失败", f.ErrMsg
				case isOKStatus(f.Status):
					tc.State, tc.Note = "synced", "复制记录里有成功任务"
					if f.FinishedAt != nil {
						tc.Note = "已于 " + f.FinishedAt.Format("2006-01-02 15:04") + " 同步"
					}
				default:
					tc.State, tc.Note = "not_synced", "同步进行中或状态未知（"+f.Status+"）"
				}
			}
			sc.Tags = append(sc.Tags, tc)
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServiceKey < out[j].ServiceKey })

	s.St.Audit(r.Context(), userOf(r).Username, "image.check", req.Project,
		map[string]any{"services": len(req.Services)}, nil, clientIP(r))
	ok(w, out)
}

func isOKStatus(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "succeed", "succeeded", "success":
		return true
	}
	return false
}

func isFailedStatus(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "failed", "failure", "error":
		return true
	}
	return false
}
