package extract

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type identitySequence struct {
	responses []string
	requests  []llm.CompleteRequest
	err       error
}

func (c *identitySequence) ProviderName() string { return "offline-identity" }
func (c *identitySequence) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.requests = append(c.requests, req)
	if c.err != nil {
		return nil, c.err
	}
	i := len(c.requests) - 1
	if i >= len(c.responses) {
		i = len(c.responses) - 1
	}
	return &llm.CompleteResponse{Text: c.responses[i], FinishReason: "stop"}, nil
}

// 名称、slug 和索引曾把不同事实压成同一成员，反过来整篇摘要则会引起无关身份漂移。
func TestV013SemanticIdentityAndAllContributions(t *testing.T) {
	t.Parallel()
	a := dktypes.AnnotatedItem{Tag: dktypes.TagConcept, Name: "Aero Link", Content: "policy", Detail: "only before 2020"}
	b := a
	b.Detail = "never after 2024"
	c := a
	c.Name = "Aero-Link"
	docs := AssignIDs([]*dktypes.AnnotatedDocument{{Skill: "s", FilePath: "a", Items: []dktypes.AnnotatedItem{a, b, c}}, {Skill: "s", FilePath: "b", Items: []dktypes.AnnotatedItem{a}}})
	ids := docs[0].Items
	if ids[0].ID == ids[1].ID || ids[0].ID == ids[2].ID || ids[0].ID != docs[1].Items[0].ID {
		t.Fatal("semantic signatures collapsed or duplicate source changed identity")
	}
	reordered := AssignIDs([]*dktypes.AnnotatedDocument{{Skill: "s", FilePath: "moved", Items: []dktypes.AnnotatedItem{b, a}}})
	if reordered[0].Items[1].ID != ids[0].ID {
		t.Fatal("unrelated/order/path changed identity")
	}
	contributions := LookupMemberContributions(docs, []string{ids[0].ID})
	if len(contributions) != 2 || contributions[0].FilePath != "a" || contributions[1].FilePath != "b" {
		t.Fatalf("lost sources: %+v", contributions)
	}
	prompt := assembleAnnotatedMessage(docs)
	for _, text := range []string{"only before 2020", "never after 2024", "source=a", "source=b"} {
		if !strings.Contains(prompt, text) {
			t.Errorf("prompt missing %q", text)
		}
	}
}

func mappingResponse(nodes []Node) string {
	wire, _ := json.Marshal(map[string]any{"domains": []any{map[string]any{"name": "D", "subdomains": []any{map[string]any{"name": "S", "entities": nodes}}}}})
	return string(wire)
}

// 历史05的首个丢失是 Concept 被放在 entities 数组；修复只重做被解析器剔除的成员。
func TestV013CategoryMismatchTargetedRecovery(t *testing.T) {
	t.Parallel()
	docs := []*dktypes.AnnotatedDocument{{Skill: "s", FilePath: "a", Items: []dktypes.AnnotatedItem{{ID: "good", Content: "good"}, {ID: "lost", Content: "lost"}}}}
	first := mappingResponse([]Node{{ID: "g", Label: "Entity", Name: "Good", Confidence: 1, Members: []string{"good"}}, {ID: "l", Label: "Concept", Name: "Lost", Confidence: 1, Members: []string{"lost"}}})
	second := `{"domains":[{"name":"D","subdomains":[{"name":"S","concepts":[{"id":"l","label":"Concept","name":"Lost","confidence":1,"members":["lost"]}]}]}]}`
	client := &identitySequence{responses: []string{first, second}}
	res, rpt, err := NewExtractor(client, 0).ExtractWithCoverageOptions(context.Background(), docs, CoverageOptions{MaxRetries: 1})
	if err != nil || rpt.Outcome != "complete" || rpt.ExtractAttempts != 2 || rpt.FinalCoverage.CoveredIncidences != 2 {
		t.Fatalf("not repaired: %+v %v", rpt, err)
	}
	if len(res.Domains[0].Subdomains[0].Entities) != 1 || len(res.Domains[0].Subdomains[0].Concepts) != 1 || strings.Contains(client.requests[1].User, "id=good") {
		t.Fatal("successful sibling was rerun/lost")
	}
}

func TestV013CoverageExhaustionAndPostFilterRepair(t *testing.T) {
	t.Parallel()
	docs := []*dktypes.AnnotatedDocument{{Skill: "s", FilePath: "a", Items: []dktypes.AnnotatedItem{{ID: "m", Content: "fact"}}}}
	client := &identitySequence{responses: []string{`{"domains":[]}`}}
	res, rpt, err := NewExtractor(client, 0).ExtractWithCoverageOptions(context.Background(), docs, CoverageOptions{MaxRetries: 1})
	if err != nil || rpt.Outcome != "partial" || !rpt.Exhausted || len(client.requests) != 2 {
		t.Fatalf("exhaustion lost scope: %+v %v", rpt, err)
	}
	client.responses = []string{mappingResponse([]Node{{ID: "n", Label: "Entity", Name: "N", Confidence: 1, Members: []string{"m"}}})}
	res, rpt, err = NewExtractor(client, 0).RepairFinalCoverage(context.Background(), res, docs, CoverageOptions{MaxRetries: -1})
	if err != nil || rpt.Outcome != "complete" {
		t.Fatalf("final repair failed: %v %+v", err, rpt)
	}
	if err := ValidateFusionCompleteness(res, docs, 0, true); err != nil {
		t.Fatal(err)
	}
	final := ComputeFinalCoverage(res, docs)
	if final.Outcome != "partial" || final.CoveredUnits != 0 {
		t.Fatal("pre-description count reused")
	}
}

func TestV013DedupDoesNotUseAliasAsSemanticEvidence(t *testing.T) {
	t.Parallel()
	nodes := []Node{{ID: "same", Label: "Entity", Name: "Pump", Summary: "moves water", Members: []string{"pump"}, SourceSkills: []string{"one"}}, {ID: "same", Label: "Entity", Name: "Library", Summary: "software", Members: []string{"library"}}, {ID: "copy", Label: "Entity", Name: "Pump", Summary: "moves water", Members: []string{"pump"}, SourceSkills: []string{"two"}}}
	domains := []Domain{{Subdomains: []Subdomain{{Entities: nodes}}}}
	if n := DedupFusionNodes(domains); n != 1 {
		t.Fatalf("wrong duplicates %d", n)
	}
	kept := domains[0].Subdomains[0].Entities
	if len(kept) != 2 || kept[0].ID == kept[1].ID || len(kept[0].SourceSkills) != 2 {
		t.Fatalf("alias erased distinct node: %+v", kept)
	}
}

func TestV013RecoveryCanAttachEquivalentMemberWithoutIdentityGrowth(t *testing.T) {
	t.Parallel()
	docs := []*dktypes.AnnotatedDocument{{Skill: "one", FilePath: "one/a", Items: []dktypes.AnnotatedItem{{ID: "a", Name: "Pump", Content: "moves water", Detail: "only above 2 bar"}}}, {Skill: "two", FilePath: "two/b", Items: []dktypes.AnnotatedItem{{ID: "b", Name: "Pump", Content: "moves water", Detail: "only above 2 bar"}}}}
	first := mappingResponse([]Node{{ID: "pump", Label: "Entity", Name: "Pump", Summary: "moves water", Confidence: 1, Members: []string{"a"}}})
	second := mappingResponse([]Node{{ID: "retained#1", Label: "Entity", Name: "Pump", Summary: "moves water", Confidence: 1, Members: []string{"b"}}})
	client := &identitySequence{responses: []string{first, second}}
	res, rpt, err := NewExtractor(client, 0).ExtractWithCoverageOptions(context.Background(), docs, CoverageOptions{MaxRetries: 1})
	if err != nil || rpt.Outcome != "complete" || len(rpt.NeedsDescription) != 1 {
		t.Fatalf("failed attachment: %+v %v", rpt, err)
	}
	nodes := res.Domains[0].Subdomains[0].Entities
	if len(nodes) != 1 || len(nodes[0].Members) != 2 || NodeUUID(nodes[0].Members) != NodeUUID([]string{"a", "b"}) {
		t.Fatal("recovery grew duplicate identity")
	}
	if !strings.Contains(client.requests[1].User, "alias=retained#1") || !strings.Contains(client.requests[1].User, "only above 2 bar") {
		t.Fatal("retained semantic context missing")
	}
	// 后续最终过滤修复只能扩展既有 UUID 的成员；正文待重核，不能被映射响应覆盖。
	nodes[0].ID = "11111111-1111-5111-8111-111111111111"
	nodes[0].Members = []string{"a"}
	nodes[0].Description = "accepted description"
	client.requests = nil
	client.responses = []string{second}
	res, rpt, err = NewExtractor(client, 0).RepairFinalCoverage(context.Background(), res, docs, CoverageOptions{MaxRetries: -1})
	n := res.Domains[0].Subdomains[0].Entities[0]
	if err != nil || n.ID != "11111111-1111-5111-8111-111111111111" || n.Description != "accepted description" || len(rpt.NeedsDescription) != 1 || strings.Contains(client.requests[0].User, n.ID) {
		t.Fatal("pinned identity/prose/private UUID not preserved")
	}
}

func TestV013DuplicateAliasRedirectRemainsObjectBound(t *testing.T) {
	t.Parallel()
	pump := Node{ID: "a", Label: "Entity", Name: "Pump", Members: []string{"pump"}}
	copy := pump
	copy.ID = "b"
	logger := Node{ID: "a", Label: "Entity", Name: "Logger", Members: []string{"log"}}
	target := Node{ID: "c", Label: "Entity", Name: "Target", Members: []string{"target"}}
	domains := []Domain{{Slug: "d", Subdomains: []Subdomain{{Slug: "s1", Entities: []Node{logger, target}, Relations: []Relation{{Source: "b", Target: "c", Kind: "references"}}}, {Slug: "s2", Entities: []Node{pump, copy}}}}}
	DedupFusionNodes(domains)
	AssignNodeUUIDs(domains)
	rels := domains[0].Subdomains[0].Relations
	if len(rels) != 1 || rels[0].Source != NodeUUID([]string{"pump"}) {
		t.Fatalf("redirect guessed local wrong object: %+v", rels)
	}
}

func TestV013RepeatedRetainedProposalDoesNotGrowDuplicateObject(t *testing.T) {
	t.Parallel()
	docs := []*dktypes.AnnotatedDocument{{Skill: "s", FilePath: "a", Items: []dktypes.AnnotatedItem{{ID: "a", Content: "water"}, {ID: "b", Content: "water"}}}}
	res := &Result{Domains: []Domain{{Name: "D", Slug: "d", Subdomains: []Subdomain{{Name: "S", Slug: "s", Entities: []Node{{ID: "pump", Label: "Entity", Name: "Pump", Confidence: 1, Members: []string{"a"}}}}}}}}
	n := Node{ID: "retained#1", Label: "Entity", Name: "Pump", Confidence: 1, Members: []string{"b"}}
	c := &identitySequence{responses: []string{mappingResponse([]Node{n, n})}}
	res, rpt, err := NewExtractor(c, 0).RepairFinalCoverage(context.Background(), res, docs, CoverageOptions{MaxRetries: -1})
	if err != nil || rpt.Outcome != "complete" || len(res.Domains[0].Subdomains[0].Entities) != 1 || len(res.Domains[0].Subdomains[0].Entities[0].Members) != 2 {
		t.Fatalf("repeated attachment grew identity: %+v %v", rpt, err)
	}
}

func TestV013AmbiguousLocalAliasCannotFallThroughToUnrelatedGlobal(t *testing.T) {
	t.Parallel()
	domains := []Domain{{Slug: "d", Subdomains: []Subdomain{{Slug: "one", Entities: []Node{{ID: "same", Label: "Entity", Name: "A", Members: []string{"a"}}, {ID: "same", Label: "Entity", Name: "B", Members: []string{"b"}}, {ID: "t", Label: "Entity", Name: "T", Members: []string{"t"}}}, Relations: []Relation{{Source: "same", Target: "t"}}}, {Slug: "two", Entities: []Node{{ID: "same", Label: "Entity", Name: "Unrelated", Members: []string{"u"}}}}}}}
	DedupFusionNodes(domains)
	AssignNodeUUIDs(domains)
	if len(domains[0].Subdomains[0].Relations) != 0 {
		t.Fatal("ambiguous local alias became unrelated global object")
	}
}
