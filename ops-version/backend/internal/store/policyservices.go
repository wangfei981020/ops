package store

import (
	"context"
	"time"
)

// PolicyService 某条复制规则推过的一个服务。
type PolicyService struct {
	ServiceKey string `json:"service_key"`
	// Tags 这条规则推过这个服务的多少个版本
	Tags int `json:"tags"`
	// LastAt 最近一次推它是什么时候（没推过时为 null）。
	//
	// ⚠️ 与 SyncTaskRow.FinishedAt 同一个坑：**别在 SQL 里手拼时区标记**。
	//	`DATE_FORMAT(..., '%Y-%m-%dT%H:%i:%sZ')` 会把本地时间贴上 UTC 标签，
	//	前端再按 UTC 转一次，界面上整整差一个时区。
	LastAt *time.Time `json:"last_at"`
	// Failed 这些推送里有多少是失败的 —— 非零时这个服务要优先看
	Failed int `json:"failed"`
}

// ServicesOfPolicy 一条复制规则推过哪些服务。
//
// 🔴 存在的理由：「按服务查同步」原来要先选 Harbor **再选项目**，
//
//	而项目和「这条规则推什么」根本不是一回事 —— 一条规则可能跨项目，
//	一个项目里也大半服务不在任何规则里。让人先猜一个项目，
//	猜错了就是「至少选一个服务」而下拉里根本没有他要的那个。
//
// 规则本身就定义了覆盖范围，直接从它的复制记录里列服务，
// 既不用猜，列出来的每一个也**一定有同步状态可看**。
//
// policyRef=0 = 不限规则（所有规则推过的服务的并集）。
func (s *Store) ServicesOfPolicy(ctx context.Context, policyRef int64) ([]PolicyService, error) {
	q := `SELECT t.service_key, COUNT(DISTINCT t.tag), MAX(t.finished_at),
	             SUM(CASE WHEN t.status NOT IN ('Succeed','Succeeded') THEN 1 ELSE 0 END)
	        FROM sync_tasks t`
	args := []any{}
	if policyRef > 0 {
		q += ` WHERE t.policy_ref = ?`
		args = append(args, policyRef)
	}
	q += ` GROUP BY t.service_key ORDER BY t.service_key`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PolicyService{}
	for rows.Next() {
		var p PolicyService
		if err := rows.Scan(&p.ServiceKey, &p.Tags, &p.LastAt, &p.Failed); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
