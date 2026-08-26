package api

import (
	"encoding/json"
	"strings"
	"testing"

	"ops-version-backend/internal/store"
)

// 改 workload_exclude 必须在审计里看得出「改前 → 改后」。
// 生产上正是这次改动引发了 042/043 两个 P0，而审计只记了 {id, endpoint}。
func TestRuleDiffCapturesExcludeChange(t *testing.T) {
	before := store.Org{Envs: []store.OrgEnv{{
		Env: "UAT", ProjectID: 1,
		WorkloadExclude: []string{"*--game-server-backend"}, // 那条笔误规则
	}}}
	req := orgReq{Envs: []envReq{{
		Env: "UAT", ProjectID: 1,
		WorkloadExclude: []string{"*-game-server-backend"}, // 改对之后
	}}}

	d := ruleDiff(before, req)
	if len(d) != 1 {
		t.Fatalf("应当记录 1 处变更，实际 %d", len(d))
	}
	b, _ := json.Marshal(d[0])
	s := string(b)
	if !strings.Contains(s, "*--game-server-backend") || !strings.Contains(s, "*-game-server-backend") {
		t.Fatalf("审计详情里必须同时有改前和改后的值，实际: %s", s)
	}
	t.Logf("✅ %s", s)
}

// 没动规则就不该记 —— 否则每次保存都刷一条噪音，真正的变更会被淹掉
func TestRuleDiffQuietWhenNothingChanged(t *testing.T) {
	envs := []store.OrgEnv{{Env: "UAT", ProjectID: 1, WorkloadExclude: []string{"*-x"}}}
	before := store.Org{Envs: envs}
	req := orgReq{Envs: []envReq{{Env: "UAT", ProjectID: 1, WorkloadExclude: []string{"*-x"}}}}
	if d := ruleDiff(before, req); len(d) != 0 {
		t.Fatalf("规则没变时不该记录，实际 %+v", d)
	}
}

// 🔴 凭据绝不能进审计
func TestEnvSnapshotHasNoCredential(t *testing.T) {
	in := store.Org{Envs: []store.OrgEnv{{
		Env: "UAT", CredentialEnc: "SUPER-SECRET-BLOB", WorkloadExclude: []string{"*-x"},
	}}}
	b, _ := json.Marshal(envSnapshot(in))
	if strings.Contains(string(b), "SUPER-SECRET") {
		t.Fatalf("凭据泄漏进了审计详情: %s", b)
	}
	if !strings.Contains(string(b), "*-x") {
		t.Fatal("配置本身应当被记下来")
	}
}
