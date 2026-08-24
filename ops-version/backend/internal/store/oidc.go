package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// OIDCConfig 单点登录配置。刻意只有一条 —— 见 011_oidc.sql 的说明。
type OIDCConfig struct {
	Enabled         bool   `json:"enabled"`
	DisplayName     string `json:"display_name"`
	Issuer          string `json:"issuer"`
	ClientID        string `json:"client_id"`
	ClientSecretEnc string `json:"-"` // 永不回显
	HasSecret       bool   `json:"has_secret"`
	AuthorizeURL    string `json:"authorize_url"`
	TokenURL        string `json:"token_url"`
	UserinfoURL     string `json:"userinfo_url"`
	Scopes          string `json:"scopes"`
	GroupsClaim     string `json:"groups_claim"`
	UsernameClaim   string `json:"username_claim"`
	DefaultRole     string `json:"default_role"`
	AllowUnmapped   bool   `json:"allow_unmapped"`
	InsecureTLS     bool   `json:"insecure_tls"`
}

func (s *Store) GetOIDCConfig(ctx context.Context) (OIDCConfig, error) {
	var c OIDCConfig
	var secret sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT enabled, display_name, issuer, client_id, client_secret_enc,
		       authorize_url, token_url, userinfo_url, scopes,
		       groups_claim, username_claim, default_role, allow_unmapped, insecure_tls
		  FROM oidc_config WHERE id = 1`).
		Scan(&c.Enabled, &c.DisplayName, &c.Issuer, &c.ClientID, &secret,
			&c.AuthorizeURL, &c.TokenURL, &c.UserinfoURL, &c.Scopes,
			&c.GroupsClaim, &c.UsernameClaim, &c.DefaultRole, &c.AllowUnmapped, &c.InsecureTLS)
	if err != nil {
		return c, err
	}
	c.ClientSecretEnc = secret.String
	c.HasSecret = strings.TrimSpace(secret.String) != ""
	return c, nil
}

// SaveOIDCConfig 保存配置。secretEnc 为空表示**不改动**已有密钥。
//
// 🔴 与 Harbor / 平台凭据同一条规矩：读接口不回显密钥，
// 那么保存时"空"就只能理解成"没改"，否则每次编辑别的字段都会把密钥清掉。
func (s *Store) SaveOIDCConfig(ctx context.Context, c OIDCConfig, secretEnc string) error {
	q := `UPDATE oidc_config SET enabled=?, display_name=?, issuer=?, client_id=?,
	        authorize_url=?, token_url=?, userinfo_url=?, scopes=?,
	        groups_claim=?, username_claim=?, default_role=?, allow_unmapped=?, insecure_tls=?`
	args := []any{c.Enabled, c.DisplayName, c.Issuer, c.ClientID,
		c.AuthorizeURL, c.TokenURL, c.UserinfoURL, c.Scopes,
		c.GroupsClaim, c.UsernameClaim, c.DefaultRole, c.AllowUnmapped, c.InsecureTLS}
	if strings.TrimSpace(secretEnc) != "" {
		q += `, client_secret_enc=?`
		args = append(args, secretEnc)
	}
	q += ` WHERE id = 1`
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// RoleMapping IdP 下发的组/角色 → 本系统角色。
type RoleMapping struct {
	ID         int64  `json:"id"`
	GroupValue string `json:"group_value"`
	RoleCode   string `json:"role_code"`
	Note       string `json:"note"`
}

func (s *Store) ListRoleMappings(ctx context.Context) ([]RoleMapping, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, group_value, role_code, note
		  FROM oidc_role_mappings WHERE deleted_at IS NULL ORDER BY group_value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RoleMapping{}
	for rows.Next() {
		var m RoleMapping
		if err := rows.Scan(&m.ID, &m.GroupValue, &m.RoleCode, &m.Note); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) SaveRoleMapping(ctx context.Context, m RoleMapping) (int64, error) {
	if m.ID > 0 {
		_, err := s.db.ExecContext(ctx,
			`UPDATE oidc_role_mappings SET group_value=?, role_code=?, note=?
			  WHERE id=? AND deleted_at IS NULL`, m.GroupValue, m.RoleCode, m.Note, m.ID)
		return m.ID, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO oidc_role_mappings (group_value, role_code, note) VALUES (?,?,?)`,
		m.GroupValue, m.RoleCode, m.Note)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeleteRoleMapping(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE oidc_role_mappings SET deleted_at=NOW() WHERE id=? AND deleted_at IS NULL`, id)
	return err
}

// OIDCUser 一次 SSO 登录带回来的身份。
type OIDCUser struct {
	Username    string
	DisplayName string
	Email       string
	Subject     string
	RoleCode    string
	Groups      string
}

// UpsertOIDCUser 建号或更新。
//
// 🔴 **不能用 `INSERT ... ON DUPLICATE KEY UPDATE`。**
//
// users 表的唯一键是 `(username, deleted_at)`，而活跃用户的 deleted_at 恒为 NULL ——
// **MySQL 的唯一索引里 NULL 不参与唯一性判断**，所以这个约束对活跃用户形同虚设：
// ON DUPLICATE KEY 永远不触发，每次 SSO 登录都在**新插一行**。
// 而 UserByName 用 QueryRow 只取第一行（最早那条），于是：
//
//	oidc/login          role=editor   ← 算出来的新角色
//	auth/session_issued role=viewer   ← 会话却绑到了旧行
//
// 表现就是"改了映射、重新登录、角色纹丝不动"，而后端日志里角色明明是对的
// 。
//
// 改成**先查后写**：查到就按 id 更新，查不到才插入。
// ⚠️ 用事务 + FOR UPDATE：同一个人并发登录两次（浏览器重试、双标签页）时，
// 不锁的话两条都查不到、两条都插入 —— 又回到多行的老问题。
//
// ⚠️ 认人用 username 不用 sub：换 IdP 时 sub 会整套换掉
// （同事的 GitLab 接 MXID 撞的 "Email taken" 就是这个根因）。sub 只存下来备查。
//
// 🔴 每次登录都重算角色，而不是只在建号时定一次 ——
// 否则 IdP 那边把人从管理员组移出去，这里还是管理员。
func (s *Store) UpsertOIDCUser(ctx context.Context, in OIDCUser) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	var lockedUntil sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT id, role_locked_until FROM users WHERE username=? AND deleted_at IS NULL FOR UPDATE`,
		in.Username).Scan(&id, &lockedUntil)

	switch {
	case err == sql.ErrNoRows:
		res, err := tx.ExecContext(ctx, `
			INSERT INTO users (username, display_name, email, password_hash, auth_source,
			                   role_code, enabled, oidc_subject, oidc_groups, last_login_at)
			VALUES (?, ?, ?, '', 'sso', ?, 1, ?, ?, NOW())`,
			in.Username, in.DisplayName, in.Email, in.RoleCode, in.Subject, in.Groups)
		if err != nil {
			return User{}, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return User{}, err
		}
	case err != nil:
		return User{}, err
	default:
		// 🔴 角色锁定：锁着的用户**不按组覆盖角色**。
		//
		//    不锁的话，管理员在界面上手工改的角色，这个人下一次 SSO 登录
		//    就被组映射覆盖回去 —— 表现是"我明明改了，他登录一次又变回来了"，
		//    而且没有任何提示，只能靠猜。
		//
		// ⚠️ 判据是**到期时间还没过**，不是"字段非空"：
		//    锁过期了就该恢复成跟着组走，这正是给锁加期限的意义。
		//    写成 `lockedUntil.Valid` 的话，锁会永久生效，
		//    人调岗了、从组里移出去了也不会变 —— 而没有任何东西提醒你。
		locked := lockedUntil.Valid && lockedUntil.Time.After(time.Now())
		if locked {
			// 角色和 role_locked_until 都不动，其余身份信息照常同步
			if _, err := tx.ExecContext(ctx, `
				UPDATE users SET display_name=?, email=?, auth_source='sso',
				                 oidc_subject=?, oidc_groups=?, last_login_at=NOW()
				 WHERE id=?`,
				in.DisplayName, in.Email, in.Subject, in.Groups, id); err != nil {
				return User{}, err
			}
			break
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE users SET display_name=?, email=?, auth_source='sso',
			                 role_code=?, oidc_subject=?, oidc_groups=?, last_login_at=NOW()
			 WHERE id=?`,
			in.DisplayName, in.Email, in.RoleCode, in.Subject, in.Groups, id); err != nil {
			return User{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return User{}, err
	}

	// 🔴 按 **id** 读回，不是按 username。
	//    按 username 读的话，万一库里已经有历史遗留的重复行，
	//    还是会读到别的那条 —— 而我们刚写的明明是这一条。
	return s.UserByID(ctx, id)
}

// Branding 品牌自定义（白标）。空字段 = 用产品内置的。
type Branding struct {
	AppName     string `json:"app_name"`
	LogoData    string `json:"logo_data"`
	FaviconData string `json:"favicon_data"`
	Tagline     string `json:"tagline"`
	UpdatedBy   string `json:"updated_by"`
}

func (s *Store) GetBranding(ctx context.Context) (Branding, error) {
	var b Branding
	var logo, fav sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT app_name, logo_data, favicon_data, tagline, updated_by
		  FROM branding WHERE id = 1`).
		Scan(&b.AppName, &logo, &fav, &b.Tagline, &b.UpdatedBy)
	b.LogoData, b.FaviconData = logo.String, fav.String
	return b, err
}

func (s *Store) SaveBranding(ctx context.Context, b Branding, by string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE branding SET app_name=?, logo_data=?, favicon_data=?, tagline=?, updated_by=?
		 WHERE id=1`, b.AppName, nullIfEmpty(b.LogoData), nullIfEmpty(b.FaviconData), b.Tagline, by)
	return err
}
