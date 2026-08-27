package store

import (
	"context"
	"strings"
	"time"
)

// SyncTaskRow 一次「把某个版本推给某方」的记录。
type SyncTaskRow struct {
	PolicyName string `json:"policy_name"`
	OrgName    string `json:"org_name"`
	ServiceKey string `json:"service_key"`
	Tag        string `json:"tag"`
	Status     string `json:"status"`
	ErrMsg     string `json:"err_msg"`
	// FinishedAt 这次推送完成的时刻。
	//
	// 🔴 用 *time.Time，**不要在 SQL 里自己拼时区标记**。
	//
	//	原来是 `DATE_FORMAT(t.finished_at, '%Y-%m-%dT%H:%i:%sZ')` ——
	//	末尾那个 Z 是**硬编码**的。而列里存的是本地时区的墙钟值，
	//	于是「北京时间 09:49」被贴上 UTC 标签发给前端，
	//	`new Date()` 照 UTC 解析再转回本地，界面显示成 **17:49**。
	//	整整差一个时区，而**数据本身是对的**，错的只有那一个字母。
	//
	//	生产实测（2026-08-27）：Harbor 界面 09:49，本站显示 17:49。
	//
	// ⚠️ 交给驱动（按 DSN 里钉住的会话时区解析）和 encoding/json
	//	（序列化成带偏移的 RFC3339）—— 它们用的是同一套基准，
	//	不会像手写字符串那样各说各话。
	FinishedAt *time.Time `json:"finished_at"`
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
	             COALESCE(t.err_msg,''), t.finished_at
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
