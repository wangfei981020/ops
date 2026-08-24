package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"ops-version-backend/internal/auth"
	"ops-version-backend/internal/compare"

	"ops-version-backend/logx"
)

// ─────────────── 用户 ───────────────

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, username, display_name, email, auth_source, role_code,
		       visible_orgs, enabled, last_login_at, role_locked_until, role_lock_reason
		  FROM users WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var en int
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.AuthSource,
			&u.RoleCode, &u.VisibleOrgs, &en, &u.LastLoginAt,
			&u.RoleLockedUntil, &u.RoleLockReason); err != nil {
			return nil, err
		}
		u.Enabled = en == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

type UserInput struct {
	Username    string
	DisplayName string
	Email       string
	Password    string // 空 = 不改
	RoleCode    string
	VisibleOrgs string
	Enabled     bool
}

// SaveUser 建或改。id=0 为新建。
//
// 🔴 改角色后调用方**必须**再调 DeleteUserSessions —— 角色在会话里，
// 不踢的话降权看着像没生效，权限实际还留着。
func (s *Store) SaveUser(ctx context.Context, id int64, in UserInput) (int64, error) {
	if !auth.RoleValid(in.RoleCode) {
		return 0, fmt.Errorf("角色 %q 不合法", in.RoleCode)
	}
	if id == 0 {
		if in.Password == "" {
			return 0, fmt.Errorf("新建用户必须设置密码")
		}
		h, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		if err != nil {
			return 0, err
		}
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO users (username, display_name, email, password_hash, auth_source,
			  role_code, visible_orgs, enabled)
			VALUES (?,?,?,?,'local',?,?,?)`,
			in.Username, in.DisplayName, in.Email, string(h),
			in.RoleCode, in.VisibleOrgs, boolToInt(in.Enabled))
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}

	// 🔴 最后一个超管不能降权/停用 —— 那会让系统彻底锁死，谁都改不回来
	var cur string
	var curEnabled int
	if err := s.db.QueryRowContext(ctx,
		`SELECT role_code, enabled FROM users WHERE id=? AND deleted_at IS NULL`, id).
		Scan(&cur, &curEnabled); err != nil {
		return 0, err
	}
	if cur == auth.RoleSuperAdmin && (in.RoleCode != auth.RoleSuperAdmin || !in.Enabled) {
		n, err := s.CountSuperAdmins(ctx, id)
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, fmt.Errorf("这是最后一个超级管理员，不能降权或停用 —— 否则没人能改回来")
		}
	}

	q := `UPDATE users SET display_name=?, email=?, role_code=?, visible_orgs=?, enabled=?`
	args := []any{in.DisplayName, in.Email, in.RoleCode, in.VisibleOrgs, boolToInt(in.Enabled)}
	if in.Password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		if err != nil {
			return 0, err
		}
		q += `, password_hash=?`
		args = append(args, string(h))
	}
	q += ` WHERE id=?`
	args = append(args, id)
	_, err := s.db.ExecContext(ctx, q, args...)
	return id, err
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	var role string
	if err := s.db.QueryRowContext(ctx,
		`SELECT role_code FROM users WHERE id=? AND deleted_at IS NULL`, id).Scan(&role); err != nil {
		return err
	}
	if role == auth.RoleSuperAdmin {
		n, err := s.CountSuperAdmins(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("这是最后一个超级管理员，不能删除")
		}
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET deleted_at=NOW(), enabled=0 WHERE id=?`, id); err != nil {
		return err
	}
	// 删了必须立刻踢会话，否则他手里的 cookie 还能用到过期
	return s.DeleteUserSessions(ctx, id)
}

// ─────────────── MCP 令牌 ───────────────

type MCPToken struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Prefix   string `json:"prefix"`
	RoleCode string `json:"role_code"`
	Visible  string `json:"visible_orgs"`
	Enabled  bool   `json:"enabled"`
	// ExpiresAt nil = 永不过期。
	//
	// 🔴 必须出到界面上：这是发给外部接入方和 AI 的长期凭据，
	//    「它什么时候失效」是管理这些令牌唯一需要知道的事。
	//    ⚠️ expired 由**后端**判定，不让前端拿时间跟本地时钟比 ——
	//    浏览器时钟偏几分钟，界面说"还有效"而接口已经在拒了。
	ExpiresAt  *time.Time `json:"expires_at"`
	Expired    bool       `json:"expired"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (s *Store) ListMCPTokens(ctx context.Context) ([]MCPToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, token_prefix, role_code, visible_orgs, enabled,
		       expires_at, last_used_at, created_by, created_at
		  FROM mcp_tokens WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MCPToken{}
	for rows.Next() {
		var t MCPToken
		var en int
		var lu, ex sql.NullTime
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.RoleCode, &t.Visible,
			&en, &ex, &lu, &t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Enabled = en == 1
		if ex.Valid {
			v := ex.Time
			t.ExpiresAt = &v
			t.Expired = v.Before(time.Now())
		}
		if lu.Valid {
			v := lu.Time
			t.LastUsedAt = &v
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeMCPToken 停用（软删）。令牌一旦发出去就收不回来，只能靠这里吊销。
func (s *Store) RevokeMCPToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE mcp_tokens SET enabled=0, deleted_at=NOW() WHERE id=?`, id)
	return err
}

// ─────────────── 对比方案 ───────────────

type PlanCol struct {
	OrgID int64 `json:"org_id"`
	// ProjectID 一列 = 平台 × 项目 × 环境。
	// ⚠️ 老方案存的 JSON 里没有这个字段，解出来是 0 ——
	//    读到 0 必须解释成「该环境自己挂的项目」，不是「没有项目」。
	//    这条规则在 api.resolveProject 里，别在别处另写一份。
	ProjectID int64  `json:"project_id"`
	Env       string `json:"env"`
}

type ComparePlan struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	Columns  []PlanCol `json:"columns"`
	OnlyDiff bool      `json:"only_diff"`
	// Ignores 人为忽略项，随方案存。可解除 —— 对方以后上线了这个服务，
	// 不该逼人重建整个方案。
	Ignores   compare.IgnoreSet `json:"ignores"`
	CreatedBy string            `json:"created_by"`
	CreatedAt time.Time         `json:"created_at"`
}

func (s *Store) ListPlans(ctx context.Context) ([]ComparePlan, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, columns_json, only_diff,
		       ignores_json, created_by, created_at
		  FROM comparison_plans WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ComparePlan{}
	for rows.Next() {
		var p ComparePlan
		var cj string
		var ij sql.NullString
		var od int
		if err := rows.Scan(&p.ID, &p.Name, &cj,
			&od, &ij, &p.CreatedBy, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.OnlyDiff = od == 1
		// 🔴 库里的 JSON 坏了不能静默变空。
		//    columns_json 解析失败 → p.Columns 空 → 方案打开后**一列都没有**，
		//    而界面上看着就像"这个方案本来就没配列"，没人会想到去查库。
		// ⚠️ 不中断整个查询：一个方案坏了不该让方案列表整个打不开。
		if err := json.Unmarshal([]byte(cj), &p.Columns); err != nil {
			logx.Warn("store", "plan_columns_broken", map[string]any{
				"plan_id": p.ID, "name": p.Name, "err": err.Error(), "raw": clipText(cj, 200)})
		}
		// NULL / 空串 = 没有忽略规则。留空 IgnoreSet，不是 nil map ——
		// IgnoredCell 读 nil map 不会崩，但前端拿到 `"cells":null` 又是一次白屏。
		p.Ignores = compare.IgnoreSet{Services: []string{}, Cells: map[string][]string{}}
		if ij.Valid && strings.TrimSpace(ij.String) != "" {
			if err := json.Unmarshal([]byte(ij.String), &p.Ignores); err != nil {
				// 忽略规则坏了 → 那些本该被忽略的服务突然全冒出来，
				// 表现是"我明明忽略过的又回来了"，而这最容易被当成功能 bug
				logx.Warn("store", "plan_ignores_broken", map[string]any{
					"plan_id": p.ID, "name": p.Name, "err": err.Error()})
			}
			if p.Ignores.Services == nil {
				p.Ignores.Services = []string{}
			}
			if p.Ignores.Cells == nil {
				p.Ignores.Cells = map[string][]string{}
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SavePlan(ctx context.Context, id int64, p ComparePlan, actor string) (int64, error) {
	if strings.TrimSpace(p.Name) == "" {
		return 0, fmt.Errorf("方案名必填")
	}
	if len(p.Columns) < 2 {
		return 0, fmt.Errorf("至少要两列才能对比")
	}
	cj, _ := json.Marshal(p.Columns)
	ij, _ := json.Marshal(p.Ignores)
	if id == 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO comparison_plans (name, columns_json,
			  only_diff, ignores_json, created_by)
			VALUES (?,?,?,?,?)`, p.Name, string(cj),
			boolToInt(p.OnlyDiff), string(ij), actor)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE comparison_plans SET name=?, columns_json=?,
		  only_diff=?, ignores_json=?
		 WHERE id=?`, p.Name, string(cj),
		boolToInt(p.OnlyDiff), string(ij), id)
	return id, err
}

func (s *Store) DeletePlan(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE comparison_plans SET deleted_at=NOW() WHERE id=?`, id)
	return err
}
