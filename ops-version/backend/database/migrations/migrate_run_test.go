package migrations

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// 🔴 迁移必须用**真正的执行器**验，不能用 mysql CLI。
//
// 两者的语句切分规则不同：CLI 按分号切（跨行无所谓），
// 而 runner.go 的 splitSQL 按「行尾分号」切 —— 写在同一行的
// `PREPARE ...; EXECUTE ...;` 会被当成一条整体送给 MySQL，报 1064。
// 实测栽过：CLI 全绿，部署后 pod 直接 CrashLoopBackOff。
func TestMigrationsOnRealCopy(t *testing.T) {
	dsn := os.Getenv("TEST_MIG_DSN")
	if dsn == "" {
		t.Skip("需要 TEST_MIG_DSN（指向一份真实数据的副本）")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Run(db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	// 幂等：再跑一遍不能出错
	if err := Run(db); err != nil {
		t.Fatalf("重跑失败（迁移必须幂等，pod 重启会再跑一次）：%v", err)
	}
}
