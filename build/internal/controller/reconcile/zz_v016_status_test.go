package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/runner"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dkconfig"
)

type v016ControllerFailureClient struct {
	mode   string
	calls  atomic.Int32
	cancel context.CancelFunc
}

func (*v016ControllerFailureClient) ProviderName() string { return "offline-v016-outer-controller" }
func (c *v016ControllerFailureClient) Complete(ctx context.Context, _ llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.calls.Add(1)
	switch c.mode {
	case "permanent":
		return nil, fmt.Errorf("openai_compatible: missing API key")
	case "cancelled":
		c.cancel()
		return nil, ctx.Err()
	default:
		return nil, fmt.Errorf("openai_compatible: status 503: temporary fixture outage")
	}
}

// 原故障：外层Step丢弃真实runner的失败报告，并把已证明永久的配置错误投入重试循环。
// This invokes the actual reconciler, real runner and finite document processor.
func TestV016ControllerKeepsActualBuildFailureReceipt(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"permanent", "invalid-config", "retryable", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			recon, st, _, _ := publicationSetup(t, model.StateFullBuild)
			// Commit source bytes only in the fixture's local mirror. No remote publication occurs.
			wt := recon.ws.SourceWorktree("run")
			raw := "---\nname: fixture\n---\nThe source retains an independent substantive admission rule.\n"
			if err := os.WriteFile(filepath.Join(wt, "SKILL.md"), []byte(raw), 0644); err != nil {
				t.Fatal(err)
			}
			if err := recon.ws.CommitAll(context.Background(), wt, "synthetic source fixture", "fixture", "fixture@example.test"); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("git", "rev-parse", "HEAD")
			cmd.Dir = wt
			head, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if err := st.UpdateRunDesiredSHA(context.Background(), "run", strings.TrimSpace(string(head))); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &v016ControllerFailureClient{mode: mode, cancel: cancel}
			cfg := dkconfig.LLMConfig{MaxRetries: 1}
			if mode == "invalid-config" {
				cfg.MaxTokens = -1
			}
			recon.runner = runner.NewRunnerWithClient(client, cfg)
			result, stepErr := recon.Step(ctx, "run")
			if stepErr != nil && !(mode == "cancelled" && errors.Is(stepErr, context.Canceled)) {
				t.Fatalf("outer stage failed without a durable receipt: %+v err=%v", result, stepErr)
			}
			saved, err := st.GetRun(context.Background(), "run")
			if err != nil {
				t.Fatal(err)
			}
			wantState, wantCalls := model.StateFailedRetryable, int32(2)
			if mode == "permanent" {
				wantState, wantCalls = model.StateFailedPermanent, 1
			}
			if mode == "invalid-config" {
				wantState, wantCalls = model.StateFailedPermanent, 0
			}
			if mode == "cancelled" {
				wantCalls = 1
			}
			if saved.State != wantState || client.calls.Load() != wantCalls {
				t.Fatalf("failure classification or finite attempts wrong: saved=%+v calls=%d", saved, client.calls.Load())
			}
			if mode == "cancelled" && saved.ErrorCode != "cancelled_full_build" {
				t.Fatalf("actual cancellation lost its distinct reason: %+v", saved)
			}
			if saved.ReportPath == "" {
				t.Fatalf("runner error discarded structured receipt: %+v", saved)
			}
			rel, err := filepath.Rel(filepath.Dir(recon.candidateDBPath("run")), saved.ReportPath)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("receipt is outside the existing private run directory: %s", saved.ReportPath)
			}
			body, err := os.ReadFile(saved.ReportPath)
			if err != nil {
				t.Fatal(err)
			}
			var receipt runner.BuildResult
			if err := json.Unmarshal(body, &receipt); err != nil {
				t.Fatal(err)
			}
			expectedKind := "unavailable"
			if mode == "permanent" || mode == "invalid-config" {
				expectedKind = "permanent"
			}
			if mode == "cancelled" {
				expectedKind = "cancelled"
			}
			if receipt.Status != "failed" || receipt.FailureKind != expectedKind || len(receipt.Reasons) == 0 || (mode != "invalid-config" && len(receipt.Report) == 0) {
				t.Fatalf("receipt lost actual terminal build facts: %s", body)
			}
			if mode == "invalid-config" {
				if len(receipt.Report) != 0 || !strings.Contains(strings.Join(receipt.Reasons, " "), "maxTokens") {
					t.Fatalf("preflight configuration invented stage attempts: %s", body)
				}
			} else {
				var factual struct {
					Status    string
					DocsTotal int
					Documents []annotation.DocumentResult
				}
				if err := json.Unmarshal(receipt.Report, &factual); err != nil {
					t.Fatal(err)
				}
				if factual.Status != "failed" || factual.DocsTotal != 1 || len(factual.Documents) != 1 || factual.Documents[0].FilePath != "SKILL.md" || len(factual.Documents[0].Attempts) != int(wantCalls) {
					t.Fatalf("receipt dropped source denominator/attempt evidence: %s", receipt.Report)
				}
				doc := factual.Documents[0]
				if mode == "permanent" && (doc.Exhausted || doc.Attempts[0].Outcome != "permanent") {
					t.Fatalf("permanent configuration error consumed recoveries: %+v", doc)
				}
				if mode == "retryable" && (!doc.Exhausted || doc.Outcome != annotation.DocumentUnavailable) {
					t.Fatalf("temporary failure hidden as permanent: %+v", doc)
				}
				if mode == "cancelled" && (doc.Exhausted || doc.Outcome != annotation.DocumentCancelled) {
					t.Fatalf("actual caller cancellation lost: %+v", doc)
				}
			}
			next, nextErr := recon.Step(context.Background(), "run")
			if nextErr != nil || next.Advanced || client.calls.Load() != wantCalls {
				t.Fatalf("terminal/backoff run cycled into the builder: %+v err=%v calls=%d", next, nextErr, client.calls.Load())
			}
		})
	}
}
