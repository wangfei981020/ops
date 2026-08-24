package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"ops-version-backend/logx"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"ops-version-backend/internal/auth"
)

var ErrNotFound = errors.New("not found")

// ErrInUse 对象还被别处引用，不能删。
//
// 🔴 单独一个错误类型，好让 API 层回 409 而不是 500 ——
// "删不了因为还有人在用"是**用户能自己解决**的情况，
// 报成 500 会让人以为系统坏了，然后去查日志找不到任何异常。
var ErrInUse = errors.New("in use")

// ErrTokenExpired MCP 令牌过期。
//
// 🔴 与 ErrNotFound 分开：两者对**接入方**的下一步完全不同 ——
//
//	不存在 / 已吊销 → 去查是不是抄错了、是不是被人吊销了
//	过期           → 找管理员续期就行
//
// 共用一个错误的话，对方拿到「令牌无效或已停用」会朝错误的方向查半天。
var ErrTokenExpired = errors.New("令牌已过期")

type User struct {
	ID          int64
	Username    string
	DisplayName string
	Email       string
	AuthSource  string
	RoleCode    string
	VisibleOrgs string
	Enabled     bool
	LastLoginAt sql.NullTime

	// ─── 角色锁定 ───
	//
	// 🔴 锁着的用户，SSO 登录**不按组覆盖角色**。
	//    不锁的话，手工改的角色下一次登录就被组映射覆盖回去，且毫无提示。
	//
	// ⚠️ 判「有没有锁着」一律用 RoleLocked()，不要直接看字段是否非空 ——
	//    过期的锁必须失效，那正是给锁加期限的意义。
	RoleLockedUntil sql.NullTime
	RoleLockReason  string
}

// RoleLocked 现在是不是锁着的。
func (u User) RoleLocked() bool {
	return u.RoleLockedUntil.Valid && u.RoleLockedUntil.Time.After(time.Now())
}

// Scope 把用户的角色和数据范围合成一个判定对象。
func (u User) Scope() auth.Scope {
	s := auth.Scope{Role: u.RoleCode}
	if strings.TrimSpace(u.VisibleOrgs) != "" {
		s.Orgs = map[int64]bool{}
		for _, p := range strings.Split(u.VisibleOrgs, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
				s.Orgs[id] = true
			}
		}
	}
	return s
}

// EnsureSuperUser 确保本地超管存在。
//
// 🔴 本地账号在接了 SSO 之后也**必须保留**：SSO 挂掉时它是唯一的逃生通道。
// 别的系统上吃过亏 —— OIDC 没调通就把密码登录关了，结果超管也进不去，只能改数据库救。
//
// 🔴 判据是"**这个超管账号**在不在"，不是"库里有没有用户"。
//
// 原来写的是 `SELECT COUNT(*) FROM users ... > 0 就跳过`，后果有两层：
//  1. SSO 建了任何账号之后，超管就再也不会被创建 —— 逃生通道悄悄没了，
//     而这个函数的存在意义正是提供那条通道
//  2. 改了 SUPER_PASSWORD 也永远不生效，且**没有任何提示**
//     （实测过：管理员按新密码登了四次全是 401，日志里只有干巴巴的 401）
//
// 密码默认**不覆盖**（每次重启都重置会让人改不了密码）。
// 忘了密码走 `-reset-password` 子命令，见 ResetPassword。
func (s *Store) EnsureSuperUser(ctx context.Context, username, password string) error {
	var id int64
	var existing string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, role_code FROM users WHERE username=? AND deleted_at IS NULL`,
		username).Scan(&id, &existing)

	if err == sql.ErrNoRows {
		if password == "" {
			return fmt.Errorf("超管 %q 不存在，必须提供 SUPER_PASSWORD 才能创建", username)
		}
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO users (username, display_name, password_hash, auth_source, role_code)
			 VALUES (?,?,?,'local',?)`,
			username, username, string(h), auth.RoleSuperAdmin)
		logx.Info("boot", "super_user_created", map[string]any{"user": username})
		return err
	}
	if err != nil {
		return err
	}

	// 账号在。⚠️ 角色被人改低过就补回来 —— 超管被降权等于逃生通道失效，
	//    而那是"最后一个管理员把自己降权了"这类事故的典型形态。
	if existing != auth.RoleSuperAdmin {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE users SET role_code=?, enabled=1 WHERE id=?`, auth.RoleSuperAdmin, id); err != nil {
			return err
		}
		logx.Info("boot", "super_user_role_restored", map[string]any{
			"user": username, "was": existing})
	}

	return nil
}

func (s *Store) UserByName(ctx context.Context, name string) (User, string, error) {
	var u User
	var hash string
	var enabled int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username, display_name, email, auth_source, role_code,
		       visible_orgs, enabled, password_hash, last_login_at,
		       role_locked_until, role_lock_reason
		  FROM users WHERE username=? AND deleted_at IS NULL`, name).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.AuthSource, &u.RoleCode,
			&u.VisibleOrgs, &enabled, &hash, &u.LastLoginAt,
			&u.RoleLockedUntil, &u.RoleLockReason)
	if err == sql.ErrNoRows {
		return u, "", ErrNotFound
	}
	u.Enabled = enabled == 1
	return u, hash, err
}

func (s *Store) CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// CreateSession 建会话。
//
// 会话存库而不是纯 JWT，是为了「改角色能立刻踢人」—— 见 DeleteUserSessions。
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, expires_at) VALUES (?,?,?)`,
		id, userID, time.Now().Add(ttl))
	if err != nil {
		return "", err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE users SET last_login_at=? WHERE id=?`, time.Now(), userID)
	return id, nil
}

// UserBySession 取会话对应的用户。过期会话视同不存在。
func (s *Store) UserBySession(ctx context.Context, sid string) (User, error) {
	var u User
	var enabled int
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.display_name, u.email, u.auth_source, u.role_code,
		       u.visible_orgs, u.enabled
		  FROM sessions s JOIN users u ON u.id=s.user_id
		 WHERE s.id=? AND s.expires_at>? AND u.deleted_at IS NULL`,
		sid, time.Now()).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.AuthSource,
			&u.RoleCode, &u.VisibleOrgs, &enabled)
	if err == sql.ErrNoRows {
		return u, ErrNotFound
	}
	u.Enabled = enabled == 1
	if !u.Enabled {
		// 账号被停用时立刻失效，不等会话过期
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) DeleteSession(ctx context.Context, sid string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, sid)
	return err
}

// DeleteUserSessions 踢掉某人的全部会话。
//
// 🔴 改角色后**必须**调这个：角色信息在会话里，不踢的话降权看着像没生效 ——
// 权限实际还留着，这是最危险的一种「看起来正常」。
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// CountSuperAdmins 统计还剩几个超管。
// 🔴 用于阻止「最后一个超管把自己降权」—— 那会让系统彻底锁死，谁都改不回来。
func (s *Store) CountSuperAdmins(ctx context.Context, excludeID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role_code=? AND enabled=1 AND deleted_at IS NULL AND id<>?`,
		auth.RoleSuperAdmin, excludeID).Scan(&n)
	return n, err
}

// Audit 记一条审计。
//
// 🔴 detail 里绝不能出现凭据明文。调用方传进来的 map 应该已经脱敏 ——
// 这里再兜一道：见 scrubKeys。
func (s *Store) Audit(ctx context.Context, actor, action, target string, detail map[string]any, err error, ip string) {
	res, msg := "success", ""
	if err != nil {
		res, msg = "failed", err.Error()
		msg = clipText(msg, 500)
	}
	var d any
	if detail != nil {
		scrub(detail)
		b, _ := json.Marshal(detail)
		d = string(b)
	}
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO audit_logs (actor, action, target, detail, result, error_msg, ip)
		VALUES (?,?,?,?,?,?,?)`, actor, action, target, d, res, msg, ip)
}

// scrubKeys 这些 key 一律不落审计。
// 审计日志是给人看的、会被导出的，凭据进去就等于泄露。
var scrubKeys = []string{"password", "api_key", "apikey", "token", "secret", "credential"}

func scrub(m map[string]any) {
	for k := range m {
		lk := strings.ToLower(k)
		for _, bad := range scrubKeys {
			if strings.Contains(lk, bad) {
				m[k] = "***"
				break
			}
		}
	}
}

// MCPScopeByToken 用令牌换角色与数据范围。
//
// 令牌只存哈希，这里按哈希查 —— 明文只在创建时返回一次。
// 能被读出来的令牌等于没有令牌。
func (s *Store) MCPScopeByToken(ctx context.Context, plain string) (auth.Scope, string, error) {
	h := sha256.Sum256([]byte(plain))
	hash := hex.EncodeToString(h[:])

	var name, role, vis string
	var expires sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT name, role_code, visible_orgs, expires_at
		  FROM mcp_tokens
		 WHERE token_hash=? AND enabled=1 AND deleted_at IS NULL`, hash).
		Scan(&name, &role, &vis, &expires)
	if err == sql.ErrNoRows {
		return auth.Scope{}, "", ErrNotFound
	}
	if err != nil {
		return auth.Scope{}, "", err
	}
	if expires.Valid && expires.Time.Before(time.Now()) {
		// 🔴 过期要和「不存在/已吊销」分开报。
		//    共用 ErrNotFound 的话，对方拿到的是「令牌无效或已停用」——
		//    他会去查是不是被吊销了、是不是抄错了，而实际只要续期。
		//    ⚠️ 错误里带上到期时间：对方不知道我们这边设了多久。
		return auth.Scope{}, "", fmt.Errorf("%w（%s 到期），请联系管理员续期",
			ErrTokenExpired, expires.Time.Format("2006-01-02 15:04"))
	}
	// 未知角色不给任何权限，而不是回落 viewer —— 见 auth.RoleValid
	if !auth.RoleValid(role) {
		return auth.Scope{}, "", fmt.Errorf("令牌绑定的角色 %q 不合法", role)
	}

	_, _ = s.db.ExecContext(ctx, `UPDATE mcp_tokens SET last_used_at=? WHERE token_hash=?`,
		time.Now(), hash)

	u := User{RoleCode: role, VisibleOrgs: vis}
	return u.Scope(), name, nil
}

// CreateMCPToken 生成一条令牌，返回明文（只此一次）。
// DefaultMCPTokenDays MCP 令牌的默认有效期。
//
// 🔴 这些令牌是发给**外部接入方和 AI** 的长期凭据。永不过期意味着：
//
//	对方公司换了对接人、AI 的配置被复制到别处、令牌进了某个人的笔记 ——
//	这些都不会有任何时点强制我们回头看一眼。
//	加期限不是为了防谁，是为了让「这条还该不该存在」这个问题**有人问**。
//
// ⚠️ 与角色锁定同一套理由，所以也是 90 天。
const DefaultMCPTokenDays = 90

// CreateMCPToken 发一条 MCP 令牌。
//
// days <= 0 表示**永不过期** —— 允许，但调用方必须显式传，
// 不能靠默认值悄悄变成永久（那正是 的成因：
// 过期校验写得好好的，写入侧根本不支持设置，于是全部令牌 expires_at 都是 NULL）。
func (s *Store) CreateMCPToken(ctx context.Context, name, role, visible, creator string, days int) (string, error) {
	if !auth.RoleValid(role) {
		return "", fmt.Errorf("角色 %q 不合法", role)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	plain := "opsv_" + hex.EncodeToString(b)
	h := sha256.Sum256([]byte(plain))
	// ⚠️ 到期时间在 Go 这边算，不用 MySQL 的 DATE_ADD(NOW(),...)：
	//    NOW() 走服务端时区，而本系统的时间语义统一按 Go 的 time.Local。
	//    两套时区混用会让"还有几天到期"差上几个小时。
	var expires any
	if days > 0 {
		expires = time.Now().AddDate(0, 0, days)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO mcp_tokens (name, token_hash, token_prefix, role_code, visible_orgs, created_by, expires_at)
		VALUES (?,?,?,?,?,?,?)`,
		name, hex.EncodeToString(h[:]), plain[:13], role, visible, creator, expires)
	if err != nil {
		return "", err
	}
	return plain, nil
}

// UserByID 按主键读用户。
//
// 🔴 与 UserByName 的分工：写完之后要读回**刚写的那一条**，只能按 id。
// 按 username 读的话，库里若有历史遗留的重复行（见 UpsertOIDCUser 的说明），
// 会读到别的那条 —— 而那正是"角色改了却不生效"的成因。
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	var u User
	var enabled int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username, display_name, email, auth_source, role_code,
		       visible_orgs, enabled, last_login_at, role_locked_until, role_lock_reason
		  FROM users WHERE id=? AND deleted_at IS NULL`, id).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.AuthSource, &u.RoleCode,
			&u.VisibleOrgs, &enabled, &u.LastLoginAt, &u.RoleLockedUntil, &u.RoleLockReason)
	if err == sql.ErrNoRows {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	u.Enabled = enabled == 1
	return u, nil
}

// ResetPassword 重置任意用户的密码。**只给命令行子命令用**，没有 HTTP 入口。
//
// 🔴 为什么不做成环境变量开关（原来的 SUPER_PASSWORD_RESET）：
// 那种开关有个改不掉的缺陷 —— **忘了撤就每次重启都把密码打回去**。
// 而"重启"是运维日常动作（改配置、扩容、节点漂移），
// 于是"我明明改过密码，怎么又变回去了"会周期性地发生，且极难联想到那个变量。
//
// 子命令是一次性的：跑完就结束，不留任何持续生效的状态。
//
// ⚠️ 必须**踢掉该用户所有会话**：密码换了而旧会话还能用，等于没换。
// 忘密码的场景里，"怀疑账号被人用了"恰恰是最常见的动机。
func (s *Store) ResetPassword(ctx context.Context, username, password string) error {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM users WHERE username=? AND deleted_at IS NULL`, username).Scan(&id)
	if err == sql.ErrNoRows {
		return fmt.Errorf("用户 %q 不存在（或已被删除）", username)
	}
	if err != nil {
		return err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	// enabled=1 一并置上：忘密码的账号常常同时是被停用的那个
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash=?, enabled=1 WHERE id=?`, string(h), id); err != nil {
		return err
	}
	n, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, id)
	if err != nil {
		return err
	}
	killed, _ := n.RowsAffected()
	logx.Info("cli", "password_reset", map[string]any{
		"user": username, "sessions_killed": killed})
	return nil
}
