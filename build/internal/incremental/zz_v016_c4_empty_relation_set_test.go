package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

const v016IndependentPump = "Atlas pump operates only above 2 bar. Safety valve remains closed during maintenance."

type v016EmptyRelationClient struct {
	base  v016ChangedClient
	mode  string
	calls atomic.Int64
}

func (c *v016EmptyRelationClient) ProviderName() string { return "offline-v016-empty-relations" }
func (c *v016EmptyRelationClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.calls.Add(1)
	if strings.Contains(req.User, "human-only protected relation") {
		return nil, fmt.Errorf("protected human relation entered model input")
	}
	respond := func(v any) (*llm.CompleteResponse, error) {
		b, err := json.Marshal(v)
		return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}, err
	}
	if strings.Contains(req.System, "SOURCE-RELATION GENERATE") && c.mode == "transport" {
		return nil, fmt.Errorf("offline injected relation transport failure")
	}
	if strings.Contains(req.System, "原文验证专家") {
		switch c.mode {
		case "unknown":
			return respond(map[string]any{"disposition": "insufficient_evidence", "reason": "offline source support is unknown"})
		case "rejected":
			return respond(map[string]any{"disposition": "rejected", "reason": "offline candidate is unsupported", "relation_supported": false, "direction_supported": false, "explicit_source_assertion": false})
		}
	}
	if c.mode == "empty" {
		switch {
		case strings.Contains(req.System, "知识结构化标注专家"):
			content := strings.SplitN(req.User, "文档内容 / content:\n", 2)[1]
			offset := strings.Index(content, v016IndependentPump)
			if offset < 0 {
				return nil, fmt.Errorf("missing independent changed source")
			}
			line := 1 + strings.Count(content[:offset], "\n")
			span := map[string]any{"start_line": line, "end_line": line, "quote": v016IndependentPump}
			items := []any{}
			for i, name := range []string{"Atlas pump", "Safety valve"} {
				text := []string{"Atlas pump operates only above 2 bar.", v016ValveDetail}[i]
				items = append(items, map[string]any{"tag": "entity", "name": name, "content": text, "detail": text, "confidence": .95, "source_span": span})
			}
			return respond(map[string]any{"items": items})
		case strings.Contains(req.System, "知识融合重写专家"):
			alias, ok := v013IncrementalNodeAlias(req.User)
			if !ok {
				return nil, fmt.Errorf("missing independent fusion alias")
			}
			detail := "Atlas pump must not run during maintenance. Atlas pump operates only above 2 bar."
			if strings.Contains(req.User, `"name":"Safety valve"`) {
				detail = v016ValveDetail
			}
			return respond(map[string]any{"descriptions": []any{map[string]string{"node_id": alias, "summary": detail, "description": detail}}})
		case strings.Contains(req.System, "SOURCE-RELATION GENERATE"):
			return respond(map[string]any{"relations": []any{}})
		}
	}
	return c.base.Complete(ctx, req)
}

// 原故障：合法零旧边没有 keptCross 键，完整关系结果仍使普通 Run 丢弃待提交节点。
// Seed through production ingest and actual managed writeback, then exercise Run and SQLite.
func TestV016C4EmptyRetainedRelationSet(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"selected", "empty", "missing", "unknown", "transport", "rejected", "legacy-human"} {
		t.Run(mode, func(t *testing.T) { v016EmptyRelationRun(t, mode) })
	}
}

func v016EmptyRelationRun(t *testing.T, mode string) {
	t.Helper()
	ctx := context.Background()
	root, dbPath := filepath.Join(t.TempDir(), "repo"), filepath.Join(t.TempDir(), "graph.db")
	const repoID, skill, path = "local:v016-empty-relations", "mini", "mini/references/pumps.md"
	const pumpID, valveID, loggerID = "11111111-1111-5111-8111-111111111111", "22222222-2222-5222-8222-222222222222", "33333333-3333-5333-8333-333333333333"
	nodes := []extract.Node{
		{ID: pumpID, Name: "Atlas pump", Summary: "Atlas pump maintenance rule", Description: "Atlas pump must not run during maintenance.", Confidence: .9, Members: []string{"old-atlas-pump"}, SourceSkills: []string{skill}},
		{ID: valveID, Name: "Safety valve", Summary: "Safety valve maintenance rule", Description: v016ValveDetail, Confidence: .9, Members: []string{"old-safety-valve"}, SourceSkills: []string{skill}},
		{ID: loggerID, Name: "Logger L", Summary: "Independent logger", Description: "Logger L records inventory only.", Confidence: .9, Members: []string{"old-logger"}, SourceSkills: []string{skill}},
	}
	graph := &extract.Result{Domains: []extract.Domain{{Slug: "operations", Name: "Operations", SourceSkills: []string{skill}, Subdomains: []extract.Subdomain{{Slug: "pumps", Name: "Pumps", Entities: nodes}}}}}
	mapping := map[string][]ingest.MemberSource{}
	doc := &dktypes.AnnotatedDocument{Skill: skill, FilePath: path}
	oldRaw := "# Original\n"
	for _, node := range nodes {
		mapping[node.Members[0]] = []ingest.MemberSource{{Skill: skill, FilePath: path}}
		doc.Items = append(doc.Items, dktypes.AnnotatedItem{ID: node.Members[0], Tag: dktypes.TagEntity, Name: node.Name, Content: node.Description, Detail: node.Description})
		oldRaw += node.Description + "\n"
	}
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := ingest.IngestDomains(ctx, db, ingest.IngestDomainsOptions{Result: graph, MemberSources: mapping})
	if err != nil || seed.SkillNodes != 1 || seed.ProvidesEdges != 1 || seed.ComposesEdges != 4 || seed.SemanticEdges != 0 || seed.NodeSourcesInserted != 3 {
		t.Fatalf("invalid production seed: %+v / %v", seed, err)
	}
	st := newStores(db)
	if mode == "legacy-human" {
		for _, edge := range []*dktypes.Edge{
			{SourceID: pumpID, TargetID: valveID, Kind: dktypes.KindReferences, Description: "old uncertain inference", Properties: `{"retain":"legacy"}`, Provenance: dktypes.ProvenanceLLMInferred, Confidence: 0},
			{SourceID: valveID, TargetID: pumpID, Kind: dktypes.KindReferences, Description: "human-only protected relation", Properties: `{"retain":"human"}`, Provenance: dktypes.ProvenanceHumanCurated, Confidence: .7},
		} {
			if err := st.edges.Insert(ctx, edge); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.identity.Set(ctx, repoID); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, skill), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, skill, "SKILL.md"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := applyIncrementalWriteback(ctx, st, newRunStateWithDocs(path), nil, nil, root); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	client := &v016EmptyRelationClient{mode: mode}
	base, err := NewIncrementalOrchestrator(Options{Client: client, RepositoryIdentity: repoID, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	if report, err := base.Run(ctx, root, dbPath); err != nil || report.Status != "complete" {
		t.Fatalf("baseline: %+v / %v", report, err)
	}
	before := snapshotMoveLedger(t, dbPath)
	generated, err := os.ReadFile(filepath.Join(root, path))
	if err != nil || !strings.Contains(string(generated), "kg:uuid=") {
		t.Fatalf("managed baseline absent: %s / %v", generated, err)
	}
	delta := v016ChangedSource
	if mode == "empty" {
		delta = v016IndependentPump
	}
	if err := os.WriteFile(filepath.Join(root, path), append(generated, []byte("\n\n"+delta+"\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	client.base.source = oldRaw + "\n" + delta
	originals := []extract.FrozenSource{extract.FreezeSource(skill, path, client.base.source)}
	suppliedDocs := []*dktypes.AnnotatedDocument{doc}
	if mode == "missing" {
		originals = nil
		suppliedDocs = nil
	}
	o, err := NewIncrementalOrchestrator(Options{Client: client, RepositoryIdentity: repoID, MaxRetries: -1, Sources: originals, SourceDocuments: suppliedDocs})
	if err != nil {
		t.Fatal(err)
	}
	report, runErr := o.Run(ctx, root, dbPath)
	after := snapshotMoveLedger(t, dbPath)
	success := mode == "selected" || mode == "empty" || mode == "legacy-human"
	if !success {
		if runErr == nil || report.Status == "complete" || len(report.UnresolvedDocs) == 0 || report.SourceAssociations.Committed != 0 {
			t.Fatalf("failed source certified: %+v / %v", report, runErr)
		}
		wantOutcome := annotation.DocumentUnavailable
		if mode == "rejected" {
			wantOutcome = annotation.DocumentPartial
		}
		wantReason := map[string]string{"missing": "frozen original sources unavailable", "unknown": "insufficient_evidence", "transport": "offline injected relation transport failure", "rejected": "rejected_semantic"}[mode]
		if report.Relations == nil || len(report.Relations.Scopes) != 1 || report.Relations.Scopes[0].Outcome != wantOutcome || !strings.Contains(report.Relations.Scopes[0].Reason, wantReason) || len(report.NodeOutcomes) != 2 {
			t.Fatalf("negative did not reach intended relation failure: %+v / %+v", report.Relations, report.NodeOutcomes)
		}
		if !reflect.DeepEqual(before.Nodes, after.Nodes) || !reflect.DeepEqual(before.Edges, after.Edges) || !reflect.DeepEqual(before.Sources, after.Sources) {
			t.Fatalf("failed relation source mutated graph/source ledger: nodes=%t edges=%t sources=%t", reflect.DeepEqual(before.Nodes, after.Nodes), reflect.DeepEqual(before.Edges, after.Edges), reflect.DeepEqual(before.Sources, after.Sources))
		}
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || !strings.Contains(string(body), delta) {
			t.Fatal("failed source lost unabsorbed original prose")
		}
		check, err := storage.OpenReadOnly(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		state, err := storage.NewFileStateRepo(check).Get(ctx, repoID, path)
		check.Close()
		if err != nil || state == nil || state.ContentHash != writeback.HashEditable(string(generated)) {
			t.Fatalf("failed source advanced durable baseline: %+v / %v", state, err)
		}
		retry, retryErr := o.Run(ctx, root, dbPath)
		if retryErr == nil || retry.Status == "complete" || retry.C4Segments != 1 || retry.SourceAssociations.Committed != 0 {
			t.Fatalf("failed C4 stopped being retryable: %+v / %v", retry, retryErr)
		}
		return
	}
	if runErr != nil || report.Status != "complete" || report.NodesMerged != 2 || report.SourceAssociations.Committed != 2 || len(after.Sources) != len(before.Sources)+2 {
		raw, _ := json.Marshal(report)
		t.Fatalf("legitimate empty retained set failed: %s / %v", raw, runErr)
	}
	if report.Relations == nil || len(report.Relations.Scopes) != 1 || report.Relations.Scopes[0].Outcome != annotation.DocumentComplete {
		t.Fatalf("source relation coverage incomplete: %+v", report.Relations)
	}
	want := 1
	if mode == "empty" {
		want = 0
	}
	if len(report.Relations.Selected) != want {
		t.Fatalf("selected=%d want=%d", len(report.Relations.Selected), want)
	}
	semantic := 0
	for _, edge := range after.Edges {
		if (edge.SourceID == pumpID || edge.SourceID == valveID || edge.SourceID == loggerID) && extract.IsValidRelationKind(string(edge.Kind)) {
			semantic++
			if edge.Kind == dktypes.KindDependsOn {
				var envelope reflowAssertionEnvelope
				if edge.SourceID != pumpID || edge.TargetID != valveID || json.Unmarshal([]byte(edge.Description), &envelope) != nil || len(envelope.Assertions) != 1 {
					t.Fatalf("persisted selected relation lost identity/evidence: %+v", edge)
				}
				assertion := envelope.Assertions[0]
				if assertion.Classification != "explicit" || len(assertion.Sources) != 1 || assertion.Sources[0].Path != path || len(assertion.Evidence) != 2 {
					t.Fatalf("persisted selected relation lost source credit: %+v", assertion)
				}
			}
		}
	}
	if mode == "legacy-human" {
		want += 2
	}
	if semantic != want {
		t.Fatalf("persisted semantic edges=%d want=%d", semantic, want)
	}
	for _, edge := range before.Edges {
		if edge.Kind != dktypes.KindReferences {
			continue
		}
		found := false
		for _, now := range after.Edges {
			found = found || reflect.DeepEqual(edge, now)
		}
		if !found {
			t.Fatalf("old protected/legacy edge changed: %+v", edge)
		}
	}
	for _, node := range before.Nodes {
		found := false
		for _, now := range after.Nodes {
			if node.ID == now.ID {
				found = true
				if node.ID == loggerID && !reflect.DeepEqual(node, now) {
					t.Fatal("unrelated node changed")
				}
			}
		}
		if !found {
			t.Fatalf("old UUID disappeared: %s", node.ID)
		}
	}
	for _, source := range before.Sources {
		found := false
		for _, now := range after.Sources {
			found = found || reflect.DeepEqual(source, now)
		}
		if !found {
			t.Fatalf("old member incidence changed: %+v", source)
		}
	}
	if report.DocsRewritten != 1 {
		t.Fatalf("C4 was not actually written back: %+v", report)
	}
	calls := client.calls.Load()
	unchanged, err := o.Run(ctx, root, dbPath)
	if err != nil || unchanged.Status != "complete" || unchanged.C4Segments != 0 || client.calls.Load() != calls {
		t.Fatalf("successful writeback not stable: %+v / %v", unchanged, err)
	}
}

// 空集合只来自完整读取；实际存储读取失败仍不能构造权威替换键。
// A real SQLite read error must leave both views absent and pending sources uncommitted.
func TestV016EmptyRelationReadFailureHasNoAuthority(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"nodes", "outgoing-edges"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "read-failure.db")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			st := newStores(db)
			key := SubdomainKey{"operations", "pumps"}
			if err := st.nodes.Insert(ctx, &dktypes.Node{ID: "pending", Label: dktypes.LabelEntity, Name: "Pump", Domain: key.Domain, Subdomain: key.Subdomain, Provenance: dktypes.ProvenanceExtraction}); err != nil {
				t.Fatal(err)
			}
			if mode == "nodes" {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.Conn().ExecContext(ctx, "DROP TABLE edges"); err != nil {
				t.Fatal(err)
			}
			rs := newRunState()
			rs.dirty.DirtyNodeFused("pending", key)
			ao := &alignOutcome{pending: map[string]*pendingNode{"pending": {UUID: "pending", Domain: key.Domain, Subdomain: key.Subdomain}}, sourceRows: []storage.NodeSource{{NodeUUID: "pending", MemberID: "new-member", FilePath: "pumps.md"}}}
			client := &v016EmptyRelationClient{}
			r := newReflower(client, 4096, st)
			final, kept := r.recomputeRelationsWithSources(ctx, rs.dirty, ao, nil, nil, nil, extract.RelationProcessOptions{MaxRetries: -1}, rs)
			if _, ok := final[key]; ok {
				t.Fatal("read failure created final authority")
			}
			if _, ok := kept[key]; ok {
				t.Fatal("read failure created retained authority")
			}
			outcomes := r.relationReflowOutcomes()
			wantReason := "read nodes"
			if mode == "outgoing-edges" {
				wantReason = "read edges"
			}
			if len(outcomes) != 1 || !strings.Contains(outcomes[0].Reason, wantReason) || client.calls.Load() != 0 {
				t.Fatalf("read failure was not localized before model work: %+v", outcomes)
			}
			if err := omitFailedRelationNodes(ao, &reflowResult{relations: final, keptCross: kept}, rs, map[string]string{"pumps.md": "unabsorbed source"}); err != nil {
				t.Fatal(err)
			}
			if len(ao.pending) != 0 || len(ao.sourceRows) != 0 || !rs.failedDocsSnapshot()["pumps.md"] {
				t.Fatal("read failure retained pending source commit")
			}
		})
	}
}
