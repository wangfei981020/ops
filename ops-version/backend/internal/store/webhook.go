package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// WebhookToken 入站 webhook 的认证令牌（目前只有 Harbor 复制事件用）。
//
// 🔴 只存哈希，明文仅在创建时返回一次 —— 与 MCP 令牌同一套做法。
// 这个端点能写入对账依据，令牌泄露 = 任何人都能伪造「已同步」，
// 而伪造出来的是一个看着完全正常的绿灯。
type WebhookToken struct {
	ID         int64
	Name       string
	Hash       []byte
	Enabled    bool
	LastUsedAt sql.NullTime
	// LastEvent 最近一次收到的事件摘要。
	//
	// 🔴 没有它的话，「Harbor 说推成功、我们说收到了、可就是没数据」
	//    这个状态在界面上完全查不下去 —— 原因只在后端日志里，
	//    而用的人多半没有日志权限。
	LastEvent string
	CreatedBy string
	CreatedAt time.Time
}

// ListWebhookTokens 取所有启用的令牌（含哈希，供比对用）。
func (s *Store) ListWebhookTokens(ctx context.Context) ([]WebhookToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, token_hash, enabled, last_used_at, COALESCE(last_event,''), created_by, created_at
		  FROM webhook_tokens WHERE deleted_at IS NULL AND enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebhookToken{}
	for rows.Next() {
		var t WebhookToken
		var en int
		if err := rows.Scan(&t.ID, &t.Name, &t.Hash, &en, &t.LastUsedAt, &t.LastEvent,
			&t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Enabled = en == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateWebhookToken 生成一个令牌，返回**明文**（只此一次）。
func (s *Store) CreateWebhookToken(ctx context.Context, name, creator string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	plain := "opsver_hook_" + hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(plain))
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO webhook_tokens (name, token_hash, created_by) VALUES (?,?,?)`,
		strings.TrimSpace(name), sum[:], creator); err != nil {
		return "", err
	}
	return plain, nil
}

// DeleteWebhookToken 吊销一个令牌。
func (s *Store) DeleteWebhookToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhook_tokens SET deleted_at = NOW(), enabled = 0 WHERE id = ? AND deleted_at IS NULL`, id)
	return err
}

// TouchWebhookToken 记一次成功调用。
//
// 🔴 有这个时间戳，才分得清「配了但从没生效」和「一直在工作」——
// 两者在界面上本来长得一模一样，而前者才是要去查的。
func (s *Store) TouchWebhookToken(ctx context.Context, id int64, summary string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhook_tokens SET last_used_at = NOW(), last_event = ? WHERE id = ?`,
		truncate(summary, 500), id)
	return err
}

// WebhookSyncTask webhook 推来的一条镜像复制结果。
type WebhookSyncTask struct {
	ServiceKey string
	Tag        string
	Status     string
	FinishedAt time.Time
	// TaskID Harbor 的 task id。只有回查 API 那条路径才有 ——
	// webhook payload 里的 artifact 不带它。失败原因要靠它去拉日志。
	//
	// scan:skip 不从库里来：sync_tasks 没有这一列，也不需要有 ——
	// 它只在"收到事件的当下回查 Harbor"那一小段时间里有用（拿失败日志），
	// 落库之后再也用不上，存了反而会让人以为可以事后拿它去查。
	TaskID int64
	// ErrMsg 失败原因（Harbor task 日志，已截断）。空 = 成功，或没去拉。
	ErrMsg string
}

// SaveWebhookSyncTasks 把 webhook 推来的复制结果写进 sync_tasks，返回落库条数。
//
// 🔴 与轮询写的是同一张表，靠 source 列区分：
//
//	poll    轮询 REST API 拿的 —— **没有版本号**（Harbor 那个接口不给）
//	webhook 事件推来的         —— 有版本号，对账归因靠的就是它
//
// 🔴 **exec_id 必须落库**（2026-09-18 修）。原来这里写死 0。
//
//	版本号只有在**事件到达的当下**拿得到 —— Harbor 的 task.resource
//	过一会儿就变 null。而卡片改成"按执行汇总"之后，发卡时才去问 Harbor 要
//	明细，那时候版本号已经没了，于是整张卡全是「未取到版本号」。
//
//	事件到达时这里本来就把版本号存下来了，只是没记是哪次 execution，
//	汇总时想取也取不出来。记上 exec_id 之后，汇总直接读本表即可（见
//	TasksOfExecution），**不用再问 Harbor**。
//
// ⚠️ 唯一键 (policy_ref, exec_id, service_key, tag) 仍然保证幂等：
//
//	同一次执行里同一个版本重复推送只留一条，Harbor 重发也不会翻倍；
//	不同执行推同一个版本会各留一条 —— 那本来就是两次不同的复制。
//
// ⚠️ 策略名对不上时返回 0 而不是报错：Harbor 那边改过策略名、
//
//	或这条策略我们还没拉取过，都会这样。调用方据此打 WARN ——
//	静默丢弃的话，表现是「webhook 配了但同步状态还是未知」，没人查得到原因。
//
// 返回的 ref 是匹配到的 sync_policies.id（0 = 没匹配上）。
//
// 🔴 ref 必须回给调用方：通知要不要发，取决于**那条规则**的通知开关，
// 而这里是整条链路上唯一一处知道是哪条规则的地方。
func (s *Store) SaveWebhookSyncTasks(ctx context.Context, policyName, destEndpoint, srcProject string,
	execID int64, list []WebhookSyncTask,
) (saved int, ref int64, matchedBy string, err error) {
	if len(list) == 0 {
		return 0, 0, "", nil
	}
	ref, matchedBy, err = s.resolvePolicy(ctx, policyName, destEndpoint, srcProject)
	if err != nil || ref == 0 {
		return 0, ref, matchedBy, err
	}
	n := 0
	for _, t := range list {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO sync_tasks (policy_ref, exec_id, service_key, tag, status, err_msg, finished_at, source)
			VALUES (?,?,?,?,?,?,?, 'webhook')
			ON DUPLICATE KEY UPDATE status=VALUES(status), err_msg=VALUES(err_msg),
			  finished_at=VALUES(finished_at), source='webhook'`,
			ref, execID, t.ServiceKey, t.Tag, t.Status, truncate(t.ErrMsg, 500),
			nullIfZero(t.FinishedAt)); err != nil {
			return n, ref, matchedBy, err
		}
		n++
	}
	return n, ref, matchedBy, nil
}

// resolvePolicy 把 webhook 事件关联回一条复制规则。
//
// 🔴 两条线索，按可靠性排：
//
//	① 规则名 —— 最准，但 Harbor 的 payload 里**没有这个字段**。
//	   我们原来取的 description 是规则的「描述」，用户不填就是空串
//	   （实测过就是空的，于是 saved 一直是 0 而 succeeded=1）。
//	② 目标 registry 地址 —— dest_resource.endpoint 对应 sync_policies.dest_registry。
//	   不唯一（多条规则可以推给同一个 Harbor），但归因真正要的只是「推给哪个平台」。
//
// ⚠️ 只认**已绑定平台**的规则：没绑平台的规则归不了因，拿它当匹配结果
//
//	等于把事件挂到一个说不出推给谁的地方，还不如明说没匹配上。
//
// ⚠️ 目标 registry 命中多条、且它们绑的**不是同一个平台**时，必须放弃。
//
//	随便挑一条的话，归因会把镜像算到另一家头上 —— 那比没有归因更糟：
//	对账表上会显示"已同步"，而实际推给的是别人。
func (s *Store) resolvePolicy(ctx context.Context, policyName, destEndpoint, srcProject string) (int64, string, error) {
	if n := strings.TrimSpace(policyName); n != "" {
		var id int64
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM sync_policies WHERE name = ? ORDER BY id DESC LIMIT 1`, n).Scan(&id)
		if err == nil {
			return id, "name", nil
		}
		if err != sql.ErrNoRows {
			return 0, "", fmt.Errorf("查复制规则 %q: %w", n, err)
		}
	}

	ep := strings.TrimRight(strings.TrimSpace(destEndpoint), "/")

	// ② 源项目 + 目标地址 —— **精确**到具体规则。
	//
	// 🔴 这一层才是真正解决问题的：多条规则指向同一个目标 Harbor 时
	//    （生产上 appA / bizB / monitoring 都推向 partner-harbor），
	//    光看目标地址必然命中多条，appA 推的镜像会被记成 bizB 推的。
	//    源项目把它们区分开。
	if sp := strings.TrimSpace(srcProject); sp != "" && ep != "" {
		var id int64
		err := s.db.QueryRowContext(ctx, `
			SELECT id FROM sync_policies
			 WHERE src_project = ? AND TRIM(TRAILING '/' FROM dest_registry) = ?
			   AND org_id IS NOT NULL
			 ORDER BY id LIMIT 1`, sp, ep).Scan(&id)
		if err == nil {
			return id, "src_project", nil
		}
		if err != sql.ErrNoRows {
			return 0, "", fmt.Errorf("按源项目查复制规则: %w", err)
		}
	}

	if ep == "" {
		return 0, "none", nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, org_id FROM sync_policies
		 WHERE TRIM(TRAILING '/' FROM dest_registry) = ? AND org_id IS NOT NULL
		 ORDER BY id`, ep)
	if err != nil {
		return 0, "", fmt.Errorf("按目标 registry 查复制规则: %w", err)
	}
	defer rows.Close()
	var firstID, firstOrg int64
	multiOrg := false
	for rows.Next() {
		var id, org int64
		if err := rows.Scan(&id, &org); err != nil {
			return 0, "", err
		}
		if firstID == 0 {
			firstID, firstOrg = id, org
			continue
		}
		if org != firstOrg {
			multiOrg = true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	switch {
	case firstID == 0:
		return 0, "none", nil
	case multiOrg:
		// 说不清推给谁 —— 宁可不归因，也不能算到别人头上
		return 0, "ambiguous", nil
	default:
		return firstID, "dest_registry", nil
	}
}

// TasksOfExecution 取这次执行已经落库的镜像明细。
//
// 🔴 **汇总卡片的版本号从这里来，不去问 Harbor。**
//
// 版本号只在复制刚结束的那一小段时间里能从 Harbor 拿到（task.resource 过后变 null）。
// 而 webhook 是**每推一个镜像来一次**，每次到达时我们都已经趁热把版本号存进来了 ——
// 等到整次执行结束要发卡时，库里本来就有全套，再去问 Harbor 只会拿到一堆空的。
//
// ⚠️ 只回有版本号的行（落库时就不收空的），所以调用方拿到的每一条都带 tag。
func (s *Store) TasksOfExecution(ctx context.Context, policyRef, execID int64) ([]WebhookSyncTask, error) {
	if policyRef <= 0 || execID <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT service_key, tag, status, COALESCE(err_msg,''), finished_at
		  FROM sync_tasks
		 WHERE policy_ref=? AND exec_id=?
		 ORDER BY id`, policyRef, execID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebhookSyncTask{}
	for rows.Next() {
		var t WebhookSyncTask
		var fin sql.NullTime
		if err := rows.Scan(&t.ServiceKey, &t.Tag, &t.Status, &t.ErrMsg, &fin); err != nil {
			return nil, err
		}
		if fin.Valid {
			t.FinishedAt = fin.Time
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
