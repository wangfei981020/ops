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

// Decryptor 解密组织凭据。抽成接口是为了让采集器能被测试
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

// CollectAll 采集所有启用的组织。
//
// 一个组织失败不影响其他组织 —— 这是刻意的：
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

// CollectOrg 采集一个组织的所有环境。
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

	for _, env := range in.Envs {
		if onlyEnv != "" && env.Env != onlyEnv {
			continue
		}
		if onlyProject != 0 && env.ProjectID != onlyProject {
			continue
		}
		if err := c.collectEnv(ctx, in, env); err != nil {
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

	switch {
	case failCount == 0:
		return nil // collectEnv 内部已把状态置为 success
	case okCount == 0:
		// 全挂：按第一个错误分类。分类是为了让人一眼知道该找谁 ——
		// 密码错找对方管理员、网络不通找网络、403 找对方要权限，处理路径完全不同
		return c.st.MarkSyncFailed(ctx, in.ID, classify(firstErr), firstErr.Error())
	default:
		// 部分成功：单独一态。
		// 🔴 不能算成 success —— 那会让「PROD 拉到了、UAT 没拉到」看起来一切正常，
		//    而 UAT 那半边的对账结论其实是基于旧数据的。
		return c.st.MarkSyncFailed(ctx, in.ID, "partial",
			fmt.Sprintf("%d/%d 个环境采集失败，首个错误：%v", failCount, okCount+failCount, firstErr))
	}
}

func (c *Collector) collectEnv(ctx context.Context, in store.Org, env store.OrgEnv) (retErr error) {
	started := time.Now()
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
		if err := c.st.MarkEnvCollect(context.WithoutCancel(ctx), in.ID, env.ProjectID, env.Env, st, errMsg); err != nil {
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
		if in.ProviderType != "argocd" {
			return fmt.Errorf("%s/%s 未配置集群", in.Name, env.Env)
		}
		clusters = []string{""}
	}

	rules := providers.Rules{
		NS:       providers.NSRules{Include: env.NSInclude, Exclude: env.NSExclude},
		Workload: providers.WorkloadRules{Include: env.WorkloadInclude, Exclude: env.WorkloadExclude},
	}
	logx.Debug("collect", "env_start", map[string]any{
		"org": in.Name, "env": env.Env, "provider": in.ProviderType,
		"clusters": clusters, "ns_include": env.NSInclude, "ns_exclude": env.NSExclude})

	// 一个环境可能跨多个集群，合并结果。
	// 合并时若同一个 service_key 在两个集群上版本不同 → 同名冲突，
	// 跟 ns 误抓是同一类问题，同样拒绝判定。
	merged := map[string]providers.ServiceSnapshot{}
	var order []string
	var allPods []providers.PodInfo
	for _, cluster := range clusters {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		res, err := p.ListServices(cctx, cluster, rules, true)
		cancel()
		if err != nil {
			return fmt.Errorf("集群 %s: %w", cluster, err)
		}
		snaps := res.Services
		allPods = append(allPods, res.Pods...)
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
	if err := c.st.SaveSnapshots(ctx, in.ID, env.ProjectID, env.Env, out); err != nil {
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

	logx.Info("collect", "env_done", map[string]any{
		"org": in.Name, "env": env.Env,
		"services": len(out), "pods": len(allPods), "clusters": len(clusters)})
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
		"datasource": in.DatasourceName,
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

	// 类型同样可以来自数据源 —— 平台绑了数据源就不必再填一遍类型
	ptype := in.ProviderType
	if strings.TrimSpace(ptype) == "" {
		ptype = in.DSProviderType
	}
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
