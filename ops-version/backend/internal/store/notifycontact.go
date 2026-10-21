package store

import (
	"context"
	"database/sql"
	"strings"
)

// NotifyContact 通知人：Teams 渠道同步失败时 @ 的人（见迁移 031）。
type NotifyContact struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	TeamsAccount string `json:"teams_account"`
	Note         string `json:"note"`
	Enabled      bool   `json:"enabled"`
	// Channels 勾选了他的渠道数，删人前让人看得到影响面
	Channels int `json:"channels"`
}

func (s *Store) ListContacts(ctx context.Context) ([]NotifyContact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.teams_account, c.note, c.enabled,
		       (SELECT COUNT(*) FROM notify_channel_mentions m
		          JOIN notify_channels ch ON ch.id = m.channel_id AND ch.deleted_at IS NULL
		         WHERE m.contact_id = c.id)
		  FROM notify_contacts c ORDER BY c.name, c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyContact{}
	for rows.Next() {
		var c NotifyContact
		var en int
		if err := rows.Scan(&c.ID, &c.Name, &c.TeamsAccount, &c.Note, &en, &c.Channels); err != nil {
			return nil, err
		}
		c.Enabled = en == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveContact 新建（id=0）或修改通知人。调用方负责校验并规整账号（小写）。
func (s *Store) SaveContact(ctx context.Context, id int64, c NotifyContact) (int64, error) {
	if id == 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO notify_contacts (name, teams_account, note, enabled) VALUES (?,?,?,?)`,
			c.Name, c.TeamsAccount, c.Note, boolToInt(c.Enabled))
		if err != nil {
			return 0, friendlyDBErr(err)
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE notify_contacts SET name=?, teams_account=?, note=?, enabled=? WHERE id=?`,
		c.Name, c.TeamsAccount, c.Note, boolToInt(c.Enabled), id)
	if err != nil {
		return 0, friendlyDBErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 值没变也是 0 行，再确认一下是不是真的不存在
		var x int
		if s.db.QueryRowContext(ctx, `SELECT 1 FROM notify_contacts WHERE id=?`, id).Scan(&x) == sql.ErrNoRows {
			return 0, ErrNotFound
		}
	}
	return id, nil
}

// DeleteContact 删通知人，连同各渠道对他的勾选。
func (s *Store) DeleteContact(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM notify_channel_mentions WHERE contact_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notify_contacts WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ChannelMentions 一个渠道同步失败时要 @ 的人：只返回启用的。
func (s *Store) ChannelMentions(ctx context.Context, channelID int64) ([]NotifyContact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.teams_account
		  FROM notify_channel_mentions m
		  JOIN notify_contacts c ON c.id = m.contact_id
		 WHERE m.channel_id = ? AND c.enabled = 1
		 ORDER BY c.name, c.id`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyContact{}
	for rows.Next() {
		c := NotifyContact{Enabled: true}
		if err := rows.Scan(&c.ID, &c.Name, &c.TeamsAccount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// channelMentionIDs 全部渠道勾选的通知人 id（含停用的人，表单要能显示出来）。
func (s *Store) channelMentionIDs(ctx context.Context) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT channel_id, contact_id FROM notify_channel_mentions ORDER BY channel_id, contact_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var ch, c int64
		if err := rows.Scan(&ch, &c); err != nil {
			return nil, err
		}
		out[ch] = append(out[ch], c)
	}
	return out, rows.Err()
}

// setChannelMentions 整体替换一个渠道的勾选。不存在的通知人 id 直接忽略
// （表单打开后有人把通知人删了），不让整次保存失败。
func setChannelMentions(ctx context.Context, tx *sql.Tx, channelID int64, ids []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM notify_channel_mentions WHERE channel_id=?`, channelID); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{channelID}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO notify_channel_mentions (channel_id, contact_id)
		SELECT ?, id FROM notify_contacts WHERE id IN (`+ph+`)`, args...)
	return err
}
