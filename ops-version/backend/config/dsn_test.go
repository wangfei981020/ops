package config

import (
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// 🔴 拆分环境变量的核心价值：密码里可以有任意字符。
// 手拼 "user:pass@tcp(host:port)/db" 时，密码含 @ # : / 会让 DSN 解析出错，
// 而报错指向「DSN 格式不对」，没人会想到是密码里的一个字符。
func TestDSNHandlesSpecialCharsInPassword(t *testing.T) {
	for _, pw := range []string{
		"<password>", "p:a/s@s#w0rd", "a@b:c/d#e", "简单密码",
	} {
		c := &Config{MySQLHost: "mysql.db-services", MySQLPort: 3306,
			MySQLUser: "ops_version_user", MySQLPassword: pw, MySQLDatabase: "ops_version"}
		dsn := c.DSN()
		// 用驱动自己的解析器回读，能解出来才算真的没问题
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil {
			t.Fatalf("密码 %q 拼出的 DSN 解析失败: %v\nDSN=%s", pw, err, dsn)
		}
		if cfg.Passwd != pw {
			t.Errorf("密码 %q 回读成了 %q", pw, cfg.Passwd)
		}
		if cfg.DBName != "ops_version" {
			t.Errorf("库名回读错误: %q", cfg.DBName)
		}
		if !strings.Contains(cfg.Addr, "3306") {
			t.Errorf("地址回读错误: %q", cfg.Addr)
		}
	}
}

func TestLoadRejectsMissing(t *testing.T) {
	for _, k := range []string{"MYSQL_HOST", "MYSQL_USER", "MYSQL_PASSWORD", "ENCRYPT_KEY", "JWT_SECRET"} {
		os.Unsetenv(k)
	}
	if _, err := Load(); err == nil {
		t.Fatal("缺配置时必须拒绝启动")
	} else if !strings.Contains(err.Error(), "MYSQL_HOST") {
		t.Errorf("错误信息要点名缺了哪个: %v", err)
	}
}
