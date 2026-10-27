package extract

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

// A finite whole-set revision can partly succeed before a different verifier
// remains unknown. Superseded false assertions must stay only in history;
// already verified corrected assertions and exact unchanged siblings survive.
func TestR2PartialRevisionDoesNotSelectSupersededFalseAssertion(t *testing.T) {
	res, docs, originals := v014Fixture(1)
	sd := &res.Domains[0].Subdomains[0]
	sd.Entities = append(sd.Entities,
		Node{ID: "c1", Name: "c1", Members: []string{"mc1"}},
		Node{ID: "d1", Name: "d1", Members: []string{"md1"}})
	docs[0].Items = append(docs[0].Items,
		dktypes.AnnotatedItem{ID: "mc1", Name: "c1", Content: "c1 references d1. c1 references a1 only during inspection."},
		dktypes.AnnotatedItem{ID: "md1", Name: "d1", Content: "c1 references d1."})
	originals[0] = FreezeSource("fixture", "1.md", originals[0].Content+" c1 references d1. c1 references a1 only during inspection.")
	client := &v014RelationClient{}
	generation := 0
	client.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		switch {
		case strings.Contains(req.System, "SOURCE-RELATION GENERATE"):
			generation++
			rows := v014Rows(req)
			if generation == 1 {
				rows[0].Description = "a1 always depends on b1"
			}
			rows = append(rows,
				Relation{Source: "r3", Target: "r4", Kind: "references", Description: "c1 references d1", Confidence: .9},
				Relation{Source: "r3", Target: "r1", Kind: "references", Description: "c1 references a1 only during inspection", Confidence: .9})
			if generation == 1 {
				rows[2].Description = "c1 always references a1"
			}
			return v014JSON(map[string]any{"relations": rows}), nil
		case !strings.Contains(req.System, "SOURCE-RELATION"):
			var in crossNeighborhood
			_ = json.Unmarshal([]byte(req.User), &in)
			if in.Relation.Source == "c1" && in.Relation.Target == "a1" {
				if generation == 1 {
					return v014JSON(map[string]any{"disposition": "rejected", "reason": "Repair the whole set: c1/a1 references only during inspection, not always; the preceding a1/b1 dependency is also overbroad, because the original says only when bypass is off, not always."}), nil
				}
				return v014JSON(map[string]any{"disposition": "insufficient_evidence", "reason": "Unable to assess the c1/a1 relationship."}), nil
			}
		}
		return v014DefaultReply(ctx, req)
	}
	report := NewExtractor(client, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
	if generation != 2 || report.Scopes[0].Outcome == annotation.DocumentComplete || !report.Scopes[0].Exhausted {
		t.Fatalf("not the intended exhausted partial revision: generation=%d report=%+v", generation, report)
	}
	var selected, graph []string
	for _, d := range report.Selected {
		selected = append(selected, d.Relation.Description)
	}
	for _, r := range sd.Relations {
		graph = append(graph, r.Description)
	}
	t.Logf("scope=%s selected=%q graph=%q", report.Scopes[0].Outcome, selected, graph)
	for _, d := range report.Selected {
		if d.Relation.Description == "a1 always depends on b1" {
			t.Fatal("known superseded false assertion remains Selected/explicit alongside corrected selected assertion")
		}
	}
	if len(report.Selected) != 2 || len(sd.Relations) != 2 {
		t.Fatalf("corrected candidate or good exact sibling lost: selected=%q graph=%q", selected, graph)
	}
}

// Exercise literal UUID masking through generation, whole-set coverage, source
// verification and final relation decoding, including an original marker collision.
func TestR2RelationOriginalUUIDSurvivesAllSteps(t *testing.T) {
	res, docs, originals := v014Fixture(1)
	literal := "82b8c760-9946-4caa-8c90-34d4af551a3a"
	content := "a1 depends on b1 only for operator " + literal + ". Original text contains [[cairn-source-id-0:1]]."
	originals[0] = FreezeSource("fixture", "1.md", content)
	docs[0].Items[0].Content = content
	client := &v014RelationClient{}
	client.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if strings.Contains(req.User, literal) {
			t.Fatal("raw source UUID leaked into model request")
		}
		if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
			var in relationProcessInput
			_ = json.Unmarshal([]byte(req.User), &in)
			rows := v014Rows(req)
			rows[0].Description = in.Sources[0].Content
			return v014JSON(map[string]any{"relations": rows}), nil
		}
		return v014DefaultReply(ctx, req)
	}
	report := NewExtractor(client, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
	if len(report.Selected) != 1 || report.Scopes[0].Outcome != annotation.DocumentComplete {
		t.Fatalf("literal source processing failed: %+v", report)
	}
	d := report.Selected[0]
	if d.Relation.Description != content || len(d.Evidence) != 2 || d.Evidence[0].Quote != content || d.Evidence[0].EndByte != len(content) {
		t.Fatalf("original literal lost or evidence changed: %+v", d)
	}
}

// Identical relation text across two independent originals has one graph row
// but two source-bound decisions; evidence must not silently inherit first-source credit.
func TestR2SameTripleRetainsDistinctSourceEvidence(t *testing.T) {
	res, docs, originals := v014Fixture(1)
	copyDoc := *docs[0]
	copyDoc.FilePath = "2.md"
	docs = append(docs, &copyDoc)
	originals = append(originals, FreezeSource("fixture", "2.md", originals[0].Content+" Independent second report."))
	client := &v014RelationClient{}
	focus := ""
	client.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
			var in relationProcessInput
			_ = json.Unmarshal([]byte(req.User), &in)
			focus = in.FocusSourceID
		}
		if !strings.Contains(req.System, "SOURCE-RELATION") {
			var in crossNeighborhood
			_ = json.Unmarshal([]byte(req.User), &in)
			yes := true
			v := crossVerdict{Disposition: "supported", Reason: "Exact original conditional assertion", RelationSupported: &yes, DirectionSupported: &yes, ExplicitSourceAssertion: &yes}
			for _, source := range in.Sources {
				if source.ID == focus {
					v.Evidence = []crossQuote{{"source", "ma1", source.ID, source.Content}, {"target", "mb1", source.ID, source.Content}}
				}
			}
			return v014JSON(v), nil
		}
		return v014DefaultReply(ctx, req)
	}
	report := NewExtractor(client, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
	if len(report.Selected) != 2 || len(res.Domains[0].Subdomains[0].Relations) != 1 {
		t.Fatalf("unexpected relation/source dedup: %+v", report)
	}
	seen := map[string]bool{}
	for _, d := range report.Selected {
		if d.Outcome != annotation.DocumentComplete || len(d.Evidence) != 2 {
			t.Fatalf("unverified source decision: %+v", d)
		}
		seen[d.Evidence[0].SourceID] = true
	}
	if len(seen) != 2 {
		t.Fatalf("source distinction lost: %+v", report.Selected)
	}
}

// Every terminal stop after accepted coverage must see the reconciled selection.
// The earlier source and the unchanged sibling must not be assessed again.
func TestV014PartialRevisionTerminalStopsKeepOnlyCurrentSelections(t *testing.T) {
	for _, terminal := range []string{"unknown", "transport", "permanent", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			res, docs, originals := v014Fixture(2)
			sd := &res.Domains[0].Subdomains[1]
			sd.Entities = append(sd.Entities,
				Node{ID: "c2", Name: "c2", Members: []string{"mc2"}},
				Node{ID: "d2", Name: "d2", Members: []string{"md2"}})
			docs[1].Items = append(docs[1].Items,
				dktypes.AnnotatedItem{ID: "mc2", Name: "c2", Content: "c2 references d2. c2 references a2 only during inspection."},
				dktypes.AnnotatedItem{ID: "md2", Name: "d2", Content: "c2 references d2."})
			originals[1] = FreezeSource("fixture", "2.md", originals[1].Content+" c2 references d2. c2 references a2 only during inspection.")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &v014RelationClient{}
			generation, terminalCalls := 0, 0
			checks := map[string]int{}
			client.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
					var in relationProcessInput
					_ = json.Unmarshal([]byte(req.User), &in)
					if in.FocusSourceID == "s2" {
						generation++
						rows := v014Rows(req)
						if generation == 1 {
							rows[0].Description = "a2 always depends on b2"
						}
						rows = append(rows,
							Relation{Source: "r5", Target: "r6", Kind: "references", Description: "c2 references d2", Confidence: .9},
							Relation{Source: "r5", Target: "r3", Kind: "references", Description: "c2 references a2 only during inspection", Confidence: .9})
						return v014JSON(map[string]any{"relations": rows}), nil
					}
				} else if !strings.Contains(req.System, "SOURCE-RELATION") {
					var in crossNeighborhood
					_ = json.Unmarshal([]byte(req.User), &in)
					checks[in.Relation.Source+"/"+in.Relation.Target]++
					if in.Relation.Source == "c2" && in.Relation.Target == "a2" {
						if generation == 1 {
							return v014JSON(map[string]any{"disposition": "rejected", "reason": "Repair the whole set: the earlier a2/b2 dependency also omitted the bypass condition."}), nil
						}
						terminalCalls++
						switch terminal {
						case "transport":
							return nil, errors.New("temporary transport failure")
						case "permanent":
							return nil, errors.New("openai_compatible: missing API key")
						case "cancelled":
							cancel()
							return nil, ctx.Err()
						default:
							return v014JSON(map[string]any{"disposition": "insufficient_evidence", "reason": "assessment remains unknown"}), nil
						}
					}
				}
				return v014DefaultReply(ctx, req)
			}
			report := NewExtractor(client, 4096).ProcessRelations(ctx, res, docs, originals, RelationProcessOptions{MaxRetries: 1})
			if generation != 2 || terminalCalls != 1 || checks["a1/b1"] != 1 || checks["c2/d2"] != 1 {
				t.Fatalf("shared budget reset or successful sibling replayed: generations=%d stops=%d checks=%v", generation, terminalCalls, checks)
			}
			wantOutcome, wantExhausted := annotation.DocumentUnavailable, true
			if terminal == "cancelled" {
				wantOutcome, wantExhausted = annotation.DocumentCancelled, false
			} else if terminal == "permanent" {
				wantExhausted = false
			}
			out := report.Scopes[1]
			if report.Scopes[0].Outcome != annotation.DocumentComplete || out.Outcome != wantOutcome || out.Exhausted != wantExhausted || out.Reason == "" {
				t.Fatalf("terminal facts changed: %+v", report.Scopes)
			}
			if len(report.Selected) != 3 || len(sd.Relations) != 2 || len(res.Domains[0].Subdomains[0].Relations) != 1 {
				t.Fatalf("selection counts: selected=%d changed=%d other-source=%d", len(report.Selected), len(sd.Relations), len(res.Domains[0].Subdomains[0].Relations))
			}
			for _, d := range report.Selected {
				if d.Relation.Description == "a2 always depends on b2" || d.Relation.Target == "a2" {
					t.Fatalf("obsolete or unverified assertion selected: %+v", d)
				}
			}
			foundHistory := false
			for _, d := range report.Decisions {
				if d.Relation.Description == "a2 always depends on b2" && d.Classification == "explicit" {
					foundHistory = true
				}
			}
			if !foundHistory {
				t.Fatal("superseded decision history erased")
			}
		})
	}
}

// Retiring a selection also retires its cached verdict. A later reintroduction
// must be assessed again, while a still-present exact sibling stays cached.
func TestV014RevisionReintroducedCandidateGetsFreshAssessment(t *testing.T) {
	res, docs, originals := v014Fixture(1)
	sd := &res.Domains[0].Subdomains[0]
	sd.Entities = append(sd.Entities,
		Node{ID: "c1", Name: "c1", Members: []string{"mc1"}},
		Node{ID: "d1", Name: "d1", Members: []string{"md1"}})
	docs[0].Items = append(docs[0].Items,
		dktypes.AnnotatedItem{ID: "mc1", Name: "c1", Content: "c1 references d1. c1 references a1 only during inspection."},
		dktypes.AnnotatedItem{ID: "md1", Name: "d1", Content: "c1 references d1."})
	originals[0] = FreezeSource("fixture", "1.md", originals[0].Content+" c1 references d1. c1 references a1 only during inspection.")
	client := &v014RelationClient{}
	generation := 0
	checks := map[string]int{}
	client.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
			generation++
			rows := v014Rows(req)
			if generation == 2 {
				rows = nil
			}
			rows = append(rows,
				Relation{Source: "r3", Target: "r4", Kind: "references", Description: "c1 references d1", Confidence: .9},
				Relation{Source: "r3", Target: "r1", Kind: "references", Description: "c1 references a1 only during inspection", Confidence: .9})
			return v014JSON(map[string]any{"relations": rows}), nil
		}
		if !strings.Contains(req.System, "SOURCE-RELATION") {
			var in crossNeighborhood
			_ = json.Unmarshal([]byte(req.User), &in)
			checks[in.Relation.Source+"/"+in.Relation.Target]++
			if in.Relation.Source == "c1" && in.Relation.Target == "a1" && generation < 3 {
				return v014JSON(map[string]any{"disposition": "rejected", "reason": "Revise the whole candidate set against the original."}), nil
			}
		}
		return v014DefaultReply(ctx, req)
	}
	report := NewExtractor(client, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 2})
	if generation != 3 || checks["a1/b1"] != 2 || checks["c1/d1"] != 1 || checks["c1/a1"] != 3 {
		t.Fatalf("retired verdict reused or unchanged sibling replayed: generation=%d checks=%v", generation, checks)
	}
	if report.Scopes[0].Outcome != annotation.DocumentComplete || len(report.Selected) != 3 || len(sd.Relations) != 3 {
		t.Fatalf("reintroduced verified candidate missing or duplicated: %+v", report)
	}
}

// A replacement omission has no authority until whole-set coverage accepts it.
func TestV014UnknownRevisionCoverageRetainsPreviouslyVerifiedSelection(t *testing.T) {
	res, docs, originals := v014Fixture(1)
	client := &v014RelationClient{}
	generation, checks := 0, 0
	client.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
			generation++
			rows := v014Rows(req)
			if generation == 1 {
				rows = append(rows, Relation{Source: "r1", Target: "r2", Kind: "references", Description: "unsupported second assertion", Confidence: .9})
			} else {
				rows = []Relation{}
			}
			return v014JSON(map[string]any{"relations": rows}), nil
		}
		if strings.Contains(req.System, "SOURCE-RELATION COVERAGE") && generation == 2 {
			return v014JSON(map[string]any{"complete": nil, "supported": nil, "reason": "unknown revised coverage"}), nil
		}
		if !strings.Contains(req.System, "SOURCE-RELATION") {
			checks++
			var in crossNeighborhood
			_ = json.Unmarshal([]byte(req.User), &in)
			if in.Relation.Kind == "references" {
				return v014JSON(map[string]any{"disposition": "rejected", "reason": "Remove the unsupported second assertion; preserve the correct conditional dependency."}), nil
			}
		}
		return v014DefaultReply(ctx, req)
	}
	report := NewExtractor(client, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
	if generation != 2 || checks != 2 || !report.Scopes[0].Exhausted || report.Scopes[0].Outcome == annotation.DocumentComplete || len(report.Selected) != 1 || len(res.Domains[0].Subdomains[0].Relations) != 1 {
		t.Fatalf("unaccepted omission retired verified selection: %+v", report)
	}
}
