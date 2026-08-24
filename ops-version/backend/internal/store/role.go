package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"ops-version-backend/internal/auth"
)

// Role 一个角色。与 auth.Role 同形，这层多带 ID / 审计字段。
type Role struct {
	ID      int64    `json:"id"`
	Code    string   `json:"code"`
	Name    string   `json:"name"`
	Perms   []string `json:"perms"`
	Builtin bool     `json:"builtin"`
	Note    string   `json:"note"`
	// InUse 有多少个用户在用。删除前要看这个，界面上也要显示 ——
	// 「这个角色能不能删」是人点删除前唯一想知道的事
	InUse int `json:"in_use"`
}

// ErrRoleBuiltin 内置角色不可改不可删。
var ErrRoleBuiltin = errors.New("内置角色不能修改或删除")

// ListRoles 全部角色（含内置），带在用人数。
func (s *Store) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.code, r.name, r.perms, r.builtin, r.note,
		       (SELECT COUNT(*) FROM users u
		         WHERE u.role_code = r.code AND u.deleted_at IS NULL) AS in_use
		  FROM roles r WHERE r.deleted_at IS NULL
		 ORDER BY r.builtin DESC, r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		var r Role
		var perms string
		var builtin int
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &perms, &builtin, &r.Note, &r.InUse); err != nil {
			return nil, err
		}
		r.Builtin = builtin == 1
		// 🔴 逗号串必须在这里拆成数组。
		//    直接把 "view,export" 塞给前端的 string[] 会让整页崩 ——
		//    某个同类产品 栽过一次，症状是接口 200、页面白屏。
		r.Perms = splitComma(perms)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveRole 新建或改一个自定义角色。id=0 为新建。
func (s *Store) SaveRole(ctx context.Context, id int64, r Role, actor string) (int64, error) {
	code := strings.TrimSpace(r.Code)
	if code == "" {
		return 0, fmt.Errorf("角色码必填")
	}
	if strings.TrimSpace(r.Name) == "" {
		return 0, fmt.Errorf("角色名必填")
	}
	// 🔴 角色码要能安全地写进 users.role_code 并被 URL/日志引用。
	//    不挡的话，一个带空格或逗号的角色码会把 perms 的逗号串解析弄乱。
	for _, ch := range code {
		ok := ch == '_' || ch == '-' ||
			(ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
		if !ok {
			return 0, fmt.Errorf("角色码只能用小写字母、数字、下划线和连字符：%q", code)
		}
	}
	// ⚠️ 不许用内置角色的码新建 —— 会撞唯一索引，但报出来的是 1062
	//    这种看不懂的错。在这里挡住，给一句人能懂的话。
	if _, isBuiltin := auth.RoleOf(code); isBuiltin && id == 0 {
		return 0, fmt.Errorf("角色码 %q 已被内置角色占用", code)
	}
	if len(r.Perms) == 0 {
		return 0, fmt.Errorf("至少要选一项权限 —— 零权限的角色等于禁用账号，不该用角色来表达")
	}
	// 🔴 权限码写错不会报错，只是那项权限永远不生效，而界面上复选框还勾着。
	//    存进去之前就挡住。
	for _, p := range r.Perms {
		if !auth.PermValid(auth.Perm(p)) {
			return 0, fmt.Errorf("未知的权限码：%q", p)
		}
	}
	perms := strings.Join(r.Perms, ",")

	if id == 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO roles (code, name, perms, builtin, note, created_by)
			VALUES (?,?,?,0,?,?)`, code, r.Name, perms, r.Note, actor)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}

	// 内置角色不可改：它包含哪些权限是安全契约的一部分，
	// 出事后要说得清当时它是什么。
	var builtin int
	if err := s.db.QueryRowContext(ctx,
		`SELECT builtin FROM roles WHERE id=? AND deleted_at IS NULL`, id).
		Scan(&builtin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("角色不存在")
		}
		return 0, err
	}
	if builtin == 1 {
		return 0, ErrRoleBuiltin
	}
	// ⚠️ code 不许改。改了之后 users.role_code 还指向旧码，
	//    那些人当场变成「未知角色 = 零权限」，而界面上看不出任何异常。
	_, err := s.db.ExecContext(ctx, `
		UPDATE roles SET name=?, perms=?, note=? WHERE id=?`,
		r.Name, perms, r.Note, id)
	return id, err
}

// DeleteRole 删一个自定义角色。
//
// 🔴 还有人在用就不许删。
//
//	删了的话那些人的 role_code 指向一个不存在的角色 = 零权限，
//	而界面上他们看着还是"有角色"的 —— 只是什么都点不动。
//	这种状态没人查得出来，所以从源头堵死。
func (s *Store) DeleteRole(ctx context.Context, id int64) error {
	var builtin int
	var code string
	if err := s.db.QueryRowContext(ctx,
		`SELECT builtin, code FROM roles WHERE id=? AND deleted_at IS NULL`, id).
		Scan(&builtin, &code); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("角色不存在")
		}
		return err
	}
	if builtin == 1 {
		return ErrRoleBuiltin
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role_code=? AND deleted_at IS NULL`, code).
		Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w：还有 %d 个用户在用这个角色，先把他们改成别的角色", ErrInUse, n)
	}
	// 组映射里引用了也不许删 —— 删了之后那条映射静默失效，
	// 表现是"这个组的人登录后没有角色"，而映射表看着好好的
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM oidc_role_mappings WHERE role_code=?`, code).Scan(&n); err != nil {
		// 表可能不存在（没接 SSO），不因此挡住删除
		n = 0
	}
	if n > 0 {
		return fmt.Errorf("%w：组→角色映射里还有 %d 条指向它，先去改映射", ErrInUse, n)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE roles SET deleted_at=NOW() WHERE id=?`, id)
	return err
}

// LoadRolesIntoAuth 把库里的角色装进 auth 的注册表，并校验内置角色没被改过。
//
// 🔴 每次改角色之后都要再调一次 —— 否则新建的角色在权限判定里不存在，
// 表现是"我明明建了这个角色，分配给人之后他什么都点不动"。
func (s *Store) LoadRolesIntoAuth(ctx context.Context) error {
	list, err := s.ListRoles(ctx)
	if err != nil {
		return err
	}
	out := make([]auth.Role, 0, len(list))
	for _, r := range list {
		perms := map[auth.Perm]bool{}
		for _, p := range r.Perms {
			perms[auth.Perm(p)] = true
		}
		out = append(out, auth.Role{
			Code: r.Code, Name: r.Name, Perms: perms, Builtin: r.Builtin, Note: r.Note,
		})
	}
	if err := auth.VerifyBuiltins(out); err != nil {
		return err
	}
	auth.SetRoles(out)
	return nil
}

func splitComma(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
