package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

// Full 的废 seed 拒绝曾污染已完成来源；增量持久旧边仍必须保守失败。
// Scripted controls verify state handling, not a model's semantic judgment.
func TestV016NewSeedAuditRejectionIsCandidateLocalOnly(t *testing.T) {
	for _, tc := range []struct {
		name, verdict string
		newSeed       bool
		want          annotation.DocumentOutcome
		exhausted     bool
		calls         int
	}{
		{"full-rejected", "rejected", true, annotation.DocumentComplete, false, 1},
		{"default-existing-rejected", "rejected", false, annotation.DocumentPartial, false, 1},
		{"full-positive-not-selected", "supported", true, annotation.DocumentComplete, false, 1},
		{"full-unknown", "insufficient_evidence", true, annotation.DocumentUnavailable, true, 2},
		{"full-invalid-verdict", "invalid", true, annotation.DocumentUnavailable, true, 2},
		{"full-bad-quotes", "bad-quotes", true, annotation.DocumentPartial, false, 1},
		{"full-transport", "transport", true, annotation.DocumentUnavailable, true, 2},
		{"full-permanent", "permanent", true, annotation.DocumentUnavailable, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, docs, originals := v014Fixture(1)
			seed := Relation{Source: "a1", Target: "b1", Kind: "references", Description: "discarded supplied seed", Confidence: .8}
			res.Domains[0].Subdomains[0].Relations = []Relation{seed}
			calls := 0
			c := &v014RelationClient{}
			c.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				var in crossNeighborhood
				_ = json.Unmarshal([]byte(req.User), &in)
				if in.Relation.Description != seed.Description {
					return v014DefaultReply(ctx, req)
				}
				calls++
				switch tc.verdict {
				case "supported":
					return v014Supported(req), nil
				case "bad-quotes":
					yes := true
					return v014JSON(crossVerdict{Disposition: "supported", Reason: "bad evidence control", RelationSupported: &yes, DirectionSupported: &yes, Evidence: []crossQuote{{"source", "ma1", "s1", "invented quote"}}}), nil
				case "transport":
					return nil, fmt.Errorf("temporary scripted transport error")
				case "permanent":
					return nil, fmt.Errorf("openai_compatible: status 401: scripted permanent control")
				default:
					return v014JSON(map[string]any{"disposition": tc.verdict, "reason": "unsupported supplied seed control"}), nil
				}
			}
			rpt := NewExtractor(c, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1, SuppliedCandidatesAreNew: tc.newSeed})
			if calls != tc.calls || rpt.Scopes[0].Outcome != tc.want || rpt.Scopes[0].Exhausted != tc.exhausted {
				t.Fatalf("calls=%d scope=%+v", calls, rpt.Scopes[0])
			}
			if len(rpt.Selected) != 1 || len(res.Domains[0].Subdomains[0].Relations) != 1 || res.Domains[0].Subdomains[0].Relations[0].Description == seed.Description {
				t.Fatalf("discarded audit seed inserted or current selection lost: %+v", rpt)
			}
			d := rpt.Decisions[0]
			if d.Classification != "unknown" {
				t.Fatalf("old audit gained explicit-source credit: %+v", d)
			}
			if tc.verdict == "rejected" && (d.Disposition != "rejected_semantic" || d.Outcome != annotation.DocumentPartial || !strings.Contains(d.Reason, "unsupported supplied seed") || len(d.Attempts) == 0) {
				t.Fatalf("actual rejected decision was washed into source completion: %+v", d)
			}
			if tc.verdict == "bad-quotes" && d.Disposition != "rejected_evidence" {
				t.Fatalf("wrong negative exercised: %+v", d)
			}
		})
	}
}

// 未知后确定拒绝虽恢复 Full 候选过滤状态，也不能为下一个 audit 充值预算。
func TestV016NewSeedAuditRecoveryDoesNotRefillBudget(t *testing.T) {
	res, docs, originals := v014Fixture(1)
	res.Domains[0].Subdomains[0].Relations = []Relation{
		{Source: "a1", Target: "b1", Kind: "references", Description: "first discarded seed", Confidence: .8},
		{Source: "a1", Target: "b1", Kind: "references", Description: "second unknown seed", Confidence: .8},
	}
	calls := map[string]int{}
	c := &v014RelationClient{}
	c.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		var in crossNeighborhood
		_ = json.Unmarshal([]byte(req.User), &in)
		if strings.HasSuffix(in.Relation.Description, "seed") {
			calls[in.Relation.Description]++
			if in.Relation.Description == "first discarded seed" && calls[in.Relation.Description] == 2 {
				return v014JSON(map[string]any{"disposition": "rejected", "reason": "definitely unsupported seed"}), nil
			}
			return v014JSON(map[string]any{"disposition": "insufficient_evidence", "reason": "temporarily unknown"}), nil
		}
		return v014DefaultReply(ctx, req)
	}
	rpt := NewExtractor(c, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1, SuppliedCandidatesAreNew: true})
	if calls["first discarded seed"] != 2 || calls["second unknown seed"] != 1 || rpt.Scopes[0].Outcome != annotation.DocumentUnavailable || !rpt.Scopes[0].Exhausted || rpt.Decisions[0].Outcome != annotation.DocumentPartial {
		t.Fatalf("new seed exception reset a shared allowance or erased rejection: %+v calls=%v", rpt, calls)
	}
}

// 新 seed 例外不能覆盖当前选择的失败、全文遗漏、先前 partial 或取消。
func TestV016NewSeedOptionDoesNotCertifyActiveFailure(t *testing.T) {
	for _, mode := range []string{"active-reject", "missing-required-edge", "cancel-audit"} {
		t.Run(mode, func(t *testing.T) {
			res, docs, originals := v014Fixture(1)
			res.Domains[0].Subdomains[0].Relations = []Relation{{Source: "a1", Target: "b1", Kind: "references", Description: "discarded supplied seed", Confidence: .8}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			generations := 0
			c := &v014RelationClient{}
			c.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
					generations++
				}
				if mode == "missing-required-edge" && strings.Contains(req.System, "SOURCE-RELATION COVERAGE") {
					return v014JSON(map[string]any{"complete": false, "supported": true, "reason": "required explicit edge is missing", "critique": "restore required dependency and condition"}), nil
				}
				if !strings.Contains(req.System, "SOURCE-RELATION") {
					var in crossNeighborhood
					_ = json.Unmarshal([]byte(req.User), &in)
					if mode == "active-reject" && in.Relation.Kind == "depends_on" {
						return v014JSON(map[string]any{"disposition": "rejected", "reason": "wrong active dependency direction"}), nil
					}
					if mode == "cancel-audit" && in.Relation.Description == "discarded supplied seed" {
						cancel()
						return nil, context.Canceled
					}
				}
				return v014DefaultReply(ctx, req)
			}
			rpt := NewExtractor(c, 4096).ProcessRelations(ctx, res, docs, originals, RelationProcessOptions{MaxRetries: 1, SuppliedCandidatesAreNew: true})
			want := annotation.DocumentPartial
			if mode == "cancel-audit" {
				want = annotation.DocumentCancelled
			} else if generations != 2 || !rpt.Scopes[0].Exhausted {
				t.Fatalf("active defect did not use the finite shared recovery: %+v generations=%d", rpt, generations)
			}
			if rpt.Scopes[0].Outcome != want {
				t.Fatalf("new-seed option masked active failure: %+v", rpt)
			}
		})
	}
}

// 全局 s3 在单原文邻域是局部 s1；原实现未告知 focus，导致合法检查选错来源。
func TestV016VerifierFocusUsesLocalSourceIdentity(t *testing.T) {
	t.Parallel()
	res, docs, originals := v014Fixture(3)
	members := map[string]map[sourceKey]bool{}
	frozen := map[sourceKey]FrozenSource{}
	for _, doc := range docs {
		key := sourceKey{doc.Skill, doc.FilePath}
		for _, it := range doc.Items {
			members[it.ID] = map[sourceKey]bool{key: true}
		}
	}
	for _, src := range originals {
		frozen[sourceKey{src.Skill, src.Path}] = src
	}
	index := indexEndpoints(res.Domains)
	scope := endpointScope{"d", "s3"}
	r := Relation{Source: "a3", Target: "b3", Kind: "depends_on", Description: originals[2].Content}
	source, target, ss, ts := index.relation(scope, r)
	w := prepareCrossCandidate(crossTask{scope: scope, relation: r, source: source, target: target, ss: ss, ts: ts}, members, frozen, map[sourceKey]bool{}, 4096, sourceKey{"fixture", "3.md"})
	var in crossNeighborhood
	if err := json.Unmarshal([]byte(w.request.User), &in); err != nil {
		t.Fatal(err)
	}
	if !w.requestable || len(in.Sources) != 1 || in.FocusSourceID != "s1" || in.FocusSource == nil || in.FocusSource.ID != "s1" || in.FocusSource.Path != "3.md" || in.FocusSource.SHA256 != originals[2].SHA256 || in.Sources[0].ID != in.FocusSourceID {
		t.Fatalf("global source identity leaked into local neighborhood: %+v", in)
	}
	legacy := prepareCrossCandidate(w.task, members, frozen, map[sourceKey]bool{}, 4096)
	if strings.Contains(legacy.request.User, "focus_source") {
		t.Fatal("ordinary cross verifier contract changed without a focus")
	}
}

// 跨源端点身份仍可分别引用两份原文，但不能仅凭 focus 身份词为他源完整断言背书。
func TestV016VerifierFocusKeepsCrossSourceIdentityAndAssertionCheck(t *testing.T) {
	for _, focusSupported := range []bool{true, false} {
		t.Run(fmt.Sprint(focusSupported), func(t *testing.T) {
			res, docs, originals := v014Fixture(2)
			seen := 0
			c := &v014RelationClient{}
			c.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				var gen relationProcessInput
				_ = json.Unmarshal([]byte(req.User), &gen)
				if strings.Contains(req.System, "SOURCE-RELATION GENERATE") && gen.FocusSourceID == "s2" {
					return v014JSON(map[string]any{"relations": []Relation{{Source: "r1", Target: "r3", Kind: "depends_on", Description: "cross-source assertion control", Confidence: .9}}}), nil
				}
				var in crossNeighborhood
				_ = json.Unmarshal([]byte(req.User), &in)
				if in.Relation.Description == "cross-source assertion control" {
					seen++
					if in.FocusSource == nil || in.FocusSourceID != "s2" || in.FocusSource.Path != "2.md" || in.FocusSource.SHA256 != originals[1].SHA256 || len(in.Sources) != 2 || !strings.Contains(req.System, "focus 只提及端点") {
						t.Fatalf("missing exact focus/complete-assertion request contract: %+v / %s", in, req.System)
					}
					v := v014Supported(req)
					var verdict crossVerdict
					_ = json.Unmarshal([]byte(v.Text), &verdict)
					verdict.ExplicitSourceAssertion = &focusSupported
					return v014JSON(verdict), nil
				}
				return v014DefaultReply(ctx, req)
			}
			rpt := NewExtractor(c, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1, SuppliedCandidatesAreNew: true})
			if seen != 1 {
				t.Fatalf("cross control absent: %+v", rpt)
			}
			last := rpt.Scopes[1]
			if focusSupported {
				if last.Outcome != annotation.DocumentComplete || len(rpt.Selected) != 2 {
					t.Fatalf("cross-source endpoint identities were forbidden: %+v", rpt)
				}
			} else if last.Outcome != annotation.DocumentPartial || !last.Exhausted || len(rpt.Selected) != 1 {
				t.Fatalf("focus identity quote substituted for complete assertion: %+v", rpt)
			}
		})
	}
}

// Fresh mini verifier37 rejected a borrowed condition, but revise38 lost both
// its concrete reason and a locatable alias-wire candidate. Exercise that same
// supported/explicit=false shape through the existing finite recovery loop.
func TestV016FocusRejectionFeedbackRepairsWithinExistingBudget(t *testing.T) {
	for _, repair := range []bool{true, false} {
		t.Run(fmt.Sprint(repair), func(t *testing.T) {
			res, docs, originals := v014Fixture(2)
			const compound = "dependency plus validity condition borrowed from 1.md"
			const reason = "2.md states the dependency but does not state the validity condition from 1.md"
			generations, verifications := map[string]int{}, 0
			var failed Relation
			c := &v014RelationClient{}
			c.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				var in relationProcessInput
				_ = json.Unmarshal([]byte(req.User), &in)
				if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
					generations[in.FocusSourceID]++
					if in.FocusSourceID == "s2" {
						rows := v014Rows(req)
						rows[0].Description = compound
						failed = rows[0]
						if generations["s2"] > 1 {
							wire, _ := json.Marshal(failed)
							for _, detail := range []string{"fixture/2.md", reason, string(wire)} {
								if !strings.Contains(in.Feedback, detail) {
									t.Errorf("revision lost actionable focus rejection %q: %s", detail, in.Feedback)
								}
							}
							if repair {
								rows = v014Rows(req)
							}
						}
						return v014JSON(map[string]any{"relations": rows}), nil
					}
				}
				var candidate crossNeighborhood
				_ = json.Unmarshal([]byte(req.User), &candidate)
				if candidate.Relation.Description == compound {
					verifications++
					var verdict crossVerdict
					_ = json.Unmarshal([]byte(v014Supported(req).Text), &verdict)
					no := false
					verdict.ExplicitSourceAssertion = &no
					verdict.Reason = reason
					return v014JSON(verdict), nil
				}
				return v014DefaultReply(ctx, req)
			}
			rpt := NewExtractor(c, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1, SuppliedCandidatesAreNew: true})
			if generations["s1"] != 1 || generations["s2"] != 2 || rpt.Scopes[0].Outcome != annotation.DocumentComplete {
				t.Fatalf("recovery reran sibling or changed budget: %+v generations=%v", rpt, generations)
			}
			want, selected, checks := annotation.DocumentPartial, 1, 2
			if repair {
				want, selected, checks = annotation.DocumentComplete, 2, 1
			}
			if rpt.Scopes[1].Outcome != want || rpt.Scopes[1].Exhausted == repair || len(rpt.Selected) != selected || verifications != checks {
				t.Fatalf("incorrect finite repair result: %+v verifies=%d", rpt, verifications)
			}
			for _, d := range rpt.Selected {
				if d.Relation.Description == compound {
					t.Fatal("other-source qualifier was selected")
				}
			}
		})
	}
}

// F03/F04 each state one side of the same-day attributed disagreement. The
// shared focus contract must preserve that existing two-assertion exception;
// scripted predicate negatives still exercise the existing hard verifier gates.
func TestV016FocusContractPreservesStrictTwoSourceContradiction(t *testing.T) {
	for _, mode := range []string{"F03-F04", "absence", "different-time", "different-condition", "incomplete", "focus-contribution-false"} {
		t.Run(mode, func(t *testing.T) {
			res, docs, originals := v014AcceptanceFixture(t)
			client := v014AcceptanceGoodClient(t)
			reply := client.reply
			checks := 0
			client.reply = func(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if !strings.Contains(req.System, relationFocusAssertionContract) {
					t.Fatal("generation/coverage/verifier focus contracts drifted")
				}
				var in crossNeighborhood
				_ = json.Unmarshal([]byte(req.User), &in)
				if in.Relation.Kind != "contradicts" {
					return reply(ctx, req)
				}
				checks++
				v := v014AcceptanceVerdict(t, req)
				predicates := v["contradiction"].(map[string]bool)
				switch mode {
				case "absence":
					predicates["incompatible"] = false
				case "different-time":
					predicates["same_time"] = false
				case "different-condition":
					predicates["same_conditions"] = false
				case "incomplete":
					delete(predicates, "same_attribute")
				case "focus-contribution-false":
					// Five true contradiction gates cannot replace a false
					// explicit focus-contribution predicate.
					v["explicit_source_assertion"] = false
					v["reason"] = "focus provides endpoint identity but no conflicting assertion"
				}
				return v014AcceptanceReply(t, v), nil
			}
			rpt := NewExtractor(client, 65536).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
			if checks == 0 {
				t.Fatal("F03/F04 contradiction was not verified")
			}
			selected := 0
			for _, d := range rpt.Selected {
				if d.Relation.Kind == "contradicts" {
					selected++
					if len(d.Evidence) < 2 || d.Classification != "explicit" {
						t.Fatalf("lost two-sided explicit evidence: %+v", d)
					}
				}
			}
			if (mode == "F03-F04" && selected != 1) || (mode != "F03-F04" && selected != 0) {
				t.Fatalf("contradiction exception bypassed strict predicates: selected=%d report=%+v", selected, rpt)
			}
		})
	}
}
