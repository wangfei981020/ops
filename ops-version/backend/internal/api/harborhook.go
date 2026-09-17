package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ops-version-backend/internal/imageref"
	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
	"ops-version-backend/notify"
	"ops-version-backend/providers"
)

// webhookTaskWindow 回查任务明细时，只认这个时间窗内结束的 task。
//
// 🔴 Harbor 的 execution 是**长期累积**的：按 execution_id 查任务明细，
// 拿回来的是这条复制规则历次执行的全部 task，不是"刚刚这一次"。
//
// 生产实测（2026-08-26，134 条 task_without_tag 日志）：
// 查询时刻 − task 结束时刻的中位数是 **258 天**，最大 363 天，
// 一分钟以内的一条都没有。那些陈年 task 的 resource 字段早已是 null，
// 于是「趁热去 API 拿版本号」实际拿回一批没有版本号的历史记录。
//
// 15 分钟：复制通常在秒级到分钟级完成，webhook 到达时 task 刚结束不久；
// 留出余量是为了容忍 Harbor 与本服务之间的时钟偏差和推送延迟。
const webhookTaskWindow = 15 * time.Minute

// harborEvent Harbor webhook 的 REPLICATION 事件。
//
// 🔴 只取我们真正要用的字段。Harbor 各版本 payload 会长，
// 全量映射一遍的话，对方加个字段我们就得跟着改，而那些字段我们根本不看。
type harborEvent struct {
	Type      string `json:"type"`
	OccurAt   int64  `json:"occur_at"`
	EventData struct {
		Replication struct {
			HarborHostname string `json:"harbor_hostname"`
			JobStatus      string `json:"job_status"`
			// 🔴 Description 是复制规则的**「描述」**字段，**不是规则名** ——
			//    而描述在 Harbor 里是可以不填的。实测过（2026-08-25）推来的就是空串，
			//    于是关联不到规则，saved 一直是 0，而 succeeded=1 明明有镜像。
			//
			//    Harbor 的 REPLICATION payload 里**根本没有规则名**这个字段，
			//    所以名字对不上时得靠 dest_resource 兜底（见 resolvePolicy）。
			Description string `json:"description"`
			// 🔴🔴 **版本号的钥匙。**
			//
			//    Harbor 的 task 明细里，`resource` 字典（含 repository + tag）
			//    只在复制刚结束的那一小段时间里有值，过后就变成 null，
			//    只剩 `xxx [1 item(s) in total]` 这种没有版本号的字符串。
			//
			//    所以拿版本号必须在**收到 webhook 的当下**就带着这个 id 去查 API，
			//    像老的 harbor-replication 脚本那样。等 30 分钟一轮的采集去拉，
			//    查到的一定是已经没有版本号的那份（实测 9 条 8-25 的 task 全是 null）。
			ExecutionID   int64           `json:"execution_id"`
			TriggerType   string          `json:"trigger_type"`
			PolicyCreator string          `json:"policy_creator"`
			SrcResource   *harborResource `json:"src_resource"`
			DestResource  *harborResource `json:"dest_resource"`
			// ⚠️ **这两个数组里不一定有版本号。** 实测过（2026-08-25）
			//    name_tag 拿到的是 `biz-xxx-frontend [1 item(s) in total]` ——
			//    有服务名、没版本号，成功和失败的终态事件都一样。
			//
			//    版本号的**权威来源是 REST API 的 task**（见 providers.Harbor.Tasks），
			//    webhook 只是"这条规则刚动过"的加速信号。
			//    我一度把这里注释成"webhook 才有 tag、API 没有 tag"，正好写反了：
			//    真相是 API 有，只是当时取值逻辑短路在 dst_resource 上没读到。
			SuccessfulArtifact []harborArtifact `json:"successful_artifact"`
			FailedArtifact     []harborArtifact `json:"failed_artifact"`
			// 🔴 原样留一份。
			//
			//    这条链路已经因为「不知道对方到底给了什么」返工过三次：
			//    以为 description 是规则名（实际是空的描述）、
			//    以为 name_tag 带版本号（实际拿到是空的）。
			//    每猜错一次就要等下一次真实事件才能验证，而事件几小时才来一次。
			//
			//    对接外部系统时，**先把原文记下来**比任何推断都便宜。
			RawSuccessful json.RawMessage `json:"-"`
		} `json:"replication"`
	} `json:"event_data"`
}

// harborResource 复制的源 / 目标。
//
// 🔴 payload 里没有规则名，这是**唯一**能把事件关联回复制规则的线索：
// endpoint 对应本站 sync_policies.dest_registry。
type harborResource struct {
	RegistryName string `json:"registry_name"`
	RegistryType string `json:"registry_type"`
	Endpoint     string `json:"endpoint"`
	Namespace    string `json:"namespace"`
}

type harborArtifact struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	NameTag string `json:"name_tag"`
	// 🔴🔴 **版本号在这里，不在 name_tag。**
	//
	//    实测过（2026-08-26 10:07:26 的真实 payload）：
	//      "name_tag":   "biz-svc-frontend [1 item(s) in total]"   ← 没有版本号
	//      "references": ["20260826020637-30"]                                    ← 版本号在这
	//
	//    name_tag 里 `[N item(s) in total]` 是「这个 artifact 有 N 个 tag」的意思，
	//    Harbor 把具体的 tag 放进 references 数组，而不是拼进 name_tag。
	//
	//    ⚠️ 这个字段找了整整四个版本。真正找到它的方式很朴素：
	//    把 **webhook 原文完整打进日志**，然后读它。
	//    在那之前我一直在已知字段之间猜来猜去，从没想过"有没有我没见过的字段"。
	References []string `json:"references"`
}

// harborHook 接收 Harbor 的复制事件。
//
// 🔴 这个端点**不走登录态** —— Harbor 不会带 cookie，只会带我们让它带的 Auth Header。
//
//	但它能写入对账依据，所以认证一步都不能省：伪造一条「已同步」出来，
//	界面上会显示成一个正常的绿灯，没有任何地方看得出异常。
//
// ⚠️ 一律返回 2xx（除非认证失败）。Harbor 对失败**不重发**，
//
//	而我们这边解析不了某一条不该让整个事件重来 —— 那只会丢更多。
//	解析问题一律记日志，让人事后能查，而不是让 Harbor 去重试。
func (s *Server) harborHook(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.St.ListWebhookTokens(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// 🔴 常量时间比较，且**无论有没有配令牌都走完整个循环** ——
	//    提前 return 会让「令牌不存在」和「令牌不对」在响应时间上有差别。
	presented := strings.TrimSpace(r.Header.Get("Authorization"))
	sum := sha256.Sum256([]byte(presented))
	matched := int64(0)
	for _, t := range tokens {
		if subtle.ConstantTimeCompare(sum[:], t.Hash) == 1 {
			matched = t.ID
		}
	}
	if presented == "" || matched == 0 {
		logx.Warn("webhook", "harbor_auth_failed", map[string]any{
			"remote": clientIP(r), "has_header": presented != "", "tokens": len(tokens),
			"note": "Harbor 那边的 Auth Header 与本站令牌对不上；令牌明文只在创建时显示一次，对不上就重建一个"})
		fail(w, http.StatusUnauthorized, "unauthorized", "webhook 令牌无效")
		return
	}

	// 🔴 先把原文留下来再解析。
	//    这条链路为了「对方到底给了什么」返工过四次，每次都要等下一个真实事件
	//    才能验证。原文进日志之后，再有格式问题看一眼就知道，不用猜也不用等。
	body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if readErr != nil {
		logx.Warn("webhook", "harbor_body_read_failed", map[string]any{"err": readErr.Error()})
		ok(w, map[string]any{"ok": false, "reason": "读取请求体失败，已记录"})
		return
	}
	logx.Info("webhook", "harbor_payload_raw", map[string]any{
		"bytes": len(body), "raw": truncateStr(string(body), 4000)})

	var ev harborEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		// ⚠️ 解析失败也返回 200：Harbor 不重发，让它重试没有意义，
		//    而 4xx 会在对方的 webhook 页面上堆一片红，掩盖真正的连通性问题。
		logx.Warn("webhook", "harbor_bad_payload", map[string]any{"err": err.Error()})
		ok(w, map[string]any{"ok": false, "reason": "payload 解析失败，已记录"})
		return
	}
	if !strings.EqualFold(ev.Type, "REPLICATION") {
		// 订阅了别的事件类型 —— 不是错误，如实说一声就好。
		// ⚠️ 摘要照样要记：「一直在收事件但都不是复制事件」是个真实场景
		//    （Harbor 那边事件类型选错了），不记的话界面上只看得到"最近调用"在跳。
		_ = s.St.TouchWebhookToken(r.Context(), matched,
			fmt.Sprintf("收到 %s 事件（不是 REPLICATION，已跳过）", ev.Type))
		ok(w, map[string]any{"ok": true, "skipped": ev.Type})
		return
	}

	rep := ev.EventData.Replication
	policyName := strings.TrimSpace(rep.Description)
	at := time.Now()
	if ev.OccurAt > 0 {
		at = time.Unix(ev.OccurAt, 0)
	}

	// 🔴 只要有一条 artifact 解析不出版本号，就把**整个 artifact 数组的原文**记下来。
	//    正常时不记（避免噪音），异常时一次给全 —— 不用再等下一轮事件。
	needRaw := false
	for _, a := range rep.SuccessfulArtifact {
		if !strings.Contains(a.NameTag, ":") {
			needRaw = true
			break
		}
	}
	if needRaw {
		raw, _ := json.Marshal(rep.SuccessfulArtifact)
		logx.Warn("webhook", "artifact_raw", map[string]any{
			"note":   "有 artifact 的 name_tag 里没有版本号，把原文记下来供排查",
			"raw":    truncateStr(string(raw), 2000),
			"policy": policyName,
			"dest_ns": func() string {
				if rep.DestResource != nil {
					return rep.DestResource.Namespace
				}
				return ""
			}(),
		})
	}

	// 🔴🔴 **同一次复制，Harbor 会发两次 webhook**：开始时一次、结束时一次。
	//
	//    开始那次的 successful_artifact 里**没有版本号**（镜像还没推完，
	//    Harbor 自己也还不知道最终推了哪些 tag）。把它也当成功记下来，
	//    落的就是一条「服务名对、规则对、版本号空」的废记录 ——
	//    正是线上看到的现象。
	//
	//    判据用**包含法**：只认列举出来的终态，其余一律跳过。
	//    反过来写成「排除 InProgress」的话，Harbor 哪天多一种中间态
	//    （或者大小写变了）就会重新漏进来，而这种漏是静默的。
	//
	//    旁证：同集群的老告警服务 harbor-replication 也是收两次 POST，
	//    只对第二次打印镜像列表 —— 它早就这么过滤了。
	switch strings.ToLower(strings.TrimSpace(rep.JobStatus)) {
	case "success", "succeed", "succeeded", "failure", "failed", "stopped":
		// 终态，继续
	default:
		logx.Info("webhook", "skip_non_terminal", map[string]any{
			"status": rep.JobStatus, "policy": policyName,
			"artifacts": len(rep.SuccessfulArtifact),
			"note":      "复制还没结束，这时候的 artifact 没有版本号，等结束那条"})
		ok(w, map[string]any{"ok": true, "saved": 0, "skipped": "non_terminal"})
		return
	}

	// 版本号是从哪个字段读到的 —— 按要求日志里必须体现来源。
	refFrom := map[string]int{}
	rows := make([]store.WebhookSyncTask, 0, len(rep.SuccessfulArtifact)+len(rep.FailedArtifact))
	collect := func(list []harborArtifact, defStatus string) {
		for _, a := range list {
			// name_tag 形如 `bi-central-backend:20260824083840-15f85908-223`
			ref := imageref.Parse(providers.CleanHarborName(a.NameTag))
			if ref.Name == "" {
				logx.Warn("webhook", "harbor_artifact_unparsed", map[string]any{
					"policy": policyName, "name_tag": a.NameTag,
					"note": "解析不出服务名，这一条丢弃 —— 归因会少一条，但不会错"})
				continue
			}
			// 🔴 解析出服务名但**没有版本号**时，把原始串记下来。
			//
			//    这条链路存在的全部意义就是拿到版本号 —— 拿不到就等于白接。
			//    而"拿不到"有两种可能：Harbor 没给，或者我们解析丢了，
			//    两者的处理完全不同（找对方 vs 改代码），只有原始串能分开。
			//
			// ⚠️ 这是第二次因为没记原始值而卡住（上一次是 policy 字段）：
			//    外部系统给的东西，**解析失败或结果异常时一定要留原文**，
			//    否则只能干等下一次事件再猜一轮。
			// name_tag 里没有版本号时用 references —— 生产上这才是常态。
			if ref.Tag == "" {
				for _, rf := range a.References {
					if rf = strings.TrimSpace(rf); rf != "" {
						ref.Tag = rf
						refFrom["references"]++
						break
					}
				}
			} else {
				refFrom["name_tag"]++
			}
			if ref.Tag == "" {
				logx.Warn("webhook", "artifact_without_tag", map[string]any{
					"service": ref.Name, "raw_name_tag": a.NameTag,
					"artifact_type": a.Type, "artifact_status": a.Status,
					"note": "name_tag 里没有版本号，这条丢弃 —— 落库只会变成永久对不上的废记录"})
				// 🔴 **不落库**。这张表存在的意义就是回答「同步过去的是哪个版本」，
				//    一条版本为空的记录回答不了任何问题，却会永远占着位置：
				//    终态那次带版本的事件进来时是另一条记录，盖不掉它。
				continue
			}
			st := strings.TrimSpace(a.Status)
			if st == "" {
				st = defStatus
			}
			rows = append(rows, store.WebhookSyncTask{
				ServiceKey: ref.Name, Tag: ref.Tag, Status: st, FinishedAt: at,
			})
		}
	}
	collect(rep.SuccessfulArtifact, "Succeed")
	collect(rep.FailedArtifact, "Failed")
	if len(refFrom) > 0 {
		logx.Info("webhook", "artifact_version_source", map[string]any{
			"by_field": refFrom, "artifacts": len(rows)})
	}

	// 🔴🔴 **趁热去 API 拿版本号。**
	//
	//    webhook 的 name_tag 实测只有 `xxx [1 item(s) in total]`，没有版本号；
	//    而 task 明细里的 `resource` 字典（repository + tag）**只在复制刚结束时有值**。
	//    老的 harbor-replication 脚本正是在收到事件的同一秒去查 API，才拿得到版本号。
	//
	//    这一步失败不影响主流程：拿不到就退回用 webhook 解析出的服务名，
	//    归因会少版本号，但不会错。
	hb := s.harborOfEvent(r.Context(), rep.HarborHostname)
	if rep.ExecutionID > 0 && hb != nil {
		if apiRows := s.tasksAtWebhookTime(r.Context(), hb, rep.ExecutionID, at); len(apiRows) > 0 {
			withTag := 0
			for _, x := range apiRows {
				if x.Tag != "" {
					withTag++
				}
			}
			logx.Info("webhook", "tasks_from_api", map[string]any{
				"exec_id": rep.ExecutionID, "tasks": len(apiRows), "with_tag": withTag,
				"note": "已趁复制刚结束回查 API；with_tag 是真正拿到版本号的条数"})
			// 只有真的拿到版本号才替换 —— API 也没有版本号时，
			// 两边内容一样，替换与否无所谓，但保持来源单一更好排查。
			if withTag > 0 {
				rows = apiRows
			}
		}
	}

	// 🔴 关联复制规则：先按名字，拿不到就用目标 registry 兜底。
	//
	//    Harbor 的 payload 里没有规则名（description 是"描述"且常为空），
	//    而归因真正需要的只是「推给哪个平台」—— dest_resource.endpoint
	//    对应本站 sync_policies.dest_registry，足够定位到平台。
	//
	// ⚠️ 同一个目标 registry 下可能有多条规则（生产上 appA/bizB/monitoring
	//    都指向同一个 Harbor）。只要它们绑的是**同一个平台**就够用；
	//    绑到不同平台时说不清推给谁，那时必须报出来而不是随便挑一条。
	destEndpoint, srcProject := "", ""
	if rep.DestResource != nil {
		destEndpoint = rep.DestResource.Endpoint
		// 🔴 用 **dest_ns** 当源项目，不是 src_ns。
		//
		//    Harbor 复制时目标项目与源项目同名（appA → appA、bizB → bizB），
		//    而 src_resource.namespace 实测拿到的是源 registry 的名字，
		//    对不上我们存的源项目。dest_ns 才是那个 `bizB`。
		//    ——这是从生产真实 payload 里看出来的，不是猜的（17:35:48 那条）。
		srcProject = rep.DestResource.Namespace
	}
	if srcProject == "" && rep.SrcResource != nil {
		srcProject = rep.SrcResource.Namespace
	}
	saved, policyRef, matchedBy, err := s.St.SaveWebhookSyncTasks(r.Context(), policyName, destEndpoint, srcProject, rows)
	if err != nil {
		// 🔴 存不进去要**明说**并返回非 2xx？不 —— Harbor 不重发，返回 5xx 只是让它记一条失败。
		//    但日志必须是 ERROR：这条链路断了的表现是「同步状态一直是未知」，
		//    而那看起来跟「还没配 webhook」一模一样。
		logx.Error("webhook", "harbor_save_failed", map[string]any{
			"policy": policyName, "artifacts": len(rows), "err": err.Error()})
		ok(w, map[string]any{"ok": false, "reason": "写入失败，已记录"})
		return
	}
	if saved == 0 && len(rows) > 0 {
		// 🔴 收到了 artifact 却一条没落库 = 策略名对不上（Harbor 改过名，或这条策略我们没拉过）。
		//    静默的话，表现是「webhook 配了但同步状态还是未知」，查不到原因。
		logx.Warn("webhook", "policy_not_found", map[string]any{
			"policy": policyName, "dest_endpoint": destEndpoint, "artifacts": len(rows),
			"note": "既没按策略名匹配上、目标 registry 也对不上本站任何一条已绑定平台的复制规则"})
	}
	// 🔴 把 payload 里所有能用来关联的字段都记下来。
	//    上一轮就卡在"policy 是空的，那还能用什么"——日志里没有别的字段，
	//    只能等下一次复制发生再看。记全了才不用重来一遍。
	fields := map[string]any{
		"policy": policyName, "status": rep.JobStatus,
		"succeeded": len(rep.SuccessfulArtifact), "failed": len(rep.FailedArtifact),
		"saved": saved, "matched_by": matchedBy,
		"trigger": rep.TriggerType, "creator": rep.PolicyCreator, "src_project_used": srcProject,
	}
	if rep.SrcResource != nil {
		fields["src_registry"] = rep.SrcResource.RegistryName
		fields["src_ns"] = rep.SrcResource.Namespace
	}
	if rep.DestResource != nil {
		fields["dest_registry"] = rep.DestResource.RegistryName
		fields["dest_endpoint"] = rep.DestResource.Endpoint
		fields["dest_ns"] = rep.DestResource.Namespace
	}
	logx.Info("webhook", "harbor_replication", fields)

	// 🔴 摘要写回令牌，让「为什么没落库」在**界面上**答得出来。
	//    三种失败各有不同的下一步，摘要必须能分出来：
	//      策略名对不上 → 去核对 Harbor 的复制规则名
	//      没有 artifact → Harbor 推的是"状态变更"而不是"复制完成"
	//      解析不出服务名 → name_tag 格式意外
	summary := fmt.Sprintf("策略「%s」·成功 %d 失败 %d · 落库 %d 条", policyName,
		len(rep.SuccessfulArtifact), len(rep.FailedArtifact), saved)
	switch {
	case len(rows) == 0:
		summary += "｜⚠ 事件里没有镜像明细（Harbor 可能只推了状态变更）"
	case saved == 0 && policyName == "":
		summary += fmt.Sprintf("｜⚠ Harbor 没给规则名，目标 %s 也对不上本站已绑定平台的复制规则", destEndpoint)
	case saved == 0:
		summary += fmt.Sprintf("｜⚠ 本站没有名为「%s」的复制规则，对不上就没法归因", policyName)
	}
	_ = s.St.TouchWebhookToken(r.Context(), matched, summary)

	s.notifyReplication(r.Context(), hb, policyRef, replicationNotice{
		JobStatus: rep.JobStatus, Trigger: rep.TriggerType, ExecID: rep.ExecutionID,
		PolicyName: policyName, DestEndpoint: destEndpoint, SrcProject: srcProject,
		At: at,
	}, rows)
	ok(w, map[string]any{"ok": true, "saved": saved})
}

// replicationNotice 一次复制事件里与「要不要通知、通知什么」有关的部分。
//
// ⚠️ 单独一个结构体而不是一串参数：原来是 7 个位置参数、其中 4 个 string
// （policy / destEndpoint / srcProject / jobStatus），顺序写错编译照过。
type replicationNotice struct {
	JobStatus    string
	Trigger      string
	ExecID       int64
	PolicyName   string
	DestEndpoint string
	SrcProject   string
	At           time.Time
}

// notifyReplication 把复制结果发到已配置的通知渠道，接替原来那个独立的
// harbor-replication 服务。
//
// # 发不发，只看这条规则的通知开关
//
// 🔴 不再按触发方式分级（原来是"自动触发且成功就不发"）。现在：
//
//	规则开了通知 → 成功、失败都发
//	规则没开     → 一条都不发，落一条 skipped 说明原因
//
// 防刷屏由白名单承担（人只勾自己关心的几条规则），比按触发方式猜精确得多。
//
// ⚠️ 这条路是**实时**的（Harbor 一推事件就发）；采集器那条是**兜底**，
// 靠 (规则, execution id) 去重，webhook 发成功过就不再重复发。
//
// ⚠️ 复用**已有的多渠道机制**（notify_channels + 三态记录），不另起一套：
//
//	另起一套的话，「为什么这条没通知我」会有两个互不相干的答案，
//	而排查的人不知道该看哪一个。
//
// ⚠️ 通知失败不影响已经落库的同步记录 —— 数据是主线，通知是支线。
func (s *Server) notifyReplication(ctx context.Context, hb *providers.Harbor,
	policyRef int64, n replicationNotice, rows []store.WebhookSyncTask,
) {
	okList, failList := []store.WebhookSyncTask{}, []store.WebhookSyncTask{}
	for _, x := range rows {
		if providers.IsFailed(x.Status) {
			failList = append(failList, x)
		} else {
			okList = append(okList, x)
		}
	}
	level := notify.LevelOK
	if len(failList) > 0 || notify.IsFailedStatus(n.JobStatus) {
		level = notify.LevelFailed
	}
	rec := store.NotifyRecordInput{
		PolicyRef: policyRef, HarborExecID: n.ExecID,
		Level: string(level), Trigger: n.Trigger,
	}

	// 🔴 没关联到规则就**不猜**。
	//
	//    通知开关挂在规则上，关联不上就无从判断该不该发。这时两种做法都是错的：
	//    一律发 → 等于白名单被绕过，用户关掉的规则照样吵；
	//    静默丢 → 事件真的是关心的那条规则时，人永远等不到通知且无痕迹。
	//    所以：不发，但落一条 skipped 写明原因，界面上查得到。
	if policyRef == 0 {
		rec.State = "skipped"
		rec.Reason = "事件没能关联到本站任何一条复制规则，无从判断是否该通知（见 policy_not_found 日志）"
		_ = s.St.SaveNotifyRecord(ctx, rec)
		return
	}
	p, err := s.St.PolicyByRef(ctx, policyRef)
	if err != nil {
		logx.Warn("webhook", "policy_row_failed", map[string]any{
			"ref": policyRef, "err": err.Error()})
		return
	}

	d := notify.ShouldNotify(p.NotifyEnabled, n.Trigger)
	rec.Reason = d.Reason
	if d.Warn {
		logx.Warn("webhook", "unknown_trigger", map[string]any{
			"trigger": n.Trigger, "policy": p.Name, "reason": d.Reason})
	}
	if !d.Send {
		rec.State = "skipped"
		_ = s.St.SaveNotifyRecord(ctx, rec)
		return
	}

	rep := notify.Replication{
		Level: level, Policy: p.Name, OrgName: p.OrgName, DestRegistry: p.DestRegistry,
		Trigger: n.Trigger, ExecID: n.ExecID, SyncedAt: n.At, Realtime: true,
		Succeeded: len(okList), Failed: len(failList), Total: len(rows),
	}
	// 🔴 耗时和总数只能从 execution 详情来 —— webhook 的 payload 里没有开始/结束时间，
	//    而 artifact 数组只数得出"这一次推了几个"，数不出这条 execution 总共几个。
	//    老的 harbor-replication 也是收到事件后回查这个接口算的。
	//
	// ⚠️ 查不到就保持零值：Card 会**整行不显示**耗时，而不是显示「0 秒」。
	if hb != nil && n.ExecID > 0 {
		if e, err := hb.Execution(ctx, n.ExecID); err == nil {
			if !e.StartedAt.IsZero() && !e.EndedAt.IsZero() && e.EndedAt.After(e.StartedAt) {
				rep.Duration = e.EndedAt.Sub(e.StartedAt)
			}
			if e.Total > 0 {
				rep.Total, rep.Succeeded, rep.Failed = e.Total, e.Succeeded, e.Failed
			}
			if rep.Trigger == "" {
				rep.Trigger = e.TriggerType
			}
		} else {
			logx.Info("webhook", "execution_detail_failed", map[string]any{
				"exec_id": n.ExecID, "err": err.Error(),
				"note": "拿不到耗时，通知里不显示那一行（不编一个 0 秒出来）"})
		}
	}
	for _, x := range okList {
		rep.OK = append(rep.OK, notify.Image{Service: x.ServiceKey, Tag: x.Tag})
	}
	for _, x := range failList {
		rep.Bad = append(rep.Bad, notify.Image{
			Service: x.ServiceKey, Tag: x.Tag, Reason: x.ErrMsg})
	}

	text := notify.Text(rep)
	card := notify.Card(rep)
	rec.Content = text

	chans, err := s.St.ListChannels(ctx)
	if err != nil {
		logx.Error("webhook", "list_channels_failed", map[string]any{"err": err.Error()})
		return
	}
	for _, ch := range chans {
		if !ch.Enabled || ch.WebhookEnc == "" {
			continue
		}
		// 渠道绑了平台就只发那个平台的 —— 与采集器那条路同一套判断
		if ch.OrgID != nil && (p.OrgID == nil || *ch.OrgID != *p.OrgID) {
			continue
		}
		hook, err := s.Ciph.Decrypt(ch.WebhookEnc)
		if err != nil {
			logx.Warn("webhook", "channel_decrypt_failed", map[string]any{"channel": ch.Name})
			continue
		}
		// ⚠️ 这里原来又声明了一个局部 reason，把外层那个遮蔽掉了，
		//    于是通知记录里的「原因」一直是空串 —— 界面上看不出这条为什么发。
		r := rec
		r.ChannelID, r.State, r.Attempts = ch.ID, "sent", 1
		if err := notify.SendFeishuCard(hook, card); err != nil {
			r.State, r.ErrMsg = "failed", err.Error()
			logx.Warn("webhook", "notify_failed", map[string]any{"channel": ch.Name, "err": err.Error()})
		}
		_ = s.St.SaveNotifyRecord(ctx, r)
	}
}

// ---------- 令牌管理 ----------

// webhookInfo 给界面用：端点地址 + 已有令牌。
//
// 🔴 地址由**后端**拼，不让前端拼：Harbor 在集群内填的是 svc 地址，
// 而前端只知道自己的浏览器地址（往往是公网域名）—— 让它拼必然拼错，
// 而拼错的表现是 Harbor 那边一直失败，没人会想到是地址的问题。
func (s *Server) webhookInfo(w http.ResponseWriter, r *http.Request) {
	list, err := s.St.ListWebhookTokens(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	out := []map[string]any{}
	for _, t := range list {
		row := map[string]any{"id": t.ID, "name": t.Name, "created_by": t.CreatedBy,
			"created_at": t.CreatedAt.Format("2006-01-02 15:04:05"), "last_used_at": "",
			"last_event": t.LastEvent}
		if t.LastUsedAt.Valid {
			row["last_used_at"] = t.LastUsedAt.Time.Format("2006-01-02 15:04:05")
		}
		out = append(out, row)
	}
	ok(w, map[string]any{"path": "/api/webhooks/harbor", "tokens": out})
}

func (s *Server) createWebhookToken(w http.ResponseWriter, r *http.Request) {
	req, err := body[struct {
		Name string `json:"name"`
	}](r)
	if err != nil || strings.TrimSpace(req.Name) == "" {
		fail(w, http.StatusBadRequest, "bad_request", "要给令牌起个名字（写清用途，将来才知道能不能吊销）")
		return
	}
	plain, err := s.St.CreateWebhookToken(r.Context(), req.Name, userOf(r).Username)
	s.St.Audit(r.Context(), userOf(r).Username, "webhook_token.create", req.Name, nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// ⚠️ 明文只此一次。存哈希的意义就在于我们自己也拿不回来
	ok(w, map[string]any{"token": plain})
}

func (s *Server) deleteWebhookToken(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := s.St.DeleteWebhookToken(r.Context(), id)
	s.St.Audit(r.Context(), userOf(r).Username, "webhook_token.delete",
		fmt.Sprintf("id=%d", id), nil, err, clientIP(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

// truncateStr 日志里贴原文时限长 —— 一条 artifact 数组可能很大，
// 而排查只需要看清字段名和形态，前 2000 字符足够。
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…（已截断）"
}

// harborOfEvent 按事件里的 harbor_hostname 找到本站对应的 Harbor 配置，建一个客户端。
//
// 🔴 回查任务明细（拿版本号、拿失败原因）和回查 execution 详情（拿耗时）
// 用的是同一个客户端 —— 原来它藏在 tasksAtWebhookTime 里面，
// 于是通知那边想查耗时就只能再复制一份找 Harbor 的逻辑。
//
// 找不到返回 nil，调用方各自降级：没有版本号照样落库，没有耗时就不显示那一行。
func (s *Server) harborOfEvent(ctx context.Context, hostname string) *providers.Harbor {
	harbors, err := s.St.ListHarbors(ctx)
	if err != nil {
		logx.Warn("webhook", "list_harbors_failed", map[string]any{"err": err.Error()})
		return nil
	}
	host := strings.TrimSpace(hostname)
	var target *store.Harbor
	for i := range harbors {
		h := &harbors[i]
		if !h.Enabled {
			continue
		}
		if host != "" && strings.Contains(h.Endpoint, host) {
			target = h
			break
		}
	}
	// hostname 对不上（或 Harbor 没给）时，只有唯一一个 Harbor 才敢兜底 ——
	// 多个的话选错就是把 A 的版本号安到 B 头上，比没有版本号更糟。
	if target == nil {
		enabled := []*store.Harbor{}
		for i := range harbors {
			if harbors[i].Enabled {
				enabled = append(enabled, &harbors[i])
			}
		}
		if len(enabled) == 1 {
			target = enabled[0]
		} else {
			logx.Warn("webhook", "harbor_not_matched", map[string]any{
				"harbor_hostname": hostname, "enabled_harbors": len(enabled),
				"note": "按 harbor_hostname 找不到对应的 Harbor，且不止一个，放弃回查（不猜）"})
			return nil
		}
	}
	pw := ""
	if target.CredentialEnc != "" {
		v, err := s.Ciph.Decrypt(target.CredentialEnc)
		if err != nil {
			logx.Warn("webhook", "harbor_credential_failed", map[string]any{
				"harbor": target.Name, "err": err.Error()})
			return nil
		}
		pw = v
	}
	return &providers.Harbor{Endpoint: target.Endpoint, Username: target.Username,
		Password: pw, InsecureTLS: target.InsecureTLS}
}

// tasksAtWebhookTime 收到 webhook 的当下，立刻按 execution_id 回查任务明细。
//
// 🔴 时机就是全部意义所在：Harbor 的 task 里 `resource`（repository + tag）
// 只在复制刚结束的一小段时间内有值，之后变成 null，只剩不带版本号的字符串。
// 30 分钟一轮的定时采集去拉，拉到的必然是没有版本号的那份。
//
// 任何一步失败都只记日志、返回空，让调用方退回 webhook 自带的信息 ——
// 版本号是锦上添花，不能因为拿不到就把整条同步记录丢了。
func (s *Server) tasksAtWebhookTime(ctx context.Context, hb *providers.Harbor,
	execID int64, at time.Time,
) []store.WebhookSyncTask {
	tasks, err := hb.Tasks(ctx, execID)
	if err != nil {
		logx.Warn("webhook", "api_tasks_failed", map[string]any{
			"exec_id": execID, "err": err.Error()})
		return nil
	}
	// 🔴 只给**失败**的 task 拉日志，为的是通知里那行「原因」。
	//
	//    老的 harbor-replication 只报"失败 N 个"，收到的人还得自己去 Harbor 翻，
	//    而原因就躺在 task 日志里。采集器那条路早就在拉了（harborsync.go），
	//    webhook 这条路原来没拉 —— 同一件事两条路表现不一致。
	//
	// ⚠️ 只拉失败的：一次执行可能几百个 task，全拉会把 Harbor 打爆，
	//    而成功任务的日志没有任何价值。
	for i := range tasks {
		if providers.IsFailed(tasks[i].Status) && tasks[i].TaskID > 0 {
			tasks[i].ErrMsg = hb.TaskLog(ctx, execID, tasks[i].TaskID, 400)
		}
	}

	out := make([]store.WebhookSyncTask, 0, len(tasks))
	stale, noTag := 0, 0
	for _, t := range tasks {
		fin := t.FinishedAt
		if fin.IsZero() {
			fin = at
		}
		// 🔴 只要**这一次**复制刚产生的 task。
		//
		//    Harbor 的 execution 是长期累积的：一个 execution 下躺着这条规则
		//    历次复制的全部 task，按 execution_id 查会把它们**一并**带回来。
		//    而 `resource` 只在复制刚结束时有值 —— 陈年 task 早就是 null 了。
		//
		//    生产实测（2026-08-26 日志 134 条 task_without_tag）：
		//      查询时刻 − task 结束时刻，中位数 **258 天**，最大 363 天，
		//      1 分钟以内的**一条都没有**，超过 1 小时的 132/134。
		//      它们无一例外 resource=null、name_tag=""、name=""。
		//
		//    不拦的话，每收到一次 webhook 就往库里灌一批没有版本号的记录，
		//    界面上表现为「Harbor 未记录版本」——而这张表存在的全部意义
		//    就是回答「同步过去的是哪个版本」。
		if at.Sub(fin) > webhookTaskWindow {
			stale++
			continue
		}
		// 🔴 版本号为空的不落库 —— 与 collect() 保持**同一个标准**。
		//
		//    原来 collect()（webhook 自带的 artifact）严格丢弃空版本号，
		//    而这条 API 回查路径全盘接收，两条路径写进同一张表。
		//    调用方还是 `if withTag > 0 { rows = apiRows }` —— 整批替换：
		//    一次复制里只要有一个 task 查到版本号，其余查不到的也跟着落库。
		//
		// ⚠️ 同一份数据的两条入口用不同的过滤标准，迟早会从宽的那条漏进来。
		if strings.TrimSpace(t.Tag) == "" {
			noTag++
			continue
		}
		out = append(out, store.WebhookSyncTask{
			ServiceKey: t.ServiceKey, Tag: t.Tag, Status: t.Status, FinishedAt: fin,
			TaskID: t.TaskID, ErrMsg: t.ErrMsg,
		})
	}
	if stale > 0 || noTag > 0 {
		logx.Info("webhook", "api_tasks_filtered", map[string]any{
			"exec_id": execID, "kept": len(out), "stale": stale, "no_tag": noTag,
			"window": webhookTaskWindow.String(),
			"note": "stale=不属于本次复制的历史 task；no_tag=Harbor 没给版本号。" +
				"两者都不落库：没有版本号的记录回答不了「推的是哪个版本」"})
	}
	return out
}

// shortHost 从 endpoint 里取主机名，用于通知标题。
// Harbor 的复制事件里没有规则名，只能拿目标地址凑一个人能看懂的标识。
func shortHost(endpoint string) string {
	v := strings.TrimSpace(endpoint)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://")
	if i := strings.IndexAny(v, "/:"); i > 0 {
		v = v[:i]
	}
	if v == "" {
		return "未知目标"
	}
	return v
}
