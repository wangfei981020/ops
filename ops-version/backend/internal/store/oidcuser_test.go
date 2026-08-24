package store

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// 🔴 SSO 重复登录必须**更新同一行**，不能每次新插一条。
//
// 原实现用 `INSERT ... ON DUPLICATE KEY UPDATE`，而 users 的唯一键是
// (username, deleted_at)，活跃行的 deleted_at 恒为 NULL —— MySQL 的唯一索引里
// NULL 不参与唯一判断，于是约束形同虚设、ON DUPLICATE 永不触发。
// 表现：改了组→角色映射、重新登录，角色纹丝不动。
func TestUpsertOIDCUserUpdatesSameRow(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("需要 TEST_MYSQL_DSN")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{db: db}
	ctx := context.Background()

	const name = "upsert-test-user"
	defer db.Exec("DELETE FROM users WHERE username=?", name)
	_, _ = db.Exec("DELETE FROM users WHERE username=?", name)

	u1, err := s.UpsertOIDCUser(ctx, OIDCUser{Username: name, RoleCode: "viewer", Groups: "g1"})
	if err != nil {
		t.Fatalf("首次建号失败: %v", err)
	}
	if u1.RoleCode != "viewer" {
		t.Fatalf("首次角色 = %q，要 viewer", u1.RoleCode)
	}

	// 第二次登录，角色变了（模拟管理员改了组映射）
	u2, err := s.UpsertOIDCUser(ctx, OIDCUser{Username: name, RoleCode: "editor", Groups: "g1"})
	if err != nil {
		t.Fatalf("二次登录失败: %v", err)
	}

	// ① 必须是**同一行**
	if u2.ID != u1.ID {
		t.Errorf("两次登录拿到不同的 id（%d vs %d）—— 又插了新行", u1.ID, u2.ID)
	}
	// ② 角色必须已更新（这正是用户报的现象）
	if u2.RoleCode != "editor" {
		t.Errorf("二次登录角色 = %q，要 editor —— 角色没刷新", u2.RoleCode)
	}
	// ③ 库里只能有一条活跃行
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM users WHERE username=? AND deleted_at IS NULL", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("活跃行数 = %d，要 1 —— 重复登录插出了多行", n)
	}

	// ④ 读回来的也必须是新角色（会话就是拿这个建的）
	got, _, err := s.UserByName(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if got.RoleCode != "editor" {
		t.Errorf("按用户名读回 = %q，要 editor", got.RoleCode)
	}
}
