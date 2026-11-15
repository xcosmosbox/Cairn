package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

// A zero-call unchanged phase cannot expose a stale caller cache. Serialize a
// recovered pending source, then a changed old source, and consume those exact
// bindings in the same source-grounded relation helper used by changed builds.
func TestV016CallerContextRepairThenChanged(t *testing.T) {
	t.Parallel()
	const skill = "fixture"
	const oldPath, pendingPath = "fixture/references/old.md", "fixture/references/pending.md"
	makeDoc := func(path, name, fact string) *dktypes.AnnotatedDocument {
		return &dktypes.AnnotatedDocument{Skill: skill, FilePath: path, Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagEntity, Name: name, Content: fact, Detail: fact, Confidence: .9}}}
	}
	old := makeDoc(oldPath, "Pump", "The pump limit is 8 bar before June.")
	pending := makeDoc(pendingPath, "Battery", "Battery B has capacity 12 ampere-hours.")
	changed := makeDoc(oldPath, "Pump", "From June the pump limit is 6 bar.")
	pending.Items[0].SourceSpan = &dktypes.SourceSpan{StartLine: 90, EndLine: 90, Quote: pending.Items[0].Content}
	changed.Items[0].SourceSpan = &dktypes.SourceSpan{StartLine: 900, EndLine: 900, Quote: changed.Items[0].Content}
	originals := []extract.FrozenSource{extract.FreezeSource(skill, oldPath, old.Items[0].Content+"\n\n"+changed.Items[0].Content), extract.FreezeSource(skill, pendingPath, pending.Items[0].Content)}
	previous := extract.AssignIDs([]*dktypes.AnnotatedDocument{old})
	before, _ := json.Marshal([]any{previous, pending, changed})
	complete := func(d *dktypes.AnnotatedDocument) annotation.DocumentResult {
		return annotation.DocumentResult{Skill: d.Skill, FilePath: d.FilePath, Document: d, Outcome: annotation.DocumentComplete}
	}
	roundTrip := func(name string, docs []*dktypes.AnnotatedDocument) []*dktypes.AnnotatedDocument {
		path := filepath.Join(t.TempDir(), name+".json")
		if err := writeNew(path, docs); err != nil {
			t.Fatal(err)
		}
		var restored []*dktypes.AnnotatedDocument
		if err := readJSON(path, &restored); err != nil {
			t.Fatal(err)
		}
		return restored
	}
	repaired := roundTrip("repair", v016CallerDocuments(previous, []annotation.DocumentResult{complete(pending)}, originals))
	results := []annotation.DocumentResult{complete(changed)}
	for _, outcome := range []annotation.DocumentOutcome{annotation.DocumentPartial, annotation.DocumentUnavailable, annotation.DocumentCancelled} {
		bad := makeDoc(oldPath, "Unsupported", "not accepted")
		r := complete(bad)
		r.Outcome = outcome
		results = append(results, r)
	}
	results = append(results, annotation.DocumentResult{Outcome: annotation.DocumentComplete}, complete(&dktypes.AnnotatedDocument{Skill: skill, FilePath: oldPath}))
	next := roundTrip("changed", v016CallerDocuments(repaired, results, originals))
	if len(next) != 3 || next[1].Items[0].SourceSpan.StartLine != 1 || next[2].Items[0].SourceSpan.StartLine != 3 {
		t.Fatalf("accepted old/repaired/changed material or original spans lost: %+v", next)
	}
	if len(v016CallerDocuments(next, results, originals)) != len(next) {
		t.Fatal("identical accepted cache grew")
	}
	after, _ := json.Marshal([]any{previous, pending, changed})
	if string(before) != string(after) {
		t.Fatal("caller or factual report was mutated")
	}
	assigned := extract.AssignIDs([]*dktypes.AnnotatedDocument{old, pending, changed})
	wanted := map[string]bool{}
	for _, doc := range assigned {
		for _, item := range doc.Items {
			wanted[item.ID] = true
		}
	}
	for _, tc := range []struct {
		name     string
		docs     []*dktypes.AnnotatedDocument
		complete bool
	}{{"stale-predecessor-control", previous, false}, {"restored-repair-then-changed", next, true}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := clientFunc(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				calls++
				if tc.complete {
					var input struct {
						Material []struct {
							Items []dktypes.AnnotatedItem `json:"items"`
						} `json:"member_material"`
					}
					if err := json.Unmarshal([]byte(req.User), &input); err != nil {
						t.Fatal(err)
					}
					seen := map[string]bool{}
					for _, material := range input.Material {
						for _, item := range material.Items {
							seen[item.ID] = true
						}
					}
					if len(seen) != len(wanted) {
						t.Fatalf("next changed helper consumed stale or rejected material: %v", seen)
					}
					for member := range wanted {
						if !seen[member] {
							t.Fatalf("actual assigned member missing: %s", member)
						}
					}
				}
				text := `{"relations":[]}`
				if strings.HasPrefix(req.System, "SOURCE-RELATION COVERAGE") {
					text = `{"complete":true,"supported":true,"reason":"controlled facts have no enum-supported edge"}`
				}
				return &llm.CompleteResponse{Text: text, FinishReason: "stop"}, nil
			})
			if _, err := incremental.NewIncrementalOrchestrator(incremental.Options{Client: client, Sources: originals, SourceDocuments: tc.docs}); err != nil {
				t.Fatalf("next changed entry rejects restored caller identity: %v", err)
			}
			graph := &extract.Result{Domains: []extract.Domain{{Slug: "equipment", Subdomains: []extract.Subdomain{{Slug: "devices", Entities: []extract.Node{{ID: "pump", Label: "Entity", Name: "Pump", Members: []string{assigned[0].Items[0].ID, assigned[2].Items[0].ID}}, {ID: "battery", Label: "Entity", Name: "Battery", Members: []string{assigned[1].Items[0].ID}}}}}}}}
			report := extract.NewExtractor(client, 4096).ProcessRelations(context.Background(), graph, tc.docs, originals, extract.RelationProcessOptions{MaxRetries: -1})
			allComplete := len(report.Scopes) == 2
			for _, scope := range report.Scopes {
				allComplete = allComplete && scope.Outcome == annotation.DocumentComplete
			}
			if allComplete != tc.complete || calls == 0 {
				t.Fatalf("changed consumption status=%+v calls=%d", report.Scopes, calls)
			}
		})
	}
}

func TestV016CallerOriginalsRestorePartialWithoutAcceptingCandidates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const marker = "different-folder/SKILL.md"
	const healthy = "different-folder/references/healthy.md"
	const pending = "different-folder/references/pending.md"
	for path, body := range map[string]string{marker: "---\nname: canonical\n---\n", healthy: "Original healthy fact.\n", pending: "Pending original fact.\r\n"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	frozenTree := filepath.Join(t.TempDir(), "original")
	roster, err := copyV016Tree(root, frozenTree, true)
	if err != nil {
		t.Fatal(err)
	}
	all, err := v016CallerOriginals(nil, frozenTree, roster)
	if err != nil || len(all) != 3 {
		t.Fatalf("complete raw input unavailable: %+v %v", all, err)
	}
	var partial []extract.FrozenSource
	for _, source := range all {
		if source.Skill != "canonical" {
			t.Fatal("folder name replaced canonical skill")
		}
		if source.Path != pending {
			partial = append(partial, source)
		}
	}
	for i, source := range partial {
		if source.Path == healthy {
			partial[i] = extract.FreezeSource(source.Skill, source.Path, source.Content+"New current fact.\n")
		}
	}
	restored, err := v016CallerOriginals(partial, frozenTree, roster)
	if err != nil || len(restored) != 3 {
		t.Fatalf("partial predecessor not restored: %+v %v", restored, err)
	}
	for _, source := range restored {
		if source.Path == healthy && !strings.Contains(source.Content, "New current fact.") {
			t.Fatal("current changed snapshot overwritten by base raw source")
		}
		if source.Path == pending && source.Content != "Pending original fact.\r\n" {
			t.Fatal("pending raw bytes changed")
		}
	}
	accepted := extract.AssignIDs([]*dktypes.AnnotatedDocument{{Skill: "canonical", FilePath: healthy, Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagConcept, Name: "Healthy fact", Content: "Original healthy fact."}}}})
	if len(v016CallerDocuments(accepted, nil, restored)) != 1 {
		t.Fatal("raw availability promoted pending annotation")
	}
	if err := extract.ValidateSourceContext(restored, accepted); err != nil {
		t.Fatal(err)
	}
	bad := append([]extract.FrozenSource(nil), partial...)
	bad[0].Skill = "wrong-namespace"
	if _, err := v016CallerOriginals(bad, frozenTree, roster); err == nil {
		t.Fatal("conflicting namespace silently adopted")
	}
	if err := os.WriteFile(filepath.Join(frozenTree, pending), []byte("different input"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := v016CallerOriginals(partial, frozenTree, roster); err == nil {
		t.Fatal("changed frozen bytes accepted")
	}
}

func TestV016UnavailablePredecessorCannotRunDependentPhase(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "fixed")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "original.md"), []byte("retained original"), 0600); err != nil {
		t.Fatal(err)
	}
	oldPhase, oldTree, oldPrevious := *v016Phase, *v016SourceTree, *v016Previous
	defer func() { *v016Phase, *v016SourceTree, *v016Previous = oldPhase, oldTree, oldPrevious }()
	*v016Phase, *v016SourceTree, *v016Previous = "unchanged", source, filepath.Join(root, "missing-full")
	out := filepath.Join(root, "dependent")
	if err := runV016Phase(out, 32768, time.Second, 1); err == nil {
		t.Fatal("missing baseline became a completed in-place check")
	}
	raw, err := os.ReadFile(filepath.Join(out, "phase-finish.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Status           string `json:"status"`
		ProviderAttempts int    `json:"provider_attempts"`
	}
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "unavailable" || receipt.ProviderAttempts != 0 {
		t.Fatalf("dependent receipt=%s", raw)
	}
	if _, err = os.Stat(filepath.Join(out, "requests.jsonl")); !os.IsNotExist(err) {
		t.Fatal("dependent phase initialized provider transport")
	}
	var roster []v016SourceIdentity
	if err = readJSON(filepath.Join(out, "source-roster.json"), &roster); err != nil || len(roster) != 2 {
		t.Fatalf("denominator lost: %+v %v", roster, err)
	}
}

// An unavailable shared identity used to invite one identical failing request
// per source. Only one actual denial may be sent; all sources keep local errors.
func TestV016AuthenticationDenialStopsSiblingRequests(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := &v016AuthClient{next: clientFunc(func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
		calls.Add(1)
		return nil, fmt.Errorf("openai_compatible: status 401: Authentication Fails")
	})}
	var workers sync.WaitGroup
	for i := 0; i < 14; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if response, err := client.Complete(context.Background(), llm.CompleteRequest{}); response != nil || err == nil || !strings.Contains(err.Error(), "status 401:") {
				t.Errorf("denial became semantic/provider success: response=%v err=%v", response, err)
			}
		}()
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("provider calls=%d, want one authentic denial", calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Complete(ctx, llm.CompleteRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sibling=%v", err)
	}
}

func TestV016NonAuthenticationFailureDoesNotStopIndependentSources(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := &v016AuthClient{next: clientFunc(func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if calls.Add(1) == 1 {
			return nil, fmt.Errorf("openai_compatible: status 503: unavailable")
		}
		return &llm.CompleteResponse{Text: "actual next response"}, nil
	})}
	if _, err := client.Complete(context.Background(), llm.CompleteRequest{}); err == nil {
		t.Fatal("first failure disappeared")
	}
	if response, err := client.Complete(context.Background(), llm.CompleteRequest{}); err != nil || response == nil || calls.Load() != 2 {
		t.Fatalf("independent source stopped: response=%v err=%v calls=%d", response, err, calls.Load())
	}
}

// Builder preparation must copy exact original bytes and empty legitimate
// markers, without accepting a neighboring inventory/expectation JSON file.
func TestV016SourceTreeCopiesExactBytesAndRejectsInventoryOrLinks(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"inventory", "link"} {
		t.Run(invalid, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "sources")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "SKILL.md"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			original := []byte("qualified fact\r\nUUID 123e4567-e89b-12d3-a456-426614174000\n")
			if err := os.WriteFile(filepath.Join(source, "original.md"), original, 0600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "prepared")
			roster, err := copyV016Tree(source, out, true)
			if err != nil || len(roster) != 2 {
				t.Fatalf("roster=%v err=%v", roster, err)
			}
			copied, err := os.ReadFile(filepath.Join(out, "original.md"))
			if err != nil || string(copied) != string(original) {
				t.Fatal("authoritative bytes changed")
			}
			if invalid == "inventory" {
				err = os.WriteFile(filepath.Join(source, "inventory.json"), []byte(`{"expected":"secret judge material"}`), 0600)
			} else {
				err = os.Symlink(filepath.Join(source, "original.md"), filepath.Join(source, "linked.md"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = copyV016Tree(source, filepath.Join(root, "rejected"), true); err == nil {
				t.Fatal("non-source evidence/link accepted")
			}
		})
	}
}
