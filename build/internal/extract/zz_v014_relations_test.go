package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type v014RelationClient struct {
	requests []llm.CompleteRequest
	reply    func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error)
}

func (c *v014RelationClient) ProviderName() string { return "offline-v014" }
func (c *v014RelationClient) Complete(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.requests = append(c.requests, r)
	return c.reply(ctx, r)
}
func v014JSON(v any) *llm.CompleteResponse {
	b, _ := json.Marshal(v)
	return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}
}
func v014Fixture(n int) (*Result, []*dktypes.AnnotatedDocument, []FrozenSource) {
	res := &Result{Domains: []Domain{{Slug: "d", Name: "D"}}}
	var docs []*dktypes.AnnotatedDocument
	var originals []FrozenSource
	for i := 1; i <= n; i++ {
		a, b := fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
		ma, mb := "m"+a, "m"+b
		sd := Subdomain{Slug: fmt.Sprint("s", i), Name: fmt.Sprint("S", i), Entities: []Node{{ID: a, Name: a, Members: []string{ma}, Description: "generated summary is not evidence"}, {ID: b, Name: b, Members: []string{mb}, Description: "generated summary is not evidence"}}}
		res.Domains[0].Subdomains = append(res.Domains[0].Subdomains, sd)
		path := fmt.Sprint(i, ".md")
		text := fmt.Sprintf("%s depends on %s only when bypass is off; when bypass is on it does not require %s.", a, b, b)
		docs = append(docs, &dktypes.AnnotatedDocument{Skill: "fixture", FilePath: path, Items: []dktypes.AnnotatedItem{{ID: ma, Name: a, Content: "all content " + a, Detail: "all detail " + a}, {ID: mb, Name: b, Content: "all content " + b, Detail: "all detail " + b}}})
		originals = append(originals, FreezeSource("fixture", path, text))
	}
	return res, docs, originals
}
func v014Rows(req llm.CompleteRequest) []Relation {
	var in relationProcessInput
	_ = json.Unmarshal([]byte(req.User), &in)
	var aliases []string
	for _, n := range in.Nodes {
		if len(n.Members) > 0 {
			for _, s := range in.Sources {
				if s.ID == in.FocusSourceID && containsMember(s.Members, n.Members[0]) {
					aliases = append(aliases, n.Alias)
				}
			}
		}
	}
	if len(aliases) < 2 {
		return nil
	}
	return []Relation{{Source: aliases[0], Target: aliases[1], Kind: "depends_on", Description: "only when bypass is off; when on no dependency", Confidence: .9}}
}
func v014Supported(req llm.CompleteRequest) *llm.CompleteResponse {
	var in crossNeighborhood
	_ = json.Unmarshal([]byte(req.User), &in)
	yes := true
	v := crossVerdict{Disposition: "supported", Reason: "explicit condition and direction preserved", RelationSupported: &yes, DirectionSupported: &yes, ExplicitSourceAssertion: &yes}
	for _, pair := range []struct {
		role     string
		endpoint CrossEndpoint
	}{{"source", in.Source}, {"target", in.Target}} {
		for _, s := range in.Sources {
			if len(pair.endpoint.Members) > 0 && containsMember(s.Members, pair.endpoint.Members[0]) {
				v.Evidence = append(v.Evidence, crossQuote{pair.role, pair.endpoint.Members[0], s.ID, s.Content})
				break
			}
		}
	}
	return v014JSON(v)
}
func v014DefaultReply(_ context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
	if strings.Contains(r.System, "SOURCE-RELATION GENERATE") {
		return v014JSON(map[string]any{"relations": v014Rows(r)}), nil
	}
	if strings.Contains(r.System, "SOURCE-RELATION COVERAGE") {
		return v014JSON(map[string]any{"complete": true, "supported": true, "reason": "all explicit relations covered"}), nil
	}
	return v014Supported(r), nil
}

// 原故障只看节点详述，遗漏边不会被发现；现在全文覆盖反馈修复失败来源且不重跑成功兄弟。
func TestV014SourceCoverageRepairRetainsSibling(t *testing.T) {
	res, docs, originals := v014Fixture(2)
	generation := map[string]int{}
	c := &v014RelationClient{}
	c.reply = func(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
		var in relationProcessInput
		_ = json.Unmarshal([]byte(r.User), &in)
		if strings.Contains(r.System, "SOURCE-RELATION GENERATE") {
			generation[in.FocusSourceID]++
			if !strings.Contains(r.User, "all detail a1") || !strings.Contains(r.User, originals[1].Content) {
				t.Fatal("complete originals/member material missing")
			}
			if in.FocusSourceID == "s2" && generation["s2"] == 1 {
				return v014JSON(map[string]any{"relations": []Relation{}}), nil
			}
			if in.FocusSourceID == "s2" && (!strings.Contains(in.Feedback, "missing qualified dependency") || len(in.Candidates) != 0) {
				t.Fatal("revision missing original candidate or critique")
			}
		}
		if strings.Contains(r.System, "SOURCE-RELATION COVERAGE") && in.FocusSourceID == "s2" && len(in.Candidates) == 0 {
			return v014JSON(map[string]any{"complete": false, "supported": true, "reason": "omitted explicit edge", "critique": "missing qualified dependency"}), nil
		}
		return v014DefaultReply(ctx, r)
	}
	rpt := NewExtractor(c, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
	if len(rpt.Selected) != 2 || generation["s1"] != 1 || generation["s2"] != 2 || len(rpt.Scopes) != 2 {
		t.Fatalf("selection/scopes: %+v generations=%v", rpt, generation)
	}
	for _, scope := range rpt.Scopes {
		if scope.Outcome != annotation.DocumentComplete || scope.MaterialID == "" || scope.CandidateID == "" || scope.Exhausted {
			t.Fatalf("scope: %+v", scope)
		}
	}
	for _, d := range rpt.Selected {
		if d.Classification != "explicit" || len(d.Evidence) != 2 || d.CandidateID == "" || d.MaterialID == "" {
			t.Fatalf("decision: %+v", d)
		}
		for _, ev := range d.Evidence {
			var text string
			for _, s := range d.Sources {
				if s.ID == ev.SourceID {
					for _, o := range originals {
						if o.SHA256 == s.SHA256 {
							text = o.Content
						}
					}
				}
			}
			if text[ev.StartByte:ev.EndByte] != ev.Quote {
				t.Fatal("quote not original bytes")
			}
		}
	}
}

// 未知检查必须重核同一候选，预算不能在核验/修订之间重新开始。
func TestV014UnknownReassessesSameCandidateAndExhaustion(t *testing.T) {
	for _, recoverable := range []bool{false, true} {
		t.Run(fmt.Sprint(recoverable), func(t *testing.T) {
			res, docs, originals := v014Fixture(2)
			c := &v014RelationClient{}
			var unknownRequests []llm.CompleteRequest
			generations := 0
			c.reply = func(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if strings.Contains(r.System, "SOURCE-RELATION GENERATE") {
					generations++
				}
				if !strings.Contains(r.System, "SOURCE-RELATION") && strings.Contains(r.User, "ma2") {
					unknownRequests = append(unknownRequests, r)
					if !recoverable || len(unknownRequests) == 1 {
						return v014JSON(map[string]any{"disposition": "insufficient_evidence", "reason": "unknown"}), nil
					}
				}
				return v014DefaultReply(ctx, r)
			}
			rpt := NewExtractor(c, 1000).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
			if generations != 2 || len(unknownRequests) != 2 || !reflect.DeepEqual(unknownRequests[0], unknownRequests[1]) {
				t.Fatalf("failed step replay: %d %d", generations, len(unknownRequests))
			}
			want := 1
			if recoverable {
				want = 2
			}
			if len(rpt.Selected) != want || len(res.Domains[0].Subdomains[0].Relations) != 1 {
				t.Fatalf("siblings lost: %+v", rpt)
			}
			last := rpt.Scopes[1]
			if recoverable && last.Outcome != annotation.DocumentComplete {
				t.Fatalf("not recovered: %+v", last)
			}
			if !recoverable && (last.Outcome != annotation.DocumentUnavailable || !last.Exhausted) {
				t.Fatalf("unknown falsely certified: %+v", last)
			}
		})
	}
}

// 同三元组的时间/条件差异原先被三元组去重静默抹除。
func TestV014QualifierDedupAndDanglingRecovery(t *testing.T) {
	res, docs, originals := v014Fixture(1)
	originals[0] = FreezeSource(originals[0].Skill, originals[0].Path, originals[0].Content+" 2025 source disagrees: bypass on has no dependency.")
	sd := &res.Domains[0].Subdomains[0]
	sd.Relations = []Relation{{Source: "a1", Target: "missing-b", Kind: "depends_on", Description: "old invalid alias"}}
	c := &v014RelationClient{}
	c.reply = func(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if strings.Contains(r.System, "SOURCE-RELATION GENERATE") {
			rows := v014Rows(r)
			other := rows[0]
			other.Description = "2025 source disagrees: bypass on has no dependency"
			rows = append(rows, other, rows[0])
			return v014JSON(map[string]any{"relations": rows}), nil
		}
		return v014DefaultReply(ctx, r)
	}
	e := NewExtractor(c, 1000)
	rpt := e.ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
	if len(sd.Relations) != 2 || len(rpt.Selected) != 2 || rpt.Decisions[0].Classification != "unknown" || rpt.Decisions[0].Disposition != "rejected_endpoint" {
		t.Fatalf("dangling/facets: %+v %+v", sd.Relations, rpt)
	}
	rpt = e.ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
	if len(sd.Relations) != 2 || len(rpt.Selected) != 2 {
		t.Fatalf("repeat growth: %+v", rpt)
	}
}

func TestV014UnsupportedAndUnknownCannotCertify(t *testing.T) {
	for _, mode := range []string{"inferred", "unknown", "invented-quote", "wrong-direction"} {
		t.Run(mode, func(t *testing.T) {
			res, docs, originals := v014Fixture(1)
			c := &v014RelationClient{}
			c.reply = func(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if strings.Contains(r.System, "SOURCE-RELATION") {
					return v014DefaultReply(ctx, r)
				}
				response := v014Supported(r)
				var v crossVerdict
				_ = json.Unmarshal([]byte(response.Text), &v)
				no := false
				switch mode {
				case "inferred":
					v.ExplicitSourceAssertion = &no
				case "unknown":
					v.ExplicitSourceAssertion = nil
				case "invented-quote":
					v.Evidence[0].Quote = "generated summary is not evidence"
				case "wrong-direction":
					v.DirectionSupported = &no
				}
				return v014JSON(v), nil
			}
			rpt := NewExtractor(c, 1000).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
			if len(rpt.Selected) != 0 || len(res.Domains[0].Subdomains[0].Relations) != 0 || rpt.Scopes[0].Outcome == annotation.DocumentComplete || !rpt.Scopes[0].Exhausted {
				t.Fatalf("false source credit: %+v", rpt)
			}
		})
	}
}

// 缺原文的旧接口保留原内容，但不能生成新机器边或声称原文核验成功。
func TestV014LegacyMissingOriginalsAndCancellation(t *testing.T) {
	res, docs, originals := v014Fixture(1)
	sd := &res.Domains[0].Subdomains[0]
	sd.Relations = []Relation{{Source: "a1", Target: "missing", Kind: "references", Description: "retain candidate"}}
	before := append([]Relation(nil), sd.Relations...)
	c := &v014RelationClient{reply: v014DefaultReply}
	e := NewExtractor(c, 1000)
	legacy := e.IdentifyRelations(context.Background(), res)
	repair := e.RepairDanglingRelations(context.Background(), res, docs)
	if len(c.requests) != 0 || len(legacy.Failures) == 0 || !repair.Degraded || repair.Cleaned != 0 || !reflect.DeepEqual(before, sd.Relations) {
		t.Fatal("source-less helper modified/certified candidates")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rpt := e.ProcessRelations(ctx, res, docs, originals, RelationProcessOptions{})
	if len(c.requests) != 0 || rpt.Scopes[0].Outcome != annotation.DocumentCancelled || len(rpt.Selected) != 0 {
		t.Fatalf("cancellation: %+v", rpt)
	}
}

func TestV014QualifiedEndpointSurvivesUUID(t *testing.T) {
	res, docs, originals := v014Fixture(2)
	res.Domains[0].Subdomains[1].Entities[1].ID = "b1"
	originals[0] = FreezeSource(originals[0].Skill, originals[0].Path, originals[0].Content+" a1 explicitly references b2 in the second source.")
	c := &v014RelationClient{}
	c.reply = func(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if strings.Contains(r.System, "SOURCE-RELATION GENERATE") {
			var in relationProcessInput
			_ = json.Unmarshal([]byte(r.User), &in)
			if in.FocusSourceID == "s1" {
				return v014JSON(map[string]any{"relations": []Relation{{Source: "r1", Target: "r4", Kind: "references", Description: "explicit cross source link", Confidence: .9}}}), nil
			}
			return v014JSON(map[string]any{"relations": []Relation{}}), nil
		}
		return v014DefaultReply(ctx, r)
	}
	rpt := NewExtractor(c, 1000).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
	if len(rpt.Selected) != 1 {
		t.Fatalf("scope collision: %+v", rpt)
	}
	AssignNodeUUIDs(res.Domains)
	rel := res.Domains[0].Subdomains[0].Relations[0]
	if rel.Target != NodeUUID([]string{"mb2"}) || rel.Source != NodeUUID([]string{"ma1"}) {
		t.Fatalf("wrong scoped binding: %+v", rel)
	}
}

// 肯定布尔与遗漏反馈不能同时认证，错误反馈须保持同一候选重新核验。
func TestV014ContradictoryCoverageAndMalformedGenerationStayUnknown(t *testing.T) {
	for _, mode := range []string{"coverage", "generation"} {
		t.Run(mode, func(t *testing.T) {
			res, docs, originals := v014Fixture(1)
			c := &v014RelationClient{}
			var failed []llm.CompleteRequest
			c.reply = func(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if mode == "coverage" && strings.Contains(r.System, "SOURCE-RELATION COVERAGE") {
					failed = append(failed, r)
					return v014JSON(map[string]any{"complete": true, "supported": true, "reason": "contradictory", "critique": "missing required edge"}), nil
				}
				if mode == "generation" && strings.Contains(r.System, "SOURCE-RELATION GENERATE") {
					failed = append(failed, r)
					return &llm.CompleteResponse{Text: "{", FinishReason: "stop"}, nil
				}
				return v014DefaultReply(ctx, r)
			}
			rpt := NewExtractor(c, 1000).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
			if rpt.Scopes[0].Outcome != annotation.DocumentUnavailable || !rpt.Scopes[0].Exhausted || len(rpt.Selected) != 0 || len(failed) != 2 {
				t.Fatalf("malformed certification: %+v", rpt)
			}
			if mode == "coverage" && !reflect.DeepEqual(failed[0], failed[1]) {
				t.Fatal("malformed coverage changed candidate")
			}
		})
	}
}

// 实际生产客户端的永久配置错误不得消耗恢复预算；不是伪造错误字符串。
func TestV014ProductionClientPermanentEndpointStops(t *testing.T) {
	for _, stage := range []string{"generation", "coverage", "verify"} {
		t.Run(stage, func(t *testing.T) {
			res, docs, originals := v014Fixture(1)
			production, err := llm.NewOpenAICompatClient(llm.Config{Endpoint: "ftp://invalid.local", APIKeyEnv: "CAIRN_V014_UNUSED_KEY", MaxRetries: -1})
			if err != nil {
				t.Fatal(err)
			}
			c := &v014RelationClient{}
			c.reply = func(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
				match := stage == "generation" && strings.Contains(r.System, "SOURCE-RELATION GENERATE") || stage == "coverage" && strings.Contains(r.System, "SOURCE-RELATION COVERAGE") || stage == "verify" && !strings.Contains(r.System, "SOURCE-RELATION")
				if match {
					return production.Complete(ctx, r)
				}
				return v014DefaultReply(ctx, r)
			}
			rpt := NewExtractor(c, 1000).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 3})
			scope := rpt.Scopes[0]
			if scope.Outcome != annotation.DocumentUnavailable || scope.Exhausted || len(rpt.Selected) != 0 || scope.Attempts[len(scope.Attempts)-1].Outcome != "permanent" {
				t.Fatalf("permanent config retried/certified: %+v", rpt)
			}
			want := 1
			if stage == "coverage" {
				want = 2
			}
			if stage == "verify" {
				want = 3
			}
			if len(c.requests) != want {
				t.Fatalf("requests=%d want=%d", len(c.requests), want)
			}
		})
	}
}

// 同一旧候选与新候选不能各拿一份预算；永久错误也不能换阶段重放。
func TestV014SuppliedCandidateSharesSourceBudgetAndPermanentStop(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(fmt.Sprint(permanent), func(t *testing.T) {
			res, docs, originals := v014Fixture(1)
			res.Domains[0].Subdomains[0].Relations = []Relation{{Source: "a1", Target: "b1", Kind: "depends_on", Description: "only when bypass is off; when on no dependency", Confidence: .9}}
			c := &v014RelationClient{}
			assessments := 0
			c.reply = func(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if permanent {
					return nil, fmt.Errorf("openai_compatible: missing API key")
				}
				if !strings.Contains(r.System, "SOURCE-RELATION") {
					assessments++
					return v014JSON(map[string]any{"disposition": "insufficient_evidence", "reason": "unknown"}), nil
				}
				return v014DefaultReply(ctx, r)
			}
			rpt := NewExtractor(c, 1000).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
			if permanent && len(c.requests) != 1 || !permanent && assessments != 2 || len(rpt.Selected) != 0 {
				t.Fatalf("budget reset/dependent replay: calls=%d assessments=%d report=%+v", len(c.requests), assessments, rpt)
			}
		})
	}
}

// 接受完整修订后只缓存仍在集合中的精确候选；旧宽泛版本只能留在历史 Decisions。
func TestV014CompleteRevisionWithdrawsChangedSelectionKeepsExactSibling(t *testing.T) {
	res, docs, originals := v014Fixture(1)
	sd := &res.Domains[0].Subdomains[0]
	sd.Entities = append(sd.Entities, Node{ID: "c1", Name: "c1", Members: []string{"ma1"}}, Node{ID: "d1", Name: "d1", Members: []string{"mb1"}})
	c := &v014RelationClient{}
	generation := 0
	c.reply = func(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if strings.Contains(r.System, "SOURCE-RELATION GENERATE") {
			generation++
			rows := v014Rows(r)
			if generation == 1 {
				rows[0].Description = "overbroad dependency"
			}
			rows = append(rows, Relation{Source: "r3", Target: "r4", Kind: "references", Description: "second assertion", Confidence: .9})
			return v014JSON(map[string]any{"relations": rows}), nil
		}
		if !strings.Contains(r.System, "SOURCE-RELATION") {
			var in crossNeighborhood
			_ = json.Unmarshal([]byte(r.User), &in)
			if generation == 1 && in.Relation.Source == "c1" {
				return v014JSON(map[string]any{"disposition": "rejected", "reason": "repair whole set: preceding dependency lost bypass condition"}), nil
			}
		}
		return v014DefaultReply(ctx, r)
	}
	rpt := NewExtractor(c, 1000).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
	if generation != 2 || len(rpt.Selected) != 2 || len(sd.Relations) != 2 {
		t.Fatalf("replacement set: %+v", rpt)
	}
	for _, r := range sd.Relations {
		if r.Description == "overbroad dependency" {
			t.Fatal("superseded assertion survived accepted replacement")
		}
	}
	foundHistory := false
	for _, d := range rpt.Decisions {
		if d.Relation.Description == "overbroad dependency" {
			foundHistory = true
		}
	}
	if !foundHistory {
		t.Fatal("superseded evidence history was erased")
	}
}

// 延迟旧候选核验的暂时未知恢复后不能残留共享 unavailable，也不能重置剩余预算。
func TestV014RecoveredDeferredAuditRestoresFactualScopeWithoutBudgetReset(t *testing.T) {
	for _, secondUnknown := range []bool{false, true} {
		t.Run(fmt.Sprint(secondUnknown), func(t *testing.T) {
			res, docs, originals := v014Fixture(1)
			res.Domains[0].Subdomains[0].Relations = []Relation{{Source: "a1", Target: "b1", Kind: "depends_on", Description: "first retained old assertion", Confidence: .9}, {Source: "a1", Target: "b1", Kind: "references", Description: "second retained old assertion", Confidence: .9}}
			c := &v014RelationClient{}
			calls := map[string]int{}
			c.reply = func(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if strings.Contains(r.System, "SOURCE-RELATION GENERATE") {
					return v014JSON(map[string]any{"relations": []Relation{}}), nil
				}
				if !strings.Contains(r.System, "SOURCE-RELATION") {
					var in crossNeighborhood
					_ = json.Unmarshal([]byte(r.User), &in)
					calls[in.Relation.Kind]++
					if in.Relation.Kind == "depends_on" && calls[in.Relation.Kind] == 1 || in.Relation.Kind == "references" && secondUnknown {
						return v014JSON(map[string]any{"disposition": "insufficient_evidence", "reason": "temporarily unknown"}), nil
					}
				}
				return v014DefaultReply(ctx, r)
			}
			rpt := NewExtractor(c, 1000).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: 1})
			if calls["depends_on"] != 2 || calls["references"] != 1 {
				t.Fatalf("deferred sibling skipped or budget reset: %v %+v", calls, rpt)
			}
			scope := rpt.Scopes[0]
			if !secondUnknown && (scope.Outcome != annotation.DocumentComplete || scope.Reason != "" || scope.Exhausted) {
				t.Fatalf("recovered audit left stale state: %+v", scope)
			}
			if secondUnknown && (scope.Outcome != annotation.DocumentUnavailable || !scope.Exhausted) {
				t.Fatalf("shared budget was reset: %+v", scope)
			}
		})
	}
}
