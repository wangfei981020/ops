package providers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ops-version-backend/internal/imageref"
	"ops-version-backend/logx"
)

// Rancher 通过 Rancher 读一个下游集群。
//
// 关键认知：**Rancher 本身就是 apiserver 的反向代理** ——
// 能登 UI 就能拿到数据，不需要对方给集群 kubeconfig 或证书。
// 请求打到 /k8s/clusters/<clusterId>/... ，Rancher 转发到目标集群的 apiserver，
// 权限范围与该账号在 UI 上能看到的完全一致。
//
// ⚠️ 与 Kite 的两处关键差异：
//   - Rancher 用 `Authorization: Bearer <token>`；Kite 只认 Cookie，传 Bearer 会 401
//   - Rancher 的密码登录把 token 放**响应体**；Kite 放 Set-Cookie 且响应是 204
//
// ⚠️ 一个平台可能有两套 Rancher（UAT 一个、PROD 一个），
// 所以 endpoint 和凭据是**按环境**配的，不是按组织 —— 见 org_envs 表。
type Rancher struct {
	Endpoint string // https://rancher.x-corp.com
	AuthType string // password | api_key
	Username string
	Password string
	APIKey   string // Bearer token（Account & API Keys 里创建的）

	// InsecureTLS 对方 Rancher 用自签证书时才开。
	// 默认关：默默跳过证书校验会让中间人攻击无声无息，必须是显式选择。
	InsecureTLS bool

	HTTP *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

func (r *Rancher) Type() string { return "rancher" }

func (r *Rancher) client() *http.Client {
	if r.HTTP != nil {
		return readOnlyClient(r.HTTP)
	}
	c := &http.Client{Timeout: 60 * time.Second}
	if r.InsecureTLS {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	// 🔴 只读闸门包在最外层：TLS 配置在里面，拦截在外面 ——
	//    顺序反了的话，改 TLS 的那行会把闸门整个覆盖掉。
	return readOnlyClient(c)
}

// ensureToken 密码方式换 token；API Key 方式直接用。
//
// Rancher 的 local provider 登录：POST /v3-public/localProviders/local?action=login
// 返回体里的 token 形如 `token-xxxxx:yyyyyyyy`，直接当 Bearer 用。
//
// ⚠️ 如果对方是用 SSO 登录 Rancher（没有本地密码），这条路走不通，
// 必须回落到 API Key 或手工导入 —— 报错要说清楚，别让人以为是密码打错了。
func (r *Rancher) ensureToken(ctx context.Context) (string, error) {
	if r.AuthType == "api_key" {
		if r.APIKey == "" {
			return "", fmt.Errorf("%w: 未配置 API Key", ErrAuth)
		}
		return r.APIKey, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.token != "" && time.Now().Add(5*time.Minute).Before(r.tokenExp) {
		return r.token, nil
	}

	body, _ := json.Marshal(map[string]any{
		"username":     r.Username,
		"password":     r.Password,
		"responseType": "json",
		// ttl 0 = 用 Rancher 默认；不主动要长 token，够一次采集就行
	})
	url := strings.TrimRight(r.Endpoint, "/") + "/v3-public/localProviders/local?action=login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("%w: Rancher 拒绝了用户名或密码 (HTTP %d)。"+
			"若该账号是通过 SSO 登录的，本地密码方式不可用，请改用 API Key", ErrAuth, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: 登录返回 HTTP %d", ErrUnreachable, resp.StatusCode)
	}

	var out struct {
		Token   string `json:"token"`
		Expired string `json:"expiresAt"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
		// 2xx 但没拿到 token —— 显式报错，不要返回空 token 让后续以 401 面目出现，
		// 那会被误判成密码错，实际可能是对方 Rancher 关了 local provider
		return "", fmt.Errorf("%w: 登录返回 %d 但响应里没有 token（对方可能禁用了本地认证）",
			ErrAuth, resp.StatusCode)
	}
	r.token = out.Token
	if t, e := time.Parse(time.RFC3339, out.Expired); e == nil && !t.IsZero() {
		r.tokenExp = t
	} else {
		r.tokenExp = time.Now().Add(12 * time.Hour)
	}
	return r.token, nil
}

func (r *Rancher) get(ctx context.Context, path string) ([]byte, error) {
	return r.getWith(ctx, path, false)
}

// getQuiet 与 get 相同，但失败**不打 WARN**。
//
// 只给"探测型"请求用：明知可能没权限、失败了也有兜底路径的那种
// （如展开 ns 时试着列 namespaces）。
// ⚠️ 预期内的失败打成 WARN，等于每次采集都往日志里灌假警报 ——
// 久了人就不看 WARN 了，真出事那条也被淹掉。
func (r *Rancher) getQuiet(ctx context.Context, path string) ([]byte, error) {
	return r.getWith(ctx, path, true)
}

func (r *Rancher) getWith(ctx context.Context, path string, quiet bool) ([]byte, error) {
	tok, err := r.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(r.Endpoint, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok) // ⚠️ 与 Kite 相反，Rancher 认 Bearer

	started := time.Now()
	resp, err := r.client().Do(req)
	if err != nil {
		logx.Debug("rancher", "request_fail", map[string]any{
			"path": path, "endpoint": r.Endpoint, "err": err.Error(),
			"ms": time.Since(started).Milliseconds()})
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	logx.Debug("rancher", "request", map[string]any{
		"path": path, "endpoint": r.Endpoint, "status": resp.StatusCode,
		"bytes": len(data), "ms": time.Since(started).Milliseconds()})

	if resp.StatusCode >= 300 {
		body := SafeBody(data, 600)
		lvl := logx.Warn
		if quiet {
			lvl = logx.Debug
		}
		lvl("rancher", "request_failed", map[string]any{
			"path": path, "endpoint": r.Endpoint, "status": resp.StatusCode,
			"auth_type": r.AuthType, "body": body})
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			r.mu.Lock()
			r.token = ""
			r.mu.Unlock()
			return nil, fmt.Errorf("%w: Rancher 拒绝了这个凭据（认证方式 %s）", ErrAuth, r.AuthType)
		case resp.StatusCode == http.StatusForbidden:
			// 🔴 必须分清"集群级 list 被拒"和"这个 ns 被拒"，两者该做的事相反：
			//   前者是**我们要多了** —— 账号只有 project/namespace 级权限（Rancher 常态），
			//     该改的是我们的采集范围，不是去给账号加集群权限
			//   后者才是账号确实缺这一个 ns
			// 原来只有一句"去 Rancher 绑集群只读角色"，把人直接引向放大权限 ——
			// 而那个账号在 Rancher UI 里明明看得到 Pod，于是更困惑。
			return nil, fmt.Errorf("%w: %s", ErrForbidden, rancherForbiddenHint(path, body))
		default:
			return nil, fmt.Errorf("%w: Rancher 返回 HTTP %d，详情见服务端日志", ErrUnreachable, resp.StatusCode)
		}
	}
	return data, nil
}

func (r *Rancher) Probe(ctx context.Context) error {
	if _, err := r.ensureToken(ctx); err != nil {
		return err
	}
	_, err := r.get(ctx, "/v3/clusters?limit=1")
	return err
}

// Clusters 列出这套 Rancher 管的下游集群，配置组织时给用户下拉选。
// 返回 clusterId → 显示名。clusterId 形如 c-m-xxxxxxx，是后面拼代理路径要用的。
func (r *Rancher) Clusters(ctx context.Context) (map[string]string, error) {
	data, err := r.get(ctx, "/v3/clusters")
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("解析 clusters 失败: %w", err)
	}
	m := map[string]string{}
	for _, c := range out.Data {
		m[c.ID] = c.Name
	}
	return m, nil
}

// ListServices 走 Rancher 的 apiserver 代理拉工作负载。
//
// 🔴 **配了确切 ns 就逐个 ns 请求**，理由与 Kite 那边完全一致（见 kite.go 的说明）。
//
// Rancher 只是把请求转给目标集群的 apiserver，所以吃的是 **k8s 原生 RBAC**：
//
//	/apis/apps/v1/deployments                    → 集群级 list，要 ClusterRole
//	/apis/apps/v1/namespaces/<ns>/deployments    → ns 级 list，Role 即可
//
// 对方给我们的通常是「几个业务 ns 的只读 Role」，走第一条必然 403，
// 而 403 里只会说 `deployments is forbidden ... at the cluster scope` ——
// 看着像"权限没给够"，其实是我们要多了。
//
// ⚠️ ns 规则为空或含通配时只能回退到集群级 list（通配必须拿到全量才能匹配），
//
//	那种情况确实需要 ClusterRole。
func (r *Rancher) ListServices(ctx context.Context, clusterID string, rules Rules, withRuntime bool) (*ListResult, error) {
	base := "/k8s/clusters/" + clusterID + "/apis/apps/v1/"
	exactNS, expanded := r.resolveNamespaces(ctx, clusterID, rules)
	// 🔴 展开成功却一个都没匹配 → 明确报出来，**不要回退去拉整集群**。
	//    回退的话必然 403，然后报「权限不足」—— 而真实原因是规则没匹配上。
	if expanded && len(exactNS) == 0 {
		return nil, nsNoMatchErr(clusterID, rules, r.candidateNamespaces(ctx, clusterID))
	}
	var rows []rawWorkload
	var excluded []ExcludedService

	for _, res := range []string{"deployments", "statefulsets"} {
		var data []byte
		var err error
		if len(exactNS) > 0 {
			data, err = r.fetchNS(ctx, base, res, exactNS)
		} else {
			data, err = r.get(ctx, base+res)
		}
		if err != nil {
			if res == "deployments" {
				// 🔴 deployments 读不到时，**先试试只用 Pod 采**再决定失败。
				//
				// 客户给的只读账号常常只授了 pods（Rancher UI 的 Workloads→Pods
				// 能看到东西，但 apps/v1 的 deployments 一个都读不到）。
				// 那种情况下直接报错，等于因为一个我们不需要的资源而完全采不到数据 ——
				// 而 Pod 的 spec.containers[].image 本身就带完整 tag，
				// 而且它是**实际在跑的**版本，比 Deployment 声明的更接近事实。
				if !errors.Is(err, ErrForbidden) {
					return nil, err
				}
				logx.Info("rancher", "fallback_pods_only", map[string]any{
					"cluster": clusterID,
					"note":    "读不到 deployments，改从 Pod 反推服务版本（账号可能只授了 pods）"})
				return r.listFromPods(ctx, clusterID, rules, exactNS, err)
			}
			continue
		}
		var l k8sList
		if err := json.Unmarshal(data, &l); err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", res, err)
		}
		for _, it := range l.Items {
			if !rules.NS.Match(it.Metadata.Namespace) {
				continue
			}
			// workload 级过滤：各家部署的服务集合并不相同，
			// 只按 ns 抄会让对账表多出一堆「对方没有」的噪音行
			if !rules.Workload.Match(it.Metadata.Name) {
				// 🔴 记下**被排掉了什么**，而不是让下游拿规则反推。
				//    规则比的是 workload 名，而预检/对账认的是 ServiceKey ——
				//    helm 会把 release 名拼进 workload 名，两者对不上。
				for _, c := range it.Spec.Template.Spec.Containers {
					if ref := imageref.Parse(c.Image); ref.Name != "" {
						excluded = append(excluded, ExcludedService{
							ServiceKey: ref.Name, Workload: it.Metadata.Name,
							Namespace: it.Metadata.Namespace,
						})
					}
				}
				continue
			}
			for _, c := range it.Spec.Template.Spec.Containers {
				rows = append(rows, rawWorkload{
					Namespace: it.Metadata.Namespace,
					Name:      it.Metadata.Name,
					Kind:      res,
					Image:     c.Image,
				})
			}
		}
	}

	out := &ListResult{Services: buildSnapshots(rows, nil), Excluded: excluded}
	if withRuntime {
		out.Pods = r.fillRuntime(ctx, clusterID, rules, out.Services)
	}
	return out, nil
}

func (r *Rancher) fillRuntime(ctx context.Context, clusterID string, rules Rules, snaps []ServiceSnapshot) []PodInfo {
	// pods 同样逐 ns 拉（k8s 原生路径是 /api/v1/namespaces/<ns>/pods）
	var data []byte
	var err error
	ns, expanded := r.resolveNamespaces(ctx, clusterID, rules)
	switch {
	case len(ns) > 0:
		data, err = r.fetchNS(ctx, "/k8s/clusters/"+clusterID+"/api/v1/", "pods", ns)
	case expanded:
		// 展开成功但零命中：这里不该去拉整集群（必然 403，白打一次请求）。
		// ⚠️ 走到这一步说明 ListServices 已经报错返回了，正常不会到这儿；
		//    留着是防止将来有人在别处直接调 fillRuntime。
		return nil
	default:
		data, err = r.get(ctx, "/k8s/clusters/"+clusterID+"/api/v1/pods")
		if err != nil {
			// 🔴 整集群拉不到时，用**刚采到的服务所在的 ns** 逐个再拉一遍。
			//
			//    那些 ns 的权限一定是有的 —— 它们的 Deployment 我们刚读到了。
			//    不兜这一下的话，project 级只读账号永远拿不到 Pod 明细，
			//    导出的明细页只有一句「可能是 pod 接口读取失败」，
			//    而"能拿到多少就给多少"本来完全做得到。
			if fromSnaps := namespacesOf(snaps); len(fromSnaps) > 0 {
				logx.Info("rancher", "pods_fallback_by_ns", map[string]any{
					"cluster": clusterID, "namespaces": len(fromSnaps),
					"note": "整集群 pods 被拒，改用刚采到的服务所在 ns 逐个拉"})
				data, err = r.fetchNS(ctx, "/k8s/clusters/"+clusterID+"/api/v1/", "pods", fromSnaps)
			}
		}
	}
	if err != nil {
		// ⚠️ 必须记一条：不记的话，导出的明细页只写着「可能是 pod 接口读取失败」，
		//    而到底是不是、为什么，日志里一个字都没有。
		logx.Warn("rancher", "pods_unavailable", map[string]any{
			"cluster": clusterID, "err": err.Error(),
			"note": "拿不到 Pod 明细；版本比对不受影响（那是从 Deployment 来的）"})
		return nil
	}
	var l podList
	if json.Unmarshal(data, &l) != nil {
		return nil
	}
	running := map[string]imageref.Ref{}
	wanted := make(map[string]bool, len(snaps))
	for _, sn := range snaps {
		wanted[sn.ServiceKey] = true
	}
	var pods []PodInfo
	for _, it := range l.Items {
		if !rules.NS.Match(it.Metadata.Namespace) {
			continue
		}
		for _, cs := range it.Status.ContainerStatuses {
			ref := imageref.Parse(cs.Image)
			if ref.Name == "" {
				continue
			}
			ref.Digest = imageref.Parse(cs.ImageID).Digest
			running[ref.Name] = ref
			// 🔴 只留被 workload 规则采进来的那些服务的 Pod。
			//    Pod 名带随机后缀，不能直接按 workload 名匹配 ——
			//    用「这个服务在不在快照里」来判，天然与上面的过滤一致。
			if !wanted[ref.Name] {
				continue
			}
			pods = append(pods, podInfoOf(it.Metadata.Namespace, it.Metadata.Name,
				it.Spec.NodeName, it.Status.Phase, it.Status.PodIP, it.Status.StartTime,
				cs.Name, cs.Ready, cs.RestartCount, ref))
		}
	}
	for i := range snaps {
		if v, ok := running[snaps[i].ServiceKey]; ok {
			snaps[i].RunningTag = v.Tag
			if snaps[i].Digest == "" {
				snaps[i].Digest = v.Digest
			}
		}
	}
	return pods
}

// fetchNS 逐个 ns 走 k8s 原生的 ns 级路径拉取，再拼成一个列表。
//
// ⚠️ 与 Kite 那边同样的取舍：单个 ns 失败不中断整体（对方可能列了一个已删的 ns），
// 但全部失败必须报错 —— 返回空列表会在界面上显示成「这个平台没有任何服务」，
// 跟「对方真的下线了所有服务」长得一模一样。
func (r *Rancher) fetchNS(ctx context.Context, base, res string, nss []string) ([]byte, error) {
	var chunks [][]byte
	var lastErr error
	for _, ns := range nss {
		data, err := r.get(ctx, base+"namespaces/"+url.PathEscape(ns)+"/"+res)
		if err != nil {
			lastErr = err
			logx.Warn("rancher", "namespace_failed", map[string]any{
				"namespace": ns, "resource": res, "err": err.Error()})
			continue
		}
		chunks = append(chunks, data)
	}
	if len(chunks) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return mergeItems(chunks)
}

// mergeItems 把多次 list 的响应合并成一个 List 对象。
//
// 🔴 **必须用 json.RawMessage，不能解析成具体结构再序列化回去。**
//
// 我第一版用 k8sList 解析后再 Marshal —— 对 Deployment 看着没问题，
// 但同一个函数也被 pods 用，而 Pod 的 spec.nodeName / status.podIP /
// status.containerStatuses 根本不在 k8sList 里：
// 解析时被丢弃，Marshal 出来就永远是空的，**全程不报错**。
// 表现是「Pod 明细里节点、IP、digest 全为空」，
// 看起来像对方集群没这些数据，而不像我们把它们扔了。
//
// 原样透传就不存在这个问题，也不必为每种资源各写一个合并函数。
func mergeItems(chunks [][]byte) ([]byte, error) {
	var items []json.RawMessage
	for _, c := range chunks {
		var l struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(c, &l); err != nil {
			return nil, err
		}
		items = append(items, l.Items...)
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	return json.Marshal(struct {
		Items []json.RawMessage `json:"items"`
	}{items})
}

// resolveNamespaces 定出这次要逐个请求哪些 ns；返回 nil 表示只能整集群拉。
//
// 🔴 Rancher 的只读账号**多数是 project 级的**（Rancher 的权限模型以 project 为中心，
// 一个 project 圈若干 namespace）。这类账号在 Rancher UI 里能正常看到 Pod 列表，
// 但打 k8s 的**集群级** list 会被拒：
//
//	deployments.apps is forbidden: User "u-xxxxx" cannot list resource "deployments"
//	in API group "apps" **at the cluster scope**
//
// 于是"UI 里明明看得到，工具却说没权限"—— 人的第一反应是我们要的权限太大，
// 其实是我们**打错了接口**。逐 ns 请求只要 namespace 级权限，正好对上。
//
// ⚠️ 与 Kite 同一套思路（见 kite.go），但候选集来源不同：
// Rancher 没有"我的角色"这种接口，只能问集群要 namespace 列表。
// resolveNamespaces 定出这次要逐个查询哪些 namespace。
//
// 🔴 第二个返回值 expanded 区分两件**处理方式完全不同**的事：
//
//	expanded=false —— 读不到 ns 列表，无法展开通配 → 只能回退整集群拉
//	expanded=true  —— 展开成功，ns 就是结果（**可能是空的**）
//
// ⚠️ 少了这个区分，「展开成功但零命中」会和「无法展开」混成一路，
//
//	双双回退去拉整集群 → 403 → 报「权限不足」。
//	而真实原因是「你写的 ns 规则在这个集群上一个都没匹配上」——
//	一个去放大权限，一个去改规则，方向完全相反。实测被这条误导过。
func (r *Rancher) resolveNamespaces(ctx context.Context, clusterID string, rules Rules) (ns []string, expanded bool) {
	if ex := exactNamespaces(rules.NS); len(ex) > 0 {
		return ex, true
	}
	// 规则本身就是空的 = 不限 ns，那本来就该整集群拉
	if len(rules.NS.Include) == 0 {
		return nil, false
	}
	cands := r.candidateNamespaces(ctx, clusterID)
	if len(cands) == 0 {
		return nil, false
	}
	var out []string
	for _, n := range cands {
		if rules.NS.Match(n) {
			out = append(out, n)
		}
	}
	logx.Info("rancher", "ns_expanded", map[string]any{
		"cluster": clusterID, "include": rules.NS.Include,
		"candidates": len(cands), "matched": len(out),
		"sample": sampleNS(cands, 8)})
	return out, true
}

// nsNoMatchErr 通配展开成功但一个都没匹配上。
//
// 🔴 这是**配置问题**，不是权限问题，更不是"对方什么都没部署"。
// 所以既不能报权限不足（方向反了），也不能返回空结果当成功
// （空结果和"对方真的没部署"在界面上分不出来）。
//
// ⚠️ 必须把**实际存在的 ns** 列几个出来 —— 只说"没匹配上"的话，
// 人下一步只能去 Rancher 界面上一个个翻。
func nsNoMatchErr(clusterID string, rules Rules, cands []string) error {
	return fmt.Errorf(
		"ns 规则在集群 %s 上没有匹配到任何命名空间：「ns 包含」写的是 %s，"+
			"而这个账号在该集群能看到的是 %s（共 %d 个）。"+
			"⚠️ 这不是权限问题 —— 对方可能没有这些命名空间，或者名字跟你写的不一样。"+
			"照上面列出的实际名字改「ns 包含」即可。",
		clusterID, strings.Join(rules.NS.Include, "、"),
		strings.Join(sampleNS(cands, 12), "、"), len(cands))
}

// sampleNS 取前 n 个做样例。全列出来会把错误信息撑成几百行。
func sampleNS(all []string, n int) []string {
	if len(all) <= n {
		return all
	}
	out := make([]string, 0, n+1)
	out = append(out, all[:n]...)
	return append(out, fmt.Sprintf("…等 %d 个", len(all)))
}

// candidateNamespaces 拿一份可用来展开的 ns 列表。
//
// ⚠️ 这个接口本身也可能被拒（namespaces 同样是集群级资源）。
// 拒了就返回 nil 回退整集群拉 —— 那条路会给出明确的提示让人手填 ns，
// 而不是在这里静默返回空集合（空集合会让采集"成功但零结果"，
// 跟"对方什么都没部署"分不出来）。
func (r *Rancher) candidateNamespaces(ctx context.Context, clusterID string) []string {
	// ① k8s API（经 Rancher 代理）—— 集群级账号走这条
	if ns := r.nsFromK8sAPI(ctx, clusterID); len(ns) > 0 {
		return ns
	}
	// ② 🔴 Rancher 自己的 v3 接口 —— **project 级只读账号能读到它可见的 ns**。
	//
	//    k8s 的 namespaces 是集群级资源，project 级账号必然被拒；
	//    而 Rancher v3 会按调用者的权限过滤，只返回他看得见的那些。
	//    少了这条兜底，通配（biz-*）对 project 级账号完全用不了 ——
	//    用户被迫把几十个 ns 名逐行手打，而拼错了不会报错，
	//    只是那个 ns 悄悄没被采集。实测就是这么卡住的。
	if ns := r.nsFromRancherV3(ctx, clusterID); len(ns) > 0 {
		return ns
	}
	logx.Debug("rancher", "ns_list_denied", map[string]any{
		"cluster": clusterID,
		"note":    "k8s API 和 Rancher v3 两条路都读不到 namespace 列表，无法展开通配"})
	return nil
}

func (r *Rancher) nsFromK8sAPI(ctx context.Context, clusterID string) []string {
	data, err := r.getQuiet(ctx, "/k8s/clusters/"+clusterID+"/api/v1/namespaces")
	if err != nil {
		logx.Debug("rancher", "ns_k8s_denied", map[string]any{
			"cluster": clusterID, "err": err.Error(),
			"note": "集群级 namespaces 被拒，改用 Rancher v3 接口再试"})
		return nil
	}
	var l struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &l) != nil || len(l.Items) == 0 {
		return nil
	}
	out := make([]string, 0, len(l.Items))
	for _, it := range l.Items {
		out = append(out, it.Metadata.Name)
	}
	return out
}

// nsFromRancherV3 用 Rancher 自己的 v3 接口列 namespace。
//
// 🔴 与 k8s API 的关键差别：v3 **按调用者的权限过滤**，
// 一个只被授予了某几个 project 的账号，在这里能读到那几个 project 下的 ns，
// 而同一个账号去读 k8s 的 /api/v1/namespaces 会被直接拒。
//
// ⚠️ 只用来**展开通配**，不用来放大采集范围：展开出来的 ns 仍要过 rules.NS.Match，
// 而后续取 workload 走的还是逐 ns 查询（Role 级权限即可）。
func (r *Rancher) nsFromRancherV3(ctx context.Context, clusterID string) []string {
	data, err := r.getQuiet(ctx, "/v3/clusters/"+url.PathEscape(clusterID)+"/namespaces?limit=-1")
	if err != nil {
		logx.Debug("rancher", "ns_v3_denied", map[string]any{
			"cluster": clusterID, "err": err.Error(),
			"note": "Rancher v3 也读不到 ns —— 只能在「ns 包含」里手填确切的 ns 名"})
		return nil
	}
	var l struct {
		Data []struct {
			Name string `json:"name"`
			// ⚠️ v3 的 id 形如 "c-m-xxx:ns-name"，name 才是纯 ns 名。
			//    用 id 的话后续逐 ns 查询会拼出一个不存在的路径，
			//    表现是"展开成功了但一个服务都采不到"。
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &l) != nil || len(l.Data) == 0 {
		return nil
	}
	out := make([]string, 0, len(l.Data))
	for _, it := range l.Data {
		n := strings.TrimSpace(it.Name)
		if n == "" {
			// 兜底：个别版本只给 id，取冒号后那段
			if i := strings.LastIndexByte(it.ID, ':'); i >= 0 {
				n = it.ID[i+1:]
			}
		}
		if n != "" {
			out = append(out, n)
		}
	}
	logx.Debug("rancher", "ns_from_v3", map[string]any{
		"cluster": clusterID, "count": len(out),
		"note": "k8s API 读不到，改用 Rancher v3 展开成功（project 级账号的正常情况）"})
	return out
}

// rancherForbiddenHint 按请求形态给出**能直接照做**的 403 说明。
//
// 判据用两条：请求路径里有没有 namespaces 段，以及 Rancher 返回的原文里
// 有没有 "at the cluster scope"（k8s 在集群级 list 被拒时会明确这么写）。
func rancherForbiddenHint(path, body string) string {
	ns := ""
	if i := strings.Index(path, "/namespaces/"); i >= 0 {
		rest := path[i+len("/namespaces/"):]
		if j := strings.IndexByte(rest, '/'); j > 0 {
			ns = rest[:j]
		}
	}
	if ns != "" {
		return "Rancher 拒绝了这次请求：账号读不到命名空间 " + ns + " —— " +
			"去 Rancher 把这个 namespace 所在的 project 加到该账号的只读授权里，" +
			"或把它从本环境的「ns 包含」里去掉。"
	}
	// 集群级 list 被拒
	extra := ""
	if strings.Contains(body, "at the cluster scope") {
		extra = "（Rancher 原话里写着 at the cluster scope，就是这个意思）"
	}
	return "Rancher 拒绝了这次请求：读整个集群的工作负载需要**集群级**权限，" +
		"而这个账号多半只有 project / namespace 级的只读授权" + extra + "。" +
		"⚠️ 不必去放大权限 —— 在本环境的「ns 包含」里填上要采集的命名空间（一行一个），" +
		"我们就会按 namespace 逐个读取，project 级授权就够了。" +
		"（自动展开需要账号能列出 namespace 列表，你这个账号读不到，所以要手填。）"
}

// listFromPods 只用 Pod 采集服务版本 —— deployments 读不到时的兜底。
//
// 🔴 与正常路径的**语义差别必须说清楚**：
//   - 正常路径读 Deployment 的 spec，副本缩到 0 的服务也有版本
//   - 这条路径只看得到**有 Pod 在跑的**服务；副本 0 的会整个消失
//
// 所以它是**降级**不是等价替代。采到的数据仍然是真实的（Pod 里跑的就是这个镜像），
// 只是覆盖面可能小于对方实际部署的服务集合。
//
// ⚠️ 一个 Pod 都读不到时，把**原来那个 deployments 的 403** 原样抛回去，
// 而不是报"没有 Pod" —— 后者会把"权限不够"伪装成"对方什么都没部署"。
func (r *Rancher) listFromPods(ctx context.Context, clusterID string, rules Rules,
	exactNS []string, deployErr error) (*ListResult, error) {

	base := "/k8s/clusters/" + clusterID + "/api/v1/"
	var data []byte
	var err error
	if len(exactNS) > 0 {
		data, err = r.fetchNS(ctx, base, "pods", exactNS)
	} else {
		data, err = r.get(ctx, base+"pods")
	}
	if err != nil {
		// Pod 也读不到 ⇒ 报原来那个错（deployments 的），它更能说明问题
		return nil, deployErr
	}

	var l podList
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("解析 pods 失败: %w", err)
	}

	var rows []rawWorkload
	// 🔴 Pod 明细也要一并带出去。
	//
	//    这份 Pod 数据**已经在手里了** —— 原来只拿它反推服务版本，
	//    然后把明细扔掉，于是「版本比对有 164 行、Pod 明细页整页空」，
	//    导出的 Excel 上只有一句「采集成功，但没有 Pod 明细」。
	//    而这条路径正是「只读账号只有 pods 权限」时走的，
	//    也就是**最需要明细**的那种情况（没有 Deployment 可看）。
	//
	// ⚠️ 与 mergeItems 那次是同一类错：数据取到了，却在中途被丢掉，全程不报错。
	var pods []PodInfo
	for _, it := range l.Items {
		if !rules.NS.Match(it.Metadata.Namespace) {
			continue
		}
		// 🔴 workload 名要从 Pod 名剥掉副本后缀，否则每个 Pod 都成了一个"服务"。
		//    Deployment 的 Pod 是 <deploy>-<rs哈希>-<随机5位>，
		//    StatefulSet 的是 <sts>-<序号> —— 两种形状都要认。
		wl := workloadNameOfPod(it.Metadata.Name)
		if !rules.Workload.Match(wl) {
			continue
		}
		// ⚠️ 用 status.containerStatuses 而不是 spec.containers：
		//    前者是**实际拉起来的**镜像。滚动更新中途两者会不一致，
		//    而这条路径的意义正是"现在真的在跑什么"。
		for _, cs := range it.Status.ContainerStatuses {
			if cs.Image == "" {
				continue
			}
			rows = append(rows, rawWorkload{
				Namespace: it.Metadata.Namespace,
				Name:      wl,
				Kind:      "pods", // 标明数据来源与正常路径不同
				Image:     cs.Image,
			})
			ref := imageref.Parse(cs.Image)
			if ref.Name == "" {
				continue
			}
			// digest 来自 imageID —— 那是**实际拉下来的**那一份
			ref.Digest = imageref.Parse(cs.ImageID).Digest
			pods = append(pods, podInfoOf(it.Metadata.Namespace, it.Metadata.Name,
				it.Spec.NodeName, it.Status.Phase, it.Status.PodIP, it.Status.StartTime,
				cs.Name, cs.Ready, cs.RestartCount, ref))
		}
	}
	if len(rows) == 0 {
		// 有 Pod 权限但一个都没匹配上 —— 仍然按原来的 403 报，
		// 因为更可能是"能读的 ns 里没有目标服务"而不是"对方没部署"
		return nil, deployErr
	}

	out := &ListResult{
		Services: buildSnapshots(rows, rules.Workload.Include), Pods: pods,
		// 🔴 这个标记要一路传到对账表头。只写日志的话，看表的人无从知道
		//    这一列的「未部署」其实是「副本 0，我们看不见」。
		Degraded:       true,
		DegradedReason: "版本取自 Pod 的 imageID（读不到 deployments）；副本为 0 的服务不会出现在结果里",
	}
	logx.Info("rancher", "collected_from_pods", map[string]any{
		"cluster": clusterID, "services": len(out.Services),
		"pods_total": len(l.Items), "pods_kept": len(pods),
		"note": "数据来自 Pod；副本为 0 的服务不会出现在结果里"})
	return out, nil
}

// workloadNameOfPod 从 Pod 名推回 workload 名。
//
//	Deployment:  wallet-backend-7d4f8b9c6d-x2k9p → wallet-backend
//	StatefulSet: mysql-0                          → mysql
//
// ⚠️ 只剥**明显是生成的**后缀（10 位左右的哈希 + 5 位随机；或纯数字序号）。
// 剥过头会把 `foo-v2` 这类真名字截断 —— 那会让两个不同服务合并成一个。
func workloadNameOfPod(pod string) string {
	parts := strings.Split(pod, "-")
	if len(parts) < 2 {
		return pod
	}
	last := parts[len(parts)-1]

	// StatefulSet：末段是纯数字序号
	if isAllDigits(last) {
		return strings.Join(parts[:len(parts)-1], "-")
	}
	// Deployment：末两段是 <rs哈希>-<随机5位>，都是小写字母数字
	if len(parts) >= 3 && len(last) == 5 && isAlnumLower(last) {
		mid := parts[len(parts)-2]
		if len(mid) >= 5 && len(mid) <= 11 && isAlnumLower(mid) {
			return strings.Join(parts[:len(parts)-2], "-")
		}
	}
	return pod
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isAlnumLower(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// namespacesOf 从已采到的服务里取出去重后的 ns 列表。
//
// 用途：整集群拉 Pod 被拒时的兜底 —— 这些 ns 的读权限一定是有的
// （它们的 Deployment 刚刚才读到）。
func namespacesOf(snaps []ServiceSnapshot) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range snaps {
		n := strings.TrimSpace(s.Namespace)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}
