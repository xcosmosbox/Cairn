package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dkconfig"
)

type v016RunnerUnavailable struct{ calls atomic.Int32 }

func (*v016RunnerUnavailable) ProviderName() string { return "offline-v016-runner-unavailable" }
func (c *v016RunnerUnavailable) Complete(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.calls.Add(1)
	return nil, fmt.Errorf("openai_compatible: status 503: fixed unavailable provider")
}

// 原故障：生产runner在err分支返回空BuildResult，丢失来源分母、有限尝试和最终失败状态。
// Exercise the controller's actual entry; a canned report conversion cannot prove this.
func TestV016RunnerRetainsFiniteFailureReport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if e := os.MkdirAll(filepath.Join(root, "references"), 0755); e != nil {
		t.Fatal(e)
	}
	for p, s := range map[string]string{"SKILL.md": "---\nname: fixture\n---\nThe marker contains an independent substantive fact.\n", "references/source.md": "The reference contains another independent fact.\n"} {
		if e := os.WriteFile(filepath.Join(root, p), []byte(s), 0644); e != nil {
			t.Fatal(e)
		}
	}
	c := &v016RunnerUnavailable{}
	r := NewRunnerWithClient(c, dkconfig.LLMConfig{MaxRetries: 1})
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	result, e := r.Full(context.Background(), FullRequest{RepoPath: root, DBPath: dbPath, RepositoryIdentity: "local:v016-runner"})
	if e == nil {
		t.Fatal("unavailable full run reported success")
	}
	var public struct {
		Status  string
		Reasons []string
	}
	encoded, _ := json.Marshal(result)
	if e := json.Unmarshal(encoded, &public); e != nil {
		t.Fatal(e)
	}
	if public.Status != "failed" || len(public.Reasons) == 0 || result.DBPath != dbPath || len(result.Report) == 0 {
		t.Fatalf("controller lost failure/status result: %s err=%v", encoded, e)
	}
	var factual struct {
		Status    string
		DocsTotal int
		Documents []annotation.DocumentResult
	}
	if e := json.Unmarshal(result.Report, &factual); e != nil {
		t.Fatal(e)
	}
	if factual.Status != public.Status || factual.DocsTotal != 2 || len(factual.Documents) != 2 {
		t.Fatalf("controller source denominator/status differs: %s", result.Report)
	}
	for _, doc := range factual.Documents {
		if doc.Outcome != annotation.DocumentUnavailable || !doc.Exhausted || len(doc.Attempts) != 2 || doc.Reason == "" {
			t.Fatalf("runner discarded finite failure identity: %+v", doc)
		}
	}
	if c.calls.Load() != 4 {
		t.Fatalf("nested/unbounded retries: got %d want 4", c.calls.Load())
	}
	if _, e := os.Stat(dbPath); !os.IsNotExist(e) {
		t.Fatalf("all-failed source set published a database: %v", e)
	}
}

// A typed nil interface is rejected before either ordinary entry can touch inputs.
func TestV016RunnerRejectsTypedNilClientBeforeWork(t *testing.T) {
	t.Parallel()
	for _, entry := range []string{"full", "incremental"} {
		t.Run(entry, func(t *testing.T) {
			real := NewRunnerWithClient((*v016RunnerUnavailable)(nil), dkconfig.LLMConfig{MaxRetries: 1})
			root := t.TempDir()
			path := filepath.Join(root, "uncreated.db")
			var result BuildResult
			var err error
			if entry == "full" {
				result, err = real.Full(context.Background(), FullRequest{RepoPath: filepath.Join(root, "missing-source"), DBPath: path})
			} else {
				result, err = real.Incremental(context.Background(), IncrementalRequest{RepoPath: filepath.Join(root, "missing-source"), DBPath: path})
			}
			if err == nil || result.Status != "failed" || result.FailureKind != "permanent" || len(result.Reasons) == 0 {
				t.Fatalf("known nil client retried or lost: %+v err=%v", result, err)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("preflight created a database: %v", statErr)
			}
		})
	}
}

func TestV016ReviewNegativeTokensClassifiesPermanent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("This source is substantive."), 0644); err != nil {
		t.Fatal(err)
	}
	c := &v016RunnerUnavailable{}
	r := NewRunnerWithClient(c, dkconfig.LLMConfig{MaxTokens: -1, MaxRetries: 1})
	result, err := r.Full(context.Background(), FullRequest{RepoPath: root, DBPath: filepath.Join(t.TempDir(), "graph.db"), RepositoryIdentity: "local:review-negative-tokens"})
	if err == nil || c.calls.Load() != 0 {
		t.Fatalf("invalid local config probe calls=%d err=%v", c.calls.Load(), err)
	}
	t.Logf("status=%s failureKind=%s calls=%d reasons=%v report=%s", result.Status, result.FailureKind, c.calls.Load(), result.Reasons, result.Report)
	if result.FailureKind != "permanent" {
		t.Fatalf("local negative tokens is permanent, got %s", result.FailureKind)
	}
}
