package extract

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

func boundedTestID(name string) string  { return uuid.NewSHA1(uuid.Nil, []byte(name)).String() }
func boundedTestPin(name string) string { return boundedHash([]byte(name)) }
func boundedTestMaterialBytes(n boundedNode) int {
	var projection []map[string]string
	for _, incidence := range n.incidences {
		projection = append(projection, map[string]string{"doc_id": incidence.Source.DocID, "member_id": incidence.Item.ID, "path": incidence.Source.Path, "original_sha256": incidence.Source.OriginalSHA256, "name": incidence.Item.Name, "content": incidence.Item.Content, "detail": incidence.Item.Detail})
	}
	return len(boundedJSON(projection))
}
func boundedTestInput(t *testing.T, nodes, subdomains, documents int) BoundedStageInput {
	t.Helper()
	in := BoundedStageInput{Graph: &Result{}, Identity: BoundedStageIdentity{CodeSourceCommit: strings.Repeat("1", 40), CorpusSHA256: boundedTestPin("corpus"), RosterSHA256: boundedTestPin("complete roster"), AcceptedSetSHA256: boundedTestPin("accepted checkpoints"), MethodSHA256: boundedTestPin("bounded extraction method"), ParentCatalogueSHA256: boundedTestPin("mapping80 catalogue")}}
	domain := Domain{Slug: "domain", Name: "Domain", Summary: "Full domain summary", SourceSkills: []string{"source"}}
	for s := 0; s < subdomains; s++ {
		domain.Subdomains = append(domain.Subdomains, Subdomain{Slug: fmt.Sprintf("subdomain%d", s), Name: fmt.Sprintf("Subdomain%d", s), Summary: "Full subdomain summary"})
	}
	for i := 0; i < nodes; i++ {
		member := fmt.Sprintf("original-unit-%03d", i)
		node := Node{ID: boundedTestID(fmt.Sprintf("node%d", i)), Label: "Entity", Name: fmt.Sprintf("Node %d <script> \" && OR ∑", i), Summary: "Complete summary", Description: fmt.Sprintf("Complete original description %d", i), Confidence: .8, Members: []string{member}, SourceSkills: []string{"source"}, Synonyms: []string{"full synonym"}}
		domain.Subdomains[i%subdomains].Entities = append(domain.Subdomains[i%subdomains].Entities, node)
	}
	in.Graph.Domains = []Domain{domain}
	for d := 0; d < documents; d++ {
		docID := boundedTestPin(fmt.Sprintf("doc%d", d))
		src := BoundedStageSource{DocID: docID, Skill: "source", Path: "references/" + docID + ".md", OriginalSHA256: boundedTestPin(fmt.Sprintf("original%d", d)), AcceptedCheckpointSHA256: boundedTestPin(fmt.Sprintf("accepted%d", d))}
		in.Sources = append(in.Sources, src)
		doc := &dktypes.AnnotatedDocument{Skill: src.Skill, FilePath: src.Path}
		for i := 0; i < nodes; i++ {
			detail := fmt.Sprintf("FULL source%d item%d with end marker END-%d-%d", d, i, d, i)
			if i == 0 {
				detail = ""
			}
			doc.Items = append(doc.Items, dktypes.AnnotatedItem{ID: fmt.Sprintf("original-unit-%03d", i), Tag: dktypes.TagEntity, Name: fmt.Sprintf("Full material name%d", i), Content: fmt.Sprintf("Full material content%d fromsource%d \"{}&& OR\" 中文", i, d), Detail: detail, Confidence: .9, SourceSpan: &dktypes.SourceSpan{StartLine: i + 1, EndLine: i + 1, Quote: fmt.Sprintf("full quote%d-%d", d, i)}})
		}
		in.Documents = append(in.Documents, doc)
	}
	return boundedTestRebind(t, in)
}
func boundedTestRebind(t *testing.T, in BoundedStageInput) BoundedStageInput {
	t.Helper()
	identity, e := ComputeBoundedStageIdentity(in)
	if e != nil {
		t.Fatal(e)
	}
	in.Identity = identity
	in.Expected = identity
	return in
}
func boundedTestCross(t *testing.T, in BoundedStageInput, pairs []BoundedPair) BoundedCrossPairs {
	t.Helper()
	cross := BoundedCrossPairs{CatalogueSHA256: in.Identity.CatalogueSHA256, DescriptionSHA256: in.Identity.DescriptionSHA256, Pairs: pairs}
	var e error
	cross.ExpectedSHA256, e = BoundedCrossPairDigest(cross)
	if e != nil {
		t.Fatal(e)
	}
	return cross
}
func boundedTestPlan(t *testing.T, in BoundedStageInput, relation bool) *BoundedStagePlan {
	t.Helper()
	var p *BoundedStagePlan
	var e error
	if relation {
		p, e = BuildBoundedRelationPlan(in, boundedTestCross(t, in, nil))
	} else {
		p, e = BuildBoundedDescriptionPlan(in)
	}
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func boundedTestResponse(t *testing.T, w BoundedStageWork, allKinds bool) *llm.CompleteResponse {
	t.Helper()
	var rows []any
	if w.plan.stage == boundedDescription {
		for i := 0; i < w.hi-w.lo; i++ {
			rows = append(rows, map[string]any{"node": fmt.Sprintf("n%d", i+1), "description": fmt.Sprintf("Complete synthesized description %d 中文", i)})
		}
		return &llm.CompleteResponse{FinishReason: "stop", Text: string(boundedJSON(map[string]any{"descriptions": rows}))}
	}
	for i := 0; i < w.hi-w.lo; i++ {
		tuples := []any{}
		if allKinds {
			for dir := 0; dir < 2; dir++ {
				for _, kind := range []string{"triggers", "depends_on", "references", "generalizes", "composes", "contradicts"} {
					tuples = append(tuples, []any{dir, kind, .9})
				}
			}
		}
		rows = append(rows, map[string]any{"pair": fmt.Sprintf("p%d", i+1), "relations": tuples})
	}
	return &llm.CompleteResponse{FinishReason: "stop", Text: string(boundedJSON(map[string]any{"pairs": rows}))}
}
func boundedTestDeltas(t *testing.T, p *BoundedStagePlan) []BoundedStageDelta {
	t.Helper()
	var deltas []BoundedStageDelta
	for _, w := range p.Works() {
		d, e := w.Validate(boundedTestResponse(t, w, false))
		if e != nil {
			t.Fatal(e)
		}
		deltas = append(deltas, d)
	}
	return deltas
}

// 原风险：共享member的后一个来源覆盖前一个，空Detail被当作没有素材，或真UUID流入prompt。
// Every complete source incidence survives, including optional empty Detail and hostile source text.
func TestBoundedDescriptionsFullSharedIncidencesAndAliases(t *testing.T) {
	t.Parallel()
	in := boundedTestInput(t, 2, 1, 3)
	before := boundedJSON(in)
	p := boundedTestPlan(t, in, false)
	w := p.Works()[0]
	r, e := w.Request(1)
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range in.Documents {
		for _, it := range d.Items {
			for _, text := range []string{it.Name, it.Content, it.Detail, it.SourceSpan.Quote} {
				if text != "" && !strings.Contains(r.User, string(boundedJSON(text))[1:len(boundedJSON(text))-1]) {
					t.Fatalf("full source material lost: %q", text)
				}
			}
		}
	}
	if !strings.Contains(r.User, `"detail":""`) || r.MaxTokens != 65536 {
		t.Fatal("optional empty Detail or reservation lost")
	}
	for _, d := range in.Graph.Domains {
		for _, sd := range d.Subdomains {
			if strings.Contains(r.User, boundedSubdomainID(d.Slug, sd.Slug)) || strings.Contains(r.User, boundedDomainID(d.Slug)) {
				t.Fatal("hierarchy UUID exposed")
			}
			for _, n := range sd.Entities {
				if strings.Contains(r.User, n.ID) {
					t.Fatal("final node UUID exposed")
				}
			}
		}
	}
	if string(before) != string(boundedJSON(in)) {
		t.Fatal("planning/request mutated inputs")
	}
	// Snapshot must remain unchanged after caller edits every external mutable input surface.
	in.Documents[0].Items[0].SourceSpan.Quote = "mutated"
	in.Documents[0].Items[0].Content = "mutated"
	in.Graph.Domains[0].Subdomains[0].Entities[0].Name = "mutated"
	in.Sources[0].OriginalSHA256 = boundedTestPin("changed")
	again, _ := w.Request(1)
	if !reflect.DeepEqual(r, again) {
		t.Fatal("request aliases caller-owned memory")
	}
}

func TestBoundedDescriptionsEightNineAndSameSubdomain(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 8, 9, 17} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			t.Parallel()
			in := boundedTestInput(t, n, 1, 1)
			p := boundedTestPlan(t, in, false)
			if len(p.Works()) != (n+7)/8 {
				t.Fatal("8-node ceiling not enforced")
			}
			for _, w := range p.Works() {
				if w.hi-w.lo > 8 {
					t.Fatal("oversized leaf")
				}
				sum := 512
				sub := ""
				for _, id := range p.tasks[w.task].nodes[w.lo:w.hi] {
					bn := p.snapshot.nodes[id]
					if sub != "" && sub != bn.subdomain {
						t.Fatal("mixed subdomain leaf")
					}
					sub = bn.subdomain
					sum += bn.cap + 256
				}
				if sum > 32768 {
					t.Fatal("description planning budget overflow")
				}
			}
		})
	}
	in := boundedTestInput(t, 9, 3, 1)
	p := boundedTestPlan(t, in, false)
	for _, w := range p.Works() {
		sub := ""
		for _, id := range p.tasks[w.task].nodes {
			if sub != "" && sub != p.snapshot.nodes[id].subdomain {
				t.Fatal("subdomain boundary crossed")
			}
			sub = p.snapshot.nodes[id].subdomain
		}
	}
}

// C计的是编码后字符串字节，中文、引号和反斜线不能按rune或未转义长度计费。
func TestBoundedDescriptionCapsAtBothFormulaKneesAndEncodedBoundary(t *testing.T) {
	t.Parallel()
	for _, target := range []int{767, 768, 769, 12031, 12032, 12033} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			t.Parallel()
			in := boundedTestInput(t, 1, 1, 1)
			in.Documents[0].Items[0].Detail = ""
			in = boundedTestRebind(t, in)
			p := boundedTestPlan(t, in, false)
			var bn boundedNode
			for _, n := range p.snapshot.nodes {
				bn = n
			}
			base := boundedTestMaterialBytes(bn)
			if base > target {
				t.Fatalf("fixture base%d exceeds target%d", base, target)
			}
			in.Documents[0].Items[0].Detail = strings.Repeat("x", target-base)
			in = boundedTestRebind(t, in)
			p = boundedTestPlan(t, in, false)
			for _, n := range p.snapshot.nodes {
				bn = n
			}
			if boundedTestMaterialBytes(bn) != target {
				t.Fatal("material byte target mismatch")
			}
			want := 2*target + 512
			if want < 2048 {
				want = 2048
			}
			if want > 24576 {
				want = 24576
			}
			if bn.cap != want {
				t.Fatalf("C=%d want%d", bn.cap, want)
			}
			w := p.Works()[0]
			for _, extra := range []int{0, 1} {
				text := strings.Repeat("x", want-2+extra)
				response := &llm.CompleteResponse{FinishReason: "stop", Text: string(boundedJSON(map[string]any{"descriptions": []any{map[string]any{"node": "n1", "description": text}}}))}
				_, err := w.Validate(response)
				if (err != nil) != (extra == 1) {
					t.Fatalf("encoded C boundary extra%d err%v", extra, err)
				}
			}
		})
	}
	in := boundedTestInput(t, 1, 1, 1)
	p := boundedTestPlan(t, in, false)
	w := p.Works()[0]
	var cap int
	for _, n := range p.snapshot.nodes {
		cap = n.cap
	}
	for _, text := range []string{strings.Repeat("中", cap/3+1), strings.Repeat("\\", cap/2), strings.Repeat("\"", cap/2)} {
		if _, e := w.Validate(&llm.CompleteResponse{FinishReason: "stop", Text: string(boundedJSON(map[string]any{"descriptions": []any{map[string]any{"node": "n1", "description": text}}}))}); e == nil {
			t.Fatal("accepted rune/unescaped boundary overflow")
		}
	}
	large := boundedTestInput(t, 2, 1, 1)
	for i := range large.Documents[0].Items {
		large.Documents[0].Items[i].Detail = strings.Repeat("x", 15000)
	}
	large = boundedTestRebind(t, large)
	if len(boundedTestPlan(t, large, false).Works()) != 2 {
		t.Fatal("large full-material nodes not split into singleton leaves")
	}
}

func TestBoundedDescriptionGreedyPlanningBudgetExactBoundary(t *testing.T) {
	t.Parallel()
	for _, m := range []int{7680, 7681} {
		in := boundedTestInput(t, 2, 1, 1)
		initial := boundedTestPlan(t, in, false)
		for i := range in.Documents[0].Items {
			item := &in.Documents[0].Items[i]
			var base int
			for _, n := range initial.snapshot.nodes {
				if n.node.Members[0] == item.ID {
					base = boundedTestMaterialBytes(n)
				}
			}
			item.Detail += strings.Repeat("x", m-base)
		}
		in = boundedTestRebind(t, in)
		p := boundedTestPlan(t, in, false)
		want := 1
		if m == 7681 {
			want = 2
		}
		if len(p.Works()) != want {
			t.Fatalf("M%d: leaves%d expected%d", m, len(p.Works()), want)
		}
		for _, n := range p.snapshot.nodes {
			if boundedTestMaterialBytes(n) != m {
				t.Fatal("fixture material size mismatch")
			}
		}
	}
}

func TestBoundedDescriptionAuxiliaryMetadataCannotInflateC(t *testing.T) {
	t.Parallel()
	in := boundedTestInput(t, 1, 1, 1)
	before := boundedTestPlan(t, in, false)
	var originalCap int
	for _, node := range before.snapshot.nodes {
		originalCap = node.cap
	}
	oldMaterial := in.Identity.MaterialSHA256
	in.Documents[0].Items[0].SourceSpan.Quote = strings.Repeat("auxiliary span metadata 中文", 3000)
	in = boundedTestRebind(t, in)
	after := boundedTestPlan(t, in, false)
	for _, node := range after.snapshot.nodes {
		if node.cap != originalCap {
			t.Fatal("auxiliary metadata inflated description C")
		}
	}
	if in.Identity.MaterialSHA256 == oldMaterial {
		t.Fatal("complete metadata missing from material root")
	}
	request, err := after.Works()[0].Request(1)
	if err != nil || !strings.Contains(request.User, in.Documents[0].Items[0].SourceSpan.Quote) {
		t.Fatal("full auxiliary source metadata not retained")
	}
	for _, invalid := range []string{".", "..", "../source.md", "/absolute.md", "a\\b.md"} {
		bad := boundedTestInput(t, 1, 1, 1)
		bad.Sources[0].Path = invalid
		bad.Documents[0].FilePath = invalid
		if _, err := ComputeBoundedStageIdentity(bad); err == nil {
			t.Fatalf("accepted invalid source path%q", invalid)
		}
	}
}

func TestBoundedCanonicalShufflesAndHiddenHierarchyRoots(t *testing.T) {
	t.Parallel()
	in := boundedTestInput(t, 17, 3, 3)
	want := boundedTestPlan(t, in, false)
	r := rand.New(rand.NewSource(83))
	for i := 0; i < 12; i++ {
		r.Shuffle(len(in.Documents), func(a, b int) { in.Documents[a], in.Documents[b] = in.Documents[b], in.Documents[a] })
		r.Shuffle(len(in.Sources), func(a, b int) { in.Sources[a], in.Sources[b] = in.Sources[b], in.Sources[a] })
		for _, doc := range in.Documents {
			r.Shuffle(len(doc.Items), func(a, b int) { doc.Items[a], doc.Items[b] = doc.Items[b], doc.Items[a] })
		}
		r.Shuffle(len(in.Graph.Domains[0].Subdomains), func(a, b int) {
			in.Graph.Domains[0].Subdomains[a], in.Graph.Domains[0].Subdomains[b] = in.Graph.Domains[0].Subdomains[b], in.Graph.Domains[0].Subdomains[a]
		})
		for j := range in.Graph.Domains[0].Subdomains {
			nodes := in.Graph.Domains[0].Subdomains[j].Entities
			r.Shuffle(len(nodes), func(a, b int) { nodes[a], nodes[b] = nodes[b], nodes[a] })
		}
		got := boundedTestPlan(t, in, false)
		if got.id != want.id {
			t.Fatal("input completion order affected plan identity")
		}
		for j, w := range got.Works() {
			a, _ := w.Request(1)
			b, _ := want.Works()[j].Request(1)
			if !reflect.DeepEqual(a, b) {
				t.Fatal("input order affected request")
			}
		}
	}
	old := in.Identity.CatalogueSHA256
	in.Graph.Domains[0].Subdomains[0].Slug = boundedTestID("different hierarchy")
	computed, e := ComputeBoundedStageIdentity(in)
	if e != nil || computed.CatalogueSHA256 == old {
		t.Fatal("hidden hierarchy identity omitted from digest")
	}
	if _, e := BuildBoundedDescriptionPlan(in); e == nil {
		t.Fatal("accepted changed hidden hierarchy under frozen root")
	}
}

func TestBoundedIndependentRootsAndInputValidation(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"code", "corpus", "roster", "accepted", "method", "parent", "source", "material", "catalogue", "description"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			in := boundedTestInput(t, 2, 1, 1)
			switch field {
			case "code":
				in.Expected.CodeSourceCommit = strings.Repeat("2", 40)
			case "corpus":
				in.Expected.CorpusSHA256 = boundedTestPin("wrong")
			case "roster":
				in.Expected.RosterSHA256 = boundedTestPin("wrong")
			case "accepted":
				in.Expected.AcceptedSetSHA256 = boundedTestPin("wrong")
			case "method":
				in.Expected.MethodSHA256 = boundedTestPin("wrong")
			case "parent":
				in.Expected.ParentCatalogueSHA256 = boundedTestPin("wrong")
			case "source":
				in.Sources[0].OriginalSHA256 = boundedTestPin("wrong")
			case "material":
				in.Documents[0].Items[0].Content += "wrong"
			case "catalogue":
				in.Graph.Domains[0].Subdomains[0].Entities[0].Members = []string{"wrong"}
			case "description":
				in.Graph.Domains[0].Subdomains[0].Entities[0].Description += "wrong"
			}
			if _, e := BuildBoundedDescriptionPlan(in); e == nil {
				t.Fatal("accepted mismatching independent root")
			}
		})
	}
	for _, change := range []string{"invalid-utf8-detail", "invalid-utf8-hidden-id", "nil-doc", "duplicate-source", "duplicate-member", "unowned-member", "nan-confidence", "wrong-label", "reassigned-uuid"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			in := boundedTestInput(t, 2, 1, 1)
			switch change {
			case "invalid-utf8-detail":
				in.Documents[0].Items[0].Detail = string([]byte{0xff})
			case "invalid-utf8-hidden-id":
				in.Graph.Domains[0].Slug = string([]byte{0xff})
			case "nil-doc":
				in.Documents[0] = nil
			case "duplicate-source":
				in.Sources = append(in.Sources, in.Sources[0])
				in.Documents = append(in.Documents, in.Documents[0])
			case "duplicate-member":
				in.Documents[0].Items = append(in.Documents[0].Items, in.Documents[0].Items[0])
			case "unowned-member":
				in.Graph.Domains[0].Subdomains[0].Entities = in.Graph.Domains[0].Subdomains[0].Entities[:1]
			case "nan-confidence":
				in.Documents[0].Items[0].Confidence = math.NaN()
			case "wrong-label":
				in.Graph.Domains[0].Subdomains[0].Entities[0].Label = "Concept"
			case "reassigned-uuid":
				in.Graph.Domains[0].Subdomains[0].Entities[0].ID = "temporary-alias"
			}
			if _, e := ComputeBoundedStageIdentity(in); e == nil {
				t.Fatal("accepted malformed inputs")
			}
		})
	}
}

func TestBoundedRelationAllSameSubdomainPairsAcrossBlocks(t *testing.T) {
	t.Parallel()
	in := boundedTestInput(t, 10, 1, 1)
	p := boundedTestPlan(t, in, true)
	seen := map[BoundedPair]bool{}
	total := 0
	for _, w := range p.Works() {
		if w.hi-w.lo > 48 {
			t.Fatal("pair cap")
		}
		for _, pair := range p.tasks[w.task].pairs[w.lo:w.hi] {
			if seen[pair] {
				t.Fatal("duplicate unordered ownership")
			}
			seen[pair] = true
			total++
		}
		r, e := w.Request(1)
		if e != nil {
			t.Fatal(e)
		}
		for _, n := range p.snapshot.nodes {
			if strings.Contains(r.User, n.node.ID) {
				t.Fatal("real endpoint UUID exposed")
			}
		}
		d, e := w.Validate(boundedTestResponse(t, w, true))
		if e != nil {
			t.Fatal(e)
		}
		if len(d.relations) != (w.hi-w.lo)*12 {
			t.Fatal("both directions/six kinds not retained")
		}
		for _, rel := range d.relations {
			if rel.Description != p.snapshot.nodes[rel.Source].node.Name+" --["+rel.Kind+"]--> "+p.snapshot.nodes[rel.Target].node.Name {
				t.Fatal("model prose or truncated endpoint used")
			}
		}
	}
	if total != 45 {
		t.Fatalf("missing same-SD crossblock pairs%d", total)
	}
	in = boundedTestInput(t, 11, 1, 1)
	if len(boundedTestPlan(t, in, true).Works()) != 2 {
		t.Fatal("49+ pair boundary not split")
	}
}

// 原风险：纯[]被解释为“没有新增边”，使旧seed边跨入新代且保留旧模型rationale。
// A complete fresh relation phase accepts no seed edges and leaves unsupported pair results empty.
func TestBoundedRelationRequiresFreshEmptySeedAndRestoresSemanticPrompt(t *testing.T) {
	t.Parallel()
	in := boundedTestInput(t, 2, 1, 1)
	plan := boundedTestPlan(t, in, true)
	for _, attempt := range []int{1, 2} {
		request, err := plan.Works()[0].Request(attempt)
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{"real, explicit semantic relations", "supplied full endpoint descriptions", "stated direction", "Mere co-occurrence or a shared category is not evidence", "empty relations list when unsupported", "only the requested endpoints", "do not infer hierarchy or ownership relations"} {
			if !strings.Contains(request.System, required) {
				t.Fatalf("semantic contract missing %q", required)
			}
		}
	}
	before := boundedGraphBytes(in.Graph)
	deltas := boundedTestDeltas(t, plan)
	out, err := plan.Apply(in.Graph, deltas)
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range out.Domains {
		for _, subdomain := range domain.Subdomains {
			if len(subdomain.Relations) != 0 {
				t.Fatal("explicit empty result fabricated or retained edges")
			}
		}
	}
	if string(before) != string(boundedGraphBytes(in.Graph)) {
		t.Fatal("empty result mutated original graph")
	}
	entities := in.Graph.Domains[0].Subdomains[0].Entities
	in.Graph.Domains[0].Subdomains[0].Relations = []Relation{{Source: entities[0].ID, Target: entities[1].ID, Kind: "references", Description: "old model rationale that cannot survive fresh []", Confidence: .7}}
	in = boundedTestRebind(t, in)
	if _, err := BuildBoundedRelationPlan(in, boundedTestCross(t, in, nil)); err == nil {
		t.Fatal("fresh relation plan accepted existing seed edge")
	}
}

// 实际映射器的层级是slug而非UUID，同名子域在不同父域下必须保持独立。
// Repeated child slugs must neither create false same-scope pairs nor duplicate applied edges.
func TestBoundedRealHierarchySlugsRemainScopedToTheirParents(t *testing.T) {
	t.Parallel()
	in := boundedTestInput(t, 4, 2, 1)
	first := in.Graph.Domains[0]
	second := first
	second.Slug, second.Name = "second-domain", "Second domain"
	first.Subdomains = append([]Subdomain(nil), first.Subdomains[:1]...)
	second.Subdomains = append([]Subdomain(nil), second.Subdomains[1:]...)
	first.Subdomains[0].Slug, second.Subdomains[0].Slug = "common", "common"
	in.Graph.Domains = []Domain{first, second}
	in = boundedTestRebind(t, in)
	descriptions := boundedTestPlan(t, in, false)
	if len(descriptions.Works()) != 2 {
		t.Fatal("same child slug merged distinct description scopes")
	}
	same := boundedTestPlan(t, in, true)
	total := 0
	for _, task := range same.tasks {
		total += len(task.pairs)
	}
	if total != 2 {
		t.Fatalf("false same-subdomain pairs across parents: %d", total)
	}
	a, b := first.Subdomains[0].Entities[0].ID, second.Subdomains[0].Entities[0].ID
	if a > b {
		a, b = b, a
	}
	cross := boundedTestCross(t, in, []BoundedPair{{a, b}})
	plan, err := BuildBoundedRelationPlan(in, cross)
	if err != nil {
		t.Fatal(err)
	}
	var deltas []BoundedStageDelta
	for _, work := range plan.Works() {
		delta, err := work.Validate(boundedTestResponse(t, work, true))
		if err != nil {
			t.Fatal(err)
		}
		deltas = append(deltas, delta)
	}
	out, err := plan.Apply(in.Graph, deltas)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, domain := range out.Domains {
		for _, sub := range domain.Subdomains {
			if sub.Slug != "common" {
				t.Fatal("child slug reassigned")
			}
			for _, relation := range sub.Relations {
				count++
				source := plan.snapshot.nodes[relation.Source]
				if source.subdomain != boundedSubdomainID(domain.Slug, sub.Slug) {
					t.Fatal("relation applied to wrong parent scope")
				}
			}
		}
	}
	if count != 36 {
		t.Fatalf("expected 3pairs*12triples, got%d", count)
	}
}

func TestBoundedCrossInventoryEndpointCapAndEmptyCoverage(t *testing.T) {
	t.Parallel()
	in := boundedTestInput(t, 70, 70, 1)
	var ids []string
	for _, sd := range in.Graph.Domains[0].Subdomains {
		ids = append(ids, sd.Entities[0].ID)
	}
	sort.Strings(ids)
	var pairs []BoundedPair
	for i := 0; i < len(ids); i += 2 {
		pairs = append(pairs, BoundedPair{ids[i], ids[i+1]})
	}
	cross := boundedTestCross(t, in, pairs)
	p, e := BuildBoundedRelationPlan(in, cross)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Works()) != 2 {
		t.Fatal("64-endpoint limit not independently enforced")
	}
	var deltas []BoundedStageDelta
	for _, w := range p.Works() {
		endpoints := map[string]bool{}
		for _, pair := range p.tasks[w.task].pairs {
			endpoints[pair.First] = true
			endpoints[pair.Second] = true
		}
		if len(endpoints) > 64 || len(p.tasks[w.task].pairs) > 48 {
			t.Fatal("cross leaf limit")
		}
		d, e := w.Validate(boundedTestResponse(t, w, false))
		if e != nil {
			t.Fatal("explicit empty pair list rejected", e)
		}
		deltas = append(deltas, d)
	}
	before := boundedGraphBytes(in.Graph)
	out, e := p.Apply(in.Graph, deltas)
	if e != nil {
		t.Fatal(e)
	}
	if string(before) != string(boundedGraphBytes(in.Graph)) || boundedHash(boundedGraphBytes(boundedCopyGraph(in.Graph))) != boundedHash(boundedGraphBytes(out)) {
		t.Fatal("empty pair result mutated graph")
	}
	for _, change := range []string{"catalogue", "description", "pin", "duplicate", "same-subdomain", "self", "unknown", "reverse"} {
		t.Run(change, func(t *testing.T) {
			x := cross
			x.Pairs = append([]BoundedPair(nil), cross.Pairs...)
			switch change {
			case "catalogue":
				x.CatalogueSHA256 = boundedTestPin("wrong")
			case "description":
				x.DescriptionSHA256 = boundedTestPin("wrong")
			case "pin":
				x.ExpectedSHA256 = boundedTestPin("wrong")
			case "duplicate":
				x.Pairs = append(x.Pairs, x.Pairs[0])
			case "same-subdomain":
				other := boundedTestInput(t, 2, 1, 1)
				var nids []string
				for id := range boundedTestPlan(t, other, false).snapshot.nodes {
					nids = append(nids, id)
				}
				sort.Strings(nids)
				y := boundedTestCross(t, other, []BoundedPair{{nids[0], nids[1]}})
				if _, e := BuildBoundedRelationPlan(other, y); e == nil {
					t.Fatal("accepted same-SD external pair")
				}
				return
			case "self":
				x.Pairs[0].Second = x.Pairs[0].First
			case "unknown":
				x.Pairs[0] = BoundedPair{boundedTestID("unknown1"), boundedTestID("unknown2")}
				if x.Pairs[0].First > x.Pairs[0].Second {
					x.Pairs[0].First, x.Pairs[0].Second = x.Pairs[0].Second, x.Pairs[0].First
				}
				x.ExpectedSHA256, _ = BoundedCrossPairDigest(x)
			case "reverse":
				x.Pairs[0].First, x.Pairs[0].Second = x.Pairs[0].Second, x.Pairs[0].First
			}
			if _, e := BuildBoundedRelationPlan(in, x); e == nil {
				t.Fatal("accepted invalid cross inventory")
			}
		})
	}
}

// 原风险：部分合法记录被累积写入、null变0、枚举大小写折叠或重复键“最后一个获胜”。
func TestBoundedStrictDescriptionAndRelationResponses(t *testing.T) {
	t.Parallel()
	description := boundedTestPlan(t, boundedTestInput(t, 1, 1, 1), false).Works()[0]
	relation := boundedTestPlan(t, boundedTestInput(t, 2, 1, 1), true).Works()[0]
	for _, text := range []string{`null`, `[]`, `{}`, `{"Descriptions":[]}`, `{"descriptions":null}`, `{"descriptions":[],"descriptions":[{"node":"n1","description":"ok"}]}`, `{"descriptions":[{"Node":"n1","description":"ok"}]}`, `{"descriptions":[{"node":"n1","description":null}]}`, `{"descriptions":[{"node":"n2","description":"ok"}]}`, `{"descriptions":[{"node":"n1","description":" "}]}`, `{"descriptions":[{"node":"n1","description":"ok","rationale":"extra"}]}`, `{"descriptions":[{"node":"n1","description":"ok"}]} prose`, `{"descriptions":[{"node":"n1","description":"\ud800"}]}`, string([]byte{'{', 0xff, '}'})} {
		t.Run("description/"+fmt.Sprint(len(text))+text[:1], func(t *testing.T) {
			if _, e := description.Validate(&llm.CompleteResponse{FinishReason: "stop", Text: text}); e == nil {
				t.Fatalf("accepted hostile description: %s", text)
			}
		})
	}
	for _, relations := range []string{`null`, `[[0,"triggers",null]]`, `[[0,"triggers","0.9"]]`, `[[null,"triggers",0.9]]`, `[[2,"triggers",0.9]]`, `[[0.0,"triggers",0.9]]`, `[[0,"TRIGGERS",0.9]]`, `[[0,"provides",0.9]]`, `[[0,"triggers",-0.1]]`, `[[0,"triggers",1.1]]`, `[[0,"triggers",1e999]]`, `[[0,"triggers",NaN]]`, `[[0,"triggers",0.9,"rationale"]]`, `[[0,"triggers",0.9],[0,"triggers",0.8]]`} {
		text := `{"pairs":[{"pair":"p1","relations":` + relations + `}]}`
		if _, e := relation.Validate(&llm.CompleteResponse{FinishReason: "stop", Text: text}); e == nil {
			t.Fatalf("accepted hostile tuple: %s", text)
		}
	}
	for _, text := range []string{`{"Pairs":[]}`, `{"pairs":[{"Pair":"p1","relations":[]}]}`, `{"pairs":[{"pair":"p01","relations":[]}]}`, `{"pairs":[{"pair":"p2","relations":[]}]}`, `{"pairs":[{"pair":"p1","relations":[],"rationale":"extra"}]}`, `{"pairs":[{"pair":"p1","relations":[]},{"pair":"p1","relations":[]}]}`, `{"pairs":[]}`, `{"pairs":[{"pair":"p1","relations":[],"relations":[[0,"triggers",0.8]]}]}`} {
		if _, e := relation.Validate(&llm.CompleteResponse{FinishReason: "stop", Text: text}); e == nil {
			t.Fatal("accepted bad pair coverage/schema", text)
		}
	}
	for _, w := range []BoundedStageWork{description, relation} {
		for _, finish := range []string{"", "length", "tool_calls", "STOP", "end_turn"} {
			resp := boundedTestResponse(t, w, false)
			resp.FinishReason = finish
			if _, e := w.Validate(resp); e == nil {
				t.Fatal("accepted non-stop finish")
			}
		}
		resp := boundedTestResponse(t, w, false)
		resp.Text += strings.Repeat(" ", 32769)
		if _, e := w.Validate(resp); e != nil {
			t.Fatal("rejected harmless whitespace despite bounded canonical body", e)
		}
		if _, e := w.Validate(nil); e == nil {
			t.Fatal("accepted nil response")
		}
	}
	if _, e := description.Validate(&llm.CompleteResponse{FinishReason: "stop", Text: `{"descriptions":[{"node":"n1","description":"\ud83d\ude00中文"}]}`}); e != nil {
		t.Fatal("valid surrogate pair rejected", e)
	}
}

func TestBoundedRequestsAttemptsAndPredeliverySplit(t *testing.T) {
	t.Parallel()
	for _, relation := range []bool{false, true} {
		p := boundedTestPlan(t, boundedTestInput(t, 6, 1, 1), relation)
		w := p.Works()[0]
		one, _ := w.Request(1)
		two, e := w.Request(2)
		if e != nil || one.User != two.User || !strings.Contains(two.System, "Repair:") || strings.Contains(two.System, "previous raw secret") {
			t.Fatal("repair is not fixed and sanitized")
		}
		for _, n := range []int{-1, 0, 3, 99} {
			if _, e := w.Request(n); e == nil {
				t.Fatal("unbounded logical attempt")
			}
		}
		children, e := w.Split()
		if e != nil || len(children) != 2 || children[0].lo != w.lo || children[0].hi != children[1].lo || children[1].hi != w.hi || children[0].ID() == children[1].ID() {
			t.Fatal("split ownership invalid")
		}
		for _, child := range children {
			if _, e := child.Request(1); e != nil {
				t.Fatal(e)
			}
		}
		again, _ := w.Request(1)
		if !reflect.DeepEqual(one, again) {
			t.Fatal("split mutated parent")
		}
	}
	w := boundedTestPlan(t, boundedTestInput(t, 1, 1, 1), false).Works()[0]
	if _, e := w.Split(); e == nil {
		t.Fatal("singleton split did not stop")
	}
	if _, e := (BoundedStageWork{}).Request(1); e == nil {
		t.Fatal("zero work request")
	}
}

func TestBoundedWholePhaseApplyRequiresAllNonoverlappingLeavesAndCopies(t *testing.T) {
	t.Parallel()
	for _, relation := range []bool{false, true} {
		in := boundedTestInput(t, 10, 1, 2)
		before := boundedGraphBytes(in.Graph)
		p := boundedTestPlan(t, in, relation)
		var deltas []BoundedStageDelta
		for _, w := range p.Works() {
			children, e := w.Split()
			if e != nil {
				children = []BoundedStageWork{w}
			}
			for _, child := range children {
				d, e := child.Validate(boundedTestResponse(t, child, relation))
				if e != nil {
					t.Fatal(e)
				}
				deltas = append(deltas, d)
			}
		}
		out, e := p.Apply(in.Graph, deltas)
		if e != nil {
			t.Fatal(e)
		}
		if string(before) != string(boundedGraphBytes(in.Graph)) {
			t.Fatal("Apply mutated original graph")
		}
		for _, d := range out.Domains {
			for _, sd := range d.Subdomains {
				for _, n := range sd.Entities {
					if _, ok := p.snapshot.nodes[n.ID]; !ok || len(n.Members) == 0 {
						t.Fatal("final UUID or members changed")
					}
				}
			}
		}
		for _, bad := range [][]BoundedStageDelta{nil, deltas[:len(deltas)-1], append(append([]BoundedStageDelta(nil), deltas...), deltas[0])} {
			if _, e := p.Apply(in.Graph, bad); e == nil {
				t.Fatal("partial/duplicate terminal leaves accepted")
			}
		}
		parent := p.Works()[0]
		parentDelta, e := parent.Validate(boundedTestResponse(t, parent, relation))
		if e != nil {
			t.Fatal(e)
		}
		overlap := append(append([]BoundedStageDelta(nil), deltas...), parentDelta)
		if _, e := p.Apply(in.Graph, overlap); e == nil {
			t.Fatal("parent plus child accepted")
		}
		other := boundedTestPlan(t, in, relation)
		if _, e := other.Apply(in.Graph, deltas); e == nil {
			t.Fatal("foreign plan deltas accepted")
		}
		changed := boundedCopyGraph(in.Graph)
		changed.Domains[0].Slug = boundedTestID("new root")
		if _, e := p.Apply(changed, deltas); e == nil {
			t.Fatal("changed hidden hierarchy root accepted")
		}
		changed = boundedCopyGraph(in.Graph)
		changed.Domains[0].Provenance = "changed hidden provenance"
		if _, e := p.Apply(changed, deltas); e == nil {
			t.Fatal("changed hidden domain provenance accepted")
		}
		out.Domains[0].Subdomains[0].Entities[0].Members[0] = "mutated-copy"
		if string(before) != string(boundedGraphBytes(in.Graph)) {
			t.Fatal("returned graph aliases original")
		}
		again, e := p.Apply(in.Graph, deltas)
		if e != nil || again.Domains[0].Subdomains[0].Entities[0].Members[0] == "mutated-copy" {
			t.Fatal("repeated pure Apply aliases earlier output")
		}
		if !relation {
			tampered := append([]BoundedStageDelta(nil), deltas...)
			tampered[0].descriptions = map[string]string{"injected": "not validated"}
			if _, e := p.Apply(in.Graph, tampered); e == nil {
				t.Fatal("modified delta accepted")
			}
		}
	}
}

func TestBoundedEmptyRelationPhaseAndCanonicalPairOwnership(t *testing.T) {
	t.Parallel()
	in := boundedTestInput(t, 1, 1, 1)
	p := boundedTestPlan(t, in, true)
	if len(p.Works()) != 0 {
		t.Fatal("one node manufactured relation work")
	}
	if _, e := p.Apply(in.Graph, nil); e != nil {
		t.Fatal("complete empty phase rejected", e)
	}
	in = boundedTestInput(t, 4, 2, 1)
	var ids []string
	for id := range boundedTestPlan(t, in, false).snapshot.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var crossPairs []BoundedPair
	nodes := boundedTestPlan(t, in, false).snapshot.nodes
	for i, a := range ids {
		for _, b := range ids[i+1:] {
			if nodes[a].subdomain != nodes[b].subdomain {
				crossPairs = append(crossPairs, BoundedPair{a, b})
			}
		}
	}
	cross := boundedTestCross(t, in, crossPairs)
	a, e := BuildBoundedRelationPlan(in, cross)
	if e != nil {
		t.Fatal(e)
	}
	for i, j := 0, len(cross.Pairs)-1; i < j; i, j = i+1, j-1 {
		cross.Pairs[i], cross.Pairs[j] = cross.Pairs[j], cross.Pairs[i]
	}
	b, e := BuildBoundedRelationPlan(in, cross)
	if e != nil || a.id != b.id {
		t.Fatal("cross input order affected frozen plan")
	}
	total := 0
	for _, task := range a.tasks {
		total += len(task.pairs)
	}
	if total != 6 {
		t.Fatalf("same plus cross inventory total%d expected6", total)
	}
	// Identical graph with different frozen cross work must not produce colliding work identities.
	cross2 := boundedTestCross(t, in, crossPairs[:len(crossPairs)-1])
	c, e := BuildBoundedRelationPlan(in, cross2)
	if e != nil || a.id == c.id {
		t.Fatal("pair ownership absent from plan root")
	}
}

func TestBoundedTupleAndCanonicalWholeBodyBudget(t *testing.T) {
	t.Parallel()
	if 48*(12*48+96)+512 != 32768 {
		t.Fatal("canonical proof arithmetic changed")
	}
	in := boundedTestInput(t, 11, 1, 1)
	p := boundedTestPlan(t, in, true)
	w := p.Works()[0]
	resp := boundedTestResponse(t, w, true)
	if len(resp.Text) > 32768 {
		t.Fatal("legal maximal tuple inventory exceeded whole body")
	}
	if _, e := w.Validate(resp); e != nil {
		t.Fatal(e)
	}
	// The contract bounds canonical content; alternate valid numeric spelling is not a hidden raw-byte cap.
	text := `{"pairs":[{"pair":"p1","relations":[[0,"triggers",0.` + strings.Repeat("0", 32768) + `1]]}]}`
	small := boundedTestPlan(t, boundedTestInput(t, 2, 1, 1), true).Works()[0]
	if _, e := small.Validate(&llm.CompleteResponse{FinishReason: "stop", Text: text}); e != nil {
		t.Fatal("bounded canonical numeric body rejected", e)
	}
	// Optional fields remain present and unmodified in the material digest.
	root := in.Identity.MaterialSHA256
	in.Documents[0].Items[0].SourceSpan = nil
	computed, e := ComputeBoundedStageIdentity(in)
	if e != nil || computed.MaterialSHA256 == root {
		t.Fatal("optional source span not bound")
	}
	var v any
	if e := json.Unmarshal([]byte(resp.Text), &v); e != nil {
		t.Fatal(e)
	}
}
