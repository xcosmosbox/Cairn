package extract

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type v013AcceptanceClient struct {
	mu       sync.Mutex
	requests []llm.CompleteRequest
	reply    func(context.Context, llm.CompleteRequest, int) (*llm.CompleteResponse, error)
}

func (*v013AcceptanceClient) ProviderName() string { return "v013-offline-acceptance" }
func (c *v013AcceptanceClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	n := len(c.requests)
	c.mu.Unlock()
	return c.reply(ctx, req, n)
}

func v013Wire(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Name-only IDs used to merge unrelated Atlas objects, slug collisions and
// detail-only distinctions before any model could evaluate their meaning.
func TestV013AcceptanceIdentityAndSourceContributions(t *testing.T) {
	t.Parallel()
	item := func(name, content, detail string) dktypes.AnnotatedItem {
		return dktypes.AnnotatedItem{Tag: dktypes.TagEntity, Name: name, Content: content, Detail: detail, Confidence: 1}
	}
	docs := []*dktypes.AnnotatedDocument{
		{Skill: "operations", FilePath: "references/pump.md", Items: []dktypes.AnnotatedItem{item("Atlas", "pump", "Only above 2 bar; never during maintenance.")}},
		{Skill: "operations", FilePath: "references/library.md", Items: []dktypes.AnnotatedItem{item("Atlas", "software library", "Maintained by Harbor Team; unrelated to pump."), item("Aero Link", "radio channel", "radio"), item("Aero-Link", "steel bracket", "bracket")}},
		{Skill: "operations", FilePath: "references/detail.md", Items: []dktypes.AnnotatedItem{item("Atlas", "pump", "Only above 3 bar; never during maintenance.")}},
		{Skill: "operations", FilePath: "references/duplicate.md", Items: []dktypes.AnnotatedItem{item("Atlas", "pump", "Only above 2 bar; never during maintenance.")}},
	}
	assigned := AssignIDs(docs)
	pump := assigned[0].Items[0].ID
	if pump == assigned[1].Items[0].ID || pump == assigned[2].Items[0].ID || assigned[1].Items[1].ID == assigned[1].Items[2].ID {
		t.Fatal("distinct meaning collided before mapping")
	}
	if pump != assigned[3].Items[0].ID || docs[0].Items[0].ID != "" {
		t.Fatal("true duplicate identity or input immutability failed")
	}
	cs := LookupMemberContributions(assigned, []string{pump})
	if len(cs) != 2 || cs[0].FilePath == cs[1].FilePath || cs[0].CandidateID == "" || cs[1].CandidateID == "" {
		t.Fatal("duplicate lost a candidate-scoped source contribution")
	}
	// A different candidate, source order and duplicate-source addition cannot
	// rename the existing semantic object or churn its first-created UUID.
	reordered := AssignIDs([]*dktypes.AnnotatedDocument{docs[3], docs[2], docs[1], docs[0]})
	if pump != reordered[3].Items[0].ID || NodeUUID([]string{pump}) != NodeUUID([]string{pump, pump}) {
		t.Fatal("repeat churned stable identity")
	}
	subset := AssignIDs(docs[:1])
	if subset[0].Items[0].ID != pump {
		t.Fatal("whole-candidate/source roster leaked into persistent identity")
	}
	newCandidate := *docs[0]
	newCandidate.Items = append(append([]dktypes.AnnotatedItem(nil), newCandidate.Items...), item("unrelated", "new fact", "new detail"))
	if AssignIDs([]*dktypes.AnnotatedDocument{&newCandidate})[0].Items[0].ID != pump {
		t.Fatal("unrelated item churned identity")
	}
	// Grouping receives detail and each source, including a detail-only distinction.
	input := assembleAnnotatedMessage(assigned)
	for _, want := range []string{"Only above 2 bar", "Only above 3 bar", "references/pump.md", "references/duplicate.md", "software library", "steel bracket"} {
		if !strings.Contains(input, want) {
			t.Fatalf("grouping omitted source material %q", want)
		}
	}
}

// Early counts cannot certify a graph after description/UUID/candidate filters.
func TestV013AcceptanceRecomputeActualSurvivorCoverage(t *testing.T) {
	t.Parallel()
	docs := []*dktypes.AnnotatedDocument{{Skill: "s", FilePath: "a.md", Items: []dktypes.AnnotatedItem{{ID: "a", Content: "A"}, {ID: "b", Content: "B"}}}, {Skill: "s", FilePath: "b.md", Items: []dktypes.AnnotatedItem{{ID: "a", Content: "A"}}}}
	res := &Result{Domains: []Domain{{Name: "D", Slug: "d", Subdomains: []Subdomain{{Name: "S", Slug: "s", Entities: []Node{
		{ID: "a-node", Label: "Entity", Name: "A", Description: "A", Confidence: 1, Members: []string{"a"}},
		{ID: "b-node", Label: "Entity", Name: "B", Confidence: 1, Members: []string{"b"}},
	}}}}}}
	early := ComputeFinalCoverage(res, docs)
	if early.Outcome != "complete" || early.InputUnits != 2 || early.CoveredIncidences != 3 {
		t.Fatalf("unexpected initial coverage: %+v", early)
	}
	if err := ValidateFusionCompleteness(res, docs, 0, true); err != nil {
		t.Fatal(err)
	}
	AssignNodeUUIDs(res.Domains)
	selected, _, err := SelectMaterializationCandidates(res, 0)
	if err != nil {
		t.Fatal(err)
	}
	final := ComputeFinalCoverage(selected, docs)
	if final.Outcome != "partial" || final.Reason == "" || final.CoveredUnits != 1 || final.CoveredIncidences != 2 || !reflect.DeepEqual(final.UncoveredIDs, []string{"b"}) || len(final.MissingContributions) != 1 {
		t.Fatalf("stale early coverage survived a real filter: %+v", final)
	}
}
