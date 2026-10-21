package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ops-version-backend/internal/store"
	"ops-version-backend/notify"
)

// ---------- 通知人（Teams 渠道同步失败时 @ 的人） ----------

func (s *Server) listContacts(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListContacts(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}

type contactReq struct {
	Name         string `json:"name"`
	TeamsAccount string `json:"teams_account"`
	Note         string `json:"note"`
	Enabled      bool   `json:"enabled"`
}

func (s *Server) saveContact(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	req, err := body[contactReq](r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	acct := notify.NormalizeTeamsAccount(req.TeamsAccount)
	note := strings.TrimSpace(req.Note)
	// 每种不合规各报各的原因
	switch {
	case name == "":
		fail(w, http.StatusBadRequest, "bad_request", "名字必填")
		return
	case !notify.ValidMentionName(name):
		fail(w, http.StatusBadRequest, "bad_request", "名字不能超过 64 个字，也不能带 < >")
		return
	case acct == "":
		fail(w, http.StatusBadRequest, "bad_request", "Teams 账号必填（一般是公司邮箱）")
		return
	case !notify.ValidTeamsAccount(acct):
		fail(w, http.StatusBadRequest, "bad_request", "Teams 账号格式不对，应为登录邮箱，如 name@example.com")
		return
	case len([]rune(note)) > 255:
		fail(w, http.StatusBadRequest, "bad_request", "备注不能超过 255 个字")
		return
	}
	newID, err := s.St.SaveContact(r.Context(), id, store.NotifyContact{
		Name: name, TeamsAccount: acct, Note: note, Enabled: req.Enabled,
	})
	action := "notify.contact_create"
	if id > 0 {
		action = "notify.contact_update"
	}
	s.St.Audit(r.Context(), userOf(r).Username, action, name,
		map[string]any{"teams_account": acct, "enabled": req.Enabled}, err, clientIP(r))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "not_found", "通知人不存在，可能已被删除")
		return
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ok(w, map[string]any{"id": newID})
}

func (s *Server) deleteContact(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := s.St.DeleteContact(r.Context(), id)
	s.St.Audit(r.Context(), userOf(r).Username, "notify.contact_delete",
		strconv.FormatInt(id, 10), nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}
