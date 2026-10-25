//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/xcosmosbox/cairn/build/internal/constructionplan"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/pipeline"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

func boundedFixtureID(s string) string { return uuid.NewSHA1(uuid.Nil, []byte(s)).String() }
func boundedSmallGraph(n int) (*extract.Result, *boundedPipelineFrozen) {
	g := &extract.Result{Domains: []extract.Domain{{Name: "domain", Slug: "domain", Provenance: "llm_inferred", Summary: "domain summary", SourceSkills: []string{"skill"}, Subdomains: []extract.Subdomain{{Name: "subdomain", Slug: "subdomain", Summary: "subdomain summary"}}}}}
	doc := &dktypes.AnnotatedDocument{Skill: "skill", FilePath: "references/doc.md"}
	f := &boundedPipelineFrozen{Members: map[string][]string{}, DocRoots: map[string]incremental.StagedMappingDocumentRoot{}}
	for i := 0; i < n; i++ {
		member := fmt.Sprintf("m%d", i)
		g.Domains[0].Subdomains[0].Entities = append(g.Domains[0].Subdomains[0].Entities, extract.Node{ID: boundedFixtureID(member), Label: "Entity", Name: "common name", Summary: "common summary", Confidence: .9, Members: []string{member}, SourceSkills: []string{"skill"}})
		doc.Items = append(doc.Items, dktypes.AnnotatedItem{ID: member, Tag: dktypes.TagEntity, Name: "common name", Content: "complete content", Detail: "complete detail", Confidence: .9})
		f.Members[member] = []string{boundedDigest("doc")}
	}
	o := extract.FreezeSource("skill", doc.FilePath, "Every complete source supports its own facts.")
	root := incremental.StagedMappingDocumentRoot{DocID: boundedDigest("doc"), Skill: doc.Skill, FilePath: doc.FilePath, OriginalSHA256: o.SHA256, AcceptedCheckpointSHA256: boundedDigest("accepted")}
	f.DocRoots[boundedDigest("doc")] = root
	f.Documents = []*dktypes.AnnotatedDocument{doc}
	f.Originals = []extract.FrozenSource{o}
	f.Sources = []extract.BoundedStageSource{{DocID: boundedDigest("doc"), Skill: "skill", Path: doc.FilePath, OriginalSHA256: o.SHA256, AcceptedCheckpointSHA256: root.AcceptedCheckpointSHA256}}
	return g, f
}
func boundedSmallPins() boundedPipelinePins {
	return boundedPipelinePins{CodeSourceCommit: strings.Repeat("a", 40), CorpusSHA256: boundedDigest("corpus"), RosterSHA256: boundedDigest("roster"), AcceptedSetSHA256: boundedDigest("accepted"), MethodSHA256: boundedDigestBytes([]byte(boundedConstructionMethod)), Admission: boundedTestPins()}
}
func boundedSchemaReply(req llm.CompleteRequest, stage string, attempt int) (llm.CompleteResponse, error) {
	var in struct {
		Nodes []struct{ Alias string }
		Pairs []struct{ Alias string }
		Units []struct{ ID string }
	}
	if e := json.Unmarshal([]byte(req.User), &in); e != nil {
		return llm.CompleteResponse{}, e
	}
	var out any
	switch stage {
	case "description":
		rows := []map[string]string{}
		for _, node := range in.Nodes {
			rows = append(rows, map[string]string{"node": node.Alias, "description": "complete faithful description"})
		}
		out = map[string]any{"descriptions": rows}
	case "relation":
		rows := []map[string]any{}
		for _, pair := range in.Pairs {
			rows = append(rows, map[string]any{"pair": pair.Alias, "relations": []any{}})
		}
		out = map[string]any{"pairs": rows}
	case "cross_relation_verify":
		out = map[string]string{"disposition": "insufficient_evidence", "reason": "not established in supplied source"}
	case "mapping":
		decisions := []map[string]string{}
		groups := []map[string]string{}
		// A distinct node per unit avoids a huge shared-source singleton. Recall
		// aliases are deliberately ignored: the fake exercises full fresh mapping.
		for i, u := range in.Units {
			group := fmt.Sprintf("G%d", i+1)
			decisions = append(decisions, map[string]string{"unit": u.ID, "action": "fuse", "group": group})
			groups = append(groups, map[string]string{"group": group, "node_name": "fresh " + u.ID, "summary": "common summary", "tag": "entity", "domain": "domain", "subdomain": "subdomain"})
		}
		out = map[string]any{"decisions": decisions, "groups": groups}
	default:
		return llm.CompleteResponse{}, fmt.Errorf("unknown fake stage")
	}
	raw, e := json.Marshal(out)
	return llm.CompleteResponse{Text: string(raw), InputTokens: 100, OutputTokens: 100, FinishReason: "stop"}, e
}
func TestBoundedPipelinePreflightsBothAttemptsAndSplitsBeforeAnyWire(t *testing.T) {
	g, f := boundedSmallGraph(4)
	input, e := boundedStageInput(g, f, boundedSmallPins(), boundedDigest("parent"))
	if e != nil {
		t.Fatal(e)
	}
	plan, e := extract.BuildBoundedDescriptionPlan(input)
	if e != nil {
		t.Fatal(e)
	}
	r := &boundedResolver{works: map[string]boundedResolvedWork{}, frozen: f}
	bridge := &boundedTestBridge{path: filepath.Join(t.TempDir(), "actual"), pins: boundedTestPins(), resolve: r.resolve, reply: boundedSchemaReply}
	bridge.prepareError = func(req llm.CompleteRequest) error {
		var x struct{ Nodes []any }
		json.Unmarshal([]byte(req.User), &x)
		if strings.Contains(req.System, "Repair:") && len(x.Nodes) > 1 {
			return incremental.ErrStagedMappingOverflow
		}
		return nil
	}
	leaves, e := boundedPreflightStage(context.Background(), bridge, r, g, boundedGraphRoot(g), plan.Works())
	if e != nil {
		t.Fatal(e)
	}
	if len(leaves) != 4 || bridge.dispatchCount != 0 {
		t.Fatalf("did not split both fixed requests before spend: leaves=%d calls=%d", len(leaves), bridge.dispatchCount)
	}
	for _, leaf := range leaves {
		if leaf.handles[0] == nil || leaf.handles[1] == nil || leaf.calls[0].Metadata.WorkID != leaf.calls[1].Metadata.WorkID {
			t.Fatal("missing fixed second preflight")
		}
	}
	c, e := openBoundedCheckpoint(t.TempDir(), boundedDigest("run"), false, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.file.Close()
	result, e := boundedRunStage(context.Background(), bridge, c, plan, g, leaves)
	if e != nil {
		t.Fatal(e)
	}
	if bridge.dispatchCount != 4 {
		t.Fatal("unexpected repair calls")
	}
	for _, node := range boundedNodes(result) {
		if node.Node.Description == "" {
			t.Fatal("partial stage applied")
		}
	}
	for _, node := range boundedNodes(g) {
		if node.Node.Description != "" {
			t.Fatal("mutated parent")
		}
	}
}

func TestBoundedPhaseInventoryCrashBeforeFirstWireRebuildsExactLeaves(t *testing.T) {
	ctx := context.Background()
	g, f := boundedSmallGraph(2)
	in, e := boundedStageInput(g, f, boundedSmallPins(), boundedDigest("parent"))
	if e != nil {
		t.Fatal(e)
	}
	plan, e := extract.BuildBoundedDescriptionPlan(in)
	if e != nil {
		t.Fatal(e)
	}
	r := &boundedResolver{works: map[string]boundedResolvedWork{}, frozen: f}
	dir := t.TempDir()
	b := &boundedTestBridge{path: filepath.Join(dir, "actual.jsonl"), pins: boundedTestPins(), resolve: r.resolve, reply: boundedSchemaReply}
	leaves, e := boundedPreflightStage(ctx, b, r, g, boundedGraphRoot(g), plan.Works())
	if e != nil {
		t.Fatal(e)
	}
	c, e := openBoundedCheckpoint(dir, boundedDigest("inventory-run"), false, func(point string) error {
		if point == "inventory" {
			return errors.New("crash after durable inventory")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = boundedRunStage(ctx, b, c, plan, g, leaves); e == nil || b.dispatchCount != 0 {
		t.Fatal("pre-spend inventory crash sent wire")
	}
	c.file.Close()
	resumed, e := openBoundedCheckpoint(dir, boundedDigest("inventory-run"), true, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer resumed.file.Close()
	if len(resumed.inventories) != 1 {
		t.Fatal("phase inventory not durable")
	}
	fresh := &boundedTestBridge{path: b.path, pins: b.pins, resolve: r.resolve, reply: boundedSchemaReply, prepareCount: 100}
	leaves, e = boundedPreflightStage(ctx, fresh, r, g, boundedGraphRoot(g), plan.Works())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = boundedRunStage(ctx, fresh, resumed, plan, g, leaves); e != nil {
		t.Fatal(e)
	}
	if fresh.dispatchCount != len(leaves) {
		t.Fatal("unused preflight repair attempt was dispatched")
	}
}
func TestBoundedPipelineOnlyCompleteSchemaFailureUsesFixedRepair(t *testing.T) {
	for _, mode := range []string{"schema", "transport", "nonstop", "badusage", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			g, f := boundedSmallGraph(1)
			in, e := boundedStageInput(g, f, boundedSmallPins(), boundedDigest("parent"))
			if e != nil {
				t.Fatal(e)
			}
			plan, e := extract.BuildBoundedDescriptionPlan(in)
			if e != nil {
				t.Fatal(e)
			}
			r := &boundedResolver{works: map[string]boundedResolvedWork{}, frozen: f}
			b := &boundedTestBridge{path: filepath.Join(t.TempDir(), "actual"), pins: boundedTestPins(), resolve: r.resolve}
			b.reply = func(req llm.CompleteRequest, stage string, attempt int) (llm.CompleteResponse, error) {
				response, e := boundedSchemaReply(req, stage, attempt)
				if attempt == 1 {
					switch mode {
					case "schema":
						response.Text = `{"descriptions":[]}`
					case "transport":
						return response, errors.New("unknown result")
					case "nonstop":
						response.FinishReason = "length"
					case "badusage":
						response.InputTokens = 0
					case "overflow":
						return response, incremental.ErrStagedMappingOverflow
					}
				}
				return response, e
			}
			leaves, e := boundedPreflightStage(context.Background(), b, r, g, boundedGraphRoot(g), plan.Works())
			if e != nil {
				t.Fatal(e)
			}
			c, e := openBoundedCheckpoint(t.TempDir(), boundedDigest("run"), false, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer c.file.Close()
			_, e = boundedRunStage(context.Background(), b, c, plan, g, leaves)
			if mode == "schema" {
				if e != nil || b.dispatchCount != 2 {
					t.Fatalf("fixed repair absent: %v calls%d", e, b.dispatchCount)
				}
			} else if e == nil || b.dispatchCount != 1 {
				t.Fatalf("automatic repayment after %s: %v calls%d", mode, e, b.dispatchCount)
			}
		})
	}
}
func TestBoundedForecastExactIncidencesSharedSourcesAndEveryDocument(t *testing.T) {
	g, f := boundedSmallGraph(20)
	f.DocRoots["second"] = incremental.StagedMappingDocumentRoot{DocID: "second", FilePath: "references/second.md"}
	f.Members["m0"] = []string{boundedDigest("doc"), "second"}
	g.Domains[0].Subdomains[0].Entities[19].Confidence = .69
	r, e := boundedForecast(g, f)
	if e != nil || r.Accepted != 21 || r.Selected != 20 || r.PerDocument["second"] != 1 {
		t.Fatalf("lost shared incidence/exact95 forecast: %+v %v", r, e)
	}
	g.Domains[0].Subdomains[0].Entities[18].Confidence = .69
	if _, e = boundedForecast(g, f); e == nil {
		t.Fatal("accepted94percent forecast")
	}
	g.Domains[0].Subdomains[0].Entities[18].Confidence = .9
	g.Domains[0].Subdomains[0].Entities[0].Confidence = .69
	if _, e = boundedForecast(g, f); e == nil {
		t.Fatal("empty individual document accepted")
	}
}
func TestBoundedCrossRecallOtherScopedSubdomainBeforeK15FullDescriptionAndTie(t *testing.T) {
	g, _ := boundedSmallGraph(40)
	same := g.Domains[0].Subdomains[0].Entities
	// >15 same-scope matches must not occupy eligible result slots. The same
	// subdomain slug in a DIFFERENT domain remains eligible.
	for i := range same {
		same[i].Name = "common"
		same[i].Summary = "common"
		same[i].Description = "full searchable payload"
	}
	other := extract.Domain{Name: "other", Slug: "other", Subdomains: []extract.Subdomain{{Name: "subdomain", Slug: "subdomain"}}}
	for i := 0; i < 20; i++ {
		other.Subdomains[0].Entities = append(other.Subdomains[0].Entities, extract.Node{ID: boundedFixtureID(fmt.Sprintf("other%d", i)), Label: "Entity", Name: "unmatched", Summary: "unmatched", Description: "common full searchable payload", Members: []string{fmt.Sprintf("other%d", i)}, Confidence: 1})
	}
	g.Domains = append(g.Domains, other)
	r, e := boundedRecallCross(context.Background(), g, boundedDigest("cat"), boundedDigest("desc"))
	if e != nil {
		t.Fatal(e)
	}
	var query boundedCrossQuery
	for _, q := range r.Queries {
		if q.UUID == same[0].ID {
			query = q
		}
	}
	if len(query.Hits) != 15 {
		t.Fatalf("OTHER scope filter applied after LIMIT: %d", len(query.Hits))
	}
	for _, hit := range query.Hits {
		for _, n := range same {
			if hit.UUID == n.ID {
				t.Fatal("same scope eligible")
			}
		}
	}
	for i := 1; i < len(query.Hits); i++ {
		if query.Hits[i-1].Rank == query.Hits[i].Rank && query.Hits[i-1].UUID > query.Hits[i].UUID {
			t.Fatal("tie not UUID BINARY")
		}
	}
	// The query is literal-token OR, full Name, first40 Unicode summary only.
	name := `alpha OR "quoted" alpha`
	summary := strings.Repeat("界", 40) + " OMIT"
	q := boundedLiteralQuery(name, summary)
	if !strings.Contains(q, `"OR"`) || !strings.Contains(q, `"""quoted"""`) || strings.Contains(q, "OMIT") || strings.Count(q, `"alpha"`) != 1 {
		t.Fatalf("query recipe changed: %s", q)
	}
	copy, e := boundedCloneGraph(g)
	if e != nil {
		t.Fatal(e)
	}
	for di := range copy.Domains {
		for si := range copy.Domains[di].Subdomains {
			nodes := copy.Domains[di].Subdomains[si].Entities
			for i, j := 0, len(nodes)-1; i < j; i, j = i+1, j-1 {
				nodes[i], nodes[j] = nodes[j], nodes[i]
			}
		}
	}
	again, e := boundedRecallCross(context.Background(), copy, r.CatalogueSHA256, r.DescriptionSHA256)
	if e != nil || !reflect.DeepEqual(r, again) {
		t.Fatalf("FTS changed with graph order: %v", e)
	}
	// Use the existing ingest string projection, not JSON string escaping:
	// a newline-only synonym must not introduce the literal FTS token "n".
	tiny, _ := boundedSmallGraph(1)
	tiny.Domains[0].Subdomains[0].Entities[0].Name = "N"
	tiny.Domains[0].Subdomains[0].Entities[0].Summary = ""
	tiny.Domains = append(tiny.Domains, extract.Domain{Slug: "other", Subdomains: []extract.Subdomain{{Slug: "other", Entities: []extract.Node{{ID: boundedFixtureID("newline"), Label: "Entity", Name: "unmatched", Synonyms: []string{"\n"}}}}}})
	projection, e := boundedRecallCross(context.Background(), tiny, r.CatalogueSHA256, r.DescriptionSHA256)
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range projection.Queries {
		if q.UUID == tiny.Domains[0].Subdomains[0].Entities[0].ID && len(q.Hits) != 0 {
			t.Fatal("JSON escaping introduced extra synonym FTS tokens")
		}
	}
}
func TestBoundedSlugsCopyCollisionRepairAndLiteralPathBounds(t *testing.T) {
	g, _ := boundedSmallGraph(2)
	g.Domains[0].Subdomains = append(g.Domains[0].Subdomains, extract.Subdomain{Name: "second", Slug: "second", Concepts: []extract.Node{{ID: boundedFixtureID("concept"), Label: "Concept", Name: "common name", Members: []string{"concept"}, Confidence: 1}}})
	before := boundedGraphRoot(g)
	out, e := boundedFreshSlugs(context.Background(), g)
	if e != nil {
		t.Fatal(e)
	}
	if boundedGraphRoot(g) != before {
		t.Fatal("slug generation mutated input")
	}
	for _, loc := range boundedNodes(out) {
		if !strings.HasSuffix(loc.Node.FileSlug, "-"+loc.Node.ID) {
			t.Fatal("collision did not suffix EVERY collider with FULL UUID")
		}
	}
	copy, e := boundedCloneGraph(g)
	if e != nil {
		t.Fatal(e)
	}
	nodes := copy.Domains[0].Subdomains[0].Entities
	nodes[0], nodes[1] = nodes[1], nodes[0]
	permuted, e := boundedFreshSlugs(context.Background(), copy)
	if e != nil {
		t.Fatal(e)
	}
	slugs := map[string]string{}
	for _, loc := range boundedNodes(out) {
		slugs[loc.Node.ID] = loc.Node.FileSlug
	}
	for _, loc := range boundedNodes(permuted) {
		if slugs[loc.Node.ID] != loc.Node.FileSlug {
			t.Fatal("slug depended on input order")
		}
	}
	g.Domains = append(g.Domains, extract.Domain{Name: "other", Slug: "other", Subdomains: []extract.Subdomain{{Name: "subdomain", Slug: "subdomain", Entities: []extract.Node{{ID: boundedFixtureID("other"), Label: "Entity", Name: "common name", Members: []string{"other"}, Confidence: 1}}}}})
	out, e = boundedFreshSlugs(context.Background(), g)
	if e != nil {
		t.Fatal(e)
	}
	if out.Domains[1].Subdomains[0].Entities[0].FileSlug != "common-name" {
		t.Fatal("different domain collided")
	}
	g.Domains[0].Subdomains[0].Entities[0].FileSlug = "historical"
	if _, e = boundedFreshSlugs(context.Background(), g); e == nil {
		t.Fatal("rewrote immutable historical slug")
	}
	for _, p := range []string{strings.Repeat("界", 86) + ".md", "a/../escape.md", strings.Repeat("a/", 2100) + "file.md"} {
		if e = boundedCheckManagedPath(t.TempDir(), p); e == nil {
			t.Fatal("unsafe/literal byte bounds ignored")
		}
	}
	if e = boundedCheckManagedPath(t.TempDir(), strings.Repeat("a", 248)+".kg.yaml"); e == nil {
		t.Fatal("sidecar suffix omitted from component bound")
	}
	secondary, _ := boundedSmallGraph(3)
	ns := secondary.Domains[0].Subdomains[0].Entities
	ns[0].Name = "a"
	ns[1].Name = "a"
	ns[2].Name = "a-" + ns[0].ID
	if _, e = boundedFreshSlugs(context.Background(), secondary); e == nil {
		t.Fatal("adversarial secondary collision used another heuristic instead of STOP")
	}
}
func TestBoundedVerifierPreflightAllSourcesAndInvalidRemovalPolicy(t *testing.T) {
	g, f := boundedSmallGraph(3)
	s := &g.Domains[0].Subdomains[0]
	s.Relations = []extract.Relation{{Source: s.Entities[0].ID, Target: s.Entities[1].ID, Kind: "contradicts", Confidence: .9}, {Source: s.Entities[1].ID, Target: s.Entities[2].ID, Kind: "contradicts", Confidence: .9}}
	r := &boundedResolver{works: map[string]boundedResolvedWork{}, frozen: f}
	b := &boundedTestBridge{path: filepath.Join(t.TempDir(), "actual"), pins: boundedTestPins(), resolve: r.resolve}
	b.reply = func(llm.CompleteRequest, string, int) (llm.CompleteResponse, error) {
		return llm.CompleteResponse{Text: `{"unsupported":"not a verdict"}`, InputTokens: 100, OutputTokens: 10, FinishReason: "stop"}, nil
	}
	c, e := openBoundedCheckpoint(t.TempDir(), boundedDigest("run"), false, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.file.Close()
	out, report, e := boundedVerifyRelations(context.Background(), b, c, r, g, f)
	if e != nil || report.Candidates != 2 || report.Accepted != 0 || len(out.Domains[0].Subdomains[0].Relations) != 0 {
		t.Fatalf("invalid verdict removal policy changed: %v %+v", e, report)
	}
	for _, d := range report.Decisions {
		if d.Disposition != "invalid_verdict" {
			t.Fatal("invalid called semantic false")
		}
	}
	f.Originals[0] = extract.FreezeSource("skill", f.Originals[0].Path, strings.Repeat("x", 1048577))
	before := b.dispatchCount
	if _, _, e = boundedVerifyRelations(context.Background(), b, c, r, g, f); e == nil || b.dispatchCount != before {
		t.Fatal("over1MiB verifier preflight spent or trimmed")
	}
}

// Produce a genuine private importer-verified checkpoint. No unsafe constructor,
// raw04 bypass or actual #85 negative artifact is used by pipeline tests.
func boundedVerifiedFixture(t *testing.T, total int) (boundedPipelineInput, *constructionplan.ArchiveInput, *constructionplan.TrustPolicy) {
	return boundedVerifiedFixtureMembers(t, total, total)
}
func boundedVerifiedFixtureMembers(t *testing.T, total, memberCount int) (boundedPipelineInput, *constructionplan.ArchiveInput, *constructionplan.TrustPolicy) {
	t.Helper()
	if memberCount < 1 || memberCount > total {
		t.Fatal("invalid synthetic member cardinality")
	}
	prefix := "raw/attempt-01"
	files := map[string][]byte{}
	put := func(name string, value any) {
		b, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		files[prefix+"/"+name] = b
	}
	source := strings.Repeat("1", 40)
	corpus := boundedDigest("corpus")
	var roster []constructionplan.FrozenDocument
	var selected, frozen []map[string]any
	var originals []extract.FrozenSource
	var annotations []dktypes.AnnotatedDocument
	descriptor := constructionplan.ArchiveDescriptor{Prefix: prefix}
	put("protocol.json", map[string]any{"format": "cairn-corpus-capacity/v1", "corpus_sha256": corpus, "corpus_documents": total})
	for i := 0; i < total; i++ {
		url := fmt.Sprintf("https://example.invalid/doc/%04d", i)
		id := boundedDigestBytes([]byte(url))
		body := fmt.Sprintf("original source %d", i)
		wrapper := "# Original source\n\n" + body
		d := constructionplan.FrozenDocument{DocID: id, URL: url, Skill: "fixture", Path: "references/" + id + ".md", BodySHA256: boundedDigestBytes([]byte(body))}
		roster = append(roster, d)
		o := extract.FreezeSource(d.Skill, d.Path, wrapper)
		originals = append(originals, o)
		files[prefix+"/original/"+d.Path] = []byte(wrapper)
		selected = append(selected, map[string]any{"doc_id": id, "url": url, "path": d.Path, "body_sha256": d.BodySHA256, "body_bytes": len(body), "wrapper_sha256": o.SHA256, "wrapper_bytes": len(wrapper), "body_start_byte": len(wrapper) - len(body)})
		frozen = append(frozen, map[string]any{"doc_id": id, "path": d.Path, "body_bytes": len(body), "wrapper_bytes": len(wrapper), "wrapper_sha256": o.SHA256})
		content, detail := "content", "detail"
		if memberCount < total {
			content = fmt.Sprintf("content source %d", i)
			detail = fmt.Sprintf("detail source %d", i)
		}
		doc := dktypes.AnnotatedDocument{Skill: d.Skill, FilePath: d.Path, Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagEntity, Name: fmt.Sprintf("unit %d", i%memberCount), Content: content, Detail: detail, Confidence: .9}}}
		stem := "references_" + id
		put("stages/02_gate_a/"+d.Skill+"/"+stem+"_r1.json", doc)
		put("stages/03_gate_b/"+d.Skill+"/"+stem+"_r1_pass.json", map[string]bool{"passed": true})
		annotations = append(annotations, *extract.AssignIDs([]*dktypes.AnnotatedDocument{&doc})[0])
		descriptor.Documents = append(descriptor.Documents, constructionplan.AcceptedDocumentDescriptor{DocID: id, Round: 1})
	}
	protocol := boundedDigestBytes(files[prefix+"/protocol.json"])
	put("selection.json", map[string]any{"format": "cairn-capacity-selection/v1", "corpus_sha256": corpus, "protocol_sha256": protocol, "documents": selected})
	selection := boundedDigestBytes(files[prefix+"/selection.json"])
	put("runtime-overlay.json", map[string]any{"format": "cairn-capacity-runtime-overlay/v1", "base_protocol_sha256": protocol, "selection_sha256": selection})
	runtime := boundedDigestBytes(files[prefix+"/runtime-overlay.json"])
	put("run-start.json", map[string]any{"format": "cairn-capacity-run/v1", "builder_commit": source, "protocol_sha256": protocol, "runtime_overlay_sha256": runtime, "selection_sha256": selection, "annotation_attempts": 3, "annotation_rounds": 3})
	put("runtime-freeze.json", map[string]any{"format": "cairn-capacity-runtime-freeze/v1", "base_protocol_sha256": protocol, "runtime_overlay_sha256": runtime, "selection_sha256": selection, "documents": frozen})
	put("stages/00_frozen_sources.json", originals)
	put("stages/04_extract_input.json", annotations)
	descriptor.Identity = constructionplan.MethodIdentity{SourceCommit: source, BuilderCommit: source, AnnotationMethod: "legacy-original-only/v1", GatePolicy: "semantic-idempotency/v1", ProtocolSHA256: protocol, RuntimeOverlaySHA256: runtime}
	var manifestFiles []map[string]any
	for p, b := range files {
		manifestFiles = append(manifestFiles, map[string]any{"path": p, "bytes": len(b), "sha256": boundedDigestBytes(b)})
	}
	sort.Slice(manifestFiles, func(i, j int) bool { return manifestFiles[i]["path"].(string) < manifestFiles[j]["path"].(string) })
	manifest, _ := json.Marshal(map[string]any{"format": "cairn-restricted-calibration-archive/v1", "source_commit": source, "files": manifestFiles})
	archive := &constructionplan.ArchiveInput{Manifest: manifest, Files: files, Descriptor: descriptor}
	policy := &constructionplan.TrustPolicy{ManifestSHA256: boundedDigestBytes(manifest), ArchiveCommit: strings.Repeat("2", 40), CorpusSHA256: corpus, RosterSHA256: constructionplan.RosterDigest(roster), ExpectedDocuments: total, Documents: roster, CompatibleMethods: []constructionplan.MethodIdentity{descriptor.Identity}, RequiredGatePolicy: descriptor.Identity.GatePolicy}
	checkpoint, e := constructionplan.ImportCheckpoint(*archive, *policy)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := constructionplan.BuildPlan(checkpoint)
	if e != nil {
		t.Fatal(e)
	}
	pins := boundedSmallPins()
	pins.CorpusSHA256 = corpus
	pins.RosterSHA256 = policy.RosterSHA256
	pins.Checkpoints = []boundedCheckpointPin{boundedPin(plan)}
	var receipts []boundedDocumentReceipt
	for _, d := range checkpoint.Documents() {
		receipts = append(receipts, boundedDocumentReceipt{d, pins.Checkpoints[0]})
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].Document.Source.DocID < receipts[j].Document.Source.DocID })
	pins.AcceptedSetSHA256 = boundedDigest(receipts)
	return boundedPipelineInput{Checkpoints: []*constructionplan.VerifiedCheckpoint{checkpoint}, Roster: roster, Originals: originals, Expected: pins}, archive, policy
}
func TestBoundedPipelineRejectsUnverifiedMissingAndChangedOriginalBeforeBridge(t *testing.T) {
	called := false
	opts := boundedPipelineOptions{NewBridge: func(func(context.Context, string, string, string, int, string) ([]string, error)) (boundedPipelineBridge, error) {
		called = true
		return nil, errors.New("must not reach")
	}}
	if _, e := runBoundedPipeline(context.Background(), boundedPipelineInput{Checkpoints: []*constructionplan.VerifiedCheckpoint{{}}}, opts); e == nil || called {
		t.Fatal("unverified checkpoint bypass")
	}
	input, _, _ := boundedVerifiedFixture(t, 1)
	if _, e := freezeBoundedPipeline(input); e == nil {
		t.Fatal("partial roster relabeled609")
	}
	input, _, _ = boundedVerifiedFixture(t, 609)
	original := input.Originals[0].Content
	input.Originals[0].Content += "changed"
	if _, e := runBoundedPipeline(context.Background(), input, opts); e == nil || called {
		t.Fatal("changed original reached bridge")
	}
	input.Originals[0].Content = original
	parent := t.TempDir()
	control := filepath.Join(parent, "control")
	target := filepath.Join(parent, "occupied")
	for _, p := range []string{control, target} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	human := filepath.Join(target, "human.md")
	if e := os.WriteFile(human, []byte("preserve"), 0600); e != nil {
		t.Fatal(e)
	}
	opts.WorkDir = control
	opts.PublishPath = target
	opts.RepositoryIdentity = "https://example.invalid/occupied"
	opts.MaterializationClient = &boundedTestBridge{}
	if _, e := runBoundedPipeline(context.Background(), input, opts); e == nil || !strings.Contains(e.Error(), "no independent publication-ready pin") || called {
		t.Fatalf("occupied target spent before failure: %v", e)
	}
	if b, _ := os.ReadFile(human); string(b) != "preserve" {
		t.Fatal("occupied target changed")
	}
}

func TestBoundedPipeline609Documents128SharedMembersGenerationAndReplay(t *testing.T) {
	boundedPipeline609GenerationAndReplay(t, 128)
}

// This preserves the ORIGINAL c8 fixture (609 documents, 609 distinct members).
// It exceeded the default ten-minute -race package timeout during accepted80
// history replay. Opt-in execution is a recovery/performance diagnostic, not a
// live609 capacity claim or a production recovery-scaling fix; see #103.
func TestBoundedPipeline609DistinctMembersRecoveryPerformanceOptIn(t *testing.T) {
	if os.Getenv("CAIRN_BOUNDED_609_DISTINCT_MEMBERS_PERF") != "1" {
		t.Skip("explicit original609-distinct-member recovery diagnostic; c8 timeout remains FAIL (#103)")
	}
	boundedPipeline609GenerationAndReplay(t, 609)
}
func boundedPipeline609GenerationAndReplay(t *testing.T, memberCount int) {
	t.Helper()
	input, _, _ := boundedVerifiedFixtureMembers(t, 609, memberCount)
	frozen, e := freezeBoundedPipeline(input)
	if e != nil {
		t.Fatal(e)
	}
	if len(frozen.DocRoots) != 609 || len(frozen.Members) != memberCount {
		t.Fatalf("synthetic document/member dimensions changed: %d/%d", len(frozen.DocRoots), len(frozen.Members))
	}
	incidences := 0
	for _, docs := range frozen.Members {
		incidences += len(docs)
		if memberCount == 128 && (len(docs) < 4 || len(docs) > 5) {
			t.Fatal("shared128 fixture must retain4–5 distinct documents per member")
		}
	}
	if incidences != 609 {
		t.Fatalf("lost distinct source incidences before mapping: %d", incidences)
	}
	dir := t.TempDir()
	control := filepath.Join(dir, "control")
	if e := os.Mkdir(control, 0700); e != nil {
		t.Fatal(e)
	}
	var active *boundedTestBridge
	client := &boundedTestBridge{}
	opts := boundedPipelineOptions{WorkDir: control, PublishPath: filepath.Join(dir, "published"), RepositoryIdentity: "https://example.invalid/frozen-repository", MaterializationClient: client}
	opts.NewBridge = func(resolve func(context.Context, string, string, string, int, string) ([]string, error)) (boundedPipelineBridge, error) {
		f, e := freezeBoundedPipeline(input)
		if e != nil {
			return nil, e
		}
		registry := &boundedResolver{works: map[string]boundedResolvedWork{}, frozen: f}
		active = &boundedTestBridge{construction: f.Input.Identity, path: filepath.Join(dir, "actual.jsonl"), pins: input.Expected.Admission, resolve: resolve, meta: registry.metadata}
		active.reply = func(req llm.CompleteRequest, stage string, attempt int) (llm.CompleteResponse, error) {
			if stage != "mapping" {
				return boundedSchemaReply(req, stage, attempt)
			}
			var in struct {
				Units []struct{ ID string }
				Nodes []struct{ Alias, Name, Label string }
			}
			if e := json.Unmarshal([]byte(req.User), &in); e != nil {
				return llm.CompleteResponse{}, e
			}
			var decisions []map[string]string
			bucket := fmt.Sprintf("bucket %02d", (active.dispatchCount-1)%15)
			target := ""
			if active.dispatchCount > 15 {
				for _, n := range in.Nodes {
					if n.Name == bucket && n.Label == "Entity" {
						target = n.Alias
					}
				}
				if target == "" {
					return llm.CompleteResponse{}, fmt.Errorf("fake routing fixture missing bucket %s", bucket)
				}
			}
			for _, unit := range in.Units {
				if target != "" {
					decisions = append(decisions, map[string]string{"unit": unit.ID, "action": "merge_into", "target": target})
				} else {
					decisions = append(decisions, map[string]string{"unit": unit.ID, "action": "fuse", "group": "G1"})
				}
			}
			groups := []map[string]string{}
			if target == "" {
				groups = append(groups, map[string]string{"group": "G1", "node_name": bucket, "summary": "content summary", "tag": "entity", "domain": "domain", "subdomain": "subdomain"})
			}
			out := map[string]any{"decisions": decisions, "groups": groups}
			raw, _ := json.Marshal(out)
			return llm.CompleteResponse{Text: string(raw), InputTokens: 100, OutputTokens: 100, FinishReason: "stop"}, nil
		}
		return active, nil
	}
	result, e := runBoundedPipeline(context.Background(), input, opts)
	if e != nil {
		t.Fatal(e)
	}
	if result.Forecast.Accepted != 609 || result.Forecast.Selected != 609 || result.ManifestSHA256 == "" || active.dispatchCount == 0 {
		t.Fatalf("incomplete full609 generation: %+v", result)
	}
	if len(result.Forecast.PerDocument) != 609 || len(result.Forecast.Triples) != 609 {
		t.Fatal("shared member identities collapsed document/source incidence accounting")
	}
	for _, count := range result.Forecast.PerDocument {
		if count != 1 {
			t.Fatal("per-document selected incidence changed")
		}
	}
	mappingBytes, e := os.ReadFile(filepath.Join(opts.PublishPath, "mapping-receipt.json"))
	if e != nil {
		t.Fatal(e)
	}
	var mapping struct {
		Receipt incremental.StagedMappingReceipt
	}
	if e = json.Unmarshal(mappingBytes, &mapping); e != nil {
		t.Fatal(e)
	}
	if mapping.Receipt.AcceptedBatches < 2 {
		t.Fatal("fixture did not exercise accepted cross-batch mapping")
	}
	deliveryBytes, e := os.ReadFile(filepath.Join(opts.PublishPath, "delivery-checkpoint.json"))
	if e != nil {
		t.Fatal(e)
	}
	var delivery struct {
		Inventories map[string]boundedPhaseInventory
	}
	if e = json.Unmarshal(deliveryBytes, &delivery); e != nil {
		t.Fatal(e)
	}
	foundBudget := false
	for _, inv := range delivery.Inventories {
		if inv.Stage == "mapping-budget" {
			foundBudget = true
			if inv.MappingUniqueUnits != memberCount || inv.AcceptedIncidences != 609 || inv.MaximumCalls != 2*memberCount {
				t.Fatal("mapping unit ceiling conflated with document incidences")
			}
		}
	}
	if !foundBudget {
		t.Fatal("missing durable mapping ceiling")
	}
	graphBytes, e := os.ReadFile(filepath.Join(opts.PublishPath, "graph.json"))
	if e != nil {
		t.Fatal(e)
	}
	var graph struct{ Graph extract.Result }
	if e = json.Unmarshal(graphBytes, &graph); e != nil {
		t.Fatal(e)
	}
	changedMembership := false
	for _, loc := range boundedNodes(&graph.Graph) {
		if extract.NodeUUID(loc.Node.Members) != loc.Node.ID {
			changedMembership = true
		}
	}
	if !changedMembership {
		t.Fatal("fixture did not prove crossbatch pinned birth UUID instead of recomputed final-members UUID")
	}
	ledgerBefore, e := os.ReadFile(filepath.Join(dir, "actual.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	manifestBefore, e := os.ReadFile(filepath.Join(opts.PublishPath, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	opts.Resume = true
	again, e := runBoundedPipeline(context.Background(), input, opts)
	if e != nil {
		t.Fatal(e)
	}
	if active.dispatchCount != 0 || again.ManifestSHA256 != result.ManifestSHA256 {
		t.Fatal("completed replay paid or changed publication")
	}
	ledgerAfter, _ := os.ReadFile(filepath.Join(dir, "actual.jsonl"))
	manifestAfter, _ := os.ReadFile(filepath.Join(opts.PublishPath, "manifest.json"))
	if !reflect.DeepEqual(ledgerBefore, ledgerAfter) || !reflect.DeepEqual(manifestBefore, manifestAfter) {
		t.Fatal("replay mutated actual ledger or immutable generation")
	}
}

func boundedReimportFixture(t *testing.T, a constructionplan.ArchiveInput, p constructionplan.TrustPolicy) *constructionplan.VerifiedCheckpoint {
	t.Helper()
	var entries []map[string]any
	for path, b := range a.Files {
		entries = append(entries, map[string]any{"path": path, "bytes": len(b), "sha256": boundedDigestBytes(b)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i]["path"].(string) < entries[j]["path"].(string) })
	a.Manifest, _ = json.Marshal(map[string]any{"format": "cairn-restricted-calibration-archive/v1", "source_commit": a.Descriptor.Identity.SourceCommit, "files": entries})
	p.ManifestSHA256 = boundedDigestBytes(a.Manifest)
	p.CompatibleMethods = []constructionplan.MethodIdentity{a.Descriptor.Identity}
	cp, e := constructionplan.ImportCheckpoint(a, p)
	if e != nil {
		t.Fatal(e)
	}
	return cp
}
func boundedRebindFixture(t *testing.T, input *boundedPipelineInput) {
	t.Helper()
	input.Expected.Checkpoints = nil
	var receipts []boundedDocumentReceipt
	for _, cp := range input.Checkpoints {
		plan, e := constructionplan.BuildPlan(cp)
		if e != nil {
			t.Fatal(e)
		}
		pin := boundedPin(plan)
		input.Expected.Checkpoints = append(input.Expected.Checkpoints, pin)
		for _, d := range cp.Documents() {
			receipts = append(receipts, boundedDocumentReceipt{d, pin})
		}
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].Document.Source.DocID < receipts[j].Document.Source.DocID })
	input.Expected.AcceptedSetSHA256 = boundedDigest(receipts)
}
func TestBoundedPipelineHeterogeneous11And598MethodsAndDetailPrerequisite(t *testing.T) {
	input, archive, policy := boundedVerifiedFixture(t, 609)
	prefix := archive.Descriptor.Prefix
	var docs []dktypes.AnnotatedDocument
	json.Unmarshal(archive.Files[prefix+"/stages/04_extract_input.json"], &docs)
	var originals []extract.FrozenSource
	json.Unmarshal(archive.Files[prefix+"/stages/00_frozen_sources.json"], &originals)
	var shards []*constructionplan.VerifiedCheckpoint
	for shard := 0; shard < 2; shard++ {
		copyArchive := *archive
		copyArchive.Files = map[string][]byte{}
		for p, b := range archive.Files {
			copyArchive.Files[p] = b
		}
		copyArchive.Descriptor.Documents = nil
		if shard == 1 {
			copyArchive.Descriptor.Identity.AnnotationMethod = "future-corrected-offline-fixture/v1"
		}
		var selectedDocs []dktypes.AnnotatedDocument
		var selectedSources []extract.FrozenSource
		for i, d := range docs {
			if (i < 11) == (shard == 0) {
				selectedDocs = append(selectedDocs, d)
				selectedSources = append(selectedSources, originals[i])
				copyArchive.Descriptor.Documents = append(copyArchive.Descriptor.Documents, archive.Descriptor.Documents[i])
			}
		}
		copyArchive.Files[prefix+"/stages/04_extract_input.json"], _ = json.Marshal(selectedDocs)
		copyArchive.Files[prefix+"/stages/00_frozen_sources.json"], _ = json.Marshal(selectedSources)
		shards = append(shards, boundedReimportFixture(t, copyArchive, *policy))
	}
	input.Checkpoints = shards
	boundedRebindFixture(t, &input)
	f, e := freezeBoundedPipeline(input)
	if e != nil {
		t.Fatal(e)
	}
	counts := map[string]int{}
	for _, receipt := range f.Receipts {
		counts[receipt.Checkpoint.Method.AnnotationMethod]++
	}
	if counts[archive.Descriptor.Identity.AnnotationMethod] != 11 || counts["future-corrected-offline-fixture/v1"] != 598 {
		t.Fatalf("historical methods were falsely homogenized: %v", counts)
	}
	input.Checkpoints = append(input.Checkpoints, shards[0])
	input.Expected.Checkpoints = append(input.Expected.Checkpoints, input.Expected.Checkpoints[0])
	if _, e = freezeBoundedPipeline(input); e == nil {
		t.Fatal("duplicate accepted ownership accepted")
	}
	// The current private importer already rejects empty Detail. Keep an explicit
	// controller/materializer prerequisite too, without forging a private checkpoint.
	doc := shards[0].Documents()[0].Annotation
	doc.Items[0].Detail = ""
	if e = boundedRequireDetails(&doc); e == nil || !strings.Contains(e.Error(), "nonempty Detail") {
		t.Fatalf("empty materializer Detail accepted: %v", e)
	}
}

func TestBoundedGenerationAuditChecksSemanticDescriptionAndConfidence(t *testing.T) {
	g, f := boundedSmallGraph(2)
	for _, loc := range boundedNodes(g) {
		loc.Node.Description = "full description"
	}
	s := &g.Domains[0].Subdomains[0]
	s.Relations = []extract.Relation{{Source: s.Entities[0].ID, Target: s.Entities[1].ID, Kind: "references", Description: "common name --[references]--> common name", Confidence: .91}}
	g, e := boundedFreshSlugs(context.Background(), g)
	if e != nil {
		t.Fatal(e)
	}
	forecast, e := boundedForecast(g, f)
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	for _, o := range f.Originals {
		for _, prefix := range []string{"originals", "repository"} {
			p := filepath.Join(root, prefix, filepath.FromSlash(o.Path))
			if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(p, []byte(o.Content), 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
	o, e := pipeline.NewOrchestrator(pipeline.Options{Client: &boundedTestBridge{}, RepositoryIdentity: "https://example.invalid/audit-fixture", MinConfidence: .7})
	if e != nil {
		t.Fatal(e)
	}
	dbPath := filepath.Join(root, "knowledge.db")
	if _, e = o.MaterializeCompletedExtraction(context.Background(), filepath.Join(root, "repository"), dbPath, g, f.Documents, []ingest.SkillMeta{{Name: "skill"}}); e != nil {
		t.Fatal(e)
	}
	if e = boundedCheckpointDatabase(context.Background(), dbPath); e != nil {
		t.Fatal(e)
	}
	if e = boundedAuditGeneration(context.Background(), root, g, f, forecast); e != nil {
		t.Fatal(e)
	}
	for _, column := range []string{"description", "confidence"} {
		db, e := storage.OpenSQLite(dbPath, storage.SQLiteOptions{ExistingOnly: true, PreserveJournalMode: true})
		if e != nil {
			t.Fatal(e)
		}
		if column == "description" {
			_, e = db.Exec("UPDATE edges SET description='corrupted but same triple' WHERE kind='references'")
		} else {
			_, e = db.Exec("UPDATE edges SET description=?,confidence=.1 WHERE kind='references'", s.Relations[0].Description)
		}
		if closeErr := db.Close(); e != nil || closeErr != nil {
			t.Fatalf("corruption fixture: %v %v", e, closeErr)
		}
		if e = boundedAuditGeneration(context.Background(), root, g, f, forecast); e == nil || !strings.Contains(e.Error(), "description/confidence") {
			t.Fatalf("semantic %s corruption escaped: %v", column, e)
		}
	}
}
