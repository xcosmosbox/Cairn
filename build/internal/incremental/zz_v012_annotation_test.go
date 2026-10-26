package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

type v012AnnotationClient func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error)

func (f v012AnnotationClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	return f(ctx, req)
}
func (v012AnnotationClient) ProviderName() string { return "v012-deterministic" }

func v012Candidate(detail string) *llm.CompleteResponse {
	row := map[string]interface{}{"items": []map[string]interface{}{{"tag": "concept", "name": "source rule", "content": "Source-bound rule", "detail": detail, "confidence": 0.8}}}
	b, _ := json.Marshal(row)
	return &llm.CompleteResponse{Text: string(b)}
}
func v012Assessment(omitted bool) *llm.CompleteResponse {
	if omitted {
		return &llm.CompleteResponse{Text: `{"supported":true,"reason":"missing required prohibition","unsupported_items":[],"omissions":["Preserve the source prohibition"]}`}
	}
	return &llm.CompleteResponse{Text: `{"supported":true,"reason":"source faithful and complete","unsupported_items":[],"omissions":[]}`}
}
func v012Plan(paths ...string) *PipelinePlan {
	p := &PipelinePlan{C4ByDoc: map[string][]TextSegment{}, SkillByDoc: map[string]string{}}
	for _, path := range paths {
		p.C4ByDoc[path] = []TextSegment{{Text: path}}
		p.SkillByDoc[path] = "skill"
	}
	return p
}
func v012Await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for deterministic fixture")
	}
}

// 旧的一次核验会丢失修复机会；批量必须等耗尽终态且不重跑成功兄弟。
// A semantic repair must be document-local; the batch joins every terminal result.
func TestV012C4ThreeDocumentRepairExhaustionAndJoin(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	annotations, assessments := map[string]int{}, map[string]int{}
	reached, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	client := v012AnnotationClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		path := ""
		for _, p := range []string{"immediate.md", "repair.md", "exhaust.md"} {
			if strings.Contains(req.User, p) {
				path = p
				break
			}
		}
		if path == "" {
			return nil, fmt.Errorf("source identity missing: %s", req.User)
		}
		assess := strings.Contains(req.System, "语义核验")
		mu.Lock()
		if assess {
			assessments[path]++
		} else {
			annotations[path]++
		}
		n := assessments[path]
		generated := annotations[path]
		mu.Unlock()
		if !assess {
			return v012Candidate(path + fmt.Sprintf(" candidate %d", generated)), nil
		}
		if path == "exhaust.md" && n == 2 {
			close(reached)
			<-release
		}
		return v012Assessment(path == "exhaust.md" || (path == "repair.md" && generated == 1)), nil
	})
	pp := v012Plan("repair.md", "immediate.md", "exhaust.md")
	input := map[string]string{"immediate.md": "immediate.md source rule", "repair.md": "repair.md source prohibition", "exhaust.md": "exhaust.md source prohibition"}
	done := make(chan *annotateOutput, 1)
	go func() { done <- newAnnotator(client, 1000, 1, 0).annotateC4Docs(context.Background(), pp, input) }()
	v012Await(t, reached)
	select {
	case <-done:
		t.Fatal("batch returned before exhaustion reached a terminal state")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	var out *annotateOutput
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not join")
	}
	if len(out.Results) != 3 || len(out.Docs) != 2 || len(out.Skipped) != 1 || out.Skipped[0].Path != "exhaust.md" {
		t.Fatalf("output=%+v", out)
	}
	for i, path := range []string{"exhaust.md", "immediate.md", "repair.md"} {
		r := out.Results[i]
		if r.FilePath != path || r.Skill != "skill" || r.CandidateID == "" {
			t.Fatalf("identity result=%+v", r)
		}
		want := annotation.DocumentComplete
		if path == "exhaust.md" {
			want = annotation.DocumentPartial
		}
		if r.Outcome != want || (want == annotation.DocumentPartial && (!r.Exhausted || r.Reason == "")) {
			t.Fatalf("terminal result=%+v", r)
		}
		wantSteps := []string{"annotate", "assess"}
		if path != "immediate.md" {
			wantSteps = append(wantSteps, "revise", "assess")
		}
		if len(r.Attempts) != len(wantSteps) {
			t.Fatalf("attempts=%+v", r.Attempts)
		}
		for j, step := range wantSteps {
			if r.Attempts[j].Step != step {
				t.Fatalf("attempt steps=%+v", r.Attempts)
			}
		}
		if r.Document != nil && len(r.Document.Items) > 0 && r.Document.Items[0].ID != "" {
			t.Fatal("member ID assignment mutated assessed candidate evidence")
		}
	}
	for _, doc := range out.Docs {
		if doc.FilePath == "exhaust.md" || len(doc.Items) != 1 || doc.Items[0].ID == "" {
			t.Fatalf("unaccepted or unidentified candidate=%+v", doc)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for path, want := range map[string]int{"immediate.md": 1, "repair.md": 2, "exhaust.md": 2} {
		if annotations[path] != want || assessments[path] != want {
			t.Fatalf("%s annotations=%d assessments=%d want=%d", path, annotations[path], assessments[path], want)
		}
	}
	// Existing unresolved/pruning contract consumes the explicit skip; no failed candidate is an empty replacement.
	resolved, unresolved := resolveAndPruneC4Docs(pp, out, nil)
	if resolved["exhaust.md"] {
		t.Fatal("exhausted source baseline was marked resolved")
	}
	found := false
	for _, u := range unresolved {
		if u.Path == "exhaust.md" && u.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("exhaustion reason lost")
	}
	if input["exhaust.md"] != "exhaust.md source prohibition" {
		t.Fatal("unresolved prose mutated")
	}
}

type v012ProcessorFunc func(context.Context, string, string, string) annotation.DocumentResult

func (f v012ProcessorFunc) Process(ctx context.Context, skill, path, text string) annotation.DocumentResult {
	return f(ctx, skill, path, text)
}

// 取消不应把已经通过的兄弟擦除；终态汇合后仍保留每篇取消原因。
// Cancellation joins pending documents and retains an already completed sibling once.
func TestV012C4CancellationRetainsCompletedSibling(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	complete, pending := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := map[string]int{}
	client := v012AnnotationClient(func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		path := "complete.md"
		if strings.Contains(req.User, "cancel.md") {
			path = "cancel.md"
		}
		mu.Lock()
		calls[path]++
		mu.Unlock()
		if path == "cancel.md" {
			close(pending)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if strings.Contains(req.System, "语义核验") {
			return v012Assessment(false), nil
		}
		return v012Candidate("complete.md retained source"), nil
	})
	processor := annotation.NewDocumentProcessor(client, 1000, 1, 0)
	a := &annotator{processor: v012ProcessorFunc(func(ctx context.Context, skill, path, text string) annotation.DocumentResult {
		result := processor.Process(ctx, skill, path, text)
		if path == "complete.md" {
			close(complete)
		}
		return result
	})}
	done := make(chan *annotateOutput, 1)
	go func() {
		done <- a.annotateC4Docs(ctx, v012Plan("complete.md", "cancel.md"), map[string]string{"complete.md": "complete.md kept source", "cancel.md": "cancel.md unresolved prose"})
	}()
	v012Await(t, complete)
	v012Await(t, pending)
	select {
	case <-done:
		t.Fatal("batch returned before cancellation terminal state")
	default:
	}
	cancel()
	var out *annotateOutput
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not join")
	}
	if len(out.Docs) != 1 || out.Docs[0].FilePath != "complete.md" || len(out.Skipped) != 1 || out.Skipped[0].Path != "cancel.md" || len(out.Results) != 2 {
		t.Fatalf("output=%+v", out)
	}
	if out.Results[0].Outcome != annotation.DocumentCancelled || out.Results[0].Reason == "" || out.Results[1].Outcome != annotation.DocumentComplete {
		t.Fatalf("results=%+v", out.Results)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["complete.md"] != 2 || calls["cancel.md"] != 1 {
		t.Fatalf("caller cancellation reran completed or cancelled work: %v", calls)
	}
}

// 所有生成、反馈修订和核验都只能看到块外原文；UUID 字面量仍是普通来源内容。
// Human blocks stay outside every model request, including repair; outside UUID literals survive.
func TestV012C4HumanBlocksExcludedFromRepair(t *testing.T) {
	t.Parallel()
	const humanID = "11111111-1111-5111-8111-111111111111"
	const literal = "22222222-2222-5222-8222-222222222222"
	full := "<!-- kg:uuid=" + humanID + " tag=entity -->\n## Human unit\nHuman curated text\n<!-- /kg:uuid=" + humanID + " -->\n\nOutside rule " + literal + " prohibits deletion.\n"
	blocks, _, _ := parseKGBlocks(full)
	c4 := buildC4Text(full, blocks)
	calls, generated, assessed := 0, 0, 0
	client := v012AnnotationClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		calls++
		if strings.Contains(req.User, humanID) || strings.Contains(req.User, "Human curated") || !strings.Contains(req.User, literal) {
			return nil, fmt.Errorf("human boundary or source literal violated")
		}
		if strings.Contains(req.System, "语义核验") {
			assessed++
			return v012Assessment(assessed == 1), nil
		}
		generated++
		return v012Candidate("Outside rule " + literal + " prohibits deletion."), nil
	})
	out := newAnnotator(client, 1000, 1, 0).annotateC4Docs(context.Background(), v012Plan("ref.md"), map[string]string{"ref.md": c4})
	if calls != 4 || generated != 2 || assessed != 2 || len(out.Docs) != 1 || len(out.Skipped) != 0 {
		t.Fatalf("calls=%d generated=%d assessed=%d output=%+v", calls, generated, assessed, out)
	}
	if strings.Count(c4, "\n") != strings.Count(full, "\n") || !strings.Contains(out.Docs[0].Items[0].Detail, literal) {
		t.Fatal("source alignment or UUID literal lost")
	}
}

// 不可用候选与合法零产出都不能消耗 C4 散文或推进 baseline。
// Unavailability and a legitimate zero-item result retain existing skip protection.
func TestV012C4UnavailableAndZeroItemsRemainSkipped(t *testing.T) {
	t.Parallel()
	client := v012AnnotationClient(func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
		return nil, fmt.Errorf("fixture provider unavailable")
	})
	out := newAnnotator(client, 1000, -1, 0).annotateC4Docs(context.Background(), v012Plan("unavailable.md"), map[string]string{"unavailable.md": "unresolved source"})
	if len(out.Results) != 1 || out.Results[0].Outcome != annotation.DocumentUnavailable || out.Results[0].Reason == "" || len(out.Skipped) != 1 || len(out.Docs) != 0 {
		t.Fatalf("unavailable output=%+v", out)
	}
	emptyCalls := 0
	emptyClient := v012AnnotationClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		emptyCalls++
		if strings.Contains(req.System, "语义核验") {
			return v012Assessment(false), nil
		}
		return &llm.CompleteResponse{Text: `{"items":[]}`}, nil
	})
	empty := newAnnotator(emptyClient, 1000, -1, 0).annotateC4Docs(context.Background(), v012Plan("empty.md"), map[string]string{"empty.md": "Non-substantive navigation."})
	if emptyCalls != 2 {
		t.Fatalf("nonempty zero-item source was not assessed: calls=%d", emptyCalls)
	}
	if len(empty.Skipped) != 1 || len(empty.Docs) != 0 || len(empty.Results) != 1 || empty.Results[0].Outcome != annotation.DocumentComplete {
		t.Fatalf("zero-item output=%+v", empty)
	}
	resolved, unresolved := resolveAndPruneC4Docs(v012Plan("empty.md"), empty, nil)
	if resolved["empty.md"] || len(unresolved) != 1 {
		t.Fatal("zero-item source was resolved")
	}
	// Legitimate empty input is complete without a provider call, yet still cannot resolve C4.
	neverCalled := v012AnnotationClient(func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
		t.Error("legitimate empty source reached the provider")
		return nil, fmt.Errorf("unexpected provider call")
	})
	blank := newAnnotator(neverCalled, 1000, -1, 0).annotateC4Docs(context.Background(), v012Plan("blank.md"), map[string]string{"blank.md": " \n"})
	if len(blank.Results) != 1 || blank.Results[0].Outcome != annotation.DocumentComplete || len(blank.Skipped) != 1 || len(blank.Docs) != 0 {
		t.Fatalf("legitimate empty output=%+v", blank)
	}
	blankResolved, blankUnresolved := resolveAndPruneC4Docs(v012Plan("blank.md"), blank, nil)
	if blankResolved["blank.md"] || len(blankUnresolved) != 1 {
		t.Fatal("legitimate empty source bypassed C4 skip protection")
	}
}

// 参数只能透传同一全篇恢复额度；不能在 C4 侧再包一层重试。
// Constructor parameters use the same extra-attempt/default contract without an adapter loop.
func TestV012C4RecoveryAllowanceMatchesSharedProcessor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		retries, calls int
	}{{"negative-disables", -1, 1}, {"one-extra", 1, 2}, {"zero-defaults-three", 0, 4}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := v012AnnotationClient(func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
				calls++
				return nil, fmt.Errorf("fixture unavailable")
			})
			out := newAnnotator(client, 1000, tc.retries, 0).annotateC4Docs(context.Background(), v012Plan("ref.md"), map[string]string{"ref.md": "source rule"})
			if calls != tc.calls || len(out.Results) != 1 || len(out.Results[0].Attempts) != tc.calls || out.Results[0].Outcome != annotation.DocumentUnavailable {
				t.Fatalf("calls=%d want=%d output=%+v", calls, tc.calls, out)
			}
		})
	}
}
