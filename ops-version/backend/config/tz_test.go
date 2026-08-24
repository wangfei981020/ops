package config

import (
	"strings"
	"testing"
	"time"
)

func TestLocalTZOffsetFormat(t *testing.T) {
	got := localTZOffset()
	// MySQL 要的形状是 ±HH:MM
	if len(got) != 6 || (got[0] != '+' && got[0] != '-') || got[3] != ':' {
		t.Fatalf("格式不对：%q，要 ±HH:MM", got)
	}
	// 与 Go 自己算的偏移一致
	_, off := time.Now().Zone()
	want := off / 3600
	var h int
	if _, err := fmtSscan(got[1:3], &h); err != nil {
		t.Fatal(err)
	}
	if got[0] == '-' {
		h = -h
	}
	if h != want {
		t.Errorf("小时偏移 = %d，要 %d（%q）", h, want, got)
	}
}

// DSN 必须带上 time_zone —— 少了它，SQL 里的 NOW() 会用 MySQL 服务端时区，
// 与 Go 写入的时间差出好几个小时，而读出来都按同一个 loc 解释，看不出哪个是错的。
func TestDSNCarriesTimeZone(t *testing.T) {
	c := &Config{MySQLUser: "u", MySQLPassword: "p", MySQLHost: "h", MySQLPort: 3306, MySQLDatabase: "d"}
	dsn := c.DSN()
	if !strings.Contains(dsn, "time_zone") {
		t.Fatalf("DSN 里没有 time_zone：%s", dsn)
	}
	if !strings.Contains(dsn, "parseTime=true") {
		t.Errorf("DSN 里缺 parseTime：%s", dsn)
	}
}

func fmtSscan(s string, out *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, nil
		}
		n = n*10 + int(c-'0')
	}
	*out = n
	return 1, nil
}
