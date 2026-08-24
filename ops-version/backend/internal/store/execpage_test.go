package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// 🔴 回归：游标必须和**排序键**一致。
//
// 执行记录按 (started_at DESC, id DESC) 排，而我第一版拿 id 当游标 ——
// Harbor 拉回来的执行记录**插入顺序与执行时间无关**（补拉历史时尤其明显），
// 于是 `id < 上一页最小 id` 切出来的集合和「按时间排在后面」不是同一批。
//
// 实测栽过：25 条、每页 10，翻到第三页只剩 1 条，而且和第一页有重叠。
//
// 这条测试特意把 **id 顺序和时间顺序造成相反的**，只有复合游标才能过。
func TestExecutionPagingCursorMatchesOrder(t *testing.T) {
	dsn := os.Getenv("TEST_MIG_DSN")
	if dsn == "" {
		t.Skip("需要 TEST_MIG_DSN")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{db: db}
	ctx := context.Background()

	// 造一条规则 + 25 条执行，**id 越大时间越早**（与排序相反）
	var harborID int64
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM harbors WHERE deleted_at IS NULL LIMIT 1`).Scan(&harborID); err != nil {
		t.Skip("库里没有 Harbor，跳过")
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO sync_policies (harbor_id, policy_id, name, dest_registry, trigger_type, enabled)
		VALUES (?, 999001, 'zz-分页测试', 'x', 'manual', 1)`, harborID)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	t.Cleanup(func() {
		c := context.Background()
		_, _ = db.ExecContext(c, `DELETE FROM sync_executions WHERE policy_ref=?`, pid)
		_, _ = db.ExecContext(c, `DELETE FROM sync_policies WHERE id=?`, pid)
	})

	const total = 25
	base := time.Now()
	for i := 0; i < total; i++ {
		// i 越大 → id 越大 → 时间越早
		at := base.Add(-time.Duration(i) * time.Hour)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO sync_executions (policy_ref, exec_id, trigger_type, status,
			  total, succeeded, failed, started_at, ended_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			pid, 900000+i, "manual", "Succeed", 1, 1, 0, at, at); err != nil {
			t.Fatal(err)
		}
	}

	// 翻页遍历，只数这条规则的记录
	seen := map[int64]bool{}
	var beforeAt *time.Time
	var beforeID int64
	for page := 0; page < 20; page++ {
		p, err := s.ListExecutions(ctx, 10, beforeAt, beforeID)
		if err != nil {
			t.Fatal(err)
		}
		hit := 0
		for _, r := range p.Rows {
			if r.PolicyName != "zz-分页测试" {
				continue
			}
			if seen[r.ID] {
				t.Fatalf("id=%d 在两页里都出现了 —— 游标和排序键对不上", r.ID)
			}
			seen[r.ID] = true
			hit++
		}
		_ = hit
		if p.NextBefore == 0 {
			break
		}
		beforeAt, beforeID = p.NextBeforeAt, p.NextBefore
	}
	if len(seen) != total {
		t.Errorf("翻完只拿到 %d 条，实际有 %d 条 —— 有记录被跳过了", len(seen), total)
	}
}
