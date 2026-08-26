// OpsVersion 多组织版本对账。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/term"

	"ops-version-backend/config"
	"ops-version-backend/crypto"
	"ops-version-backend/database"
	"ops-version-backend/internal/api"
	"ops-version-backend/internal/collector"
	"ops-version-backend/internal/metrics"
	"ops-version-backend/internal/store"
	"ops-version-backend/logx"
)

var version = "dev" // 构建时 -ldflags 注入

func main() {
	// 子命令必须在读配置**之前**判断吗？不 —— 重置密码同样需要数据库配置。
	// 但它必须在**启动服务之前**分叉出去，跑完就退出。
	resetUser := flag.String("reset-password", "",
		"重置指定用户的密码后退出（密码从终端读入，不走参数）")
	showVersion := flag.Bool("version", false, "打印版本后退出")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		// 配置不全就直接退出，不要带着坏配置跑 ——
		// 加密密钥缺失这种事，等到第一次存凭据才发现就晚了
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}

	// 先设日志级别，再做任何会打日志的事
	logx.SetLevel(cfg.LogLevel)

	db, err := database.Open(cfg.DSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "连接数据库失败:", err)
		os.Exit(1)
	}
	defer db.Close()

	ciph, err := crypto.New(cfg.EncryptKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "初始化加密失败:", err)
		os.Exit(1)
	}

	st := store.New(db)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ── 子命令：重置密码，跑完即退出 ──
	if *resetUser != "" {
		if err := runResetPassword(ctx, st, *resetUser); err != nil {
			fmt.Fprintln(os.Stderr, "重置失败:", err)
			os.Exit(1)
		}
		return
	}

	if err := st.EnsureSuperUser(ctx, cfg.SuperUser, cfg.SuperPassword); err != nil {
		fmt.Fprintln(os.Stderr, "初始化超管失败:", err)
		os.Exit(1)
	}

	// 🔴 角色表要在**开始服务之前**装进内存，并校验内置角色没被改过。
	//    装不上就拒绝启动：带着一份不确定的权限表对外服务，
	//    比起不起来危险得多 —— 表现会是「界面显示有这个权限、点了却 403」，
	//    或者更糟，反过来。
	if err := st.LoadRolesIntoAuth(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "装载角色失败:", err)
		os.Exit(1)
	}

	coll := collector.New(st, ciph)
	srv := &api.Server{St: st, Cfg: cfg, Ciph: ciph, Coll: coll}

	// 定时采集。
	// 启动后先等一会儿再跑第一轮：Pod 刚起来时数据库连接池、依赖服务都还在预热，
	// 这时候去打外部 API 失败率偏高，会在界面上留下一条没必要的失败记录。
	go func() {
		select {
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
			return
		}
		t := time.NewTicker(cfg.CollectInterval)
		defer t.Stop()
		for {
			coll.CollectAll(ctx)
			select {
			case <-t.C:
			case <-ctx.Done():
				return
			}
		}
	}()

	// 预置通知指标：一条都没发过时 *Vec 不输出样本，
	// 「通知从来没成功过」这种最严重的状态会在监控上完全静默
	metrics.EnsureNotify()

	// Harbor 同步拉取。
	//
	// ⚠️ **与版本采集分开一个 goroutine**：Harbor 挂了不该影响版本对账，反之亦然。
	// 混在一个循环里的话，一边超时会把另一边一起拖住 ——
	// 表现是「Harbor 连不上，结果连版本数据也不更新了」。
	go func() {
		select {
		case <-time.After(45 * time.Second):
		case <-ctx.Done():
			return
		}
		coll.RunHarborLoop(ctx, cfg.CollectInterval)
	}()

	// 健康检查独立监听 metrics 端口。
	//
	// 与业务端口分开是刻意的：业务端口被慢查询占满、hang 死的时候，
	// 探针打业务端口会超时 → kubelet 判定「进程没了」把 Pod 杀掉重建，
	// 而实际问题是数据库慢，重建解决不了还会放大。
	// 独立端口能让「卡住」和「死了」在监控上分得开。
	//
	// ⚠️ 代价是 /ready 反映不了业务端口的状态 —— 见下面 draining 那段注释。
	go func() {
		m := http.NewServeMux()
		// Prometheus 指标。ServiceMonitor 抓的就是这里 ——
		// chart 里 metrics.path 必须与之一致，不一致时对象照样创建成功、
		// 抓取一直 404，而 kubectl get servicemonitor 看着完全正常
		m.Handle("/metrics", promhttp.Handler())
		// /health 存活：进程还在就返回 200，**刻意不查数据库** ——
		// 查了的话数据库一抖动就会触发全部副本重启，把小故障放大成全站不可用
		m.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
		// /ready 就绪：要查数据库。连不上就摘流量，但**不重启**
		m.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
			// 🔴 正在关闭时必须立刻报「不就绪」，这是**发布期白屏的根因**：
			//
			//    /ready 挂在 metrics 端口上，而业务流量走另一个端口。
			//    原来收到 SIGTERM 后直接 h.Shutdown()（业务端口开始拒新连接），
			//    可 metrics 端口还好好活着、db 也 ping 得通 —— 于是 /ready 继续返回 200，
			//    kubelet 认为这个 Pod 还 Ready，Endpoints 不摘，
			//    Istio/nginx 继续把请求送进来，而业务端口已经不收了 → 504。
			//
			//    生产是单副本，这个窗口里没有第二个 Pod 兜底，
			//    表现就是「更新过程中整个页面白屏」。
			//
			// ⚠️ 顺序不能反：必须**先**让这里返回 503、等 Endpoints 传播出去，
			//    **再**去 Shutdown 业务端口。反过来就是上面那个 bug。
			if draining.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("draining"))
				return
			}
			c, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			if err := db.PingContext(c); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("db: " + err.Error()))
				return
			}
			_, _ = w.Write([]byte("ready"))
		})
		mh := &http.Server{Addr: fmt.Sprintf(":%d", cfg.MetricsPort), Handler: m,
			ReadHeaderTimeout: 10 * time.Second}
		metricsSrv.Store(mh)
		if err := mh.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logx.J("boot", "metrics_listen_fail", map[string]any{"err": err.Error()})
		}
	}()

	h := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logx.J("boot", "listen", map[string]any{
			"port": cfg.Port, "version": version,
			"collect_interval": cfg.CollectInterval.String()})
		if err := h.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logx.J("boot", "listen_fail", map[string]any{"err": err.Error()})
			os.Exit(1)
		}
	}()

	<-ctx.Done()

	// ─── 优雅下线：先摘流量，再关服务 ───
	//
	// 🔴 这个顺序是发布期不白屏的关键。收到 SIGTERM 之后，Pod 并不会
	//    立刻从 Endpoints 里消失 —— 摘除是异步的，还要等 kube-proxy /
	//    Istio sidecar 同步到。这段时间里请求**仍然会送进来**，
	//    而如果我们已经开始 Shutdown，它们全部会失败。
	//
	//    所以：① 先让 /ready 返回 503（主动告诉 kubelet 别再给我流量）
	//         ② 等一会儿，让摘除传播出去，期间**继续正常服务**
	//         ③ 再优雅关闭业务端口（等已在处理的请求做完）
	//         ④ 最后关 metrics 端口
	//
	// ⚠️ chart 里的 preStop sleep 是同一件事的另一半保险：preStop 在 SIGTERM
	//    **之前**执行，能更早开始排空。两者都留着 —— 直接 kubectl delete pod
	//    或节点驱逐时 preStop 照样跑，而这里这段在任何路径下都生效。
	draining.Store(true)
	logx.J("boot", "draining", map[string]any{
		"wait": drainWait.String(),
		"note": "已停止上报就绪，等待 Endpoints 摘除后再关闭；期间仍正常处理请求"})
	// ⚠️ 故意用 Sleep 而不是 select ctx：ctx 已经因为 SIGTERM 取消了，
	//    监听它会立刻返回，这段等待就白写了 —— 而等待本身就是目的。
	time.Sleep(drainWait)

	logx.J("boot", "shutdown", nil)
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = h.Shutdown(sctx)
	// metrics 端口最后关：它承载 /ready，早关了 kubelet 探不到会当成"探测失败"，
	// 与"主动报告不就绪"在事件里长得不一样，排查时会误导
	if mh, ok := metricsSrv.Load().(*http.Server); ok && mh != nil {
		_ = mh.Shutdown(sctx)
	}
}

// draining 进程是否正在下线。只由 /ready 读，收到 SIGTERM 后置位。
var draining atomic.Bool

// metricsSrv 指标/探针端口的 server，关闭时要用。
// ⚠️ 用 atomic.Value 而不是普通变量：它在另一个 goroutine 里创建。
var metricsSrv atomic.Value

// drainWait 置位「不就绪」之后、开始关闭之前的等待时间。
//
// 🔴 要盖住 Endpoints 摘除传播到所有数据面的时间。
//
//	kubelet 的 readiness 周期是 5s、failureThreshold 3 —— 但我们是**主动**报 503，
//	第一次探测就会失败，所以真正要等的是"探测到 → endpoint 摘除 → sidecar 收到"。
//	Istio 环境下这条链比纯 kube-proxy 长，给到 10s 才有余量。
//
// ⚠️ 必须小于 chart 里的 terminationGracePeriodSeconds（60s），
//	否则等待还没结束就被 SIGKILL，优雅关闭一步都走不到。
const drainWait = 10 * time.Second

// runResetPassword 交互式重置密码。
//
// 🔴 密码**从终端读**，不做成命令行参数：
// 参数会进 shell 历史、进程列表（ps 能看到）、以及容器的事件记录 ——
// 而重置密码的场景本身往往就是"怀疑凭据泄露了"。
//
// ⚠️ 不做静默模式（读 stdin 管道）也是刻意的：那等于又开了一条能写进脚本的路，
// 而写进脚本就会被提交到某个仓库里。真要自动化，应该走别的机制。
func runResetPassword(ctx context.Context, st *store.Store, username string) error {
	fmt.Printf("重置用户 %q 的密码。\n", username)

	pw1, err := readPasswordTwice()
	if err != nil {
		return err
	}
	if err := st.ResetPassword(ctx, username, pw1); err != nil {
		return err
	}
	fmt.Printf("✓ 已重置 %q 的密码，该账号的所有会话已失效，需要重新登录。\n", username)
	return nil
}

func readPasswordTwice() (string, error) {
	fmt.Print("新密码: ")
	p1, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("读取密码失败（这个命令需要在终端里运行）: %w", err)
	}
	fmt.Print("再输一次: ")
	p2, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", err
	}
	if string(p1) != string(p2) {
		return "", fmt.Errorf("两次输入不一致")
	}
	// ⚠️ 长度下限要有：这个口子直通超管，弱口令的代价比别处大
	if len(p1) < 8 {
		return "", fmt.Errorf("密码至少 8 位")
	}
	return string(p1), nil
}
