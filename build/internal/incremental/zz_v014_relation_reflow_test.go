package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

type v014ReflowClient struct {
	mu                   sync.Mutex
	calls                []llm.CompleteRequest
	generate             []string
	verifierFailure      string
	protected            string
	malformedGenerations int
	extraRelation        bool
}

func (c *v014ReflowClient) ProviderName() string { return "offline-v014-reflow" }
func (c *v014ReflowClient) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, req)
	if c.protected != "" && strings.Contains(req.User, c.protected) {
		return nil, fmt.Errorf("human content leaked")
	}
	result := func(v any) (*llm.CompleteResponse, error) {
		data, _ := json.Marshal(v)
		return &llm.CompleteResponse{Text: string(data), FinishReason: "stop"}, nil
	}
	if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
		if c.malformedGenerations > 0 {
			c.malformedGenerations--
			return &llm.CompleteResponse{Text: "not JSON", FinishReason: "stop"}, nil
		}
		var input struct {
			FocusSourceID string `json:"focus_source_id"`
			Sources       []struct {
				ID      string   `json:"id"`
				Members []string `json:"members"`
			} `json:"original_sources"`
			Nodes []struct {
				Alias   string   `json:"alias"`
				Members []string `json:"members"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal([]byte(req.User), &input); err != nil {
			return nil, err
		}
		alias := map[string]string{}
		for _, n := range input.Nodes {
			for _, m := range n.Members {
				alias[m] = n.Alias
			}
		}
		rows := []map[string]any{}
		focusRelevant := false
		for _, source := range input.Sources {
			if source.ID == input.FocusSourceID {
				for _, member := range source.Members {
					if member == "m-a" || member == "m-b" {
						focusRelevant = true
					}
				}
			}
		}
		for _, description := range c.generate {
			if !focusRelevant {
				continue
			}
			rows = append(rows, map[string]any{"source": alias["m-a"], "target": alias["m-b"], "kind": "depends_on", "description": description, "confidence": .91})
		}
		if c.extraRelation {
			for _, source := range input.Sources {
				if source.ID == input.FocusSourceID {
					for _, member := range source.Members {
						if member == "m-g" || member == "m-h" {
							rows = append(rows, map[string]any{"source": alias["m-g"], "target": alias["m-h"], "kind": "depends_on", "description": "Independent G requires H.", "confidence": .95})
							break
						}
					}
				}
			}
		}
		return result(map[string]any{"relations": rows})
	}
	if strings.Contains(req.System, "SOURCE-RELATION COVERAGE") {
		return result(map[string]any{"complete": true, "supported": true, "reason": "all intended source relations retained", "critique": ""})
	}
	if c.verifierFailure == "transport" {
		return nil, fmt.Errorf("fixture verifier unavailable")
	}
	if c.verifierFailure == "unknown" {
		return result(map[string]any{"disposition": "insufficient_evidence", "reason": "fixture cannot establish relation"})
	}
	var input struct {
		Source struct {
			Members []string `json:"members"`
		} `json:"source"`
		Target struct {
			Members []string `json:"members"`
		} `json:"target"`
		Sources []struct {
			ID, Content string
			Members     []string
		} `json:"original_sources"`
	}
	if err := json.Unmarshal([]byte(req.User), &input); err != nil {
		return nil, err
	}
	evidence := []map[string]string{}
	for role, members := range map[string][]string{"source": input.Source.Members, "target": input.Target.Members} {
		found := false
		for _, source := range input.Sources {
			for _, member := range members {
				for _, owned := range source.Members {
					if member == owned && !found {
						evidence = append(evidence, map[string]string{"endpoint": role, "member_id": member, "source_id": source.ID, "quote": source.Content})
						found = true
					}
				}
			}
		}
	}
	return result(map[string]any{"disposition": "supported", "reason": "exact originals establish both endpoints, direction and full conditions", "relation_supported": true, "direction_supported": true, "explicit_source_assertion": true, "evidence": evidence})
}
func (c *v014ReflowClient) requests() []llm.CompleteRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.CompleteRequest(nil), c.calls...)
}

func v014ReflowFixture(t *testing.T) (*stores, *DirtySet, SubdomainKey, []*dktypes.AnnotatedDocument, []extract.FrozenSource) {
	t.Helper()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "relations.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := newStores(db)
	key := SubdomainKey{"engineering", "admission"}
	for _, row := range []struct{ id, member, name, body string }{{"a", "m-a", "Admission", "Admission requires quota before 2025, except emergency admission. After 2025 admission requires quota only during scheduled operation."}, {"b", "m-b", "Quota", "Quota is required by admission before 2025; emergency admission is exempt. After 2025 quota is required only during scheduled admission."}, {"c", "m-c", "Independent", "An independent retained reference."}} {
		if err := st.nodes.Insert(context.Background(), &dktypes.Node{ID: row.id, Label: dktypes.LabelConcept, Name: row.name, Summary: row.body, Description: row.body, Domain: key.Domain, Subdomain: key.Subdomain, Provenance: dktypes.ProvenanceLLMInferred, Confidence: .9}); err != nil {
			t.Fatal(err)
		}
		if err := st.sources.InsertBatch(context.Background(), []storage.NodeSource{{NodeUUID: row.id, MemberID: row.member, Skill: "fixture", FilePath: row.id + ".md"}}); err != nil {
			t.Fatal(err)
		}
	}
	docs := []*dktypes.AnnotatedDocument{}
	originals := []extract.FrozenSource{}
	for _, id := range []string{"a", "b", "c"} {
		node, _ := st.nodes.GetByID(context.Background(), id)
		docs = append(docs, &dktypes.AnnotatedDocument{Skill: "fixture", FilePath: id + ".md", Items: []dktypes.AnnotatedItem{{ID: "m-" + id, Tag: dktypes.TagConcept, Name: node.Name, Content: node.Summary, Detail: node.Description}}})
		originals = append(originals, extract.FreezeSource("fixture", id+".md", node.Description))
	}
	dirty := NewDirtySet()
	dirty.DirtyNodeFused("a", key)
	return st, dirty, key, docs, originals
}
func v014OldEdge(t *testing.T, st *stores, source, target string) *dktypes.Edge {
	t.Helper()
	edge := &dktypes.Edge{SourceID: source, TargetID: target, Kind: dktypes.KindDependsOn, Description: "old time-qualified inference", Properties: `{"curated_metadata":"keep"}`, Provenance: dktypes.ProvenanceLLMInferred, Confidence: .73, SourceRefs: "historical-source", Bidirectional: 1, Cardinality: "1:N", CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
	if err := st.edges.Insert(context.Background(), edge); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.edges.GetOutgoing(context.Background(), source, 0)
	for _, row := range rows {
		if row.TargetID == target && row.Kind == edge.Kind {
			return row
		}
	}
	t.Fatal("inserted edge missing")
	return nil
}

// 原失败模式：touched 的合法机器边因最终集合遗漏而被删除，甚至丢失所有 Edge 扩展列。
// Exercise both actual changed helper and I-8; absence/unknown/transport never authorizes deletion.
func TestV014RelationReflowRetainsTouchedOldEdge(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"", "unknown", "transport"} {
		t.Run(failure, func(t *testing.T) {
			st, dirty, key, docs, originals := v014ReflowFixture(t)
			old := v014OldEdge(t, st, "a", "b")
			client := &v014ReflowClient{verifierFailure: failure}
			r := newReflower(client, 4096, st)
			rs := newRunState()
			final, kept := r.recomputeRelationsWithSources(context.Background(), dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
			if len(final[key]) != 0 || len(kept[key]) != 1 || !reflect.DeepEqual(old, kept[key][0]) {
				t.Fatalf("old edge was cast/overwritten: final=%+v kept=%+v", final, kept)
			}
			if err := applyIncrementalIngest(context.Background(), st, dirty, nil, &reflowResult{relations: final, keptCross: kept}, rs); err != nil {
				t.Fatal(err)
			}
			rows, _ := st.edges.GetOutgoing(context.Background(), "a", 0)
			if len(rows) != 1 || !reflect.DeepEqual(old, rows[0]) {
				t.Fatalf("persisted old fields changed: before=%+v after=%+v", old, rows)
			}
			report := r.relationProcessReport()
			if report == nil || len(report.Decisions) == 0 || len(client.requests()) == 0 {
				t.Fatal("source-aware verification path was not exercised")
			}
			for _, d := range report.Decisions {
				if d.Classification == "explicit" {
					t.Fatal("old inference gained explicit credit")
				}
			}
		})
	}
}

// 新关系的限定和来源各自保留；同一三元组不能被旧的 triple-only 去重静默裁掉。
// Full and changed helpers consume the same originals, then stable facets survive actual I-8/repeat.
func TestV014RelationReflowSourceParityAndVariants(t *testing.T) {
	t.Parallel()
	st, dirty, key, docs, originals := v014ReflowFixture(t)
	old := v014OldEdge(t, st, "a", "b")
	descriptions := []string{"Admission requires quota before 2025 except emergencies.", "After 2025 admission requires quota only during scheduled operation."}
	direct := &extract.Result{Domains: []extract.Domain{{Name: key.Domain, Slug: key.Domain, Subdomains: []extract.Subdomain{{Name: key.Subdomain, Slug: key.Subdomain, Concepts: []extract.Node{{ID: "full-a", Name: "Admission", Members: []string{"m-a"}}, {ID: "full-b", Name: "Quota", Members: []string{"m-b"}}, {ID: "full-c", Name: "Independent", Members: []string{"m-c"}}}}}}}}
	full := extract.NewExtractor(&v014ReflowClient{generate: descriptions}, 4096).ProcessRelations(context.Background(), direct, docs, originals, extract.RelationProcessOptions{MaxRetries: -1})
	r := newReflower(&v014ReflowClient{generate: descriptions}, 4096, st)
	rs := newRunState()
	final, kept := r.recomputeRelationsWithSources(context.Background(), dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
	if len(full.Selected) == 0 || len(final[key]) != 1 {
		t.Fatalf("shared selection absent: full=%+v changed=%+v warnings=%v", full, final, rs.warningsSnapshot())
	}
	var envelope reflowAssertionEnvelope
	if err := json.Unmarshal([]byte(final[key][0].Description), &envelope); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	legacy := false
	for _, a := range envelope.Assertions {
		if a.Classification == "explicit" {
			got[a.Description] = true
			if len(a.Sources) < 2 || len(a.Evidence) < 2 {
				t.Fatal("facet lost exact source binding")
			}
		}
		if a.Classification == "legacy_unverified" && a.Description == old.Description {
			legacy = true
		}
	}
	for _, description := range descriptions {
		if !got[description] {
			t.Fatalf("qualifier lost: %s", description)
		}
	}
	if !legacy {
		t.Fatal("old unverified meaning disappeared or inherited explicit credit")
	}
	for _, selected := range full.Selected {
		if !got[selected.Relation.Description] {
			t.Fatal("full/changed helper mismatch")
		}
	}
	if err := applyIncrementalIngest(context.Background(), st, dirty, nil, &reflowResult{relations: final, keptCross: kept}, rs); err != nil {
		t.Fatal(err)
	}
	persisted, _ := st.edges.GetOutgoing(context.Background(), "a", 0)
	if len(persisted) != 1 {
		t.Fatal("variant coalescing did not fit existing triple identity")
	}
	want := *old
	want.Description = final[key][0].Description
	if !reflect.DeepEqual(&want, persisted[0]) {
		t.Fatalf("metadata changed: %+v", persisted[0])
	}
	again, _ := r.recomputeRelationsWithSources(context.Background(), dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, newRunState())
	if len(again[key]) != 1 || again[key][0].Description != final[key][0].Description {
		t.Fatal("unchanged repeat grew relation facets")
	}
}

// 原失败模式：跨域边在 target 新来源变化时走 direct-retain shortcut，不核验两端原文。
func TestV014RelationReflowIncomingCrossRevalidatesBothOriginals(t *testing.T) {
	t.Parallel()
	st, _, key, docs, originals := v014ReflowFixture(t)
	ctx := context.Background()
	node, _ := st.nodes.GetByID(ctx, "b")
	node.Domain = "platform"
	node.Subdomain = "quota"
	if err := st.nodes.Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	old := v014OldEdge(t, st, "a", "b")
	independent := v014OldEdge(t, st, "a", "c")
	dirty := NewDirtySet()
	dirty.DirtyNodeFused("b", SubdomainKey{node.Domain, node.Subdomain})
	client := &v014ReflowClient{}
	r := newReflower(client, 4096, st)
	rs := newRunState()
	final, kept := r.recomputeRelationsWithSources(ctx, dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
	if !dirty.Subdomains[key] || len(kept[key]) != 2 {
		t.Fatalf("incoming owner or independent edge omitted: dirty=%+v kept=%+v", dirty, kept)
	}
	found := false
	for _, req := range client.requests() {
		if strings.Contains(req.System, "跨子域关系") && strings.Contains(req.User, originals[0].Content) && strings.Contains(req.User, originals[1].Content) {
			found = true
		}
	}
	if !found {
		t.Fatal("changed cross relation did not revalidate both frozen endpoint originals")
	}
	if err := applyIncrementalIngest(ctx, st, dirty, nil, &reflowResult{relations: final, keptCross: kept}, rs); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.edges.GetOutgoing(ctx, "a", 0)
	if len(rows) != 2 || !reflect.DeepEqual(old, rows[0]) || !reflect.DeepEqual(independent, rows[1]) {
		t.Fatal("cross or independent old edge fields changed")
	}
}

// 缺失冻结原文须显式 unavailable；人工名称/正文/边描述在带原文入口也不能泄入 prompt。
func TestV014RelationReflowMissingOriginalsAndHumanIsolation(t *testing.T) {
	t.Parallel()
	st, dirty, key, docs, originals := v014ReflowFixture(t)
	ctx := context.Background()
	old := v014OldEdge(t, st, "a", "b")
	client := &v014ReflowClient{}
	r := newReflower(client, 4096, st)
	final, kept := r.recomputeRelations(ctx, dirty, nil, nil, newRunState())
	if len(client.requests()) != 0 || len(final[key]) != 0 || !reflect.DeepEqual(kept[key][0], old) {
		t.Fatal("legacy missing-original path trusted derived content")
	}
	report := r.relationProcessReport()
	if report == nil || len(report.Scopes) == 0 || report.Scopes[0].Outcome != annotation.DocumentUnavailable {
		t.Fatal("missing originals reported phantom complete")
	}
	secret := "HUMAN PRIVATE CURATED SENTINEL"
	a, _ := st.nodes.GetByID(ctx, "a")
	a.Name = secret
	a.Summary = secret
	a.Description = secret
	a.Provenance = dktypes.ProvenanceHumanCurated
	if err := st.nodes.Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	human := &dktypes.Edge{SourceID: "b", TargetID: "a", Kind: dktypes.KindReferences, Description: secret, Properties: secret, Provenance: dktypes.ProvenanceHumanCurated, Confidence: 1}
	if err := st.edges.Insert(ctx, human); err != nil {
		t.Fatal(err)
	}
	client = &v014ReflowClient{protected: secret}
	r = newReflower(client, 4096, st)
	_, kept = r.recomputeRelationsWithSources(ctx, dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, newRunState())
	if len(client.requests()) == 0 || len(kept[key]) != 2 {
		t.Fatal("human isolation fixture did not exercise source-aware path")
	}
	for _, req := range client.requests() {
		if strings.Contains(req.User, secret) {
			t.Fatal("human content leaked into relation prompt")
		}
	}
}

// 原失败模式：只能沿旧边找到跨域端点；新原文明示的首条跨域关系根本没有候选目录。
func TestV014RelationReflowDiscoversNewCrossWithoutOldEdge(t *testing.T) {
	t.Parallel()
	st, _, key, docs, originals := v014ReflowFixture(t)
	ctx := context.Background()
	node, _ := st.nodes.GetByID(ctx, "b")
	node.Domain = "platform"
	node.Subdomain = "quota"
	if err := st.nodes.Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	dirty := NewDirtySet()
	dirty.DirtyNodeFused("b", SubdomainKey{node.Domain, node.Subdomain})
	r := newReflower(&v014ReflowClient{generate: []string{"Admission requires quota before 2025 except emergencies."}}, 4096, st)
	rs := newRunState()
	final, _ := r.recomputeRelationsWithSources(ctx, dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
	if len(final[key]) != 1 || final[key][0].Source != "a" || final[key][0].Target != "b" || !dirty.Subdomains[key] {
		t.Fatalf("new cross source not discovered/applied: final=%+v dirty=%+v warnings=%v", final, dirty, rs.warningsSnapshot())
	}
}

// I-8 旧实现按 triple 折叠机器重复行；没有原文输出也会丢掉独立限定/元数据。
// Withhold only the affected replacement scope and prove exact old rows survive real ingest.
func TestV014RelationReflowDuplicateRowsDeferOnlyTheirScope(t *testing.T) {
	t.Parallel()
	st, dirty, key, docs, originals := v014ReflowFixture(t)
	ctx := context.Background()
	v014OldEdge(t, st, "a", "b")
	if err := st.edges.Insert(ctx, &dktypes.Edge{SourceID: "a", TargetID: "b", Kind: dktypes.KindDependsOn, Description: "different legacy condition", Properties: `{"distinct":true}`, SourceRefs: "other-source", Confidence: .62, Provenance: dktypes.ProvenanceLLMInferred, Cardinality: "M:N"}); err != nil {
		t.Fatal(err)
	}
	other := SubdomainKey{"isolated", "good"}
	for _, id := range []string{"g", "h"} {
		if err := st.nodes.Insert(ctx, &dktypes.Node{ID: id, Label: dktypes.LabelConcept, Name: id, Summary: "Independent G requires H.", Domain: other.Domain, Subdomain: other.Subdomain, Provenance: dktypes.ProvenanceLLMInferred, Confidence: 1}); err != nil {
			t.Fatal(err)
		}
		if err := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: id, MemberID: "m-" + id, Skill: "fixture", FilePath: id + ".md"}}); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, &dktypes.AnnotatedDocument{Skill: "fixture", FilePath: id + ".md", Items: []dktypes.AnnotatedItem{{ID: "m-" + id, Name: id, Content: "Independent G requires H.", Detail: "Independent G requires H."}}})
		originals = append(originals, extract.FreezeSource("fixture", id+".md", "Independent G requires H."))
	}
	dirty.DirtyNodeFused("g", other)
	before, _ := st.edges.GetOutgoing(ctx, "a", 0)
	r := newReflower(&v014ReflowClient{extraRelation: true, generate: []string{"Admission requires quota before 2025 except emergencies."}}, 4096, st)
	rs := newRunState()
	final, kept := r.recomputeRelationsWithSources(ctx, dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
	if _, ok := final[key]; ok {
		t.Fatal("lossy duplicate-row replacement was published")
	}
	if rows, ok := final[other]; !ok || len(rows) != 1 {
		t.Fatal("independent successful scope was withheld")
	}
	if len(r.relationProcessReport().Selected) == 0 {
		t.Fatal("verified selected evidence disappeared while application deferred")
	}
	deferred, unapplied := false, false
	for _, out := range r.relationReflowOutcomes() {
		if out.Scope == key && out.State == "application_deferred" && out.Outcome == annotation.DocumentUnavailable {
			deferred = true
		}
		if out.Scope == key && out.State == "verified_new_unapplied" {
			unapplied = true
		}
	}
	if !deferred || !unapplied {
		t.Fatal("deferred application falsely reported applied success")
	}
	if err := applyIncrementalIngest(ctx, st, dirty, nil, &reflowResult{relations: final, keptCross: kept}, rs); err != nil {
		t.Fatal(err)
	}
	after, _ := st.edges.GetOutgoing(ctx, "a", 0)
	independent, _ := st.edges.GetOutgoing(ctx, "g", 0)
	if len(independent) != 1 || independent[0].TargetID != "h" {
		t.Fatal("independent verified scope was not persisted")
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("legacy duplicate full rows changed: before=%+v after=%+v", before, after)
	}
}

// changed 入口同样使用共享有限预算：可修复生成失败后完成，不重复成功来源。
func TestV014RelationReflowSharedFiniteRepair(t *testing.T) {
	t.Parallel()
	st, dirty, key, docs, originals := v014ReflowFixture(t)
	client := &v014ReflowClient{malformedGenerations: 1, generate: []string{"Admission requires quota before 2025 except emergencies."}}
	r := newReflower(client, 4096, st)
	final, _ := r.recomputeRelationsWithSources(context.Background(), dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: 1}, newRunState())
	if len(final[key]) != 1 {
		t.Fatal("changed helper failed to reuse finite source repair")
	}
	recovered := false
	for _, scope := range r.relationProcessReport().Scopes {
		if scope.Outcome != annotation.DocumentComplete {
			t.Fatalf("normal repaired source unfinished: %+v", scope)
		}
		if len(scope.Attempts) > 3 {
			recovered = true
		}
	}
	if !recovered {
		t.Fatal("repair attempt was not exposed")
	}
}

// 原文能支持新机器断言也不授权覆盖同身份人工边；核验成功与实际未应用分别报告。
func TestV014RelationReflowHumanCollisionReportsUnapplied(t *testing.T) {
	t.Parallel()
	st, dirty, key, docs, originals := v014ReflowFixture(t)
	ctx := context.Background()
	secret := "PRIVATE HUMAN RELATION CONDITION"
	if err := st.edges.Insert(ctx, &dktypes.Edge{SourceID: "a", TargetID: "b", Kind: dktypes.KindDependsOn, Description: secret, Properties: `{"owner":"human"}`, Provenance: dktypes.ProvenanceHumanCurated, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.edges.GetOutgoing(ctx, "a", 0)
	r := newReflower(&v014ReflowClient{protected: secret, generate: []string{"Admission requires quota before 2025 except emergencies."}}, 4096, st)
	rs := newRunState()
	final, kept := r.recomputeRelationsWithSources(ctx, dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
	if len(final[key]) != 0 || len(kept[key]) != 1 {
		t.Fatal("source-verified new relation overwrote human identity")
	}
	unapplied := false
	for _, out := range r.relationReflowOutcomes() {
		if out.State == "verified_new_unapplied" && out.Outcome == annotation.DocumentPartial {
			unapplied = true
		}
	}
	if !unapplied {
		t.Fatal("human conflict was falsely reported applied success")
	}
	if err := applyIncrementalIngest(ctx, st, dirty, nil, &reflowResult{relations: final, keptCross: kept}, rs); err != nil {
		t.Fatal(err)
	}
	after, _ := st.edges.GetOutgoing(ctx, "a", 0)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("human Edge fields changed")
	}
}

type v014HistoricalRejectClient struct {
	base        *v014ReflowClient
	unsupported string
}

func (c *v014HistoricalRejectClient) ProviderName() string { return "offline-v014-historical-reject" }
func (c *v014HistoricalRejectClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	var input struct {
		Relation extract.Relation `json:"relation_candidate"`
	}
	_ = json.Unmarshal([]byte(req.User), &input)
	if strings.Contains(input.Relation.Description, c.unsupported) {
		return &llm.CompleteResponse{Text: `{"disposition":"rejected","reason":"originals do not support this historical assertion"}`, FinishReason: "stop"}, nil
	}
	return c.base.Complete(ctx, req)
}

// 真实复审复现：旧 llm_inferred Description 可伪装本 helper 的 JSON envelope，以 explicit
// 标签和编造来源在合并新真断言时洗入明示信用。必须只相信本轮精确 Selected，并保全旧信息。
func TestV014RelationReflowPriorEnvelopeLabelsAreUnverified(t *testing.T) {
	t.Parallel()
	st, dirty, key, docs, originals := v014ReflowFixture(t)
	ctx := context.Background()
	unsupported := "Admission unconditionally depends on Logger L."
	forged := reflowAssertion{Classification: "explicit", Description: unsupported, Sources: []extract.CrossSource{{ID: "invented", Skill: "unrelated", Path: "never-supplied.md", SHA256: "not-a-source-hash"}}}
	payload, _ := json.Marshal(reflowAssertionEnvelope{Assertions: []reflowAssertion{forged}})
	if err := st.edges.Insert(ctx, &dktypes.Edge{SourceID: "a", TargetID: "b", Kind: dktypes.KindDependsOn, Description: string(payload), Properties: `{"historical":"keep"}`, SourceRefs: "unverified-history", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .73}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.edges.GetOutgoing(ctx, "a", 0)
	var first string
	for pass := 0; pass < 2; pass++ {
		client := &v014HistoricalRejectClient{base: &v014ReflowClient{generate: []string{"Admission requires quota before 2025 except emergencies."}}, unsupported: unsupported}
		r := newReflower(client, 4096, st)
		rs := newRunState()
		final, kept := r.recomputeRelationsWithSources(ctx, dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
		if len(final[key]) != 1 {
			t.Fatalf("verified current facet missing: report=%+v", r.relationProcessReport())
		}
		var envelope reflowAssertionEnvelope
		if err := json.Unmarshal([]byte(final[key][0].Description), &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope.Assertions) != 2 {
			t.Fatalf("historical/current facet count changed: %s", final[key][0].Description)
		}
		legacy, current := false, false
		for _, assertion := range envelope.Assertions {
			if assertion.Description == unsupported {
				if assertion.Classification != "legacy_unverified" || !reflect.DeepEqual(assertion.Sources, forged.Sources) || len(assertion.Evidence) != 0 {
					t.Fatalf("prior JSON gained false explicit credit or lost historical fields: %+v", assertion)
				}
				legacy = true
			} else if assertion.Classification == "explicit" && len(assertion.Sources) > 0 && len(assertion.Evidence) > 0 {
				current = true
			}
		}
		if !legacy || !current {
			t.Fatal("history or currently verified facet lost")
		}
		if pass == 0 {
			first = final[key][0].Description
		} else if first != final[key][0].Description {
			t.Fatal("downgraded history/reverified current facet grew on repeat")
		}
		if err := applyIncrementalIngest(ctx, st, dirty, nil, &reflowResult{relations: final, keptCross: kept}, rs); err != nil {
			t.Fatal(err)
		}
		after, _ := st.edges.GetOutgoing(ctx, "a", 0)
		want := *before[0]
		want.Description = first
		if len(after) != 1 || !reflect.DeepEqual(&want, after[0]) {
			t.Fatal("old Edge metadata changed")
		}
	}
}

// 旧正文只是形似 envelope 且带额外业务字段时，typed Unmarshal 不得吞掉未识别的旧信息。
func TestV014RelationReflowEnvelopeLikeLegacyTextPreservedWhole(t *testing.T) {
	t.Parallel()
	st, dirty, key, docs, originals := v014ReflowFixture(t)
	ctx := context.Background()
	legacy := `{"cairn_relation_assertions_v1":[{"classification":"explicit","description":"Historical uncertain assertion","condition":"critical extra field"}],"source_note":"retain the whole historical object"}`
	if err := st.edges.Insert(ctx, &dktypes.Edge{SourceID: "a", TargetID: "b", Kind: dktypes.KindDependsOn, Description: legacy, Provenance: dktypes.ProvenanceLLMInferred, Confidence: .73}); err != nil {
		t.Fatal(err)
	}
	var first string
	for pass := 0; pass < 2; pass++ {
		r := newReflower(&v014ReflowClient{generate: []string{"Admission requires quota before 2025 except emergencies."}}, 4096, st)
		rs := newRunState()
		final, kept := r.recomputeRelationsWithSources(ctx, dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
		if len(final[key]) != 1 {
			t.Fatal("verified new facet missing")
		}
		var envelope reflowAssertionEnvelope
		if err := json.Unmarshal([]byte(final[key][0].Description), &envelope); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, assertion := range envelope.Assertions {
			if assertion.Classification == "legacy_unverified" && assertion.Description == legacy {
				found = true
			}
		}
		if !found {
			t.Fatal("unknown historical fields or raw meaning discarded")
		}
		if pass == 0 {
			first = final[key][0].Description
		} else if first != final[key][0].Description {
			t.Fatal("whole historical fallback nested or grew on repeat")
		}
		if err := applyIncrementalIngest(ctx, st, dirty, nil, &reflowResult{relations: final, keptCross: kept}, rs); err != nil {
			t.Fatal(err)
		}
	}
}
