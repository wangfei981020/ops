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
	//      "name_tag":   "biz-baccarat-h5-c-game-frontend [1 item(s) in total]"   ← 没有版本号
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
	if rep.ExecutionID > 0 {
		if apiRows := s.tasksAtWebhookTime(r.Context(), rep.HarborHostname,
			rep.ExecutionID, at); len(apiRows) > 0 {
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
		//    而 src_resource.namespace 实测拿到的是 "asia-dev"（源 registry 的名字），
		//    对不上我们存的源项目。dest_ns 才是那个 `bizB`。
		//    ——这是从生产真实 payload 里看出来的，不是猜的（17:35:48 那条）。
		srcProject = rep.DestResource.Namespace
	}
	if srcProject == "" && rep.SrcResource != nil {
		srcProject = rep.SrcResource.Namespace
	}
	saved, matchedBy, err := s.St.SaveWebhookSyncTasks(r.Context(), policyName, destEndpoint, srcProject, rows)
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

	s.notifyReplication(r.Context(), rep.JobStatus, policyName, destEndpoint, srcProject, rows, at)
	ok(w, map[string]any{"ok": true, "saved": saved})
}

// notifyReplication 把复制结果发到已配置的通知渠道，接替原来那个独立的
// harbor-replication 服务。
//
// ⚠️ 复用**已有的多渠道机制**（notify_channels + 三态记录），不另起一套：
//
//	另起一套的话，「为什么这条没通知我」会有两个互不相干的答案，
//	而排查的人不知道该看哪一个。
//
// ⚠️ 通知失败不影响已经落库的同步记录 —— 数据是主线，通知是支线。
func (s *Server) notifyReplication(ctx context.Context, jobStatus, policy,
	destEndpoint, srcProject string, rows []store.WebhookSyncTask, at time.Time,
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
	if len(failList) > 0 || notify.IsFailedStatus(jobStatus) {
		level = notify.LevelFailed
	}

	// 🔴 **webhook 这条路成功也发**，与被它替代的 harbor-replication 保持一致。
	//
	//    这里不走 notify.ShouldNotify —— 那套分级（自动触发且成功只入库不发）
	//    留给**采集器**那条路用，两条路都发成功通知会重复刷屏：
	//    webhook 实时发一次，30 分钟后采集器发现同一条 execution 状态变化又发一次。
	//
	//    分工：webhook = 实时通知（主），采集器 = 兜底对账（只在失败时补发）。
	//
	// ⚠️ 通知失败不影响已经落库的同步记录 —— 数据是主线，通知是支线。
	reason := "复制事件通知（成功也发，与原 harbor-replication 行为一致）"

	// Harbor 的 payload 里没有规则名，空着的话通知开头会是「Harbor 复制成功：」
	// 后面跟一片空白 —— 收到的人不知道是哪条链路。用源项目→目标兜底。
	label := strings.TrimSpace(policy)
	if label == "" {
		if srcProject != "" {
			label = srcProject + " → " + shortHost(destEndpoint)
		} else {
			label = shortHost(destEndpoint)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s Harbor 复制%s：%s\n", map[bool]string{true: "❌", false: "✅"}[level == notify.LevelFailed],
		map[bool]string{true: "失败", false: "成功"}[level == notify.LevelFailed], label)
	fmt.Fprintf(&b, "时间：%s\n成功 %d 个，失败 %d 个\n",
		at.Format("2006-01-02 15:04:05"), len(okList), len(failList))
	// 🔴 列出具体镜像。只报数量的话「成功 8 个」看不出少推了哪一个 ——
	//    而少推的那个正是对账要问的。上限 10 条，超出说明被截断了。
	show := func(title string, list []store.WebhookSyncTask) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s\n", title)
		for i, a := range list {
			if i >= 10 {
				fmt.Fprintf(&b, "…… 其余 %d 个未列出\n", len(list)-10)
				break
			}
			// 🔴 带上版本号。原来这里打的是 a.NameTag，而那个字段的值是
			//    `xxx [1 item(s) in total]` —— 通知里看不到版本号，
			//    而版本号正是收通知的人唯一关心的东西。
			if a.Tag != "" {
				fmt.Fprintf(&b, "  %s:%s\n", a.ServiceKey, a.Tag)
			} else {
				fmt.Fprintf(&b, "  %s（未取到版本号）\n", a.ServiceKey)
			}
		}
	}
	show("成功：", okList)
	show("失败：", failList)
	text := b.String()

	chans, err := s.St.ListChannels(ctx)
	if err != nil {
		logx.Error("webhook", "list_channels_failed", map[string]any{"err": err.Error()})
		return
	}
	for _, ch := range chans {
		if !ch.Enabled || ch.WebhookEnc == "" {
			continue
		}
		hook, err := s.Ciph.Decrypt(ch.WebhookEnc)
		if err != nil {
			logx.Warn("webhook", "channel_decrypt_failed", map[string]any{"channel": ch.Name})
			continue
		}
		// ⚠️ 这里原来又声明了一个局部 reason，把外层那个遮蔽掉了，
		//    于是通知记录里的「原因」一直是空串 —— 界面上看不出这条为什么发。
		state, errMsg := "sent", ""
		if err := notify.SendFeishu(hook, text); err != nil {
			state, errMsg = "failed", err.Error()
			logx.Warn("webhook", "notify_failed", map[string]any{"channel": ch.Name, "err": err.Error()})
		}
		_ = s.St.SaveNotifyRecord(ctx, ch.ID, 0, string(level), "event_based", state, reason, errMsg, text, 1)
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

// tasksAtWebhookTime 收到 webhook 的当下，立刻按 execution_id 回查任务明细。
//
// 🔴 时机就是全部意义所在：Harbor 的 task 里 `resource`（repository + tag）
// 只在复制刚结束的一小段时间内有值，之后变成 null，只剩不带版本号的字符串。
// 30 分钟一轮的定时采集去拉，拉到的必然是没有版本号的那份。
//
// 任何一步失败都只记日志、返回空，让调用方退回 webhook 自带的信息 ——
// 版本号是锦上添花，不能因为拿不到就把整条同步记录丢了。
func (s *Server) tasksAtWebhookTime(ctx context.Context, hostname string,
	execID int64, at time.Time,
) []store.WebhookSyncTask {
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
	hb := &providers.Harbor{Endpoint: target.Endpoint, Username: target.Username,
		Password: pw, InsecureTLS: target.InsecureTLS}
	tasks, err := hb.Tasks(ctx, execID)
	if err != nil {
		logx.Warn("webhook", "api_tasks_failed", map[string]any{
			"harbor": target.Name, "exec_id": execID, "err": err.Error()})
		return nil
	}
	out := make([]store.WebhookSyncTask, 0, len(tasks))
	for _, t := range tasks {
		fin := t.FinishedAt
		if fin.IsZero() {
			fin = at
		}
		out = append(out, store.WebhookSyncTask{
			ServiceKey: t.ServiceKey, Tag: t.Tag, Status: t.Status, FinishedAt: fin,
		})
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
