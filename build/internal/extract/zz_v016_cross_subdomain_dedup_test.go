package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type crossDedupClient struct {
	mu          sync.Mutex
	mapping     string
	material    map[string]string
	generations map[string]int
	assessments int
}

func (*crossDedupClient) ProviderName() string { return "offline-cross-subdomain-dedup" }
func (c *crossDedupClient) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	text := ""
	switch {
	case strings.Contains(req.System, "领域知识融合专家"):
		text = c.mapping
	case strings.Contains(req.System, "语义核验专家"):
		c.assessments++
		text = nodeSupported
	case strings.Contains(req.System, "节点知识融合重写专家"):
		metadata, err := readNodeWireMetadata(req.User)
		if err != nil || c.material[metadata.Name] == "" {
			return nil, fmt.Errorf("missing synthetic node material or alias: %v", err)
		}
		for _, path := range []string{"source-a.md", "source-b.md"} {
			if !strings.Contains(req.User, path) {
				return nil, fmt.Errorf("source contribution %s was omitted", path)
			}
		}
		c.generations[metadata.Name]++
		summary := metadata.Name + " applies with its source conditions."
		if c.generations[metadata.Name] > 1 {
			// 两次均合法的措辞若分别写在同一 UUID 的候选上，末端必须拒绝冲突。
			// Simulate harmless paraphrase drift from independent duplicate calls.
			summary = "The source conditions qualify " + metadata.Name + "."
		}
		text = nodeWire(metadata.NodeID, summary, c.material[metadata.Name])
	default:
		return nil, fmt.Errorf("unexpected synthetic construction stage")
	}
	return &llm.CompleteResponse{Text: text, FinishReason: "stop"}, nil
}

func crossDedupNodes(domains []Domain) []Node {
	var nodes []Node
	for _, d := range domains {
		for _, sd := range d.Subdomains {
			nodes = append(nodes, sd.Entities...)
			nodes = append(nodes, sd.Concepts...)
		}
	}
	return nodes
}

// 旧去重在子域边界重置：同一映射调用两次融合后措辞不同，末端将两个真实事实全部过滤。
// Exercise ordinary mapping through final selection; raw retention cannot stand in for survivors.
func TestV016CrossSubdomainDedupBeforeFusionPreservesSourcesAndRelations(t *testing.T) {
	t.Parallel()
	material := map[string]string{
		"Control rule": "The controller operates only above 2 units and must not operate during maintenance. The control rule references the cooling rule.",
		"Cooling rule": "Cooling starts only above 30 degrees and stops during maintenance.",
	}
	items := []dktypes.AnnotatedItem{
		{ID: "control", Tag: dktypes.TagConcept, Name: "Control rule", Content: material["Control rule"], Confidence: 1},
		{ID: "cooling", Tag: dktypes.TagConcept, Name: "Cooling rule", Content: material["Cooling rule"], Confidence: 1},
	}
	docs := []*dktypes.AnnotatedDocument{
		{Skill: "one", FilePath: "source-a.md", Items: append([]dktypes.AnnotatedItem(nil), items...)},
		{Skill: "two", FilePath: "source-b.md", Items: append([]dktypes.AnnotatedItem(nil), items...)},
	}
	for _, doc := range docs {
		for i := range doc.Items {
			doc.Items[i].ID = doc.Skill + "-" + doc.Items[i].ID
		}
	}
	nodes := []Node{
		{ID: "control", Label: "Concept", Name: "Control rule", Summary: "Qualified controller operation.", Confidence: 1, Members: []string{"one-control", "two-control"}},
		{ID: "cooling", Label: "Concept", Name: "Cooling rule", Summary: "Qualified cooling operation.", Confidence: 1, Members: []string{"one-cooling", "two-cooling"}},
	}
	relation := Relation{Source: "control", Target: "cooling", Kind: "references", Description: "The control rule references the cooling rule.", Confidence: .8}
	wire, err := json.Marshal(llmOutput{Domains: []Domain{{Name: "Operations", Subdomains: []Subdomain{
		{Name: "First", Concepts: append([]Node(nil), nodes...)},
		{Name: "Second", Concepts: append([]Node(nil), nodes...), Relations: []Relation{relation}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	client := &crossDedupClient{mapping: string(wire), material: material, generations: map[string]int{}}
	extractor := NewExtractor(client, 4096)
	res, mapping, err := extractor.ExtractWithCoverageOptions(context.Background(), docs, CoverageOptions{MaxRetries: -1})
	if err != nil || mapping.Outcome != "complete" {
		t.Fatalf("ordinary mapping failed: %+v %v", mapping, err)
	}
	if mapping.DuplicateNodesMerged != 2 {
		t.Errorf("cross-subdomain duplicates were not removed before fusion: %+v", mapping)
	}
	described := extractor.FuseDescriptions(context.Background(), res, docs)
	if described.NodesTotal != 2 || described.NodesRewritten != 2 || client.generations["Control rule"] != 1 || client.generations["Cooling rule"] != 1 || client.assessments != 2 {
		t.Errorf("logical objects were independently regenerated: %+v generations=%v assessments=%d", described, client.generations, client.assessments)
	}
	AssignNodeUUIDs(res.Domains)
	selected, report, err := SelectMaterializationCandidates(res, 0)
	if err != nil || report.NodesSelected != 2 || report.NodesFiltered != 0 {
		t.Fatalf("faithful logical objects were filtered after prose drift: %+v %v", report, err)
	}
	coverage := ComputeFinalCoverage(selected, docs)
	if coverage.Outcome != "complete" || coverage.InputUnits != 4 || coverage.CoveredUnits != 4 || coverage.InputIncidences != 4 || coverage.CoveredIncidences != 4 {
		t.Fatalf("member or source incidence lost: %+v", coverage)
	}
	for _, node := range crossDedupNodes(selected.Domains) {
		if node.ID != NodeUUID(node.Members) || node.Description != material[node.Name] || len(LookupMemberContributions(docs, node.Members)) != 2 || len(node.SourceSkills) != 2 {
			t.Fatalf("member-derived identity, facts or source bindings changed: %+v", node)
		}
	}
	relations := selected.Domains[0].Subdomains[1].Relations
	want := relation
	want.Source, want.Target = NodeUUID(nodes[0].Members), NodeUUID(nodes[1].Members)
	if len(relations) != 1 || relations[0] != want {
		t.Fatalf("old relation identity or assertion lost: %+v", relations)
	}
}

// 移走的端点若只换 ID，旧子域同名对象会截走引用；旧 qualified frame 也必须更新归属。
// Both ordinary and already-qualified references follow the exact object across scopes.
func TestV016CrossSubdomainDedupRedirectsQualifiedScopeNotLocalNamesake(t *testing.T) {
	t.Parallel()
	a := Node{ID: "shared", Label: "Concept", Name: "Source", Summary: "Source fact", Members: []string{"source"}, Confidence: 1}
	b := Node{ID: "shared-target", Label: "Concept", Name: "Target", Summary: "Target fact", Members: []string{"target"}, Confidence: 1}
	copyA, copyB := a, b
	copyA.ID, copyB.ID = "old", "old-target"
	otherA := Node{ID: a.ID, Label: "Entity", Name: "Different source", Members: []string{"other-source"}, Confidence: 1}
	otherB := Node{ID: b.ID, Label: "Entity", Name: "Different target", Members: []string{"other-target"}, Confidence: 1}
	beta := endpointScope{"d", "beta"}
	plain := Relation{Source: copyA.ID, Target: copyB.ID, Kind: "references", Description: "An attributed conditional reference.", Confidence: .7}
	framed := plain
	framed.Source = qualifiedRelationAlias(endpointRef{beta, &copyA})
	framed.Target = qualifiedRelationAlias(endpointRef{beta, &copyB})
	domains := []Domain{{Slug: "d", Subdomains: []Subdomain{
		{Slug: "alpha", Concepts: []Node{a, b}},
		{Slug: "beta", Entities: []Node{otherA, otherB}, Concepts: []Node{copyA, copyB}, Relations: []Relation{plain}},
		{Slug: "gamma", Relations: []Relation{framed}},
	}}}
	if n := DedupFusionNodes(domains); n != 2 {
		t.Fatalf("wrong exact-duplicate count %d", n)
	}
	index := indexEndpoints(domains)
	for _, si := range []int{1, 2} {
		sd := domains[0].Subdomains[si]
		source, target, ss, ts := index.relation(endpointScope{"d", sd.Slug}, sd.Relations[0])
		if ss != "resolved" || ts != "resolved" || source.scope.subdomain != "alpha" || target.scope.subdomain != "alpha" || source.node.Name != a.Name || target.node.Name != b.Name {
			t.Fatalf("redirect followed stale scope or unrelated local alias: %s %+v %s %+v", ss, source, ts, target)
		}
	}
	AssignNodeUUIDs(domains)
	want := plain
	want.Source, want.Target = NodeUUID(a.Members), NodeUUID(b.Members)
	for _, si := range []int{1, 2} {
		if got := domains[0].Subdomains[si].Relations; len(got) != 1 || got[0] != want {
			t.Fatalf("qualified object binding or relation assertion changed: %+v", got)
		}
	}
}

// 新生成的 frame 可能恰好是另一节点的字面 ID；沿用 resolver 优先级但不能把边交给该对象。
// A literal alias must not hijack a newly framed reference to a different semantic object.
func TestV016CrossSubdomainDedupRejectsFrameHijackedByLiteralAlias(t *testing.T) {
	t.Parallel()
	for _, hijackerScope := range []string{"beta", "gamma"} {
		t.Run(hijackerScope, func(t *testing.T) {
			a := Node{ID: "canonical", Label: "Concept", Name: "A", Members: []string{"a"}, Confidence: 1}
			copyA := a
			copyA.ID = "old"
			target := Node{ID: "target", Label: "Entity", Name: "T", Members: []string{"t"}, Confidence: 1}
			frame := qualifiedRelationAlias(endpointRef{endpointScope{"d", "alpha"}, &a})
			hijacker := Node{ID: frame, Label: "Concept", Name: "Other object", Members: []string{"other"}, Confidence: 1}
			domains := []Domain{{Slug: "d", Subdomains: []Subdomain{
				{Slug: "alpha", Concepts: []Node{a}},
				{Slug: "beta", Concepts: []Node{copyA}, Entities: []Node{target}, Relations: []Relation{{Source: "old", Target: "target", Kind: "references"}}},
				{Slug: "gamma"},
			}}}
			for si := range domains[0].Subdomains {
				if domains[0].Subdomains[si].Slug == hijackerScope {
					domains[0].Subdomains[si].Concepts = append(domains[0].Subdomains[si].Concepts, hijacker)
				}
			}
			scope := endpointScope{"d", "beta"}
			before, status := indexEndpoints(domains).resolve(scope, "old")
			if status != "resolved" || before.node.Name != a.Name {
				t.Fatal("original reference was not uniquely bound to the intended copy")
			}
			if n := DedupFusionNodes(domains); n != 1 {
				t.Fatalf("wrong exact duplicate count: %d", n)
			}
			if _, status := indexEndpoints(domains).resolve(scope, domains[0].Subdomains[1].Relations[0].Source); status == "resolved" {
				t.Fatal("new framed alias was intercepted by an unrelated raw identity")
			}
			AssignNodeUUIDs(domains)
			if len(domains[0].Subdomains[1].Relations) != 0 || len(crossDedupNodes(domains)) != 3 {
				t.Fatal("unsafe edge was admitted or a distinct object was removed")
			}
		})
	}
}

func TestV016CrossSubdomainDedupKeepsTrueAmbiguityAndDifferentDomains(t *testing.T) {
	t.Parallel()
	a := Node{ID: "same", Label: "Concept", Name: "A", Members: []string{"a"}, Confidence: 1}
	b := Node{ID: "same", Label: "Concept", Name: "B", Members: []string{"b"}, Confidence: 1}
	target := Node{ID: "target", Label: "Entity", Name: "T", Members: []string{"t"}, Confidence: 1}
	beta := endpointScope{"d", "beta"}
	domains := []Domain{{Slug: "d", Subdomains: []Subdomain{
		{Slug: "alpha", Concepts: []Node{a}},
		{Slug: "beta", Concepts: []Node{a, b}, Entities: []Node{target}, Relations: []Relation{
			{Source: "same", Target: "target"},
			{Source: qualifiedRelationAlias(endpointRef{beta, &a}), Target: "target"},
		}},
	}}}
	if n := DedupFusionNodes(domains); n != 1 {
		t.Errorf("exact copy not removed: %d", n)
	}
	AssignNodeUUIDs(domains)
	if len(domains[0].Subdomains[1].Relations) != 0 || len(crossDedupNodes(domains)) != 3 {
		t.Fatal("true old alias ambiguity became a guessed endpoint or erased distinct objects")
	}
	crossDomains := []Domain{{Slug: "one", Subdomains: []Subdomain{{Slug: "s", Concepts: []Node{a}}}}, {Slug: "two", Subdomains: []Subdomain{{Slug: "s", Concepts: []Node{a}}}}}
	if n := DedupFusionNodes(crossDomains); n != 0 || len(crossDedupNodes(crossDomains)) != 2 {
		t.Fatal("same-domain repair silently merged across domains")
	}
}

func TestV016CrossSubdomainDedupRequiresEverySemanticField(t *testing.T) {
	t.Parallel()
	changes := []struct {
		name string
		edit func(*Node)
	}{
		{"label", func(n *Node) { n.Label = "Entity" }},
		{"name", func(n *Node) { n.Name = "Other" }},
		{"summary", func(n *Node) { n.Summary = "A different assertion" }},
		{"description", func(n *Node) { n.Description = "A different detail" }},
		{"members", func(n *Node) { n.Members = []string{"other-member"} }},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			a := Node{ID: "same", Label: "Concept", Name: "Rule", Summary: "Qualified fact", Description: "Complete detail", Members: []string{"member"}, Confidence: 1}
			b := a
			change.edit(&b)
			domains := []Domain{{Slug: "d", Subdomains: []Subdomain{{Slug: "a", Concepts: []Node{a}}, {Slug: "b", Concepts: []Node{b}}}}}
			if n := DedupFusionNodes(domains); n != 0 || len(crossDedupNodes(domains)) != 2 {
				t.Fatal("alias or partial semantic equality merged a different object")
			}
		})
	}
}

// 导出 helper 仍可能收到未经过 parser 的手工节点；新增 pass 不能吞掉原本分开的坏分类。
// Do not infer or normalize missing/mismatched labels while extending cross-scope dedup.
func TestV016CrossSubdomainDedupDoesNotMergeInvalidContainerLabels(t *testing.T) {
	t.Parallel()
	for _, crossScope := range []bool{false, true} {
		for _, label := range []string{"", "Entity", "Concept"} {
			t.Run(fmt.Sprintf("cross=%t/label=%q", crossScope, label), func(t *testing.T) {
				a := Node{ID: "entity-slot", Label: label, Name: "Same text", Summary: "Same assertion", Members: []string{"member"}, Confidence: 1}
				b := a
				b.ID = "concept-slot"
				subdomains := []Subdomain{{Slug: "alpha", Entities: []Node{a}}}
				if crossScope {
					subdomains = append(subdomains, Subdomain{Slug: "beta", Concepts: []Node{b}})
				} else {
					subdomains[0].Concepts = []Node{b}
				}
				domains := []Domain{{Slug: "d", Subdomains: subdomains}}
				if n := DedupFusionNodes(domains); n != 0 {
					t.Fatalf("new pass merged invalid classification: merged=%d", n)
				}
				kept := crossDedupNodes(domains)
				if len(kept) != 2 || !reflect.DeepEqual(kept[0], a) || !reflect.DeepEqual(kept[1], b) {
					t.Fatal("missing/mismatched label was inferred or object was removed")
				}
			})
		}
	}
}

func TestV016CrossSubdomainDedupFirstClassificationMetadataAndIdempotence(t *testing.T) {
	t.Parallel()
	for _, reverse := range []bool{false, true} {
		a := Node{ID: "a", Label: "Concept", Name: "Rule", Summary: "Qualified fact", Members: []string{"one", "two"}, Synonyms: []string{"A"}, SourceSkills: []string{"first"}, Confidence: .4, FileSlug: "first-primary"}
		b := a
		b.ID, b.Members, b.Synonyms, b.SourceSkills, b.Confidence, b.FileSlug = "b", []string{"two", "one", "one"}, []string{"B"}, []string{"second"}, .9, "second-primary"
		subdomains := []Subdomain{{Slug: "alpha", Concepts: []Node{a}}, {Slug: "beta", Concepts: []Node{b}}}
		if reverse {
			subdomains[0], subdomains[1] = subdomains[1], subdomains[0]
		}
		first := subdomains[0].Concepts[0]
		domains := []Domain{{Slug: "d", Subdomains: subdomains}}
		if n := DedupFusionNodes(domains); n != 1 || len(domains[0].Subdomains[0].Concepts) != 1 || len(domains[0].Subdomains[1].Concepts) != 0 {
			t.Fatal("canonical classification did not follow existing traversal order")
		}
		node := domains[0].Subdomains[0].Concepts[0]
		if node.ID != first.ID || node.FileSlug != first.FileSlug || node.Confidence != .9 || NodeUUID(node.Members) != NodeUUID([]string{"one", "two"}) {
			t.Fatalf("canonical identity/metadata/member-set changed: %+v", node)
		}
		for _, values := range [][]string{node.Synonyms, node.SourceSkills} {
			if len(values) != 2 {
				t.Fatal("metadata union lost a contributor")
			}
		}
		skills := append([]string(nil), node.SourceSkills...)
		sort.Strings(skills)
		if !reflect.DeepEqual(skills, []string{"first", "second"}) {
			t.Fatal("source skills were not conserved")
		}
		before, _ := json.Marshal(domains)
		if n := DedupFusionNodes(domains); n != 0 {
			t.Fatal("repeated calibration changed an already canonical graph")
		}
		after, _ := json.Marshal(domains)
		if string(before) != string(after) {
			t.Fatal("deduplication was not idempotent")
		}
	}
}
