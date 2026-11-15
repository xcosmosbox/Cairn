package incremental

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// 原故障：同一来源旧规则经主动/被动改写后不共享FTS词，缺席候选而被重复建点。
// Scripted placement checks candidate availability and UUID flow, not model efficacy.
func TestV016SourceBoundRecallRestoresCandidateAndPinsUUID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := v016C4RecallStores(t)
	n := &dktypes.Node{ID: "private-old-rule", Label: dktypes.LabelConcept, Name: "夜场入厅票签限制", Summary: "拒绝失效凭证", Description: "过期票签不用于入厅。", Domain: "operations", Subdomain: "editions", Provenance: dktypes.ProvenanceExtraction}
	if err := st.nodes.Insert(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: n.ID, MemberID: "unrelated-old-member", Skill: "hall", FilePath: "pass.md"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.fts.RebuildAll(ctx); err != nil {
		t.Fatal(err)
	}
	docs := []*dktypes.AnnotatedDocument{{Skill: "hall", FilePath: "pass.md", Items: []dktypes.AnnotatedItem{{ID: "hall-rephrased", Tag: dktypes.TagConcept, Name: "入馆凭据规则", Content: "失效证件仍遭拒收", Detail: "失效证件仍遭拒收", Confidence: 1}}}}
	units, _ := flattenUnits(docs)
	for _, candidate := range newAligner(nil, 65536, st, 15).recall(ctx, units, newRunState()) {
		if candidate.ID == n.ID {
			t.Fatal("fixture must reproduce lexical miss before the source union")
		}
	}
	calls := 0
	c := v013IncrementalAcceptanceClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		calls++
		if strings.Contains(req.User, n.ID) || strings.Contains(req.User, "private-human") || strings.Contains(req.User, "PRIVATE HUMAN") {
			t.Fatal("persistent identity or human content leaked")
		}
		m := regexp.MustCompile(`alias=([^\s]+) \[Concept\] 夜场入厅票签限制（`).FindStringSubmatch(req.User)
		if len(m) != 2 {
			t.Fatal("source-bound old rule missing from alignment candidates")
		}
		return &llm.CompleteResponse{Text: v013IncrementalWire(t, alignLLMOutput{Decisions: []alignDecision{{Unit: docs[0].Items[0].ID, Action: "merge_into", Target: m[1]}}})}, nil
	})
	a := newAligner(c, 65536, st, 15)
	a.maxRetries = -1
	out, err := a.align(ctx, docs, newRunState())
	if err != nil || calls != 1 || out.report.Outcome != "complete" || out.pending[n.ID] == nil || out.pending[n.ID].IsNew || len(out.pending) != 1 || len(out.sourceRows) != 1 {
		t.Fatalf("old UUID flow failed: calls=%d out=%+v err=%v", calls, out, err)
	}
	if out.sourceRows[0].MemberID != "hall-rephrased" || out.sourceRows[0].Skill != "hall" || out.sourceRows[0].FilePath != "pass.md" {
		t.Fatalf("candidate association invented new source evidence: %+v", out.sourceRows)
	}
}

// 普通Run必须保留坏来源原文/baseline，同时真正提交健康兄弟；不是只测计数适配器。
// Exercise ordinary entry, real source commits and file-state protection with scripted semantics.
func TestV016SourceBoundRecallRunPreservesBadBaselineAndCommitsSibling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root, dbPath := t.TempDir(), filepath.Join(t.TempDir(), "kg.db")
	skill := filepath.Base(root)
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	st := newStores(db)
	pump := &dktypes.Node{ID: "11111111-1111-5111-8111-111111111111", Label: dktypes.LabelEntity, Name: "Atlas pump", Summary: "Atlas pump always runs.", Description: "Atlas pump must not run during maintenance.", Domain: "operations", Subdomain: "pumps", Provenance: dktypes.ProvenanceExtraction, Confidence: .9}
	valve := &dktypes.Node{ID: "22222222-2222-5222-8222-222222222222", Label: dktypes.LabelEntity, Name: "Safety valve", Summary: "Safety valve maintenance rule", Description: v016ValveDetail, Domain: "operations", Subdomain: "pumps", Provenance: dktypes.ProvenanceExtraction, Confidence: .9}
	for _, n := range []*dktypes.Node{pump, valve, {ID: "domain", Label: dktypes.LabelDomain, Name: "Operations", Domain: "operations", Provenance: dktypes.ProvenanceExtraction}, {ID: "subdomain", Label: dktypes.LabelSubdomain, Name: "Pumps", Domain: "operations", Subdomain: "pumps", Provenance: dktypes.ProvenanceExtraction}} {
		if err := st.nodes.Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	var oldItems []dktypes.AnnotatedItem
	for i, n := range []*dktypes.Node{pump, valve} {
		id := fmt.Sprintf("old-%d", i)
		if err := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: n.ID, MemberID: id, Skill: skill, FilePath: "references/old.md"}}); err != nil {
			t.Fatal(err)
		}
		oldItems = append(oldItems, dktypes.AnnotatedItem{ID: id, Tag: dktypes.TagEntity, Name: n.Name, Content: n.Description, Detail: n.Description})
	}
	const identity = "local:source-recall-run"
	if err := st.identity.Set(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := st.version.Bump(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.fts.RebuildAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := applyIncrementalWriteback(ctx, st, newRunStateWithDocs("references/old.md"), nil, nil, root); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, file := range []string{"SKILL.md", "references/bad.md"} {
		if err := os.WriteFile(filepath.Join(root, file), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	baseClient := &v016ChangedClient{source: v016ChangedSource}
	calls := 0
	injected := false
	client := v013IncrementalAcceptanceClient(func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if strings.Contains(req.User, "Blocked source rule") {
			if strings.Contains(req.System, "知识结构化标注专家") {
				// Fault appears after detection/source-namespace preflight, at the read boundary under test.
				faultDB, err := storage.NewDB(storage.DBOptions{Path: dbPath})
				if err != nil {
					return nil, err
				}
				_, err = faultDB.Conn().Exec(`INSERT INTO node_sources(node_uuid,member_id,skill,file_path) VALUES ('missing-old-node','old-bad',?,'references/bad.md')`, skill)
				faultDB.Close()
				if err != nil {
					return nil, err
				}
				injected = true
				return v012Candidate("Blocked source rule"), nil
			}
			if strings.Contains(req.System, "语义核验") {
				return v012Assessment(false), nil
			}
		}
		if strings.Contains(req.User, "unit_id=") {
			calls++
			if strings.Contains(req.User, "Blocked source rule") {
				t.Fatal("bad dependent unit reached ordinary judge")
			}
		}
		return baseClient.Complete(ctx, req)
	})
	o, err := NewIncrementalOrchestrator(Options{Client: client, RepositoryIdentity: identity, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(ctx, root, dbPath); err != nil {
		t.Fatal(err)
	}
	db, err = storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	st = newStores(db)
	before, err := st.files.Get(ctx, identity, "references/bad.md")
	if err != nil || before == nil {
		t.Fatalf("bad baseline setup: %+v %v", before, err)
	}
	db.Close()
	const badBody = "Blocked source rule"
	if err := os.WriteFile(filepath.Join(root, "references/bad.md"), []byte(badBody), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "references/new.md"), []byte(v016ChangedSource), 0644); err != nil {
		t.Fatal(err)
	}
	rawOld := "# Pump and valve\n" + pump.Description + "\n" + valve.Description
	o, err = NewIncrementalOrchestrator(Options{Client: client, RepositoryIdentity: identity, MaxRetries: -1, Sources: []extract.FrozenSource{extract.FreezeSource(skill, "references/old.md", rawOld)}, SourceDocuments: []*dktypes.AnnotatedDocument{{Skill: skill, FilePath: "references/old.md", Items: oldItems}}})
	if err != nil {
		t.Fatal(err)
	}
	rpt, runErr := o.Run(ctx, root, dbPath)
	if !injected || rpt == nil || rpt.Status == "complete" || calls != 1 || rpt.NodesMerged != 2 || rpt.SourceAssociations.Committed != 2 {
		t.Fatalf("ordinary sibling commit failed: report=%+v err=%v calls=%d", rpt, runErr, calls)
	}
	db, err = storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st = newStores(db)
	after, err := st.files.Get(ctx, identity, "references/bad.md")
	if err != nil || after == nil || *after != *before {
		t.Fatalf("bad baseline advanced: before=%+v after=%+v err=%v", before, after, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "references/bad.md"))
	if err != nil || string(body) != badBody {
		t.Fatal("blocked source original was changed")
	}
	rows, err := st.sources.ListByFile(ctx, "references/new.md")
	if err != nil || len(rows) != 2 {
		t.Fatalf("healthy source not really committed: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.NodeUUID != pump.ID && row.NodeUUID != valve.ID {
			t.Fatal("healthy existing UUID lost")
		}
	}
	goodBaseline, err := st.files.Get(ctx, identity, "references/new.md")
	if err != nil || goodBaseline == nil {
		t.Fatal("healthy source baseline not advanced")
	}
}

func v016SourceRecallDB(t *testing.T) (*storage.DB, *stores) {
	t.Helper()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "kg.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := newStores(db)
	for _, n := range []*dktypes.Node{
		{ID: "domain", Label: dktypes.LabelDomain, Name: "Operations"},
		{ID: "subdomain", Label: dktypes.LabelSubdomain, Name: "Rules"},
		{ID: "private-source-rule", Label: dktypes.LabelConcept, Name: "storedrule", Summary: "existing rule"},
		{ID: "private-human-rule", Label: dktypes.LabelConcept, Name: "SECRET HUMAN NAME", Summary: "SECRET HUMAN SUMMARY", Description: "SECRET HUMAN DETAIL", Provenance: dktypes.ProvenanceHumanCurated},
	} {
		n.Domain, n.Subdomain = "operations", "rules"
		if n.Provenance == "" {
			n.Provenance = dktypes.ProvenanceExtraction
		}
		if err := st.nodes.Insert(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	return db, st
}

func v016SourceRecallDoc(skill, file, id string) *dktypes.AnnotatedDocument {
	return &dktypes.AnnotatedDocument{Skill: skill, FilePath: file, Items: []dktypes.AnnotatedItem{{ID: id, Tag: dktypes.TagConcept, Name: "incomingrule", Content: "incoming fact", Detail: "incoming fact", Confidence: 1}}}
}

// Legacy empty-member/unknown-span associations are readable candidates, never new evidence.
func TestV016SourceBoundRecallLegacyHumanAndKnownEmpty(t *testing.T) {
	t.Parallel()
	db, st := v016SourceRecallDB(t)
	if _, err := db.Conn().Exec(`INSERT INTO node_sources(node_uuid,member_id,skill,file_path) VALUES ('private-source-rule','','hall','legacy.md'),('private-human-rule','','hall','legacy.md'),('subdomain','','hall','legacy.md')`); err != nil {
		t.Fatal(err)
	}
	docs := []*dktypes.AnnotatedDocument{v016SourceRecallDoc("hall", "legacy.md", "legacy-new"), v016SourceRecallDoc("hall", "new.md", "new")}
	calls := 0
	c := v013IncrementalAcceptanceClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		calls++
		if !strings.Contains(req.User, "storedrule") || !strings.Contains(req.User, "protected human object") {
			t.Fatal("legacy candidates missing")
		}
		for _, secret := range []string{"private-source-rule", "private-human-rule", "SECRET HUMAN", "[Subdomain]"} {
			if strings.Contains(req.User, secret) {
				t.Fatalf("candidate identity/privacy/label guard: %s", secret)
			}
		}
		return &llm.CompleteResponse{Text: v013IncrementalWire(t, alignLLMOutput{Decisions: []alignDecision{{Unit: "legacy-new", Action: "attach", Target: "sd#1", NodeName: "separate rule", Tag: "concept"}, {Unit: "new", Action: "attach", Target: "sd#1", NodeName: "new rule", Tag: "concept"}}})}, nil
	})
	a := newAligner(c, 65536, st, 15)
	a.maxRetries = -1
	out, err := a.align(context.Background(), docs, newRunState())
	if err != nil || calls != 1 || out.report.Outcome != "complete" || len(out.sourceRows) != 2 {
		t.Fatalf("legacy/empty flow: %+v calls=%d err=%v", out, calls, err)
	}
	rows, err := st.sources.ListByFile(context.Background(), "legacy.md")
	if err != nil || len(rows) != 3 {
		t.Fatal("candidate read mutated old associations")
	}
	for _, row := range rows {
		if row.MemberID != "" || row.StartLine != 0 || row.EndLine != 0 {
			t.Fatalf("legacy evidence was fabricated: %+v", row)
		}
	}
}

// Read errors/invalid associations must not become a false empty pool or shrink coverage.
func TestV016SourceBoundRecallLocalFailureAndCompleteDenominator(t *testing.T) {
	for _, mode := range []string{"dangling", "empty-node", "read-error", "node-read-error", "missing-skill", "missing-path", "shared-second-source", "all-blocked", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			db, st := v016SourceRecallDB(t)
			bad := v016SourceRecallDoc("hall", "bad.md", "bad")
			docs := []*dktypes.AnnotatedDocument{bad, v016SourceRecallDoc("hall", "good.md", "good")}
			if mode == "missing-skill" {
				bad.Skill = ""
			}
			if mode == "missing-path" {
				bad.FilePath = ""
			}
			if mode == "shared-second-source" {
				docs = append([]*dktypes.AnnotatedDocument{v016SourceRecallDoc("hall", "first.md", "bad")}, docs...)
			}
			if mode == "all-blocked" || mode == "node-read-error" || mode == "cancelled" {
				docs = docs[:1]
			}
			if mode != "missing-skill" && mode != "missing-path" && mode != "cancelled" {
				node := "absent"
				if mode == "empty-node" {
					node = ""
				}
				if mode == "node-read-error" {
					node = "private-source-rule"
				}
				if _, err := db.Conn().Exec(`INSERT INTO node_sources(node_uuid,member_id,skill,file_path) VALUES (?, 'old', 'hall', 'bad.md')`, node); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "read-error" {
				if _, err := db.Conn().Exec(`ALTER TABLE node_sources RENAME TO source_backing; CREATE VIEW node_sources AS SELECT CASE WHEN file_path='bad.md' THEN json_extract('invalid','$') ELSE node_uuid END AS node_uuid,member_id,skill,file_path,start_line,end_line FROM source_backing`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "node-read-error" {
				broken, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "closed.db")})
				if err != nil {
					t.Fatal(err)
				}
				st.nodes = storage.NewNodeRepo(broken)
				broken.Close()
			}
			calls := 0
			c := v013IncrementalAcceptanceClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				calls++
				if strings.Contains(req.User, "unit_id=bad") || !strings.Contains(req.User, "unit_id=good") {
					t.Fatal("unavailable/shared unit entered judge or healthy sibling disappeared")
				}
				return &llm.CompleteResponse{Text: v013IncrementalWire(t, alignLLMOutput{Decisions: []alignDecision{{Unit: "good", Action: "attach", Target: "sd#1", NodeName: "healthy rule", Tag: "concept"}}})}, nil
			})
			a := newAligner(c, 65536, st, 15)
			a.maxRetries = 9 // A dependency failure must not consume even an available retry allowance.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			rs := newRunState()
			out, err := a.align(ctx, docs, rs)
			if mode == "cancelled" {
				if err != context.Canceled || calls != 0 || out.report.Outcome != "cancelled" {
					t.Fatalf("cancellation misclassified: %+v %v calls=%d", out, err, calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantCalls, wantCoverage, wantOutcome := 1, 1, "partial"
			if len(docs) == 1 {
				wantCalls, wantCoverage, wantOutcome = 0, 0, "unavailable"
			}
			if calls != wantCalls || len(out.report.Attempts) != wantCalls || out.report.Exhausted || out.report.Outcome != wantOutcome || out.report.InputIncidences != len(docs) || out.report.CoveredIncidences != wantCoverage || len(out.report.MissingIDs) != 1 || out.report.MissingIDs[0] != "bad" || len(out.unitsByID) != 1+wantCoverage {
				t.Fatalf("local coverage/attempts: mode=%s calls=%d out=%+v", mode, calls, out)
			}
			if len(rs.warningsSnapshot()) == 0 {
				t.Fatal("unavailable association silently dropped")
			}
			paths := []string{}
			for _, doc := range docs {
				paths = append(paths, doc.FilePath)
			}
			resolved, unresolved := resolveAndPruneC4Docs(v012Plan(paths...), &annotateOutput{Docs: docs}, out)
			if resolved[bad.FilePath] || len(unresolved) != len(docs)-wantCoverage || (wantCoverage == 1 && !resolved["good.md"]) {
				t.Fatalf("source baseline/pruning contract: resolved=%+v unresolved=%+v", resolved, unresolved)
			}
		})
	}
}

// The same source can contain distinct owners. Source membership must never decide identity.
func TestV016SourceBoundRecallAllContributionsAndNoForcedMerge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := v016C4RecallStores(t)
	for i := 0; i < 19; i++ {
		n := &dktypes.Node{ID: fmt.Sprintf("source-%02d", i), Label: dktypes.LabelConcept, Name: fmt.Sprintf("stored%02d", i), Domain: "operations", Subdomain: "editions", Provenance: dktypes.ProvenanceExtraction}
		if err := st.nodes.Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
		if err := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: n.ID, MemberID: "old", Skill: "hall", FilePath: "second.md"}}); err != nil {
			t.Fatal(err)
		}
	}
	// Foreign skill at the same path, same skill at a prefix path, and fake member prefixes confer no authority.
	for _, row := range []storage.NodeSource{{NodeUUID: "private-ticket", MemberID: "hall-fake-prefix", Skill: "other", FilePath: "second.md"}, {NodeUUID: "private-january", MemberID: "hall-fake-prefix", Skill: "hall", FilePath: "second.md.extra"}} {
		if err := st.sources.InsertBatch(ctx, []storage.NodeSource{row}); err != nil {
			t.Fatal(err)
		}
	}
	docs := []*dktypes.AnnotatedDocument{{Skill: "hall", FilePath: "first.md", Items: []dktypes.AnnotatedItem{{ID: "shared", Tag: dktypes.TagConcept, Name: "unmatchedincoming", Content: "incoming", Detail: "first", Confidence: 1}}}, {Skill: "hall", FilePath: "second.md", Items: []dktypes.AnnotatedItem{{ID: "shared", Tag: dktypes.TagConcept, Name: "unmatchedincoming", Content: "incoming", Detail: "second", Confidence: 1}}}}
	calls := 0
	c := v013IncrementalAcceptanceClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		calls++
		for i := 0; i < 19; i++ {
			if !strings.Contains(req.User, fmt.Sprintf("stored%02d", i)) {
				t.Fatalf("complete source roster was truncated: %d", i)
			}
		}
		if strings.Contains(req.User, "霜羽票签") {
			t.Fatal("foreign skill/path prefix became source authority")
		}
		if !strings.Contains(req.User, "first") || !strings.Contains(req.User, "second") {
			t.Fatal("shared member lost actual contributions")
		}
		return &llm.CompleteResponse{Text: v013IncrementalWire(t, alignLLMOutput{Decisions: []alignDecision{{Unit: "shared", Action: "attach", Target: "sd#1", NodeName: "different owner", Tag: "concept"}}})}, nil
	})
	a := newAligner(c, 65536, st, 15)
	a.maxRetries = -1
	out, err := a.align(ctx, docs, newRunState())
	if err != nil || calls != 1 || len(out.pending) != 1 || len(out.sourceRows) != 2 || out.report.InputIncidences != 2 || out.report.CoveredIncidences != 2 {
		t.Fatalf("source roster/coverage: calls=%d out=%+v err=%v", calls, out, err)
	}
	for _, p := range out.pending {
		if !p.IsNew || p.Name != "different owner" {
			t.Fatal("source association forced a merge")
		}
	}
}
