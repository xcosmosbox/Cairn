package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
)

type incompleteFullRelationClient struct{ next *v016EntryClient }

func (*incompleteFullRelationClient) ProviderName() string { return "offline-incomplete-full-relation" }
func (c *incompleteFullRelationClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	if strings.HasPrefix(req.System, "SOURCE-RELATION COVERAGE") {
		var in struct {
			Focus   string                      `json:"focus_source_id"`
			Sources []struct{ ID, Path string } `json:"original_sources"`
		}
		if err := json.Unmarshal([]byte(req.User), &in); err != nil {
			return nil, err
		}
		for _, source := range in.Sources {
			if source.ID == in.Focus && source.Path == "references/quota.md" {
				return v016Reply(map[string]any{"complete": false, "supported": true, "reason": "controlled unresolved relation", "critique": "retain the original for a later normal repair"})
			}
		}
	}
	return c.next.Complete(ctx, req)
}

func TestV016FullPendingKeepsBlocksBaselineAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	path := "references/record.md"
	const raw = "The team focuses on the hearing.\n"
	update := writeback.NodeUpdate{UUID: "11111111-1111-4111-8111-111111111111", Tag: "Concept", Name: "Hearing attention", Summary: "Hearing attention", Description: strings.TrimSpace(raw), Domain: "Operations", Subdomain: "Rules", DomainSlug: "operations", SubdomainSlug: "rules", Provenance: "extraction", Members: []string{"member"}}
	if err := writeback.RewriteDoc(root, path, []writeback.NodeUpdate{update}, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(root, path))
	baseline, _ := os.ReadFile(filepath.Join(root, path) + ".kg.yaml")
	if !strings.Contains(string(before), raw) {
		t.Fatal("fixture must contain raw text INSIDE a KG block")
	}
	source := extract.FreezeSource("fixture", path, raw)
	relations := &extract.RelationProcessReport{Scopes: []extract.RelationScopeOutcome{{Outcome: annotation.DocumentPartial, Sources: []extract.CrossSource{{Skill: source.Skill, Path: source.Path, SHA256: source.SHA256}}}}}
	for i := 0; i < 2; i++ {
		retained, err := retainIncompleteFullSources(context.Background(), root, []extract.FrozenSource{source}, relations)
		if err != nil || len(retained) != 1 {
			t.Fatalf("pending source unavailable: %v %v", retained, err)
		}
		after, _ := os.ReadFile(filepath.Join(root, path))
		want := append(append(append([]byte(nil), before...), '\n', '\n'), []byte(raw)...)
		if !bytes.Equal(after, want) {
			t.Fatal("managed bytes changed or pending prose duplicated")
		}
		currentBaseline, _ := os.ReadFile(filepath.Join(root, path) + ".kg.yaml")
		if !bytes.Equal(baseline, currentBaseline) {
			t.Fatal("pending work advanced sidecar/editable baseline")
		}
		sidecar, err := writeback.ReadSidecar(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		diff := incremental.DiffDoc(path, sidecar, string(after), false)
		if len(diff.C4) == 0 || len(diff.C1)+len(diff.C2)+len(diff.C3) != 0 {
			t.Fatalf("wrong normal retry boundary: %+v", diff)
		}
	}
}

func TestV016FullPendingRejectsIdentityAndUnsafeOrCancelledWrites(t *testing.T) {
	for _, mode := range []string{"hash", "namespace", "symlink", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := "source.md"
			const raw = "Frozen original.\n"
			if err := os.WriteFile(filepath.Join(root, path), []byte("original target bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			source := extract.FreezeSource("fixture", path, raw)
			binding := extract.CrossSource{Skill: source.Skill, Path: path, SHA256: source.SHA256}
			ctx := context.Background()
			if mode == "hash" {
				binding.SHA256 = "wrong"
			}
			if mode == "namespace" {
				binding.Skill = "other"
			}
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "symlink" {
				target := filepath.Join(t.TempDir(), "target.md")
				if err := os.WriteFile(target, []byte("original target bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(root, path)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, path)); err != nil {
					t.Fatal(err)
				}
			}
			report := &extract.RelationProcessReport{Scopes: []extract.RelationScopeOutcome{{Outcome: annotation.DocumentUnavailable, Sources: []extract.CrossSource{binding}}}}
			if retained, err := retainIncompleteFullSources(ctx, root, []extract.FrozenSource{source}, report); err == nil || len(retained) != 0 {
				t.Fatalf("unsafe pending write accepted: %v %v", retained, err)
			}
			current, err := os.ReadFile(filepath.Join(root, path))
			if err != nil || string(current) != "original target bytes" {
				t.Fatalf("failure mutated target: %q %v", current, err)
			}
		})
	}
}

// Annotation succeeds for both sources; a later relation failure must not
// consume the failed source's raw prose and make the next ordinary Run a no-op.
func TestV016FullIncompleteRelationRetainsPendingOriginal(t *testing.T) {
	root := v016Root(t)
	path := "references/quota.md"
	raw, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	client := &incompleteFullRelationClient{next: &v016EntryClient{}}
	orch, err := NewOrchestrator(Options{Client: client, MaxRetries: -1, RepositoryIdentity: "local:full-pending"})
	if err != nil {
		t.Fatal(err)
	}
	report, runErr := orch.RunFullRebuild(context.Background(), root, filepath.Join(t.TempDir(), "graph.db"))
	if runErr == nil || report.Status != "partial" || report.DocsTotal != 2 || report.DocsRewritten != 2 {
		t.Fatalf("wrong failed scope: %+v %v", report, runErr)
	}
	current, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), string(raw)) {
		t.Fatal("Full consumed exact original after incomplete relation; ordinary Run cannot retry it")
	}
	for _, doc := range []string{path, "SKILL.md"} {
		data, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatal(err)
		}
		sidecar, err := writeback.ReadSidecar(filepath.Join(root, doc))
		if err != nil {
			t.Fatal(err)
		}
		change := incremental.DiffDoc(doc, sidecar, string(data), false)
		if (len(change.C4) > 0) != (doc == path) || len(change.C1)+len(change.C2)+len(change.C3) != 0 {
			t.Fatalf("wrong next ordinary diff %s: %+v", doc, change)
		}
	}
	if client.next.calls["annotate:SKILL.md"] != 1 || client.next.calls["annotate:references/quota.md"] != 1 {
		t.Fatal("retention reran annotation")
	}
}
