package store

import "context"

// CollectedService 一个采集到的服务，带它出现在哪些环境里。
type CollectedService struct {
	ServiceKey string   `json:"service_key"`
	Envs       []string `json:"envs"`
}

// CollectedServices 某平台采集到的全部服务名。
//
// env 为空 = 不限环境（各环境的并集）。
//
// ⚠️ 带上 envs：同一个服务可能只在 UAT 有、PROD 没有。
// 勾选时看不到这个，人会以为「勾了就两边都算」，而实际只有一边有。
func (s *Store) CollectedServices(ctx context.Context, orgID int64, env string) ([]CollectedService, error) {
	q := `SELECT service_key, env FROM service_versions WHERE org_id=?`
	args := []any{orgID}
	if env != "" {
		q += ` AND env=?`
		args = append(args, env)
	}
	q += ` ORDER BY service_key, env`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	idx := map[string]int{}
	out := []CollectedService{}
	for rows.Next() {
		var k, e string
		if err := rows.Scan(&k, &e); err != nil {
			return nil, err
		}
		if i, seen := idx[k]; seen {
			out[i].Envs = append(out[i].Envs, e)
			continue
		}
		idx[k] = len(out)
		out = append(out, CollectedService{ServiceKey: k, Envs: []string{e}})
	}
	return out, rows.Err()
}
