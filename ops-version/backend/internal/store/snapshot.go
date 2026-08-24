package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"ops-version-backend/internal/compare"
	"ops-version-backend/internal/imageref"
	"ops-version-backend/providers"

	"ops-version-backend/logx"
)

// SaveSnapshots 把一次采集的结果全量写入某个 (组织, 环境)，并算出变更历史。
//
// 🔴 **全量覆盖，不做增量。**
// 增量同步会漏掉「服务被删除了」—— 而「对方 prod 少了一个服务」正是这张表最该发现的信号之一。
// 用增量的话，那个服务会永远留在表里，看着一切正常。
//
// 🔴 **采集失败时绝不能调用本函数。**
// 失败要走 MarkSyncFailed：保留旧快照 + 把组织标成失败态。
// 如果失败时用空结果覆盖，界面会显示「该组织没有任何服务」，
// 而这跟「对方真的下线了所有服务」在表上长得一模一样 —— 看不出区别就等于没告警。
// SaveSnapshots 全量覆盖某一列（平台 × 项目 × 环境）的快照。
//
// 🔴 projectID 是**唯一键的一部分**，每一处 WHERE 都必须带上它。
//
//	漏一处的表现是跨项目串数据：读旧快照时把隔壁项目的读进来，
//	于是这个项目的服务被判成"新增"，隔壁项目的被判成"消失"并**删掉**。
//	两个项目跑同名服务时（这正是引入 project 维度的原因）必然发生。
func (s *Store) SaveSnapshots(ctx context.Context, orgID, projectID int64, env string, snaps []providers.ServiceSnapshot) error {
	now := time.Now()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1) 读旧快照，用于算 diff。只取算变更需要的列
	old := map[string]string{} // service_key → tag
	rows, err := tx.QueryContext(ctx,
		`SELECT service_key, tag FROM service_versions WHERE org_id=? AND project_id=? AND env=?`,
		orgID, projectID, env)
	if err != nil {
		return fmt.Errorf("读旧快照: %w", err)
	}
	for rows.Next() {
		var k, t string
		if err := rows.Scan(&k, &t); err != nil {
			rows.Close()
			return err
		}
		old[k] = t
	}
	rows.Close()

	// 2) 写入新快照（upsert）
	seen := map[string]bool{}
	for _, sp := range snaps {
		seen[sp.ServiceKey] = true

		wl, _ := json.Marshal(sp.Workloads)
		var conflict any
		if len(sp.Conflicts) > 0 {
			b, _ := json.Marshal(sp.Conflicts)
			conflict = string(b)
		}
		var buildNo any
		if sp.BuildNo != nil {
			buildNo = *sp.BuildNo
		}

		_, err := tx.ExecContext(ctx, `
			INSERT INTO service_versions
			  (org_id, project_id, env, service_key, image_repo, tag, running_tag, digest,
			   build_no, is_versioned, namespace, workloads, workload_cnt, conflict_detail, observed_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON DUPLICATE KEY UPDATE
			  image_repo=VALUES(image_repo), tag=VALUES(tag), running_tag=VALUES(running_tag),
			  digest=VALUES(digest), build_no=VALUES(build_no), is_versioned=VALUES(is_versioned),
			  namespace=VALUES(namespace), workloads=VALUES(workloads),
			  workload_cnt=VALUES(workload_cnt), conflict_detail=VALUES(conflict_detail),
			  observed_at=VALUES(observed_at)`,
			orgID, projectID, env, sp.ServiceKey, sp.ImageRepo, sp.Tag, sp.RunningTag, sp.Digest,
			buildNo, boolToInt(sp.IsVersioned), sp.Namespace, string(wl), len(sp.Workloads),
			conflict, now)
		if err != nil {
			return fmt.Errorf("写快照 %s: %w", sp.ServiceKey, err)
		}

		// 3) 变更历史
		oldTag, existed := old[sp.ServiceKey]
		switch {
		case !existed:
			if err := insertChange(ctx, tx, orgID, projectID, env, sp.ServiceKey, "", sp.Tag, "added", now); err != nil {
				return err
			}
		case oldTag != sp.Tag:
			// 升级还是回滚，靠构建号判；判不出就统一记 upgrade，不猜
			typ := "upgrade"
			if sp.BuildNo != nil {
				if ob := imageref.BuildNoOf(oldTag); ob != nil && *sp.BuildNo < *ob {
					typ = "rollback"
				}
			}
			if err := insertChange(ctx, tx, orgID, projectID, env, sp.ServiceKey, oldTag, sp.Tag, typ, now); err != nil {
				return err
			}
		}
	}

	// 4) 消失的服务：删快照 + 记一条 removed。
	//    这条是全量覆盖唯一能给出的信号，增量做不到。
	for key, oldTag := range old {
		if seen[key] {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM service_versions WHERE org_id=? AND project_id=? AND env=? AND service_key=?`,
			orgID, projectID, env, key); err != nil {
			return err
		}
		if err := insertChange(ctx, tx, orgID, projectID, env, key, oldTag, "", "removed", now); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE orgs SET last_sync_at=?, last_sync_status='success', last_sync_error=''
		 WHERE id=?`, now, orgID); err != nil {
		return err
	}
	return tx.Commit()
}

func insertChange(ctx context.Context, tx *sql.Tx, orgID, projectID int64, env, key, oldTag, newTag, typ string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO version_changes (org_id, project_id, env, service_key, old_tag, new_tag, change_type, changed_at)
		VALUES (?,?,?,?,?,?,?,?)`, orgID, projectID, env, key, oldTag, newTag, typ, at)
	return err
}

// MarkSyncFailed 采集失败时调这个，**不碰快照数据**。
//
// status 必须是分类过的（auth_failed / unreachable / forbidden / error），
// 不能都塞成 "error" —— 密码错、网络不通、权限不足三种的处理方式完全不同，
// 笼统一个「连接失败」等于没说，看表的人不知道该找谁。
func (s *Store) MarkSyncFailed(ctx context.Context, orgID int64, status, msg string) error {
	msg = clipText(msg, 500)
	_, err := s.db.ExecContext(ctx,
		`UPDATE orgs SET last_sync_at=?, last_sync_status=?, last_sync_error=? WHERE id=?`,
		time.Now(), status, msg, orgID)
	return err
}

// LoadSnapshots 读出对账需要的快照，key 为 compare.Column.Key()。
func (s *Store) LoadSnapshots(ctx context.Context, cols []compare.Column) (map[string][]compare.Snapshot, error) {
	out := map[string][]compare.Snapshot{}
	for _, c := range cols {
		// 🔴 采集失败的列直接跳过，不读旧数据。
		//    读了就等于拿隔夜数据冒充当前状态 —— 对账引擎会把它当成有效值参与判定。
		if !c.Healthy() {
			out[c.Key()] = nil
			continue
		}
		// 🔴 ProjectID==0 = **不限项目，查这个平台该环境下的全部**。
		//
		//    原来无条件拼 `project_id=?`，于是 ProjectID=0 时查的是
		//    `project_id=0` —— 而真实数据的 project_id 是 1、2……
		//    **一行都查不到，还不报错**。
		//
		//    实测过（2026-08-21）：
		//      list_versions(org=我方, env=UAT)              → count 0
		//      list_versions(org=我方, env=UAT, project=G32) → count 102
		//
		//    而 MCP 的 project 参数是**可选**的 —— 最自然的那种调用返回空，
		//    AI 会照着回答「这个平台没部署任何服务」。
		//    applyProjectFilter 的注释写的是「留空 = 跨项目全量」，
		//    **设计意图对，实现没跟上**。
		//
		// ⚠️ 界面不受影响：它构造 Column 时 ProjectID 是真实值。
		q := `SELECT service_key, tag, running_tag, digest, build_no, is_versioned,
			       namespace, workloads, conflict_detail, observed_at
			  FROM service_versions
			 WHERE org_id=? AND env=?`
		args := []any{c.OrgID, c.Env}
		if c.ProjectID != 0 {
			q += ` AND project_id=?`
			args = append(args, c.ProjectID)
		}
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		var list []compare.Snapshot
		for rows.Next() {
			var sp compare.Snapshot
			var buildNo sql.NullInt64
			var versioned int
			var wl, conflict sql.NullString
			if err := rows.Scan(&sp.ServiceKey, &sp.Tag, &sp.RunningTag, &sp.Digest,
				&buildNo, &versioned, &sp.Namespace, &wl, &conflict, &sp.ObservedAt); err != nil {
				rows.Close()
				return nil, err
			}
			if buildNo.Valid {
				b := int(buildNo.Int64)
				sp.BuildNo = &b
			}
			// 🔴 项目筛选放在**读出来之后**而不是拼进 SQL：
			//    通配语义必须与 providers 层完全一致（前后缀两种），
			//    翻成 SQL LIKE 会多出 `_` 这个通配符 —— 服务名里常有下划线，
			//    于是 `a_b` 这条规则会连 `axb` 一起吃掉，而没人会想到去查 SQL。
			if !c.Filter.Matches(sp.ServiceKey) {
				continue
			}
			sp.IsVersioned = versioned == 1
			sp.HasConflict = conflict.Valid && conflict.String != ""
			if wl.Valid {
				if err := json.Unmarshal([]byte(wl.String), &sp.Workloads); err != nil {
					// workloads 坏了 → 导出的明细里这个服务"没有工作负载"，
					// 而那看起来只是数据少了一点，不像是坏了
					logx.Warn("store", "workloads_broken", map[string]any{
						"service": sp.ServiceKey, "org_id": c.OrgID, "env": c.Env, "err": err.Error()})
				}
			}
			list = append(list, sp)
		}
		rows.Close()
		if c.ProjectID == 0 {
			list = markCrossProjectConflicts(list)
		}
		out[c.Key()] = list
	}
	return out, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SavePods 全量覆盖写 Pod 明细。
//
// 🔴 与 SaveSnapshots 一样是**先删后插**，不做增量合并：
// 增量会让已经被删掉的 Pod 永远留在表里，导出时显示成「还在跑」，
// 而那正是排查时最误导人的一种假象 —— 你以为有 3 个副本，实际只剩 1 个。
//
// ⚠️ 调用方必须**确认这次真的拉到了数据**再调。拿空切片调一次
// 就会把上一次的明细清空，表现为「导出里 Pod 明细突然全没了」。
func (s *Store) SavePods(ctx context.Context, orgID, projectID int64, env string, pods []providers.PodInfo) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		// ⚠️ 必须带 project_id：不带的话，采集 A 项目会把 B 项目的 Pod 明细清空 ——
		//    而 Pod 明细不影响版本比对，B 项目那边只是"明细页突然空了"，没人会立刻发现。
		`DELETE FROM service_pods WHERE org_id=? AND project_id=? AND env=?`,
		orgID, projectID, env); err != nil {
		return err
	}
	now := time.Now()
	for _, p := range pods {
		var started any
		if !p.StartedAt.IsZero() {
			started = p.StartedAt
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO service_pods (org_id, project_id, env, service_key, namespace, pod_name, container,
			  image_repo, tag, phase, ready, restarts, pod_ip, node, started_at, observed_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, projectID, env, p.ServiceKey, p.Namespace, p.PodName, p.Container,
			p.ImageRepo, p.Tag, p.Phase, boolToInt(p.Ready), p.Restarts,
			p.PodIP, p.Node, started, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListPods 读某几列的 Pod 明细，导出用。cols 为空则返回空。
func (s *Store) ListPods(ctx context.Context, orgID, projectID int64, env string) ([]providers.PodInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT service_key, namespace, pod_name, container, image_repo, tag,
		       phase, ready, restarts, pod_ip, node, started_at
		  FROM service_pods WHERE org_id=? AND project_id=? AND env=?
		 ORDER BY namespace, service_key, pod_name`, orgID, projectID, env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []providers.PodInfo{}
	for rows.Next() {
		var p providers.PodInfo
		var ready int
		var started sql.NullTime
		if err := rows.Scan(&p.ServiceKey, &p.Namespace, &p.PodName, &p.Container,
			&p.ImageRepo, &p.Tag, &p.Phase, &ready, &p.Restarts,
			&p.PodIP, &p.Node, &started); err != nil {
			return nil, err
		}
		p.Ready = ready == 1
		if started.Valid {
			p.StartedAt = started.Time
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// markCrossProjectConflicts 跨项目查询时，同名服务版本不一致要标成冲突。
//
// 🔴 不标的话它们会**静默互相覆盖**：调用方按 service_key 建索引，
// 后读到的那条盖掉先读到的，而结果看不出少了什么 ——
// 拿到的是"某一个项目的版本"，却以为是"这个平台的版本"。
//
// 标成 HasConflict 之后判定会走 CellConflict → 行结论「无法判定」，
// 也就是**明说这里判不了**，而不是给一个看着正常的错答案。
//
// ⚠️ 只在 ProjectID==0（不限项目）时做。指定了项目就不存在跨项目同名的问题。
func markCrossProjectConflicts(list []compare.Snapshot) []compare.Snapshot {
	seen := map[string]int{} // service_key → 在 list 里的下标
	dup := map[string]bool{}
	for i, sp := range list {
		j, ok := seen[sp.ServiceKey]
		if !ok {
			seen[sp.ServiceKey] = i
			continue
		}
		// 同名且版本不同 —— 两个项目跑着不同版本，合并成一条就是撒谎
		if list[j].Tag != sp.Tag {
			dup[sp.ServiceKey] = true
		}
	}
	if len(dup) == 0 {
		return list
	}
	for i := range list {
		if dup[list[i].ServiceKey] {
			list[i].HasConflict = true
		}
	}
	return list
}

// ListServiceKeys 取某平台某环境**最近一次采集到**的服务名清单。
//
// 用于「服务包含 / 排除」规则的保存前预检：拿真实服务名跑一遍规则，
// 告诉用户每条规则现在命中几个。规则是人手填的自由文本，
// 写错一个字符（`*--x` 比 `*-x` 多一个连字符）就永远不命中，
// 而保存、界面、比对全都一声不吭 —— 。
//
// ⚠️ 与 LoadSnapshots 不同，这里**不看采集是否健康**：
// 预检要的是「上次抄回来的那批名字」，哪怕那次采集后来失败了，
// 这批名字仍然是判断规则写没写对的最好依据。
func (s *Store) ListServiceKeys(ctx context.Context, orgID, projectID int64, env string) ([]string, error) {
	q := `SELECT DISTINCT service_key FROM service_versions WHERE org_id=? AND env=?`
	args := []any{orgID, env}
	// ProjectID==0 = 不限项目（与 LoadSnapshots 同一套语义，别再分叉）
	if projectID != 0 {
		q += ` AND project_id=?`
		args = append(args, projectID)
	}
	q += ` ORDER BY service_key`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
