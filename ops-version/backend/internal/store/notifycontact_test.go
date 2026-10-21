package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

func TestNotifyContactsAndMentions(t *testing.T) {
	dsn := os.Getenv("TEST_MIG_DSN")
	if dsn == "" {
		t.Skip("需要 TEST_MIG_DSN")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Store{db: db}
	ctx := context.Background()
	t.Cleanup(func() {
		db.Exec(`DELETE FROM notify_channel_mentions WHERE channel_id IN (SELECT id FROM notify_channels WHERE name LIKE 'zz-通知人%')`)
		db.Exec(`DELETE FROM notify_channels WHERE name LIKE 'zz-通知人%'`)
		db.Exec(`DELETE FROM notify_contacts WHERE teams_account LIKE 'zz-%'`)
	})

	a, err := s.SaveContact(ctx, 0, NotifyContact{Name: "Alice", TeamsAccount: "zz-alice@example.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.SaveContact(ctx, 0, NotifyContact{Name: "Bob", TeamsAccount: "zz-bob@example.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// 同一账号不能建两个通知人，报错要是人话
	if _, err := s.SaveContact(ctx, 0, NotifyContact{Name: "Alice2", TeamsAccount: "zz-alice@example.com", Enabled: true}); err == nil ||
		!strings.Contains(err.Error(), "已经有通知人") {
		t.Fatalf("重复账号应报友好错误，得到 %v", err)
	}
	if _, err := s.SaveContact(ctx, 999999999, NotifyContact{Name: "x", TeamsAccount: "zz-x@example.com"}); err != ErrNotFound {
		t.Fatalf("改不存在的人应 ErrNotFound，得到 %v", err)
	}

	// 新建渠道同时勾两个人，外加一个不存在的 id（应被忽略而不是整次失败）
	ch, err := s.SaveChannel(ctx, 0, ChannelInput{Name: "zz-通知人-teams", Kind: "teams", Enabled: true,
		MentionIDs: []int64{a, b, 999999999}})
	if err != nil {
		t.Fatal(err)
	}
	ms, err := s.ChannelMentions(ctx, ch)
	if err != nil || len(ms) != 2 {
		t.Fatalf("应 @ 2 人，得到 %d（%v）", len(ms), err)
	}

	// MentionIDs=nil 表示不改
	if _, err := s.SaveChannel(ctx, ch, ChannelInput{Name: "zz-通知人-teams", Kind: "teams", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if ms, _ = s.ChannelMentions(ctx, ch); len(ms) != 2 {
		t.Fatalf("nil 不应改动勾选，得到 %d", len(ms))
	}

	// 停用 Bob：不再 @，但勾选保留（表单还看得到）
	if _, err := s.SaveContact(ctx, b, NotifyContact{Name: "Bob", TeamsAccount: "zz-bob@example.com", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if ms, _ = s.ChannelMentions(ctx, ch); len(ms) != 1 || ms[0].Name != "Alice" {
		t.Fatalf("停用的人不该被 @：%+v", ms)
	}
	list, err := s.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.ID == ch && len(c.MentionIDs) != 2 {
			t.Fatalf("渠道列表应仍显示 2 个勾选，得到 %v", c.MentionIDs)
		}
	}
	contacts, _ := s.ListContacts(ctx)
	for _, c := range contacts {
		if c.ID == a && c.Channels != 1 {
			t.Fatalf("Alice 应被 1 个渠道勾选，得到 %d", c.Channels)
		}
	}

	// 删 Alice：关联一起删
	if err := s.DeleteContact(ctx, a); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM notify_channel_mentions WHERE contact_id=?`, a).Scan(&n)
	if n != 0 {
		t.Fatalf("删人后关联应清掉，还剩 %d", n)
	}

	// 空切片 = 清空
	if _, err := s.SaveChannel(ctx, ch, ChannelInput{Name: "zz-通知人-teams", Kind: "teams", Enabled: true, MentionIDs: []int64{}}); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM notify_channel_mentions WHERE channel_id=?`, ch).Scan(&n)
	if n != 0 {
		t.Fatalf("空切片应清空勾选，还剩 %d", n)
	}
}
