// Package collector 把 provider 拉到的数据落进库。
//
// 它只做编排：解析连接 → 建 provider → 拉数据 → 落库 / 标失败。
// 判定逻辑在 internal/compare，取数逻辑在 providers，这里都不掺和。
package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ops-version-backend/internal/metrics"
	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
	"ops-version-backend/providers"
)

// Decryptor 解密平台凭据。抽成接口是为了让采集器能被测试
// （测试里塞个明文实现即可，不必搬一套 AES 进来）。
type Decryptor interface {
	Decrypt(enc string) (string, error)
}

// Credential 凭据的明文形态，加密存进 credential_enc。
type Credential struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	// InsecureTLS 对方 Rancher 用自签证书时才开。
	// 默认关：默默跳过证书校验会让中间人攻击无声无息，必须是显式选择。
	InsecureTLS bool `json:"insecure_tls,omitempty"`
}

type Collector struct {
	st  *store.Store
	dec Decryptor
}

func New(st *store.Store, dec Decryptor) *Collector { return &Collector{st: st, dec: dec} }

// CollectAll 采集所有启用的平台。
//
// 一个平台失败不影响其他平台 —— 这是刻意的：
// N 个客户里有一个 token 过期，不该让整次采集什么都没更新。
func (c *Collector) CollectAll(ctx context.Context) {
	insts, err := c.st.ListOrgs(ctx, true)
	if err != nil {
		logx.J("collect", "list_orgs_fail", map[string]any{"err": err.Error()})
		return
	}
	for _, in := range insts {
		// 🔴 先预置指标再采集：*Vec 没被用过时不输出样本，
		//    「从未成功采集过」的组织会因为指标缺席而让陈旧告警永远不触发。
		//    manual_import 也要预置 —— 它同样会过期，只是靠人来更新
		for _, e := range in.Envs {
			metrics.Ensure(in.Name, e.Env)
		}
		if in.ProviderType == "manual_import" {
			continue // 手工导入的数据由上传接口写入，不参与定时采集
		}
		if err := c.CollectOrg(ctx, in); err != nil {
			logx.Error("collect", "org_fail", map[string]any{"org": in.Name, "err": err.Error()})
		}
	}
}

// CollectOrg 采集一个平台的所有环境。
func (c *Collector) CollectOrg(ctx context.Context, in store.Org) error {
	return c.CollectOrgEnv(ctx, in, "")
}

// CollectOrgEnv 采集一个平台。onlyEnv 非空时只采那一个环境。
//
// 存在按环境采的理由：界面上「刷新数据」是**逐列**做的 ——
// 一列一个请求，进度才看得见（"正在刷新 3/6 列"）。
// 整平台一把梭的话，用户只能盯着一个转圈，不知道卡在哪一列、还要多久。
func (c *Collector) CollectOrgEnv(ctx context.Context, in store.Org, onlyEnv string) error {
	return c.CollectOrgEnvProject(ctx, in, onlyEnv, 0)
}

// CollectOrgEnvProject 采集一个平台。
//
// onlyEnv 非空 = 只采那个环境；onlyProject 非零 = 只采那个项目。
//
// 🔴 必须能按项目收窄：一个平台的同一环境现在可能有多行（每个项目一行），
// 界面上「刷新这一列」刷的是**一个项目的一个环境**，而不是该环境的全部项目。
// 不收窄的话，刷新一列会连带去打对方系统 N 次，而进度条只显示一列。
func (c *Collector) CollectOrgEnvProject(ctx context.Context, in store.Org, onlyEnv string, onlyProject int64) error {
	if len(in.Envs) == 0 {
		return c.st.MarkSyncFailed(ctx, in.ID, "error", "未配置任何环境映射")
	}

	var firstErr error
	okCount, failCount := 0, 0

	// 🔴 同一轮内复用「集群 + 完整规则」都相同的拉取结果。
	//
	//    环境行的粒度是「项目 × 环境」，而拉数据的粒度是「集群 × 规则」——
	//    生产上 A公司 三个项目的 ns_include 全是 app-uat，于是对方平台的 Rancher
	//    每轮被打三遍，其中两遍结果**一模一样**（pods=106 services=93），
	//    连 403 → fallback_pods_only 的完整流程都各走一次。
	//
	// ⚠️ key 里必须含**完整规则**（ns + workload），不能只含 ns：
	//    规则不同则结果不同，复用会把 A 项目的服务集合写进 B 项目的快照。
	//    只有规则逐字相同才复用 —— 那时结果必然相同，语义上绝对安全。
	//    （A公司 的 项目B 与 项目C 规则完全一致 → 复用；项目A 有排除规则 → 各拉各的。）
	shared := map[string]*providers.ListResult{}

	for _, env := range in.Envs {
		if onlyEnv != "" && env.Env != onlyEnv {
			continue
		}
		if onlyProject != 0 && env.ProjectID != onlyProject {
			continue
		}
		if err := c.collectEnv(ctx, in, env, shared); err != nil {
			failCount++
			if firstErr == nil {
				firstErr = err
			}
			logx.Error("collect", "env_fail", map[string]any{
				"org": in.Name, "env": env.Env, "err": err.Error(),
				"kind": classify(err)})
			continue
		}
		okCount++
	}

	// 🔴 指定了环境却一个都没匹配上 —— 必须报错。
	//    不拦的话会落进下面 failCount==0 那一支，**什么都没采却返回成功**，
	//    界面上显示"刷新完成"而数据一点没动。
	//    这类"零结果当成功"是本项目反复撞到的形态。
	if onlyEnv != "" && okCount == 0 && failCount == 0 {
		return fmt.Errorf("平台 %s 没有名为 %q 的环境", in.Name, onlyEnv)
	}

	// 🔴 记状态与返回结果是**两件事**，绝不能混。
	//
	//    原来这里写的是 `return c.st.MarkSyncFailed(...)` —— 而 MarkSyncFailed
	//    返回的是**写库**的错误，写成功就是 nil。于是：
	//      采集全挂 → 状态记成 failed → 写库成功 → 返回 nil → 接口 200「成功」
	//    界面上点「采集」显示成功，实际一条数据都没采到，平台状态却是红的。
	//
	//    实测（2026-08-25）：环境指到一个连不通的地址，日志里明明是
	//    `env_fail: 网络不可达 ... no such host`，接口照样 200。
	//
	// ⚠️ 上面十几行就写着「什么都没采却返回成功」是必须防的形态 ——
	//    那里防住了「环境没匹配上」，却在紧挨着的这两支上犯了同一个错。
	//    记状态失败只该记日志：它盖不住、也不该盖住真正的采集错误。
	markFailed := func(status, msg string) {
		if err := c.st.MarkSyncFailed(ctx, in.ID, status, msg); err != nil {
			logx.Warn("collect", "mark_sync_failed", map[string]any{
				"org": in.Name, "err": err.Error(),
				"note": "状态没记上，但采集错误照常往上抛"})
		}
	}
	switch {
	case failCount == 0:
		return nil // collectEnv 内部已把状态置为 success
	case okCount == 0:
		// 全挂：按第一个错误分类。分类是为了让人一眼知道该找谁 ——
		// 密码错找对方管理员、网络不通找网络、403 找对方要权限，处理路径完全不同
		markFailed(classify(firstErr), firstErr.Error())
		return firstErr
	default:
		// 部分成功：单独一态。
		// 🔴 不能算成 success —— 那会让「PROD 拉到了、UAT 没拉到」看起来一切正常，
		//    而 UAT 那半边的对账结论其实是基于旧数据的。
		msg := fmt.Sprintf("%d/%d 个环境采集失败，首个错误：%v", failCount, okCount+failCount, firstErr)
		markFailed("partial", msg)
		// %w 保留错误链：handler 靠 errors.Is 分类成 auth_failed / unreachable / forbidden，
		// 包成纯文本的话分类全退化成 unknown，界面上就说不出"该找谁"了
		return fmt.Errorf("%s: %w", msg, firstErr)
	}
}

func (c *Collector) collectEnv(
	ctx context.Context, in store.Org, env store.OrgEnv,
	shared map[string]*providers.ListResult,
) (retErr error) {
	started := time.Now()
	// 本轮有没有走降级路径（读不到 deployments，从 Pod 反推）。
	// 🔴 在 defer 之前声明，让状态落库时能带上它 —— 降级的后果是
	//    副本为 0 的服务采不到，对账时会显示成「该平台未部署此服务」。
	// ⚠️ 一个环境可能跨多个集群：**任一集群降级，整列就该标降级**，
	//    因为对账表上这一列是合并后的结果，看表的人分不出是哪个集群。
	var degraded bool
	var degradedNote string
	defer func() {
		metrics.CollectDuration.WithLabelValues(in.Name, env.Env).Observe(time.Since(started).Seconds())
		st := "success"
		errMsg := ""
		if retErr != nil {
			st = classify(retErr)
			errMsg = retErr.Error()
		}
		metrics.CollectTotal.WithLabelValues(in.Name, env.Env, st).Inc()

		// 🔴 成功和失败都要落库。只记成功的话，"上次采集时刻"永远停在最后一次成功上，
		//    于是"刚才试过但失败了"和"根本没跑过"看起来一模一样。
		// ⚠️ 用 context.WithoutCancel：调用方的 ctx 可能已经因为超时被取消，
		//    而"这次采集失败了"这个事实恰恰是超时时最需要记下来的。
		if err := c.st.MarkEnvCollect(context.WithoutCancel(ctx), in.ID, env.ProjectID, env.Env,
			store.EnvCollectResult{
				Status: st, ErrMsg: errMsg,
				Degraded: degraded, DegradedNote: degradedNote,
			}); err != nil {
			logx.Warn("collect", "mark_failed", map[string]any{
				"org": in.Name, "env": env.Env, "err": err.Error()})
		}
	}()

	p, err := c.BuildProvider(in, env)
	if err != nil {
		return err
	}
	clusters := env.ClusterRefs
	if len(clusters) == 0 {
		// 🔴 ArgoCD 允许不指定集群：它的"集群"是 destination，
		//    而"一个 ArgoCD 管一个集群"是最常见的形态 ——
		//    强制填，填错的表现是**零结果**，跟"对方没有服务"长得一样。
		//    留空时用一个空 ref 跑一轮，ArgoCD 侧会匹配所有 destination。
		//
		// ⚠️ 其余 provider 不能这么放：Kite/Rancher 的集群是**必填参数**，
		//    空集群会打出一个语义不明的请求，而不是"查全部"。
		// 🔴 判据必须是**实际用的**类型，不是平台上写的那个。
		//    环境选了 ArgoCD 数据源、而平台类型是 rancher 时，
		//    用平台类型判断会要求填集群 —— 而 ArgoCD 本来就不需要，
		//    表现是「A公司/PROD 未配置集群」，人去填集群反而更错。
		//    实测撞到过过（2026-08-25）：这是同一个 bug 的第二处，
		//    上一次只改了建 provider 那处，这处漏了 —— 所以现在两处都调
		//    providerTypeOf，判据只留一份。
		if providerTypeOf(in, env) != "argocd" {
			return fmt.Errorf("%s/%s 未配置集群", in.Name, env.Env)
		}
		clusters = []string{""}
	}

	rules := providers.Rules{
		NS:       providers.NSRules{Include: env.NSInclude, Exclude: env.NSExclude},
		Workload: providers.WorkloadRules{Include: env.WorkloadInclude, Exclude: env.WorkloadExclude},
	}
	logx.Debug("collect", "env_start", map[string]any{
		"org": in.Name, "env": env.Env, "provider": providerTypeOf(in, env),
		"clusters": clusters, "ns_include": env.NSInclude, "ns_exclude": env.NSExclude})

	// 一个环境可能跨多个集群，合并结果。
	// 合并时若同一个 service_key 在两个集群上版本不同 → 同名冲突，
	// 跟 ns 误抓是同一类问题，同样拒绝判定。
	merged := map[string]providers.ServiceSnapshot{}
	var order []string
	var allPods []providers.PodInfo
	var allExcluded []providers.ExcludedService
	for _, cluster := range clusters {
		// 规则逐字相同 → 结果必然相同 → 同一轮内只拉一次
		ck := cluster + "\x00" + rulesKey(rules)
		res, hit := shared[ck]
		if hit {
			logx.Debug("collect", "reuse_pull", map[string]any{
				"org": in.Name, "env": env.Env, "project": env.ProjectID, "cluster": cluster,
				"note": "本轮已用相同规则拉过这个集群，复用结果，不再打对方接口"})
		} else {
			cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			r, err := p.ListServices(cctx, cluster, rules, true)
			cancel()
			if err != nil {
				return fmt.Errorf("集群 %s: %w", cluster, err)
			}
			res = r
			shared[ck] = r
		}
		snaps := res.Services
		allPods = append(allPods, res.Pods...)
		if res.Degraded {
			degraded = true
			degradedNote = res.DegradedReason
		}
		// 被规则排掉的服务：记成事实存下来，供预检与对账使用
		allExcluded = append(allExcluded, res.Excluded...)
		logx.Debug("collect", "cluster_done", map[string]any{
			"org": in.Name, "env": env.Env, "cluster": cluster,
			"services": len(snaps), "pods": len(res.Pods)})
		for _, s := range snaps {
			prev, exists := merged[s.ServiceKey]
			if !exists {
				merged[s.ServiceKey] = s
				order = append(order, s.ServiceKey)
				continue
			}
			prev.Workloads = append(prev.Workloads, s.Workloads...)
			if prev.Tag != s.Tag {
				if len(prev.Conflicts) == 0 {
					prev.Conflicts = append(prev.Conflicts, providers.ConflictItem{
						Namespace: prev.Namespace, Workload: strings.Join(prev.Workloads, ","), Tag: prev.Tag,
					})
				}
				prev.Conflicts = append(prev.Conflicts, providers.ConflictItem{
					Namespace: s.Namespace, Workload: strings.Join(s.Workloads, ","), Tag: s.Tag,
				})
			}
			merged[s.ServiceKey] = prev
		}
	}

	out := make([]providers.ServiceSnapshot, 0, len(order))
	for _, k := range order {
		out = append(out, merged[k])
	}

	// 🔴 只有真正拉到数据才走 SaveSnapshots（它会全量覆盖）。
	//    上面任何一步出错都已 return，不会用空结果把旧快照冲掉。
	// 🔴 顺序很讲究：**先读上一轮的排除清单**，再存本轮的，最后存快照。
	//
	//    变更检测要同时知道两件事：
	//      · 上一轮谁被排除了 → 它这次出现是"规则放开"，不是服务新增
	//      · 本轮谁被排除了   → 它这次消失是"我们不再采它"，不是服务下线
	//    两者缺一，added/removed 就配不上对。
	//
	// ⚠️ 必须在 SaveExcluded **之前**读 —— 存完就变成本轮的了，上一轮的信息没了。
	prevExcluded, err := c.st.ListExcludedKeys(ctx, in.ID, env.ProjectID, env.Env)
	if err != nil {
		// 读不到就当上一轮什么都没排除：最坏情况是多记一条 added，
		// 而让整轮采集失败会连版本数据都丢掉，代价大得多
		logx.Warn("collect", "load_prev_excluded_failed", map[string]any{
			"org": in.Name, "env": env.Env, "err": err.Error()})
		prevExcluded = map[string]string{}
	}

	currExcluded := make(map[string]string, len(allExcluded))
	for _, e := range allExcluded {
		currExcluded[e.ServiceKey] = e.Workload
	}
	// 规则集合变了就说明这一轮是「规则变更后的第一次采集」——
	// 说出来，否则事后看变更历史会以为那天服务真的动了
	if len(prevExcluded) != len(currExcluded) {
		logx.Info("collect", "exclusion_set_changed", map[string]any{
			"org": in.Name, "env": env.Env, "project": env.ProjectID,
			"before": len(prevExcluded), "after": len(currExcluded),
			"note": "采集规则的作用范围变了，本轮的快照差异不代表对方部署有变动",
		})
	}

	// 存失败只记 WARN 不中断 —— 缺了它只是让预检/对账退回"拿规则反推"的老行为
	if err := c.st.SaveExcluded(ctx, in.ID, env.ProjectID, env.Env, allExcluded); err != nil {
		logx.Warn("collect", "save_excluded_failed", map[string]any{
			"org": in.Name, "env": env.Env, "err": err.Error(),
			"note": "预检与对账会退回按规则反推，helm 环境下可能不准"})
	}
	if err := c.st.SaveSnapshots(ctx, in.ID, env.ProjectID, env.Env, out, prevExcluded, currExcluded); err != nil {
		return err
	}
	// Pod 明细失败不影响这次采集的结论 —— 版本对账靠的是 service_versions。
	// 但要留一条 WARN，否则「导出里 Pod 明细一直是空的」会查不到原因。
	if err := c.st.SavePods(ctx, in.ID, env.ProjectID, env.Env, allPods); err != nil {
		logx.Warn("collect", "save_pods_failed", map[string]any{
			"org": in.Name, "env": env.Env, "err": err.Error()})
	}
	// 🔴 只在真正成功后才推进这个时间戳 —— 它是「数据陈旧」告警的唯一依据
	metrics.LastSuccessTimestamp.WithLabelValues(in.Name, env.Env).Set(float64(time.Now().Unix()))
	metrics.ServicesTotal.WithLabelValues(in.Name, env.Env).Set(float64(len(out)))

	// 🔴 版本是从哪拿的，必须进日志。
	//
	//    客户普遍只给 Pod 的读权限，读不到 deployments 时我们从 Pod 的 imageID 反推 ——
	//    两条路拿到的东西不完全等价（Pod 反推看不到副本为 0 的服务）。
	//    界面上**不显示**这个区别（那是内部术语，看的人只会以为我们出了故障），
	//    所以它必须在日志里说清楚，否则事后没有任何地方查得到
	//    「这一列当时到底是怎么采的」。
	versionFrom := "deployment"
	if degraded {
		versionFrom = "pod"
	}
	logx.Info("collect", "env_done", map[string]any{
		"org": in.Name, "env": env.Env,
		"services": len(out), "pods": len(allPods), "clusters": len(clusters),
		"version_from": versionFrom, "note": degradedNote})
	return nil
}

// BuildProvider 导出给 API 层用（测试连通、拉集群列表都需要现建一个 provider）。
func (c *Collector) BuildProvider(in store.Org, env store.OrgEnv) (providers.Provider, error) {
	endpoint, authType, credEnc := env.Conn(in)
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("%s/%s 未配置连接地址（环境级、平台级、数据源三处都为空）", in.Name, env.Env)
	}
	// 🔴 记下**用的是哪一层**。三层连接信息叠加时，
	//    "改了数据源却没生效"唯一的线索就是这条（它其实走的是平台级那份）。
	logx.Debug("collect", "conn_source", map[string]any{
		"org": in.Name, "env": env.Env,
		"source": env.ConnSource(in), "endpoint": endpoint,
		// 🔴 数据源名和**实际用的系统类型**都要记：
		//    "拿 rancher 协议连了 argocd 地址"这类错，只看地址是看不出来的
		"datasource": envOr(env.DSName, in.DatasourceName),
		"provider":   envOr(env.DSProviderType, in.ProviderType),
	})

	var cred Credential
	if credEnc != "" {
		plain, err := c.dec.Decrypt(credEnc)
		if err != nil {
			// 解密失败最常见的原因是换了加密密钥。
			// 必须说清楚是「凭据读不出来」而不是「密码错」—— 后者会让人去改密码，白折腾
			return nil, fmt.Errorf("%w: 凭据解密失败（加密密钥是否变过？）: %v", providers.ErrAuth, err)
		}
		if err := json.Unmarshal([]byte(plain), &cred); err != nil {
			return nil, fmt.Errorf("%w: 凭据格式不对: %v", providers.ErrAuth, err)
		}
	}

	ptype := providerTypeOf(in, env)
	switch ptype {
	case "kite":
		return &providers.Kite{
			Endpoint: endpoint, AuthType: authType,
			Username: cred.Username, Password: cred.Password, APIKey: cred.APIKey,
		}, nil
	case "rancher":
		return &providers.Rancher{
			Endpoint: endpoint, AuthType: authType,
			Username: cred.Username, Password: cred.Password, APIKey: cred.APIKey,
			InsecureTLS: cred.InsecureTLS,
		}, nil
	case "argocd":
		return &providers.ArgoCD{
			Endpoint: endpoint, AuthType: authType,
			Username: cred.Username, Password: cred.Password, APIKey: cred.APIKey,
			InsecureTLS: cred.InsecureTLS,
		}, nil
	default:
		return nil, fmt.Errorf("不支持的 provider 类型 %q", ptype)
	}
}

// classify 把错误映射成 last_sync_status 的取值。
//
// 🔴 未识别的错误落到 "error" 并**打 WARN 日志** ——
// 新增一种失败模式时必须被发现，不能静默归到「其他」，
// 那样界面上永远显示一句没用的「采集失败」。
func classify(err error) string {
	switch {
	case errors.Is(err, providers.ErrAuth):
		return "auth_failed"
	case errors.Is(err, providers.ErrUnreachable):
		return "unreachable"
	case errors.Is(err, providers.ErrForbidden):
		return "forbidden"
	default:
		logx.J("collect", "unclassified_error", map[string]any{
			"level": "warn", "err": err.Error()})
		return "error"
	}
}

// rulesKey 把一套采集规则压成一个可比较的字符串。
//
// 🔴 必须覆盖**全部**规则维度（ns 包含/排除 + workload 包含/排除）。
// 漏掉任何一维，两套不同规则就会算出同一个 key，于是 A 项目的服务集合
// 被复用进 B 项目的快照 —— 而那是静默的：两个项目的快照都"有数据"，
// 只是其中一份是错的，界面上完全看不出来。
//
// ⚠️ 顺序敏感是**故意的**：规则顺序不同就当成不同的 key。
// 排序后再比虽然能多命中一些复用，但那要求"顺序不影响结果"这个前提永远成立，
// 而 exclude 优先于 include 这类语义一旦以后变了，这里就会悄悄出错。
// 少复用几次只是慢一点，复用错了是数据错。
func rulesKey(r providers.Rules) string {
	var b strings.Builder
	write := func(tag string, xs []string) {
		b.WriteString(tag)
		for _, x := range xs {
			b.WriteString("\x01")
			b.WriteString(strings.TrimSpace(x))
		}
		b.WriteString("\x02")
	}
	write("ni", r.NS.Include)
	write("ne", r.NS.Exclude)
	write("wi", r.Workload.Include)
	write("we", r.Workload.Exclude)
	return b.String()
}

// envOr 环境级的值优先，空则用平台级的。只用于日志展示。
func envOr(envVal, orgVal string) string {
	if strings.TrimSpace(envVal) != "" {
		return envVal
	}
	return orgVal
}


// providerTypeOf 这个环境实际该用哪种系统协议。
//
// 🔴 必须与连接信息**同一套优先级**（见 store.OrgEnv.Conn），否则会拿 A 的协议连 B 的地址：
//
//	环境引用了数据源 → 用那个数据源的类型（同一客户 UAT 用 Rancher、PROD 用 ArgoCD 很常见）
//	否则             → 平台自己的类型 → 平台数据源的类型
//
// ⚠️ 抽成函数是因为它有**两个**调用点：建 provider、判断"要不要填集群"。
//
//	原来两处各写各的，改了前者漏了后者 —— 表现从"拿 rancher 协议连 argocd 地址超时"
//	变成"A公司/PROD 未配置集群"，看着是两个 bug，其实是同一个判据散在两处。
func providerTypeOf(in store.Org, env store.OrgEnv) string {
	if strings.TrimSpace(env.DSEndpoint) != "" && strings.TrimSpace(env.DSProviderType) != "" {
		return env.DSProviderType
	}
	if t := strings.TrimSpace(in.ProviderType); t != "" {
		return t
	}
	return in.DSProviderType
}
