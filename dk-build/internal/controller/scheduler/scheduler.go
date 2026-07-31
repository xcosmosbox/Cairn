// Package scheduler 实现 dkd 的轮询调度与 HTTP API（产品化 Prompt §3、§13）。
//
// 轮询是最终一致性的基础能力，webhook 只是低延迟唤醒信号。即使启用 webhook，
// daemon 仍周期性核对 Source branch、Source PR、Catalog PR 和 Release 状态，
// 确保 webhook 丢失/重复/乱序后系统仍能自行收敛（§3）。
package scheduler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dkconfig"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/controller/githubapp"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/controller/reconcile"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/controller/store"
)

// Scheduler 周期性推进活跃 run 并为有新 SHA 的 repo 创建新 run。
type Scheduler struct {
	cfg     *dkconfig.Config
	store   *store.Store
	forge   githubapp.Forge
	recon   *reconcile.Reconciler
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	tickMu  sync.Mutex // 防止并发 tick（Wakeup 去重）
}

// New 创建 Scheduler。
func New(cfg *dkconfig.Config, st *store.Store, forge githubapp.Forge, recon *reconcile.Reconciler) *Scheduler {
	return &Scheduler{cfg: cfg, store: st, forge: forge, recon: recon}
}

// Start 启动后台轮询循环（非阻塞）。
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()
	go s.loop(ctx)
}

// Stop 停止轮询。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.running = false
}

func (s *Scheduler) loop(ctx context.Context) {
	pollInterval := s.cfg.Service.PollInterval.Std()
	if pollInterval <= 0 {
		pollInterval = 2 * time.Minute
	}
	// 快 ticker：频繁推进活跃 run（包括等待 PR 合并的 run）。
	// 慢 ticker：检查 source repo 是否有新 SHA。
	fastTicker := time.NewTicker(30 * time.Second)
	defer fastTicker.Stop()
	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()
	// 启动立即跑一轮。
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-fastTicker.C:
			s.stepActiveRuns(ctx)
		case <-pollTicker.C:
			s.tick(ctx)
		}
	}
}

// stepActiveRuns 只推进活跃 run（不检查新 SHA），用快 ticker 频繁调用。
func (s *Scheduler) stepActiveRuns(ctx context.Context) {
	// 防止并发 tick。
	if !s.tickMu.TryLock() {
		return
	}
	defer s.tickMu.Unlock()
	runs, err := s.store.ActiveRuns(ctx)
	if err != nil {
		log.Printf("[scheduler] 读取活跃 run 失败: %v", err)
		return
	}
	for _, run := range runs {
		s.stepRunWithTimeout(ctx, run.RunID)
	}
}

// tick 执行一轮完整调度：推进活跃 run + 为有新 SHA 的 repo 创建新 run。
func (s *Scheduler) tick(ctx context.Context) {
	// 防止并发 tick（Wakeup 去重）。
	if !s.tickMu.TryLock() {
		return
	}
	defer s.tickMu.Unlock()
	// 1. 推进所有活跃 run，持续 Step 直到无法继续（等待外部 / 终态）。
	runs, err := s.store.ActiveRuns(ctx)
	if err != nil {
		log.Printf("[scheduler] 读取活跃 run 失败: %v", err)
	} else {
		for _, run := range runs {
			s.stepRunWithTimeout(ctx, run.RunID)
		}
	}
	// 2. 为每个 repo 检查新 SHA（若无活跃 run）。
	repos, err := s.store.ListRepos(ctx)
	if err != nil {
		log.Printf("[scheduler] 读取 repos 失败: %v", err)
		return
	}
	for _, repo := range repos {
		if !repo.Enabled {
			continue
		}
		active, _ := s.hasActiveRun(ctx, repo.ID)
		if active {
			continue
		}
		sha, err := s.forge.GetBranchSHA(ctx, repo.GitHubOwner, repo.GitHubName, repo.Branch)
		if err != nil {
			log.Printf("[scheduler] 获取 %s SHA 失败: %v", repo.ID, err)
			continue
		}
		// 仅当 source SHA 变化时创建新 run（首次 last_seen 为空自然满足）。
		// 不再因 last_stable_bundle_digest 为空而反复建 run——否则 stable 从未成功时，
		// 每个 poll 周期都会新建全量 run（run 一旦进入非活跃态 hasActiveRun 就挡不住），
		// 形成全量重建风暴。失败/阻塞的 run 应通过 /retry 或人工 unblock 恢复。
		if sha != repo.LastSeenSourceSHA {
			if _, err := s.recon.CreateRun(ctx, repo.ID); err != nil {
				log.Printf("[scheduler] 创建 run for %s 失败: %v", repo.ID, err)
			} else {
				log.Printf("[scheduler] 为 %s 创建新 run（SHA: %s）", repo.ID, sha[:12])
			}
		}
	}
}

// stepRunWithTimeout 持续推进一个 run，每步带超时（防止 LLM/git 卡死阻塞调度）。
// 超时统一使用 config.llm.timeout——与 pipeline 各阶段的 stage timeout 共用同一配置值。
func (s *Scheduler) stepRunWithTimeout(ctx context.Context, runID string) {
	stepTimeout := s.cfg.LLM.Timeout.Std()
	if stepTimeout <= 0 {
		stepTimeout = 30 * time.Minute // 兜底
	}
	for i := 0; i < 100; i++ {
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
		res, err := s.recon.Step(stepCtx, runID)
		cancel()
		if err != nil {
			log.Printf("[scheduler] 推进 run %s 失败: %v", runID, err)
			return
		}
		if !res.Advanced {
			return // 到达等待状态或终态
		}
		log.Printf("[scheduler] run %s: %s → %s (%s)", runID, res.From, res.To, res.Message)
	}
}

func (s *Scheduler) hasActiveRun(ctx context.Context, repoID string) (bool, error) {
	runs, err := s.store.ListRuns(ctx, repoID, 10)
	if err != nil {
		return false, err
	}
	for _, r := range runs {
		if r.State.IsActive() {
			return true, nil
		}
	}
	return false, nil
}

// Wakeup 立即触发一轮调度（webhook 唤醒用）。
func (s *Scheduler) Wakeup(ctx context.Context) {
	go s.tick(ctx)
}

// ─── HTTP API（§13）────────────────────────────────────────────

// HTTPServer 是管理 API + webhook 入口。
type HTTPServer struct {
	cfg        *dkconfig.Config
	store      *store.Store
	sched      *Scheduler
	recon      *reconcile.Reconciler
	webhookSecret string
	adminToken    string
}

// NewHTTPServer 创建 HTTP server。
func NewHTTPServer(cfg *dkconfig.Config, st *store.Store, sched *Scheduler, recon *reconcile.Reconciler) *HTTPServer {
	h := &HTTPServer{cfg: cfg, store: st, sched: sched, recon: recon}
	h.webhookSecret = dkconfig.ResolveSecret(cfg.GitHub.WebhookSecretEnv)
	h.adminToken = dkconfig.ResolveSecret(cfg.Service.AdminTokenEnv)
	return h
}

// Routes 返回 http.Handler（所有路由注册）。
func (h *HTTPServer) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handleHealthz)
	mux.HandleFunc("/readyz", h.handleReadyz)
	mux.HandleFunc("/api/v1/repos", h.handleRepos)
	mux.HandleFunc("/api/v1/repos/", h.handleRepoActions)
	mux.HandleFunc("/api/v1/runs", h.handleRuns)
	mux.HandleFunc("/api/v1/runs/", h.handleRunActions)
	mux.HandleFunc("/webhooks/github", h.handleWebhook)
	return h.authMiddleware(mux)
}

func (h *HTTPServer) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// healthz/readyz/webhook 不需要 admin token。
		path := r.URL.Path
		if path == "/healthz" || path == "/readyz" || path == "/webhooks/github" {
			next.ServeHTTP(w, r)
			return
		}
		// localhost 不强制 token（默认只监听 localhost）。
		if h.isLocalhost(r) && h.adminToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		// 非 localhost 或配置了 token：校验 bearer。
		if h.adminToken != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+h.adminToken {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (h *HTTPServer) isLocalhost(r *http.Request) bool {
	host := r.Host
	return strings.HasPrefix(host, "127.0.0.1") || strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "[::1]")
}

func (h *HTTPServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *HTTPServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := h.store.DB().Ping(); err != nil {
		http.Error(w, `{"status":"not_ready"}`, http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte(`{"status":"ready"}`))
}

func (h *HTTPServer) handleRepos(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repos, err := h.store.ListRepos(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, repos)
}

func (h *HTTPServer) handleRepoActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/repos/"), "/")
	if len(parts) < 1 {
		http.NotFound(w, r)
		return
	}
	repoID := parts[0]
	if len(parts) == 1 {
		// GET /api/v1/repos/{id}
		repo, err := h.store.GetRepo(ctx, repoID)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, repo)
		return
	}
	if len(parts) == 2 && parts[1] == "reconcile" && r.Method == "POST" {
		// 创建并立即推进一个 run。
		runID, err := h.recon.CreateRun(ctx, repoID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		h.sched.Wakeup(ctx)
		writeJSON(w, map[string]string{"run_id": runID})
		return
	}
	http.NotFound(w, r)
}

func (h *HTTPServer) handleRuns(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repoID := r.URL.Query().Get("repo")
	runs, err := h.store.ListRuns(ctx, repoID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, runs)
}

func (h *HTTPServer) handleRunActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/runs/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	runID := parts[0]
	action := parts[1]
	switch action {
	case "retry":
		// 把 Blocked/Failed 的 run 回到 Idle。
		if err := h.store.TransitionRun(ctx, runID, "Idle", "manual retry"); err != nil {
			// Idle 可能不合法（从 Blocked）；用 FailedRetryable 中转。
			h.store.TransitionRun(ctx, runID, "FailedRetryable", "manual retry")
			h.store.TransitionRun(ctx, runID, "Idle", "manual retry")
		}
		h.sched.Wakeup(ctx)
		writeJSON(w, map[string]string{"status": "retrying"})
	case "unblock":
		h.store.TransitionRun(ctx, runID, "Idle", "manual unblock")
		h.sched.Wakeup(ctx)
		writeJSON(w, map[string]string{"status": "unblocked"})
	default:
		http.NotFound(w, r)
	}
}

func (h *HTTPServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	// 签名校验（§4.1）。
	if h.webhookSecret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !verifyWebhook(h.webhookSecret, body, sig) {
			http.Error(w, `{"error":"invalid signature"}`, http.StatusUnauthorized)
			return
		}
	}
	deliveryID := r.Header.Get("X-GitHub-Delivery")
	eventType := r.Header.Get("X-GitHub-Event")
	// delivery 去重（§6 webhook_deliveries）。
	dup, err := h.store.ClaimDelivery(ctx, deliveryID, eventType, "")
	if err != nil {
		http.Error(w, "dedup error", http.StatusInternalServerError)
		return
	}
	if dup {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"duplicate"}`))
		return
	}
	// 快速返回，把工作交给调度器。
	h.sched.Wakeup(ctx)
	h.store.MarkDeliveryProcessed(ctx, deliveryID, "queued")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"queued"}`))
}

// ─── 工具 ────────────────────────────────────────────────────────

func verifyWebhook(secret string, body []byte, sigHeader string) bool {
	if sigHeader == "" {
		return false
	}
	prefix := "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}
	got, err := hex.DecodeString(sigHeader[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	return hmac.Equal(got, want)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// RunOnce 跑一轮调度后返回（dkd run --once 用）。
func (s *Scheduler) RunOnce(ctx context.Context) {
	s.tick(ctx)
	// 持续推进直到所有 run 到终态或等待外部（PR）。
	for {
		runs, err := s.store.ActiveRuns(ctx)
		if err != nil || len(runs) == 0 {
			break
		}
		anyAdvanced := false
		for _, run := range runs {
			res, err := s.recon.Step(ctx, run.RunID)
			if err != nil {
				log.Printf("[once] run %s: %v", run.RunID, err)
			}
			if err == nil && res.Advanced {
				anyAdvanced = true
			}
		}
		if !anyAdvanced {
			break // 剩余 run 都在等待外部（PR 合并）。
		}
	}
	fmt.Println("[once] 本轮调度完成（剩余 run 等待外部合并）")
}
