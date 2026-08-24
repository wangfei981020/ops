package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"ops-version-backend/internal/auth"
	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
)

// listRolesFull 角色管理页用：带 id / 名字 / 在用人数 / 是否内置。
//
// ⚠️ 与 GET /api/roles 分开：那个是给所有人的下拉用的（只要 view 权限），
// 这个含"多少人在用"，属于用户管理的信息，要 user.admin。
func (s *Server) listRolesFull(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListRoles(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}

// listPerms 全部权限码。前端的权限复选框按这个渲染 ——
// 🔴 前端**不许自己维护一份权限清单**：新增一项权限时前端不更新，
// 那项权限就永远没人能勾上，而后端明明已经支持了。
func (s *Server) listPerms(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, p := range auth.AllPerms() {
		out = append(out, map[string]any{"code": string(p)})
	}
	ok(w, out)
}

func (s *Server) saveRole(w http.ResponseWriter, r *http.Request) {
	req, err := body[store.Role](r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	actor := userOf(r).Username
	newID, err := s.St.SaveRole(r.Context(), id, req, actor)
	if err != nil {
		if errors.Is(err, store.ErrRoleBuiltin) {
			fail(w, http.StatusConflict, "role_builtin", err.Error())
			return
		}
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// 🔴 存完必须重新装载，否则新角色在权限判定里不存在 ——
	//    表现是"我明明建了这个角色，分配给人之后他什么都点不动"。
	if err := s.St.LoadRolesIntoAuth(r.Context()); err != nil {
		logx.Warn("roles", "reload_failed", map[string]any{"err": err.Error()})
	}
	s.St.Audit(r.Context(), actor, "role.save", strconv.FormatInt(newID, 10),
		map[string]any{"code": req.Code, "perms": req.Perms}, nil, clientIP(r))
	ok(w, map[string]any{"id": newID})
}

func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.St.DeleteRole(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, store.ErrRoleBuiltin):
			fail(w, http.StatusConflict, "role_builtin", err.Error())
		case errors.Is(err, store.ErrInUse):
			// 409 而不是 400：这不是"请求写错了"，是"现在还不能删"，
			// 而且错误里带着可执行的下一步（去把那些人改成别的角色）
			fail(w, http.StatusConflict, "in_use", err.Error())
		default:
			fail(w, http.StatusBadRequest, "bad_request", err.Error())
		}
		return
	}
	if err := s.St.LoadRolesIntoAuth(r.Context()); err != nil {
		logx.Warn("roles", "reload_failed", map[string]any{"err": err.Error()})
	}
	actor := userOf(r).Username
	s.St.Audit(r.Context(), actor, "role.delete", strconv.FormatInt(id, 10), nil, nil, clientIP(r))
	ok(w, map[string]any{"ok": true})
}

// ─────────────── 角色锁定 ───────────────

func (s *Server) setRoleLock(w http.ResponseWriter, r *http.Request) {
	req, err := body[struct {
		// Days <=0 表示解锁。不传时用默认 90 天 —— 但**前端必须显式显示这个默认值**，
		// 悄悄替人选一个期限，等于让人在不知情的情况下接受了一个复核周期。
		Days   *int   `json:"days"`
		Reason string `json:"reason"`
	}](r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	days := store.DefaultRoleLockDays
	if req.Days != nil {
		days = *req.Days
	}
	if err := s.St.SetRoleLock(r.Context(), id, days, req.Reason); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	actor := userOf(r).Username
	action := "user.role_lock"
	if days <= 0 {
		action = "user.role_unlock"
	}
	s.St.Audit(r.Context(), actor, action, strconv.FormatInt(id, 10),
		map[string]any{"days": days, "reason": req.Reason}, nil, clientIP(r))
	ok(w, map[string]any{"ok": true, "days": days})
}

// listExpiringLocks 快到期（或已过期）的角色锁。
//
// 🔴 这个接口是角色锁定「有期限」这件事**唯一被看见的途径**。
//
//	没有它，到期那天用户的角色会静默变回组映射的值 ——
//	管理员完全不知道发生了什么，只会收到一句「他的权限怎么自己变了」。
//	那正是给锁加期限想避免的失败模式，结果反而制造了它。
//
// ⚠️ 含**已经过期**的（within 是"到这个时刻为止"，不是"从现在起"）：
//
//	过期的锁已经不起作用了，但那个人的角色刚刚被改回去 ——
//	这恰恰是最需要被看见的一刻。只列"将要到期"会把它漏掉。
func (s *Server) listExpiringLocks(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	list, err := s.St.ExpiringRoleLocks(r.Context(), time.Duration(days)*24*time.Hour)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := []map[string]any{}
	for _, u := range list {
		row := map[string]any{
			"id": u.ID, "username": u.Username, "display_name": u.DisplayName,
			"role_code": u.RoleCode, "reason": u.RoleLockReason,
			// 🔴 expired 由**后端**判定。前端拿到期时间跟本地时间比的话，
			//    浏览器时钟偏几分钟就会和后端的实际行为不一致 ——
			//    界面说"还没到期"，而后端已经按组映射把角色改回去了。
			"expired": !u.RoleLocked(),
		}
		if u.RoleLockedUntil.Valid {
			row["locked_until"] = u.RoleLockedUntil.Time
		}
		out = append(out, row)
	}
	ok(w, out)
}
