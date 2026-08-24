package compare

import "strings"

// ProjectFilter 一个项目「吃哪些服务」。
//
// 🔴 三档，按优先级，够用即止：
//   ① ns 隔离      → 环境的 ns 规则已经分开了，这里两项都留空
//   ② 名字有规律   → Include 写通配（biz-*）
//   ③ 无规律       → Pins 列确切服务名
//
// ⚠️ **两项都为空 = 全收**，不是「全不收」。
//
//	这是唯一合理的默认：绝大多数平台只有一个项目，配置页上什么都不填，
//	此时若解释成「全不收」，比对表会整列空白，而配置看着完全正常。
//	（同一条陷阱在 ns_include 上已经栽过一次。）
type ProjectFilter struct {
	Include []string
	Pins    []string
}

// Empty 没有任何规则 —— 调用方据此跳过过滤，而不是逐条去匹配空列表。
func (f ProjectFilter) Empty() bool { return len(f.Include) == 0 && len(f.Pins) == 0 }

// Matches 该服务是否属于这个项目。
//
// Include 与 Pins 是**或**的关系：通配覆盖大多数，Pins 补那几个不合规律的。
func (f ProjectFilter) Matches(serviceKey string) bool {
	if f.Empty() {
		return true
	}
	for _, p := range f.Pins {
		if strings.TrimSpace(p) == serviceKey {
			return true
		}
	}
	for _, p := range f.Include {
		if matchPattern(p, serviceKey) {
			return true
		}
	}
	return false
}

// matchPattern 前后缀通配。与 providers 层同一套语义 ——
// 两处规则不一致的话，「采回来了但比对时不认」这种事没人查得出来。
func matchPattern(pat, s string) bool {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return false
	}
	if pat == "*" {
		return true
	}
	pre, suf := strings.HasPrefix(pat, "*"), strings.HasSuffix(pat, "*")
	switch {
	case pre && suf:
		return strings.Contains(s, strings.Trim(pat, "*"))
	case suf:
		return strings.HasPrefix(s, strings.TrimSuffix(pat, "*"))
	case pre:
		return strings.HasSuffix(s, strings.TrimPrefix(pat, "*"))
	default:
		return pat == s
	}
}
