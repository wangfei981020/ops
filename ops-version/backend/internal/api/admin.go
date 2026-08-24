package api

import (
	"net/http"
	"strconv"

	"ops-version-backend/internal/auth"
	"ops-version-backend/internal/store"
)

// ─────────────── 用户 ───────────────

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListUsers(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 🔴 不返回 password_hash。哈希虽然不是明文，但泄露了就能离线爆破
	out := []map[string]any{}
	for _, u := range list {
		row := map[string]any{
			"id": u.ID, "username": u.Username, "display_name": u.DisplayName,
			"email": u.Email, "auth_source": u.AuthSource, "role_code": u.RoleCode,
			"visible_orgs": u.VisibleOrgs, "enabled": u.Enabled,
		}
		if u.LastLoginAt.Valid {
			row["last_login_at"] = u.LastLoginAt.Time
		}
		// 🔴 锁定状态要出到界面上。
		//    看不到的话，「这个人的角色为什么改不动 / 为什么登录后又变回去」
		//    完全没有线索 —— 而这正是加锁定之前那个 bug 的症状。
		//    ⚠️ role_locked 与 role_locked_until 都给：
		//    前端**不许自己拿到期时间跟当前时间比**来推「锁没锁」，
		//    浏览器时钟和服务端差几分钟就会两处显示不一致。判据一律以后端为准。
		row["role_locked"] = u.RoleLocked()
		row["role_lock_reason"] = u.RoleLockReason
		if u.RoleLockedUntil.Valid {
			row["role_locked_until"] = u.RoleLockedUntil.Time
		} else {
			row["role_locked_until"] = nil
		}
		out = append(out, row)
	}
	ok(w, out)
}

type userReq struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	RoleCode    string `json:"role_code"`
	VisibleOrgs string `json:"visible_orgs"`
	Enabled     bool   `json:"enabled"`
}

func (s *Server) saveUser(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	req, err := body[userReq](r)
	if err != nil || (id == 0 && req.Username == "") {
		fail(w, http.StatusBadRequest, "bad_request", "用户名必填")
		return
	}
	newID, err := s.St.SaveUser(r.Context(), id, store.UserInput{
		Username: req.Username, DisplayName: req.DisplayName, Email: req.Email,
		Password: req.Password, RoleCode: req.RoleCode,
		VisibleOrgs: req.VisibleOrgs, Enabled: req.Enabled,
	})
	s.St.Audit(r.Context(), userOf(r).Username, "user.save", req.Username,
		map[string]any{"id": id, "role": req.RoleCode, "enabled": req.Enabled}, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// 🔴 改完必须踢会话：角色在会话里，不踢的话降权看着像没生效，权限实际还留着
	if id > 0 {
		_ = s.St.DeleteUserSessions(r.Context(), id)
	}
	ok(w, map[string]any{"id": newID})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if id == userOf(r).ID {
		fail(w, http.StatusBadRequest, "bad_request", "不能删除自己")
		return
	}
	err := s.St.DeleteUser(r.Context(), id)
	s.St.Audit(r.Context(), userOf(r).Username, "user.delete",
		strconv.FormatInt(id, 10), nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, role := range auth.AllRoles() {
		perms := []string{}
		for _, p := range role.PermList() {
			perms = append(perms, string(p))
		}
		// 🔴 name 和 builtin 必须给：自定义角色在界面上没有 i18n 文案，
		//    前端只能显示后端给的名字。不给的话，一个叫 notify_only 的角色
		//    在用户列表里会渲染成 `opsversion:role.notify_only` 这种原始 key。
		out = append(out, map[string]any{
			"code": role.Code, "name": role.Name,
			"builtin": role.Builtin, "perms": perms,
		})
	}
	ok(w, out)
}

// ─────────────── MCP 令牌 ───────────────

func (s *Server) listMCPTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListMCPTokens(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list) // 只有前缀，没有完整令牌 —— 明文只在创建时返回一次
}

func (s *Server) revokeMCPToken(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := s.St.RevokeMCPToken(r.Context(), id)
	s.St.Audit(r.Context(), userOf(r).Username, "mcp_token.revoke",
		strconv.FormatInt(id, 10), nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

// ─────────────── 对比方案 ───────────────

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListPlans(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}

func (s *Server) savePlan(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	req, err := body[store.ComparePlan](r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	newID, err := s.St.SavePlan(r.Context(), id, req, userOf(r).Username)
	s.St.Audit(r.Context(), userOf(r).Username, "plan.save", req.Name,
		map[string]any{"columns": len(req.Columns)}, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ok(w, map[string]any{"id": newID})
}

func (s *Server) deletePlan(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := s.St.DeletePlan(r.Context(), id)
	s.St.Audit(r.Context(), userOf(r).Username, "plan.delete",
		strconv.FormatInt(id, 10), nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}
