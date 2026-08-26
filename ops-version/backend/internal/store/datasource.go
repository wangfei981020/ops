package store

import (
	"context"
	"database/sql"
	"strings"
)

// Datasource 连接信息，可被多个平台共用。
//
// 🔴 从平台里独立出来的理由：同一个 Rancher/ArgoCD/Kite 常被 N 个平台共用。
// 之前要把地址和凭据重复配 N 遍 —— 改一次密码要改 N 处，
// 漏一处就是一个平台悄悄采集失败，而失败原因是"认证失败"，
// 没人会想到是"另外那处忘了改"。
type Datasource struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	ProviderType  string `json:"provider_type"`
	Endpoint      string `json:"endpoint"`
	AuthType      string `json:"auth_type"`
	CredentialEnc string `json:"-"` // 永不回显
	HasCredential bool   `json:"has_credential"`
	InsecureTLS   bool   `json:"insecure_tls"`
	Enabled       bool   `json:"enabled"`
	// UsedBy 有几个平台在用 —— 删除前要看这个
	UsedBy int `json:"used_by"`
}

func (s *Store) ListDatasources(ctx context.Context) ([]Datasource, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.name, d.provider_type, d.endpoint, d.auth_type,
		       d.credential_enc, d.insecure_tls, d.enabled,
		       (SELECT COUNT(*) FROM orgs o
		         WHERE o.datasource_id = d.id AND o.deleted_at IS NULL) AS used_by
		  FROM datasources d WHERE d.deleted_at IS NULL ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Datasource{}
	for rows.Next() {
		var d Datasource
		var cred sql.NullString
		var insecure, enabled int
		if err := rows.Scan(&d.ID, &d.Name, &d.ProviderType, &d.Endpoint, &d.AuthType,
			&cred, &insecure, &enabled, &d.UsedBy); err != nil {
			return nil, err
		}
		d.CredentialEnc = cred.String
		d.HasCredential = strings.TrimSpace(cred.String) != ""
		d.InsecureTLS, d.Enabled = insecure == 1, enabled == 1
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDatasource(ctx context.Context, id int64) (Datasource, error) {
	list, err := s.ListDatasources(ctx)
	if err != nil {
		return Datasource{}, err
	}
	for _, d := range list {
		if d.ID == id {
			return d, nil
		}
	}
	return Datasource{}, ErrNotFound
}

// SaveDatasource 建或改。credentialEnc 为空 = **不改动**已有凭据。
//
// ⚠️ 与平台/环境凭据同一条规矩：凭据永不回显，所以前端提交上来多数是空的，
// 那表示"没改"不是"清空"。
func (s *Store) SaveDatasource(ctx context.Context, id int64, d Datasource) (int64, error) {
	if id == 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO datasources (name, provider_type, endpoint, auth_type,
			  credential_enc, insecure_tls, enabled)
			VALUES (?,?,?,?,?,?,?)`,
			d.Name, d.ProviderType, d.Endpoint, d.AuthType,
			nullIfEmpty(d.CredentialEnc), boolToInt(d.InsecureTLS), boolToInt(d.Enabled))
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	q := `UPDATE datasources SET name=?, provider_type=?, endpoint=?, auth_type=?,
	        insecure_tls=?, enabled=?`
	args := []any{d.Name, d.ProviderType, d.Endpoint, d.AuthType,
		boolToInt(d.InsecureTLS), boolToInt(d.Enabled)}
	if strings.TrimSpace(d.CredentialEnc) != "" {
		q += `, credential_enc=?`
		args = append(args, d.CredentialEnc)
	}
	q += ` WHERE id=? AND deleted_at IS NULL`
	args = append(args, id)
	_, err := s.db.ExecContext(ctx, q, args...)
	return id, err
}

// DeleteDatasource 软删。
//
// 🔴 有平台在用就拒绝 —— 删了之后那些平台会变成"没有连接信息"，
// 而表现是采集失败且原因是"未配置地址"，看不出是被谁删的。
func (s *Store) DeleteDatasource(ctx context.Context, id int64) error {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM orgs WHERE datasource_id=? AND deleted_at IS NULL`, id).
		Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE datasources SET deleted_at=NOW() WHERE id=? AND deleted_at IS NULL`, id)
	return err
}

// Project 一个平台下的一个项目。对比表的一列 = 项目 × 环境。
type Project struct {
	ID    int64  `json:"id"`
	OrgID int64  `json:"org_id"`
	Name  string `json:"name"`
	// ServiceInclude 服务名通配（biz-*）。多项目挤在同一 ns 时用它区分
	ServiceInclude []string `json:"service_include"`
	// ServicePins 手工指定的确切服务名。
	// ⚠️ 界面上必须**从已采集的服务列表勾选**，不能让人手打 ——
	// 168 个服务没人会填，填了会拼错，而拼错时不报错，只是那个服务永远不出现。
	ServicePins []string `json:"service_pins"`
	SortOrder   int      `json:"sort_order"`
	Enabled     bool     `json:"enabled"`
	EnvCount    int      `json:"env_count"`
}

func (s *Store) ListProjects(ctx context.Context, orgID int64) ([]Project, error) {
	// 🔴 必须 JOIN orgs 并检查组织还在不在：只看项目自己的 deleted_at 的话，
	//    组织被删掉之后它的项目仍会留在列表里 —— 一个属于"看不见的组织"的项目
	//    常驻项目列表，界面上既删不掉也说不清归属。
	// ⚠️ DeleteOrg 本身是对的（会级联软删 projects），这类残留是
	//    级联逻辑补上**之前**删的组织留下的历史数据 —— 所以光修 DeleteOrg 不够，
	//    查询这一侧也得挡住，否则老数据永远漏出来。
	q := `SELECT p.id, p.org_id, p.name, p.service_include, p.service_pins,
	             p.sort_order, p.enabled,
	             (SELECT COUNT(*) FROM org_envs e WHERE e.project_id = p.id) AS env_count
	        FROM projects p
	        JOIN orgs o ON o.id = p.org_id AND o.deleted_at IS NULL
	       WHERE p.deleted_at IS NULL`
	args := []any{}
	if orgID > 0 {
		q += ` AND p.org_id = ?`
		args = append(args, orgID)
	}
	q += ` ORDER BY p.org_id, p.sort_order, p.name`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		var inc, pins sql.NullString
		var enabled int
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &inc, &pins,
			&p.SortOrder, &enabled, &p.EnvCount); err != nil {
			return nil, err
		}
		// 🔴 nil 会被序列化成 JSON `null`，而前端类型写的是 string[] ——
		//    `p.service_include.length` 当场抛异常，**整个弹窗白屏**，
		//    而接口本身 200、日志里什么都没有。空列表必须是 []，不是 null。
		p.ServiceInclude = orEmpty(splitLines(inc.String))
		p.ServicePins = orEmpty(splitLines(pins.String))
		p.Enabled = enabled == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SaveProject(ctx context.Context, id int64, p Project) (int64, error) {
	if id == 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO projects (org_id, name, service_include, service_pins, sort_order, enabled)
			VALUES (?,?,?,?,?,?)`,
			p.OrgID, p.Name, joinLines(p.ServiceInclude), joinLines(p.ServicePins),
			p.SortOrder, boolToInt(p.Enabled))
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE projects SET name=?, service_include=?, service_pins=?, sort_order=?, enabled=?
		 WHERE id=? AND deleted_at IS NULL`,
		p.Name, joinLines(p.ServiceInclude), joinLines(p.ServicePins),
		p.SortOrder, boolToInt(p.Enabled), id)
	return id, err
}

// DeleteProject 软删。
//
// 🔴 还有环境挂着就拒绝：删了之后那些环境会成为孤儿 ——
// 不参与任何比对，也不在界面上，只是静静躺在库里。
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM org_envs WHERE project_id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET deleted_at=NOW() WHERE id=? AND deleted_at IS NULL`, id)
	return err
}

// orEmpty 保证切片出参永远是 []，不是 null。
func orEmpty(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}
