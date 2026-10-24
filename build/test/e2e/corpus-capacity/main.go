// corpus-capacity invokes the unchanged production pipeline on a frozen,
// corpus-only twelve-document selection. It is not an evaluation runner.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	buildmeta "github.com/xcosmosbox/cairn/build/internal"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/pipeline"
	"github.com/xcosmosbox/cairn/core/dkconfig"
)

func rootPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Capacity command failed; retained local receipts identify the boundary.")
		os.Exit(1)
	}
}

func run() error {
	mode := flag.String("mode", "prepare", "prepare, preflight, run, audit or trace-audit; no whole-run retry")
	corpus := flag.String("corpus", "", "exact frozen corpus.json; no QA input")
	out := flag.String("out", "", "new prepare output, or prepared directory for preflight/run/audit")
	selectionSHA := flag.String("selection-sha256", "", "reviewed exact selection.json SHA256 required after preparation")
	allowPaid := flag.Bool("allow-paid", false, "explicit separate paid execution admission; not granted by implementation")
	maxTokens := flag.Int("max-tokens", 65536, "explicit output reserve for actual production requests")
	requestTimeout := flag.Duration("request-timeout", 600*time.Second, "finite provider request timeout")
	stageTimeout := flag.Duration("stage-timeout", 90*time.Minute, "finite stage timeout")
	runTimeout := flag.Duration("run-timeout", 90*time.Minute, "finite total timeout")
	python := flag.String("token-python", "", "isolated official recipe environment Python executable")
	pythonSHA := flag.String("token-python-sha256", "", "exact Python executable SHA256")
	runtimeReceipt := flag.String("token-runtime-receipt", "", "reviewed installed official runtime receipt")
	runtimeSHA := flag.String("token-runtime-sha256", "", "exact token runtime receipt SHA256")
	flag.Parse()
	if *out == "" || flag.NArg() != 0 {
		return fmt.Errorf("output required and positional arguments forbidden")
	}
	if *mode == "trace-audit" {
		_, failures, err := writeTraceAudit(*out)
		if err != nil {
			return err
		}
		if len(failures) > 0 {
			return fmt.Errorf("trace incomplete; forensic receipt retained")
		}
		return nil
	}
	protocol := filepath.Join(rootPath(), "e2e/benchmarks/multihop_public/capacity/protocol.json")
	if *mode == "prepare" {
		if *allowPaid {
			return fmt.Errorf("prepare is offline")
		}
		return prepare(*corpus, *out, protocol)
	}
	raw, err := os.ReadFile(filepath.Join(*out, "selection.json"))
	if err != nil || *selectionSHA == "" || sha(raw) != *selectionSHA {
		return fmt.Errorf("reviewed selection pin required")
	}
	if *mode == "audit" {
		_, err = audit(context.Background(), *out)
		return err
	}
	if *mode != "run" && *mode != "preflight" {
		return fmt.Errorf("unknown mode")
	}
	s, err := verifyPrepared(*out, false)
	if err != nil {
		return err
	}
	if *maxTokens < 1 || *maxTokens > dkconfig.DeepSeekMaxOutputTokens || *requestTimeout <= 0 || *stageTimeout <= 0 || *runTimeout <= 0 {
		return fmt.Errorf("positive finite limits required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *runTimeout)
	defer cancel()
	adapter := filepath.Join(rootPath(), "e2e/benchmarks/multihop_public/capacity/token_count.py")
	adapterBytes, err := os.ReadFile(adapter)
	if err != nil {
		return err
	}
	counter := externalCounter{Python: *python, PythonSHA: *pythonSHA, Adapter: adapter, AdapterSHA: sha(adapterBytes), Receipt: *runtimeReceipt, ReceiptSHA: *runtimeSHA}
	// A free renderer check before starting a run cannot establish the fit of
	// later generated requests. The transport independently checks every attempt.
	probe := []byte(`{"model":"deepseek-flash","messages":[{"role":"system","content":"Return JSON."},{"role":"user","content":"JSON capacity preflight."}],"max_tokens":64,"thinking":{"type":"enabled"},"response_format":{"type":"json_object"}}`)
	if _, err = counter.Count(ctx, probe); err != nil {
		return fmt.Errorf("verified official tokenizer runtime required")
	}
	if *mode == "preflight" {
		return writeNew(filepath.Join(*out, "preflight.json"), map[string]any{"status": "offline_tokenizer_and_original_identity_pass", "selection_sha256": *selectionSHA, "selected_documents": len(s.Documents), "model_calls": 0, "runtime_receipt_sha256": *runtimeSHA, "full609_admitted": false})
	}
	if !*allowPaid || os.Getenv("CAIRN_LLM_API_KEY") == "" {
		return fmt.Errorf("separate execution admission and runtime key required")
	}
	version, commit := buildmeta.BuilderVersion()
	binaryPath, err := os.Executable()
	if err != nil {
		return err
	}
	binaryBytes, err := os.ReadFile(binaryPath)
	if err != nil {
		return err
	}
	effective := map[string]any{"format": "cairn-capacity-run/v1", "started_at": now(), "selection_sha256": *selectionSHA, "protocol_sha256": s.ProtocolSHA, "builder_version": version, "builder_commit": commit,
		"production_source_digest": buildmeta.SourceDigest("annotation/", "extract/", "pipeline/", "llm/", "discovery/", "ingest/", "writeback/"), "endpoint": dkconfig.DeepSeekEndpoint, "requested_model": dkconfig.DeepSeekFlashModel,
		"executable_sha256": sha(binaryBytes),
		"max_tokens":        *maxTokens, "http_max_retries": 3, "annotation_attempts": 3, "annotation_rounds": 3, "min_confidence": 0.7, "concurrency": 8,
		"request_timeout_seconds": requestTimeout.Seconds(), "stage_timeout_seconds": stageTimeout.Seconds(), "run_timeout_seconds": runTimeout.Seconds(), "whole_run_retries": 0,
		"token_runtime_sha256": *runtimeSHA, "token_python_sha256": *pythonSHA, "token_adapter_sha256": sha(adapterBytes), "thinking": "unchanged production request; see exact per-attempt metadata", "provider_backing_version": "documentation maps alias to V4.1-Flash-1017; actual served revision unknown unless returned",
		"original_span_mapping": "selection offsets bind unchanged original bodies; optional original span hints remain unknown/unverified and do not locate rewritten Markdown", "holdout": "closed; no QA inputs"}
	if err = writeNew(filepath.Join(*out, "run-start.json"), effective); err != nil {
		return err
	}
	privateLog, err := os.OpenFile(filepath.Join(*out, "private-builder.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer privateLog.Close()
	oldLog := log.Writer()
	log.SetOutput(privateLog)
	defer log.SetOutput(oldLog)
	ledger, file, err := openLedger(filepath.Join(*out, "requests.jsonl"), cancel)
	if err != nil {
		return err
	}
	defer file.Close()
	previous := http.DefaultTransport
	observer := &traceTransport{next: previous, ledger: ledger, counter: counter}
	http.DefaultTransport = observer
	defer func() { http.DefaultTransport = previous }()
	client, err := llm.NewOpenAICompatClient(llm.Config{Provider: "openai_compatible", APIKeyEnv: "CAIRN_LLM_API_KEY", Model: dkconfig.DeepSeekFlashModel, Endpoint: dkconfig.DeepSeekEndpoint, MaxTokens: *maxTokens, Timeout: requestTimeout.String(), MaxRetries: 3})
	if err != nil {
		return err
	}
	bounded := &boundedClient{next: client, sem: make(chan struct{}, 8), ledger: ledger}
	orch, err := pipeline.NewOrchestrator(pipeline.Options{RepositoryIdentity: "cairn-multihop-public-originals-v1", Client: bounded, MaxTokens: *maxTokens, MaxRetries: 3, MaxRollbacks: 3, MinConfidence: 0.7, DumpDir: filepath.Join(*out, "stages"), StageTimeout: *stageTimeout})
	if err != nil {
		return err
	}
	// Keep a human-visible heartbeat even during a long provider call. It contains
	// counts only, never a source body, provider error or credential.
	done := make(chan struct{})
	stopped := make(chan struct{})
	var stopOnce sync.Once
	stopHeartbeat := func() { stopOnce.Do(func() { close(done); <-stopped }) }
	defer stopHeartbeat()
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = ledger.append(map[string]any{"event": "progress", "time_utc": now(), "logical_calls": bounded.sequence.Load(), "wire_admission_attempts": observer.sequence.Load(), "admitted_transport_attempts": observer.sent.Load(), "http_responses": observer.responses.Load()})
				fmt.Fprintf(os.Stderr, "Capacity active: logical=%d wire_attempts=%d responses=%d elapsed_context_remaining=%v\n", bounded.sequence.Load(), observer.sequence.Load(), observer.responses.Load(), time.Until(deadline(ctx)).Round(time.Second))
			}
		}
	}()
	report, buildErr := orch.RunFullRebuild(ctx, filepath.Join(*out, "source"), filepath.Join(*out, "knowledge.db"))
	stopHeartbeat()
	if err = writeNew(filepath.Join(*out, "build-report.json"), report); err != nil {
		return err
	}
	finish := map[string]any{"finished_at": now(), "build_error": buildErr != nil, "context_error": ctx.Err() != nil, "ledger_ok": ledger.failure() == nil, "logical_calls": bounded.sequence.Load(), "wire_admission_attempts": observer.sequence.Load(), "admitted_transport_attempts": observer.sent.Load(), "http_responses": observer.responses.Load(), "peak_logical_concurrency": bounded.peak.Load(), "reported_input_tokens": observer.input.Load(), "reported_output_tokens": observer.output.Load(), "responses_without_usage": observer.missingUsage.Load(), "transport_attempts_without_response": observer.sent.Load() - observer.responses.Load(), "usage_scope": "reported values only; absent/failed usage unknown; no invented cost", "private_log": "not_public; may contain provider text; do not upload unreviewed"}
	if err = writeNew(filepath.Join(*out, "run-finish.json"), finish); err != nil {
		return err
	}
	if buildErr != nil || ledger.failure() != nil {
		_, _, _ = writeTraceAudit(*out)
		return fmt.Errorf("build or durable ledger failed")
	}
	_, err = audit(ctx, *out)
	return err
}

func deadline(ctx context.Context) time.Time { d, _ := ctx.Deadline(); return d }
