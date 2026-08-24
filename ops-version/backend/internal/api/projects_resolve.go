package api

import "ops-version-backend/internal/store"

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
