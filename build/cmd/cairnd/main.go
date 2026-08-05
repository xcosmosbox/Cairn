// Command cairnd 是 DK Controller daemon 入口（产品化 Prompt §13）。
//
// 用法：
//   cairnd daemon --config config.yaml            # 常驻轮询 + HTTP API
//   cairnd run --once --config config.yaml        # 跑一轮 reconcile 后退出
//   cairnd reconcile --config config.yaml --repo payment  # 为指定 repo 创建并推进一个 run
//   cairnd validate-config --config config.yaml   # 校验配置后退出
//
// 本地 fake 模式：加 --fake 使用 FakeForge（无需 GitHub 凭证）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xcosmosbox/cairn/core/dkconfig"
	coregh "github.com/xcosmosbox/cairn/core/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/fakeforge"
	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/publisher"
	"github.com/xcosmosbox/cairn/build/internal/controller/reconcile"
	"github.com/xcosmosbox/cairn/build/internal/controller/runner"
	"github.com/xcosmosbox/cairn/build/internal/controller/scheduler"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/controller/workspace"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	sub := os.Args[1]
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	configPath := fs.String("config", "", "配置文件路径 (YAML)")
	repoID := fs.String("repo", "", "reconcile 子命令：指定 repo id")
	fake := fs.Bool("fake", false, "使用 FakeForge（本地测试，无需 GitHub 凭证）")
	_ = fs.Parse(os.Args[2:])

	if *configPath == "" && sub != "help" {
		*configPath = os.Getenv("CAIRND_CONFIG")
	}
	if *configPath == "" && sub != "help" {
		fmt.Fprintln(os.Stderr, "错误：--config 必填（或设置 CAIRND_CONFIG 环境变量）")
		usage()
	}

	switch sub {
	case "validate-config":
		cfg, err := dkconfig.Load(*configPath)
		mustNoErr(err)
		fmt.Printf("配置校验通过：%d 个 repo\n", len(cfg.Repos))
		for _, r := range cfg.Repos {
			fmt.Printf("  - %s: %s/%s @ %s\n", r.ID, r.Owner, r.Name, r.Branch)
		}
		return
	case "daemon", "run", "reconcile":
	default:
		usage()
	}

	cfg, err := dkconfig.Load(*configPath)
	mustNoErr(err)

	// 确保所有目录存在（必须在 store.Open 之前，否则 state_db 父目录不存在 → CANTOPEN）。
	os.MkdirAll(filepath.Dir(cfg.Service.StateDB), 0o755)
	os.MkdirAll(cfg.Service.WorkspacesDir, 0o755)
	os.MkdirAll(cfg.Service.CacheDir, 0o755)
	os.MkdirAll(cfg.Service.BundleDir, 0o755)

	// 打开 store。
	st, err := store.Open(cfg.Service.StateDB)
	mustNoErr(err)
	defer st.Close()

	// 同步 repos 到 store。
	for _, r := range cfg.Repos {
		st.UpsertRepo(context.Background(), toManagedRepo(r))
	}

	// 构造 Forge。
	forge := buildForge(cfg, *fake, cfg.Service.WorkspacesDir)

	// 构造组件。
	ws := workspace.New(cfg.Service.WorkspacesDir)
	var pipelineRunner runner.PipelineRunner
	if *fake {
		pipelineRunner = &runner.FakeRunner{}
	} else {
		rr, err := runner.NewRunner(cfg.LLM)
		mustNoErr(err)
		pipelineRunner = rr
	}
	catalogRef := githubapp.RepoRef{Owner: parseOwner(cfg.Catalog.Repo), Name: parseName(cfg.Catalog.Repo)}
	pub := publisher.New(forge, ws, catalogRef)
	// 可选：LLM PR 摘要器（任一 repo 启用 pr_summary_llm 时生效）。
	var prSummarizer runner.PRSummarizer
	for _, r := range cfg.Repos {
		if r.Rewrite.PRSummaryLLM {
			if !*fake {
				client, err := llm.NewOpenAICompatClient(llm.Config{
					Provider: cfg.LLM.Provider, APIKeyEnv: cfg.LLM.APIKeyEnv,
					Model: cfg.LLM.Model, MaxTokens: 2000,
					Endpoint: cfg.LLM.Endpoint, Timeout: "60s", MaxRetries: 2,
				})
				if err == nil {
					prSummarizer = runner.NewLLMSummarizer(client)
				}
			} else {
				prSummarizer = &runner.FakePRSummarizer{}
			}
			break
		}
	}
	recon := reconcile.New(reconcile.Options{
		Config: cfg, Store: st, Forge: forge, WS: ws, Runner: pipelineRunner, Publisher: pub,
		PRSummarizer: prSummarizer, HolderID: hostname(),
	})
	sched := scheduler.New(cfg, st, forge, recon)

	switch sub {
	case "daemon":
		runDaemon(cfg, st, sched, recon)
	case "run":
		if *fake || cfg.Service.Mode == dkconfig.ModeOnce {
			// --once 模式：用 RunOnce。
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		sched.RunOnce(ctx)
	case "reconcile":
		if *repoID == "" {
			fmt.Fprintln(os.Stderr, "错误：reconcile 需要 --repo")
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		runID, err := recon.CreateRun(ctx, *repoID)
		mustNoErr(err)
		// 持续推进直到终态、等待外部、超时或步数耗尽。
		for i := 0; i < 500; i++ {
			res, err := recon.Step(ctx, runID)
			if err != nil {
				log.Printf("reconcile: %v", err)
			}
			if !res.Advanced {
				break
			}
			run, _ := st.GetRun(ctx, runID)
			if run.State.IsTerminal() {
				break
			}
			if ctx.Err() != nil {
				log.Printf("reconcile: 超时或取消")
				break
			}
		}
		run, _ := st.GetRun(ctx, runID)
		fmt.Printf("run %s 最终状态: %s\n", runID, run.State)
	}
}

func runDaemon(cfg *dkconfig.Config, st *store.Store, sched *scheduler.Scheduler, recon *reconcile.Reconciler) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动调度器。
	sched.Start(ctx)
	defer sched.Stop()

	// 启动 HTTP server。
	httpServer := scheduler.NewHTTPServer(cfg, st, sched, recon)
	srv := &http.Server{
		Addr:    cfg.Service.ListenAddr,
		Handler: httpServer.Routes(),
	}
	go func() {
		log.Printf("[cairnd] HTTP 监听 %s", cfg.Service.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[cairnd] HTTP server: %v", err)
		}
	}()

	// 信号处理（§17：SIGTERM 停止新任务、持久化状态）。
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("[cairnd] 收到信号 %s，正在关闭...", sig)
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)
	log.Printf("[cairnd] 已关闭")
}

func buildForge(cfg *dkconfig.Config, fake bool, wsDir string) githubapp.Forge {
	if fake {
		return fakeforge.New()
	}
	// 真实 GitHub App。
	key, err := cfg.GitHub.ResolvePrivateKey()
	mustNoErr(err)
	auth, err := coregh.NewAppAuth(cfg.GitHub.AppID, key, cfg.GitHub.APIBaseURL)
	mustNoErr(err)
	// installation ID 需要先解析（用第一个 repo）。
	if len(cfg.Repos) == 0 {
		log.Fatal("无 repo 配置")
	}
	r := cfg.Repos[0]
	instID, err := auth.ResolveInstallation(context.Background(), r.Owner, r.Name)
	mustNoErr(err)
	return githubapp.NewHTTPForge(auth, instID)
}

func toManagedRepo(r dkconfig.RepoConfig) (m model.ManagedRepo) {
	return model.ManagedRepo{
		ID: r.ID, GitHubOwner: r.Owner, GitHubName: r.Name,
		SourceURL: r.URL, Branch: r.Branch, KGGroup: r.KGGroup, Enabled: true,
		CreatedAt: time.Now().UTC(),
	}
}

func hostname() string {
	h, _ := os.Hostname()
	if h == "" {
		return "cairnd-unknown"
	}
	return h
}

func parseOwner(slug string) string {
	for i, c := range slug {
		if c == '/' {
			return slug[:i]
		}
	}
	return slug
}
func parseName(slug string) string {
	for i, c := range slug {
		if c == '/' {
			return slug[i+1:]
		}
	}
	return slug
}

func mustNoErr(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "cairnd: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `用法: cairnd <子命令> [flags]

子命令:
  daemon          常驻轮询 + HTTP API
  run --once      跑一轮 reconcile 后退出
  reconcile       为指定 repo 创建并推进一个 run
  validate-config 校验配置后退出

通用 flags:
  --config <path>   配置文件路径（或 CAIRND_CONFIG 环境变量）
  --fake            使用 FakeForge（本地测试）`)
	os.Exit(2)
}
