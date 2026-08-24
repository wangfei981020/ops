// Package providers 把「从某个组织拉服务版本」这件事抽象成一个接口。
//
// 我方走 Kite、客户走 Rancher、拿不到 API 的客户走手工导入 —— 它们是同一个接口的不同实现。
// 我方**不是特例分支**：如果给我方单独写一条 CMDB 直读的路径，代码里就会多一条
// 完全不同的分支，长期要维护两套。
//
// ⚠️ 接口必须把「认证头怎么带」也抽象掉，不能假设都是 Bearer：
// 实测 Kite 只认 Cookie（`Cookie: auth_token=<JWT>`），传 `Authorization: Bearer` 返回 401；
// 而 Rancher 用的就是 Bearer。这个差异如果等到写实现时才发现，接口就得返工。
package providers

import (
	"context"
	"errors"
	"strings"
	"time"

	"ops-version-backend/internal/imageref"
	"ops-version-backend/logx"
)

// 采集失败的分类。
//
// 🔴 这几种**必须分开**，不能统一成一个「失败」，更不能退化成「返回空列表」：
// 认证失败如果表现为空列表，界面会显示「该组织没有任何服务」，
// 而这跟「对方真的下线了所有服务」在表上长得一模一样 —— 看不出区别就等于没告警。
var (
	ErrAuth        = errors.New("认证失败")  // 密码错 / token 过期 → last_sync_status=auth_failed
	ErrUnreachable = errors.New("网络不可达") // DNS/超时/拒绝连接 → unreachable
	ErrForbidden   = errors.New("权限不足")  // 认证过了但读不到资源 → forbidden
)

// ServiceSnapshot 一个服务在某组织某环境的当前状态。
type ServiceSnapshot struct {
	// ServiceKey 对账 key = 镜像名最后一段。见 internal/imageref 的说明
	ServiceKey string
	ImageRepo  string // 完整仓库路径，仅展示

	// Tag 来自 deployment.spec —— 声明要跑什么
	Tag string
	// RunningTag 来自 pod 的 containerStatuses[].imageID —— 实际在跑什么。
	// 与 Tag 不一致 = 正在滚动更新，或滚动卡住了。
	// 只看 Tag 会把「YAML 改了但一个 pod 都没起来」显示成「已升级」——会骗人的绿灯。
	RunningTag string
	Digest     string

	Namespace   string
	Workloads   []string // 共用这个镜像的 workload 名（一个镜像可能被多个 Deployment 用）
	BuildNo     *int
	IsVersioned bool

	// Conflicts 非空 = 同名冲突：同一个镜像名在多个 workload 上跑着**不同的版本**。
	// 通常是 ns 规则误抓（app-* 把 app-uat 和 app-prod 一起抓进来了）。
	// 🔴 此时必须拒绝判定 —— 静默取第一个的话，你看到的可能是 uat 的版本却以为是 prod 的，
	//    整张表的结论全错，而且从界面上完全看不出来。
	Conflicts []ConflictItem
}

// PodInfo 一个 Pod 的运行时明细。
//
// 为什么要单独存而不是只留 workload 级的副本数：
// 排查「版本改了但没生效」时，真正要看的是**各副本各自的状态** ——
// 3 个副本里 2 个跑新版 1 个卡在旧版、某个节点上的副本一直 CrashLoop、
// 某个副本重启了 47 次，这些在 workload 级的一个数字里全看不见。
//
// ⚠️ 这些数据**本来就已经拉到了**：算 RunningTag 时打的就是 pod 接口，
// 只是过去只取了 containerStatuses 的 image/imageID，其余字段全丢掉了。
// 所以多存这一份不增加任何一次网络请求。
type PodInfo struct {
	Namespace  string `json:"namespace"`
	PodName    string `json:"pod_name"`
	Container  string `json:"container"`
	ServiceKey string `json:"service_key"`
	ImageRepo  string `json:"image_repo"`
	Tag        string `json:"tag"`
	// Phase 是 Pod 级状态（Running/Pending/Failed…）
	Phase string `json:"phase"`
	// Ready 是**容器级**就绪。
	// 🔴 与 Phase 严格分开：Phase=Running 但 Ready=false 是最常见的故障态
	//    （探针一直不过），只看 Phase 会把它显示成健康。
	Ready    bool   `json:"ready"`
	Restarts int    `json:"restarts"`
	PodIP    string `json:"pod_ip"`
	Node     string `json:"node"`
	// StartedAt 用来算 Age。存时刻而不是存「跑了多久」——
	// 后者一落库就开始骗人，导出时算出来的是采集那一刻的年龄。
	StartedAt time.Time `json:"started_at"`
}

// podInfoOf 把 k8s 的 pod 字段拼成 PodInfo。Kite 与 Rancher 共用 ——
// 两边打的都是标准 k8s API，字段一模一样，各写一份必然漂移。
func podInfoOf(ns, pod, node, phase, ip, startTime, container string,
	ready bool, restarts int, ref imageref.Ref,
) PodInfo {
	// 解析不出时间就留零值。零值在导出时显示为「—」，
	// 不能拿 time.Now() 兜底 —— 那会让一个刚采到的 Pod 显示成「刚启动」
	var started time.Time
	if startTime != "" {
		if t, err := time.Parse(time.RFC3339, startTime); err == nil {
			started = t
		}
	}
	return PodInfo{
		Namespace: ns, PodName: pod, Container: container,
		ServiceKey: ref.Name, ImageRepo: ref.Raw, Tag: ref.Tag,
		Phase: phase, Ready: ready, Restarts: restarts,
		PodIP: ip, Node: node, StartedAt: started,
	}
}

// ConflictItem 冲突明细，要能让人一眼看出是哪几个 workload 打架。
type ConflictItem struct {
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	Tag       string `json:"tag"`
}

// Provider 一个组织的数据来源。
type Provider interface {
	// Type 返回 kite / rancher / argocd / kubeconfig / manual_import
	Type() string

	// Probe 测试连通性。返回的错误必须是上面三种之一（可 wrap），
	// 好让「测试连通」按钮能告诉用户到底是密码错了、网络不通、还是权限不够 ——
	// 这三种的处理方式完全不同，混成一个「连接失败」等于没说。
	Probe(ctx context.Context) error

	// ListServices 拉取指定集群下、符合 ns 规则的服务版本。
	// withRuntime=true 时额外打一次 pod 接口，取 RunningTag/Digest **以及 Pod 明细**。
	//
	// ⚠️ 返回结构体而不是多个返回值：以后再加东西（比如 Event、HPA 状态）
	// 不用把每个实现和调用方的签名再改一遍。
	ListServices(ctx context.Context, clusterRef string, rules Rules, withRuntime bool) (*ListResult, error)
}

// ListResult 一次采集的产出。
//
// Pods 与 Services 来自**同一次** pod 请求 —— 之所以放在一起返回，
// 是因为它们本来就是一次网络往返里的东西，拆成两个方法会白白多打一次接口。
type ListResult struct {
	Services []ServiceSnapshot
	// Pods 仅当 withRuntime=true 时有值。pod 接口失败时为 nil，
	// 但**不影响 Services** —— 少了「发布中」判断和明细，版本对账本身仍然成立。
	Pods []PodInfo
}

// NSRules 命名空间匹配规则。
//
// 每个组织每个环境各配各的 —— 客户那边 ns 怎么划分我们控制不了，
// 也不需要跟我方一致（对账 key 是镜像名，跟 ns 无关）。
type NSRules struct {
	// Include 支持 * 前缀匹配（app-*）。为空 = 全部 ns
	Include []string
	// Exclude 优先级高于 Include。
	// ⚠️ 必须有它：写 app-* 会把 app-uat 和 app-prod 一起抓进来，
	//    同一个镜像名命中两个环境的 workload → 同名冲突。
	Exclude []string
}

// Rules 一次采集的全部过滤规则。
//
// 合成一个结构体而不是多传一个参数：以后再加过滤维度（按标签、按镜像 registry）
// 不用把每个实现和调用方的签名再改一遍。
type Rules struct {
	NS       NSRules
	Workload WorkloadRules
}

// WorkloadRules 工作负载级过滤，与 NSRules 同一层但更细。
//
// 🔴 为什么需要它：ns 规则太粗。一个 ns 里有几十个服务，
// 而各家部署的服务集合并不相同 —— 我方 UAT 有的，对方可能压根没有。
// 只按 ns 抄的话，对账表里会多出一堆「对方没有」的噪音行，
// 把真正要看的差异淹掉。
//
// ⚠️ 匹配的是 **workload 名**（Deployment/StatefulSet 的名字）。
// 在我们的环境里它与服务名一致，但那是命名约定不是保证 ——
// 所以这只用于「抄什么回来」，**对齐仍然靠镜像名最后一段**。
type WorkloadRules struct {
	Include []string // 支持 * 前缀。为空 = 该 ns 下全部
	Exclude []string // 优先级高于 Include
}

// Match 判断一个 workload 是否参与采集。为空规则时一律通过。
func (r WorkloadRules) Match(name string) bool {
	for _, p := range r.Exclude {
		if matchPattern(strings.TrimSpace(p), name) {
			return false
		}
	}
	if len(r.Include) == 0 {
		return true
	}
	for _, p := range r.Include {
		if matchPattern(strings.TrimSpace(p), name) {
			return true
		}
	}
	return false
}

// Match 判断一个 namespace 是否参与采集。
func (r NSRules) Match(ns string) bool {
	ok := r.match(ns)
	logNSDecision(r, ns, ok)
	return ok
}

func (r NSRules) match(ns string) bool {
	for _, p := range r.Exclude {
		if matchPattern(strings.TrimSpace(p), ns) {
			return false
		}
	}
	if len(r.Include) == 0 {
		return true
	}
	for _, p := range r.Include {
		if matchPattern(strings.TrimSpace(p), ns) {
			return true
		}
	}
	return false
}

// logNSDecision 记录一次 ns 判定的输入与依据。量大，只在 debug 开。
func logNSDecision(r NSRules, ns string, matched bool) {
	if !logx.Enabled(logx.LevelDebug) {
		return
	}
	logx.Debug("provider", "ns_match", map[string]any{
		"namespace": ns, "matched": matched,
		"include": r.Include, "exclude": r.Exclude})
}

// matchPattern 只支持结尾一个 *，不做完整 glob。
// 刻意不支持复杂通配：ns 规则是给人配的，规则越复杂越容易配错，
// 而配错的表现是「某个服务从对账里凭空消失」——比报错更难发现。
// matchPattern 支持三种写法：
//
//	app-*      前缀匹配
//	*-canary   后缀匹配
//	*mid*      包含匹配
//	其余       全等
//
// 🔴 后缀与包含是后加的。原来只支持 `app-*`，而 `*-canary` 这种写法
// 又太自然了 —— 人写下去、保存成功、界面上看着规则就在那儿，**它却从不生效**。
// 配置类的东西最怕这个：没有任何反馈说明它没生效。
// MatchPattern 导出版，给包外用（采集器过滤复制规则名）。
// 🔴 必须共用同一个实现：三个输入框（ns / workload / 复制规则）
// 写同样的通配符必须得到同样的结果，各写一份必然漂移。
func MatchPattern(pat, s string) bool { return matchPattern(pat, s) }

func matchPattern(pat, s string) bool {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return false
	}
	if pat == "*" {
		return true
	}
	pre := strings.HasPrefix(pat, "*")
	suf := strings.HasSuffix(pat, "*")
	switch {
	case pre && suf:
		return strings.Contains(s, strings.Trim(pat, "*"))
	case suf:
		return strings.HasPrefix(s, strings.TrimSuffix(pat, "*"))
	case pre:
		return strings.HasSuffix(s, strings.TrimPrefix(pat, "*"))
	default:
		return pat == s
	}
}

// buildSnapshots 把「一堆 (ns, workload, image) 三元组」归并成按 service_key 聚合的快照。
//
// 归并是必要的：一个镜像可能被多个 Deployment 共用
// （真组织子：go-archive-server-backend 被 api/consumer/worker 三个 Deployment 用）。
// 用 workload 名当 key 会把它拆成三个「服务」各自对账；
// 按镜像名归并才是一行，**而且哪天 worker 忘了升级，立刻表现为同名冲突被抓出来**。
func buildSnapshots(rows []rawWorkload, allow []string) []ServiceSnapshot {
	byKey := map[string]*ServiceSnapshot{}
	order := []string{}

	for _, w := range rows {
		ref := imageref.Parse(w.Image)
		if ref.Name == "" || !ref.AllowedBy(allow) {
			continue
		}
		key := ref.Name
		s, ok := byKey[key]
		if !ok {
			s = &ServiceSnapshot{
				ServiceKey:  key,
				ImageRepo:   ref.RepoPath(),
				Tag:         ref.Tag,
				Digest:      ref.Digest,
				Namespace:   w.Namespace,
				BuildNo:     ref.BuildNo,
				IsVersioned: ref.IsVersioned,
			}
			byKey[key] = s
			order = append(order, key)
		}
		s.Workloads = append(s.Workloads, w.Name)

		// 同名不同版本 → 冲突。第一次发现时把已记录的那个也补进冲突列表，
		// 否则明细里只有第二个，看不出跟谁打架。
		if s.Tag != ref.Tag {
			if len(s.Conflicts) == 0 {
				s.Conflicts = append(s.Conflicts, ConflictItem{
					Namespace: s.Namespace, Workload: s.Workloads[0], Tag: s.Tag,
				})
			}
			s.Conflicts = append(s.Conflicts, ConflictItem{
				Namespace: w.Namespace, Workload: w.Name, Tag: ref.Tag,
			})
		}
	}

	out := make([]ServiceSnapshot, 0, len(order))
	conflicts := 0
	for _, k := range order {
		if len(byKey[k].Conflicts) > 0 {
			conflicts++
		}
		out = append(out, *byKey[k])
	}
	// 🔴 「输入 + 依据 + 结论」三样都打：多少个原始 workload 归并成了多少个服务、
	//    多少个被 registry 白名单挡掉、多少个同名冲突。
	//    只打结论的话，服务数不对时还得重新加日志才能查。
	logx.Debug("provider", "build_snapshots", map[string]any{
		"raw_workloads": len(rows), "services": len(out),
		"conflicts": conflicts, "registry_allow": allow})
	return out
}

// rawWorkload 是 provider 实现从各自 API 解析出来的中间形态。
type rawWorkload struct {
	Namespace string
	Name      string
	Kind      string
	Image     string
}
