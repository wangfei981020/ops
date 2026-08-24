package store

import "time"

// ─────────────────────────────────────────────────────────────
// 列表分页的统一口径
// ─────────────────────────────────────────────────────────────
//
// 🔴 存在的理由：同一个坏写法在四个列表接口里各写了一遍 ——
//
//	if limit <= 0 || limit > 500 { limit = 100 }
//
// 它把「没指定」和「要多了」当成同一种情况，于是 **传 501 比传 500 拿得更少**，
// 而且没有任何提示。审计那一处被用户撞到时我只修了那一处，
// 没去搜同一形状的其他处 —— 剩下三处照旧。
//
// 抽出来是为了让「下一个列表接口」不必再想一遍这件事。

// DefaultPageLimit 没指定时给多少。
const DefaultPageLimit = 100

// MaxPageLimit 单页上限。
const MaxPageLimit = 500

// ClampLimit 把请求的条数收进合法范围。
//
//	limit <= 0      → 默认值（调用方没指定）
//	limit > 上限    → **钳制到上限**（调用方要多了，给它能给的最多）
//
// ⚠️ 超上限绝不能掉回默认值 —— 那会让「要 501 条」比「要 500 条」拿得更少，
// 这是任何人都想不到的行为。
func ClampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case limit > MaxPageLimit:
		return MaxPageLimit
	default:
		return limit
	}
}

// Page 一页数据的通用信封。
//
// 🔴 Total 必须给：不给的话，人翻到最后一页也不知道自己看到的是不是全部 ——
// 而「我是不是漏看了什么」正是查列表时唯一在意的事。
//
// NextBefore 是游标（这一页最小的 id），0 = 没有下一页。
// ⚠️ 用游标而不是 offset：这些表都在持续插入，
// offset 会让同一条在两页里都出现、或者被整个跳过。
type Page[T any] struct {
	Rows       []T   `json:"rows"`
	Total      int   `json:"total"`
	NextBefore int64 `json:"next_before"`
	// NextBeforeAt 排序键不是 id 时的游标补充项。
	//
	// 🔴 游标必须和**排序键一致**。按时间排却拿 id 当游标的话，
	//    切出来的集合和「排在这一行之后」根本不是同一批 ——
	//    会重复、也会漏。执行记录实测栽过（25 条翻到第三页只剩 1 条）。
	NextBeforeAt *time.Time `json:"next_before_at,omitempty"`
}
