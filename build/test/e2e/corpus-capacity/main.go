// corpus-capacity runs the ordinary production builder on a declared source set.
// Preparation and construction never load questions, answers or HOLDOUT data.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/pipeline"
	"github.com/xcosmosbox/cairn/core/dkconfig"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Construction failed; see the private local outcomes.")
		os.Exit(1)
	}
}

func run() error {
	mode := flag.String("mode", "prepare", "prepare, run, or v016 (ordinary finite acceptance phase)")
	corpus := flag.String("corpus", "", "declared source-only JSON document array")
	out := flag.String("out", "", "new preparation directory, or prepared directory for run")
	maxTokens := flag.Int("max-tokens", dkconfig.DeepSeekDefaultThinkingTokens, "per-request output tokens")
	requestTimeout := flag.Duration("request-timeout", 600*time.Second, "per-request provider timeout")
	concurrency := flag.Int("concurrency", 8, "concurrent logical requests")
	flag.Parse()
	if *out == "" || flag.NArg() != 0 {
		return fmt.Errorf("output required; no positional arguments")
	}
	if *mode == "prepare" {
		return prepare(*corpus, *out)
	}
	if *mode == "v016" {
		if *corpus != "" {
			return fmt.Errorf("v016: use only the declared source-tree input")
		}
		return runV016Phase(*out, *maxTokens, *requestTimeout, *concurrency)
	}
	if *mode != "run" {
		return fmt.Errorf("unknown mode; historical continuation routes are retired")
	}
	if *maxTokens < 1 || *maxTokens > dkconfig.DeepSeekMaxOutputTokens || *requestTimeout <= 0 || *concurrency < 1 {
		return fmt.Errorf("invalid request limits or concurrency")
	}
	if os.Getenv("CAIRN_LLM_API_KEY") == "" {
		return fmt.Errorf("runtime credential unavailable")
	}
	if *corpus != "" {
		if err := prepare(*corpus, *out); err != nil {
			return err
		}
	}
	selected, err := bindPrepared(*out)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return buildPrepared(ctx, *out, selected, *maxTokens, *requestTimeout, *concurrency)
}

func buildPrepared(ctx context.Context, out string, selected *selection, maxTokens int, requestTimeout time.Duration, concurrency int) error {
	// 新运行只写一次参数与原文身份；没有预检/历史回放/再次批准门。
	// Bind the current inputs once; existing run receipts cannot be overwritten.
	if err := writeNew(filepath.Join(out, "run-start.json"), map[string]any{"format": "cairn-capacity-run/v2", "started_at": now(), "sources": selected, "model": dkconfig.DeepSeekFlashModel, "endpoint": dkconfig.DeepSeekEndpoint, "max_tokens": maxTokens, "request_timeout_seconds": requestTimeout.Seconds(), "concurrency": concurrency}); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("existing run identity; refusing to replay construction")
		}
		log.Printf("WARN run parameters could not be saved: %v", err)
	}
	privateLog, err := os.OpenFile(filepath.Join(out, "private-builder.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	oldLog := log.Writer()
	if err != nil {
		fmt.Fprintln(os.Stderr, "WARN private builder log unavailable")
		log.SetOutput(io.Discard)
	} else {
		defer privateLog.Close()
		log.SetOutput(privateLog)
	}
	defer log.SetOutput(oldLog)
	observer := llm.NewRawTraceTransport(out, http.DefaultTransport)
	defer func() {
		if err := observer.Close(); err != nil {
			log.Printf("WARN raw request evidence unavailable: %v", err)
		}
	}()
	client, err := llm.NewOpenAICompatClientWithTransport(llm.Config{Provider: "openai_compatible", APIKeyEnv: "CAIRN_LLM_API_KEY", Model: dkconfig.DeepSeekFlashModel, Endpoint: dkconfig.DeepSeekEndpoint, MaxTokens: maxTokens, Timeout: requestTimeout.String(), MaxRetries: -1}, observer)
	if err != nil {
		return err
	}
	bounded := &boundedClient{next: client, sem: make(chan struct{}, concurrency)}
	orch, err := pipeline.NewOrchestrator(pipeline.Options{RepositoryIdentity: "cairn-source-diagnostic-v1", Client: bounded, MaxTokens: maxTokens, MaxRetries: 1, DumpDir: filepath.Join(out, "stages")})
	if err != nil {
		return err
	}
	report, buildErr := orch.RunFullRebuild(ctx, filepath.Join(out, "source"), filepath.Join(out, "knowledge.db"))
	if err = writeNew(filepath.Join(out, "build-report.json"), report); err != nil {
		log.Printf("WARN build report unavailable: %v", err)
	}
	finish := map[string]any{"finished_at": now(), "intended_documents": selected.IntendedDocuments, "prepared_documents": len(selected.Documents), "build_error": safeError(buildErr), "caller_cancelled": ctx.Err() != nil, "usage_scope": "reported values only; missing usage is null, no invented cost"}
	if err = writeNew(filepath.Join(out, "run-finish.json"), finish); err != nil {
		log.Printf("WARN finish receipt unavailable: %v", err)
	}
	return buildErr
}
