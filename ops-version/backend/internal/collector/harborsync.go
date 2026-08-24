package collector

import (
	"context"
	"errors"
	"strings"
	"time"

	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
	"ops-version-backend/providers"
)

// SyncHarbors 拉取所有启用的 Harbor 的复制记录。
//
// manualRun=true 表示是人点了「立即拉取」—— 这会让通知规则对
// 「自动触发且成功」也回一条（人在等结果）。
//
// 这一层的产出是**归因**：同样是「对方版本落后」，
// 有了同步记录才能分清是「镜像没推过去」（我们的锅）
// 还是「推过去了对方没发版」（对方的节奏）。
// 没有它，两种情况在对账表上长得一模一样，而处理方式完全相反。
func (c *Collector) SyncHarbors(ctx context.Context, manualRun bool) {
	hs, err := c.st.ListHarbors(ctx)
	if err != nil {
		logx.Error("harborsync", "list_failed", map[string]any{"err": err.Error()})
		return
	}
	for _, h := range hs {
		if !h.Enabled {
			continue
		}
		if err := c.syncOne(ctx, h, manualRun); err != nil {
			// 🔴 分类落库，不要统一成「同步失败」——
			//    密码错、网络不通、权限不足三种的处理方式完全不同
			status := classify(err)

			// 🔴 只缺 replication 权限时**降级，不当故障**。
			//
			// Harbor 的权限是分级的：项目级 robot 能读项目/仓库/制品
			// （「按服务查版本」照常可用），只是读不到复制记录。
			// 把它记成 forbidden 会让整页飘红、每轮刷一条 WARN，
			// 看起来像系统坏了 —— 而实际上只是一个**可选**能力没开。
			// 记成 unsupported：界面上说明"这一列需要系统级 robot"，
			// 其余功能照常，日志也降为 info。
			if errors.Is(err, providers.ErrForbidden) && c.onlyReplicationMissing(ctx, h) {
				_ = c.st.MarkHarborSync(ctx, h.ID, "unsupported", err.Error())
				logx.Info("harborsync", "replication_unsupported", map[string]any{
					"harbor": h.Name,
					"note":   "凭据可读项目但读不到复制记录；按服务查版本不受影响"})
				continue
			}

			_ = c.st.MarkHarborSync(ctx, h.ID, status, err.Error())
			logx.Warn("harborsync", "failed", map[string]any{
				"harbor": h.Name, "status": status, "err": err.Error()})
			continue
		}
		_ = c.st.MarkHarborSync(ctx, h.ID, "success", "")
	}
}

// orgOfPolicy 取规则绑定的组织。没绑就返回空 —— 通知里会少一行「组织」，
// 但不该因此不发（规则没绑定不影响同步本身失败这个事实）。
func (c *Collector) orgOfPolicy(ctx context.Context, ref int64) (int64, string) {
	ps, err := c.st.ListPolicies(ctx)
	if err != nil {
		return 0, ""
	}
	for _, p := range ps {
		if p.Ref == ref && p.OrgID != nil {
			return *p.OrgID, p.OrgName
		}
	}
	return 0, ""
}

func (c *Collector) syncOne(ctx context.Context, h store.Harbor, manualRun bool) error {
	pw := ""
	if h.CredentialEnc != "" {
		v, err := c.dec.Decrypt(h.CredentialEnc)
		if err != nil {
			return err
		}
		pw = v
	}
	hb := &providers.Harbor{
		Endpoint: h.Endpoint, Username: h.Username, Password: pw, InsecureTLS: h.InsecureTLS,
	}

	policies, err := hb.Policies(ctx)
	if err != nil {
		return err
	}
	// 🔴 只留关心的那几条。一个 Harbor 上可能有几十条规则，
	//    而每条要拉 20 次执行、每次执行还要拉 tasks —— 全量拉是上千次请求，
	//    每轮采集都打一遍，会把 Harbor 拖慢。
	if len(h.PolicyFilter) > 0 {
		kept := policies[:0:0]
		for _, p := range policies {
			if matchAnyPattern(h.PolicyFilter, p.Name) {
				kept = append(kept, p)
			}
		}
		logx.Debug("harborsync", "policy_filtered", map[string]any{
			"harbor": h.Name, "total": len(policies), "kept": len(kept),
			"patterns": h.PolicyFilter})
		policies = kept
	}
	if err := c.st.SavePolicies(ctx, h.ID, policies); err != nil {
		return err
	}
	logx.Debug("harborsync", "policies", map[string]any{"harbor": h.Name, "count": len(policies)})

	for _, p := range policies {
		ref, err := c.st.PolicyRefOf(ctx, h.ID, p.PolicyID)
		if err != nil {
			continue
		}
		// 每条规则只拉最近 20 次执行：再往前的对「现在哪些镜像同步过了」没有意义，
		// 而全量拉会让第一次采集打几千次接口
		execs, err := hb.Executions(ctx, p.PolicyID, 20)
		if err != nil {
			logx.Warn("harborsync", "executions_failed", map[string]any{
				"harbor": h.Name, "policy": p.Name, "err": err.Error()})
			continue
		}
		changed, err := c.st.SaveExecutions(ctx, ref, execs)
		if err != nil {
			return err
		}
		// 只对**状态变化的**执行发通知。每轮拉取都会看到同样的历史执行，
		// 无条件通知的话一条三天前的失败会被反复重发
		for _, e := range changed {
			orgID, orgName := c.orgOfPolicy(ctx, ref)
			c.notifyExecution(ctx, ref, orgID, orgName, p.Name, e, manualRun)
		}

		// 任务明细的拉取结果要统计出来：executions 拉到了而 tasks 一条没拉到时，
		// 「按服务查同步」整个不可用、比对页的归因全变成「同步状态未知」，
		// 而这两处表现都不指向 Harbor 拉取 —— 不汇总的话没人知道断在这一层
		taskOK, taskFail, skipped := 0, 0, 0
		for _, e := range execs {
			// 进行中的执行任务列表还会变，跳过 —— 下一轮采集再拉
			if !providers.IsSucceeded(e.Status) && !providers.IsFailed(e.Status) {
				skipped++
				continue
			}
			tasks, err := hb.Tasks(ctx, e.ExecID)
			if err != nil {
				// 🔴 这里原来是**光秃秃的 continue，一句日志都没有**。
				//    于是 executions 有数据、sync_tasks 空着，而日志里什么都看不到 ——
				//    排查时只能从"界面说没推过任何服务"一路反推到这一行。
				//    紧邻的 Executions 失败是有 Warn 的，这一支当初漏了。
				taskFail++
				logx.Warn("harborsync", "tasks_failed", map[string]any{
					"harbor": h.Name, "policy": p.Name, "exec": e.ExecID,
					"err": err.Error(),
				})
				continue
			}
			taskOK++
			// 🔴 只给**失败**的任务拉日志。一次执行可能几百个 task，
			//    全拉既慢又会把 Harbor 打爆，而成功任务的日志没有价值
			for i := range tasks {
				if providers.IsFailed(tasks[i].Status) && tasks[i].TaskID > 0 {
					tasks[i].ErrMsg = hb.TaskLog(ctx, e.ExecID, tasks[i].TaskID, 400)
				}
			}
			if err := c.st.SaveTasks(ctx, ref, e.ExecID, tasks); err != nil {
				return err
			}
		}
		// 🔴 「一次都没拉到」单独升一级：这正是 的现场 ——
		//    执行记录 74 条全是 Succeed，任务明细 0 条，而界面把它说成
		//    「这条规则还没有推过任何服务」。有这条日志就能一眼分清
		//    「确实没推过」和「我们没拉到明细」。
		if taskOK == 0 && taskFail > 0 {
			logx.Error("harborsync", "tasks_all_failed", map[string]any{
				"harbor": h.Name, "policy": p.Name, "failed": taskFail,
				"note": "执行记录拉到了但任务明细一条都没拉到 —— " +
					"「按服务查同步」会完全不可用，比对页的归因会全部退化成「同步状态未知」。" +
					"先看上面的 tasks_failed 日志确认是权限还是接口问题",
			})
		} else if taskFail > 0 {
			logx.Warn("harborsync", "tasks_partial", map[string]any{
				"harbor": h.Name, "policy": p.Name,
				"ok": taskOK, "failed": taskFail, "skipped": skipped,
			})
		}
	}
	return nil
}

// RunHarborLoop 后台定时同步。
//
// ⚠️ 与版本采集分开跑：Harbor 挂了不该影响版本对账，反之亦然。
// 两者混在一个循环里的话，一边超时会把另一边也拖住。
func (c *Collector) RunHarborLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	c.SyncHarbors(ctx, false)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.SyncHarbors(ctx, false)
		}
	}
}

// matchAnyPattern 规则名是否命中任一模式。与采集层的 ns/workload 用同一套通配语义
// （app-* / *-canary / *mid* / 全等）—— 三处输入框写同样的东西必须得到同样的结果。
func matchAnyPattern(pats []string, name string) bool {
	for _, p := range pats {
		if providers.MatchPattern(strings.TrimSpace(p), name) {
			return true
		}
	}
	return false
}

// onlyReplicationMissing 判断"只是缺 replication 权限"，而不是凭据整个不可用。
//
// ⚠️ 必须真去探一次项目级接口，不能只看错误串。
// 凭据过期时 replication 同样返回 403，两者错误长得一样，
// 只凭错误分类会把"凭据坏了"也降级成"能力未开" —— 那才是真故障被藏起来。
func (c *Collector) onlyReplicationMissing(ctx context.Context, h store.Harbor) bool {
	pw := ""
	if h.CredentialEnc != "" {
		if v, err := c.dec.Decrypt(h.CredentialEnc); err == nil {
			pw = v
		}
	}
	hb := &providers.Harbor{Endpoint: h.Endpoint, Username: h.Username,
		Password: pw, InsecureTLS: h.InsecureTLS}
	caps := hb.Capabilities(ctx)
	return caps.Projects && !caps.Replication
}
