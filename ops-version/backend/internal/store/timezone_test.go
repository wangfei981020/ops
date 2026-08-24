package store

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// 🔴 复现并验证 Go 写入的时间与 SQL 里 NOW() 写入的时间必须同一个基准。
//
// ⚠️ **本地复现不了**这个 bug：本机 MySQL 的 time_zone 是 SYSTEM，
// 而系统时区恰好就是 CST，于是两种写入路径碰巧一致。
// 生产的 MySQL 服务端是 UTC，才会差出 8 小时。
// 所以这里**显式把会话时区设成 UTC 来模拟生产**，否则测了也是白测。
func TestWriteTimeBaseConsistency(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("需要 TEST_MYSQL_DSN")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tz_probe (
		id INT PRIMARY KEY AUTO_INCREMENT,
		by_go   DATETIME NULL,
		by_now  DATETIME NULL)`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP TABLE tz_probe")

	probe := func(sessionTZ string) time.Duration {
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(t.Context(), "SET time_zone=?", sessionTZ); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(t.Context(),
			`INSERT INTO tz_probe (by_go, by_now) VALUES (?, NOW())`, time.Now()); err != nil {
			t.Fatal(err)
		}
		var g, n time.Time
		if err := conn.QueryRowContext(t.Context(),
			`SELECT by_go, by_now FROM tz_probe ORDER BY id DESC LIMIT 1`).Scan(&g, &n); err != nil {
			t.Fatal(err)
		}
		d := g.Sub(n)
		if d < 0 {
			d = -d
		}
		return d
	}

	// ① 模拟生产：MySQL 会话是 UTC，而 Go 是 CST ⇒ 必然差出时区偏移
	if got := probe("+00:00"); got < 30*time.Minute {
		_, off := time.Now().Zone()
		if off != 0 {
			t.Errorf("会话时区为 UTC 时两种写入应当差出时区偏移，实测只差 %v —— "+
				"说明这个环境复现不了该问题，测试本身失去意义", got)
		}
	}

	// ② 修复后：会话时区跟应用一致 ⇒ 两种写入必须一致（允许几秒执行间隔）
	if got := probe(localTZForTest()); got > 10*time.Second {
		t.Errorf("会话时区与应用一致时，两种写入仍差 %v —— 时区参数没生效", got)
	}
}

// localTZForTest 与 config.localTZOffset 同一算法（store 包不依赖 config）
func localTZForTest() string {
	_, off := time.Now().Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	h, m := off/3600, (off%3600)/60
	return string([]byte{sign[0],
		byte('0' + h/10), byte('0' + h%10), ':',
		byte('0' + m/10), byte('0' + m%10)})
}
