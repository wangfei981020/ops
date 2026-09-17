package store

import (
	"context"
	"database/sql"
	"time"
)

type NotifyChannel struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	HasWebhook bool   `json:"has_webhook"`
	Enabled    bool   `json:"enabled"`
	OrgID      *int64 `json:"org_id"`
	OrgName    string `json:"org_name"`
	WebhookEnc string `json:"-"`
}

func (s *Store) ListChannels(ctx context.Context) ([]NotifyChannel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.kind, COALESCE(c.webhook_enc,''), c.enabled, c.org_id, COALESCE(o.name,'')
		  FROM notify_channels c
		  LEFT JOIN orgs o ON o.id = c.org_id
		 WHERE c.deleted_at IS NULL ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyChannel{}
	for rows.Next() {
		var c NotifyChannel
		var enc string
		var en int
		var org sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &enc, &en, &org, &c.OrgName); err != nil {
			return nil, err
		}
		c.WebhookEnc = enc
		// 🔴 只说配没配，永不回显 —— webhook 里带 token，等同于凭据
		c.HasWebhook = enc != ""
		c.Enabled = en == 1
		if org.Valid {
			v := org.Int64
			c.OrgID = &v
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type ChannelInput struct {
	Name       string
	Kind       string
	WebhookEnc string // 空 = 不改
	Enabled    bool
	OrgID      int64 // 0 = 全部组织
}

func (s *Store) SaveChannel(ctx context.Context, id int64, in ChannelInput) (int64, error) {
	var org any
	if in.OrgID > 0 {
		org = in.OrgID
	}
	if id == 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO notify_channels (name, kind, webhook_enc, enabled, org_id)
			VALUES (?,?,?,?,?)`,
			in.Name, in.Kind, nullIfEmpty(in.WebhookEnc), boolToInt(in.Enabled), org)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	q := `UPDATE notify_channels SET name=?, kind=?, enabled=?, org_id=?`
	args := []any{in.Name, in.Kind, boolToInt(in.Enabled), org}
	// 空 = 不改，不是清空。表单不回显 webhook，提交时那栏本来就是空的
	if in.WebhookEnc != "" {
		q += `, webhook_enc=?`
		args = append(args, in.WebhookEnc)
	}
	q += ` WHERE id=?`
	args = append(args, id)
	_, err := s.db.ExecContext(ctx, q, args...)
	return id, err
}

func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notify_channels SET deleted_at=NOW() WHERE id=?`, id)
	return err
}

// NotifyRecord 一条投递记录。
type NotifyRecord struct {
	ID       int64     `json:"id"`
	Channel  string    `json:"channel"`
	Level    string    `json:"level"`
	Trigger  string    `json:"trigger_type"`
	State    string    `json:"state"`
	Reason   string    `json:"reason"`
	ErrMsg   string    `json:"err_msg"`
	Attempts int       `json:"attempts"`
	Content  string    `json:"content"`
	At       time.Time `json:"created_at"`
}

// NotifyRecordInput 一条投递记录要写进去的东西。
//
// ⚠️ 用结构体而不是一串位置参数：原来是 9 个位置参数、其中 5 个是 string，
// `reason` 和 `errMsg` 挨着、`state` 和 `level` 挨着，写反了编译照过。
type NotifyRecordInput struct {
	ChannelID int64 // 0 = 没走到任何渠道（判定不发 / 一个渠道都没配）
	ExecRef   int64 // sync_executions.id；webhook 路径到达时那一行还不存在，传 0
	// PolicyRef + HarborExecID 是**去重键**：webhook 实时发过的那次 execution，
	// 采集器轮询到时不再发。见 029 迁移。
	PolicyRef    int64
	HarborExecID int64
	Level        string // ok | failed
	Trigger      string
	State        string // sent | skipped | failed
	Reason       string
	ErrMsg       string
	Content      string
	Attempts     int
}

func (s *Store) SaveNotifyRecord(ctx context.Context, in NotifyRecordInput) error {
	var ch, ex, pol any
	if in.ChannelID > 0 {
		ch = in.ChannelID
	}
	if in.ExecRef > 0 {
		ex = in.ExecRef
	}
	if in.PolicyRef > 0 {
		pol = in.PolicyRef
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO notify_records (channel_id, exec_ref, policy_ref, harbor_exec_id,
		  level, trigger_type, state, reason, err_msg, attempts, content)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		ch, ex, pol, in.HarborExecID, in.Level, in.Trigger, in.State,
		truncate(in.Reason, 255), truncate(in.ErrMsg, 500), in.Attempts, in.Content)
	return err
}

// AlreadyNotified 这条规则的这次 execution 是不是已经成功通知过了。
//
// 🔴 只认 state='sent'。skipped（判定不该发）和 failed（该发没发出去）都不算 ——
// 把 failed 也当"已通知"的话，webhook 投递失败之后采集器就不会补发了，
// 而"webhook 发失败"恰恰是采集器这条兜底路径存在的理由。
//
// ⚠️ execID=0（拿不到 Harbor 的 execution id）时一律返回 false：
// 0 不是一个真实的 execution，拿它当键会把所有拿不到 id 的通知**互相**去重掉。
func (s *Store) AlreadyNotified(ctx context.Context, policyRef, execID int64) (bool, error) {
	if policyRef <= 0 || execID <= 0 {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM notify_records
		 WHERE policy_ref=? AND harbor_exec_id=? AND state='sent'`, policyRef, execID).Scan(&n)
	return n > 0, err
}

func (s *Store) ListNotifyRecords(ctx context.Context, limit int) ([]NotifyRecord, error) {
	// 统一口径见 paging.go：超上限**钳制**到上限，不掉回默认值
	limit = ClampLimit(limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, COALESCE(c.name,''), r.level, r.trigger_type, r.state,
		       r.reason, r.err_msg, r.attempts, COALESCE(r.content,''), r.created_at
		  FROM notify_records r
		  LEFT JOIN notify_channels c ON c.id = r.channel_id
		 ORDER BY r.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyRecord{}
	for rows.Next() {
		var r NotifyRecord
		if err := rows.Scan(&r.ID, &r.Channel, &r.Level, &r.Trigger, &r.State,
			&r.Reason, &r.ErrMsg, &r.Attempts, &r.Content, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
