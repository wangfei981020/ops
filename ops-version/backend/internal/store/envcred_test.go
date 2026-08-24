package store

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// 🔴 环境级凭据：**留空 = 不改动**，不能被当成"清空"。
//
// 凭据永不回显，所以前端提交上来多数时候是空的。
// 平台级早就这么处理了（`if CredentialEnc != "" 才更新`），
// 而环境级是 DELETE+INSERT，空值直接写 NULL ⇒ 改任何别的字段都会把凭据抹掉。
// 实测过：第一次填的 API Key 生效了（Rancher 认出账号、返回 403 而非 401），
// 随后改了别的字段再保存，就变成"认证失败: 未配置 API Key"。
func TestSaveOrgKeepsEnvCredentialWhenBlank(t *testing.T) {
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

	const name = "envcred-test-org"
	defer db.Exec("DELETE FROM orgs WHERE name=?", name)
	_, _ = db.Exec("DELETE FROM orgs WHERE name=?", name)

	// 第一次保存：带凭据
	id, err := s.SaveOrg(ctx, 0, OrgInput{
		Name: name, ProviderType: "rancher", AuthType: "password",
		Envs: []OrgEnv{{
			Env: "UAT", ClusterRefs: []string{"local"}, CompareEnabled: true,
			Endpoint: "https://rancher.example.com", AuthType: "api_key",
			CredentialEnc: "ENC-SECRET-1",
		}},
	})
	if err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}

	// 第二次保存：只改 ns 规则，**凭据留空**（模拟用户改别的字段）
	if _, err := s.SaveOrg(ctx, id, OrgInput{
		Name: name, ProviderType: "rancher", AuthType: "password",
		Envs: []OrgEnv{{
			Env: "UAT", ClusterRefs: []string{"local"}, CompareEnabled: true,
			NSInclude: []string{"app-uat"}, // ← 新加的
			Endpoint:  "https://rancher.example.com", AuthType: "api_key",
			CredentialEnc: "", // ← 留空 = 没改
		}},
	}); err != nil {
		t.Fatalf("二次保存失败: %v", err)
	}

	var got sql.NullString
	if err := db.QueryRow(
		`SELECT credential_enc FROM org_envs WHERE org_id=? AND env='UAT'`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got.String != "ENC-SECRET-1" {
		t.Errorf("凭据 = %q，要 ENC-SECRET-1 —— 改别的字段把凭据抹掉了", got.String)
	}

	// 显式给新凭据时必须覆盖
	if _, err := s.SaveOrg(ctx, id, OrgInput{
		Name: name, ProviderType: "rancher", AuthType: "password",
		Envs: []OrgEnv{{
			Env: "UAT", ClusterRefs: []string{"local"}, CompareEnabled: true,
			Endpoint: "https://rancher.example.com", AuthType: "api_key",
			CredentialEnc: "ENC-SECRET-2",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(
		`SELECT credential_enc FROM org_envs WHERE org_id=? AND env='UAT'`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got.String != "ENC-SECRET-2" {
		t.Errorf("显式改凭据没生效：%q", got.String)
	}
}

// 超管：账号在就不动密码；被降权要补回来；开了重置开关才改密码。
func TestEnsureSuperUser(t *testing.T) {
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

	const name = "super-test-user"
	defer db.Exec("DELETE FROM users WHERE username=?", name)
	_, _ = db.Exec("DELETE FROM users WHERE username=?", name)

	// 🔴 库里已经有别的用户（SSO 建的），超管照样要能被创建 ——
	//    原实现在这里直接跳过，逃生通道就此消失
	if err := s.EnsureSuperUser(ctx, name, "First@2026"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	u1, hash1, err := s.UserByName(ctx, name)
	if err != nil {
		t.Fatalf("创建后查不到: %v", err)
	}
	if u1.RoleCode != "super_admin" {
		t.Errorf("角色 = %q，要 super_admin", u1.RoleCode)
	}

	// 不开重置：密码不能被改
	if err := s.EnsureSuperUser(ctx, name, "Second@2026"); err != nil {
		t.Fatal(err)
	}
	_, hash2, _ := s.UserByName(ctx, name)
	if hash2 != hash1 {
		t.Error("没开重置开关却改了密码 —— 每次重启都会把密码打回去")
	}

	// 被降权 → 下次启动补回来
	if _, err := db.Exec("UPDATE users SET role_code='viewer' WHERE id=?", u1.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSuperUser(ctx, name, "Second@2026"); err != nil {
		t.Fatal(err)
	}
	u3, _, _ := s.UserByName(ctx, name)
	if u3.RoleCode != "super_admin" {
		t.Errorf("降权后没补回来：%q —— 逃生通道失效", u3.RoleCode)
	}

	// ResetPassword（-reset-password 子命令走的就是它）：密码要变，且旧会话被踢
	sid, err := s.CreateSession(ctx, u1.ID, 3600e9)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx, name, "Third@2026"); err != nil {
		t.Fatal(err)
	}
	_, hash4, _ := s.UserByName(ctx, name)
	if hash4 == hash1 {
		t.Error("ResetPassword 没改密码")
	}
	// 🔴 密码换了而旧会话还能用，等于没换 —— 而忘密码的场景里
	//    "怀疑账号被人用了"恰恰是最常见的动机
	if _, err := s.UserBySession(ctx, sid); err == nil {
		t.Error("重置密码后旧会话仍然有效")
	}
	// 不存在的用户要报错，不能静默成功
	if err := s.ResetPassword(ctx, "no-such-user-xyz", "Whatever@2026"); err == nil {
		t.Error("重置不存在的用户却没报错")
	}
}
