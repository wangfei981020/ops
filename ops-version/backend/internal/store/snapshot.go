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
	"strings"
)

// SaveSnapshots 把一次采集的结果全量写入某个 (平台, 环境)，并算出变更历史。
//
// 🔴 **全量覆盖，不做增量。**
// 增量同步会漏掉「服务被删除了」—— 而「对方 prod 少了一个服务」正是这张表最该发现的信号之一。
// 用增量的话，那个服务会永远留在表里，看着一切正常。
//
// 🔴 **采集失败时绝不能调用本函数。**
// 失败要走 MarkSyncFailed：保留旧快照 + 把组织标成失败态。
// 如果失败时用空结果覆盖，界面会显示「该平台没有任何服务」，
// 而这跟「对方真的下线了所有服务」在表上长得一模一样 —— 看不出区别就等于没告警。
// SaveSnapshots 全量覆盖某一列（平台 × 项目 × 环境）的快照。
//
// 🔴 projectID 是**唯一键的一部分**，每一处 WHERE 都必须带上它。
//
//	漏一处的表现是跨项目串数据：读旧快照时把隔壁项目的读进来，
//	于是这个项目的服务被判成"新增"，隔壁项目的被判成"消失"并**删掉**。
//	两个项目跑同名服务时（这正是引入 project 维度的原因）必然发生。
//
// excludeRules 是本轮采集**生效的服务排除规则**（org_envs.workload_exclude）。
//
// 🔴 没有它就分不清「服务真的下线了」和「我们改了规则不再采它」——
//
//	两者在快照 diff 上完全一样（这一轮没看到），但含义相反。
//	实测过：把一条笔误规则改对之后，紧随其后的采集往变更历史灌了
//	18 条假的「服务下线」，而那些服务此刻仍在对方集群上跑着。
//	通知渠道一旦配上，这类变更会直接发出「服务下线」告警。
func (s *Store) SaveSnapshots(
	ctx context.Context, orgID, projectID int64, env string,
	snaps []providers.ServiceSnapshot,
	prevExcluded, currExcluded map[string]string,
) error {
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
			// 🔴 上一轮它是被规则排掉的（所以不在快照里），这一轮又出现 ——
			//    那是**规则放开了**，不是服务新上线。记 added 会造出一条
			//    没有对应 removed 的孤立记录，让「这个服务的历史」读起来像
			//    "它上线过两次"。
			//
			// ⚠️ 这是对称性要求：既然「规则排掉」不记 removed（见下面第 4 步），
			//    「规则放开」就同样不能记 added。少任何一半都会让 added/removed
			//    配不上对 —— 生产历史里已经有同一个 tag 被记 3 次 added
			//    而中间没有任何 removed 的记录。
			if _, wasExcluded := prevExcluded[sp.ServiceKey]; wasExcluded {
				logx.Info("snapshot", "skip_added_rule_relaxed", map[string]any{
					"org": orgID, "project": projectID, "env": env, "service": sp.ServiceKey,
					"note": "上一轮被采集规则排除，本轮规则放开，不记为服务新增",
				})
				break
			}
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
		// 🔴 命中排除规则 = 我们**主动不采**它了，不是它下线了。
		//    快照要删（它确实不该再参与对账），但**绝不能记 removed** ——
		//    那是一条与事实相反的历史，且永久留在变更记录里：
		//    服务回来时（规则改回去/换一列没配规则）也不会产生对应的 added 来抵消它。
		// 用**本轮采集记下的事实**判断，而不是拿规则反推 ——
		// 规则比的是 workload 名、这里的 key 是 ServiceKey，helm 下两者对不上
		if _, nowExcluded := currExcluded[key]; nowExcluded {
			logx.Info("snapshot", "skip_removed_by_rule", map[string]any{
				"org": orgID, "project": projectID, "env": env, "service": key,
				"note": "按采集规则排除，不记为服务下线",
			})
			continue
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
		//      list_versions(org=我方, env=UAT, project=项目A) → count 102
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
		} else {
			// 不限项目 = 所有项目的并集。
			// ⚠️ `project_id > 0` 而不是不加条件：历史上有过一份 project_id=0 的
			//    「平台级全量快照」，那个功能已经拆掉，但老库里可能还留着行。
			//    保留这个条件，免得它们混进并集、让同一个服务出现两次。
			q += ` AND project_id > 0`
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
	} else {
		// 🔴 "不限项目" = 所有**项目**，不含平台级全量快照（project_id=0）。
		//    不写这个条件的话，平台级那份会跟项目级的混在一起返回 ——
		//    同一个服务出现两次，而调用方（MCP、预检取样）都当它是一行。
		q += ` AND project_id > 0`
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

// SaveExcluded 全量覆盖某一列「被采集规则排掉的服务」清单。
//
// 🔴 与 SaveSnapshots 一样是**先删后插**：这份清单描述的是"本轮规则排掉了谁"，
// 规则改小之后旧记录必须消失，否则预检和对账会一直以为某个服务还被排着，
// 而它其实早就回到快照里了 —— 那种错静默且会自我延续。
func (s *Store) SaveExcluded(ctx context.Context, orgID, projectID int64, env string, list []providers.ExcludedService) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM excluded_services WHERE org_id=? AND project_id=? AND env=?`,
		orgID, projectID, env); err != nil {
		return err
	}
	now := time.Now()
	for _, e := range list {
		if strings.TrimSpace(e.ServiceKey) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO excluded_services (org_id, project_id, env, service_key, workload, namespace, observed_at)
			VALUES (?,?,?,?,?,?,?)
			ON DUPLICATE KEY UPDATE namespace=VALUES(namespace), observed_at=VALUES(observed_at)`,
			orgID, projectID, env, e.ServiceKey, e.Workload, e.Namespace, now); err != nil {
			return fmt.Errorf("写被排除服务 %s: %w", e.ServiceKey, err)
		}
	}
	return tx.Commit()
}

// ListExcludedKeys 取某一列被规则排掉的 ServiceKey 集合。
//
// 用于两处，且**必须是同一份数据**：
//   - 对账：判 missing 之前先看它是不是"我们主动没采"
//   - 预检：把它并进样本，否则已生效的规则永远显示"命中 0"
func (s *Store) ListExcludedKeys(ctx context.Context, orgID, projectID int64, env string) (map[string]string, error) {
	q := `SELECT service_key, workload FROM excluded_services WHERE org_id=? AND env=?`
	args := []any{orgID, env}
	// ProjectID==0 = 不限项目（与 LoadSnapshots / ListServiceKeys 同一套语义）
	if projectID != 0 {
		q += ` AND project_id=?`
		args = append(args, projectID)
	} else {
		q += ` AND project_id > 0` // 同上：不含平台级全量快照
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, w string
		if err := rows.Scan(&k, &w); err != nil {
			return nil, err
		}
		out[k] = w
	}
	return out, rows.Err()
}

// ListServiceWorkloads 取某一列上「服务名 → 它的 workload 名列表」。
//
// 🔴 规则预检必须按 **workload 名** 匹配，因为采集器就是按它过滤的。
// 拿 ServiceKey 去比规则在 helm 环境下会算错：helm 把 release 名拼进 workload 名
// （`opsalert-另一个产品-backend`），而镜像名是 `另一个产品-backend` ——
// 规则 `另一个产品-*` 匹配后者却匹配不上前者，于是预检报的命中与实际排除的
// **没有交集**。
//
// ⚠️ 同时并入 excluded_services：被排掉的服务已经不在快照里，
// 不并进来的话，已生效的规则会显示"命中 0"。
func (s *Store) ListServiceWorkloads(ctx context.Context, orgID, projectID int64, env string) (map[string][]string, error) {
	out := map[string][]string{}

	add := func(key, wl string) {
		key, wl = strings.TrimSpace(key), strings.TrimSpace(wl)
		if key == "" {
			return
		}
		if wl == "" {
			// workload 名缺失（老数据）时退回服务名 —— 至少还能按老语义匹配上，
			// 比整条不参与匹配好：后者会让规则显示"命中 0"
			wl = key
		}
		for _, x := range out[key] {
			if x == wl {
				return
			}
		}
		out[key] = append(out[key], wl)
	}

	q := `SELECT service_key, COALESCE(workloads,'') FROM service_versions WHERE org_id=? AND env=?`
	args := []any{orgID, env}
	if projectID != 0 {
		q += ` AND project_id=?`
		args = append(args, projectID)
	} else {
		q += ` AND project_id > 0` // 同上：不含平台级全量快照
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key, wlJSON string
		if err := rows.Scan(&key, &wlJSON); err != nil {
			rows.Close()
			return nil, err
		}
		var wls []string
		if wlJSON != "" {
			// 🔴 解析失败不能静默吞掉：wls 为空会让这个服务退回"按服务名匹配"，
			//    而那正是 那个错位的来源 —— 规则会命中一批
			//    实际不受影响的服务，且没有任何迹象说明判据已经降级了。
			if err := json.Unmarshal([]byte(wlJSON), &wls); err != nil {
				logx.Warn("store", "workloads_parse_failed", map[string]any{
					"org": orgID, "env": env, "service": key, "raw": clipText(wlJSON, 120),
					"err":  err.Error(),
					"note": "该服务的规则匹配将退回按服务名进行，helm 环境下可能不准",
				})
			}
		}
		if len(wls) == 0 {
			add(key, "")
			continue
		}
		for _, w := range wls {
			add(key, w)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 并入已被排掉的（它们不在 service_versions 里）
	ex, err := s.ListExcludedKeys(ctx, orgID, projectID, env)
	if err != nil {
		return nil, err
	}
	for k, w := range ex {
		add(k, w)
	}
	return out, nil
}
