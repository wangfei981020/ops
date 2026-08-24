package api

import (
	"net/http"
	"strconv"
)

// listOrgServices 某个平台已采集到的服务名清单。
//
// 🔴 存在的理由：项目的「指定服务」原来是手打的。
//
//	168 个服务没人会手填，填了会拼错 —— 而拼错**不报错**，
//	只是那个服务永远不出现在这个项目下，配置页上看着完全正常。
//	让人从已采到的清单里勾，拼错就不可能发生。
//
// ⚠️ 返回的是「采集到的事实」，不是「配置里写过的」——
//
//	两者的差别正是这个接口的价值：配置里写了但对方没部署的，
//	不该出现在勾选列表里让人以为它存在。
func (s *Server) listOrgServices(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if !userOf(r).Scope().CanSee(id) {
		fail(w, http.StatusForbidden, "forbidden", "没有权限查看这个平台")
		return
	}
	env := r.URL.Query().Get("env")
	list, err := s.St.CollectedServices(r.Context(), id, env)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ok(w, list)
}
