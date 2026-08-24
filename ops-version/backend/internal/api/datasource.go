package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ops-version-backend/internal/store"
)

// ─────────────── 数据源 ───────────────

func (s *Server) listDatasources(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListDatasources(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// ⚠️ 逐个抹掉密文再回：读接口永不回显凭据，只给 has_credential
	for i := range list {
		list[i].CredentialEnc = ""
	}
	ok(w, list)
}

type datasourceReq struct {
	Name         string `json:"name"`
	ProviderType string `json:"provider_type"`
	Endpoint     string `json:"endpoint"`
	AuthType     string `json:"auth_type"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	APIKey       string `json:"api_key"`
	InsecureTLS  bool   `json:"insecure_tls"`
	Enabled      *bool  `json:"enabled"`
}

func (s *Server) saveDatasource(w http.ResponseWriter, r *http.Request) {
	var req datasourceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.ProviderType) == "" {
		fail(w, http.StatusBadRequest, "bad_request", "名称和类型必填")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)

	// 凭据三者全空 = 不改动已有的（与平台/环境同一条规矩，见 encCred）
	cred, err := s.encCred(req.Username, req.Password, req.APIKey, req.InsecureTLS)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", "凭据加密失败")
		return
	}
	d := store.Datasource{
		Name: req.Name, ProviderType: req.ProviderType, Endpoint: req.Endpoint,
		AuthType: req.AuthType, CredentialEnc: cred, InsecureTLS: req.InsecureTLS,
		Enabled: req.Enabled == nil || *req.Enabled,
	}
	newID, err := s.St.SaveDatasource(r.Context(), id, d)
	s.St.Audit(r.Context(), userOf(r).Username, "datasource.save", req.Name, nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"id": newID})
}

func (s *Server) deleteDatasource(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := s.St.DeleteDatasource(r.Context(), id)
	s.St.Audit(r.Context(), userOf(r).Username, "datasource.delete", strconv.FormatInt(id, 10), nil, err, clientIP(r))
	if errors.Is(err, store.ErrInUse) {
		// 🔴 409 而不是 500：这是用户能自己解决的情况。
		//    报 500 会让人以为系统坏了，然后去翻日志找不到任何异常。
		fail(w, http.StatusConflict, "in_use",
			"还有平台在用这个数据源 —— 先把那些平台改到别的数据源，或者停用它们")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

// ─────────────── 项目 ───────────────

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	orgID, _ := strconv.ParseInt(r.URL.Query().Get("org_id"), 10, 64)
	list, err := s.St.ListProjects(r.Context(), orgID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}

func (s *Server) saveProject(w http.ResponseWriter, r *http.Request) {
	var p store.Project
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	if strings.TrimSpace(p.Name) == "" || p.OrgID == 0 {
		fail(w, http.StatusBadRequest, "bad_request", "项目名和所属平台必填")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	newID, err := s.St.SaveProject(r.Context(), id, p)
	s.St.Audit(r.Context(), userOf(r).Username, "project.save", p.Name, nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"id": newID})
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := s.St.DeleteProject(r.Context(), id)
	s.St.Audit(r.Context(), userOf(r).Username, "project.delete", strconv.FormatInt(id, 10), nil, err, clientIP(r))
	if errors.Is(err, store.ErrInUse) {
		fail(w, http.StatusConflict, "in_use",
			"还有环境挂在这个项目下 —— 先把它们移到别的项目，或者删掉那些环境")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}
