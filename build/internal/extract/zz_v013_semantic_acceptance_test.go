package extract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

const v013ItemsMarker = "## 标注单元（数组下标从0开始） / annotated items\n"

func v013SemanticCandidate(t *testing.T, req llm.CompleteRequest) dktypes.AnnotatedItem {
	t.Helper()
	parts := strings.Split(req.User, v013ItemsMarker)
	var items []dktypes.AnnotatedItem
	if len(parts) != 2 || json.Unmarshal([]byte(parts[1]), &items) != nil || len(items) != 1 {
		t.Fatal("semantic gate did not receive one complete node candidate")
	}
	return items[0]
}

func v013Generation(t *testing.T, req llm.CompleteRequest, summary, detail string) *llm.CompleteResponse {
	t.Helper()
	match := regexp.MustCompile(`Node alias=([^\s]+)`).FindStringSubmatch(req.User)
	if len(match) != 2 {
		t.Fatal("generation did not use a call-scoped node alias")
	}
	return &llm.CompleteResponse{Text: v013Wire(t, map[string]any{"descriptions": []map[string]string{{"node_id": match[1], "summary": summary, "description": detail}}}), FinishReason: "stop"}
}

// This is a deterministic semantic control, not a model-quality measurement.
// Exact candidate checks reject two different concrete qualifier losses; an
// always-supported routing fixture could not establish the revision behavior.
func TestV013AcceptanceQualifiedFusionAndWholeCandidateRevision(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "e2e", "semantic-evidence", "testdata", "v011", "supplement.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Sources []struct {
			ID      string `json:"source_id"`
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"sources"`
	}
	if json.Unmarshal(b, &fixture) != nil {
		t.Fatal("invalid frozen supplement")
	}
	wanted := map[string]bool{"F01": true, "F03": true, "F04": true}
	var docs []*dktypes.AnnotatedDocument
	var originals []string
	for _, s := range fixture.Sources {
		if wanted[s.ID] {
			originals = append(originals, s.Content)
			docs = append(docs, &dktypes.AnnotatedDocument{Skill: "operations", FilePath: s.Path, Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagEntity, Name: "Atlas pump", Content: s.Content, Detail: "Source contribution for the Atlas pump.", Confidence: 1}}})
		}
	}
	if len(docs) != 3 {
		t.Fatal("qualified source denominator changed")
	}
	// Joint Name+Content+Detail coverage is required: the full source is in
	// Content while Detail alone cannot preserve pressure, chronology or disagreement.
	docs = append(docs, &dktypes.AnnotatedDocument{Skill: "operations", FilePath: "references/battery.md", Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagEntity, Name: "Battery B", Content: "Battery B has a capacity of 12 ampere-hours.", Detail: "Battery B powers Controller C.", Confidence: 1}}})
	docs = AssignIDs(docs)
	var members []string
	for _, d := range docs[:3] {
		members = append(members, d.Items[0].ID)
	}
	full := strings.TrimSpace(strings.Join(originals, "\n"))
	faithful := "Qualified pump operation and attributed uncertain calibration estimates."
	batterySummary := "Battery B capacity and power supply."
	batteryDetail := "Battery B has a capacity of 12 ampere-hours. Battery B powers Controller C."
	badMaintenance := strings.ReplaceAll(full, "It must not run during maintenance.", "")
	badThreshold := strings.ReplaceAll(full, "It may run only when inlet pressure exceeds 2 bar, except during maintenance.", "")
	genPump, genBattery, assessPump, assessBattery := 0, 0, 0, 0
	client := &v013AcceptanceClient{reply: func(_ context.Context, req llm.CompleteRequest, _ int) (*llm.CompleteResponse, error) {
		if strings.Contains(req.User, v013ItemsMarker) {
			it := v013SemanticCandidate(t, req)
			if it.Name == "Battery B" {
				assessBattery++
				if it.Content != batterySummary || it.Detail != batteryDetail || !strings.Contains(req.User, "12 ampere-hours") || !strings.Contains(req.User, "powers Controller C") {
					t.Fatal("sibling/source candidate damaged")
				}
				return &llm.CompleteResponse{Text: `{"supported":true,"reason":"complete battery source retained","unsupported_items":[],"omissions":[]}`, FinishReason: "stop"}, nil
			}
			assessPump++
			for _, original := range originals {
				if !strings.Contains(strings.Split(req.User, v013ItemsMarker)[0], original) {
					t.Fatal("assessment omitted a full source contribution")
				}
			}
			if it.Name != "Atlas pump" {
				t.Fatal("fusion object identity changed")
			}
			if it.Content == faithful && it.Detail == full {
				return &llm.CompleteResponse{Text: `{"supported":true,"reason":"all three source assertions faithfully retained","unsupported_items":[],"omissions":[]}`, FinishReason: "stop"}, nil
			}
			deficit := "maintenance prohibition and faithful uncertain summary"
			if it.Detail == badThreshold {
				deficit = "inlet pressure exceeds 2 bar and its maintenance exception"
			}
			return &llm.CompleteResponse{Text: v013Wire(t, map[string]any{"supported": false, "reason": deficit, "unsupported_items": []int{0}, "omissions": []string{deficit}}), FinishReason: "stop"}, nil
		}
		if strings.Contains(req.User, `"name":"Battery B"`) {
			genBattery++
			return v013Generation(t, req, batterySummary, batteryDetail), nil
		}
		genPump++
		for _, original := range originals {
			if !strings.Contains(req.User, original) {
				t.Fatal("generation/revision lost a complete source")
			}
		}
		if genPump == 1 {
			return v013Generation(t, req, "Calibration error is settled at 4 percent.", badMaintenance), nil
		}
		if !strings.Contains(req.User, "Concrete semantic feedback") {
			t.Fatal("semantic defect retried without concrete critique")
		}
		if genPump == 2 {
			return v013Generation(t, req, faithful, badThreshold), nil
		}
		return v013Generation(t, req, faithful, full), nil
	}}
	res := &Result{Domains: []Domain{{Name: "Operations", Slug: "operations", Subdomains: []Subdomain{{Name: "equipment", Slug: "equipment", Entities: []Node{
		{ID: "battery", Label: "Entity", Name: "Battery B", Summary: batterySummary, Confidence: 1, Members: []string{docs[3].Items[0].ID}},
		{ID: "pump", Label: "Entity", Name: "Atlas pump", Summary: "Calibration error is settled at 4 percent.", Confidence: 1, Members: members},
	}}}}}}
	rpt := NewExtractor(client, 65536).FuseDescriptions(context.Background(), res, docs)
	if rpt.NodesRewritten != 2 || len(rpt.Outcomes) != 2 || len(rpt.Failures) != 0 || genBattery != 1 || assessBattery != 1 || genPump != 3 || assessPump != 3 {
		t.Fatalf("targeted revision/sibling retention failed: generations=%d/%d assessments=%d/%d", genBattery, genPump, assessBattery, assessPump)
	}
	for _, out := range rpt.Outcomes {
		if out.Outcome != annotation.DocumentComplete || out.CandidateID == "" || out.MaterialID == "" {
			t.Fatal("verified candidate lacks exact evidence identity")
		}
	}
	if got := res.Domains[0].Subdomains[0].Entities[1]; got.Summary != faithful || got.Description != full {
		t.Fatal("corrected summary and full detail were not both applied")
	}
	if err := ValidateFusionCompleteness(res, docs, 0, true); err != nil {
		t.Fatal(err)
	}
	// Reconcile factual semantic outcomes through the actual alias-to-UUID
	// assignment using existing before/after snapshots, independently of counts.
	verified := map[string]Node{}
	for _, node := range res.Domains[0].Subdomains[0].Entities {
		matched := false
		for _, out := range rpt.Outcomes {
			if out.ID == node.ID && out.Outcome == annotation.DocumentComplete && out.Summary == node.Summary && out.Description == node.Description {
				verified[NodeUUID(node.Members)] = node
				matched = true
			}
		}
		if !matched {
			t.Fatal("pre-UUID survivor has no matching exact semantic outcome")
		}
	}
	AssignNodeUUIDs(res.Domains)
	selected, _, err := SelectMaterializationCandidates(res, 0)
	if err != nil {
		t.Fatal(err)
	}
	final := ComputeFinalCoverage(selected, docs)
	if final.Outcome != "complete" || final.CoveredIncidences != 4 {
		t.Fatal("qualified final survivor graph lost a source incidence")
	}
	for _, node := range selected.Domains[0].Subdomains[0].Entities {
		before, ok := verified[node.ID]
		if !ok || before.Name != node.Name || before.Summary != node.Summary || before.Description != node.Description || strings.Join(before.Members, "\x00") != strings.Join(node.Members, "\x00") {
			t.Fatal("UUID/candidate filtering changed the checked semantics or member binding")
		}
	}
	ids := []string{selected.Domains[0].Subdomains[0].Entities[0].ID, selected.Domains[0].Subdomains[0].Entities[1].ID}
	// Unchanged candidates are assessed in place, never regenerated. Stable
	// semantics, identities and contribution sets matter, not model prose bytes.
	repeat := NewExtractor(client, 65536).FuseDescriptions(context.Background(), selected, docs)
	if repeat.NodesRewritten != 2 || genBattery != 1 || genPump != 3 || assessBattery != 2 || assessPump != 4 {
		t.Fatal("unchanged repeat regenerated successful semantic material")
	}
	if selected.Domains[0].Subdomains[0].Entities[0].ID != ids[0] || selected.Domains[0].Subdomains[0].Entities[1].ID != ids[1] || ComputeFinalCoverage(selected, docs).CoveredIncidences != 4 {
		t.Fatal("unchanged repeat changed identities or source incidences")
	}
}

// A repaired mapping alone is not final semantic success. The newly recovered
// node must be described/assessed before the actual final survivor recount.
func TestV013AcceptancePostFilterRepairRequiresRedescription(t *testing.T) {
	t.Parallel()
	docs := []*dktypes.AnnotatedDocument{{Skill: "s", FilePath: "a.md", Items: []dktypes.AnnotatedItem{{ID: "a", Name: "A", Content: "A valid independent fact."}, {ID: "b", Name: "B", Content: "Cooling starts only above 30 degrees Celsius.", Detail: "Cooling is prohibited during maintenance."}}}}
	good := Node{ID: "good", Label: "Entity", Name: "A", Summary: "A fact", Description: "A valid independent fact.", Confidence: 1, Members: []string{"a"}}
	res := &Result{Domains: []Domain{{Name: "D", Slug: "d", Subdomains: []Subdomain{{Name: "S", Slug: "s", Entities: []Node{good, {ID: "missing-description", Label: "Entity", Name: "B", Summary: "Cooling", Confidence: 1, Members: []string{"b"}}}}}}}}
	if err := ValidateFusionCompleteness(res, docs, 0, true); err != nil {
		t.Fatal(err)
	}
	if ComputeFinalCoverage(res, docs).Outcome != "partial" {
		t.Fatal("description filter failed to reveal omission")
	}
	client := &v013AcceptanceClient{reply: func(_ context.Context, req llm.CompleteRequest, _ int) (*llm.CompleteResponse, error) {
		if strings.Contains(req.User, v013ItemsMarker) {
			it := v013SemanticCandidate(t, req)
			if it.Name != "B" || it.Content != "Qualified cooling." || it.Detail != "Cooling starts only above 30 degrees Celsius. Cooling is prohibited during maintenance." {
				t.Fatal("recovered node did not preserve complete qualifications")
			}
			return &llm.CompleteResponse{Text: `{"supported":true,"reason":"both source fields preserved","unsupported_items":[],"omissions":[]}`, FinishReason: "stop"}, nil
		}
		if strings.Contains(req.User, "Node alias=") {
			if !strings.Contains(req.User, "30 degrees Celsius") || !strings.Contains(req.User, "prohibited during maintenance") {
				t.Fatal("recovered node lost joint Content+Detail material")
			}
			return v013Generation(t, req, "Qualified cooling.", "Cooling starts only above 30 degrees Celsius. Cooling is prohibited during maintenance."), nil
		}
		if strings.Contains(req.User, "id=a ") || !strings.Contains(req.User, "id=b ") {
			t.Fatal("final repair resubmitted a successful sibling")
		}
		return &llm.CompleteResponse{Text: `{"domains":[{"name":"D","subdomains":[{"name":"S","entities":[{"id":"recovered","label":"Entity","name":"B","summary":"Cooling","confidence":1,"members":["b"]}],"concepts":[],"relations":[]}]}]}`, FinishReason: "stop"}, nil
	}}
	recovered, rpt, err := NewExtractor(client, 65536).RepairFinalCoverage(context.Background(), res, docs, CoverageOptions{MaxRetries: 1})
	if err != nil || rpt.Outcome != "complete" || rpt.ExtractAttempts != 1 {
		t.Fatal("postfilter missing-only mapping repair failed")
	}
	nodes := recovered.Domains[0].Subdomains[0].Entities
	if len(nodes) != 2 || nodes[0].Description != good.Description || nodes[1].Description != "" {
		t.Fatal("mapping repair pretended to supply verified description")
	}
	// Use the actual recovered node's backing slice as a targeted description
	// view. The successful sibling is outside this model request scope.
	delta := &Result{Domains: []Domain{{Name: "D", Slug: "d", Subdomains: []Subdomain{{Name: "S", Slug: "s", Entities: nodes[1:]}}}}}
	described := NewExtractor(client, 65536).FuseDescriptions(context.Background(), delta, docs)
	if described.NodesRewritten != 1 || len(described.Outcomes) != 1 || described.Outcomes[0].Outcome != annotation.DocumentComplete {
		t.Fatal("recovered mapping was never semantically verified")
	}
	if err := ValidateFusionCompleteness(recovered, docs, 0, true); err != nil {
		t.Fatal(err)
	}
	AssignNodeUUIDs(recovered.Domains)
	selected, _, err := SelectMaterializationCandidates(recovered, 0)
	if err != nil || ComputeFinalCoverage(selected, docs).Outcome != "complete" || len(client.requests) != 3 {
		t.Fatal("actual final graph did not retain repaired semantics and sources")
	}
}

// Unknown/malformed assessment must keep the same candidate, while caller
// cancellation and finite exhaustion remain distinguishable from semantic success.
func TestV013AcceptanceUnknownExhaustionAndCancellation(t *testing.T) {
	t.Parallel()
	input := NodeDescriptionInput{ID: "machine", Label: "Concept", Name: "Cooling", Summary: "Cooling has qualified activation.", Description: "Cooling starts only above 30 degrees Celsius; prohibited during maintenance.", Material: "Cooling starts only above 30 degrees Celsius. Cooling is prohibited during maintenance."}
	for _, tc := range []struct {
		name    string
		retries int
		cancel  bool
		want    annotation.DocumentOutcome
		calls   int
	}{
		{"unknown then malformed then complete", 2, false, annotation.DocumentComplete, 3},
		{"unknown exhaustion", 1, false, annotation.DocumentUnavailable, 2},
		{"caller cancellation", 3, true, annotation.DocumentCancelled, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var first string
			client := &v013AcceptanceClient{reply: func(_ context.Context, req llm.CompleteRequest, n int) (*llm.CompleteResponse, error) {
				if !strings.Contains(req.User, v013ItemsMarker) {
					t.Fatal("unknown assessment regenerated candidate")
				}
				if n == 1 {
					first = req.User
				} else if req.User != first {
					t.Fatal("unknown/malformed response changed exact candidate")
				}
				if tc.cancel {
					cancel()
					return nil, context.Canceled
				}
				text := `{"status":"unknown","reason":"assessment unavailable"}`
				if tc.want == annotation.DocumentComplete && n == 2 {
					text = `{"supported":true,"unsupported_items":null,"omissions":[]}`
				}
				if n == 3 {
					text = `{"supported":true,"reason":"all cooling qualifications preserved","unsupported_items":[],"omissions":[]}`
				}
				return &llm.CompleteResponse{Text: text, FinishReason: "stop"}, nil
			}}
			out := FuseNodeDescriptions(ctx, client, 65536, tc.retries, "operations", "cooling", []NodeDescriptionInput{input}).Nodes[0]
			if out.Outcome != tc.want || len(client.requests) != tc.calls || len(out.Attempts) != tc.calls || out.Description != input.Description || out.Summary != input.Summary {
				t.Fatalf("wrong assessment terminal state: %+v", out)
			}
			if tc.want == annotation.DocumentUnavailable && (!out.Exhausted || out.Reason == "") {
				t.Fatal("exhaustion has no actual reason")
			}
			for _, attempt := range out.Attempts {
				if attempt.Step != "assess" || attempt.CandidateID != out.CandidateID {
					t.Fatal("assessment identity changed")
				}
			}
		})
	}
}

// Operational partial success cannot certify the normal case. A known semantic
// defect remains partial after its one extra recovery; the good sibling is final.
func TestV013AcceptanceSemanticExhaustionKeepsSuccessfulSibling(t *testing.T) {
	t.Parallel()
	inputs := []NodeDescriptionInput{
		{ID: "good", Label: "Entity", Name: "Battery B", Summary: "Battery capacity.", Description: "Battery B has 12 ampere-hours capacity.", Material: "Battery B has 12 ampere-hours capacity."},
		{ID: "bad", Label: "Concept", Name: "Cooling", Summary: "Cooling always runs.", Description: "Cooling is unconditional.", Material: "Cooling starts only above 30 degrees Celsius. Cooling is prohibited during maintenance."},
	}
	goodCalls, badAssessments, badRevisions := 0, 0, 0
	client := &v013AcceptanceClient{reply: func(_ context.Context, req llm.CompleteRequest, _ int) (*llm.CompleteResponse, error) {
		if strings.Contains(req.User, v013ItemsMarker) {
			it := v013SemanticCandidate(t, req)
			if it.Name == "Battery B" {
				goodCalls++
				return &llm.CompleteResponse{Text: `{"supported":true,"reason":"exact battery capacity","unsupported_items":[],"omissions":[]}`, FinishReason: "stop"}, nil
			}
			badAssessments++
			return &llm.CompleteResponse{Text: `{"supported":false,"reason":"unconditional cooling is unsupported","unsupported_items":[0],"omissions":["above 30 degrees Celsius only; prohibited during maintenance"]}`, FinishReason: "stop"}, nil
		}
		badRevisions++
		if !strings.Contains(req.User, "Concrete semantic feedback") || !strings.Contains(req.User, "30 degrees Celsius") || !strings.Contains(req.User, "prohibited during maintenance") {
			t.Fatal("revision was not bound to actual source and critique")
		}
		return v013Generation(t, req, "Cooling always runs.", "Cooling is unconditional."), nil
	}}
	out := FuseNodeDescriptions(context.Background(), client, 65536, 1, "operations", "equipment", inputs)
	if len(out.Nodes) != 2 || out.Nodes[0].Outcome != annotation.DocumentComplete || out.Nodes[1].Outcome != annotation.DocumentPartial || !out.Nodes[1].Exhausted || out.Nodes[1].Reason == "" || goodCalls != 1 || badAssessments != 2 || badRevisions != 1 {
		t.Fatal("semantic exhaustion masked failure or reran valid sibling")
	}
	if len(out.Nodes[0].Attempts) != 1 || len(out.Nodes[1].Attempts) != 3 || out.Nodes[1].Attempts[1].Step != "revise" {
		t.Fatal("actual finite attempt history missing")
	}
}
