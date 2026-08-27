package providers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"ops-version-backend/internal/imageref"
	"ops-version-backend/logx"
)

// Harbor 读复制（replication）状态。
//
// 🔴 只连**我们自己的** Harbor 就够了 —— replication 是我们往外推的，
// 执行记录全在我们这边，不需要对方的凭据。
//
// 三层数据：
//
//	/api/v2.0/replication/policies          有哪些规则、推给谁
//	/api/v2.0/replication/executions        每次执行的时间、成败、成功/失败数
//	/api/v2.0/replication/executions/{id}/tasks   **每个镜像**的明细 ← 归因靠这一层
type Harbor struct {
	Endpoint    string
	Username    string
	Password    string
	InsecureTLS bool
	HTTP        *http.Client
}

// client Harbor 的 HTTP 客户端。
//
// 🔴 也套只读闸门。
//
//	readonly.go 原来把 Harbor 排除在外，理由是「镜像同步是有意为之的写」。
//	但实际上我们**从来没有主动推过镜像** —— 推送走的是 Harbor 自己的复制策略，
//	我们只读它的执行记录。于是这个例外保护的是一件不存在的事，
//	代价却是 Harbor 这条链路上**没有任何结构性保证**：
//	谁哪天加一个 POST（删仓库、删 artifact、触发复制），不会有东西拦他。
//
//	而 Harbor 上放着的是全部镜像 —— 这是整个系统里最不该失手的地方。
//
// ⚠️ 将来真要主动推镜像，**不要**把这里改回裸客户端：
//
//	去 readonly.go 的白名单里精确登记那一条路径，让它成为一条写在明处的例外。
//	"有例外的安全规则"和"没有规则"之间的差别，就在于例外是不是逐条写下来的。
func (h *Harbor) client() *http.Client {
	if h.HTTP != nil {
		return readOnlyClient(h.HTTP)
	}
	c := &http.Client{Timeout: 60 * time.Second}
	if h.InsecureTLS {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return readOnlyClient(c)
}

func (h *Harbor) get(ctx context.Context, path string, v any) error {
	data, err := h.raw(ctx, path)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	return nil
}

// raw 发一次 GET 并返回原始响应体。
// 日志接口返回的是纯文本不是 JSON，所以取原始字节这一层要能单独用。
func (h *Harbor) raw(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(h.Endpoint, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(h.Username, h.Password)

	started := time.Now()
	resp, err := h.client().Do(req)
	if err != nil {
		logx.Debug("harbor", "request_fail", map[string]any{
			"path": path, "endpoint": h.Endpoint, "err": err.Error()})
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	logx.Debug("harbor", "request", map[string]any{
		"path": path, "endpoint": h.Endpoint, "status": resp.StatusCode,
		"bytes": len(data), "ms": time.Since(started).Milliseconds()})

	// 🔴 非 2xx 时把响应体也打出来 —— 原因全在体里。
	//    只打状态码的话，403 只能说明「没权限」，而人要知道的是「缺哪个权限」，
	//    于是每次都得去问对方要账号信息或者干脆猜。
	if resp.StatusCode >= 300 {
		body := SafeBody(data, 600)
		logx.Warn("harbor", "request_failed", map[string]any{
			"path": path, "endpoint": h.Endpoint, "status": resp.StatusCode,
			"user": h.Username, "body": body})
		// 🔴 给人看的话**不带原文** —— 原文已经进日志了（上面那条 Warn）。
		//    把几百字的 JSON 拼进错误信息，界面上就是一长条谁也不看，
		//    真正该说的「怎么办」反而被淹掉。分工是：
		//      界面 = 大概是什么问题 + 下一步做什么
		//      日志 = 完整原文，排查时去翻
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, fmt.Errorf("%w: Harbor 拒绝了这个账号或密码，检查凭据是否过期", ErrAuth)
		case resp.StatusCode == http.StatusForbidden:
			if hint := harborForbiddenHint(path); hint != "" {
				return nil, fmt.Errorf("%w: %s", ErrForbidden, hint)
			}
			return nil, fmt.Errorf("%w: 这个账号没有读该资源的权限，去 Harbor 给它补上", ErrForbidden)
		default:
			return nil, fmt.Errorf("%w: Harbor 返回 HTTP %d，详情见服务端日志", ErrUnreachable, resp.StatusCode)
		}
	}
	return data, nil
}

// Probe 测连通 —— 回答"地址对不对、凭据有没有效"。
//
// 🔴 **必须打一个要求认证的接口**，`/api/v2.0/projects` 不行。
//
// 实测（本地 Harbor v2.14）用**完全错误**的凭据打这些接口：
//
//	/api/v2.0/projects                       → 200 ← 匿名开放，只返回公开项目
//	/api/v2.0/projects/{n}/repositories      → 200 ← 同上
//	/api/v2.0/statistics                     → 401 ✓
//	/api/v2.0/users/current                  → 401（但 robot 拿到 412，语义怪）
//
// 我一度把探针从 replication 改成 /projects —— 那会让探针**永远成功**：
// 密码打错、robot 过期，界面照样显示"连接正常"，
// 直到几天后发现一条数据都没同步才回头查。
// ⚠️ 假服务端测不出这个：它按我写的规则返回，而真实 Harbor 有匿名读这条规矩。
//
// 也不能用 replication 当探针 —— 那是**系统级**接口，
// 一个完全可用的项目级 robot 会被判成连不上（见 Capabilities）。
func (h *Harbor) Probe(ctx context.Context) error {
	var x any
	return h.get(ctx, "/api/v2.0/statistics", &x)
}

// HarborCaps 这份凭据实际能读到什么。
//
// 存在的理由：Harbor 的权限是**分级**的，而我们的功能也是分级的 ——
// 「我方有哪些服务、哪些版本」只要项目级，
// 「推给对方成没成」才要系统级（replication）。
// 全有全无地判定，会让只需要前者的人被后者挡住。
type HarborCaps struct {
	// Projects 能读项目 / 仓库 / 制品 —— 「按服务查版本」靠这个
	Projects bool `json:"projects"`
	// Replication 能读复制**规则**（/replication/policies）——「同步状态」靠这个
	//
	// 🔴 与 ReplicationExec 分开：Harbor 把复制拆成两个系统级资源，
	//    机器人账户界面上那一项「镜像复制」只给执行、不给规则。
	//    合成一个字段的话，「差一个资源」和「整套权限都没有」就分不出来了。
	Replication bool `json:"replication"`
	// ReplicationExec 能读复制**执行记录**（/replication/executions）
	ReplicationExec bool `json:"replication_exec"`
	// Detail 给人看的一句话，说明缺什么、要怎么补
	Detail string `json:"detail"`
}

// Capabilities 分别探两级权限，**不因为一级失败就放弃另一级**。
func (h *Harbor) Capabilities(ctx context.Context) HarborCaps {
	var caps HarborCaps
	var x any

	// 🔴 先验凭据本身。/projects 对匿名开放，凭据无效时它照样返回 200 ——
	//    不先验一次的话，一个坏凭据会被报成"可查版本，只是读不到复制记录"，
	//    把认证失败伪装成权限分级问题。
	if err := h.Probe(ctx); err != nil {
		caps.Detail = "凭据无效或连不上：先确认地址、账号、密码 —— " + err.Error()
		return caps
	}

	caps.Projects = h.get(ctx, "/api/v2.0/projects?page_size=1", &x) == nil
	caps.Replication = h.get(ctx, "/api/v2.0/replication/policies?page_size=1", &x) == nil
	// 🔴 规则和执行要**分开探**。
	//
	//    Harbor 把复制拆成两个系统级资源，而机器人账户界面上那一项「镜像复制」
	//    只给 `replication`（执行），不给 `replication-policy`（规则）。
	//    只探规则的话，一个"能读执行、读不了规则"的账号会被笼统报成
	//    「读不到复制记录」—— 而它其实差的只是一个资源，不是整套权限。
	caps.ReplicationExec = h.get(ctx, "/api/v2.0/replication/executions?page_size=1", &x) == nil

	switch {
	case caps.Projects && caps.Replication:
		caps.Detail = "全部可用：可查版本，也可查同步状态"
	case caps.Projects && caps.ReplicationExec:
		// ⚠️ 这就是「勾了「镜像复制」却还是不通」的那种账号，最容易卡住人
		caps.Detail = "可查版本，能读复制**执行记录**，但读不了复制**规则** —— " +
			"「同步状态」那一列仍会显示为未知。" +
			"Harbor 把复制拆成了两个系统级资源：`replication-policy`（规则）和 " +
			"`replication`（执行），而机器人账户里那一项「镜像复制」只给了后者。" +
			"⚠️ 不用再建一个 robot —— 给现在这个**补上 replication-policy 的读取/查询**即可。"
	case caps.Projects:
		caps.Detail = "可查版本，但读不到复制记录 —— 「同步状态」那一列会显示为未知。" +
			"需要一个**系统级** robot，并勾上 `replication-policy`（规则）与 " +
			"`replication`（执行）两个资源的读取/查询。" +
			"不需要这一列的话，现在这份凭据就够了。"
	case caps.Replication:
		caps.Detail = "能读复制记录，但读不到项目列表 —— 「按服务查版本」用不了"
	default:
		caps.Detail = "凭据有效，但既读不到项目也读不到复制记录 —— 检查这个账号的授权范围"
	}
	return caps
}

// SyncPolicy 一条复制规则。
type SyncPolicy struct {
	PolicyID     int64
	Name         string
	DestRegistry string
	// SrcProject 这条规则从哪个源项目复制（filters 里 `appA/**` 的 `appA`）。
	//
	// 🔴 webhook 事件靠它精确定位到规则：payload 里没有规则名，
	// 而多条规则常指向同一个目标 Harbor，只按目标地址匹配会张冠李戴。
	// 空 = 这条规则没设名称过滤（全量复制），那时只能退回按目标匹配。
	SrcProject  string
	TriggerType string
	Enabled     bool
}

func (h *Harbor) Policies(ctx context.Context) ([]SyncPolicy, error) {
	var raw []struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		DestReg struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"dest_registry"`
		Trigger struct {
			Type string `json:"type"`
		} `json:"trigger"`
		// 🔴 filters 里的 name 过滤是「源项目/仓库」，形如 `appA/**`。
		//    它是把 webhook 事件精确关联回规则的**唯一**可靠线索：
		//    payload 里没有规则名，而多条规则常指向同一个目标 Harbor
		//    （生产上 appA/bizB/monitoring 都推向 partner-harbor），
		//    只按目标地址匹配会张冠李戴。
		Filters []struct {
			Type  string `json:"type"`
			Value any    `json:"value"`
		} `json:"filters"`
	}
	if err := h.get(ctx, "/api/v2.0/replication/policies?page_size=100", &raw); err != nil {
		return nil, err
	}
	out := make([]SyncPolicy, 0, len(raw))
	for _, p := range raw {
		dest := p.DestReg.URL
		if dest == "" {
			dest = p.DestReg.Name
		}
		// 取 name 过滤里的**项目名**：`appA/**` → `appA`。
		// ⚠️ value 是 any：Harbor 对不同 filter 类型给的类型不一样
		//    （name 给字符串、label 给数组），断言失败就当没有，不能 panic。
		srcProject := ""
		for _, f := range p.Filters {
			if !strings.EqualFold(f.Type, "name") {
				continue
			}
			v, ok := f.Value.(string)
			if !ok {
				continue
			}
			if i := strings.Index(v, "/"); i > 0 {
				srcProject = v[:i]
			} else if !strings.ContainsAny(v, "*?") {
				srcProject = v
			}
			break
		}
		// 🔴 解析不出源项目要说出来：webhook 归因就靠它，
		//    空着的话事件收到了却匹配不到规则，表现是「同步了但没记录」——
		//    而那时只能看到 saved=0，看不出是哪一步没成。
		if srcProject == "" {
			rawFilters, _ := json.Marshal(p.Filters)
			logx.Warn("harbor", "policy_no_src_project", map[string]any{
				"policy": p.Name, "filters": truncate(string(rawFilters), 500),
				"note": "从 filters 里解析不出源项目，这条规则的 webhook 事件会匹配不上"})
		}
		out = append(out, SyncPolicy{PolicyID: p.ID, Name: p.Name, DestRegistry: dest,
			SrcProject:  srcProject,
			TriggerType: NormalizeTrigger(p.Trigger.Type), Enabled: p.Enabled})
	}
	logx.Info("harbor", "policies_parsed", map[string]any{
		"total": len(out), "with_src_project": func() int {
			n := 0
			for _, x := range out {
				if x.SrcProject != "" {
					n++
				}
			}
			return n
		}()})
	return out, nil
}

// SyncExecution 一次执行。
type SyncExecution struct {
	ExecID      int64
	TriggerType string
	Status      string
	Total       int
	Succeeded   int
	Failed      int
	StartedAt   time.Time
	EndedAt     time.Time
}

func (h *Harbor) Executions(ctx context.Context, policyID int64, pageSize int) ([]SyncExecution, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	var raw []struct {
		ID        int64  `json:"id"`
		Status    string `json:"status"`
		Trigger   string `json:"trigger"`
		Total     int    `json:"total"`
		Succeed   int    `json:"succeed"`
		Failed    int    `json:"failed"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
	}
	// ⚠️ 钳到 Harbor 的硬上限。超过会整个请求 422（不是截断），
	//    而 422 在这里的表现是「一条复制记录都没有」—— 见 HarborMaxPageSize 的说明。
	if pageSize > HarborMaxPageSize {
		pageSize = HarborMaxPageSize
	}
	path := fmt.Sprintf("/api/v2.0/replication/executions?policy_id=%d&page_size=%d", policyID, pageSize)
	if err := h.get(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := make([]SyncExecution, 0, len(raw))
	for _, e := range raw {
		out = append(out, SyncExecution{
			ExecID: e.ID, Status: e.Status, TriggerType: NormalizeTrigger(e.Trigger),
			Total: e.Total, Succeeded: e.Succeed, Failed: e.Failed,
			StartedAt: parseHarborTime(e.StartTime), EndedAt: parseHarborTime(e.EndTime),
		})
	}
	return out, nil
}

// SyncTask 单个镜像的同步结果。
type SyncTask struct {
	ServiceKey string
	Tag        string
	Status     string
	FinishedAt time.Time
	// TaskID 用于按需拉取失败日志。Harbor 的 task 列表里**没有错误原因**，
	// 要单独打 /tasks/{id}/log 才有
	TaskID int64
	// ErrMsg 失败原因。只对失败的任务拉取 —— 成功的任务日志没有价值，
	// 而一次执行可能有几百个 task，全拉会把 Harbor 打爆
	ErrMsg string
}

// HarborMaxPageSize Harbor 服务端对 page_size 的**硬上限**。
//
// 🔴 超过它不是被截断，而是整个请求 422 失败：
//
//	/api/v2.0/replication/executions/20722/tasks?page_size=200
//	→ 422 "page_size in query should be less than or equal to 100"
//
// 这一条曾让**所有**复制规则的任务明细一条都拉不回来，而表现是
// 界面说「这条规则还没有推过任何服务」—— 一句与事实相反的断言。
const HarborMaxPageSize = 100

// tasksPageLimit 翻页次数上限，纯防御：Harbor 若因为某种原因每页都返回满页，
// 没有上限就会转成死循环，把采集卡死在这里。
const tasksPageLimit = 50

func (h *Harbor) Tasks(ctx context.Context, execID int64) ([]SyncTask, error) {
	out := []SyncTask{}
	// 版本号是从哪个字段读到的 —— 按用户要求，日志里必须体现来源。
	// Harbor 换版本、换触发方式时字段会变，这个计数是唯一的早期信号。
	fromField := map[string]int{}
	defer func() {
		if len(fromField) > 0 {
			logx.Info("harbor", "task_version_source", map[string]any{
				"exec_id": execID, "by_field": fromField, "tasks": len(out)})
		}
	}()
	// ⚠️ 必须翻页，不能只把 200 改成 100 就算完：一次执行可能有几百个 task，
	//    只取第一页的话超出的部分会被**静默丢弃** —— 那比 422 更糟，
	//    因为归因会显示成"没推过去"而不是报错。
	for page := 1; page <= tasksPageLimit; page++ {
		var raw []struct {
			ID      int64  `json:"id"`
			Status  string `json:"status"`
			SrcRes  string `json:"src_resource"`
			DstRes  string `json:"dst_resource"`
			EndTime string `json:"end_time"`
			// 🔴 版本号不一定在 src/dst_resource 里。Harbor 不同版本、
			//    不同触发方式给的 task 结构不一样，实测这几个字段都可能出现。
			//    老的 harbor-replication 脚本正是靠一条五级 fallback 链才拿得到版本号。
			// ⚠️ 字段名以老脚本 extract_image_from_task 的读法为准：
			//    `repository` + `tag`（我一度按 name/name_tag 猜，是错的）。
			//    实测过这个对象多为 null，但 null 与「字段名写错」是两回事 ——
			//    后者会在对方哪天真的给了对象时静默取不到值。
			Resource *struct {
				Repository string `json:"repository"`
				Tag        string `json:"tag"`
			} `json:"resource"`
			NameTag string `json:"name_tag"`
			Name    string `json:"name"`
		}
		path := fmt.Sprintf("/api/v2.0/replication/executions/%d/tasks?page=%d&page_size=%d",
			execID, page, HarborMaxPageSize)
		// 🔴 拿原始字节先记一份再解析。
		//    这条链路为了「Harbor 到底返回了什么」返工过四次 ——
		//    `resource` 是不是 null、`name_tag` 里有没有版本号，
		//    只有原文能回答，而每猜错一次就要等下一次真实复制事件。
		//    只记第一页：足够看清结构，又不会把日志刷爆。
		bs, err := h.raw(ctx, path)
		if err != nil {
			return nil, err
		}
		if page == 1 {
			logx.Info("harbor", "tasks_raw", map[string]any{
				"exec_id": execID, "bytes": len(bs), "raw": truncate(string(bs), 3000)})
		}
		if err := json.Unmarshal(bs, &raw); err != nil {
			return nil, fmt.Errorf("解析任务明细失败: %w（原文前 500 字：%s）",
				err, truncate(string(bs), 500))
		}
		for _, t := range raw {
			// 🔴🔴 **取第一个解析得出版本号的字段，而不是第一个非空的字段。**
			//
			//    `dst_resource` 的值常常是 `bizB/xxx [1 item(s) in total]` ——
			//    非空、能解析出服务名、**但没有版本号**。
			//    原来写成 `name := t.DstRes; if name == "" { name = t.SrcRes }`，
			//    于是 dst_resource 一非空就被采用，后面真正带版本号的字段
			//    **永远轮不到**，结果是服务名对、版本号恒为空。
			//
			//    而版本号恰恰是这条链路存在的全部意义。
			cands := []struct{ field, val string }{}
			if t.Resource != nil {
				if t.Resource.Repository != "" && t.Resource.Tag != "" {
					cands = append(cands, struct{ field, val string }{
						"resource.repository+tag", t.Resource.Repository + ":" + t.Resource.Tag})
				}
				cands = append(cands, struct{ field, val string }{
					"resource.repository", t.Resource.Repository})
			}
			cands = append(cands,
				struct{ field, val string }{"dst_resource", t.DstRes},
				struct{ field, val string }{"src_resource", t.SrcRes},
				struct{ field, val string }{"name_tag", t.NameTag},
				struct{ field, val string }{"name", t.Name})

			var ref imageref.Ref
			var from string
			var bare imageref.Ref // 只有服务名没版本号的兜底
			var bareFrom string
			for _, c := range cands {
				if strings.TrimSpace(c.val) == "" {
					continue
				}
				r := imageref.Parse(CleanHarborName(c.val))
				if r.Name == "" {
					continue
				}
				if r.Tag != "" {
					ref, from = r, c.field
					break
				}
				if bare.Name == "" {
					bare, bareFrom = r, c.field
				}
			}
			if ref.Name == "" {
				ref, from = bare, bareFrom
			}
			if ref.Name == "" {
				continue
			}
			if ref.Tag == "" {
				// 所有候选字段都没有版本号 —— 把整个 task 原文记下来。
				// 不记的话只能靠猜下一个字段，而每猜一轮要等一次真实复制事件。
				rawJSON, _ := json.Marshal(t)
				logx.Warn("harbor", "task_without_tag", map[string]any{
					"exec_id": execID, "service": ref.Name,
					"raw_task": string(rawJSON),
					"note":     "这个 task 的所有候选字段里都没有版本号，原文已记录"})
			}
			fromField[from]++
			out = append(out, SyncTask{ServiceKey: ref.Name, Tag: ref.Tag, TaskID: t.ID,
				Status: t.Status, FinishedAt: parseHarborTime(t.EndTime)})
		}
		if len(raw) < HarborMaxPageSize {
			return out, nil
		}
		if page == tasksPageLimit {
			// 撞上限要说出来。静默停在这里 = 归因数据不完整而没人知道
			logx.Warn("harbor", "tasks_page_limit", map[string]any{
				"exec": execID, "pages": page, "got": len(out),
				"note": "任务明细可能不完整，归因结果会偏保守",
			})
		}
	}
	return out, nil
}

// TaskLog 拉单个任务的日志，用于给失败任务补上原因。
//
// 🔴 只对**失败**的任务调。一次执行可能有几百个 task，
// 全量拉日志既慢又会把 Harbor 打爆，而成功任务的日志没有任何价值。
//
// 日志可能很长（含完整的推送过程），只保留尾部 —— 报错总在最后。
func (h *Harbor) TaskLog(ctx context.Context, execID, taskID int64, maxRunes int) string {
	path := fmt.Sprintf("/api/v2.0/replication/executions/%d/tasks/%d/log", execID, taskID)
	body, err := h.raw(ctx, path)
	if err != nil {
		// 拿不到日志不算失败：任务的失败状态本身已经记下了，
		// 少一句原因比整次采集中断要好
		return ""
	}
	txt := strings.TrimSpace(string(body))
	r := []rune(txt)
	if len(r) > maxRunes {
		return "…" + string(r[len(r)-maxRunes:])
	}
	return txt
}

// harborListSuffix Harbor 返回的镜像名会带 ` [3 item(s) in total]` 尾巴。
//
// 🔴 这个查文档查不到，只有真跑过 replication task 接口才知道。
// 不剥掉的话同一个服务会被算成两个 —— 一个带尾巴一个不带，
// 表现为「对方多了一个服务、我方少了一个」，最容易被误判成业务差异。
var harborListSuffix = regexp.MustCompile(`\s*\[\d+\s+item\(s\)\s+in\s+total\]\s*$`)

// harborBracketTag Harbor 单个 artifact 时把 tag 包在方括号里：
// `appA/bi-central-backend:[20260824083840-15f85908-223]`。
//
// 🔴 不剥的话存进库的 tag 是 `[20260824...]` 带括号，而对账拿真实 tag 去查，
// 永远匹配不上 —— 表现和"根本没记 tag"一模一样，都是归因失效，但成因完全不同。
var harborBracketTag = regexp.MustCompile(`:\[([^\]]+)\]$`)

func CleanHarborName(s string) string {
	s = strings.TrimSpace(harborListSuffix.ReplaceAllString(s, ""))
	// ⚠️ 多 tag 时 Harbor 写成 `repo:[t1 ... ]`，剥出来是没意义的串。
	//    只认单个 tag（不含空格和逗号），拿不准就整段丢掉，宁可"不知道"也不编一个。
	if m := harborBracketTag.FindStringSubmatch(s); m != nil {
		inner := strings.TrimSpace(m[1])
		if inner != "" && !strings.ContainsAny(inner, " ,") {
			return harborBracketTag.ReplaceAllString(s, ":"+inner)
		}
		return harborBracketTag.ReplaceAllString(s, "")
	}
	return s
}

// NormalizeTrigger 归一化触发方式。
//
// 🔴 Harbor 各版本取值不统一，实测有这些拼法：
//
//	manual
//	event / event_based / event-based
//	scheduled / schedule / cron
//
// 未识别的值**原样返回并打 WARN** —— 静默归到「其他」的话，
// 新增一种取值时没人会发现，界面上永远显示一句没用的「未知」。
func NormalizeTrigger(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "manual":
		return "manual"
	case "event", "event_based", "event-based":
		return "event_based"
	case "scheduled", "schedule", "cron":
		return "scheduled"
	case "":
		return ""
	default:
		logx.Warn("harbor", "unknown_trigger", map[string]any{
			"value": s, "msg": "未识别的 trigger 取值，已原样保留。Harbor 可能新增了类型"})
		return s
	}
}

// SucceededStatuses / FailedStatuses Harbor 的状态取值同样不统一（大小写、单复数都有）。
var (
	succeededSet = map[string]bool{"succeed": true, "success": true, "succeeded": true}
	failedSet    = map[string]bool{"failed": true, "error": true, "stopped": true}
)

func IsSucceeded(s string) bool { return succeededSet[strings.ToLower(strings.TrimSpace(s))] }
func IsFailed(s string) bool    { return failedSet[strings.ToLower(strings.TrimSpace(s))] }

func parseHarborTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, f := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ─────────────── 项目 / 仓库 / 制品 ───────────────
//
// 这一组回答的是「**我方**构建过哪些服务、各有哪些版本」。
//
// 🔴 与 replication 那一组的分工要分清：
//   这一组   = 我方 Harbor 里的事实（现在有什么）
//   那一组   = 推送过程的历史（推过什么、成没成）
// 「对方那边有没有」严格来说只有对方 Harbor 能回答，而我们没有对方的账号 ——
// 所以只能用「我方有这个 tag + 复制记录里有它的成功任务」来推断，
// 界面上必须说清这是**推断**而不是实测，不能让人以为我们真去对方那边看过。

// HarborProject 一个项目。
type HarborProject struct {
	Name      string `json:"name"`
	RepoCount int    `json:"repo_count"`
}

func (h *Harbor) Projects(ctx context.Context) ([]HarborProject, error) {
	var raw []struct {
		Name      string `json:"name"`
		RepoCount int    `json:"repo_count"`
	}
	if err := h.get(ctx, "/api/v2.0/projects?page_size=100", &raw); err != nil {
		return nil, err
	}
	out := make([]HarborProject, 0, len(raw))
	for _, p := range raw {
		out = append(out, HarborProject{Name: p.Name, RepoCount: p.RepoCount})
	}
	return out, nil
}

// HarborRepo 一个仓库 = 一个服务。
type HarborRepo struct {
	// FullName 形如 project/name，Harbor 返回的就是这个
	FullName string `json:"full_name"`
	// ServiceKey 镜像名最后一段 —— 与对账用的是**同一套 key**
	ServiceKey    string    `json:"service_key"`
	ArtifactCount int       `json:"artifact_count"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (h *Harbor) Repositories(ctx context.Context, project string) ([]HarborRepo, error) {
	var raw []struct {
		Name          string `json:"name"`
		ArtifactCount int    `json:"artifact_count"`
		UpdateTime    string `json:"update_time"`
	}
	path := "/api/v2.0/projects/" + url.PathEscape(project) + "/repositories?page_size=100"
	if err := h.get(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := make([]HarborRepo, 0, len(raw))
	for _, r := range raw {
		name := CleanHarborName(r.Name)
		key := name
		if i := strings.LastIndex(key, "/"); i >= 0 {
			key = key[i+1:]
		}
		out = append(out, HarborRepo{
			FullName: name, ServiceKey: key,
			ArtifactCount: r.ArtifactCount, UpdatedAt: parseHarborTime(r.UpdateTime),
		})
	}
	return out, nil
}

// HarborTag 一个可用版本。
type HarborTag struct {
	Tag      string    `json:"tag"`
	Digest   string    `json:"digest"`
	PushedAt time.Time `json:"pushed_at"`
}

// Tags 列一个仓库的版本，按推送时间倒序，最多 limit 个。
//
// ⚠️ 一个仓库可能有几百个 tag（我们的 Harbor 保留 100 个版本），
// 全拉既慢又没用 —— 人关心的是最近那几个。
//
// 🔴 repo 要传**不含项目名**的那一段。Harbor 的 artifacts 接口路径是
// /projects/{p}/repositories/{r}/artifacts，其中 {r} 若含 `/` 必须
// **二次转义**（%252F）—— 只转一次的话 Harbor 会把它当成路径分隔，返回 404。
func (h *Harbor) Tags(ctx context.Context, project, repo string, limit int) ([]HarborTag, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// 双重转义：先转成 %2F，再把 % 转成 %25
	esc := strings.ReplaceAll(url.PathEscape(repo), "%2F", "%252F")
	path := fmt.Sprintf("/api/v2.0/projects/%s/repositories/%s/artifacts?with_tag=true&page_size=%d&sort=-push_time",
		url.PathEscape(project), esc, limit)
	var raw []struct {
		Digest   string `json:"digest"`
		PushTime string `json:"push_time"`
		Tags     []struct {
			Name string `json:"name"`
		} `json:"tags"`
	}
	if err := h.get(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := []HarborTag{}
	for _, a := range raw {
		// 没有 tag 的制品（被覆盖过的旧 digest）跳过 —— 它们没法用来发布
		for _, t := range a.Tags {
			out = append(out, HarborTag{
				Tag: t.Name, Digest: a.Digest, PushedAt: parseHarborTime(a.PushTime),
			})
		}
	}
	return out, nil
}

// harborForbiddenHint 按被拒的接口给出**能直接照做**的说明。
//
// 🔴 Harbor 把复制拆成了**两个不同的系统级资源**，而 UI 上不容易看出区别：
//
//	replication-policy  → 复制**规则**   （/replication/policies）
//	replication         → 复制**执行**   （/replication/executions、tasks）
//
// 中文界面「机器人账户」里那一项叫「镜像复制」，勾上它给的是
// `replication` 的 create/read/list —— **不含 replication-policy**。
// 于是账号能读执行记录、却读不了规则。
//
// ⚠️ 原来这里笼统写「项目级 robot 没有权限，需要建系统级 robot」——
//
//	而用户建的**就是**系统级 robot，只是少勾了一个资源。
//	那句话会把人引向「再建一个系统级 robot」，建完还是不通。
//	实测（本地 Harbor v2.14）：只给 replication 的 robot，
//	读 /replication/policies 得 403，读 /replication/executions 得 200。
func harborForbiddenHint(path string) string {
	switch {
	case strings.Contains(path, "/replication/policies"):
		return "这个账号读不了**复制规则**。" +
			"Harbor 把复制拆成两个系统级资源：`replication-policy`（规则）和 `replication`（执行），" +
			"而机器人账户里那一项「镜像复制」只给了后者。" +
			"⚠️ 建系统级 robot 还不够 —— 要在它的系统权限里**另外勾上 replication-policy 的读取/查询**。" +
			"（可以用 API 直接补：resource=replication-policy, action=read 和 list。）"
	case strings.Contains(path, "/replication/"):
		return "这个账号读不了**复制执行记录**。" +
			"需要系统级 robot 并勾上 `replication` 的读取/查询。"
	}
	return ""
}

// truncate 日志里贴原文时限长：排查只需要看清结构和字段，前 N 个字符够用。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…（已截断）"
}
