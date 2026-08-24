package store

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

func auditStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_MIG_DSN")
	if dsn == "" {
		t.Skip("需要 TEST_MIG_DSN")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{db: db}
}

// 🔴 现象二：要多了反而给得更少。
//
// 原来是 `if limit <= 0 || limit > 500 { limit = 100 }` ——
// 传 501 比传 500 少拿一半，而且没有任何提示。
// 「没指定」和「要多了」被当成了同一种情况。
func TestAuditLimitClampsNotResets(t *testing.T) {
	s := auditStore(t)
	ctx := context.Background()
	over, err := s.ListAudit(ctx, MaxPageLimit+1, 0)
	if err != nil {
		t.Fatal(err)
	}
	at, err := s.ListAudit(ctx, MaxPageLimit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(over.Rows) != len(at.Rows) {
		t.Errorf("传 %d 拿到 %d 条，传 %d 拿到 %d 条 —— 超上限应**钳制**到上限，"+
			"而不是掉回默认值", MaxPageLimit+1, len(over.Rows), MaxPageLimit, len(at.Rows))
	}
}

// 🔴 现象一：500 条之前的历史必须取得到。
//
// 用游标翻页遍历全表，条数要对得上 total，且不能有重复。
func TestAuditCursorPaginationCoversAll(t *testing.T) {
	s := auditStore(t)
	ctx := context.Background()
	first, err := s.ListAudit(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total == 0 {
		t.Skip("库里没有审计记录")
	}
	seen := map[int64]bool{}
	before := int64(0)
	pages := 0
	for {
		p, err := s.ListAudit(ctx, 10, before)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range p.Rows {
			if seen[r.ID] {
				t.Fatalf("id=%d 在两页里都出现了 —— 游标分页不该重复", r.ID)
			}
			seen[r.ID] = true
		}
		pages++
		if p.NextBefore == 0 {
			break
		}
		before = p.NextBefore
		if pages > 500 {
			t.Fatal("翻页没有终止 —— NextBefore 没有正确收敛")
		}
	}
	if len(seen) != first.Total {
		t.Errorf("翻完拿到 %d 条，而 total=%d —— 有记录取不到", len(seen), first.Total)
	}
	t.Logf("翻了 %d 页，覆盖 %d 条", pages, len(seen))
}

// ⚠️ 最后一页不该再给游标 —— 否则界面上「下一页」永远点得动，点进去是空的
func TestAuditLastPageHasNoCursor(t *testing.T) {
	s := auditStore(t)
	ctx := context.Background()
	p, err := s.ListAudit(ctx, MaxPageLimit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total < MaxPageLimit && p.NextBefore != 0 {
		t.Errorf("总共才 %d 条、一页就取完了，却仍给了游标 %d", p.Total, p.NextBefore)
	}
}

// 🔴 现象三：必须给 total，否则人不知道自己看到的是不是全部
func TestAuditReportsTotal(t *testing.T) {
	s := auditStore(t)
	p, err := s.ListAudit(context.Background(), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total < len(p.Rows) {
		t.Errorf("total=%d 小于本页条数 %d", p.Total, len(p.Rows))
	}
}
