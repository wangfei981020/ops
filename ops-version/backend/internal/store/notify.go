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

func (s *Store) SaveNotifyRecord(ctx context.Context, channelID int64, execRef int64,
	level, trigger, state, reason, errMsg, content string, attempts int,
) error {
	var ch, ex any
	if channelID > 0 {
		ch = channelID
	}
	if execRef > 0 {
		ex = execRef
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO notify_records (channel_id, exec_ref, level, trigger_type, state,
		  reason, err_msg, attempts, content)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		ch, ex, level, trigger, state, truncate(reason, 255), truncate(errMsg, 500), attempts, content)
	return err
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
