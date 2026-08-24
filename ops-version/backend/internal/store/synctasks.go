package store

import (
	"context"
	"strings"
)

// SyncTaskRow 一次「把某个版本推给某方」的记录。
type SyncTaskRow struct {
	PolicyName string `json:"policy_name"`
	OrgName    string `json:"org_name"`
	ServiceKey string `json:"service_key"`
	Tag        string `json:"tag"`
	Status     string `json:"status"`
	ErrMsg     string `json:"err_msg"`
	FinishedAt string `json:"finished_at"`
}

// SyncTasksOf 查某几个服务的推送记录。
//
// 🔴 这些数据**全部来自我们自己的库**（sync_tasks）——
//
//	不打 Harbor、也不需要知道项目。
//	原来「按服务查同步」要先选 Harbor 再选项目，而项目只是为了
//	「拿我方 Harbor 里的全部 tag」用的，跟「推过去了没有」无关。
//	把这一步摘掉之后，回答「推过去了没有」只要两步：选规则、选服务。
//
// policyRef=0 = 不限规则。services 为空 = 不限服务（调用方负责别一次要太多）。
func (s *Store) SyncTasksOf(ctx context.Context, policyRef int64, services []string, limit int) ([]SyncTaskRow, error) {
	q := `SELECT p.name, COALESCE(o.name,''), t.service_key, t.tag, t.status,
	             COALESCE(t.err_msg,''),
	             COALESCE(DATE_FORMAT(t.finished_at, '%Y-%m-%dT%H:%i:%sZ'), '')
	        FROM sync_tasks t
	        JOIN sync_policies p ON p.id = t.policy_ref
	        LEFT JOIN orgs o ON o.id = p.org_id
	       WHERE 1=1`
	args := []any{}
	if policyRef > 0 {
		q += ` AND t.policy_ref = ?`
		args = append(args, policyRef)
	}
	if len(services) > 0 {
		q += ` AND t.service_key IN (?` + strings.Repeat(",?", len(services)-1) + `)`
		for _, s := range services {
			args = append(args, s)
		}
	}
	// ⚠️ 按时间倒序：人要看的是「最近推的是哪个版本」，不是最早的
	q += ` ORDER BY t.finished_at DESC, t.id DESC LIMIT ?`
	args = append(args, ClampLimit(limit))

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SyncTaskRow{}
	for rows.Next() {
		var r SyncTaskRow
		if err := rows.Scan(&r.PolicyName, &r.OrgName, &r.ServiceKey, &r.Tag,
			&r.Status, &r.ErrMsg, &r.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
