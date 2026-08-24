package auth

import (
	"sort"
	"strings"
)

// RoleRule 一条组 → 角色映射（与 store.RoleMapping 同形，放这里避免 auth 依赖 store）。
type RoleRule struct {
	GroupValue string
	RoleCode   string
}

// ResolveResult 一次组映射的结果，含**没能表达出来的部分**。
//
// 🔴 单独一个结构体而不是多返回值：Dropped 必须被调用方看见并记进日志。
// 返回 (string, []string) 的话，加一项就要改所有调用点，
// 而"少给了权限"这种事一旦静默发生，表现是某个人某天突然点不动某个按钮。
type ResolveResult struct {
	// Role 最终落到用户身上的角色码。空 = 一条都没命中。
	Role string
	// Matched 命中的组
	Matched []string
	// Candidates 命中的全部角色码（去重）。多于一个说明这个人同时属于多个组。
	Candidates []string
	// Dropped 因为"一个用户只能有一个角色"而丢掉的权限。
	//
	// 🔴 非空 = 组映射配出了单一角色表达不了的组合，管理员必须去改映射。
	//    这不是错误（登录照常），但必须喊出来 —— 否则就是悄悄少给权限。
	Dropped []Perm
}

// ResolveRole 依据 IdP 下发的组，定出这个人在本系统里的角色。
//
// # 🔴 为什么不再是「取等级最高的那个」
//
// 内置四个角色是包含关系（viewer ⊂ editor ⊂ admin ⊂ super_admin），
// 取最高即等于取并集。**自定义角色打破了这个前提**：
// 「只能管通知」和「只读+导出」谁高谁低无从谈起，
// 按任何一种排序取一个，都会悄悄丢掉另一个的权限。
//
// 现在的做法：
//  1. 算出命中的所有角色的权限**并集**
//  2. 找一个能覆盖这个并集的候选角色 —— 有就用它（内置的包含关系走的正是这条）
//  3. 没有就取覆盖最多的那个，并把丢掉的权限放进 Dropped 让调用方记日志
//
// 第 3 步不自动造一个"临时角色"：那个角色不在角色表里，
// 管理员在界面上看到一个不存在的角色码，只会更糊涂。
// 让映射配错的人去把映射改对，才是可收敛的做法。
func ResolveRole(groups []string, rules []RoleRule) ResolveResult {
	var res ResolveResult
	seen := map[string]bool{}
	union := map[Perm]bool{}

	for _, g := range groups {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		for _, r := range rules {
			if !matchGroup(r.GroupValue, g) {
				continue
			}
			// ⚠️ 未知的 role_code 直接跳过，不能当成"没角色"也不能瞎给。
			//    映射表里写错一个字（admln）时，静默降权比报错更难查 ——
			//    所以这里跳过，而调用方看到"命中了组却没角色"会走未映射分支。
			role, ok := RoleOf(r.RoleCode)
			if !ok {
				continue
			}
			res.Matched = append(res.Matched, g)
			if !seen[role.Code] {
				seen[role.Code] = true
				res.Candidates = append(res.Candidates, role.Code)
			}
			for p := range role.Perms {
				union[p] = true
			}
		}
	}
	res.Matched = dedup(res.Matched)
	sort.Strings(res.Candidates)
	if len(res.Candidates) == 0 {
		return res
	}

	// 挑一个覆盖并集的；挑不到就挑覆盖最多的
	best, bestN := "", -1
	for _, code := range res.Candidates {
		r, _ := RoleOf(code)
		n := 0
		for p := range union {
			if r.Perms[p] {
				n++
			}
		}
		// 平手时取 code 字典序小的 —— 结果必须可复现，
		// 不能取决于 map 遍历顺序（那会让同一个人每次登录角色都可能不同）
		if n > bestN {
			best, bestN = code, n
		}
	}
	res.Role = best

	if bestN < len(union) {
		r, _ := RoleOf(best)
		for _, p := range AllPerms() {
			if union[p] && !r.Perms[p] {
				res.Dropped = append(res.Dropped, p)
			}
		}
	}
	return res
}

// matchGroup 组值匹配，支持 `*` 通配（与 ns/服务规则同一套语义，
// 三处输入框写同样的东西必须得到同样的结果）。
func matchGroup(pattern, v string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	// 大小写不敏感：IdP 侧的组名大小写不受我们控制，
	// 而"配了映射却不生效"最常见的原因就是大小写对不上。
	p, s := strings.ToLower(pattern), strings.ToLower(v)
	if i := strings.IndexByte(p, '*'); i >= 0 {
		pre, suf := p[:i], p[i+1:]
		return len(s) >= len(pre)+len(suf) &&
			strings.HasPrefix(s, pre) && strings.HasSuffix(s, suf)
	}
	return p == s
}

func dedup(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
