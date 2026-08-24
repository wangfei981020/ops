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
	logx.J("boot", "shutdown", nil)
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = h.Shutdown(sctx)
}

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
