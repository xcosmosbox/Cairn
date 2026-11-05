package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
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

// Reuse the public mini originals/delta and the actual failed parent/January
// prose shape. This is a request/join and finite-failure control, not a semantic
// judge: scripted positive coverage does not prove provider interpretation.
func TestV016NodeContextRequestJoinsAndBodyFailure(t *testing.T) {
	for _, wrongBody := range []bool{false, true} {
		t.Run(fmt.Sprint(wrongBody), func(t *testing.T) {
			const skill = "paperhall-mini"
			const parent = "霜羽票签是一种票签，是纸岚展馆夜场使用的票签。夜场入厅依赖霜羽票签；只有霜羽票签有效时，夜场入厅才可执行。另有来源说明：本页的“票签”指霜羽票签，夜场入厅依赖它。有效期方面：2026年2月版将霜羽票签的有效期改为6分钟；9分钟仅适用于2026年1月版。过期票签仍不被夜场入厅接受。"
			jan := "2026年1月版霜羽票签是霜羽票签的2026年1月版本；其有效期为9分钟。"
			if wrongBody {
				jan = "2026年1月版霜羽票签的有效期为6分钟。"
			}
			paths := []string{"mini/SKILL.md", "mini/references/log.md", "mini/references/pass.md"}
			var originals []FrozenSource
			var docs []*dktypes.AnnotatedDocument
			rawByPath := map[string]FrozenSource{}
			for i, path := range paths {
				raw, err := os.ReadFile(filepath.Join("..", "..", "test", "e2e", "mini-baseline", "fixtures", "sources", filepath.FromSlash(path)))
				if err != nil {
					t.Fatal(err)
				}
				if i == 2 {
					delta, e := os.ReadFile(filepath.Join("..", "..", "test", "e2e", "mini-baseline", "fixtures", "change.md"))
					if e != nil {
						t.Fatal(e)
					}
					raw = append(raw, append([]byte("\n\n"), delta...)...)
				}
				src := FreezeSource(skill, path, string(raw))
				originals = append(originals, src)
				rawByPath[path] = src
				otherID, otherName := fmt.Sprint("entry-", i+1), "夜场入厅"
				if i == 2 {
					otherID, otherName = "jan-3", "2026年1月版霜羽票签"
				}
				docs = append(docs, &dktypes.AnnotatedDocument{Skill: skill, FilePath: path, Items: []dktypes.AnnotatedItem{{ID: fmt.Sprint("ticket-", i+1), Name: "霜羽票签", Content: string(raw), Detail: string(raw)}, {ID: otherID, Name: otherName, Content: string(raw), Detail: string(raw)}}})
			}
			res := &Result{Domains: []Domain{{Slug: "hall", Subdomains: []Subdomain{{Slug: "entry", Entities: []Node{
				{ID: "ticket", Name: "霜羽票签", Summary: "票签及其分别限定的版本有效期", Description: parent, Members: []string{"ticket-1", "ticket-2", "ticket-3"}},
				{ID: "jan", Name: "2026年1月版霜羽票签", Summary: jan, Description: jan, Members: []string{"jan-3"}},
				{ID: "entry", Name: "夜场入厅", Summary: "依赖有效票签", Description: "夜场入厅依赖霜羽票签；只有霜羽票签有效时才可执行。", Members: []string{"entry-1", "entry-2"}},
			}}}}}}
			generated, covered := map[string]int{}, map[string]int{}
			verified := 0
			c := &v014RelationClient{}
			c.reply = func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if strings.Contains(req.System, "SOURCE-RELATION") {
					// This assertion establishes only the shared request contract. Semantic
					// interpretation remains the independent provider's responsibility.
					for _, clause := range []string{"同一原文内只有一个兼容指代对象", "每句机械打印", "仅材料有事实而实际正文缺失"} {
						if !strings.Contains(req.System, clause) {
							t.Fatalf("missing node-context request constraint %q", clause)
						}
					}
					var in relationProcessInput
					if err := json.Unmarshal([]byte(req.User), &in); err != nil {
						t.Fatal(err)
					}
					if len(in.Sources) != 3 || len(in.Material) != 3 || len(in.Nodes) != 3 {
						t.Fatalf("actual context omitted: %+v", in)
					}
					sourceByID := map[string]crossOriginal{}
					for _, src := range in.Sources {
						want := rawByPath[src.Path]
						if src.Skill != skill || src.SHA256 != want.SHA256 || src.Content != want.Content {
							t.Fatalf("original context changed: %+v", src)
						}
						sourceByID[src.ID] = src
					}
					material := map[string]map[string]bool{}
					for _, m := range in.Material {
						material[m.SourceID] = map[string]bool{}
						for _, item := range m.Items {
							material[m.SourceID][item.ID] = true
						}
					}
					for _, n := range in.Nodes {
						for _, binding := range n.MemberSources {
							src := sourceByID[binding.SourceID]
							if !containsMember(n.Members, binding.MemberID) || !containsMember(src.Members, binding.MemberID) || !material[binding.SourceID][binding.MemberID] {
								t.Fatalf("node/source/material join broken: %+v / %+v", n, binding)
							}
						}
						if n.Name == "霜羽票签" && (n.Description != parent || len(n.MemberSources) != 3) {
							t.Fatalf("fused contributions lost: %+v", n)
						}
						if n.Name == "2026年1月版霜羽票签" && n.Description != jan {
							t.Fatalf("existing January prose silently repaired: %+v", n)
						}
					}
					if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
						generated[in.FocusSourceID]++
						if in.FocusSourceID == "s3" {
							return v014JSON(map[string]any{"relations": []Relation{}}), nil
						}
						a, b := "", ""
						for _, n := range in.Nodes {
							if n.Name == "夜场入厅" {
								a = n.Alias
							}
							if n.Name == "霜羽票签" {
								b = n.Alias
							}
						}
						description := "夜场入厅依赖霜羽票签；只有霜羽票签有效时才可执行。"
						if in.FocusSourceID == "s2" {
							description = "夜场入厅依赖本页所指的霜羽票签。"
						}
						return v014JSON(map[string]any{"relations": []Relation{{Source: a, Target: b, Kind: "depends_on", Description: description, Confidence: .9}}}), nil
					}
					covered[in.FocusSourceID]++
					if wrongBody && in.FocusSourceID == "s3" {
						return v014JSON(map[string]any{"complete": true, "supported": false, "reason": "January node wrongly says six minutes despite retained raw and member material", "critique": "restore January nine minutes and retain February six minutes in actual node prose"}), nil
					}
					return v014JSON(map[string]any{"complete": true, "supported": true, "reason": "scripted request-wiring control only"}), nil
				}
				verified++
				if !strings.Contains(req.System, relationFocusAssertionContract) {
					t.Fatal("candidate focus verifier contract lost")
				}
				var in crossNeighborhood
				if err := json.Unmarshal([]byte(req.User), &in); err != nil {
					t.Fatal(err)
				}
				yes := true
				v := crossVerdict{Disposition: "supported", Reason: "scripted explicit dependency control", RelationSupported: &yes, DirectionSupported: &yes, ExplicitSourceAssertion: &yes}
				for _, pair := range []struct {
					role     string
					endpoint CrossEndpoint
				}{{"source", in.Source}, {"target", in.Target}} {
					for _, src := range in.Sources {
						if src.ID != in.FocusSourceID {
							continue
						}
						for _, member := range pair.endpoint.Members {
							if containsMember(src.Members, member) {
								v.Evidence = append(v.Evidence, crossQuote{pair.role, member, src.ID, src.Content})
								break
							}
						}
					}
				}
				return v014JSON(v), nil
			}
			rpt := NewExtractor(c, 65536).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
			if verified != 2 || len(rpt.Selected) != 2 || rpt.Scopes[0].Outcome != annotation.DocumentComplete || rpt.Scopes[1].Outcome != annotation.DocumentComplete {
				t.Fatalf("healthy focus dependencies changed: %+v verifiercalls=%d", rpt, verified)
			}
			last := rpt.Scopes[2]
			if wrongBody {
				if generated["s3"] != 2 || covered["s3"] != 2 || last.Outcome != annotation.DocumentPartial || !last.Exhausted {
					t.Fatalf("wrong node prose laundered or budget changed: %+v / %v / %v", last, generated, covered)
				}
			} else if generated["s3"] != 1 || covered["s3"] != 1 || last.Outcome != annotation.DocumentComplete {
				t.Fatalf("request wiring control failed: %+v", last)
			}
		})
	}
}
