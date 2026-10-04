package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

func TestFusionCompletenessRejectsUnpublishableNodes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, description, detail, want string
		members                         []string
		confidence                      float64
	}{
		{name: "unknown beside valid member", members: []string{"known", "invented"}, detail: "source detail", description: "generated", confidence: 0.9, want: "unknown member"},
		{name: "no members", detail: "source detail", description: "generated", confidence: 0.9, want: "no source members"},
		{name: "missing source detail", members: []string{"known"}, description: "generated", confidence: 0.9, want: "missing detail"},
		{name: "missing generated description", members: []string{"known"}, detail: "source detail", description: " \n", confidence: 0.9, want: "no generated description"},
		{name: "complete", members: []string{"known"}, detail: "source detail", description: "generated", confidence: 0.9},
		{name: "explicit confidence exclusion preserved", members: []string{"invented"}, confidence: 0.1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := []*dktypes.AnnotatedDocument{{FilePath: "references/source.md", Items: []dktypes.AnnotatedItem{{ID: "known", Detail: tc.detail}}}}
			res := &extract.Result{Domains: []extract.Domain{{Subdomains: []extract.Subdomain{{Concepts: []extract.Node{{Name: "node", Members: tc.members, Description: tc.description, Confidence: tc.confidence}}}}}}}
			err := extract.ValidateFusionCompleteness(res, docs, 0.8, true)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

type incompleteDescriptionLLM struct{ writebackFixtureLLM }

func (c incompleteDescriptionLLM) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	if strings.Contains(req.System, "知识融合重写专家") {
		return &llm.CompleteResponse{Text: `{"descriptions":[]}`, FinishReason: "stop"}, nil
	}
	return c.writebackFixtureLLM.Complete(ctx, req)
}

func TestFullPipelineMissingDescriptionStopsBeforeDatabaseAndWriteback(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: test-skill\ndescription: reliability\n---\n# Skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(root, "references", "doc.md")
	original := "通过幂等键标识请求，服务端识别重试并复用同一结果，避免重复处理造成数据副作用。\n"
	if err := os.WriteFile(doc, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	orch, err := NewOrchestrator(Options{Client: incompleteDescriptionLLM{}, MinConfidence: 0.8, RepositoryIdentity: "local:fixture"})
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "candidate.db")
	report, err := orch.RunFullRebuild(context.Background(), root, dbPath)
	if err == nil || !strings.Contains(err.Error(), "description fusion incomplete") {
		t.Fatalf("incomplete description accepted: report=%+v err=%v", report, err)
	}
	if report.Describe == nil || report.Ingest != nil || report.Writeback != nil {
		t.Fatalf("unexpected stage progression: %+v", report)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("candidate database created: %v", err)
	}
	if actual, err := os.ReadFile(doc); err != nil || string(actual) != original {
		t.Fatalf("source changed: %q %v", actual, err)
	}
}

func TestFullIngestRejectsSilentlySkippedNodesBeforeReplacingDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "candidate.db")
	original := []byte("previous database bytes")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	// Ingest silently skips a domain without its slug, including eligible leaves.
	res := &extract.Result{Domains: []extract.Domain{{Subdomains: []extract.Subdomain{{Concepts: []extract.Node{{ID: "node", Name: "node", Confidence: 0.9}}}}}}}
	o := &Orchestrator{minConf: 0.8}
	if _, err := o.fullRebuildIngest(context.Background(), path, res, nil, nil, "local:fixture"); err == nil || !strings.Contains(err.Error(), "incomplete materialization") {
		t.Fatalf("silently skipped eligible node accepted: %v", err)
	}
	if actual, err := os.ReadFile(path); err != nil || string(actual) != string(original) {
		t.Fatalf("previous database replaced: %q %v", actual, err)
	}
}
