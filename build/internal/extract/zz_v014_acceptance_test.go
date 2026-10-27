package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

// These are source-derived deterministic controls, not empirical model-quality
// judgments. The frozen expectations stay in the test and never enter requests.
type v014AcceptanceClient struct {
	requests []llm.CompleteRequest
	reply    func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error)
}

func (*v014AcceptanceClient) ProviderName() string { return "v014-independent-offline-control" }
func (c *v014AcceptanceClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.requests = append(c.requests, req)
	return c.reply(ctx, req)
}
func v014AcceptanceReply(t *testing.T, v any) *llm.CompleteResponse {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}
}

type v014AcceptanceSource struct {
	ID      string `json:"source_id"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

func v014AcceptanceFixture(t *testing.T) (*Result, []*dktypes.AnnotatedDocument, []FrozenSource) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "e2e", "semantic-evidence", "testdata", "v011", "supplement.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Sources []v014AcceptanceSource `json:"sources"`
	}
	if err = json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	names := map[string][]string{"F01": {"Atlas pump"}, "F03": {"Atlas pump", "Valve V", "Report A"}, "F04": {"Atlas pump", "Valve V", "Controller C", "Controller D", "Report B"}, "F05": {"Atlas pump", "Controller C", "Logger L", "Sensor S"}}
	byName := map[string]*Node{}
	var docs []*dktypes.AnnotatedDocument
	var originals []FrozenSource
	for _, s := range f.Sources {
		ns, ok := names[s.ID]
		if !ok {
			continue
		}
		skill := "operations"
		if s.ID == "F05" {
			skill = "control"
		}
		d := &dktypes.AnnotatedDocument{Skill: skill, FilePath: s.Path}
		for i, name := range ns {
			member := fmt.Sprintf("%s-member-%d", s.ID, i+1)
			d.Items = append(d.Items, dktypes.AnnotatedItem{ID: member, Tag: dktypes.TagEntity, Name: name, Content: s.Content, Detail: "Exact original contribution; assessment must use frozen source bytes.", Confidence: 1})
			n := byName[name]
			if n == nil {
				n = &Node{ID: strings.ReplaceAll(name, " ", "-"), Label: "Entity", Name: name, Summary: name, Confidence: 1}
				byName[name] = n
			}
			n.Members = append(n.Members, member)
			n.Description += s.Content
		}
		docs = append(docs, d)
		originals = append(originals, FreezeSource(skill, s.Path, s.Content))
	}
	var ops, ctrl []Node
	for _, name := range []string{"Atlas pump", "Valve V", "Report A", "Report B", "Controller D", "Controller C", "Logger L", "Sensor S"} {
		n := byName[name]
		if n == nil {
			t.Fatal("frozen source denominator changed")
		}
		if name == "Controller C" || name == "Logger L" || name == "Sensor S" {
			ctrl = append(ctrl, *n)
		} else {
			ops = append(ops, *n)
		}
	}
	return &Result{Domains: []Domain{{Name: "Operations", Slug: "operations", Subdomains: []Subdomain{{Name: "Equipment", Slug: "equipment", Entities: ops}}}, {Name: "Control", Slug: "control", Subdomains: []Subdomain{{Name: "Signals", Slug: "signals", Entities: ctrl}}}}}, docs, originals
}

type v014GenerationInput struct {
	Focus string `json:"focus_source_id"`
	Nodes []struct {
		Alias string `json:"alias"`
		Name  string `json:"name"`
	} `json:"nodes"`
	Sources []struct {
		ID      string `json:"id"`
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"original_sources"`
	Relations []Relation `json:"candidate_relations"`
	Feedback  string     `json:"feedback"`
}

func v014Generation(t *testing.T, req llm.CompleteRequest) v014GenerationInput {
	t.Helper()
	var in v014GenerationInput
	if err := json.Unmarshal([]byte(req.User), &in); err != nil {
		t.Fatalf("not complete original JSON: %v", err)
	}
	return in
}
func v014ExpectedSourceRelations(t *testing.T, in v014GenerationInput) []Relation {
	t.Helper()
	aliases := map[string]string{}
	for _, n := range in.Nodes {
		aliases[n.Name] = n.Alias
	}
	mk := func(s, d, k, text string) Relation {
		if aliases[s] == "" || aliases[d] == "" {
			t.Fatalf("missing mapped source endpoint %s/%s", s, d)
		}
		return Relation{Source: aliases[s], Target: aliases[d], Kind: k, Description: text, Confidence: .9}
	}
	var path string
	for _, s := range in.Sources {
		if s.ID == in.Focus {
			path = s.Path
		}
	}
	switch {
	case strings.Contains(path, "report-a.md"):
		return []Relation{mk("Report A", "Report B", "contradicts", "On 2025-07-01 Report A estimates 3 percent and Report B estimates 5 percent calibration error, both with low confidence; preserve attributed disagreement.")}
	case strings.Contains(path, "report-b.md"):
		rows := []Relation{mk("Atlas pump", "Controller C", "depends_on", "The pump depends on Controller C from 2025-06-01.")}
		if aliases["Controller D"] != "" {
			rows = append(rows, mk("Atlas pump", "Controller D", "depends_on", "Before 2025-06-01 the pump depended on Controller D."))
		}
		return rows
	default:
		// Feed and shutdown-signal predicates have no matching frozen enum.
		// Their exact assertions remain in the already-verified node details.
		return []Relation{}
	}
}
func v014AcceptanceVerdict(t *testing.T, req llm.CompleteRequest) map[string]any {
	t.Helper()
	var n crossNeighborhood
	if err := json.Unmarshal([]byte(req.User), &n); err != nil {
		t.Fatal(err)
	}
	v := map[string]any{"disposition": "supported", "reason": "Explicit original assertion, correct direction and all qualifiers.", "relation_supported": true, "direction_supported": true, "explicit_source_assertion": true}
	var evidence []map[string]string
	for _, endpoint := range []struct {
		side string
		ep   CrossEndpoint
	}{{"source", n.Source}, {"target", n.Target}} {
		found := false
		for _, s := range n.Sources {
			for _, member := range endpoint.ep.Members {
				if !containsMember(s.Members, member) {
					continue
				}
				// Cite every contributing original for the endpoint; picking the first
				// source would falsely attribute an F04 assertion to F01/F03 only.
				evidence = append(evidence, map[string]string{"endpoint": endpoint.side, "member_id": member, "source_id": s.ID, "quote": s.Content})
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no real quote for %s %+v", endpoint.side, endpoint.ep)
		}
	}
	v["evidence"] = evidence
	if n.Relation.Kind == "contradicts" {
		v["contradiction"] = map[string]bool{"same_subject": true, "same_attribute": true, "same_time": true, "same_conditions": true, "incompatible": true}
	}
	return v
}
func v014AcceptanceGoodClient(t *testing.T) *v014AcceptanceClient {
	t.Helper()
	return &v014AcceptanceClient{reply: func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		switch {
		case strings.Contains(req.System, "SOURCE-RELATION GENERATE"):
			in := v014Generation(t, req)
			return v014AcceptanceReply(t, map[string]any{"relations": v014ExpectedSourceRelations(t, in)}), nil
		case strings.Contains(req.System, "SOURCE-RELATION COVERAGE"):
			return v014AcceptanceReply(t, map[string]any{"complete": true, "supported": true, "reason": "All source-derived eligible explicit assertions and qualifications retained.", "critique": ""}), nil
		default:
			return v014AcceptanceReply(t, v014AcceptanceVerdict(t, req)), nil
		}
	}}
}

// Triple-only dedup used to erase valid condition/time distinctions. Local
// edges also bypassed original-source verification altogether.
func TestV014AcceptanceOriginalQualifiersAndCrossEvidence(t *testing.T) {
	t.Parallel()
	res, docs, originals := v014AcceptanceFixture(t)
	client := v014AcceptanceGoodClient(t)
	report := NewExtractor(client, 65536).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
	if len(report.Scopes) != len(originals) {
		t.Fatalf("source denominator lost: %+v", report)
	}
	for _, s := range report.Scopes {
		if s.Outcome != annotation.DocumentComplete || s.CandidateID == "" || s.MaterialID == "" {
			t.Fatalf("unverified source marked success: %+v", s)
		}
	}
	var selected []Relation
	for _, d := range res.Domains {
		for _, sd := range d.Subdomains {
			selected = append(selected, sd.Relations...)
		}
	}
	if len(selected) != 3 {
		t.Fatalf("source duplicate/qualified variants lost: got %d %+v", len(selected), selected)
	}
	qualifiedC, qualifiedD := false, false
	for _, r := range selected {
		if r.Kind == "references" {
			t.Fatal("unsupported predicate relabeled as a reference edge")
		}
		if r.Source == "Atlas-pump" && r.Target == "Controller-C" && strings.Contains(r.Description, "from 2025-06-01") {
			qualifiedC = true
		}
		if r.Source == "Atlas-pump" && r.Target == "Controller-D" && strings.Contains(r.Description, "Before 2025-06-01") {
			qualifiedD = true
		}
	}
	if !qualifiedC || !qualifiedD {
		t.Fatalf("cross-domain time/direction lost: %+v", selected)
	}
	var details string
	for _, d := range res.Domains {
		for _, sd := range d.Subdomains {
			for _, n := range sd.Entities {
				details += n.Description
			}
		}
	}
	for _, want := range []string{"Valve V feeds the Atlas pump only while bypass mode is off.", "Valve V does not feed the pump while bypass mode is on.", "Controller C provides the Cedar pump with a shutdown signal."} {
		if !strings.Contains(details, want) {
			t.Fatalf("unsupported-kind assertion lost from verified graph detail: %s", want)
		}
	}
	explicit := 0
	for _, d := range report.Decisions {
		if d.Classification != "explicit" {
			continue
		}
		explicit++
		if d.CandidateID == "" || d.MaterialID == "" || len(d.Evidence) < 2 {
			t.Fatalf("explicit credit without evidence identity: %+v", d)
		}
		for _, q := range d.Evidence {
			matched := false
			for _, ref := range d.Sources {
				if ref.ID != q.SourceID {
					continue
				}
				for _, src := range originals {
					if src.Skill == ref.Skill && src.Path == ref.Path && src.SHA256 == ref.SHA256 && q.StartByte >= 0 && q.EndByte <= len(src.Content) && src.Content[q.StartByte:q.EndByte] == q.Quote {
						matched = true
					}
				}
			}
			if !matched {
				t.Fatalf("evidence not bound to frozen bytes: %+v", q)
			}
		}
	}
	if explicit < 3 {
		t.Fatalf("missing explicit source decisions: %+v", report.Decisions)
	}
	// Every prompt gets original material, never the fixture's expected inventory.
	for _, req := range client.requests {
		for _, forbidden := range []string{"cross-domain-explicit", "expected_outcome", "explicit-versus-inferred", "case_id"} {
			if strings.Contains(req.User, forbidden) {
				t.Fatal("expected inventory leaked into builder")
			}
		}
	}
	before, _ := json.Marshal(res)
	again := NewExtractor(v014AcceptanceGoodClient(t), 65536).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
	after, _ := json.Marshal(res)
	if !reflect.DeepEqual(before, after) || len(again.Scopes) != len(originals) {
		t.Fatal("unchanged helper repeat lost semantics or grew duplicate relations")
	}
}

func v014AcceptanceSingleSource(t *testing.T) (*Result, []*dktypes.AnnotatedDocument, []FrozenSource) {
	t.Helper()
	_, allDocs, allOriginals := v014AcceptanceFixture(t)
	var d *dktypes.AnnotatedDocument
	var src FrozenSource
	for _, doc := range allDocs {
		if strings.Contains(doc.FilePath, "report-b.md") {
			copyDoc := *doc
			copyDoc.Items = nil
			for _, it := range doc.Items {
				if it.Name == "Atlas pump" || it.Name == "Controller C" {
					copyDoc.Items = append(copyDoc.Items, it)
				}
			}
			d = &copyDoc
		}
	}
	for _, source := range allOriginals {
		if strings.Contains(source.Path, "report-b.md") {
			src = source
		}
	}
	if d == nil || src.Content == "" {
		t.Fatal("missing F04 original")
	}
	var nodes []Node
	for _, it := range d.Items {
		nodes = append(nodes, Node{ID: strings.ReplaceAll(it.Name, " ", "-"), Name: it.Name, Label: "Entity", Summary: it.Name, Description: src.Content, Members: []string{it.ID}, Confidence: 1})
	}
	return &Result{Domains: []Domain{{Name: "Operations", Slug: "operations", Subdomains: []Subdomain{{Name: "Equipment", Slug: "equipment", Entities: nodes}}}}}, []*dktypes.AnnotatedDocument{d}, []FrozenSource{src}
}

// A structurally legal edge, plausible quote or an always-positive semantic
// fixture must not bypass direction, explicitness or exact member/source binding.
func TestV014AcceptanceRejectsFalseExplicitCredit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"reversed-direction", func(v map[string]any) { v["direction_supported"] = false }},
		{"inferred-not-explicit", func(v map[string]any) { v["explicit_source_assertion"] = false }},
		{"missing-explicit-predicate", func(v map[string]any) { delete(v, "explicit_source_assertion") }},
		{"unknown-support", func(v map[string]any) {
			v["disposition"] = "insufficient_evidence"
			v["reason"] = "Whether Sensor S supports Controller C is unknown."
		}},
		{"invented-quote", func(v map[string]any) {
			v["evidence"].([]map[string]string)[0]["quote"] = "Controller C always powers the pump."
		}},
		{"wrong-member", func(v map[string]any) {
			v["evidence"].([]map[string]string)[0]["member_id"] = "member-from-another-source"
		}},
		{"wrong-source", func(v map[string]any) { v["evidence"].([]map[string]string)[0]["source_id"] = "s999" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, docs, originals := v014AcceptanceSingleSource(t)
			client := v014AcceptanceGoodClient(t)
			good := client.reply
			client.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if !strings.Contains(req.System, "SOURCE-RELATION ") {
					v := v014AcceptanceVerdict(t, req)
					tc.change(v)
					return v014AcceptanceReply(t, v), nil
				}
				return good(ctx, req)
			}
			report := NewExtractor(client, 65536).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
			if len(res.Domains[0].Subdomains[0].Relations) != 0 {
				t.Fatalf("false explicit relation selected: %+v", report)
			}
			if len(report.Scopes) != 1 || report.Scopes[0].Outcome == annotation.DocumentComplete || report.Scopes[0].Reason == "" {
				t.Fatalf("negative control certified success: %+v", report)
			}
			for _, d := range report.Decisions {
				if d.Classification == "explicit" && d.Disposition == "accepted" {
					t.Fatalf("false explicit decision: %+v", d)
				}
			}
		})
	}
}

// Unknown is neither a semantic rejection nor permission to regenerate. A real
// qualifier/direction defect instead requires original+whole-candidate revision.
func TestV014AcceptanceFailedStepRecoveryAndSharedAllowance(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unknown-then-pass", "semantic-revision", "mixed-budget-exhaustion"} {
		t.Run(mode, func(t *testing.T) {
			res, docs, originals := v014AcceptanceSingleSource(t)
			base := v014AcceptanceGoodClient(t)
			good := base.reply
			generations, assessments := 0, 0
			var firstAssessment string
			revised := false
			base.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
					generations++
					in := v014Generation(t, req)
					if generations > 1 {
						hasOriginal := false
						for _, source := range in.Sources {
							if source.Content == originals[0].Content {
								hasOriginal = true
							}
						}
						if !hasOriginal || !strings.Contains(req.User, "depends") || in.Feedback == "" {
							t.Fatal("revision omitted original, candidate or concrete critique")
						}
						revised = true
					}
					if mode == "semantic-revision" && generations == 1 {
						return v014AcceptanceReply(t, map[string]any{"relations": []Relation{}}), nil
					}
					if mode == "mixed-budget-exhaustion" {
						rows := v014ExpectedSourceRelations(t, in)
						rows[0].Source, rows[0].Target = rows[0].Target, rows[0].Source
						return v014AcceptanceReply(t, map[string]any{"relations": rows}), nil
					}
					return good(ctx, req)
				}
				if strings.Contains(req.System, "SOURCE-RELATION COVERAGE") {
					if mode == "semantic-revision" && generations == 1 {
						return v014AcceptanceReply(t, map[string]any{"complete": false, "supported": false, "reason": "Missing dated dependency.", "critique": "Retain the complete pump-to-C dependency from 2025-06-01; revise the whole candidate."}), nil
					}
					return good(ctx, req)
				}
				assessments++
				if mode != "semantic-revision" && assessments == 1 {
					firstAssessment = req.User
					v := v014AcceptanceVerdict(t, req)
					v["disposition"] = "insufficient_evidence"
					v["reason"] = "Verifier unknown; reassess unchanged candidate."
					return v014AcceptanceReply(t, v), nil
				}
				if mode == "unknown-then-pass" && assessments == 2 && req.User != firstAssessment {
					t.Fatal("unknown regenerated or changed the assessed candidate")
				}
				if mode == "mixed-budget-exhaustion" {
					v := v014AcceptanceVerdict(t, req)
					v["direction_supported"] = false
					v["reason"] = "The pump depends on Controller C, reverse is unsupported."
					return v014AcceptanceReply(t, v), nil
				}
				return good(ctx, req)
			}
			report := NewExtractor(base, 65536).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
			if len(report.Scopes) != 1 {
				t.Fatalf("lost failed source: %+v", report)
			}
			scope := report.Scopes[0]
			if mode == "mixed-budget-exhaustion" {
				if scope.Outcome == annotation.DocumentComplete || !scope.Exhausted || generations != 1 || assessments != 2 || len(res.Domains[0].Subdomains[0].Relations) != 0 {
					t.Fatalf("allowance reset across failed steps: gen=%d assess=%d %+v", generations, assessments, report)
				}
			} else {
				if scope.Outcome != annotation.DocumentComplete || scope.Exhausted || len(res.Domains[0].Subdomains[0].Relations) != 1 {
					t.Fatalf("recoverable case did not complete: %+v", report)
				}
				if mode == "unknown-then-pass" && generations != 1 {
					t.Fatal("unknown caused semantic generation")
				}
				if mode == "semantic-revision" && (!revised || generations != 2) {
					t.Fatal("semantic failure did not revise and recheck whole candidate")
				}
			}
			if len(scope.Attempts) == 0 || scope.CandidateID == "" || scope.MaterialID == "" {
				t.Fatal("actual attempts or candidate/material identity missing")
			}
		})
	}
}

// A supplied prior inference cannot gain explicit-source credit merely because
// it is retained/audited, and source absence is not a negative original assertion.
func TestV014AcceptancePriorInferenceAuditAndCallerCancellation(t *testing.T) {
	t.Parallel()
	res, docs, originals := v014AcceptanceFixture(t)
	// Both endpoints have real F05 member/source bindings; even a positive
	// audit cannot promote co-location or unknown support to an explicit edge.
	res.Domains[0].Subdomains[0].Relations = []Relation{{Source: "Atlas-pump", Target: "Logger-L", Kind: "depends_on", Description: "Co-location suggests a dependency (inferred).", Confidence: .4}, {Source: "Sensor-S", Target: "Controller-C", Kind: "depends_on", Description: "Whether support exists is unknown.", Confidence: .2}}
	report := NewExtractor(v014AcceptanceGoodClient(t), 65536).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
	selected := res.Domains[0].Subdomains[0].Relations
	var selectedCount int
	for _, d := range res.Domains {
		for _, sd := range d.Subdomains {
			for _, row := range sd.Relations {
				selectedCount++
				if row.Target == "Logger-L" || row.Source == "Sensor-S" {
					t.Fatalf("prior inference/unknown selected as explicit output: %+v", row)
				}
			}
		}
	}
	if selectedCount != 3 {
		t.Fatalf("prior audit damaged verified siblings: %+v", selected)
	}
	audit := 0
	for _, d := range report.Decisions {
		if d.Relation.Target == "Logger-L" || d.Relation.Source == "Sensor-S" {
			audit++
			if d.Classification == "explicit" {
				t.Fatalf("inference inherited explicit credit: %+v", d)
			}
		}
	}
	if audit < 2 {
		t.Fatal("supplied rejected/unknown candidates disappeared from audit")
	}
	res, docs, originals = v014AcceptanceSingleSource(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := v014AcceptanceGoodClient(t)
	report = NewExtractor(client, 65536).ProcessRelations(ctx, res, docs, originals, RelationProcessOptions{MaxRetries: 3})
	if len(client.requests) != 0 || len(report.Scopes) != 1 || report.Scopes[0].Outcome != annotation.DocumentCancelled || report.Scopes[0].Exhausted {
		t.Fatalf("cancellation retried or reported as exhausted: %+v", report)
	}
}

// Invalid generation aliases must enter finite source-aware repair before being
// discarded; repair may select existing valid nodes, never invent an endpoint.
func TestV014AcceptanceDanglingRepairAndRecoveryDefaults(t *testing.T) {
	t.Parallel()
	for _, budget := range []int{-1, 1, 0} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			res, docs, originals := v014AcceptanceSingleSource(t)
			client := v014AcceptanceGoodClient(t)
			good := client.reply
			generations := 0
			client.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
					generations++
					in := v014Generation(t, req)
					if generations == 1 {
						rows := v014ExpectedSourceRelations(t, in)
						rows[0].Target = "undeclared-endpoint"
						return v014AcceptanceReply(t, map[string]any{"relations": rows}), nil
					}
					if in.Feedback == "" || !strings.Contains(req.User, "undeclared-endpoint") {
						t.Fatal("dangling repair lost concrete failed candidate and critique")
					}
				}
				return good(ctx, req)
			}
			report := NewExtractor(client, 65536).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: budget})
			scope := report.Scopes[0]
			if budget < 0 {
				if generations != 1 || scope.Outcome == annotation.DocumentComplete || !scope.Exhausted || len(report.Selected) != 0 {
					t.Fatalf("disabled recovery accepted dangling relation: %+v", report)
				}
			} else if generations != 2 || scope.Outcome != annotation.DocumentComplete || scope.Exhausted || len(report.Selected) != 1 {
				t.Fatalf("finite dangling repair failed: %+v", report)
			}
			if len(res.Domains[0].Subdomains[0].Entities) != 2 {
				t.Fatal("relation repair fabricated new nodes")
			}
		})
	}
}

// Actual local production-client configuration failure is permanent, not an
// exhausted transport retry or evidence that the original assertion was false.
func TestV014AcceptancePermanentProductionClientConfiguration(t *testing.T) {
	t.Setenv("CAIRN_V014_ACCEPTANCE_KEY", "synthetic-local-test-key")
	res, docs, originals := v014AcceptanceSingleSource(t)
	client, err := llm.NewOpenAICompatClient(llm.Config{Endpoint: "ftp://example.invalid", APIKeyEnv: "CAIRN_V014_ACCEPTANCE_KEY", MaxRetries: -1, Timeout: "1s"})
	if err != nil {
		t.Fatal(err)
	}
	report := NewExtractor(client, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 3})
	if len(report.Scopes) != 1 {
		t.Fatal("lost permanent failed source")
	}
	scope := report.Scopes[0]
	if scope.Outcome != annotation.DocumentUnavailable || scope.Exhausted || len(scope.Attempts) != 1 || !strings.Contains(scope.Reason, "unsupported protocol scheme") || len(report.Selected) != 0 {
		t.Fatalf("permanent configuration retried or certified: %+v", scope)
	}
}

// Failure of one source cannot replay or erase an already successful independent
// source, or silently remove the failed source from the reported denominator.
func TestV014AcceptanceSuccessfulSourceSiblingSurvivesExhaustion(t *testing.T) {
	t.Parallel()
	res, docs, originals := v014AcceptanceFixture(t)
	client := v014AcceptanceGoodClient(t)
	good := client.reply
	generated := map[string]int{}
	client.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
			in := v014Generation(t, req)
			path := ""
			for _, s := range in.Sources {
				if s.ID == in.Focus {
					path = s.Path
				}
			}
			generated[path]++
			if strings.Contains(path, "report-a.md") {
				return &llm.CompleteResponse{Text: `{"relations":[`, FinishReason: "stop"}, nil
			}
		}
		return good(ctx, req)
	}
	report := NewExtractor(client, 65536).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
	if len(report.Scopes) != 4 {
		t.Fatal("source denominator shrank after failure")
	}
	complete, failed := 0, 0
	for _, s := range report.Scopes {
		if s.Outcome == annotation.DocumentComplete {
			complete++
		} else {
			failed++
			if !s.Exhausted || s.Reason == "" {
				t.Fatal("failed source lost exhaustion reason")
			}
		}
	}
	if complete != 3 || failed != 1 {
		t.Fatalf("failure escaped actual source scope: %+v", report.Scopes)
	}
	var qualifiedDependency bool
	for _, d := range res.Domains {
		for _, s := range d.Subdomains {
			for _, r := range s.Relations {
				if r.Source == "Atlas-pump" && r.Target == "Controller-C" && strings.Contains(r.Description, "2025-06-01") {
					qualifiedDependency = true
				}
			}
		}
	}
	if !qualifiedDependency {
		t.Fatal("successful independent dated dependency lost")
	}
	for path, n := range generated {
		want := 1
		if strings.Contains(path, "report-a.md") {
			want = 2
		}
		if n != want {
			t.Fatalf("sibling replayed or failed source over-retried: %s=%d want%d", path, n, want)
		}
	}
}

// Auditing an old candidate and generating that same candidate are one failed
// dependent scope, not permission to reset its recovery allowance or retry a
// permanent configuration error through another stage.
func TestV014AcceptanceOldCandidateSharesFailureBudget(t *testing.T) {
	res, docs, originals := v014AcceptanceSingleSource(t)
	res.Domains[0].Subdomains[0].Relations = []Relation{{Source: "Atlas-pump", Target: "Controller-C", Kind: "depends_on", Description: "The pump depends on Controller C from 2025-06-01.", Confidence: .9}}
	client := v014AcceptanceGoodClient(t)
	good := client.reply
	assessments := 0
	client.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if !strings.Contains(req.System, "SOURCE-RELATION ") {
			assessments++
			return v014AcceptanceReply(t, map[string]any{"disposition": "insufficient_evidence", "reason": "Same failed candidate remains unknown."}), nil
		}
		return good(ctx, req)
	}
	report := NewExtractor(client, 65536).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
	if assessments != 2 || len(report.Selected) != 0 {
		t.Fatalf("old audit and generation reset one candidate allowance: actual assessments=%d want2 report=%+v", assessments, report)
	}
}

func TestV014AcceptancePermanentOldAuditStopsDependentGeneration(t *testing.T) {
	t.Setenv("CAIRN_V014_ACCEPTANCE_KEY", "synthetic-local-test-key")
	res, docs, originals := v014AcceptanceSingleSource(t)
	res.Domains[0].Subdomains[0].Relations = []Relation{{Source: "Atlas-pump", Target: "Controller-C", Kind: "depends_on", Description: "The pump depends on Controller C from 2025-06-01.", Confidence: .9}}
	production, err := llm.NewOpenAICompatClient(llm.Config{Endpoint: "ftp://example.invalid", APIKeyEnv: "CAIRN_V014_ACCEPTANCE_KEY", MaxRetries: -1, Timeout: "1s"})
	if err != nil {
		t.Fatal(err)
	}
	client := &v014AcceptanceClient{reply: production.Complete}
	report := NewExtractor(client, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 3})
	if len(client.requests) != 1 || len(report.Selected) != 0 {
		t.Fatalf("permanent old audit retried through dependent generation: calls=%d report=%+v", len(client.requests), report)
	}
}
