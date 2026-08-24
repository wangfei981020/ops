package store

import (
	"testing"

	"ops-version-backend/internal/compare"
)

// 🔴 跨项目查询时，同名服务版本不一致必须标成冲突。
//
// 不标的话它们会**静默互相覆盖** —— 调用方按 service_key 建索引，
// 后读到的盖掉先读到的，拿到"某一个项目的版本"却以为是"这个平台的版本"。
// 而少了什么完全看不出来。
func TestMarkCrossProjectConflicts(t *testing.T) {
	snap := func(key, tag string) compare.Snapshot {
		return compare.Snapshot{ServiceKey: key, Tag: tag}
	}

	t.Run("同名同版本不算冲突", func(t *testing.T) {
		// 两个项目跑着同一个版本 —— 合并成一条不会撒谎
		got := markCrossProjectConflicts([]compare.Snapshot{
			snap("wallet", "v1"), snap("wallet", "v1"),
		})
		for _, s := range got {
			if s.HasConflict {
				t.Errorf("%s 版本相同不该标冲突", s.ServiceKey)
			}
		}
	})

	t.Run("同名不同版本必须标冲突", func(t *testing.T) {
		got := markCrossProjectConflicts([]compare.Snapshot{
			snap("wallet", "v1"), snap("wallet", "v2"), snap("other", "v9"),
		})
		for _, s := range got {
			if s.ServiceKey == "wallet" && !s.HasConflict {
				t.Error("wallet 在两个项目上版本不同，必须标冲突 —— 否则会被静默合并成一条")
			}
			if s.ServiceKey == "other" && s.HasConflict {
				t.Error("other 只出现一次，不该被牵连标成冲突")
			}
		}
	})

	t.Run("三个项目里有一个不同", func(t *testing.T) {
		got := markCrossProjectConflicts([]compare.Snapshot{
			snap("a", "v1"), snap("a", "v1"), snap("a", "v3"),
		})
		n := 0
		for _, s := range got {
			if s.HasConflict {
				n++
			}
		}
		// ⚠️ 三条**全部**标冲突，不能只标那个不一样的：
		//    调用方拿到的是一个 service_key 对应多条，
		//    它没法知道该信哪一条 —— 每一条都不可信。
		if n != 3 {
			t.Errorf("标了 %d 条，要 3 条（同一个服务的每一条都不可信）", n)
		}
	})

	t.Run("没有重名时原样返回", func(t *testing.T) {
		in := []compare.Snapshot{snap("a", "v1"), snap("b", "v2")}
		got := markCrossProjectConflicts(in)
		if len(got) != 2 {
			t.Fatalf("条数变了：%d", len(got))
		}
		for _, s := range got {
			if s.HasConflict {
				t.Errorf("%s 不该标冲突", s.ServiceKey)
			}
		}
	})

	t.Run("空输入不 panic", func(t *testing.T) {
		if got := markCrossProjectConflicts(nil); len(got) != 0 {
			t.Errorf("空输入应返回空，实得 %d 条", len(got))
		}
	})
}
