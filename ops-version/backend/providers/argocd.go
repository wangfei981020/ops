package providers

import (
	"bytes"
	"context"
	"crypto/tls"
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

// ArgoCD 从 ArgoCD 读各应用实际跑的镜像版本。
//
// 存在的理由：有些客户只给 ArgoCD 账号，不给 Rancher / Kite。
//
// 🔴 **它与 Kite/Rancher 看到的东西不完全是一回事**：
// ArgoCD 只知道**它自己管的**那些应用。客户手工 kubectl 部署的、
// 或由别的流水线管的服务，在这里根本不会出现 —— 表现是"对方少了几个服务"，
// 而实际上人家跑得好好的。配置页必须把这个差异说清楚。
//
// 实测要点（2026-08-19 对本地 ArgoCD 验证）：
//   - 登录：POST /api/v1/session {username,password} → {"token":"..."}
//     也可以直接用账号 token（客户通常给这个），走 Authorization: Bearer
//   - 列表：GET /api/v1/applications → items[].spec.destination.namespace 可用来按 ns 过滤
//   - 🔴 **镜像不能取 status.summary.images** —— 那是**运行中 Pod** 的汇总，
//     副本缩到 0 的服务这里是空的，会被误判成"没部署"。
//     实测 app-atmosphere-client-backend（replicas=0）summary 为空，
//     而它明明定义着 nginx:1.27。
//   - 正解：GET /api/v1/applications/{name}/managed-resources
//     从 **liveState**（集群里实际的对象）解析出容器镜像；replicas=0 也读得到。
//   - ⚠️ 用 liveState 不用 targetState：后者是 git 里定义的"应该是什么"，
//     而对账要回答的是"实际跑的是什么"。两者不一致恰恰是 OutOfSync 的情形，
//     取错了会把"还没同步过去"显示成"已经是这个版本"。
type ArgoCD struct {
	Endpoint    string // 如 https://argocd.example.com
	AuthType    string // token | password
	Username    string
	Password    string
	APIKey      string // 账号 token
	InsecureTLS bool

	HTTP *http.Client

	mu    sync.Mutex
	token string
}

func (a *ArgoCD) Type() string { return "argocd" }

func (a *ArgoCD) client() *http.Client {
	if a.HTTP != nil {
		return readOnlyClient(a.HTTP)
	}
	c := &http.Client{Timeout: 60 * time.Second}
	if a.InsecureTLS {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	// 🔴 只读闸门包在最外层，理由同 rancher.go
	return readOnlyClient(c)
}

// ensureToken 取一个可用 token。
//
// token 模式直接用配置里的；password 模式登录换一个并缓存。
// ⚠️ ArgoCD 的 session token 默认 24h，这里不解析有效期 ——
// 过期时接口返回 401，do() 会清掉缓存下次重登，比自己算有效期可靠。
func (a *ArgoCD) ensureToken(ctx context.Context) (string, error) {
	if a.AuthType == "token" || a.AuthType == "api_key" {
		if strings.TrimSpace(a.APIKey) == "" {
			return "", fmt.Errorf("%w: 没有配置 token", ErrAuth)
		}
		return a.APIKey, nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" {
		return a.token, nil
	}

	body, _ := json.Marshal(map[string]string{"username": a.Username, "password": a.Password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(a.Endpoint, "/")+"/api/v1/session", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	if resp.StatusCode >= 300 {
		sb := SafeBody(raw, 400)
		logx.Warn("argocd", "login_failed", map[string]any{
			"endpoint": a.Endpoint, "user": a.Username, "status": resp.StatusCode, "body": sb})
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusBadRequest:
			return "", fmt.Errorf("%w: ArgoCD 拒绝了用户名或密码 (HTTP %d)：%s", ErrAuth, resp.StatusCode, sb)
		case http.StatusForbidden:
			return "", fmt.Errorf("%w: 账号无权限 (HTTP %d)：%s", ErrForbidden, resp.StatusCode, sb)
		default:
			return "", fmt.Errorf("%w: 登录返回 HTTP %d：%s", ErrUnreachable, resp.StatusCode, sb)
		}
	}

	var out struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Token == "" {
		// 2xx 却没拿到 token —— 必须显式报错，不能返回空让后续以 401 的面目出现
		return "", fmt.Errorf("%w: 登录返回 %d 但响应里没有 token", ErrAuth, resp.StatusCode)
	}
	a.token = out.Token
	logx.Debug("argocd", "login", map[string]any{"endpoint": a.Endpoint, "user": a.Username})
	return a.token, nil
}

func (a *ArgoCD) do(ctx context.Context, path string) ([]byte, error) {
	tok, err := a.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(a.Endpoint, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	started := time.Now()
	resp, err := a.client().Do(req)
	if err != nil {
		logx.Debug("argocd", "request_fail", map[string]any{
			"path": path, "endpoint": a.Endpoint, "err": err.Error()})
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	logx.Debug("argocd", "request", map[string]any{
		"path": path, "endpoint": a.Endpoint, "status": resp.StatusCode,
		"bytes": len(data), "ms": time.Since(started).Milliseconds()})

	if resp.StatusCode >= 300 {
		body := SafeBody(data, 600)
		logx.Warn("argocd", "request_failed", map[string]any{
			"path": path, "endpoint": a.Endpoint, "status": resp.StatusCode,
			"auth_type": a.AuthType, "body": body})
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			a.mu.Lock()
			a.token = "" // 让下次重登
			a.mu.Unlock()
			return nil, fmt.Errorf("%w: ArgoCD 拒绝了这个凭据（认证方式 %s）", ErrAuth, a.AuthType)
		case http.StatusForbidden:
			return nil, fmt.Errorf("%w: 这个账号在 ArgoCD 上没有读应用的权限 —— "+
				"需要对目标 project 的 applications 有 get 权限", ErrForbidden)
		default:
			return nil, fmt.Errorf("%w: ArgoCD 返回 HTTP %d，详情见服务端日志", ErrUnreachable, resp.StatusCode)
		}
	}
	return data, nil
}

func (a *ArgoCD) Probe(ctx context.Context) error {
	_, err := a.do(ctx, "/api/v1/applications?fields=items.metadata.name")
	return err
}

// argoApp 应用列表里我们要的字段。
type argoApp struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Destination struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
			Server    string `json:"server"`
		} `json:"destination"`
	} `json:"spec"`
	Status struct {
		Sync struct {
			Status string `json:"status"`
		} `json:"sync"`
		Health struct {
			Status string `json:"status"`
		} `json:"health"`
	} `json:"status"`
}

// Clusters 列出 ArgoCD 里注册的目标集群，供「配置平台」时下拉选。
//
// ⚠️ ArgoCD 的"集群"是 destination（server URL 或 name），
// 与 Kite/Rancher 的集群名不是同一套命名 —— 填错了会一个应用都匹配不上，
// 所以这里必须能拉出真实值给人选，而不是让人手打。
func (a *ArgoCD) Clusters(ctx context.Context) ([]string, error) {
	data, err := a.do(ctx, "/api/v1/clusters")
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []struct {
			Name   string `json:"name"`
			Server string `json:"server"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("解析 clusters 失败: %w", err)
	}
	names := make([]string, 0, len(out.Items))
	for _, c := range out.Items {
		// name 可能为空（只注册了 server URL），退回用 server
		if n := strings.TrimSpace(c.Name); n != "" {
			names = append(names, n)
			continue
		}
		names = append(names, c.Server)
	}
	return names, nil
}

// ListServices 拉 ArgoCD 管理的应用，按 ns 规则过滤，解析出各服务的镜像版本。
//
// clusterRef 是 ArgoCD 的 destination（name 或 server URL）；留空表示不限集群
// —— 单集群的 ArgoCD 很常见，强制填集群只会让人填错。
func (a *ArgoCD) ListServices(ctx context.Context, clusterRef string, rules Rules, withRuntime bool) (*ListResult, error) {
	data, err := a.do(ctx, "/api/v1/applications")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []argoApp `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("解析 applications 失败: %w", err)
	}

	// 先按 ns 与集群过滤，再去逐个打 managed-resources ——
	// 应用可能有几百个，不过滤就是几百次请求。
	var picked []argoApp
	for _, app := range list.Items {
		if !matchesDestination(clusterRef, app) {
			continue
		}
		if !rules.NS.Match(app.Spec.Destination.Namespace) {
			continue
		}
		picked = append(picked, app)
	}
	logx.Debug("argocd", "apps_picked", map[string]any{
		"cluster": clusterRef, "total": len(list.Items), "picked": len(picked)})

	rows, excluded := a.collectWorkloads(ctx, picked, rules)
	return &ListResult{Services: buildSnapshots(rows, rules.Workload.Include), Excluded: excluded}, nil
}

// matchesDestination 判断应用是否落在目标集群上。
//
// ⚠️ clusterRef 留空 = 不限集群。ArgoCD 常见的部署形态是"一个 ArgoCD 管一个集群"，
// 那种情况下强制配集群名，填错了就是**零结果**，而零结果跟"对方没有服务"长得一样。
func matchesDestination(clusterRef string, app argoApp) bool {
	ref := strings.TrimSpace(clusterRef)
	if ref == "" {
		return true
	}
	d := app.Spec.Destination
	return ref == d.Name || ref == d.Server
}

// collectWorkloads 并发拉每个应用的 managed-resources，解析出工作负载镜像。
//
// ⚠️ 并发要限流：ArgoCD 的 managed-resources 会去 apiserver 取实时状态，
// 几百个应用同时打会把对方的 ArgoCD 拖慢 —— 我们是来读数据的，不该影响人家发布。
func (a *ArgoCD) collectWorkloads(ctx context.Context, apps []argoApp, rules Rules) ([]rawWorkload, []ExcludedService) {
	const concurrency = 6
	sem := make(chan struct{}, concurrency)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var rows []rawWorkload
	var excluded []ExcludedService

	for _, app := range apps {
		wg.Add(1)
		go func(app argoApp) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			got, gotExcluded, err := a.appWorkloads(ctx, app, rules)
			if err != nil {
				// 单个应用失败不中断整体 —— 但要留 WARN，
				// 否则"少了几个服务"会被当成"对方下线了"
				logx.Warn("argocd", "app_failed", map[string]any{
					"app": app.Metadata.Name, "namespace": app.Spec.Destination.Namespace,
					"err": err.Error()})
				return
			}
			mu.Lock()
			rows = append(rows, got...)
			excluded = append(excluded, gotExcluded...)
			mu.Unlock()
		}(app)
	}
	wg.Wait()
	return rows, excluded
}

// managedResource managed-resources 的一条。
//
// liveState / targetState 是**JSON 字符串**（不是对象），要二次解析。
type managedResource struct {
	Kind        string `json:"kind"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	LiveState   string `json:"liveState"`
	TargetState string `json:"targetState"`
}

func (a *ArgoCD) appWorkloads(ctx context.Context, app argoApp, rules Rules) ([]rawWorkload, []ExcludedService, error) {
	data, err := a.do(ctx,
		"/api/v1/applications/"+url.PathEscape(app.Metadata.Name)+"/managed-resources")
	if err != nil {
		return nil, nil, err
	}
	var mr struct {
		Items []managedResource `json:"items"`
	}
	if err := json.Unmarshal(data, &mr); err != nil {
		return nil, nil, fmt.Errorf("解析 managed-resources 失败: %w", err)
	}

	var rows []rawWorkload
	var excluded []ExcludedService
	for _, it := range mr.Items {
		kind := strings.ToLower(it.Kind)
		if kind != "deployment" && kind != "statefulset" && kind != "daemonset" {
			continue
		}
		ns := it.Namespace
		if ns == "" {
			ns = app.Spec.Destination.Namespace
		}
		if !rules.NS.Match(ns) {
			continue
		}
		if !rules.Workload.Match(it.Name) {
			// 记下被排掉的服务名
			for _, img := range containerImages(it.LiveState) {
				if ref := imageref.Parse(img); ref.Name != "" {
					excluded = append(excluded, ExcludedService{
						ServiceKey: ref.Name, Workload: it.Name, Namespace: ns,
					})
				}
			}
			continue
		}
		// 🔴 只看 liveState：集群里实际的对象。
		//    targetState 是 git 里定义的"应该是什么"，
		//    OutOfSync 时两者不同，取错了会把"还没同步过去"报成"已经是这个版本"。
		for _, img := range containerImages(it.LiveState) {
			rows = append(rows, rawWorkload{
				Namespace: ns, Name: it.Name, Kind: kind + "s", Image: img,
			})
		}
	}
	return rows, excluded, nil
}

// containerImages 从一个 k8s 对象的 JSON 里取出所有容器镜像。
//
// ⚠️ liveState 为空是正常的：资源还没同步到集群时就是空的。
// 这时**不要**回退到 targetState —— 那会把"还没部署"显示成"已经在跑"。
func containerImages(state string) []string {
	if strings.TrimSpace(state) == "" {
		return nil
	}
	var o struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if json.Unmarshal([]byte(state), &o) != nil {
		return nil
	}
	out := make([]string, 0, len(o.Spec.Template.Spec.Containers))
	for _, c := range o.Spec.Template.Spec.Containers {
		if c.Image != "" {
			out = append(out, c.Image)
		}
	}
	return out
}
