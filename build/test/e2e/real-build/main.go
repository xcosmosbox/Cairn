// real-build runs the actual Cairn orchestrator against the nicedata fixture.
// The only replay is a verified, previously accepted annotation/Gate B checkpoint.
// Extraction, repair, descriptions, relations and file slugs use the real provider.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	buildmeta "github.com/xcosmosbox/cairn/build/internal"
	"github.com/xcosmosbox/cairn/build/internal/controller/publisher"
	"github.com/xcosmosbox/cairn/build/internal/controller/runner"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/pipeline"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/evolve"
	"github.com/xcosmosbox/cairn/core/storage"
)

type boundedClient struct {
	next llm.Client
	sem  chan struct{}
}

func (c *boundedClient) ProviderName() string { return c.next.ProviderName() }
func (c *boundedClient) Complete(ctx context.Context, request llm.CompleteRequest) (*llm.CompleteResponse, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()
	return c.next.Complete(ctx, request)
}

type denyNetwork struct{}

func (denyNetwork) ProviderName() string { return "preflight-only" }
func (denyNetwork) Complete(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
	return nil, errors.New("preflight-only: network calls are disabled")
}

func save(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0644)
}

func sourceRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("compiled source path is unavailable")
	}
	root := filepath.Dir(file)
	for root != filepath.Dir(root) {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root, nil
		}
		root = filepath.Dir(root)
	}
	return "", errors.New("cannot locate runner module source")
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	repo := flag.String("repo", "", "clean source worktree at the accepted fixture commit")
	out := flag.String("out", "", "new, empty output directory")
	acceptedDump := flag.String("accepted-dump", "", "original stages containing accepted annotations and Gate B evidence")
	historicalCode := flag.String("historical-code-root", "", "original builder source for reuse-compatibility verification")
	allowPaid := flag.Bool("allow-paid", false, "permit new real provider requests after checkpoint validation")
	materializeFrom := flag.String("materialize-from", "", "completed real LLM run to materialize without any new LLM calls")
	checkpointManifest := flag.String("checkpoint-manifest", "", "reviewed SHA-256 manifest for the completed LLM run")
	checkpointManifestSHA := flag.String("checkpoint-manifest-sha256", "", "explicit SHA-256 pin for that manifest")
	maxTokens := flag.Int("max-tokens", dkconfig.DeepSeekMaxOutputTokens, "output budget for fresh extraction and downstream stages")
	concurrency := flag.Int("concurrency", 8, "maximum simultaneous real provider calls")
	requestTimeout := flag.Duration("request-timeout", 30*time.Minute, "timeout per provider request")
	stageTimeout := flag.Duration("stage-timeout", 30*time.Minute, "timeout per pipeline stage")
	runTimeout := flag.Duration("run-timeout", 90*time.Minute, "total build timeout")
	flag.Parse()
	if *repo == "" || *out == "" || *acceptedDump == "" || *historicalCode == "" {
		return errors.New("--repo, --out, --accepted-dump and --historical-code-root are required")
	}
	if *concurrency < 1 || *maxTokens < 1 || *maxTokens > dkconfig.DeepSeekMaxOutputTokens || *requestTimeout <= 0 || *stageTimeout <= 0 || *runTimeout <= 0 {
		return errors.New("invalid concurrency, token budget or timeout")
	}
	if entries, err := os.ReadDir(*out); err == nil && len(entries) != 0 {
		return errors.New("output directory must be empty; refusing to overwrite a previous run")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		return err
	}
	codeRoot, err := sourceRoot()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *runTimeout)
	defer cancel()
	if *materializeFrom != "" {
		if *allowPaid {
			return errors.New("--materialize-from cannot be combined with --allow-paid")
		}
		return runMaterialize(ctx, *repo, *out, *acceptedDump, *historicalCode, codeRoot, *materializeFrom, *checkpointManifest, *checkpointManifestSHA)
	}
	checkpoint, err := newAcceptedCheckpointClient(ctx, *repo, *acceptedDump, *historicalCode, codeRoot, denyNetwork{})
	if err != nil {
		return err
	}
	proofPath := filepath.Join(*out, "accepted-checkpoint-proof.json")
	if err := save(proofPath, checkpoint.proof); err != nil {
		return err
	}
	proofRaw, err := os.ReadFile(proofPath)
	if err != nil {
		return err
	}
	driverDigest, err := driverSourceDigest(filepath.Join(codeRoot, "build/test/e2e/real-build"))
	if err != nil {
		return err
	}
	cfg := llm.Config{Provider: "openai_compatible", APIKeyEnv: "CAIRN_LLM_API_KEY", Model: dkconfig.DeepSeekFlashModel, Endpoint: dkconfig.DeepSeekEndpoint, MaxTokens: *maxTokens, Timeout: requestTimeout.String(), MaxRetries: 2}
	effective := map[string]any{
		"provider": cfg.Provider, "model": cfg.Model, "endpoint": cfg.Endpoint,
		"max_tokens": cfg.MaxTokens, "max_retries": cfg.MaxRetries,
		"max_rollbacks": 3, "min_confidence": 0.7, "paid_request_concurrency": *concurrency,
		"request_timeout_seconds": requestTimeout.Seconds(), "stage_timeout_seconds": stageTimeout.Seconds(), "run_timeout_seconds": runTimeout.Seconds(),
		"credential_source":   "runtime environment CAIRN_LLM_API_KEY (value excluded)",
		"repository_identity": "git:github.com/xcosmosbox/kd_manifeat", "input_git_snapshot": acceptedSourceCommit,
		"thinking":          map[string]string{"type": "enabled", "reasoning_effort": "provider default (not sent as override)"},
		"annotation_reuse":  "recorded real accepted annotation and semantic-gate pass; original raw verdict not available",
		"extraction_reused": false, "downstream_reused": false,
		"accepted_checkpoint_proof_sha256": sha256Hex(proofRaw), "runner_source_sha256": driverDigest,
	}
	configPath := filepath.Join(*out, "effective-config.json")
	if err := save(configPath, effective); err != nil {
		return err
	}
	configRaw, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	version, commit := buildmeta.BuilderVersion()
	fingerprint := publisher.Fingerprint{
		BuilderVersion: version, BuilderCommit: commit,
		ControllerSchemaVer: store.SchemaVersion(), DBSchemaVersion: storage.LatestSchemaVersion(),
		PromptSetVersion:     buildmeta.SourceDigest("annotation/", "extract/", "incremental/"),
		IdentityAlgorithmVer: buildmeta.SourceDigest("extract/uuid.go"), DiscoveryRulesDigest: buildmeta.SourceDigest("discovery/"),
		Model: cfg.Model, Provider: cfg.Provider, AnnotationSchemaVer: 1, ConfigDigest: "sha256:" + sha256Hex(configRaw),
	}
	if err := save(filepath.Join(*out, "frozen-builder-fingerprint.json"), fingerprint); err != nil {
		return err
	}
	if err := save(filepath.Join(*out, "builder.json"), map[string]any{
		"builder_version": version, "builder_commit": commit, "prompt_set_version": fingerprint.PromptSetVersion,
		"model": cfg.Model, "max_tokens": cfg.MaxTokens, "provider": cfg.Provider,
		"historical_annotation_identity":          checkpoint.proof["historical_prompt_set_version"],
		"reused_annotation_implementation_digest": checkpoint.proof["reused_annotation_implementation_digest"],
		"accepted_checkpoint_proof_sha256":        sha256Hex(proofRaw), "runner_source_sha256": driverDigest,
		"fingerprint_digest": fingerprint.Digest(), "config_digest": fingerprint.ConfigDigest,
		"started_at": time.Now().UTC().Format(time.RFC3339), "paid_execution_requested": *allowPaid,
	}); err != nil {
		return err
	}
	if !*allowPaid {
		log.Printf("preflight PASS: %d accepted annotations; no new provider requests", len(checkpoint.annotations))
		return save(filepath.Join(*out, "preflight.json"), map[string]any{"ok": true, "accepted_documents": len(checkpoint.annotations), "network_calls": 0, "extraction_reused": false})
	}
	if os.Getenv(cfg.APIKeyEnv) == "" {
		return fmt.Errorf("%s is required for --allow-paid", cfg.APIKeyEnv)
	}
	usageFile, err := os.OpenFile(filepath.Join(*out, "llm-usage.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer usageFile.Close()
	// The production client has a nil transport and therefore uses DefaultTransport.
	// This process-local observer does not change requests or production behavior.
	previousTransport := http.DefaultTransport
	observer := &usageTransport{next: previousTransport, file: usageFile}
	http.DefaultTransport = observer
	defer func() { http.DefaultTransport = previousTransport }()
	client, err := llm.NewOpenAICompatClient(cfg)
	if err != nil {
		return err
	}
	bounded := &boundedClient{next: client, sem: make(chan struct{}, *concurrency)}
	checkpoint.next = bounded
	orch, err := pipeline.NewOrchestrator(pipeline.Options{
		RepositoryIdentity: "git:github.com/xcosmosbox/kd_manifeat", Client: checkpoint,
		MaxTokens: cfg.MaxTokens, MaxRetries: 2, MaxRollbacks: 3, MinConfidence: 0.7,
		DumpDir: filepath.Join(*out, "stages"), StageTimeout: *stageTimeout,
	})
	if err != nil {
		return err
	}
	db := filepath.Join(*out, "knowledge.db")
	report, buildErr := orch.RunFullRebuild(ctx, *repo, db)
	// Persist the cost record even for a failed build; length-truncated responses
	// are counted by the transport before production rejects them.
	for name, value := range map[string]any{
		"build-report.json": report, "usage-summary.json": observer.summary(), "checkpoint-replay-summary.json": checkpoint.summary(),
	} {
		if err := save(filepath.Join(*out, name), value); err != nil {
			return err
		}
	}
	if buildErr != nil {
		return buildErr
	}
	if observer.writeErr != nil {
		return fmt.Errorf("usage ledger could not be persisted: %w", observer.writeErr)
	}
	if report == nil || len(report.Skipped) != 0 || checkpoint.annotationReplays.Load() != acceptedReferenceCount || checkpoint.gateReplays.Load() != acceptedReferenceCount {
		return errors.New("incomplete document acceptance or unexpected checkpoint replay counts")
	}
	if _, err := evolve.Record(ctx, db, filepath.Join(*out, "evolution"), "nicedata-real-e2e-repair-rerun"); err != nil {
		return err
	}
	rr := runner.NewRunnerWithClient(bounded, dkconfig.LLMConfig{Model: cfg.Model})
	validation, err := rr.Validate(ctx, runner.ValidateRequest{RepoPath: *repo, DBPath: db})
	if saveErr := save(filepath.Join(*out, "validation.json"), validation); saveErr != nil {
		return saveErr
	}
	if err != nil {
		return err
	}
	if !validation.OK {
		return fmt.Errorf("candidate validation failed: %v", validation.Errors)
	}
	log.Printf("real E2E PASS: docs=%d nodes=%d edges=%d", report.DocsTotal, validation.NodeCount, validation.EdgeCount)
	return nil
}

func driverSourceDigest(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var raw []byte
	for _, entry := range entries { // ReadDir returns filename order.
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return "", err
		}
		raw = append(raw, []byte(entry.Name()+"\n")...)
		raw = append(raw, data...)
		raw = append(raw, '\n')
	}
	return sha256Hex(raw), nil
}
