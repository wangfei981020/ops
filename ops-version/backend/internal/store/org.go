package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

)

// Org 一个部署平台。我方也是一条普通记录，不是特例。
type Org struct {
	ID           int64
	Name         string
	ProviderType string // kite | rancher | argocd | kubeconfig | manual_import
	IsSelf       bool
	// Enabled 平台是否启用。停用的平台**不该出现在比对列里** ——
	// 它会被自动刷新逻辑一并去采，然后全部失败，把噪音放大成一屏红字。
	Enabled       bool
	HarborHost    string // 仅展示，不参与比对
	HarborProject string // 仅展示，客户可能与我方不同

	// 平台级连接信息，可被环境级覆盖，见 Conn()
	AuthType      string
	Endpoint      string
	CredentialEnc string

	// ─── 引用的数据源（多平台共用的连接信息）───
	//
	// ⚠️ 平台自己的 Endpoint 优先级**高于**数据源：迁移期两者可能都有值，
	// 那时以平台自己的为准，行为与升级前完全一致。
	DatasourceID    int64
	DatasourceName  string
	DSProviderType  string
	DSEndpoint      string
	DSAuthType      string
	DSCredentialEnc string

	LastSyncAt     sql.NullTime
	LastSyncStatus string
	LastSyncError  string

	Envs []OrgEnv
}

// OrgEnv 一个平台的一个环境。
type OrgEnv struct {
	Env string

	// 🔴 一个环境可以跨多个集群。
	//    Kite 填 cluster name（一个 Kite 接多个集群）；Rancher 填 clusterId（c-m-xxxx）
	ClusterRefs []string

	// DatasourceID 这个环境用哪个数据源。0 = 沿用下一层（见 Conn）。
	//
	// 🔴 客户的 UAT / PROD 常常是两套独立的 Rancher —— 有了这个字段，
	//    两套各建一个数据源、各环境各选各的即可，凭据只配一处。
	//    否则同一套 PROD 的账号密码有几个项目就要手填几遍，
	//    改密码时漏掉一处，表现是那一列「认证失败」，而人会去查账号本身。
	DatasourceID int64
	// DSProviderType 这个数据源是什么系统（kite / rancher / argocd）。
	//
	// 🔴 类型必须跟着环境的数据源走，不能只跟着平台走。
	//    同一个客户 UAT 是 Rancher、PROD 是 ArgoCD 是完全正常的场景 ——
	//    只让地址和凭据跟着环境、类型仍用平台的，就会拿 rancher 的协议
	//    去连 ArgoCD 的地址。实测过过：错误是
	//    `Post https://slib-argocd.../v3-public/localProviders/local?action=login: i/o timeout`
	//    ——地址是 ArgoCD 的，端点却是 Rancher 的，而配置每一项看着都对。
	DSProviderType  string
	DSEndpoint      string
	DSAuthType      string
	DSCredentialEnc string
	DSName          string

	NSInclude []string
	NSExclude []string

	// 🔴 workload 规则与 ns 规则同一层，都是「采什么回来」。
	//    ns 太粗：一个 ns 里几十个服务，而各家部署的服务集合并不相同 ——
	//    我方 UAT 有的，对方可能压根没有。留空 = 该 ns 下全部。
	//    ⚠️ 与方案里的「服务白名单」不是一回事：那个管「这次比哪些」，
	//    这个管「抄什么回来」。采集要全（数据留着以后能查），比对才收窄。
	WorkloadInclude []string
	WorkloadExclude []string

	// 0 = 只采集展示、不参与一致性判定。DEV/TEST 必须设 0：
	// 与 UAT/PROD 是两个 Harbor 两条独立流水线，构建号不同序列，横向比就是错的。
	CompareEnabled bool

	// ─── 这一列上次采集的结果 ───
	//
	// 🔴 必须是**环境级**而不是平台级。同一个平台的 UAT 采成功、PROD 采失败时，
	// 平台级字段只能记其中一个 —— 于是比对表上那两列会显示同一个状态，
	// 其中一列的"数据不可用"就此消失，被当成真实数据参与判定。
	LastCollectAt     sql.NullTime
	LastCollectStatus string
	LastCollectError  string
	// LastCollectDegraded 上次采集走了降级路径（读不到 deployments，从 Pod 反推）。
	// 🔴 降级时**副本为 0 的服务采不到**，对账时会显示成「该平台未部署此服务」——
	//    这个标记要一路传到对账表头，让人知道这一列的 missing 不是确定结论。
	LastCollectDegraded     bool
	LastCollectDegradedNote string

	// 🔴 连接覆盖：留空则继承平台级。
	//    我方 Kite 一个 endpoint 打多个集群 → 全留空。
	//    客户有两套 Rancher（UAT 一个、PROD 一个）→ 每个环境各填各的。
	Endpoint      string
	AuthType      string
	CredentialEnc string

	// 🔴 所属项目。对比表的一列 = 项目 × 环境。
	//    NULL（0）= 还没归到任何项目，比对时归入该平台的默认项目。
	//    ⚠️ SaveOrg 整组重写环境，这个值必须跟着一起写回 ——
	//    漏写的话，迁移刚回填好的归属会在下一次保存平台时被悄悄清空，
	//    表现是"某个项目的列突然空了"，而配置页上看不出任何异常。
	ProjectID int64
}

// Conn 解析出这个环境实际该用的连接信息。
//
// 覆盖是**整组生效**而不是逐字段：填了 endpoint 就必须连 auth 和凭据一起填。
// 逐字段回落会造出「A 的地址配 B 的密码」这种谁都想不到的组合，
// 排查时看配置每一项都对，就是连不上。
func (e OrgEnv) Conn(inst Org) (endpoint, authType, credEnc string) {
	// 环境引用的数据源（最高优先级）。
	// 🔴 排在手填之前：一个环境同时有数据源和手填时，人的意图必然是"用我选的那个数据源"——
	//    手填的往往是改用数据源之前留下的旧值，让旧值赢会让"我明明选了数据源"变成灵异事件。
	if strings.TrimSpace(e.DSEndpoint) != "" {
		return e.DSEndpoint, e.DSAuthType, e.DSCredentialEnc
	}
	// 环境级整组覆盖
	if strings.TrimSpace(e.Endpoint) != "" {
		return e.Endpoint, e.AuthType, e.CredentialEnc
	}
	// 平台自己填了连接信息（迁移期兼容 / 没绑数据源的平台）
	if strings.TrimSpace(inst.Endpoint) != "" {
		return inst.Endpoint, inst.AuthType, inst.CredentialEnc
	}
	// 数据源（多平台共用的那一份）
	return inst.DSEndpoint, inst.DSAuthType, inst.DSCredentialEnc
}

// ConnSource 这次用的是哪一层连接信息 —— 只用于日志和界面提示。
//
// 🔴 三层来源必须能说出用的是哪一层：
// "改了数据源的密码却没生效"这类问题，唯一的线索就是"它其实走的是平台级那份"。
// 没有这个的话，人会反复去改一个根本没被读到的字段。
func (e OrgEnv) ConnSource(inst Org) string {
	switch {
	case strings.TrimSpace(e.DSEndpoint) != "":
		return "env_datasource"
	case strings.TrimSpace(e.Endpoint) != "":
		return "env"
	case strings.TrimSpace(inst.Endpoint) != "":
		return "org"
	case strings.TrimSpace(inst.DSEndpoint) != "":
		return "datasource"
	default:
		return "none"
	}
}

// ListOrgs 读出所有启用的平台及其环境映射。
func (s *Store) ListOrgs(ctx context.Context, onlyEnabled bool) ([]Org, error) {
	// ⚠️ LEFT JOIN 而不是 JOIN：没绑数据源的平台（manual_import、
	//    或迁移期还用着自己连接信息的）也必须查得出来，否则它们会整个消失。
	q := `SELECT o.id, o.name, o.provider_type, o.auth_type, o.endpoint, o.credential_enc,
	             o.harbor_host, o.harbor_project, o.is_self, o.enabled,
	             o.last_sync_at, o.last_sync_status, o.last_sync_error,
	             COALESCE(o.datasource_id,0), COALESCE(d.name,''), COALESCE(d.provider_type,''),
	             COALESCE(d.endpoint,''), COALESCE(d.auth_type,''), d.credential_enc
	        FROM orgs o
	        LEFT JOIN datasources d ON d.id = o.datasource_id AND d.deleted_at IS NULL
	       WHERE o.deleted_at IS NULL`
	if onlyEnabled {
		q += ` AND o.enabled=1`
	}
	q += ` ORDER BY o.is_self DESC, o.id`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Org
	idx := map[int64]int{}
	for rows.Next() {
		var in Org
		var cred, dsCred sql.NullString
		var isSelf, enabled int
		if err := rows.Scan(&in.ID, &in.Name, &in.ProviderType, &in.AuthType,
			&in.Endpoint, &cred, &in.HarborHost, &in.HarborProject, &isSelf, &enabled,
			&in.LastSyncAt, &in.LastSyncStatus, &in.LastSyncError,
			&in.DatasourceID, &in.DatasourceName, &in.DSProviderType,
			&in.DSEndpoint, &in.DSAuthType, &dsCred); err != nil {
			return nil, err
		}
		in.CredentialEnc = cred.String
		in.DSCredentialEnc = dsCred.String
		in.IsSelf = isSelf == 1
		in.Enabled = enabled == 1
		idx[in.ID] = len(list)
		list = append(list, in)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return list, nil
	}

	// ⚠️ LEFT JOIN datasources：环境可以引用数据源（UAT/PROD 各一套 Rancher 时用）。
	//    没引用的环境 d.* 全为 NULL，走 COALESCE 兜成空串，行为与升级前一致。
	erows, err := s.db.QueryContext(ctx, `
		SELECT e.org_id, e.env, e.cluster_refs, e.ns_include, e.ns_exclude,
		       e.workload_include, e.workload_exclude, e.compare_enabled,
		       e.endpoint, e.auth_type, e.credential_enc, e.project_id,
		       e.last_collect_at, e.last_collect_status, e.last_collect_error,
		       e.last_collect_degraded, e.last_collect_degraded_note,
		       COALESCE(e.datasource_id,0), COALESCE(d.name,''), COALESCE(d.provider_type,''),
		       COALESCE(d.endpoint,''), COALESCE(d.auth_type,''), d.credential_enc
		  FROM org_envs e
		  LEFT JOIN datasources d ON d.id = e.datasource_id AND d.deleted_at IS NULL
		 ORDER BY e.id`)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var iid int64
		var e OrgEnv
		var refs, inc, exc, winc, wexc, cred, dsCred sql.NullString
		var pid sql.NullInt64
		var cmp int
		// TINYINT(1) 扫进 int 再转 bool —— 与 compare_enabled 同一套写法
		var degraded int
		if err := erows.Scan(&iid, &e.Env, &refs, &inc, &exc, &winc, &wexc, &cmp,
			&e.Endpoint, &e.AuthType, &cred, &pid,
			&e.LastCollectAt, &e.LastCollectStatus, &e.LastCollectError,
			&degraded, &e.LastCollectDegradedNote,
			&e.DatasourceID, &e.DSName, &e.DSProviderType,
			&e.DSEndpoint, &e.DSAuthType, &dsCred); err != nil {
			return nil, err
		}
		e.DSCredentialEnc = dsCred.String
		e.ClusterRefs = splitLines(refs.String)
		e.NSInclude = splitLines(inc.String)
		e.NSExclude = splitLines(exc.String)
		e.WorkloadInclude = splitLines(winc.String)
		e.WorkloadExclude = splitLines(wexc.String)
		e.CompareEnabled = cmp == 1
		e.LastCollectDegraded = degraded == 1
		e.CredentialEnc = cred.String
		e.ProjectID = pid.Int64
		if i, ok := idx[iid]; ok {
			list[i].Envs = append(list[i].Envs, e)
		}
	}
	return list, erows.Err()
}

// splitLines 换行分隔的配置值。空行和空白一律丢掉 ——
// 配置里多敲一个回车不该变成一条 "" 规则去匹配所有 ns。
//
// 🔴 空值返回 `[]` 而不是 nil。
//
//	这些字段都会直接进 JSON 出参，nil 序列化成 `null`，
//	而前端类型写的是 string[] —— `x.join()` / `x.length` 当场抛异常，
//	**整个弹窗白屏**，接口却是 200、后端日志里什么都没有。
//	实测栽过两处（项目的 service_include、Harbor 的 policy_filter）。
//	在这里兜一次，比在每个调用点各兜一次可靠 —— 后者总会漏。
func splitLines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// joinLines 把规则数组存成换行分隔的字符串，**顺手做一次清理**。
//
// 🔴 这是兜底，不是主要防线 —— 前端保存时已经清理过一次（normLines）。
//
//	但写入路径原来是裸 strings.Join，前端漏一次空行就直接进库，
//	而一条 "" 规则会匹配**所有** ns：采集范围悄悄扩大到整个集群，
//	表现是"莫名其妙多出一堆服务"，没人会想到是配置里多敲了个回车。
//	这种"错了不报错、只是结果变宽"的形态，必须在落库前拦住。
//
// 逗号也当分隔符：前端曾有一段时间敲不出换行（受控组件在 onChange 里
// 做规范化，把末尾换行吃掉了），用户只能用逗号，这些值已经存在了。
// ns / 服务名 / 集群名都不含逗号，认它没有歧义。
func joinLines(a []string) string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range a {
		for _, part := range strings.Split(raw, ",") {
			v := strings.TrimSpace(part)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return strings.Join(out, "\n")
}

// OrgInput 建/改组织的入参。凭据传密文，加密在 API 层做（store 不碰明文）。
type OrgInput struct {
	Name          string
	ProviderType  string
	AuthType      string
	Endpoint      string
	CredentialEnc string // 空字符串 = 不改动已有凭据
	// DatasourceID 引用哪个数据源。0 = 不引用，这个平台自己填地址和凭据。
	//
	// 🔴 这一列原来只读不写 —— 数据源建好了、平台也能读到它，
	//    唯独存的时候没写进去，于是「引用数据源」在界面上根本做不到。
	DatasourceID  int64
	HarborHost    string
	HarborProject string
	IsSelf        bool
	// Enabled 启用状态。🔴 **nil = 不改动**（新建时默认启用）。
	//
	// ⚠️ 不能用 bool：那样任何一次不带该字段的保存都会把平台停用 ——
	// 零值即停用是灾难。而"改了别的字段顺手把平台停了"不会报错，
	// 只是那个平台悄悄从比对列里消失（与环境凭据被清空是同一形态，见 SaveOrg）。
	Enabled *bool
	Envs    []OrgEnv
}

func (s *Store) GetOrg(ctx context.Context, id int64) (Org, error) {
	list, err := s.ListOrgs(ctx, false)
	if err != nil {
		return Org{}, err
	}
	for _, in := range list {
		if in.ID == id {
			return in, nil
		}
	}
	return Org{}, ErrNotFound
}

// SaveOrg 建或改。id=0 为新建。
//
// 🔴 CredentialEnc 为空表示「不改动已有凭据」，而不是「清空凭据」。
//
//	因为读接口从不回显凭据，前端提交时那个字段本来就是空的 ——
//	若把空当成清空，用户改个备注就会把密码抹掉，然后采集开始报认证失败，
//	而界面上看不出任何异常。
//
// SaveOrg 保存平台（含它的环境映射）。
//
// ⚠️ 出错时统一过 friendlyDBErr：唯一键冲突的原文
// （Duplicate entry '3-UAT' for key 'org_envs.uk_org_env'）对使用者毫无意义。
func (s *Store) SaveOrg(ctx context.Context, id int64, in OrgInput) (int64, error) {
	newID, err := s.saveOrg(ctx, id, in)
	return newID, friendlyDBErr(err)
}

func (s *Store) saveOrg(ctx context.Context, id int64, in OrgInput) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if id == 0 {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO orgs (name, provider_type, auth_type, endpoint,
			  credential_enc, harbor_host, harbor_project, is_self, enabled, datasource_id)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			in.Name, in.ProviderType, in.AuthType, in.Endpoint,
			nullIfEmpty(in.CredentialEnc), in.HarborHost, in.HarborProject, boolToInt(in.IsSelf),
			// 新建时 nil = 启用：没人会建一个"生下来就停用"的平台
			boolToInt(in.Enabled == nil || *in.Enabled),
			// 🔴 这一列原来只读不写 —— 数据源建好了、平台也能读到它，
			//    唯独存的时候没写进去，于是「引用数据源」这件事在界面上做不到。
			//    「字段有、没接线」的第四次。
			nullIfZeroID(in.DatasourceID))
		if err != nil {
			return 0, err
		}
		id, _ = res.LastInsertId()
	} else {
		q := `UPDATE orgs SET name=?, provider_type=?, auth_type=?, endpoint=?,
		       harbor_host=?, harbor_project=?, is_self=?, datasource_id=?`
		args := []any{in.Name, in.ProviderType, in.AuthType, in.Endpoint,
			in.HarborHost, in.HarborProject, boolToInt(in.IsSelf),
			nullIfZeroID(in.DatasourceID)}
		if in.CredentialEnc != "" {
			q += `, credential_enc=?`
			args = append(args, in.CredentialEnc)
		}
		// 🔴 只有**显式传了**才改启用状态 —— 与凭据同一条规矩：
		//    不传表示"这次没动这个字段"，不能当成"设为 false"。
		if in.Enabled != nil {
			q += `, enabled=?`
			args = append(args, boolToInt(*in.Enabled))
		}
		q += ` WHERE id=?`
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return 0, err
		}
	}

	// 🔴 整组重写前，先把**已有的环境级凭据**留一份。
	//
	// 凭据永不回显（读接口只给 has_credential），所以前端提交上来的
	// api_key/password 多数时候是空的 —— 那表示"没改"，不是"清空"。
	// 平台级已经这么处理了（上面 `if in.CredentialEnc != ""` 才更新），
	// 而环境级是 DELETE + INSERT，空值直接写成 NULL ⇒ **旧凭据被抹掉**。
	//
	// 实测过（2026-08-20）：第一次填 API Key 生效了（Rancher 认出账号 u-xxxxx，
	// 返回的是 403 权限不足而不是 401），随后改别的字段再保存一次，
	// 就变成"认证失败: 未配置 API Key" —— 用户以为 key 没生效，
	// 其实是被自己后来的保存清掉的。
	//
	// ⚠️ 两处对同一件事用两种语义，是这类"改了 A 结果 B 没了"的常见来源。
	prev := map[string]string{}
	prows, err := tx.QueryContext(ctx,
		`SELECT env, credential_enc FROM org_envs WHERE org_id=?`, id)
	if err != nil {
		return 0, err
	}
	for prows.Next() {
		var env string
		var enc sql.NullString
		if err := prows.Scan(&env, &enc); err != nil {
			prows.Close()
			return 0, err
		}
		prev[env] = enc.String
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return 0, err
	}

	// 🔴 平台必须至少有一个项目 —— 017 之后 org_envs.project_id 是 NOT NULL，
	//    而新建平台时项目还不存在（项目要挂在平台下，得先有平台）。
	//    不在这里兜的话，带环境新建平台会直接 500
	//    「Column 'project_id' cannot be null」—— 而那条报错完全看不出
	//    真正缺的是「这个平台还没有项目」。
	//
	// ⚠️ 用 INSERT IGNORE + 回查：并发两次保存同一个平台时，
	//    两边都会走到这里，靠唯一索引挡住第二次。
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO projects (org_id, name, sort_order, enabled)
		VALUES (?, '默认', 0, 1)`, id); err != nil {
		return 0, err
	}
	var defProj int64
	if err := tx.QueryRowContext(ctx,
		`SELECT MIN(id) FROM projects WHERE org_id=? AND deleted_at IS NULL`, id).
		Scan(&defProj); err != nil {
		return 0, fmt.Errorf("找不到这个平台的默认项目: %w", err)
	}

	// 环境映射整组重写：环境是一张小表，增量维护的复杂度换不来什么好处
	if _, err := tx.ExecContext(ctx, `DELETE FROM org_envs WHERE org_id=?`, id); err != nil {
		return 0, err
	}
	for _, e := range in.Envs {
		// 没指定项目 = 归到默认项目。
		// ⚠️ 不能留 0：project_id 是 NOT NULL，写 0 会指向一个不存在的项目，
		//    表现是这一列的数据谁也查不到，而配置页上看着正常。
		if e.ProjectID == 0 {
			e.ProjectID = defProj
		}
		// 没提交新凭据 ⇒ 沿用这个环境原来的那份
		if strings.TrimSpace(e.CredentialEnc) == "" {
			e.CredentialEnc = prev[e.Env]
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO org_envs (org_id, env, cluster_refs, ns_include, ns_exclude,
			  workload_include, workload_exclude,
			  compare_enabled, endpoint, auth_type, credential_enc, project_id, datasource_id)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, e.Env, joinLines(e.ClusterRefs),
			joinLines(e.NSInclude), joinLines(e.NSExclude),
			joinLines(e.WorkloadInclude), joinLines(e.WorkloadExclude),
			boolToInt(e.CompareEnabled), e.Endpoint, e.AuthType,
			nullIfEmpty(e.CredentialEnc), e.ProjectID,
			// 🔴 必须跟着一起写回。SaveOrg 是**整组重写**环境行 ——
			//    漏写这个字段，人选好的数据源会在下一次保存平台时被悄悄清空，
			//    表现是"某一列突然连不上了"，而配置页上看不出任何异常
			//    （project_id 就踩过这个坑，见它上面的注释）。
			nullIfZero64(e.DatasourceID)); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// DeleteOrg 软删一个平台，并把它的环境映射一起删掉。
//
// 🔴 环境映射**硬删**，不是保留。
//
//	原来的结论是「orgs 是软删意味着可恢复，恢复时环境配置还在才有意义」——
//	但查下来**恢复这条路根本不存在**：没有任何代码把 orgs.deleted_at 置回 NULL，
//	界面上也没有入口。那些 org_envs 留着永远不会被用到，
//	只会让每次数据模型变更的迁移都留下一批「挂不上任何活跃平台」的行
//	（017 迁移就被这个绊了一下）。
//
// ⚠️ 版本快照（service_versions / service_pods / version_changes）**不删**：
//
//	那是历史事实，不是配置。留着还能查「这个平台下线前跑的什么版本」，
//	而且它们都带 org_id，不会混进任何活跃平台的查询里。
func (s *Store) DeleteOrg(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE orgs SET deleted_at=NOW(), enabled=0 WHERE id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM org_envs WHERE org_id=?`, id); err != nil {
		return err
	}
	// 项目跟着平台走 —— 平台没了，它下面的项目也没有意义了。
	// 用软删：projects 的唯一键是 (org_id, name) 的生成列，硬删和软删对它一样，
	// 但软删留下痕迹，将来查「这个平台当时有哪些项目」还查得到。
	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET deleted_at=NOW() WHERE org_id=? AND deleted_at IS NULL`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Change 一条版本变更记录。
type Change struct {
	OrgID      int64     `json:"org_id"`
	OrgName    string    `json:"org_name"`
	Env        string    `json:"env"`
	ServiceKey string    `json:"service_key"`
	OldTag     string    `json:"old_tag"`
	NewTag     string    `json:"new_tag"`
	ChangeType string    `json:"change_type"`
	ChangedAt  time.Time `json:"changed_at"`

	// SuspectRuleChange 这条「下线」很可能是**改采集规则**造成的，不是服务真的下线。
	//
	// 🔴 判据：这条记录说某服务消失了，而该服务此刻**仍被采集规则排除着** ——
	//    说明它当时是"我们不再采它"，不是"它没了"。
	//
	//    实测过（2026-08-24）：172 条 removed 里有 62 条属于这种情况，
	//    分布在 8/20 与 8/23 的五个时刻上。这类假下线会让
	//    「这个服务最近改过什么」这一能力不可信，通知渠道一旦配上还会直接发出
	//    「服务下线」告警。
	//
	// ⚠️ 刻意**不改动历史数据**：查询时标注而不是删记录 ——
	//    ① 删了就无法回答"当初为什么记了这一条"；
	//    ② 判据依赖"当前规则"，而规则会变；标注是算出来的，规则一改标记自动跟着变，
	//       删除则是一次性的、判错了也回不来。
	SuspectRuleChange bool `json:"suspect_rule_change"`
}

func (s *Store) ListChanges(ctx context.Context, orgID int64, serviceKey string, limit int) ([]Change, error) {
	// 统一口径见 paging.go：超上限**钳制**到上限，不掉回默认值
	limit = ClampLimit(limit)
	// LEFT JOIN excluded_services：标出那些「服务其实还被规则排着」的假下线。
	// ⚠️ 用 EXISTS 而不是 JOIN 出多行 —— 一个服务可能有多条排除记录
	//    （多个 workload 指向同一个镜像名），JOIN 会让变更记录重复。
	q := `SELECT c.org_id, i.name, c.env, c.service_key, c.old_tag, c.new_tag,
	             c.change_type, c.changed_at,
	             EXISTS(SELECT 1 FROM excluded_services e
	                     WHERE e.org_id=c.org_id AND e.env=c.env
	                       AND e.service_key=c.service_key) AS suspect
	        FROM version_changes c JOIN orgs i ON i.id=c.org_id WHERE 1=1`
	var args []any
	if orgID > 0 {
		q += ` AND c.org_id=?`
		args = append(args, orgID)
	}
	if serviceKey != "" {
		q += ` AND c.service_key=?`
		args = append(args, serviceKey)
	}
	q += ` ORDER BY c.changed_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var c Change
		var suspect int
		if err := rows.Scan(&c.OrgID, &c.OrgName, &c.Env, &c.ServiceKey,
			&c.OldTag, &c.NewTag, &c.ChangeType, &c.ChangedAt, &suspect); err != nil {
			return nil, err
		}
		// 只有「下线」才谈得上"是不是假的"；升级/新增不适用
		c.SuspectRuleChange = suspect == 1 && c.ChangeType == "removed"
		out = append(out, c)
	}
	return out, rows.Err()
}

// AuditRow 审计记录。
type AuditRow struct {
	// ID 游标分页要用它。
	//
	// 🔴 分页必须用**游标**（id < 上一页最小 id），不能用 OFFSET：
	//    审计表一直在插入，翻到第 2 页时前面又多了几条 ——
	//    OFFSET 会让同一条记录在两页里都出现、或者被整个跳过，
	//    而"哪条被漏了"从界面上完全看不出来。审计恰恰是最不该漏记录的地方。
	ID        int64     `json:"id"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
	Result    string    `json:"result"`
	ErrorMsg  string    `json:"error_msg"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditPage 一页审计记录。
type AuditPage struct {
	Rows []AuditRow `json:"rows"`
	// Total 全表总条数。
	//
	// 🔴 必须给：不给的话，人翻到最后一页也不知道自己看到的是不是全部 ——
	//    而"我是不是漏看了什么"正是查审计时唯一在意的事。
	Total int `json:"total"`
	// NextBefore 下一页的游标（这一页最小的 id）。0 = 没有下一页。
	NextBefore int64 `json:"next_before"`
}

// ListAudit 读一页审计。
//
// before>0 时只返回 id 小于它的记录（游标分页）。
//
// ⚠️ limit 超上限**钳制到上限**，不是掉回默认值。
//
//	原来写的是 `if limit <= 0 || limit > 500 { limit = 100 }` ——
//	于是传 501 比传 500 少拿一半，而且没有任何提示。
//	把「没指定」和「要多了」当成同一种情况处理，是这条问题里最反直觉的一点。
func (s *Store) ListAudit(ctx context.Context, limit int, before int64) (AuditPage, error) {
	limit = ClampLimit(limit) // 统一口径见 paging.go

	var page AuditPage
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&page.Total); err != nil {
		return page, err
	}

	q := `SELECT id, actor, action, target, COALESCE(detail,''), result, error_msg, ip, created_at
	        FROM audit_logs`
	args := []any{}
	if before > 0 {
		q += ` WHERE id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	page.Rows = []AuditRow{}
	for rows.Next() {
		var a AuditRow
		if err := rows.Scan(&a.ID, &a.Actor, &a.Action, &a.Target, &a.Detail,
			&a.Result, &a.ErrorMsg, &a.IP, &a.CreatedAt); err != nil {
			return page, err
		}
		page.Rows = append(page.Rows, a)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	// ⚠️ 只有**取满一页**才认为还有下一页。
	//    不判这个的话，最后一页也会给出游标，界面上「下一页」永远点得动，
	//    点进去是空的 —— 人会以为数据丢了。
	if len(page.Rows) == limit {
		page.NextBefore = page.Rows[len(page.Rows)-1].ID
	}
	return page, nil
}

// nullIfZeroID 外键列写 0 会撞外键/让 JOIN 匹配到不存在的行，一律写 NULL。
func nullIfZeroID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// MarkEnvCollect 记一次采集尝试的结果（成功也要记）。
//
// 🔴 失败也必须落库。只记成功的话，"上次采集时刻"会永远停在最后一次成功上，
// 于是"半小时前试过但失败了"和"半小时没跑过"看起来一模一样 ——
// 而这两者要做的事完全不同（前者去查权限/网络，后者去查定时任务）。
func (s *Store) MarkEnvCollect(ctx context.Context, orgID, projectID int64, env string, r EnvCollectResult) error {
	errMsg := clipText(r.ErrMsg, 500)
	// ⚠️ 必须带 project_id：一个平台的同一环境现在可能有多行（每个项目一行）。
	//    不带的话，A 项目采失败会把 B 项目那行也标成失败 ——
	//    B 的比对列于是显示"数据不可用"，而它其实采得好好的。
	_, err := s.db.ExecContext(ctx, `
		UPDATE org_envs SET last_collect_at = NOW(), last_collect_status = ?, last_collect_error = ?,
		       last_collect_degraded = ?, last_collect_degraded_note = ?
		 WHERE org_id = ? AND project_id = ? AND env = ?`,
		r.Status, errMsg, boolToInt(r.Degraded), clipText(r.DegradedNote, 255),
		orgID, projectID, env)
	return err
}

// EnvCollectResult 一次采集尝试的结果。
//
// ⚠️ 用结构体而不是继续加参数：这个函数已经有 5 个参数，
// 再加两个位置参数，调用点就成了一串没有名字的字面量 ——
// 传错顺序编译器不会报错（都是 string/bool），而症状是状态被写反。
type EnvCollectResult struct {
	Status string
	ErrMsg string
	// Degraded 这一轮走了降级路径（读不到 deployments，从 Pod 反推）。
	// 🔴 必须落库才能传到对账表头 —— 只写日志的话，看表的人永远看不到，
	//    而降级会让副本为 0 的服务显示成「该平台未部署此服务」。
	Degraded     bool
	DegradedNote string
}

// ColumnFreshness 一列（平台×环境）的数据新鲜度。
type ColumnFreshness struct {
	OrgID   int64  `json:"org_id"`
	OrgName string `json:"org_name"`
	// 🔴 新鲜度是**按列**的，而一列 = 平台 × 项目 × 环境。
	//    少了 project_id 的话，同平台同环境的两个项目会共用一条新鲜度记录 ——
	//    A 项目刚采过、B 项目三天没采，界面上两列都显示"刚刚更新"，
	//    而"比对前自动刷新过期列"会因此跳过真正过期的那一列。
	ProjectID int64  `json:"project_id"`
	Env       string `json:"env"`
	IsSelf    bool   `json:"is_self"`
	// ObservedAt 数据本身的时刻（快照里最新那条）。
	// nil = 从来没采到过任何数据
	ObservedAt *time.Time `json:"observed_at"`
	// LastCollectAt 上次**尝试**采集的时刻
	LastCollectAt *time.Time `json:"last_collect_at"`
	Status        string     `json:"last_collect_status"`
	Error         string     `json:"last_collect_error"`
}

// ListColumnFreshness 列出所有参与比对的列及其新鲜度。
//
// ⚠️ observed_at 取 MAX 而不是任意一条：一次采集里各服务的 observed_at 相同，
// 但历史上可能有已经消失的服务留着旧记录，取 MAX 才是"这一列最后一次有数据"。
func (s *Store) ListColumnFreshness(ctx context.Context) ([]ColumnFreshness, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.id, o.name, e.project_id, e.env, o.is_self,
		       (SELECT MAX(sv.observed_at) FROM service_versions sv
		         WHERE sv.org_id = o.id AND sv.project_id = e.project_id AND sv.env = e.env)
		         AS observed_at,
		       e.last_collect_at, e.last_collect_status, e.last_collect_error
		  FROM orgs o JOIN org_envs e ON e.org_id = o.id
		 WHERE o.deleted_at IS NULL AND o.enabled = 1 AND e.compare_enabled = 1
		 ORDER BY o.is_self DESC, o.name, e.env`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ColumnFreshness{}
	for rows.Next() {
		var c ColumnFreshness
		var obs, last sql.NullTime
		if err := rows.Scan(&c.OrgID, &c.OrgName, &c.ProjectID, &c.Env, &c.IsSelf,
			&obs, &last, &c.Status, &c.Error); err != nil {
			return nil, err
		}
		if obs.Valid {
			t := obs.Time
			c.ObservedAt = &t
		}
		if last.Valid {
			t := last.Time
			c.LastCollectAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}


// nullIfZero64 0 存成 NULL。
//
// ⚠️ 外键列存 0 会撞上"没有 id=0 的数据源"，而 NULL 才是"没引用"的正确表达。
func nullIfZero64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
