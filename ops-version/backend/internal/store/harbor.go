package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"ops-version-backend/logx"
	"ops-version-backend/providers"
)

// ─────────────── Harbor 连接配置 ───────────────

type Harbor struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Username string `json:"username"`
	// 🔴 只说有没有，不说是什么。凭据永不回显，任何角色任何接口都一样
	HasCredential bool `json:"has_credential"`
	InsecureTLS   bool `json:"insecure_tls"`
	// PolicyFilter 只拉这几条复制规则（按规则名，支持 * 通配）。留空 = 全部。
	// 🔴 与「绑组织」不是一回事：那个说这条规则推给谁，这个说我们只关心哪几条。
	PolicyFilter   []string   `json:"policy_filter"`
	Enabled        bool       `json:"enabled"`
	LastSyncAt     *time.Time `json:"last_sync_at"`
	LastSyncStatus string     `json:"last_sync_status"`
	LastSyncError  string     `json:"last_sync_error"`
	CredentialEnc  string     `json:"-"`
}

func (s *Store) ListHarbors(ctx context.Context) ([]Harbor, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, endpoint, username, COALESCE(credential_enc,''), insecure_tls, enabled,
		       COALESCE(policy_filter,''), last_sync_at, last_sync_status, last_sync_error
		  FROM harbors WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Harbor{}
	for rows.Next() {
		var h Harbor
		var enc string
		var ins, en int
		var last sql.NullTime
		var pf string
		if err := rows.Scan(&h.ID, &h.Name, &h.Endpoint, &h.Username, &enc, &ins, &en,
			&pf, &last, &h.LastSyncStatus, &h.LastSyncError); err != nil {
			return nil, err
		}
		// ⚠️ 必须兜成 []：nil 序列化成 JSON null，前端 `policy_filter.join()` 当场炸，
		//    表现是「没配过过滤规则的 Harbor 一点编辑就白屏」——接口 200，日志干净。
		h.PolicyFilter = orEmpty(splitLines(pf))
		h.CredentialEnc = enc
		h.HasCredential = enc != ""
		h.InsecureTLS, h.Enabled = ins == 1, en == 1
		if last.Valid {
			v := last.Time
			h.LastSyncAt = &v
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

type HarborInput struct {
	Name          string
	Endpoint      string
	Username      string
	CredentialEnc string // 空 = 不改
	InsecureTLS   bool
	Enabled       bool
	PolicyFilter  []string
}

func (s *Store) SaveHarbor(ctx context.Context, id int64, in HarborInput) (int64, error) {
	if id == 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO harbors (name, endpoint, username, credential_enc, insecure_tls, enabled, policy_filter)
			VALUES (?,?,?,?,?,?,?)`,
			in.Name, in.Endpoint, in.Username, nullIfEmpty(in.CredentialEnc),
			boolToInt(in.InsecureTLS), boolToInt(in.Enabled), strings.Join(in.PolicyFilter, "\n"))
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	q := `UPDATE harbors SET name=?, endpoint=?, username=?, insecure_tls=?, enabled=?, policy_filter=?`
	args := []any{in.Name, in.Endpoint, in.Username, boolToInt(in.InsecureTLS),
		boolToInt(in.Enabled), strings.Join(in.PolicyFilter, "\n")}
	// 🔴 空凭据 = 不改，不是清空。表单不回显密码，提交时那一栏本来就是空的 ——
	//    当成"清空"的话，改个名字就把凭据抹掉了，下次采集报认证失败
	if in.CredentialEnc != "" {
		q += `, credential_enc=?`
		args = append(args, in.CredentialEnc)
	}
	q += ` WHERE id=?`
	args = append(args, id)
	_, err := s.db.ExecContext(ctx, q, args...)
	return id, err
}

func (s *Store) DeleteHarbor(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE harbors SET deleted_at=NOW() WHERE id=?`, id)
	return err
}

func (s *Store) MarkHarborSync(ctx context.Context, id int64, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE harbors SET last_sync_at=NOW(), last_sync_status=?, last_sync_error=? WHERE id=?`,
		status, truncate(errMsg, 500), id)
	return err
}

// ─────────────── 复制规则 / 执行 / 任务 ───────────────

// SavePolicies 落库复制规则。
//
// ⚠️ 用 upsert 而不是先删后插：policy 上绑着 org_id（人工配的「这条规则推给哪个组织」），
// 删了重插会把这个绑定丢掉，表现为「配好的归因过一会儿自己没了」。
func (s *Store) SavePolicies(ctx context.Context, harborID int64, ps []providers.SyncPolicy) error {
	for _, p := range ps {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO sync_policies (harbor_id, policy_id, name, dest_registry, src_project, trigger_type, enabled)
			VALUES (?,?,?,?,?,?,?)
			ON DUPLICATE KEY UPDATE name=VALUES(name), dest_registry=VALUES(dest_registry),
			  src_project=VALUES(src_project),
			  trigger_type=VALUES(trigger_type), enabled=VALUES(enabled)`,
			harborID, p.PolicyID, p.Name, p.DestRegistry, p.SrcProject,
			p.TriggerType, boolToInt(p.Enabled)); err != nil {
			return err
		}
	}
	return nil
}

type PolicyRow struct {
	Ref          int64  `json:"id"`
	HarborID     int64  `json:"harbor_id"`
	HarborName   string `json:"harbor_name"`
	PolicyID     int64  `json:"policy_id"`
	Name         string `json:"name"`
	DestRegistry string `json:"dest_registry"`
	OrgID        *int64 `json:"org_id"`
	OrgName      string `json:"org_name"`
	Trigger      string `json:"trigger_type"`
	Enabled      bool   `json:"enabled"`
}

func (s *Store) ListPolicies(ctx context.Context) ([]PolicyRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.harbor_id, h.name, p.policy_id, p.name, p.dest_registry,
		       p.org_id, COALESCE(o.name,''), p.trigger_type, p.enabled
		  FROM sync_policies p
		  JOIN harbors h ON h.id = p.harbor_id
		  LEFT JOIN orgs o ON o.id = p.org_id
		 WHERE h.deleted_at IS NULL
		 ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PolicyRow{}
	for rows.Next() {
		var p PolicyRow
		var org sql.NullInt64
		var en int
		if err := rows.Scan(&p.Ref, &p.HarborID, &p.HarborName, &p.PolicyID, &p.Name,
			&p.DestRegistry, &org, &p.OrgName, &p.Trigger, &en); err != nil {
			return nil, err
		}
		if org.Valid {
			v := org.Int64
			p.OrgID = &v
		}
		p.Enabled = en == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// BindPolicyOrg 把复制规则绑到组织。绑了才能把同步状态并进那一列的对账结果。
// orgID 传 0 表示解绑。
func (s *Store) BindPolicyOrg(ctx context.Context, ref int64, orgID int64) error {
	var v any
	if orgID > 0 {
		v = orgID
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sync_policies SET org_id=? WHERE id=?`, v, ref)
	return err
}

func (s *Store) PolicyRefOf(ctx context.Context, harborID, policyID int64) (int64, error) {
	var ref int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM sync_policies WHERE harbor_id=? AND policy_id=?`, harborID, policyID).Scan(&ref)
	return ref, err
}

// SaveExecutions upsert 执行记录。
//
// ⚠️ 必须 upsert：一条 InProgress 的执行下次拉到时已经变成 Succeeded，
// 只插不更的话表里会永远停在「进行中」，而告警和界面都看这个字段。
// SaveExecutions 落库并返回**状态发生变化**的那些执行。
//
// 🔴 只有变化的才该触发通知。每轮拉取都会看到同样的历史执行，
// 无条件通知的话，一条三天前的失败会每 30 分钟被重发一次 ——
// 群里刷屏，然后所有人把群设成免打扰，真正的新失败也没人看见。
//
// 判据是 (exec_id, status) 与库里已有的不同：
//
//	库里没有        → 新执行，通知
//	状态变了        → 比如 InProgress → Failed，通知
//	状态没变        → 静默
func (s *Store) SaveExecutions(ctx context.Context, policyRef int64, es []providers.SyncExecution) ([]providers.SyncExecution, error) {
	prev := map[int64]string{}
	rows, err := s.db.QueryContext(ctx,
		`SELECT exec_id, status FROM sync_executions WHERE policy_ref=?`, policyRef)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var st string
		if err := rows.Scan(&id, &st); err != nil {
			rows.Close()
			return nil, err
		}
		prev[id] = st
	}
	rows.Close()

	var changed []providers.SyncExecution
	for _, e := range es {
		if old, ok := prev[e.ExecID]; !ok || old != e.Status {
			changed = append(changed, e)
		}
	}

	for _, e := range es {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO sync_executions (policy_ref, exec_id, trigger_type, status,
			  total, succeeded, failed, started_at, ended_at, synced_at)
			VALUES (?,?,?,?,?,?,?,?,?,NOW())
			ON DUPLICATE KEY UPDATE status=VALUES(status), total=VALUES(total),
			  succeeded=VALUES(succeeded), failed=VALUES(failed),
			  ended_at=VALUES(ended_at), synced_at=NOW()`,
			policyRef, e.ExecID, e.TriggerType, e.Status, e.Total, e.Succeeded, e.Failed,
			nullIfZero(e.StartedAt), nullIfZero(e.EndedAt)); err != nil {
			return nil, err
		}
	}
	return changed, nil
}

// SaveTasks 写入任务明细。
//
// 🔴 **没有版本号的一律不落库。**
//
//	这张表回答的问题只有一个：「同步过去的是哪个版本」。
//	tag 为空的记录回答不了它，却会造成两个实打实的后果：
//
//	① 唯一键是 (policy_ref, exec_id, service_key, tag) —— tag 也在里面。
//	   于是空 tag 那条和 webhook 写入的带版本号那条**互不冲突、并排存着**，
//	   界面上同一个服务、同一个时刻出现两行，一行有版本号一行写着
//	   「Harbor 未记录版本」，看的人无从判断哪个是真的。
//
//	② 采集器每 30 分钟对**所有历史 execution** 全量拉一遍，
//	   而 Harbor 的 resource 字段只在复制刚结束时有值 ——
//	   实测这条路径拿回的 task 中位数是 258 天前的，无一例外没有版本号。
//	   不拦的话，这些废记录会随每轮采集不断累积。
//
//	版本号的可靠来源只有 webhook（趁复制刚结束回查 API）。
//	采集器拿不到版本号时，「执行记录」表里仍然有这次复制的完整记录，
//	用户不会因此以为"没同步过" —— 少的只是一条答不上话的明细。
//
// ⚠️ 与 api/harborhook.go 的 collect() 是**同一个标准**。
//
//	原来只有 webhook 那条路径做了这个过滤，采集器这条全盘接收，
//	同一张表两个入口两套标准 —— 迟早从宽的那条漏进来，而且确实漏了。
func (s *Store) SaveTasks(ctx context.Context, policyRef, execID int64, ts []providers.SyncTask) error {
	skipped := 0
	for _, t := range ts {
		if strings.TrimSpace(t.Tag) == "" {
			skipped++
			continue
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO sync_tasks (policy_ref, exec_id, service_key, tag, status, err_msg, finished_at)
			VALUES (?,?,?,?,?,?,?)
			ON DUPLICATE KEY UPDATE status=VALUES(status), err_msg=VALUES(err_msg),
			  finished_at=VALUES(finished_at)`,
			policyRef, execID, t.ServiceKey, t.Tag, t.Status,
			truncate(t.ErrMsg, 500), nullIfZero(t.FinishedAt)); err != nil {
			return err
		}
	}
	// 全部被丢弃时要说出来：那意味着这次采集对「按服务查同步」毫无贡献，
	// 而界面上只会表现为"这条规则没有明细"，不指向任何一层。
	if skipped > 0 {
		logx.Info("harborsync", "tasks_without_tag_skipped", map[string]any{
			"policy_ref": policyRef, "exec_id": execID,
			"skipped": skipped, "saved": len(ts) - skipped,
			"note": "Harbor 没给版本号（resource 已过期变 null），不落库。" +
				"版本号的可靠来源是 webhook，执行记录不受影响"})
	}
	return nil
}

type ExecutionRow struct {
	ID         int64      `json:"id"`
	PolicyName string     `json:"policy_name"`
	HarborName string     `json:"harbor_name"`
	OrgName    string     `json:"org_name"`
	ExecID     int64      `json:"exec_id"`
	Trigger    string     `json:"trigger_type"`
	Status     string     `json:"status"`
	Total      int        `json:"total"`
	Succeeded  int        `json:"succeeded"`
	Failed     int        `json:"failed"`
	StartedAt  *time.Time `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at"`
}

// ListExecutions 读一页执行记录。
//
// 🔴 必须分页：这张表是**流水**，每次复制都插一条。
//
//	用户那边一天几十条，翻不到几天前的记录，而「上次成功推的是什么时候」
//	恰恰是查同步问题时最先要看的。
//
// ⚠️ 与审计同一套：游标（before = 上一页最小 id），不是 offset ——
//
//	流水一直在插入，offset 会让同一条在两页里都出现或被整个跳过。
//
// 🔴 游标必须和**排序键**一致。
//
//	这里按 (started_at DESC, id DESC) 排，游标就得是这两个值的组合。
//	只用 id 做游标的话：Harbor 拉回来的执行记录**插入顺序与执行时间无关**
//	（补拉历史时尤其明显），于是 `id < 上一页最小 id` 切出来的集合
//	和「按时间排在后面」根本不是同一批 —— 实测 25 条翻到第三页只剩 1 条，
//	而且和第一页有重叠。审计表按 id 排所以没这问题，照搬到这里就错了。
//
// MySQL 的行比较 `(a, b) < (?, ?)` 正好表达「排在这一行之后」。
func (s *Store) ListExecutions(ctx context.Context, limit int, beforeAt *time.Time, beforeID int64) (Page[ExecutionRow], error) {
	limit = ClampLimit(limit)
	var page Page[ExecutionRow]

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sync_executions e
		  JOIN sync_policies p ON p.id = e.policy_ref
		  JOIN harbors h ON h.id = p.harbor_id
		 WHERE h.deleted_at IS NULL`).Scan(&page.Total); err != nil {
		return page, err
	}

	q := `SELECT e.id, p.name, h.name, COALESCE(o.name,''), e.exec_id, e.trigger_type, e.status,
	             e.total, e.succeeded, e.failed, e.started_at, e.ended_at
	        FROM sync_executions e
	        JOIN sync_policies p ON p.id = e.policy_ref
	        JOIN harbors h ON h.id = p.harbor_id
	        LEFT JOIN orgs o ON o.id = p.org_id
	       WHERE h.deleted_at IS NULL`
	args := []any{}
	if beforeAt != nil {
		// ⚠️ started_at 可能是 NULL（拉到一半的执行）。COALESCE 成一个
		//    极早的时间，让它们始终排在最后，且不会因为 NULL 比较而整批消失。
		q += ` AND (COALESCE(e.started_at,'1970-01-01'), e.id) < (?, ?)`
		args = append(args, *beforeAt, beforeID)
	}
	q += ` ORDER BY COALESCE(e.started_at,'1970-01-01') DESC, e.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	out := []ExecutionRow{}
	for rows.Next() {
		var e ExecutionRow
		var st, en sql.NullTime
		if err := rows.Scan(&e.ID, &e.PolicyName, &e.HarborName, &e.OrgName, &e.ExecID,
			&e.Trigger, &e.Status, &e.Total, &e.Succeeded, &e.Failed, &st, &en); err != nil {
			return page, err
		}
		if st.Valid {
			v := st.Time
			e.StartedAt = &v
		}
		if en.Valid {
			v := en.Time
			e.EndedAt = &v
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	page.Rows = out
	// ⚠️ 只有取满一页才认为还有下一页 —— 最后一页也给游标的话，
	//    界面上「下一页」永远点得动，点进去是空的
	if len(out) == limit {
		last := out[len(out)-1]
		page.NextBefore = last.ID
		if last.StartedAt != nil {
			page.NextBeforeAt = last.StartedAt
		}
	}
	return page, nil
}

// SyncFact 某个 (平台, 服务, tag) 的同步事实。对账归因用。
type SyncFact struct {
	Status     string     `json:"status"`
	FinishedAt *time.Time `json:"finished_at"`
	ErrMsg     string     `json:"err_msg"`
}

// SyncFactsOf 取某个平台所有服务最近一次的同步结果。
//
// 🔴 这是「归因」的数据来源：同样是「对方版本落后」，
// 这里能告诉你镜像到底推过去了没有 —— 推过去了是对方没发版（对方的节奏），
// 没推过去是我们的锅。没有这层数据，两者在对账表上长得一模一样。
func (s *Store) SyncFactsOf(ctx context.Context, orgID int64) (map[string]SyncFact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.service_key, t.tag, t.status, t.err_msg, t.finished_at
		  FROM sync_tasks t
		  JOIN sync_policies p ON p.id = t.policy_ref
		 WHERE p.org_id = ?
		 ORDER BY t.finished_at ASC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// key = service_key + "\x00" + tag。同一个 tag 可能同步过多次，
	// 后面的覆盖前面的 —— ORDER BY 升序保证留下的是最近一次
	out := map[string]SyncFact{}
	for rows.Next() {
		var svc, tag, status, msg string
		var fin sql.NullTime
		if err := rows.Scan(&svc, &tag, &status, &msg, &fin); err != nil {
			return nil, err
		}
		f := SyncFact{Status: status, ErrMsg: msg}
		if fin.Valid {
			v := fin.Time
			f.FinishedAt = &v
		}
		out[svc+"\x00"+tag] = f
	}
	return out, rows.Err()
}

func nullIfZero(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// AllSyncFacts 取所有复制规则的同步结果，不分组织。
//
// 用于「不限组织」的镜像检查：只要**任一**规则把这个 tag 推成功过就算已同步。
// ⚠️ 与 SyncFactsOf 的区别要说清：那个回答「推给某个平台了吗」，
// 这个回答「推出去过吗」—— 后者更宽松，用在还没绑定组织的场景。
func (s *Store) AllSyncFacts(ctx context.Context) (map[string]SyncFact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT service_key, tag, status, err_msg, finished_at
		  FROM sync_tasks ORDER BY finished_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SyncFact{}
	for rows.Next() {
		var svc, tag, status, msg string
		var fin sql.NullTime
		if err := rows.Scan(&svc, &tag, &status, &msg, &fin); err != nil {
			return nil, err
		}
		f := SyncFact{Status: status, ErrMsg: msg}
		if fin.Valid {
			v := fin.Time
			f.FinishedAt = &v
		}
		// 升序遍历，后面的覆盖前面的 → 留下最近一次
		out[svc+"\x00"+tag] = f
	}
	return out, rows.Err()
}
