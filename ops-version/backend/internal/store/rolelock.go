package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DefaultRoleLockDays 角色锁定的默认期限。
//
// 🔴 为什么必须有期限，而不是"锁上就一直锁着"：
//
//	永久锁会**悄悄和 IdP 脱节**。人调岗了、从组里被移出去了，
//	锁着的角色还在，而没有任何东西提醒你去看一眼。
//	加一个到期日，等于强制每隔一段时间重新确认"这个例外还成不成立"。
//
// 90 天是权衡：短了变成每月都要处理一堆续期，长了又失去复核的意义。
// ⚠️ 这是**策略**不是数据，所以定在代码里，不写进库的 DEFAULT。
const DefaultRoleLockDays = 90

// MaxRoleLockDays 期限上限。
// 允许填 3650 天等于允许永久锁，那就把上面那条理由绕过去了。
const MaxRoleLockDays = 365

// SetRoleLock 锁定或解锁一个用户的角色。
//
// days <= 0 表示**解锁**（恢复成跟着 IdP 组走）。
func (s *Store) SetRoleLock(ctx context.Context, userID int64, days int, reason string) error {
	if days <= 0 {
		_, err := s.db.ExecContext(ctx,
			`UPDATE users SET role_locked_until=NULL, role_lock_reason='' WHERE id=?`, userID)
		return err
	}
	if days > MaxRoleLockDays {
		return fmt.Errorf("锁定期限最多 %d 天 —— 再长就等于永久锁，"+
			"而永久锁会让这个人的角色永远不再跟 IdP 对齐", MaxRoleLockDays)
	}
	// 🔴 理由必填。到期复核时，看到一条没有理由的锁，
	//    唯一能做的决定是"续期吧，万一有用呢" —— 那这个机制就废了。
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("要写清楚为什么给这个人开例外 —— 到期复核时要看这句话")
	}
	// ⚠️ 到期时间在 Go 这边算，不用 MySQL 的 DATE_ADD(NOW(),...)：
	//    NOW() 走的是**服务端时区**，而本系统的时间语义统一按 Go 的 time.Local。
	//    两套时区混用会让"还有几天到期"差上几个小时，跨天时就是差一天。
	until := time.Now().AddDate(0, 0, days)
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET role_locked_until=?, role_lock_reason=? WHERE id=?`,
		until, strings.TrimSpace(reason), userID)
	return err
}

// ExpiringRoleLocks 快到期的锁定。
//
// 🔴 存在的意义就是**让到期这件事被看见**。
// 只在库里放一个到期时间、没人去看的话，到期那天用户的角色会突然变回组映射的值，
// 而管理员完全不知道发生了什么 —— 「他的权限怎么自己变了」。
func (s *Store) ExpiringRoleLocks(ctx context.Context, within time.Duration) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, username, display_name, role_code, role_locked_until, role_lock_reason
		  FROM users
		 WHERE deleted_at IS NULL AND role_locked_until IS NOT NULL
		   AND role_locked_until <= ?
		 ORDER BY role_locked_until`, time.Now().Add(within))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.RoleCode,
			&u.RoleLockedUntil, &u.RoleLockReason); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
