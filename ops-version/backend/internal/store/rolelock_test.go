package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// 🔴 这三条是角色锁定的**全部意义**所在，必须在真库上验：
//
//	锁着   → SSO 登录不覆盖角色（不然手工改的角色一登录就被打回去）
//	没锁   → 照常覆盖（不然锁定变成了永久禁用组映射）
//	锁过期 → 恢复覆盖（不然"有期限"就是假的，而这正是加期限的理由）
//
// 用 TEST_MIG_DSN 指向本地库的副本。空库也能跑 —— 这几条不依赖既有数据。
func openTestDB(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_MIG_DSN")
	if dsn == "" {
		t.Skip("需要 TEST_MIG_DSN")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{db: db}
}

// mkUser 造一个 SSO 用户，返回 id。测试结束硬删掉。
func mkUser(t *testing.T, s *Store, name, role string) int64 {
	t.Helper()
	ctx := context.Background()
	_, _ = s.db.ExecContext(ctx, `DELETE FROM users WHERE username=?`, name)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (username, display_name, password_hash, auth_source, role_code, enabled)
		VALUES (?,?,'','sso',?,1)`, name, name, role)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	t.Cleanup(func() {
		_, _ = s.db.ExecContext(context.Background(), `DELETE FROM users WHERE id=?`, id)
	})
	return id
}

func TestRoleLockBlocksOIDCOverwrite(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	id := mkUser(t, s, "zz-locktest", "admin")

	if err := s.SetRoleLock(ctx, id, 30, "临时借调"); err != nil {
		t.Fatal(err)
	}
	// SSO 登录带来 viewer —— 锁着，不该生效
	u, err := s.UpsertOIDCUser(ctx, OIDCUser{
		Username: "zz-locktest", DisplayName: "改过的名字", RoleCode: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if u.RoleCode != "admin" {
		t.Errorf("锁着却被组映射覆盖成 %q —— 手工改的角色一登录就被打回去了", u.RoleCode)
	}
	// ⚠️ 只有角色不动，其余身份信息照常同步 —— 否则改了显示名永远同步不过来
	if u.DisplayName != "改过的名字" {
		t.Errorf("锁定只该锁角色，显示名应照常同步，实得 %q", u.DisplayName)
	}
}

func TestNoLockAllowsOIDCOverwrite(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	mkUser(t, s, "zz-nolock", "admin")

	u, err := s.UpsertOIDCUser(ctx, OIDCUser{Username: "zz-nolock", RoleCode: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if u.RoleCode != "viewer" {
		t.Errorf("没锁就该跟着组走，实得 %q —— 否则锁定变成了永久禁用组映射", u.RoleCode)
	}
}

// 🔴 期限必须是真的：过期的锁等于没锁
func TestExpiredLockNoLongerBlocks(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	id := mkUser(t, s, "zz-expired", "admin")

	// 直接把到期时间写成昨天 —— 模拟"锁过期了"
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET role_locked_until=?, role_lock_reason='过期的锁' WHERE id=?`,
		time.Now().Add(-24*time.Hour), id); err != nil {
		t.Fatal(err)
	}
	u, err := s.UpsertOIDCUser(ctx, OIDCUser{Username: "zz-expired", RoleCode: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if u.RoleCode != "viewer" {
		t.Errorf("锁已过期却仍在挡覆盖（角色 %q）—— "+
			"判据写成了「字段非空」而不是「还没到期」，那期限就是假的", u.RoleCode)
	}
	if u.RoleLocked() {
		t.Error("过期的锁 RoleLocked() 必须为 false")
	}
}

// 解锁：days<=0
func TestUnlockClearsBoth(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	id := mkUser(t, s, "zz-unlock", "admin")
	if err := s.SetRoleLock(ctx, id, 30, "先锁上"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoleLock(ctx, id, 0, ""); err != nil {
		t.Fatalf("解锁不该要求填理由：%v", err)
	}
	u, err := s.UserByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.RoleLocked() || u.RoleLockReason != "" {
		t.Errorf("解锁后 until 和 reason 都要清掉，实得 %+v / %q",
			u.RoleLockedUntil, u.RoleLockReason)
	}
}
