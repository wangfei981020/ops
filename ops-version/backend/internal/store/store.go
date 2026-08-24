// Package store 是数据访问层。
//
// 只做「读写数据库」，判定逻辑一律在 internal/compare —— 那层是纯函数、能脱离环境反复测，
// 混进 SQL 就测不动了。
package store

import "database/sql"

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

// clipText 按**字符**截断，不按字节。
//
// 🔴 `s[:500]` 是按字节切的，中文一个字 3 字节 —— 切在中间会产生**非法 UTF-8 序列**，
// MySQL 直接拒收：`Error 1366 (HY000): Incorrect string value: '\xE8\xA1' ...`。
//
// 后果比"错误信息被截断"严重得多：**整条状态都写不进库**。
// 采集失败了，而 last_collect_status/error 还停在上一次的值 ——
// 界面上看到的是过期状态，日志里只有一条 mark_failed，
// 而那条又很容易被当成无关紧要的小毛病略过。
// 实测撞到过（2026-08-20）：错误文案里带中文提示，于是失败状态一次都没落库。
//
// ⚠️ 列宽是 VARCHAR(500)，按字符算 —— 所以这里也必须按字符算，两边口径要一致。
func clipText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
