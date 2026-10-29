package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

const v016ChangedSource = "Atlas pump may run only above 2 bar; it requires the safety valve when bypass is off. Safety valve remains closed during maintenance."
const v016ChangedSummary = "Qualified Atlas pump operation"
const v016ChangedDetail = "Atlas pump must not run during maintenance. Atlas pump may run only above 2 bar; it requires the safety valve when bypass is off."
const v016ValveDetail = "Safety valve remains closed during maintenance."

type v016ChangedClient struct {
	mu       sync.Mutex
	source   string
	requests []llm.CompleteRequest
}

func (*v016ChangedClient) ProviderName() string { return "offline-v016-changed-control" }
func (c *v016ChangedClient) Complete(_ context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, r)
	if strings.Contains(r.User, "11111111-1111-5111-8111-111111111111") || strings.Contains(r.User, "22222222-2222-5222-8222-222222222222") || strings.Contains(r.User, "<!-- kg:node") {
		return nil, fmt.Errorf("persistent identity or generated Markdown entered model source input")
	}
	result := func(v any) (*llm.CompleteResponse, error) {
		b, e := json.Marshal(v)
		return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}, e
	}
	switch {
	case strings.Contains(r.System, "知识结构化标注专家"):
		if !strings.Contains(r.User, v016ChangedSource) {
			return nil, fmt.Errorf("expected actual plain changed original")
		}
		content := strings.SplitN(r.User, "文档内容 / content:\n", 2)[1]
		line := 1 + strings.Count(content[:strings.Index(content, v016ChangedSource)], "\n")
		span := map[string]any{"start_line": line, "end_line": line, "quote": v016ChangedSource}
		return result(map[string]any{"items": []any{map[string]any{"tag": "entity", "name": "Atlas pump", "content": "Atlas pump may run only above 2 bar.", "detail": "Atlas pump may run only above 2 bar; it requires the safety valve when bypass is off.", "confidence": .95, "source_span": span}, map[string]any{"tag": "entity", "name": "Safety valve", "content": v016ValveDetail, "detail": v016ValveDetail, "confidence": .95, "source_span": span}}})
	case strings.Contains(r.System, "语义核验专家"):
		return result(map[string]any{"supported": true, "reason": "fixed original and complete node pair retain all conditions", "unsupported_items": []int{}, "omissions": []string{}})
	case strings.Contains(r.System, "增量") && strings.Contains(r.User, "unit_id="):
		decisions := []map[string]string{}
		for _, m := range regexp.MustCompile(`unit_id=([^\s]+)`).FindAllStringSubmatch(r.User, -1) {
			name := "Atlas pump"
			if strings.Contains(m[1], "safety-valve") {
				name = "Safety valve"
			}
			alias := regexp.MustCompile(`alias=([^\s]+) \[Entity\] ` + regexp.QuoteMeta(name) + `（`).FindStringSubmatch(r.User)
			if len(alias) != 2 {
				return nil, fmt.Errorf("missing real recall target %s", name)
			}
			decisions = append(decisions, map[string]string{"unit": m[1], "action": "merge_into", "target": alias[1]})
		}
		return result(map[string]any{"decisions": decisions, "groups": []any{}})
	case strings.Contains(r.System, "知识融合重写专家"):
		alias := regexp.MustCompile(`Node alias=([^\s]+)`).FindStringSubmatch(r.User)
		if len(alias) != 2 {
			return nil, fmt.Errorf("missing fusion alias")
		}
		summary, detail := v016ChangedSummary, v016ChangedDetail
		if strings.Contains(r.User, `"name":"Safety valve"`) {
			summary, detail = "Safety valve maintenance rule", v016ValveDetail
		}
		return result(map[string]any{"descriptions": []any{map[string]string{"node_id": alias[1], "summary": summary, "description": detail}}})
	case strings.Contains(r.System, "SOURCE-RELATION GENERATE"):
		var in struct {
			FocusSourceID string `json:"focus_source_id"`
			Nodes         []struct{ Alias, Name string }
			Sources       []struct{ ID, Content string } `json:"original_sources"`
		}
		if e := json.Unmarshal([]byte(r.User), &in); e != nil {
			return nil, e
		}
		for _, source := range in.Sources {
			if source.ID == in.FocusSourceID && !strings.Contains(source.Content, v016ChangedSource) {
				return result(map[string]any{"relations": []any{}})
			}
		}
		a, b := "", ""
		for _, n := range in.Nodes {
			if n.Name == "Atlas pump" {
				a = n.Alias
			}
			if n.Name == "Safety valve" {
				b = n.Alias
			}
		}
		if !strings.Contains(r.User, v016ChangedSource) {
			return nil, fmt.Errorf("generated old blocks must not replace actual changed source")
		}
		return result(map[string]any{"relations": []any{map[string]any{"source": a, "target": b, "kind": "depends_on", "description": "Atlas pump requires safety valve only when bypass is off.", "confidence": .95}}})
	case strings.Contains(r.System, "SOURCE-RELATION COVERAGE"):
		return result(map[string]any{"complete": true, "supported": true, "reason": "fixed current source condition retained"})
	case strings.Contains(r.System, "原文验证专家"):
		var in struct {
			Source, Target struct{ Members []string }
			Relation       struct{ Kind string } `json:"relation_candidate"`
			Sources        []struct {
				ID, Content string
				Members     []string
			} `json:"original_sources"`
		}
		if e := json.Unmarshal([]byte(r.User), &in); e != nil {
			return nil, e
		}
		evidence := []map[string]string{}
		for role, members := range map[string][]string{"source": in.Source.Members, "target": in.Target.Members} {
			found := false
			for _, s := range in.Sources {
				if s.Content != c.source {
					continue
				}
				for _, m := range members {
					for _, sm := range s.Members {
						if m == sm {
							evidence = append(evidence, map[string]string{"endpoint": role, "member_id": m, "source_id": s.ID, "quote": s.Content})
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				if found {
					break
				}
			}
		}
		if len(evidence) != 2 {
			return result(map[string]any{"disposition": "insufficient_evidence", "reason": "old generated material is not original evidence"})
		}
		return result(map[string]any{"disposition": "supported", "reason": "fixed original supplies both current member bindings", "relation_supported": true, "direction_supported": true, "explicit_source_assertion": in.Relation.Kind == "depends_on", "evidence": evidence})
	default:
		return nil, fmt.Errorf("unexpected changed fixture stage: %s", r.System)
	}
}

// 原故障：普通Run只消费旧description适配器，已核验的新摘要被推迟，原文关系入口未接通。
func TestV016ReviewChangedGeneratedSpanProof(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"new-document", "same-path-supplied", "same-path-missing"} {
		t.Run(mode, func(t *testing.T) { v016ChangedEntry(t, mode) })
	}
}
func v016ChangedEntry(t *testing.T, mode string) {
	ctx := context.Background()
	root := t.TempDir()
	skill := filepath.Base(root)
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	db, e := storage.NewDB(storage.DBOptions{Path: dbPath})
	if e != nil {
		t.Fatal(e)
	}
	st := newStores(db)
	pump := &dktypes.Node{ID: "11111111-1111-5111-8111-111111111111", Label: dktypes.LabelEntity, Name: "Atlas pump", Summary: "Atlas pump always runs.", Description: "Atlas pump must not run during maintenance.", Domain: "operations", Subdomain: "pumps", Provenance: dktypes.ProvenanceExtraction, Confidence: .9}
	valve := &dktypes.Node{ID: "22222222-2222-5222-8222-222222222222", Label: dktypes.LabelEntity, Name: "Safety valve", Summary: "Safety valve maintenance rule", Description: v016ValveDetail, Domain: pump.Domain, Subdomain: pump.Subdomain, Provenance: dktypes.ProvenanceExtraction, Confidence: .9}
	for _, n := range []*dktypes.Node{pump, valve, {ID: "domain", Label: dktypes.LabelDomain, Name: "Operations", Domain: pump.Domain, Provenance: dktypes.ProvenanceExtraction}, {ID: "subdomain", Label: dktypes.LabelSubdomain, Name: "Pumps", Domain: pump.Domain, Subdomain: pump.Subdomain, Provenance: dktypes.ProvenanceExtraction}} {
		if e := st.nodes.Insert(ctx, n); e != nil {
			t.Fatal(e)
		}
	}
	for _, n := range []*dktypes.Node{pump, valve} {
		if e := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: n.ID, MemberID: "old-" + strings.ReplaceAll(strings.ToLower(n.Name), " ", "-"), Skill: skill, FilePath: "references/old.md", StartLine: map[string]int{"Atlas pump": 2, "Safety valve": 3}[n.Name], EndLine: map[string]int{"Atlas pump": 2, "Safety valve": 3}[n.Name]}}); e != nil {
			t.Fatal(e)
		}
	}
	old := &dktypes.Edge{SourceID: pump.ID, TargetID: valve.ID, Kind: dktypes.KindReferences, Description: "Historical unverified inference", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 0}
	if e := st.edges.Insert(ctx, old); e != nil {
		t.Fatal(e)
	}
	if e := st.identity.Set(ctx, "local:v016-changed"); e != nil {
		t.Fatal(e)
	}
	if _, e := st.version.Bump(ctx); e != nil {
		t.Fatal(e)
	}
	if e := st.fts.RebuildAll(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := applyIncrementalWriteback(ctx, st, newRunStateWithDocs("references/old.md"), nil, nil, root); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if e := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(""), 0644); e != nil {
		t.Fatal(e)
	}
	c := &v016ChangedClient{source: v016ChangedSource}
	rawOld := "# Pump and valve\n" + pump.Description + "\n" + valve.Description
	oldDocs := []*dktypes.AnnotatedDocument{{Skill: skill, FilePath: "references/old.md", Items: []dktypes.AnnotatedItem{{ID: "old-atlas-pump", Tag: dktypes.TagEntity, Name: pump.Name, Content: pump.Description, Detail: pump.Description}, {ID: "old-safety-valve", Tag: dktypes.TagEntity, Name: valve.Name, Content: valve.Description, Detail: valve.Description}}}}
	o, e := NewIncrementalOrchestrator(Options{Client: c, RepositoryIdentity: "local:v016-changed", MaxRetries: 1})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := o.Run(ctx, root, dbPath); e != nil {
		t.Fatal(e)
	} // establish clean file baselines before C4
	var suppliedSources []extract.FrozenSource
	var suppliedDocs []*dktypes.AnnotatedDocument
	changedPath := "references/new.md"
	if mode != "new-document" {
		changedPath = "references/old.md"
	}
	if mode == "new-document" {
		suppliedSources = []extract.FrozenSource{extract.FreezeSource(skill, "references/old.md", rawOld)}
		suppliedDocs = oldDocs
		o, e = NewIncrementalOrchestrator(Options{Client: c, RepositoryIdentity: "local:v016-changed", MaxRetries: 1, Sources: suppliedSources, SourceDocuments: suppliedDocs})
		if e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(root, changedPath), []byte(v016ChangedSource), 0644); e != nil {
			t.Fatal(e)
		}
	} else {
		generated, e := os.ReadFile(filepath.Join(root, changedPath))
		if e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(root, changedPath), append(generated, []byte("\n\n"+v016ChangedSource+"\n")...), 0644); e != nil {
			t.Fatal(e)
		}
		if mode == "same-path-supplied" {
			c.source = "# Pump and valve\nAtlas pump must not run during maintenance.\n" + v016ValveDetail + "\n" + v016ChangedSource
			oldDocs := []*dktypes.AnnotatedDocument{{Skill: skill, FilePath: changedPath, Items: []dktypes.AnnotatedItem{{ID: "old-atlas-pump", Tag: dktypes.TagEntity, Name: pump.Name, Content: pump.Description, Detail: pump.Description}, {ID: "old-safety-valve", Tag: dktypes.TagEntity, Name: valve.Name, Content: valve.Description, Detail: valve.Description}}}}
			suppliedSources = []extract.FrozenSource{extract.FreezeSource(skill, changedPath, c.source)}
			suppliedDocs = oldDocs
			o, e = NewIncrementalOrchestrator(Options{Client: c, RepositoryIdentity: "local:v016-changed", MaxRetries: 1, Sources: suppliedSources, SourceDocuments: suppliedDocs})
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	contextBefore, _ := json.Marshal([]any{suppliedSources, suppliedDocs})
	rpt, e := o.Run(ctx, root, dbPath)
	contextAfter, _ := json.Marshal([]any{suppliedSources, suppliedDocs})
	if string(contextBefore) != string(contextAfter) {
		t.Fatal("ordinary caller source snapshots/member bindings were mutated")
	}
	if mode == "same-path-missing" {
		if e == nil || rpt == nil {
			t.Fatalf("missing original was reported complete: %+v err=%v", rpt, e)
		}
		if !rpt.SourceAssociationsVerified || rpt.SourceAssociations.Attempted != 2 || rpt.SourceAssociations.DistinctSelected != 2 || rpt.SourceAssociations.DuplicateRows != 0 || rpt.SourceAssociations.Committed != 0 || rpt.NodesMerged != 0 {
			t.Fatalf("failed dependent join counted uncommitted sources/nodes as progress: %+v", rpt)
		}

		var missing struct {
			Status    string
			Reasons   []string
			Relations *extract.RelationProcessReport
		}
		raw, _ := json.Marshal(rpt)
		if err := json.Unmarshal(raw, &missing); err != nil {
			t.Fatal(err)
		}
		if missing.Status == "complete" || missing.Status == "" || len(missing.Reasons) == 0 || missing.Relations == nil || len(missing.Relations.Selected) != 0 {
			t.Fatalf("missing context hidden or generated text credited: %+v", missing)
		}
		body, err := os.ReadFile(filepath.Join(root, changedPath))
		if err != nil || !strings.Contains(string(body), v016ChangedSource) {
			t.Fatal("unresolved original prose lost")
		}
		check, err := storage.OpenReadOnly(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer check.Close()
		rows, spanErr := storage.NewNodeSourceRepo(check).ListByFile(ctx, changedPath)
		if spanErr != nil {
			t.Fatal(spanErr)
		}
		for _, row := range rows {
			if !strings.HasPrefix(row.MemberID, "old-") && (row.StartLine != 0 || row.EndLine != 0) {
				t.Errorf("generated C4 coordinates persisted as original source without a raw snapshot: %+v", row)
			}
		}
		retained, err := storage.NewEdgeRepo(check).GetOutgoing(ctx, pump.ID, 0)
		if err != nil || len(retained) != 1 || retained[0].Kind != dktypes.KindReferences || retained[0].Confidence != 0 || retained[0].Description != old.Description {
			t.Fatalf("unavailable join changed old inference: %+v %v", retained, err)
		}
		next, nextErr := o.Run(ctx, root, dbPath)
		if nextErr == nil && next.Status == "complete" {
			t.Errorf("unresolved changed relation work silently became complete on unchanged retry: %+v", next)
		}
		return
	}
	if e != nil {
		t.Fatalf("supported current original failed ordinary change: %+v err=%v", rpt, e)
	}
	if rpt.NodesMerged != 2 || rpt.UnitsAnnotated != 2 {
		t.Fatalf("ordinary C4/alignment not exercised: %+v", rpt)
	}
	if !rpt.SourceAssociationsVerified || rpt.SourceAssociations.Attempted != 2 || rpt.SourceAssociations.DistinctSelected != 2 || rpt.SourceAssociations.DuplicateRows != 0 || rpt.SourceAssociations.Committed != 2 {
		t.Fatalf("source association counts are not actual selected/readback facts: %+v", rpt)
	}

	var view struct {
		Status       string
		Reasons      []string
		NodeOutcomes []extract.NodeDescriptionOutcome
		Relations    *extract.RelationProcessReport
	}
	b, _ := json.Marshal(rpt)
	if e := json.Unmarshal(b, &view); e != nil {
		t.Fatal(e)
	}
	if view.Status != "complete" || len(view.NodeOutcomes) != 2 {
		t.Fatalf("factual node outcomes/status lost: %+v", view)
	}
	acceptedPair := false
	for _, out := range view.NodeOutcomes {
		if out.Summary == v016ChangedSummary && out.Description == v016ChangedDetail && out.Outcome == annotation.DocumentComplete && out.CandidateID != "" {
			acceptedPair = true
		}
	}
	if !acceptedPair {
		t.Fatalf("existing summary correction never accepted: %+v", view.NodeOutcomes)
	}
	check, e := storage.OpenReadOnly(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer check.Close()
	got, e := storage.NewNodeRepo(check).GetByID(ctx, pump.ID)
	if e != nil || got == nil || got.Summary != v016ChangedSummary || got.Description != v016ChangedDetail {
		t.Fatalf("verified complete pair was not used by actual I8: %+v %v", got, e)
	}
	edges, e := storage.NewEdgeRepo(check).GetOutgoing(ctx, pump.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	legacy, explicit := false, false
	for _, edge := range edges {
		if edge.Kind == dktypes.KindReferences {
			legacy = true
			if edge.Confidence != 0 || strings.Contains(edge.Description, `"classification":"explicit"`) {
				t.Fatalf("old inference acquired source assertion credit: %+v", edge)
			}
		}
		if edge.Kind == dktypes.KindDependsOn {
			explicit = true
			if !strings.Contains(edge.Description, `"classification":"explicit"`) || !strings.Contains(edge.Description, changedPath) {
				t.Fatalf("current source evidence not persisted: %+v", edge)
			}
		}
	}
	if !legacy || !explicit {
		t.Fatalf("independent legacy/current relationship distinctions lost: %+v", edges)
	}
	if view.Relations == nil || len(view.Relations.Selected) == 0 {
		t.Fatal("ordinary changed path did not call source-aware helper")
	}
	// Old genuine representative spans survive; nil new source spans remain unknown.
	rows, err := storage.NewNodeSourceRepo(check).ListByNode(ctx, pump.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("changed member/source incidence dropped: %+v", rows)
	}
	for _, row := range rows {
		if row.MemberID == "old-atlas-pump" {
			if row.StartLine != 2 || row.EndLine != 2 {
				t.Fatalf("old original span replaced by generated offsets: %+v", row)
			}
		} else {
			want := 1
			if mode == "same-path-supplied" {
				want = 4
			}
			if row.StartLine != want || row.EndLine != want {
				t.Fatalf("changed original source span is not rebased from supplied raw bytes: %+v want=%d", row, want)
			}
		}
	}

	sc, e := writeback.ReadSidecar(filepath.Join(root, changedPath))
	if e != nil {
		t.Fatal(e)
	}
	matched := false
	for _, n := range sc.Nodes {
		if n.UUID == pump.ID {
			matched = true
			if n.SummaryHash != writeback.HashEditable(got.Summary) || n.DescriptionHash != writeback.HashEditable(got.Description) {
				t.Fatal("changed file pair disagrees with SQLite")
			}
			var snapshots []dktypes.Edge
			for _, s := range n.GraphMetadata.Relations {
				snapshots = append(snapshots, s.Edge())
			}
			var expected []dktypes.Edge
			allEdges, err := storage.NewEdgeRepo(check).ListAll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, edge := range allEdges {
				if edge.SourceID == pump.ID || edge.TargetID == pump.ID {
					expected = append(expected, *edge)
				}
			}
			sort.Slice(expected, func(i, j int) bool { return expected[i].ID < expected[j].ID })
			if !reflect.DeepEqual(snapshots, expected) {
				t.Fatalf("changed file graph snapshot disagrees: %+v / %+v", snapshots, expected)
			}
		}
	}
	if !matched {
		t.Fatal("merged node absent from changed source file")
	}
}

func TestV016ReviewC1FirstMutationFailureStatus(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	path := filepath.Join(dir, "graph.db")
	identityFixture(t, root, path, "local:review-status", true)
	if err := os.WriteFile(filepath.Join(root, "old-skill/SKILL.md"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Conn().Exec(`UPDATE nodes SET summary='Original summary',description='Original detail' WHERE id=?`, identityTestNode); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := writeback.RewriteDoc(root, identityTestDoc, []writeback.NodeUpdate{{UUID: identityTestNode, Tag: "Concept", Name: "A-owned knowledge", Summary: "Original summary", Description: "Original detail", Domain: "engineering", Subdomain: "queues", DomainSlug: "engineering", SubdomainSlug: "queues", Members: []string{"m1"}, Provenance: "extraction"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: "local:review-status"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Run(ctx, root, path); err != nil {
		t.Fatal(err)
	}
	db, err = storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Conn().Exec(`CREATE TRIGGER review_reject_update BEFORE UPDATE ON nodes BEGIN SELECT RAISE(ABORT,'injected first node mutation failure'); END`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := filepath.Join(root, identityTestDoc)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(b), "Original detail", "Human replacement detail", 1)
	if edited == string(b) {
		t.Fatal("no editable detail")
	}
	if err = os.WriteFile(p, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}
	report, runErr := o.Run(ctx, root, path)
	check, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	node, err := storage.NewNodeRepo(check).GetByID(ctx, identityTestNode)
	if err != nil {
		t.Fatal(err)
	}
	if runErr == nil || node.Description != "Original detail" || report.DocsRewritten != 0 {
		t.Fatalf("invalid failure probe: %+v %v node=%+v", report, runErr, node)
	}
	t.Logf("status=%s C1Detected=%d rewritten=%d actualNodeDescription=%q err=%v", report.Status, report.C1Edits, report.DocsRewritten, node.Description, runErr)
	if report.Status != "failed" {
		t.Fatalf("no mutation committed, but aggregate status=%q", report.Status)
	}
}

// A committed independent C1 sibling is actual partial progress even when the next update aborts.
func TestV016C1CommittedSiblingKeepsPartialStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	root, path := filepath.Join(dir, "repo"), filepath.Join(dir, "graph.db")
	identityFixture(t, root, path, "local:review-partial", true)
	if err := os.WriteFile(filepath.Join(root, "old-skill/SKILL.md"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	secondID := "33333333-3333-5333-8333-333333333333"
	if _, err := db.Conn().Exec(`UPDATE nodes SET summary='Original summary',description='Original detail' WHERE id=?`, identityTestNode); err != nil {
		t.Fatal(err)
	}
	second := &dktypes.Node{ID: secondID, Label: dktypes.LabelConcept, Name: "Z-deferred knowledge", Summary: "Second summary", Description: "Second detail", Domain: "engineering", Subdomain: "queues", Provenance: dktypes.ProvenanceExtraction, Confidence: 1}
	if err := storage.NewNodeRepo(db).Insert(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewNodeSourceRepo(db).InsertBatch(ctx, []storage.NodeSource{{NodeUUID: secondID, MemberID: "m2", Skill: "old-skill", FilePath: identityTestDoc}}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	updates := []writeback.NodeUpdate{{UUID: identityTestNode, Tag: "Concept", Name: "A-owned knowledge", Summary: "Original summary", Description: "Original detail", Domain: "engineering", Subdomain: "queues", DomainSlug: "engineering", SubdomainSlug: "queues", Members: []string{"m1"}, Provenance: "extraction"}, {UUID: secondID, Tag: "Concept", Name: second.Name, Summary: second.Summary, Description: second.Description, Domain: second.Domain, Subdomain: second.Subdomain, DomainSlug: second.Domain, SubdomainSlug: second.Subdomain, Members: []string{"m2"}, Provenance: "extraction"}}
	if err := writeback.RewriteDoc(root, identityTestDoc, updates, time.Now()); err != nil {
		t.Fatal(err)
	}
	o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: "local:review-partial"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(ctx, root, path); err != nil {
		t.Fatal(err)
	}
	db, err = storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`CREATE TRIGGER reject_second_update BEFORE UPDATE ON nodes WHEN OLD.id='33333333-3333-5333-8333-333333333333' BEGIN SELECT RAISE(ABORT,'injected second node mutation failure'); END`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	docPath := filepath.Join(root, identityTestDoc)
	body, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(strings.Replace(string(body), "Original detail", "Human first detail", 1), "Second detail", "Human second detail", 1)
	if edited == string(body) {
		t.Fatal("missing real editable blocks")
	}
	if err := os.WriteFile(docPath, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}
	report, runErr := o.Run(ctx, root, path)
	check, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	first, err := storage.NewNodeRepo(check).GetByID(ctx, identityTestNode)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := storage.NewNodeRepo(check).GetByID(ctx, secondID)
	if err != nil {
		t.Fatal(err)
	}
	if runErr == nil || report.C1Edits != 2 || report.DocsRewritten != 0 || first.Description != "Human first detail" || retained.Description != "Second detail" {
		t.Fatalf("independent actual commit/failure control invalid: report=%+v err=%v first=%+v second=%+v", report, runErr, first, retained)
	}
	if report.Status != "partial" {
		t.Fatalf("actual committed sibling lost from aggregate: %+v", report)
	}
}
