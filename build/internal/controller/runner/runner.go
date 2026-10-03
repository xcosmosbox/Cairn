// Package runner 把现有 pipeline / incremental / rebalance 编排器封装为类型化
// PipelineRunner 接口（产品化 Prompt §5.1、§10）。
//
// Controller 直接调用本包获得 typed report，不 shell out 到 cairn-ingest 解析人类日志。
// 提供：
//   - Runner：真实实现（调用 pipeline/incremental/rebalance 编排器 + LLM client）。
//   - FakeRunner：返回 canned 结果（供控制器 reconcile 流程测试，无需真实 LLM）。
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/pipeline"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/storage"
)

// BuildResult 是一轮构建（全量/增量）的类型化结果。
type BuildResult struct {
	DBPath             string          `json:"-"`
	NodesCreated       int             `json:"nodes_created"`
	EdgesCreated       int             `json:"edges_created"`
	DocsRewritten      int             `json:"docs_rewritten"`
	Skipped            []string        `json:"skipped,omitempty"`
	Warnings           []string        `json:"warnings,omitempty"`
	TriggeredRebalance bool            `json:"triggered_rebalance"`
	HasWriteback       bool            `json:"has_writeback"`
	Report             json.RawMessage `json:"report,omitempty"`
}

// RebalanceDecision 是 rebalance --check 的类型化结果（§10）。
type RebalanceDecision struct {
	Triggered      bool     `json:"triggered"`
	Reasons        []string `json:"reasons,omitempty"`
	NoOp           bool     `json:"no_op"`
	QModularity    float64  `json:"q_modularity"`
	SingletonRatio float64  `json:"singleton_ratio"`
	EdgeNodeRatio  float64  `json:"edge_node_ratio"`
}

// ValidationReport 是候选 DB 的验证结果（§7.2 稳定判据）。
type ValidationReport struct {
	OK        bool     `json:"ok"`
	NodeCount int      `json:"node_count"`
	EdgeCount int      `json:"edge_count"`
	Checks    []string `json:"checks,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

// FullRequest 是全量构建输入。
type FullRequest struct {
	RepoPath           string
	DBPath             string
	MinConfidence      float64
	RepositoryIdentity string
}

// IncrementalRequest 是增量构建输入。
type IncrementalRequest struct {
	RepoPath           string
	DBPath             string
	MinConfidence      float64
	RepositoryIdentity string
}

// RebalanceRequest 是重整输入。
type RebalanceRequest struct {
	RepoPath           string
	DBPath             string
	Force              bool
	RepositoryIdentity string
}

// ValidateRequest 是验证输入。
type ValidateRequest struct {
	DBPath   string
	RepoPath string
}

// PipelineRunner 是控制器调用的窄接口（§5.1）。
type PipelineRunner interface {
	Full(ctx context.Context, req FullRequest) (BuildResult, error)
	Incremental(ctx context.Context, req IncrementalRequest) (BuildResult, error)
	RebalanceCheck(ctx context.Context, req RebalanceRequest) (RebalanceDecision, error)
	Rebalance(ctx context.Context, req RebalanceRequest) (BuildResult, error)
	Validate(ctx context.Context, req ValidateRequest) (ValidationReport, error)
}

// ─── 真实 Runner ────────────────────────────────────────────────

// Runner 是真实 PipelineRunner 实现，调用现有 pipeline/incremental/rebalance 编排器。
type Runner struct {
	client       llm.Client
	maxTokens    int
	maxRetries   int
	maxRollbacks int
	recallK      int
	stageTimeout time.Duration // 从 config.llm.timeout 传入，统一用于每个 pipeline 阶段
}

// NewRunner 从 dkconfig.LLMConfig 构造真实 Runner。
// 需要 LLM API key（构建/增量/重整需真实 LLM）。
func NewRunner(cfg dkconfig.LLMConfig) (*Runner, error) {
	client, err := llm.NewOpenAICompatClient(llm.Config{
		Provider:   cfg.Provider,
		APIKeyEnv:  cfg.APIKeyEnv,
		Model:      cfg.Model,
		MaxTokens:  cfg.MaxTokens,
		Endpoint:   cfg.Endpoint,
		Timeout:    time.Duration(cfg.Timeout).String(),
		MaxRetries: cfg.MaxRetries,
	})
	if err != nil {
		return nil, fmt.Errorf("runner: 创建 LLM client: %w", err)
	}
	return &Runner{
		client:       client,
		maxTokens:    cfg.MaxTokens,
		maxRetries:   cfg.MaxRetries,
		maxRollbacks: 3,
		recallK:      5,
		stageTimeout: time.Duration(cfg.Timeout),
	}, nil
}

// NewRunnerWithClient 用给定 LLM client 构造 Runner（测试/自定义用）。
func NewRunnerWithClient(client llm.Client, cfg dkconfig.LLMConfig) *Runner {
	return &Runner{
		client:       client,
		maxTokens:    cfg.MaxTokens,
		maxRetries:   cfg.MaxRetries,
		maxRollbacks: 3,
		recallK:      5,
		stageTimeout: time.Duration(cfg.Timeout),
	}
}

func (r *Runner) Full(ctx context.Context, req FullRequest) (BuildResult, error) {
	orch, err := pipeline.NewOrchestrator(pipeline.Options{
		Client:             r.client,
		MaxTokens:          r.maxTokens,
		MaxRetries:         r.maxRetries,
		MaxRollbacks:       r.maxRollbacks,
		MinConfidence:      req.MinConfidence,
		RepositoryIdentity: req.RepositoryIdentity,
		StageTimeout:       r.stageTimeout,
	})
	if err != nil {
		return BuildResult{}, err
	}
	rpt, err := orch.RunFullRebuild(ctx, req.RepoPath, req.DBPath)
	if err != nil {
		return BuildResult{}, err
	}
	return pipelineToBuildResult(rpt, req.DBPath), nil
}

func (r *Runner) Incremental(ctx context.Context, req IncrementalRequest) (BuildResult, error) {
	orch, err := incremental.NewIncrementalOrchestrator(incremental.Options{
		Client:             r.client,
		MaxTokens:          r.maxTokens,
		MaxRetries:         r.maxRetries,
		MaxRollbacks:       r.maxRollbacks,
		MinConfidence:      req.MinConfidence,
		RepositoryIdentity: req.RepositoryIdentity,
		RecallK:            r.recallK,
	})
	if err != nil {
		return BuildResult{}, err
	}
	rpt, err := orch.Run(ctx, req.RepoPath, req.DBPath)
	if err != nil {
		return BuildResult{}, err
	}
	return incrementalToBuildResult(rpt, req.DBPath), nil
}

func (r *Runner) RebalanceCheck(ctx context.Context, req RebalanceRequest) (RebalanceDecision, error) {
	orch, err := incremental.NewRebalanceOrchestrator(r.client, r.maxTokens)
	if err != nil {
		return RebalanceDecision{}, err
	}
	rpt, err := orch.Run(ctx, req.RepoPath, req.DBPath, incremental.RebalanceRunOpts{CheckOnly: true, RepositoryIdentity: req.RepositoryIdentity})
	if err != nil {
		return RebalanceDecision{}, err
	}
	return RebalanceDecision{
		Triggered:      rpt.Triggered,
		Reasons:        rpt.TriggerReasons,
		NoOp:           rpt.NoOp,
		QModularity:    rpt.LouvainQ,
		SingletonRatio: rpt.Metrics.SingletonRatio,
		EdgeNodeRatio:  rpt.Metrics.EdgeNodeRatio,
	}, nil
}

func (r *Runner) Rebalance(ctx context.Context, req RebalanceRequest) (BuildResult, error) {
	orch, err := incremental.NewRebalanceOrchestrator(r.client, r.maxTokens)
	if err != nil {
		return BuildResult{}, err
	}
	rpt, err := orch.Run(ctx, req.RepoPath, req.DBPath, incremental.RebalanceRunOpts{Force: req.Force, RepositoryIdentity: req.RepositoryIdentity})
	if err != nil {
		return BuildResult{}, err
	}
	res := BuildResult{
		DBPath:             req.DBPath,
		TriggeredRebalance: rpt.Triggered,
	}
	if rpt.NoOp {
		res.Warnings = append(res.Warnings, rpt.NoOpReason)
	}
	return res, nil
}

func (r *Runner) Validate(ctx context.Context, req ValidateRequest) (ValidationReport, error) {
	return validateCandidate(ctx, req)
}

// validateDB 用只读连接校验候选 DB：quick_check + 节点/边计数 + schema 存在性。
func validateDB(dbPath string) (ValidationReport, error) {
	vr := ValidationReport{}
	if _, err := os.Stat(dbPath); err != nil {
		return vr, fmt.Errorf("runner: db 不存在 %s: %w", dbPath, err)
	}
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		return vr, fmt.Errorf("runner: 打开 db 只读: %w", err)
	}
	defer db.Close()
	conn := db.Conn()
	// quick_check。
	var qc string
	if err := conn.QueryRow("PRAGMA quick_check").Scan(&qc); err != nil {
		vr.Errors = append(vr.Errors, "quick_check: "+err.Error())
	} else if qc != "ok" {
		vr.Errors = append(vr.Errors, "quick_check: "+qc)
	}
	vr.Checks = append(vr.Checks, "quick_check")
	// schema：nodes/edges 表存在。
	var nodes, edges int
	if err := conn.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodes); err != nil {
		vr.Errors = append(vr.Errors, "node count: "+err.Error())
	}
	if err := conn.QueryRow("SELECT COUNT(*) FROM edges").Scan(&edges); err != nil {
		vr.Errors = append(vr.Errors, "edge count: "+err.Error())
	}
	vr.Checks = append(vr.Checks, "schema", "node_count", "edge_count")
	vr.NodeCount = nodes
	vr.EdgeCount = edges
	if len(vr.Errors) == 0 {
		vr.OK = true
	}
	return vr, nil
}

// ─── 报告转换 ────────────────────────────────────────────────────

func pipelineToBuildResult(rpt *pipeline.RunReport, dbPath string) BuildResult {
	res := BuildResult{DBPath: dbPath, DocsRewritten: rpt.DocsRewritten}
	if rpt.Ingest != nil {
		res.NodesCreated = rpt.Ingest.NodesInserted
		res.EdgesCreated = rpt.Ingest.EdgesInserted
	}
	for _, s := range rpt.Skipped {
		res.Skipped = append(res.Skipped, s.Reason)
	}
	if rpt.Writeback != nil {
		res.HasWriteback = rpt.Writeback.DocsWritten > 0
	}
	if data, err := json.Marshal(rpt); err == nil {
		res.Report = data
	}
	return res
}

func incrementalToBuildResult(rpt *incremental.RunReport, dbPath string) BuildResult {
	res := BuildResult{DBPath: dbPath, NodesCreated: rpt.NodesCreated, DocsRewritten: rpt.DocsRewritten}
	res.HasWriteback = rpt.DocsRewritten > 0
	res.Warnings = rpt.Warnings
	if data, err := json.Marshal(rpt); err == nil {
		res.Report = data
	}
	return res
}

// ─── FakeRunner（测试用）──────────────────────────────────────

// FakeRunner 返回 canned 结果，供控制器 reconcile 流程测试，无需真实 LLM。
// 它会用 storage.NewDB 真实创建一个空 KG 库（使 Validate 通过），但不调 LLM。
type FakeRunner struct {
	// OnFull/OnIncremental 可被测试覆盖以定制行为。
	OnFull        func(ctx context.Context, req FullRequest) (BuildResult, error)
	OnIncremental func(ctx context.Context, req IncrementalRequest) (BuildResult, error)
}

func (f *FakeRunner) Full(ctx context.Context, req FullRequest) (BuildResult, error) {
	if f.OnFull != nil {
		return f.OnFull(ctx, req)
	}
	// 创建空 KG 库。
	if err := ensureDB(req.DBPath); err != nil {
		return BuildResult{}, err
	}
	return BuildResult{
		DBPath:        req.DBPath,
		NodesCreated:  3,
		EdgesCreated:  2,
		DocsRewritten: 1,
		HasWriteback:  true,
	}, nil
}

func (f *FakeRunner) Incremental(ctx context.Context, req IncrementalRequest) (BuildResult, error) {
	if f.OnIncremental != nil {
		return f.OnIncremental(ctx, req)
	}
	return BuildResult{
		DBPath:       req.DBPath,
		HasWriteback: false, // 无回写差异（收敛固定点）。
	}, nil
}

func (f *FakeRunner) RebalanceCheck(ctx context.Context, req RebalanceRequest) (RebalanceDecision, error) {
	return RebalanceDecision{Triggered: false, NoOp: true}, nil
}

func (f *FakeRunner) Rebalance(ctx context.Context, req RebalanceRequest) (BuildResult, error) {
	return BuildResult{DBPath: req.DBPath}, nil
}

func (f *FakeRunner) Validate(ctx context.Context, req ValidateRequest) (ValidationReport, error) {
	return validateCandidate(ctx, req)
}

// ensureDB 创建一个空 KG 库（若不存在）。
func ensureDB(dbPath string) error {
	if _, err := os.Stat(dbPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		return err
	}
	return db.Close()
}
