package api

import (
	"strings"
	"time"

	"ops-version-backend/internal/store"
)

// resolveProject 把请求里的 project_id 解析成实际项目。
//
// 🔴 回落顺序：请求指定的 → **这个环境自己挂的** → 第一个启用的。
//
//	中间那档是关键。加项目层之前存下来的方案里没有 project_id，
//	直接回落到「列表第一个」的话，方案打开后会莫名其妙地按某个项目筛过 ——
//	实测中一个 17 个服务的方案缩到了 4 个，而界面上没有任何异常提示。
//	（列表按名字排序，中文排序恰好把新建的「平台组」排到了「默认」前面。）
//	环境自己挂的项目才是真正的归属，与是谁先建的、叫什么名字无关。
//
// ⚠️ id 指向的项目已被删除/停用时同样往下回落而不是报错 ——
//
//	方案里引用一个下线的项目是常态，为此让整次比对失败代价太大。
func resolveProject(list []store.Project, want, envProject int64) (store.Project, bool) {
	for _, id := range []int64{want, envProject} {
		if id == 0 {
			continue
		}
		for _, p := range list {
			if p.ID == id && p.Enabled {
				return p, true
			}
		}
	}
	for _, p := range list {
		if p.Enabled {
			return p, true
		}
	}
	return store.Project{}, false
}

func countEnabledProjects(list []store.Project) int {
	n := 0
	for _, p := range list {
		if p.Enabled {
			n++
		}
	}
	return n
}

// projectAll 请求里用它表示「这一列是整个平台在该环境上的全部项目」。
//
// ⚠️ 用 -1 而不是复用 0：0 已经是「按环境回落到默认项目」的语义，
// 改它会动到所有既有调用方（方案里存的老列、MCP、导出清单都传 0）。
const projectAll int64 = -1

// allProjectsLabel 平台级列在表头显示的项目名。
// 必须显示出来 —— 否则「我方/UAT」既可能是默认项目也可能是整个平台，看不出区别。
const allProjectsLabel = "全部项目"

// rollUpEnvStatus 把一个平台在某环境下**所有项目**的采集状态滚成一个。
//
// 🔴 滚动规则要保守：只要有一个项目没采成功，整列就不能算「数据可信」。
//
//	平台级视图会替整个平台下结论（"A公司 没有这个服务"），而那句话的前提是
//	"我们把 A公司 的每个项目都看过了"。有一个项目没采到就下这个结论，
//	就是把「我们没看到」说成「对方没有」—— 全站最忌讳的那种错。
//
// ⚠️ 取最"坏"的那个状态，而不是第一个/最新的：
//
//	UAT 采成功、PROD 采失败时，如果取第一个，失败那半就此消失。
func rollUpEnvStatus(in store.Org, env string) (status, errMsg string, at time.Time) {
	// 优先级：越靠前越"坏"，一旦命中就不再被更好的状态覆盖
	rank := map[string]int{
		"auth_failed": 5, "forbidden": 5, "unreachable": 5, "error": 5,
		"never": 4, "partial": 3, "success": 1,
	}
	best := 0
	for _, e := range in.Envs {
		if e.Env != env {
			continue
		}
		st := e.LastCollectStatus
		if st == "" {
			st = in.LastSyncStatus // 老数据没有环境级记录
		}
		if r := rank[st]; r > best {
			best, status, errMsg = r, st, e.LastCollectError
		}
		if e.LastCollectAt.Valid && e.LastCollectAt.Time.After(at) {
			at = e.LastCollectAt.Time
		}
	}
	if status == "" {
		status, errMsg = in.LastSyncStatus, in.LastSyncError
		if in.LastSyncAt.Valid {
			at = in.LastSyncAt.Time
		}
	}
	return status, errMsg, at
}

// rollUpDegraded 任一项目走了降级采集，整个平台级列就该标降级。
//
// 🔴 宁可多标：降级意味着副本为 0 的服务看不见，而平台级列一旦漏看一个项目，
// 「这个平台没有这个服务」就可能是错的。
func rollUpDegraded(in store.Org, env string) (bool, string) {
	for _, e := range in.Envs {
		if e.Env == env && e.LastCollectDegraded {
			return true, e.LastCollectDegradedNote
		}
	}
	return false, ""
}

// isPlaceholderProject 这个项目名是不是建平台时自动生成的占位名。
//
// 🔴 占位名不进表头：它不带业务信息，只会让「A公司·默认/UAT」比「A公司/UAT」多两个字，
// 还容易被读成"有一个叫默认的项目"。
//
// ⚠️ 只认这几个确切的写法，不做模糊匹配 —— 客户真把项目命名成「默认线路」
// 之类的时候，那是有意义的名字，不能替人家藏起来。
func isPlaceholderProject(name string) bool {
	switch strings.TrimSpace(name) {
	case "默认", "default", "Default", "DEFAULT":
		return true
	}
	return false
}
