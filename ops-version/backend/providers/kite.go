package providers

import (
	"bytes"
	"context"
	"encoding/json"
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

// Kite 通过 Kite（K8s Dashboard）读多个集群。
//
// 实测要点（2026-08-17 对本地 Kite v0.14.1 验证）：
//   - 登录：POST /api/auth/login/password → 204 No Content
//     token 在 `Set-Cookie: auth_token=<JWT>`，**不在响应体里**
//   - 🔴 JWT 只认 Cookie：`Authorization: Bearer <jwt>` 返回 401
//   - 🔴 而 **API Key 相反**：走 `Authorization: <key>`，且不加 Bearer 前缀。
//     两者位置完全不同，共用一条路径必然让其中一种恒 401（见 do() 的说明）
//   - JWT Max-Age=176400（49h），过期要重登
//   - 列表：GET /api/v1/_clusters/<cluster>/deployments[/<ns>] → **原生 k8s List 对象**
//     🔴 集群与 ns 都必须走路径段，`?cluster=` 会让鉴权取到默认集群（见 clusterPath）
//     （`{metadata, items}`，每条是完整的 Deployment，spec/status 都全）
//   - image 含完整 tag，这是选 Kite 这条路的决定性依据
//   - digest 不在 deployment 里，要从 GET /api/v1/pods 的
//     status.containerStatuses[].imageID 取
type Kite struct {
	Endpoint string // 如 http://localhost:30837
	AuthType string // password | api_key
	Username string
	Password string
	APIKey   string

	HTTP *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

func (k *Kite) Type() string { return "kite" }

func (k *Kite) client() *http.Client {
	// 🔴 一律过只读闸门（含调用方注入的 HTTP）——
	//    见 readonly.go：对别人家的系统只读，不写。
	if k.HTTP != nil {
		return readOnlyClient(k.HTTP)
	}
	return readOnlyClient(&http.Client{Timeout: 30 * time.Second})
}

// ensureToken 取一个可用 token，过期或没有就重登。
//
// 提前 5 分钟视为过期：采集可能跑几十秒，卡在有效期边缘上会中途 401，
// 而中途 401 的表现是「拉了一半」——比一开始就失败更难查。
func (k *Kite) ensureToken(ctx context.Context) (string, error) {
	if k.AuthType == "api_key" {
		return k.APIKey, nil
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	if k.token != "" && time.Now().Add(5*time.Minute).Before(k.tokenExp) {
		return k.token, nil
	}

	body, _ := json.Marshal(map[string]string{"username": k.Username, "password": k.Password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(k.Endpoint, "/")+"/api/auth/login/password", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := k.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	// ⚠️ 原来这里 io.Discard 把响应体扔了 —— 登录失败的原因正好在体里
	loginBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 300 {
		lb := SafeBody(loginBody, 400)
		logx.Warn("kite", "login_failed", map[string]any{
			"endpoint": k.Endpoint, "user": k.Username, "status": resp.StatusCode, "body": lb})
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest:
			return "", fmt.Errorf("%w: Kite 拒绝了用户名或密码 (HTTP %d)：%s", ErrAuth, resp.StatusCode, lb)
		case resp.StatusCode == http.StatusForbidden:
			return "", fmt.Errorf("%w: 账号无权限 (HTTP %d)：%s", ErrForbidden, resp.StatusCode, lb)
		default:
			return "", fmt.Errorf("%w: 登录返回 HTTP %d：%s", ErrUnreachable, resp.StatusCode, lb)
		}
	}

	logx.Debug("kite", "login", map[string]any{
		"endpoint": k.Endpoint, "user": k.Username, "status": resp.StatusCode,
		"cookies": len(resp.Cookies())})

	// 204 No Content —— token 只在 Set-Cookie 里
	for _, c := range resp.Cookies() {
		if c.Name == "auth_token" && c.Value != "" {
			k.token = c.Value
			k.tokenExp = c.Expires
			if k.tokenExp.IsZero() {
				if c.MaxAge > 0 {
					k.tokenExp = time.Now().Add(time.Duration(c.MaxAge) * time.Second)
				} else {
					k.tokenExp = time.Now().Add(12 * time.Hour) // 保守兜底
				}
			}
			return k.token, nil
		}
	}
	// 登录返回 2xx 却没给 cookie —— 这种"看似成功实则没拿到凭据"必须显式报错，
	// 不能返回空 token 让后续请求以 401 的面目出现（那会被误判成密码错）
	return "", fmt.Errorf("%w: 登录返回 %d 但响应里没有 auth_token cookie", ErrAuth, resp.StatusCode)
}

func (k *Kite) do(ctx context.Context, path string) ([]byte, error) {
	return k.doWith(ctx, path, false)
}

// doTry 与 do 相同，但失败**不打 WARN**。
//
// 只给"探测型"请求用：明知可能没权限、失败了也有兜底路径的那种
// （如展开通配时先试 /api/v1/namespaces）。
// ⚠️ 预期内的失败打成 WARN，等于每次采集都往日志里灌两条假警报 ——
// 久了人就不看 WARN 了，真出事那条也被淹掉。
func (k *Kite) doTry(ctx context.Context, path string) ([]byte, error) {
	return k.doWith(ctx, path, true)
}

func (k *Kite) doWith(ctx context.Context, path string, quiet bool) ([]byte, error) {
	tok, err := k.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(k.Endpoint, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	// 🔴 Kite 的两种认证走**完全不同**的位置，不能共用一条：
	//
	//   password → 登录换 JWT，放 `Cookie: auth_token=<jwt>`（Bearer 会被拒）
	//   api_key  → 放 `Authorization: <key>`，且**不加 Bearer 前缀**
	//              （官方文档原话 "Do not prepend Bearer"，key 形如 kite<ID>-<SECRET>）
	//
	// ⚠️ 曾经两种都塞进 Cookie，于是 api_key 模式必然 401，
	//    而 Kite 对**所有**认证失败都返回同一句 "Invalid or expired token" ——
	//    连「传错位置」和「key 不对」都分不出来，只能靠查文档才定位到。
	if k.AuthType == "api_key" {
		req.Header.Set("Authorization", tok)
	} else {
		req.Header.Set("Cookie", "auth_token="+tok)
	}

	started := time.Now()
	resp, err := k.client().Do(req)
	if err != nil {
		logx.Debug("kite", "request_fail", map[string]any{
			"path": path, "endpoint": k.Endpoint, "err": err.Error(),
			"ms": time.Since(started).Milliseconds()})
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	logx.Debug("kite", "request", map[string]any{
		"path": path, "endpoint": k.Endpoint, "status": resp.StatusCode,
		"bytes": len(data), "ms": time.Since(started).Milliseconds()})

	if resp.StatusCode >= 300 {
		body := SafeBody(data, 600)
		// 🔴 把认证方式一起打出来：Kite 的 password 与 api_key 走的传递方式**完全不同**，
		//    401 时不知道用的哪种，就分不清是「凭据错」还是「传法错」。
		lvl := logx.Warn
		if quiet {
			lvl = logx.Debug
		}
		lvl("kite", "request_failed", map[string]any{
			"path": path, "endpoint": k.Endpoint, "status": resp.StatusCode,
			"auth_type": k.AuthType, "body": body, "probe": quiet})
		// 给人看的话不带原文，原文在上面那条 Warn 里
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			k.mu.Lock()
			k.token = "" // 让下次重登
			k.mu.Unlock()
			return nil, fmt.Errorf("%w: Kite 拒绝了这个凭据（认证方式 %s）", ErrAuth, k.AuthType)
		case resp.StatusCode == http.StatusForbidden:
			// 🔴 403 必须指出**缺的是哪个集群、哪个 ns 的权限**。
			//
			// Kite 的 403 原文形如
			//   `... permission to get deployments in namespace All on cluster xxx`
			// 其中 `namespace All` 是我们没在路径里带 ns 时 Kite 自己填的默认值，
			// 而 Kite 的匹配是**字面匹配**（`match(["ns-a","ns-b"], "All")` 恒为 false）——
			// 于是按最小权限配好的角色也必然被拒。
			// 用户看到这句只会以为"权限没配对"，然后去加权限，越加越大。
			// 所以这里要把「是我们要多了」讲清楚，而不是让用户去放权限。
			return nil, fmt.Errorf("%w: %s", ErrForbidden, forbiddenHint(path))
		default:
			return nil, fmt.Errorf("%w: Kite 返回 HTTP %d，详情见服务端日志", ErrUnreachable, resp.StatusCode)
		}
	}
	return data, nil
}

// Probe 测连通。刻意分三步，好让错误能指出是哪一层出的问题。
func (k *Kite) Probe(ctx context.Context) error {
	if _, err := k.ensureToken(ctx); err != nil {
		return err
	}
	if _, err := k.do(ctx, "/api/v1/clusters"); err != nil {
		return err
	}
	return nil
}

// Clusters 列出 Kite 里配置的集群，用于「配置组织」时给用户下拉选。
func (k *Kite) Clusters(ctx context.Context) ([]string, error) {
	data, err := k.do(ctx, "/api/v1/clusters")
	if err != nil {
		return nil, err
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("解析 clusters 失败: %w", err)
	}
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Name)
	}
	return out, nil
}

// k8sList 是 Kite 直接透传的原生 k8s List 对象。
type k8sList struct {
	Items []struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name  string `json:"name"`
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
		Status struct {
			ContainerStatuses []struct {
				Image   string `json:"image"`
				ImageID string `json:"imageID"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

// podList 是 Pod 列表的解析结构。
//
// ⚠️ **不能复用 k8sList**：Pod 的 nodeName 在 `spec.nodeName`，
// 而 workload 的 spec 下面是 `template.spec` —— 两者形状不同，
// 硬套会让 nodeName 恒为空，且不报错（JSON 解析对缺字段是静默的）。
type podList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			NodeName string `json:"nodeName"`
		} `json:"spec"`
		Status struct {
			Phase             string `json:"phase"`
			PodIP             string `json:"podIP"`
			StartTime         string `json:"startTime"`
			ContainerStatuses []struct {
				Name         string `json:"name"`
				Image        string `json:"image"`
				ImageID      string `json:"imageID"`
				Ready        bool   `json:"ready"`
				RestartCount int    `json:"restartCount"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

// ListServices 拉 Deployment/StatefulSet，按 ns 规则过滤，归并成服务快照。
//
// 🔴 **配了具体 ns 就逐个 ns 请求**，不要一次拉全集群。
//
// 不带 namespace 参数时 Kite 把它当成「所有命名空间」，
// 于是权限判定要求账号对 **namespace=All** 有权限 ——
// 而按最小权限配出来的只读账号通常只被授权了那十几个业务 ns，
// 报错是 `does not have permission to get deployments in namespace All`。
// 人会以为「权限配错了」，其实配的是对的，是我们要多了。
//
// 逐 ns 请求同时解决三件事：
//  1. 权限只需要那几个 ns，符合最小权限
//  2. 少拉一大堆无关 ns 的数据
//  3. 某个 ns 没权限时只丢那一个，其余照常（见下面的容错）
//
// ⚠️ ns 规则里有通配（app-*）时没法逐个请求 —— 通配要靠拿到全量再匹配，
// 那种情况只能回退到全量请求，也就仍然需要全局权限。
func (k *Kite) ListServices(ctx context.Context, cluster string, rules Rules, withRuntime bool) (*ListResult, error) {
	var rows []rawWorkload
	var excluded []ExcludedService
	exactNS, expanded := k.resolveNamespaces(ctx, cluster, rules)
	// 🔴 展开成功却零命中 → 明确报出来，不要回退整集群（见 rancher.go 同一条）
	if expanded && len(exactNS) == 0 {
		return nil, nsNoMatchErr(cluster, rules, k.candidateNamespaces(ctx, cluster))
	}

	for _, res := range []string{"deployments", "statefulsets"} {
		var data []byte
		var err error
		if len(exactNS) > 0 {
			data, err = k.fetchByNamespaces(ctx, res, cluster, exactNS)
		} else {
			data, err = k.do(ctx, clusterPath(cluster, res, ""))
		}
		if err != nil {
			// statefulsets 读不到不算致命（可能这个集群没有），deployments 读不到才是
			if res == "deployments" {
				return nil, err
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
				// 记下被排掉的服务名
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
		out.Pods = k.fillRuntime(ctx, cluster, rules, exactNS, out.Services)
	}
	return out, nil
}

// fillRuntime 补 RunningTag / Digest —— 需要额外打一次 pod 接口，所以做成可选。
//
// 拿不到不算失败：pod 接口挂了只是少了「发布中」这个判断，
// 版本对账本身仍然成立，不该因此让整次采集失败。
// ⚠️ nss 由调用方传入，**不要在这里重新解析一次** —— 展开通配要打
// /api/v1/namespaces，重解析就等于每次采集多打一轮无谓的请求。
func (k *Kite) fillRuntime(ctx context.Context, cluster string, rules Rules, nss []string, snaps []ServiceSnapshot) []PodInfo {
	// pods 同样逐 ns 拉 —— 理由与 ListServices 一致：
	// 不带 namespace 就是要 namespace=All 的权限，而只读账号通常没有。
	// ⚠️ 这里失败不返回错误（Pod 明细是附加信息，拿不到不该让整次采集失败），
	//    但上面 fetchByNamespaces 会留 WARN，不会静默。
	var data []byte
	var err error
	if len(nss) > 0 {
		data, err = k.fetchByNamespaces(ctx, "pods", cluster, nss)
	} else {
		data, err = k.do(ctx, clusterPath(cluster, "pods", ""))
		if err != nil {
			// 🔴 整集群拉不到时，用刚采到的服务所在的 ns 逐个再拉（与 rancher.go 同一条）。
			//    那些 ns 的权限一定有 —— 它们的 Deployment 刚刚才读到。
			if fromSnaps := namespacesOf(snaps); len(fromSnaps) > 0 {
				logx.Info("kite", "pods_fallback_by_ns", map[string]any{
					"cluster": cluster, "namespaces": len(fromSnaps),
					"note": "整集群 pods 被拒，改用刚采到的服务所在 ns 逐个拉"})
				data, err = k.fetchByNamespaces(ctx, "pods", cluster, fromSnaps)
			}
		}
	}
	if err != nil {
		// ⚠️ 必须记：不记的话导出页只写着「可能是 pod 接口读取失败」，
		//    到底是不是、为什么，日志里一个字都没有
		logx.Warn("kite", "pods_unavailable", map[string]any{
			"cluster": cluster, "err": err.Error(),
			"note": "拿不到 Pod 明细；版本比对不受影响"})
		return nil
	}
	var l podList
	if json.Unmarshal(data, &l) != nil {
		return nil
	}
	// service_key → 实际在跑的 tag / digest
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
			// imageID 形如 repo@sha256:xxx，digest 在这里
			idRef := imageref.Parse(cs.ImageID)
			ref.Digest = idRef.Digest
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
		if r, ok := running[snaps[i].ServiceKey]; ok {
			snaps[i].RunningTag = r.Tag
			if snaps[i].Digest == "" {
				snaps[i].Digest = r.Digest
			}
		}
	}
	return pods
}

// exactNamespaces 取出可以逐个请求的确切 ns 名。
//
// 有任何通配就返回 nil —— 通配必须拿到全量才能匹配，
// 逐个请求会漏掉规则本该覆盖的 ns（而且是静默漏，最难发现）。
func exactNamespaces(r NSRules) []string {
	if len(r.Include) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.Include))
	for _, n := range r.Include {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if strings.Contains(n, "*") {
			return nil // 有通配 → 只能全量拉
		}
		out = append(out, n)
	}
	return out
}

// fetchByNamespaces 逐个 ns 拉取并把结果拼成一个列表。
//
// ⚠️ 单个 ns 失败**不中断整体**：客户那边常有「授权了 10 个 ns，其中 1 个已经删了」
// 的情况，为一个不存在的 ns 让整次采集失败，代价远大于少那一个 ns 的数据。
// 但要留 WARN —— 否则「少了一个 ns 的服务」会被当成「对方下线了这些服务」。
func (k *Kite) fetchByNamespaces(ctx context.Context, res, cluster string, nss []string) ([]byte, error) {
	var chunks [][]byte
	var lastErr error
	for _, ns := range nss {
		// 🔴 ns 与集群**都是路径段**：`/api/v1/_clusters/<cluster>/deployments/<ns>`
		//
		// ⚠️ `?namespace=xxx` 这个写法**会被静默忽略** —— 实测返回的还是全部 20 个
		//    ns 的 56 条，HTTP 200，没有任何报错。我一度照着 Kite 前端 bundle 里
		//    的 `new URLSearchParams({namespace: ...})` 写成了 query 参数，
		//    编译过、请求通、数据也有 —— 只是过滤根本没发生。
		//    （Kite 源码 url2namespaceresource 取的是 parts[resourceIndex+1]，
		//     `?namespace=` 只对 /events/resources 一个端点生效。）
		path := clusterPath(cluster, res, ns)
		data, err := k.do(ctx, path)
		if err != nil {
			lastErr = err
			logx.Warn("kite", "namespace_failed", map[string]any{
				"cluster": cluster, "namespace": ns, "resource": res, "err": err.Error()})
			continue
		}
		chunks = append(chunks, data)
	}
	// 🔴 一个 ns 都没成功 = 这次采集什么都没拿到，必须报错。
	//    返回空列表的话，界面上会显示成「这个平台没有任何服务」——
	//    而那跟「对方真的下线了所有服务」长得一模一样。
	if len(chunks) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return mergeItems(chunks)
}

// forbiddenHint 按请求形态给出**能直接照做**的 403 说明。
//
// 分两种，因为该动的地方完全不同：
//   - 全量请求（路径里没有 ns）：是我们要多了 → 让用户把 ns 规则写成确切名字
//   - 逐 ns 请求：是角色确实缺这一个 ns → 让用户补这一条
//
// ⚠️ 别只说"没权限，去加权限" —— 第一种情况下照做会把角色放成 `namespaces: ['*']`，
//
//	等于为了让工具跑起来而放弃最小权限，而真正该改的是我们这边的采集范围。
func forbiddenHint(path string) string {
	cluster, res, ns := parseKitePath(path)
	onCluster := ""
	if cluster != "" {
		onCluster = "集群 " + cluster + " 上"
	}

	if ns == "" {
		// 走到这里说明通配**没能展开成确切 ns**（见 resolveNamespaces）：
		// 既读不到集群的 namespaces 列表，账号角色里声明的 ns 也是通配。
		// 这时只能整集群拉，而那需要全命名空间权限。
		return fmt.Sprintf(
			"Kite 拒绝了这次请求：读%s的 %s 需要**全命名空间**权限。"+
				"该环境的「命名空间」规则为空或含通配（如 app-*），而这次没能把通配展开成具体的 ns —— "+
				"通常是因为账号既不能列命名空间，其角色里写的也是通配。"+
				"两个办法：把规则改成确切的命名空间名字；或给账号加上 namespaces 的 get 权限，"+
				"之后通配就能自动展开，只需要那几个 ns 的权限。",
			onCluster, res)
	}
	return fmt.Sprintf(
		"Kite 拒绝了这次请求：账号没有%s命名空间 %s 的 %s get 权限。"+
			"去 Kite 的「角色」里给这个角色补上该集群 + 该命名空间，或把它从本环境的命名空间规则里去掉。"+
			"⚠️ 若 Kite 日志里的集群名与这里的对不上，那是 Kite 取到了默认集群 —— 请反馈这条信息。",
		onCluster, ns, res)
}

// parseKitePath 从 `/api/v1/_clusters/<cluster>/<resource>[/<namespace>]` 拆出三段。
//
// ⚠️ 必须跟 clusterPath 的格式保持一致 —— 之前集群走 `?cluster=`，
// 这里就从 query 取；改成路径段后没同步的话，报错里的集群名会变成空，
// 而"集群名对不对得上"正是这类问题唯一的线索。
func parseKitePath(path string) (cluster, res, ns string) {
	p := path
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(p, "/api/v1/"), "/"), "/")
	if len(parts) > 0 && parts[0] == "_clusters" {
		if len(parts) > 1 {
			cluster, _ = url.PathUnescape(parts[1])
		}
		parts = parts[2:]
	}
	if len(parts) > 0 {
		res = parts[0]
	}
	if len(parts) > 1 {
		ns, _ = url.PathUnescape(parts[1])
	}
	return cluster, res, ns
}

// resolveNamespaces 定出这次要逐个请求哪些 ns；返回 nil 表示只能整集群拉。
//
// 🔴 **通配也要尽量展开成确切名**，否则「配了一个 app-* 就退回全量」
// 会把同一组里确切写的那些（biz-uat）一起拖下水，
// 结果是明明按最小权限配好了角色，采集照样报「需要全命名空间权限」。
//
// 展开的候选集有两个来源，按可靠性排序：
//  1. 集群里**实际存在**的 ns（/api/v1/namespaces）—— 最准，但要 namespaces 读权限
//  2. 当前账号**被授权**的 ns（/api/auth/user 的 roles[].namespaces）—— 不需要额外权限，
//     而按最小权限配出来的角色，这里正好就是那十几个确切的业务 ns
//
// 两个都拿不到（或角色本身写的是 `*`）才回退整集群拉。
// resolveNamespaces 定出这次要逐个查询哪些 namespace。
//
// 🔴 第二个返回值 expanded 区分两件**处理方式完全相反**的事（与 rancher.go 同一条）：
//
//	expanded=false —— 读不到 ns 列表，无法展开通配 → 回退整集群拉
//	expanded=true  —— 展开成功，结果**可能是空的**
//
// 混成一路的话，「规则一个都没匹配上」会退回去拉整集群，
// 权限不够时报「权限不足」—— 而真实原因是规则写错了或对方没有那些 ns。
func (k *Kite) resolveNamespaces(ctx context.Context, cluster string, rules Rules) (ns []string, expanded bool) {
	if ex := exactNamespaces(rules.NS); len(ex) > 0 {
		return ex, true
	}
	if len(rules.NS.Include) == 0 {
		return nil, false // 没有 include 规则 = 要整个集群，逐 ns 无从谈起
	}

	cands := k.candidateNamespaces(ctx, cluster)
	if len(cands) == 0 {
		return nil, false
	}
	var out []string
	for _, n := range cands {
		if rules.NS.Match(n) {
			out = append(out, n)
		}
	}
	logx.Info("kite", "ns_expanded", map[string]any{
		"cluster": cluster, "include": rules.NS.Include,
		"candidates": len(cands), "matched": len(out),
		"sample": sampleNS(cands, 8)})
	return out, true
}

// candidateNamespaces 取一份可用来展开通配的 ns 候选集。
//
// ⚠️ 顺序不能颠倒：先问"集群里有哪些"，拿不到才问"我被授权了哪些"。
// 后者只是授权范围，可能含早已删掉的 ns —— 对逐 ns 请求无害
// （Kite 对不存在的 ns 返回 200 空列表），但它也可能**少于**实际存在的 ns
// （角色写 `*` 时就完全展不开），所以只能当兜底。
func (k *Kite) candidateNamespaces(ctx context.Context, cluster string) []string {
	// ① 集群里实际存在的 ns
	if data, err := k.doTry(ctx, clusterPath(cluster, "namespaces", "")); err == nil {
		var l struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"items"`
		}
		if json.Unmarshal(data, &l) == nil && len(l.Items) > 0 {
			out := make([]string, 0, len(l.Items))
			for _, it := range l.Items {
				out = append(out, it.Metadata.Name)
			}
			return out
		}
	}

	// ② 当前账号被授权的 ns
	data, err := k.doTry(ctx, "/api/auth/user")
	if err != nil {
		return nil
	}
	var me struct {
		User struct {
			Roles []struct {
				Clusters   []string `json:"clusters"`
				Namespaces []string `json:"namespaces"`
			} `json:"roles"`
		} `json:"user"`
	}
	if json.Unmarshal(data, &me) != nil {
		return nil
	}
	var out []string
	for _, r := range me.User.Roles {
		if !matchAny(r.Clusters, cluster) {
			continue // 这个角色管的不是本集群
		}
		for _, n := range r.Namespaces {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			// 🔴 角色本身写的是通配（`*` 或 `app-*`）时，这里展不出确切名。
			//    返回 nil 让上层回退全量 —— 但注意：角色若是 `app-*`，
			//    全量请求同样会被拒（Kite 拿 "All" 去做字面匹配）。
			//    那种情况只能让用户把规则写成确切 ns，403 提示里已经说了。
			if strings.Contains(n, "*") {
				return nil
			}
			out = append(out, n)
		}
	}
	return out
}

// matchAny 判断 cluster 是否落在角色的 clusters 列表里（支持 `*` 与通配）。
func matchAny(patterns []string, v string) bool {
	for _, p := range patterns {
		if matchPattern(strings.TrimSpace(p), v) {
			return true
		}
	}
	return false
}

// clusterPath 构造一次 Kite 请求的路径。
//
// 🔴 **目标集群必须走路径段 `_clusters/<name>`，不能用 `?cluster=` 查询参数。**
//
// 两种写法**业务层行为一样**（都能返回目标集群的数据），
// 但 Kite 的**鉴权中间件只认路径**：
//
//	parts := strings.Split(path, "/")
//	resourceIndex := 3
//	if parts[resourceIndex] == "_clusters" { resourceIndex += 2 }   // ← 集群在这里
//
// 用 `?cluster=` 时中间件取不到集群，会拿**默认集群**去比对角色的 clusters 列表。
// 于是按最小权限配的角色必然被拒，而报错里的集群名跟你请求的那个**对不上**：
//
//	请求  /api/v1/deployments/app-admin?cluster=uat-cluster-01
//	Kite  ... in namespace app-admin on cluster infra-k8s-cluster-01
//	                                            ^^^^^ 默认集群，不是我们要的
//
// ⚠️ 这个不一致是唯一的线索，而它**只出现在 Kite 返回的响应体里** ——
//
//	状态码只有 403，业务层看不出任何异常。当初把响应体打进日志，
//	正是为了这种"错在别处、表象却像权限没配够"的情况。
func clusterPath(cluster, resource, ns string) string {
	p := "/api/v1/_clusters/" + url.PathEscape(cluster) + "/" + resource
	if ns != "" {
		p += "/" + url.PathEscape(ns)
	}
	return p
}
