package store

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// 迁移 014 在**有真实数据的库**上的结果核对。
//
// 🔴 空库验不出自动迁移对不对 —— 迁移的意义正是"把老数据搬到新结构"，
// 而空库里没有老数据。所以这个测试跑在真实数据的副本上（TEST_MIG_DSN）。
func TestMigration014OnRealData(t *testing.T) {
	dsn := os.Getenv("TEST_MIG_DSN")
	if dsn == "" {
		t.Skip("需要 TEST_MIG_DSN（指向一份真实数据的副本）")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{db: db}
	ctx := context.Background()

	orgs, err := s.ListOrgs(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) == 0 {
		t.Skip("副本里没有平台，跳过")
	}

	for _, o := range orgs {
		// ① 有连接类型的平台必须挂上数据源（manual_import 除外 —— 它没有连接源）
		if o.ProviderType != "manual_import" && o.DatasourceID == 0 {
			t.Errorf("平台 %q(%s) 没有挂上数据源 —— 迁移漏了", o.Name, o.ProviderType)
		}
		for _, e := range o.Envs {
			// ② 连接信息不能因为迁移而丢失
			ep, _, _ := e.Conn(o)
			if ep == "" && o.ProviderType != "manual_import" {
				t.Errorf("平台 %q/%s 迁移后连接地址为空 —— 采集会直接失败", o.Name, e.Env)
			}
			// 🔴 ③ 迁移期必须仍走**平台级**：平台自己的 endpoint 还在，
			//    此时若走了数据源，等于升级瞬间换掉了所有平台的连接来源
			if o.Endpoint != "" && e.Endpoint == "" {
				if got := e.ConnSource(o); got != "org" {
					t.Errorf("平台 %q/%s 连接来源 = %q，迁移期应仍为 org —— 行为与升级前不一致",
						o.Name, e.Env, got)
				}
			}
		}
	}

	// ④ 活跃平台的环境必须全部挂上项目
	var unmapped int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM org_envs e
		  JOIN orgs o ON o.id = e.org_id AND o.deleted_at IS NULL
		 WHERE e.project_id IS NULL`).Scan(&unmapped); err != nil {
		t.Fatal(err)
	}
	if unmapped != 0 {
		t.Errorf("有 %d 个活跃平台的环境没挂上项目", unmapped)
	}

	// ⑤ 每个活跃平台都要有默认项目
	for _, o := range orgs {
		ps, err := s.ListProjects(ctx, o.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(ps) == 0 {
			t.Errorf("平台 %q 没有任何项目 —— 它的环境将无处归属", o.Name)
		}
	}
	t.Logf("核对通过：%d 个平台", len(orgs))
}
