package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

func orgStore(t *testing.T) *Store {
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

// 🔴 回归：**带环境新建平台**。
//
// 017 把 org_envs.project_id 收紧成 NOT NULL，而新建平台时项目还不存在
// （项目要挂在平台下，得先有平台）——于是这条最基本的路径直接 500
// 「Column 'project_id' cannot be null」，而报错完全看不出真正缺的是
// 「这个平台还没有项目」。
//
// ⚠️ 这条路径之前一条测试都没有，所以 017 上线后才被用户撞到。
func TestSaveOrgWithEnvsCreatesDefaultProject(t *testing.T) {
	s := orgStore(t)
	ctx := context.Background()
	name := "zz-创建测试"
	_, _ = s.db.ExecContext(ctx, `DELETE FROM org_envs WHERE org_id IN (SELECT id FROM orgs WHERE name=?)`, name)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM projects WHERE org_id IN (SELECT id FROM orgs WHERE name=?)`, name)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM orgs WHERE name=?`, name)

	id, err := s.SaveOrg(ctx, 0, OrgInput{
		Name: name, ProviderType: "kite", AuthType: "password", Endpoint: "http://x",
		Envs: []OrgEnv{
			{Env: "UAT", ClusterRefs: []string{"c1"}, CompareEnabled: true},
			{Env: "PROD", ClusterRefs: []string{"c2"}, CompareEnabled: true},
		},
	})
	if err != nil {
		t.Fatalf("带环境新建平台失败：%v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = s.db.ExecContext(c, `DELETE FROM org_envs WHERE org_id=?`, id)
		_, _ = s.db.ExecContext(c, `DELETE FROM projects WHERE org_id=?`, id)
		_, _ = s.db.ExecContext(c, `DELETE FROM orgs WHERE id=?`, id)
	})

	var nEnv, nProj int
	if err := s.db.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM org_envs WHERE org_id=?),
		        (SELECT COUNT(*) FROM projects WHERE org_id=? AND deleted_at IS NULL)`,
		id, id).Scan(&nEnv, &nProj); err != nil {
		t.Fatal(err)
	}
	if nEnv != 2 {
		t.Errorf("环境写进去 %d 条，要 2 条", nEnv)
	}
	if nProj != 1 {
		t.Errorf("应自动建出 1 个默认项目，实得 %d 个", nProj)
	}
	// 环境必须挂在那个默认项目上，不能是 0
	var bad int
	_ = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM org_envs e WHERE e.org_id=? AND NOT EXISTS
		   (SELECT 1 FROM projects p WHERE p.id=e.project_id)`, id).Scan(&bad)
	if bad > 0 {
		t.Errorf("%d 条环境挂在不存在的项目上 —— 这一列的数据谁也查不到，"+
			"而配置页上看着正常", bad)
	}
}

// 🔴 删平台要把环境一起删掉。
//
// 原来只软删 orgs，org_envs 留着 —— 而**恢复平台这条路根本不存在**
// （没有任何代码把 deleted_at 置回 NULL），那些行永远不会被用到，
// 只会让每次数据模型变更的迁移都留下一批「挂不上任何活跃平台」的行。
func TestDeleteOrgRemovesEnvs(t *testing.T) {
	s := orgStore(t)
	ctx := context.Background()
	name := "zz-删除测试"
	_, _ = s.db.ExecContext(ctx, `DELETE FROM orgs WHERE name=?`, name)
	id, err := s.SaveOrg(ctx, 0, OrgInput{
		Name: name, ProviderType: "kite", AuthType: "password", Endpoint: "http://x",
		Envs: []OrgEnv{{Env: "UAT", ClusterRefs: []string{"c1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = s.db.ExecContext(c, `DELETE FROM org_envs WHERE org_id=?`, id)
		_, _ = s.db.ExecContext(c, `DELETE FROM projects WHERE org_id=?`, id)
		_, _ = s.db.ExecContext(c, `DELETE FROM orgs WHERE id=?`, id)
	})

	if err := s.DeleteOrg(ctx, id); err != nil {
		t.Fatal(err)
	}
	var nEnv, nProj, softDeleted int
	_ = s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM org_envs WHERE org_id=?),
		       (SELECT COUNT(*) FROM projects WHERE org_id=? AND deleted_at IS NULL),
		       (SELECT deleted_at IS NOT NULL FROM orgs WHERE id=?)`,
		id, id, id).Scan(&nEnv, &nProj, &softDeleted)
	if nEnv != 0 {
		t.Errorf("删平台后还剩 %d 条环境 —— 每次迁移都会留下一批挂不上的行", nEnv)
	}
	if nProj != 0 {
		t.Errorf("删平台后还剩 %d 个活跃项目", nProj)
	}
	if softDeleted != 1 {
		t.Error("平台本身应该是软删")
	}
}

// 🔴 软删之后同名平台必须能重建 —— 这才是软删该有的行为（018 迁移）
func TestSoftDeletedNameCanBeReused(t *testing.T) {
	s := orgStore(t)
	ctx := context.Background()
	name := "zz-重名测试"
	_, _ = s.db.ExecContext(ctx, `DELETE FROM orgs WHERE name=?`, name)
	in := OrgInput{Name: name, ProviderType: "kite", AuthType: "password", Endpoint: "http://x"}

	id1, err := s.SaveOrg(ctx, 0, in)
	if err != nil {
		t.Fatal(err)
	}
	// 还活着时不许重名
	if _, err := s.SaveOrg(ctx, 0, in); err == nil {
		t.Error("同名活跃平台应被拒 —— 比对表会出现两个同名列，谁也分不清哪个是哪个")
	}
	if err := s.DeleteOrg(ctx, id1); err != nil {
		t.Fatal(err)
	}
	id2, err := s.SaveOrg(ctx, 0, in)
	if err != nil {
		t.Fatalf("软删之后同名应该能重建：%v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, i := range []int64{id1, id2} {
			_, _ = s.db.ExecContext(c, `DELETE FROM org_envs WHERE org_id=?`, i)
			_, _ = s.db.ExecContext(c, `DELETE FROM projects WHERE org_id=?`, i)
			_, _ = s.db.ExecContext(c, `DELETE FROM orgs WHERE id=?`, i)
		}
	})
}
