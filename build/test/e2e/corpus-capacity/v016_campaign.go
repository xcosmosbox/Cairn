package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/pipeline"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

var (
	v016Phase      = flag.String("phase", "full", "v016: full, repeat (full raw input), unchanged (existing graph), or changed")
	v016SourceTree = flag.String("source-tree", "", "v016: source-only Markdown tree; never inventory or expectations")
	v016Previous   = flag.String("previous", "", "v016: preceding evidence directory; usable persisted data required only for unchanged/changed, with prior incompleteness retained")
	v016ChangePath = flag.String("change-path", "", "v016 changed: existing repository-relative Markdown path")
	v016ChangeFile = flag.String("change-file", "", "v016 changed: source-only prose to append outside generated blocks")
	v016Retries    = flag.Int("semantic-retries", 3, "v016: shared extra recoveries per failed semantic scope; initial call is separate; zero defaults to three, negative disables")
)

type v016SourceIdentity struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// 每个 phase 使用独立新目录和普通入口；冻结原文与验收清单永不进入同一输入接口。
// Each phase is a fresh evidence directory; only a Markdown source tree reaches the builder.
func runV016Phase(out string, maxTokens int, requestTimeout time.Duration, concurrency int) (runErr error) {
	phase := *v016Phase
	inPlace := phase == "changed" || phase == "unchanged"
	if phase != "full" && phase != "repeat" && phase != "unchanged" && phase != "changed" {
		return fmt.Errorf("v016: invalid phase")
	}
	if *v016SourceTree == "" || maxTokens < 1 || maxTokens > dkconfig.DeepSeekMaxOutputTokens || requestTimeout <= 0 || concurrency < 1 {
		return fmt.Errorf("v016: source tree and valid request parameters required")
	}
	if inPlace && *v016Previous == "" {
		return fmt.Errorf("v016: preceding phase required")
	}
	if phase == "changed" && (*v016ChangePath == "" || *v016ChangeFile == "") {
		return fmt.Errorf("v016: changed phase requires a declared source addition")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return fmt.Errorf("v016: new evidence directory required: %w", err)
	}
	started := false
	defer func() {
		if !started {
			_ = writeNew(filepath.Join(out, "phase-finish.json"), map[string]any{"finished_at": now(), "phase": phase, "status": "unavailable", "error": safeError(runErr), "provider_attempts": 0, "reason": "phase preparation or predecessor unavailable"})
		}
	}()
	roster, err := copyV016Tree(*v016SourceTree, filepath.Join(out, "original"), true)
	if err != nil {
		return err
	}
	if err = writeNew(filepath.Join(out, "source-roster.json"), roster); err != nil {
		return err
	}
	var baseline any
	baselineStatus := ""
	if inPlace {
		// Partial but usable data can support independent in-place work. Preserve
		// its incompleteness as evidence instead of requiring whole-product success.
		var previous struct {
			Error           any    `json:"error"`
			SnapshotError   any    `json:"snapshot_error"`
			Status          string `json:"status"`
			AggregateStatus string `json:"aggregate_status"`
		}
		if e := readJSON(filepath.Join(*v016Previous, "phase-finish.json"), &previous); e != nil {
			baseline = map[string]any{"status": "unknown", "receipt_error": safeError(e)}
			baselineStatus = "unknown"
		} else {
			baseline = previous
			baselineStatus = previous.Status
			if previous.AggregateStatus != "" {
				baselineStatus = previous.AggregateStatus
			}
		}
		var previousRoster []v016SourceIdentity
		if err = readJSON(filepath.Join(*v016Previous, "source-roster.json"), &previousRoster); err != nil || !reflect.DeepEqual(previousRoster, roster) {
			return fmt.Errorf("v016: fixed raw source roster changed")
		}
	}
	if inPlace {
		if _, err = copyV016Tree(filepath.Join(*v016Previous, "source"), filepath.Join(out, "source"), false); err != nil {
			return err
		}
	} else {
		if _, err = copyV016Tree(*v016SourceTree, filepath.Join(out, "source"), true); err != nil {
			return err
		}
	}
	var originals []extract.FrozenSource
	var documents []*dktypes.AnnotatedDocument
	if inPlace {
		if err = readJSON(filepath.Join(*v016Previous, "caller-frozen-sources.json"), &originals); err != nil {
			return err
		}
		if err = readJSON(filepath.Join(*v016Previous, "caller-source-documents.json"), &documents); err != nil {
			return err
		}
	}
	if inPlace {
		if err = copyV016File(filepath.Join(*v016Previous, "knowledge.db"), filepath.Join(out, "knowledge.db")); err != nil {
			return fmt.Errorf("v016: previous database: %w", err)
		}
	}
	var change any
	if phase == "changed" {
		rel := filepath.Clean(*v016ChangePath)
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Ext(rel) != ".md" {
			return fmt.Errorf("v016: invalid changed source path")
		}
		path := filepath.Join(out, "source", rel)
		before, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		addition, e := os.ReadFile(*v016ChangeFile)
		if e != nil {
			return e
		}
		if len(addition) == 0 {
			return fmt.Errorf("v016: empty change")
		}
		found := false
		for i, original := range originals {
			if filepath.ToSlash(original.Path) == filepath.ToSlash(rel) {
				current := original.Content + "\n\n" + string(addition)
				originals[i] = extract.FreezeSource(original.Skill, original.Path, current)
				found = true
			}
		}
		if !found {
			return fmt.Errorf("v016: changed raw source has no authoritative predecessor")
		}
		if err = writeBytesNew(filepath.Join(out, "change-source.md"), addition); err != nil {
			return err
		}
		changed := append(append(append([]byte(nil), before...), '\n', '\n'), addition...)
		if err = os.WriteFile(path, changed, 0600); err != nil {
			return err
		}
		change = map[string]any{"path": filepath.ToSlash(rel), "before_sha256": sha(before), "addition_sha256": sha(addition), "after_sha256": sha(changed)}
	}
	if err = writeNew(filepath.Join(out, "phase-start.json"), map[string]any{
		"format": "cairn-v016-phase/v1", "phase": phase, "started_at": now(), "sources": roster,
		"previous": *v016Previous, "baseline": baseline, "change": change, "model": dkconfig.DeepSeekFlashModel,
		"endpoint": dkconfig.DeepSeekEndpoint, "max_tokens": maxTokens, "request_timeout_seconds": requestTimeout.Seconds(),
		"concurrency": concurrency, "semantic_extra_recoveries": *v016Retries, "provider_extra_retries": -1,
		"credential_variable": "CAIRN_LLM_API_KEY", "credential_present": os.Getenv("CAIRN_LLM_API_KEY") != "",
		"inventory_supplied_to_builder": false,
	}); err != nil {
		return err
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
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	observer := llm.NewRawTraceTransport(out, http.DefaultTransport)
	defer observer.Close()
	client, err := llm.NewOpenAICompatClientWithTransport(llm.Config{Provider: "openai_compatible", APIKeyEnv: "CAIRN_LLM_API_KEY", Model: dkconfig.DeepSeekFlashModel, Endpoint: dkconfig.DeepSeekEndpoint, MaxTokens: maxTokens, Timeout: requestTimeout.String(), MaxRetries: -1}, observer)
	if err != nil {
		return err
	}
	bounded := &boundedClient{next: &v016AuthClient{next: client}, sem: make(chan struct{}, concurrency)}
	var report any
	var buildErr error
	started = true
	if inPlace {
		orch, e := incremental.NewIncrementalOrchestrator(incremental.Options{RepositoryIdentity: "cairn-v016-fixed-source-campaign-v1", Client: bounded, MaxTokens: maxTokens, MaxRetries: *v016Retries, Sources: originals, SourceDocuments: documents})
		if e != nil {
			return e
		}
		report, buildErr = orch.Run(ctx, filepath.Join(out, "source"), filepath.Join(out, "knowledge.db"))
	} else {
		orch, e := pipeline.NewOrchestrator(pipeline.Options{RepositoryIdentity: "cairn-v016-fixed-source-campaign-v1", Client: bounded, MaxTokens: maxTokens, MaxRetries: *v016Retries, DumpDir: filepath.Join(out, "stages")})
		if e != nil {
			return e
		}
		report, buildErr = orch.RunFullRebuild(ctx, filepath.Join(out, "source"), filepath.Join(out, "knowledge.db"))
	}
	if phase == "full" || phase == "repeat" {
		if e := readJSON(filepath.Join(out, "stages", "00_frozen_sources.json"), &originals); e != nil {
			log.Printf("WARN frozen input evidence: %v", e)
		}
		if e := readJSON(filepath.Join(out, "stages", "04_extract_input.json"), &documents); e != nil {
			log.Printf("WARN accepted input evidence: %v", e)
		}
	}
	if e := writeNew(filepath.Join(out, "caller-frozen-sources.json"), originals); e != nil {
		log.Printf("WARN caller sources evidence: %v", e)
	}
	if e := writeNew(filepath.Join(out, "caller-source-documents.json"), documents); e != nil {
		log.Printf("WARN caller documents evidence: %v", e)
	}
	if err = writeNew(filepath.Join(out, "build-report.json"), report); err != nil {
		log.Printf("WARN report evidence: %v", err)
	}
	snapshotErr := snapshotV016(ctx, out)
	var product struct{ Status string }
	if raw, e := json.Marshal(report); e == nil {
		_ = json.Unmarshal(raw, &product)
	}
	aggregateStatus := product.Status
	if inPlace && baselineStatus != "complete" && product.Status == "complete" {
		aggregateStatus = "partial"
	}
	finishErr := writeNew(filepath.Join(out, "phase-finish.json"), map[string]any{"finished_at": now(), "phase": phase, "status": product.Status, "aggregate_status": aggregateStatus, "baseline": baseline, "error": safeError(buildErr), "snapshot_error": safeError(snapshotErr), "caller_cancelled": ctx.Err() != nil, "usage_scope": "observed only; unavailable usage/cost stays unknown"})
	if finishErr != nil {
		log.Printf("WARN finish evidence: %v", finishErr)
	}
	if buildErr == nil && snapshotErr != nil {
		return fmt.Errorf("v016: persisted graph readback: %w", snapshotErr)
	}
	return buildErr
}

func copyV016File(from, to string) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("v016: regular source file required")
	}
	raw, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return writeBytesNew(to, raw)
}

func copyV016Tree(from, to string, sourceOnly bool) ([]v016SourceIdentity, error) {
	var roster []v016SourceIdentity
	err := filepath.WalkDir(from, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("v016: linked input rejected")
		}
		if entry.IsDir() {
			return nil
		}
		if sourceOnly && filepath.Ext(rel) != ".md" {
			return fmt.Errorf("v016: source tree accepts Markdown only: %s", rel)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = writeBytesNew(filepath.Join(to, rel), raw); err != nil {
			return err
		}
		roster = append(roster, v016SourceIdentity{Path: filepath.ToSlash(rel), SHA256: sha(raw), Bytes: len(raw)})
		return nil
	})
	return roster, err
}

func snapshotV016(ctx context.Context, out string) error {
	db, err := storage.OpenPreflightReadOnly(filepath.Join(out, "knowledge.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	nodes, err := storage.NewNodeRepo(db).ListAll(ctx)
	if err != nil {
		return err
	}
	edges, err := storage.NewEdgeRepo(db).ListAll(ctx)
	if err != nil {
		return err
	}
	sources, err := storage.NewNodeSourceRepo(db).ListAll(ctx)
	if err != nil {
		return err
	}
	return writeNew(filepath.Join(out, "persisted-graph.json"), map[string]any{"nodes": nodes, "edges": edges, "node_sources": sources})
}

// 先用一个真实生产请求核实全局鉴权；拒绝后不向每篇独立原文重复发送同一失败。
// Only the first request is serialized. A global authentication denial is reused
// as a local unavailable error, never a fabricated provider response or semantic verdict.
type v016AuthClient struct {
	next      llm.Client
	mu        sync.Mutex
	checked   bool
	authError error
}

func (c *v016AuthClient) ProviderName() string { return c.next.ProviderName() }
func (c *v016AuthClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if c.authError != nil {
		err := c.authError
		c.mu.Unlock()
		return nil, fmt.Errorf("v016: authentication unavailable after first actual provider attempt: %w", err)
	}
	if !c.checked {
		response, err := c.next.Complete(ctx, req)
		c.checked = true
		if err != nil && (strings.Contains(err.Error(), "status 401:") || strings.Contains(err.Error(), "status 403:")) {
			c.authError = err
		}
		c.mu.Unlock()
		return response, err
	}
	c.mu.Unlock()
	return c.next.Complete(ctx, req)
}
