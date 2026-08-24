package store

import (
	"context"
	"fmt"
)

// SyncGapReason 一个平台**为什么**没有复制记录。
//
// 🔴 三种成因的下一步完全不同，绝不能混成一句话：
//
//	没绑规则   → 去镜像同步页把规则绑到这个平台
//	绑了没数据 → 点「立即拉取」，或者这个平台确实还没被推过东西
//	查询失败   → 去看 Harbor 的权限 / 连通性
//
// 原来三种都显示成「未绑定复制规则」，用户照它去查绑定，
// 而绑定明明是对的 —— 真因是覆盖面不够。
func (s *Store) SyncGapReason(ctx context.Context, orgID int64, orgName string, queryErr error) string {
	if queryErr != nil {
		return fmt.Sprintf("复制记录查询失败：%s —— 这不是「没同步」，是我们没读到",
			queryErr.Error())
	}

	var bound int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_policies p
		   JOIN harbors h ON h.id = p.harbor_id
		  WHERE p.org_id = ? AND h.deleted_at IS NULL`, orgID).Scan(&bound); err != nil {
		return "无法判断同步状态（读复制规则时出错）"
	}
	if bound == 0 {
		// ⚠️ 顺带告诉他**有没有别的规则可绑** —— 一条都没有的话，
		//    要做的是先去 Harbor 拉规则，而不是找一条来绑。
		var total int
		_ = s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sync_policies p JOIN harbors h ON h.id = p.harbor_id
			  WHERE h.deleted_at IS NULL`).Scan(&total)
		if total == 0 {
			return "还没有任何复制规则 —— 去「镜像同步」页点「立即拉取」把 Harbor 的规则同步过来"
		}
		return fmt.Sprintf(
			"没有任何复制规则「推给 %s」—— 去「镜像同步」页，"+
				"在规则的「推给哪个平台」里选上它（现有 %d 条规则）", orgName, total)
	}

	// 绑了规则却一条记录都没有
	return fmt.Sprintf(
		"已有 %d 条复制规则推给 %s，但一条复制记录都没拉到 —— "+
			"去「镜像同步」页点「立即拉取」；已经拉过的话，说明这些规则还没真正执行过",
		bound, orgName)
}
