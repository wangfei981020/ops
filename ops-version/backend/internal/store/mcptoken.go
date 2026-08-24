package store

import (
	"context"
	"fmt"
	"time"
)

// MaxMCPTokenDays 有效期上限。
// 允许 3650 天等于允许永久，那就把「定期回头看一眼」这件事绕过去了。
const MaxMCPTokenDays = 365

// SetMCPTokenExpiry 给一条已存在的令牌设置/清除有效期。
//
// 🔴 存在的理由：之前发出去的令牌 expires_at 全是 NULL（永不过期），
// 而它们**已经在外部接入方手里**。只让新令牌能设有效期的话，
// 那批永久令牌会一直存在，问题只解决了一半。
//
// days <= 0 = 改成永不过期（允许，但要显式做）。
func (s *Store) SetMCPTokenExpiry(ctx context.Context, id int64, days int) error {
	if days > MaxMCPTokenDays {
		return fmt.Errorf("有效期最多 %d 天 —— 再长就等于永久，"+
			"而永久令牌不会有任何时点提醒你回头看一眼它还该不该存在", MaxMCPTokenDays)
	}
	var expires any
	if days > 0 {
		// ⚠️ 从**现在**起算，不是从创建时间起算：
		//    从创建时间算的话，给一条半年前发的令牌设 90 天 = 当场就过期了，
		//    而操作的人以为自己给了它 90 天。
		expires = time.Now().AddDate(0, 0, days)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE mcp_tokens SET expires_at=? WHERE id=? AND deleted_at IS NULL`, expires, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("令牌不存在")
	}
	return nil
}
