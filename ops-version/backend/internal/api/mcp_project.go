package api

import (
	"context"
	"fmt"
	"strings"

	"ops-version-backend/internal/compare"
	"ops-version-backend/internal/store"
)

// applyProjectFilter 按项目名收窄一列。
//
// 🔴 名字对不上时**报错**，不是静默不筛。
//
//	静默的结果是 AI 拿到一份跨项目全量，却以为是某一个项目的清单 ——
//	它不会怀疑，会直接拿去回答「项目B 上线了哪些服务」。
//	对人来说少一列很显眼，对 AI 来说多一堆服务完全看不出来。
//
// 留空 = 不筛，跨项目全量。这是 MCP 的合理默认：调用方按「平台+环境」寻址，
// 多数问题问的也是整个环境。
func applyProjectFilter(ctx context.Context, st *store.Store, col *compare.Column, name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	list, err := st.ListProjects(ctx, col.OrgID)
	if err != nil {
		return err
	}
	for _, p := range list {
		if p.Name == name {
			if !p.Enabled {
				return fmt.Errorf("项目 %q 已停用", name)
			}
			col.ProjectID = p.ID
			col.ProjectName = p.Name
			col.Filter = compare.ProjectFilter{Include: p.ServiceInclude, Pins: p.ServicePins}
			return nil
		}
	}
	names := make([]string, 0, len(list))
	for _, p := range list {
		names = append(names, p.Name)
	}
	return fmt.Errorf("平台 %s 下没有项目 %q，现有：%s",
		col.OrgName, name, strings.Join(names, "、"))
}

// projectNote 回给调用方的项目字段。
// 留空时要说清是「跨项目全量」，而不是回一个空串让人以为没有项目这回事。
func projectNote(name string) string {
	if strings.TrimSpace(name) == "" {
		return "(全部项目)"
	}
	return name
}
