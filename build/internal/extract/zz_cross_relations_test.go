package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type crossFixtureClient struct {
	calls atomic.Int64
	reply func(llm.CompleteRequest) (*llm.CompleteResponse, error)
}

func (c *crossFixtureClient) ProviderName() string { return "offline-cross-fixture" }
func (c *crossFixtureClient) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.calls.Add(1)
	return c.reply(req)
}

func crossFixture() (*Result, []*dktypes.AnnotatedDocument, []FrozenSource) {
	res := &Result{Domains: []Domain{{Name: "Engineering", Slug: "engineering", Subdomains: []Subdomain{
		{Name: "Admission", Slug: "admission", Entities: []Node{{ID: "admission", Name: "Admission", Members: []string{"member-admission"}, Confidence: .95}}, Relations: []Relation{{Source: "admission", Target: "quota", Kind: "depends_on", Confidence: .9}}},
		{Name: "Capacity", Slug: "capacity", Concepts: []Node{{ID: "quota", Name: "Quota", Members: []string{"member-quota"}, Confidence: .95}}},
	}}}}
	docs := []*dktypes.AnnotatedDocument{
		{Skill: "system", FilePath: "references/admission.md", Items: []dktypes.AnnotatedItem{{ID: "member-admission", Detail: "Generated admission detail is not original evidence."}}},
		{Skill: "system", FilePath: "references/quota.md", Items: []dktypes.AnnotatedItem{{ID: "member-quota", Detail: "Generated quota detail is not original evidence."}}},
	}
	return res, docs, []FrozenSource{
		FreezeSource("system", docs[0].FilePath, "# Admission\nAdmission requires an available quota before accepting work.\n"),
		FreezeSource("system", docs[1].FilePath, "# Quota\nQuota is the prerequisite for admission in version 2.\n"),
	}
}

func supportedCrossVerdict() crossVerdict {
	b := true
	return crossVerdict{Disposition: "supported", Reason: "Admission requires quota, in that direction.", RelationSupported: &b, DirectionSupported: &b,
		Evidence: []crossQuote{{Endpoint: "source", MemberID: "member-admission", SourceID: "s1", Quote: "Admission requires an available quota before accepting work."},
			{Endpoint: "target", MemberID: "member-quota", SourceID: "s2", Quote: "Quota is the prerequisite for admission in version 2."}}}
}

func crossReply(v crossVerdict) func(llm.CompleteRequest) (*llm.CompleteResponse, error) {
	return func(llm.CompleteRequest) (*llm.CompleteResponse, error) {
		data, _ := json.Marshal(v)
		return &llm.CompleteResponse{Text: string(data), FinishReason: "stop"}, nil
	}
}

// 原故障是跨域目标被 repair 当成漏声明、UUID 只看已遍历的子域；顺序改变不应改变端点绑定。
func TestCrossCandidateSurvivesRepairAndUUIDOrder(t *testing.T) {
	t.Parallel()
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			res, docs, originals := crossFixture()
			if reverse {
				res.Domains[0].Subdomains[0], res.Domains[0].Subdomains[1] = res.Domains[0].Subdomains[1], res.Domains[0].Subdomains[0]
			}
			client := &crossFixtureClient{reply: crossReply(supportedCrossVerdict())}
			e := NewExtractor(client, 4096)
			repair := e.RepairDanglingRelations(context.Background(), res, docs)
			if repair.DanglingFound != 0 || client.calls.Load() != 0 {
				t.Fatalf("cross candidate treated as dangling: %+v", repair)
			}
			rpt := e.ValidateCrossRelations(context.Background(), res, docs, originals, CrossRelationOptions{})
			if rpt.Candidates != 1 || rpt.Accepted != 1 {
				t.Fatalf("validation: %+v", rpt)
			}
			AssignNodeUUIDs(res.Domains)
			sd := findSubdomain(res.Domains, "engineering", "admission")
			if len(sd.Relations) != 1 || sd.Relations[0].Source != NodeUUID([]string{"member-admission"}) || sd.Relations[0].Target != NodeUUID([]string{"member-quota"}) {
				t.Fatalf("wrong binding: %+v", sd.Relations)
			}
			decision := rpt.Decisions[0]
			for _, evidence := range decision.Evidence {
				var original FrozenSource
				for _, source := range decision.Sources {
					if source.ID == evidence.SourceID {
						for _, candidate := range originals {
							if candidate.SHA256 == source.SHA256 {
								original = candidate
							}
						}
					}
				}
				if original.Content[evidence.StartByte:evidence.EndByte] != evidence.Quote || evidence.StartLine != 2 || evidence.EndLine != 2 {
					t.Fatalf("positions not computed from originals: %+v", evidence)
				}
			}
		})
	}
}

// 重名不得靠最后写入获胜；局部唯一的旧行为优先于全局同名，否则必须全局唯一。
func TestEndpointAliasResolutionFailsClosed(t *testing.T) {
	t.Parallel()
	res, _, _ := crossFixture()
	d := &res.Domains[0]
	d.Subdomains = append(d.Subdomains, Subdomain{Slug: "other", Entities: []Node{{ID: "quota", Members: []string{"other-member"}}}})
	index := indexEndpoints(res.Domains)
	if _, state := index.resolve(endpointScope{"engineering", "admission"}, "quota"); state != "ambiguous" {
		t.Fatalf("global duplicate: %s", state)
	}
	if ref, state := index.resolve(endpointScope{"engineering", "capacity"}, "quota"); state != "resolved" || ref.node.Members[0] != "member-quota" {
		t.Fatalf("local compatibility: %+v %s", ref, state)
	}
	if _, state := index.resolve(endpointScope{"engineering", "admission"}, "absent"); state != "missing" {
		t.Fatalf("missing: %s", state)
	}
	d.Subdomains[0].Entities = append(d.Subdomains[0].Entities, d.Subdomains[0].Entities[0])
	if _, state := indexEndpoints(res.Domains).resolve(endpointScope{"engineering", "admission"}, "admission"); state != "ambiguous" {
		t.Fatalf("local duplicate: %s", state)
	}
	failures := scanDangling(res.Domains)
	if len(failures) != 1 || failures[0].SourceStatus != "ambiguous" || failures[0].TargetStatus != "ambiguous" {
		t.Fatalf("missing disposition detail: %+v", failures)
	}
	AssignNodeUUIDs(res.Domains)
	if len(d.Subdomains[0].Relations) != 0 {
		t.Fatal("ambiguous relation survived UUID translation")
	}
}

// 端点解析不是语义验证：伪造引用、缺判定、缺前提方向与不同时间的断言都必须拒绝。
func TestCrossVerifierRejectionMatrix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, want string
		kind       string
		change     func(*crossVerdict)
	}{
		{"invented-quote", "rejected_evidence", "depends_on", func(v *crossVerdict) { v.Evidence[0].Quote = "Generated admission detail is not original evidence." }},
		{"wrong-member-owner", "rejected_evidence", "depends_on", func(v *crossVerdict) { v.Evidence[0].MemberID = "member-quota" }},
		{"invented-source", "rejected_evidence", "depends_on", func(v *crossVerdict) { v.Evidence[0].SourceID = "s999" }},
		{"one-endpoint-only", "rejected_evidence", "depends_on", func(v *crossVerdict) { v.Evidence = v.Evidence[:1] }},
		{"incomplete-direction", "invalid_verdict", "depends_on", func(v *crossVerdict) { v.DirectionSupported = nil }},
		{"wrong-direction", "rejected_semantic", "depends_on", func(v *crossVerdict) { b := false; v.DirectionSupported = &b }},
		{"unsupported-relation", "rejected_semantic", "references", func(v *crossVerdict) {
			v.Disposition = "rejected"
			v.Reason = "No explicit reference in the original source."
		}},
		{"absence-not-contradiction", "rejected_semantic", "contradicts", func(v *crossVerdict) { b, f := true, false; v.Contradiction = &crossContradiction{&b, &b, &b, &b, &f} }},
		{"different-time", "rejected_semantic", "contradicts", func(v *crossVerdict) { b, f := true, false; v.Contradiction = &crossContradiction{&b, &b, &f, &b, &b} }},
		{"different-conditions", "rejected_semantic", "contradicts", func(v *crossVerdict) { b, f := true, false; v.Contradiction = &crossContradiction{&b, &b, &b, &f, &b} }},
		{"incomplete-contradiction", "invalid_verdict", "contradicts", func(v *crossVerdict) {}},
		{"insufficient-source", "insufficient_evidence", "depends_on", func(v *crossVerdict) { v.Disposition = "insufficient_evidence" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, docs, sources := crossFixture()
			res.Domains[0].Subdomains[0].Relations[0].Kind = tc.kind
			v := supportedCrossVerdict()
			tc.change(&v)
			e := NewExtractor(&crossFixtureClient{reply: crossReply(v)}, 4096)
			rpt := e.ValidateCrossRelations(context.Background(), res, docs, sources, CrossRelationOptions{})
			if rpt.Candidates != 1 || rpt.Accepted != 0 || rpt.Decisions[0].Disposition != tc.want || len(res.Domains[0].Subdomains[0].Relations) != 0 {
				t.Fatalf("%s accepted or misclassified: %+v", tc.name, rpt)
			}
		})
	}
}

func TestCrossVerifierEvidenceCapacityAndFailures(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"overflow", "missing-original", "corrupt-hash", "conflicting-source", "missing-member", "cancelled", "llm-error", "malformed", "nil-response", "truncated-response", "invalid-utf8"} {
		t.Run(name, func(t *testing.T) {
			res, docs, sources := crossFixture()
			opts := CrossRelationOptions{}
			ctx := context.Background()
			want := "insufficient_evidence"
			client := &crossFixtureClient{reply: crossReply(supportedCrossVerdict())}
			switch name {
			case "overflow":
				opts.MaxEvidenceBytes = 16
			case "missing-original":
				sources = sources[:1]
			case "corrupt-hash":
				sources[0].SHA256 = strings.Repeat("0", 64)
			case "conflicting-source":
				sources = append(sources, FreezeSource(sources[0].Skill, sources[0].Path, "different bytes"))
			case "missing-member":
				docs[0].Items = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = "execution_error"
			case "llm-error":
				client.reply = func(llm.CompleteRequest) (*llm.CompleteResponse, error) {
					return nil, errors.New("backend unavailable")
				}
				want = "execution_error"
			case "malformed":
				client.reply = func(llm.CompleteRequest) (*llm.CompleteResponse, error) {
					return &llm.CompleteResponse{Text: "{broken"}, nil
				}
				want = "invalid_verdict"
			case "invalid-utf8":
				sources[0] = FreezeSource(sources[0].Skill, sources[0].Path, string([]byte{0xff, 0xfe}))
			case "truncated-response":
				client.reply = func(req llm.CompleteRequest) (*llm.CompleteResponse, error) {
					r, e := crossReply(supportedCrossVerdict())(req)
					r.FinishReason = "length"
					return r, e
				}
				want = "invalid_verdict"
			case "nil-response":
				client.reply = func(llm.CompleteRequest) (*llm.CompleteResponse, error) { return nil, nil }
				want = "execution_error"
			}
			rpt := NewExtractor(client, 4096).ValidateCrossRelations(ctx, res, docs, sources, opts)
			if rpt.Decisions[0].Disposition != want || rpt.Accepted != 0 {
				t.Fatalf("%s: %+v", name, rpt)
			}
			if name != "llm-error" && name != "malformed" && name != "nil-response" && name != "truncated-response" && client.calls.Load() != 0 {
				t.Fatal("unverifiable/oversized source reached model")
			}
		})
	}
}

// 旧单子域行为不可因新阶段新增调用或丢失边；候选数量也不应被任意64条上限裁剪。
func TestCrossVerifierKeepsLocalAndProcessesAllCandidates(t *testing.T) {
	t.Parallel()
	res, docs, originals := crossFixture()
	sd := &res.Domains[0].Subdomains[0]
	sd.Entities = append(sd.Entities, Node{ID: "local", Members: []string{"local-member"}})
	local := Relation{Source: "admission", Target: "local", Kind: "references", Description: "local behavior"}
	sd.Relations = append(sd.Relations, local)
	client := &crossFixtureClient{reply: crossReply(supportedCrossVerdict())}
	for i := 0; i < 69; i++ {
		sd.Relations = append(sd.Relations, sd.Relations[0])
	}
	rpt := NewExtractor(client, 4096).ValidateCrossRelations(context.Background(), res, docs, originals, CrossRelationOptions{MaxEvidenceBytes: 128 * 1024})
	if rpt.Candidates != 70 || rpt.Accepted != 70 || client.calls.Load() != 70 || len(sd.Relations) != 71 || !reflect.DeepEqual(sd.Relations[1], local) {
		t.Fatalf("candidate denominator/local behavior lost: %+v calls=%d", rpt, client.calls.Load())
	}
}

func TestCrossVerifierPromptBindsOriginalBytesOnly(t *testing.T) {
	t.Parallel()
	res, docs, originals := crossFixture()
	client := &crossFixtureClient{reply: func(req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		var input crossNeighborhood
		if err := json.Unmarshal([]byte(req.User), &input); err != nil {
			return nil, err
		}
		if strings.Contains(req.User, "Generated admission detail") || strings.Contains(req.User, NodeUUID([]string{"member-admission"})) {
			return nil, errors.New("generated detail or UUID leaked")
		}
		if input.Sources[0].SHA256 != originals[0].SHA256 || input.Sources[0].Content != originals[0].Content || !reflect.DeepEqual(input.Sources[0].Members, []string{"member-admission"}) {
			return nil, errors.New("original/member binding missing")
		}
		return crossReply(supportedCrossVerdict())(req)
	}}
	rpt := NewExtractor(client, 4096).ValidateCrossRelations(context.Background(), res, docs, originals, CrossRelationOptions{})
	if rpt.Accepted != 1 {
		t.Fatalf("original source validation failed: %+v", rpt)
	}
}

// 跨 domain 同名 subdomain 不能碰撞；UUID 翻译应使用完整归属而非遍历顺序。
func TestCrossDomainUUIDResolutionRetainsLocalDuplicateAliases(t *testing.T) {
	t.Parallel()
	for _, reverse := range []bool{false, true} {
		res, _, _ := crossFixture()
		res.Domains[0].Subdomains[0].Entities = append(res.Domains[0].Subdomains[0].Entities, Node{ID: "local", Members: []string{"first-local"}})
		res.Domains[0].Subdomains[0].Relations = append(res.Domains[0].Subdomains[0].Relations, Relation{Source: "admission", Target: "local", Kind: "references"})
		remote := res.Domains[0].Subdomains[1]
		remote.Slug = "admission"
		remote.Entities = []Node{{ID: "local", Members: []string{"remote-local"}}}
		res.Domains[0].Subdomains = res.Domains[0].Subdomains[:1]
		res.Domains = append(res.Domains, Domain{Slug: "other-domain", Subdomains: []Subdomain{remote}})
		if reverse {
			res.Domains[0], res.Domains[1] = res.Domains[1], res.Domains[0]
		}
		AssignNodeUUIDs(res.Domains)
		sd := findSubdomain(res.Domains, "engineering", "admission")
		if sd.Relations[0].Target != NodeUUID([]string{"member-quota"}) || sd.Relations[1].Target != NodeUUID([]string{"first-local"}) {
			t.Fatalf("crossdomain/local alias collision: %+v", sd.Relations)
		}
	}
}

// 真正同条件的互斥断言可以通过；校验器不能以一律禁用 contradicts 掩盖语义规则。
func TestCrossVerifierAllowsExplicitSameContextContradiction(t *testing.T) {
	t.Parallel()
	res, docs, sources := crossFixture()
	res.Domains[0].Subdomains[0].Relations[0].Kind = "contradicts"
	sources[0] = FreezeSource(sources[0].Skill, sources[0].Path, "In version 2 under condition X, admission is enabled.\n")
	sources[1] = FreezeSource(sources[1].Skill, sources[1].Path, "In version 2 under condition X, admission is disabled.\n")
	v := supportedCrossVerdict()
	v.Reason = "The originals explicitly assert enabled and disabled for the same version and condition."
	b := true
	v.Contradiction = &crossContradiction{&b, &b, &b, &b, &b}
	v.Evidence[0].Quote = sources[0].Content
	v.Evidence[1].Quote = sources[1].Content
	rpt := NewExtractor(&crossFixtureClient{reply: crossReply(v)}, 4096).ValidateCrossRelations(context.Background(), res, docs, sources, CrossRelationOptions{})
	if rpt.Accepted != 1 {
		t.Fatalf("explicit incompatible claims rejected: %+v", rpt)
	}
	for _, e := range rpt.Decisions[0].Evidence {
		if e.EndLine != 1 {
			t.Fatalf("trailing newline produced invented final line: %+v", e)
		}
	}
}

func TestCrossVerifierReportsEveryUnresolvedCandidate(t *testing.T) {
	t.Parallel()
	res, docs, sources := crossFixture()
	sd := &res.Domains[0].Subdomains[0]
	sd.Relations = append(sd.Relations, Relation{Source: "admission", Target: "missing", Kind: "depends_on"})
	res.Domains[0].Subdomains = append(res.Domains[0].Subdomains, Subdomain{Slug: "duplicate", Concepts: []Node{{ID: "quota", Members: []string{"other"}}}})
	sd = &res.Domains[0].Subdomains[0]
	client := &crossFixtureClient{reply: crossReply(supportedCrossVerdict())}
	rpt := NewExtractor(client, 4096).ValidateCrossRelations(context.Background(), res, docs, sources, CrossRelationOptions{})
	if rpt.Candidates != 2 || rpt.Accepted != 0 || client.calls.Load() != 0 || len(sd.Relations) != 0 {
		t.Fatalf("unresolved candidates hidden: %+v", rpt)
	}
	if rpt.Decisions[0].Disposition != "rejected_endpoint" || !strings.Contains(rpt.Decisions[0].Reason, "ambiguous") || !strings.Contains(rpt.Decisions[1].Reason, "missing") {
		t.Fatalf("endpoint statuses not explicit: %+v", rpt.Decisions)
	}
}

// 原文可能有正常持久化锚点或 UUID 业务事实：R1 仍成立，同时引文可往返到原字节/位置。
func TestCrossVerifierUUIDSourceViewRoundTripAndCollision(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"roundtrip", "invented-alias", "literal-uuid-instead-of-view"} {
		t.Run(mode, func(t *testing.T) {
			res, docs, sources := crossFixture()
			id, other := "11111111-1111-5111-8111-111111111111", "22222222-2222-5222-8222-222222222222"
			member := "member-" + id
			res.Domains[0].Subdomains[0].Entities[0].Members = []string{member}
			docs[0].Items[0].ID = member
			sourceFact := "Admission requires quota " + id + "; literal marker [[cairn-source-id-0:1]] remains data."
			targetFact := "Quota " + id + " is a prerequisite for admission; related id " + other + "."
			sources[0] = FreezeSource("system", docs[0].FilePath, "<!-- kg:node uuid=\""+id+"\" -->\n"+sourceFact+"\n")
			sources[1] = FreezeSource("system", docs[1].FilePath, "# Quota\n"+targetFact+"\n")
			client := &crossFixtureClient{reply: func(req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if sourceUUIDPattern.MatchString(req.User) {
					return nil, errors.New("UUID leaked into source-view prompt")
				}
				var input crossNeighborhood
				if err := json.Unmarshal([]byte(req.User), &input); err != nil {
					return nil, err
				}
				alias := strings.TrimPrefix(input.Source.Members[0], "member-")
				if !strings.HasPrefix(alias, "[[cairn-source-id-1:") || !strings.Contains(input.Sources[0].Content, alias) || !strings.Contains(input.Sources[1].Content, alias) || !strings.Contains(input.Sources[0].Content, "[[cairn-source-id-0:1]]") {
					return nil, errors.New("UUID alias inconsistent or collided with original literal")
				}
				v := supportedCrossVerdict()
				v.Evidence[0].MemberID = input.Source.Members[0]
				v.Evidence[0].Quote = strings.Split(input.Sources[0].Content, "\n")[1]
				v.Evidence[1].Quote = strings.Split(input.Sources[1].Content, "\n")[1]
				if mode == "invented-alias" {
					v.Evidence[0].Quote = strings.Replace(v.Evidence[0].Quote, alias, "[[cairn-source-id-1:999]]", 1)
				}
				if mode == "literal-uuid-instead-of-view" {
					v.Evidence[0].Quote = sourceFact
				}
				return crossReply(v)(req)
			}}
			rpt := NewExtractor(client, 4096).ValidateCrossRelations(context.Background(), res, docs, sources, CrossRelationOptions{})
			if mode != "roundtrip" {
				if rpt.Accepted != 0 || rpt.Decisions[0].Disposition != "rejected_evidence" {
					t.Fatalf("invented/colliding view accepted: %+v", rpt)
				}
				return
			}
			if rpt.Accepted != 1 {
				t.Fatalf("normal managed sources rejected: %+v", rpt)
			}
			evidence := rpt.Decisions[0].Evidence
			if evidence[0].Quote != sourceFact || evidence[0].MemberID != member || evidence[1].Quote != targetFact || sources[0].Content[evidence[0].StartByte:evidence[0].EndByte] != sourceFact {
				t.Fatalf("UUID view did not bind original byte offsets: %+v", evidence)
			}
			if rpt.Decisions[0].Sources[0].SHA256 != sources[0].SHA256 {
				t.Fatal("source hash changed during aliasing")
			}
		})
	}
}

func TestUUIDSourceViewRejectsMalformedAndInventedAliases(t *testing.T) {
	t.Parallel()
	id := "AAAAAAAA-AAAA-5AAA-8AAA-AAAAAAAAAAAA"
	masked, view := makeUUIDSourceView("fact " + id + " repeated " + id)
	if sourceUUIDPattern.MatchString(masked) || strings.Count(masked, view.aliasByLiteral[id]) != 2 {
		t.Fatalf("inconsistent UUID view: %s", masked)
	}
	decoded, ok := view.decode(masked)
	if !ok || decoded != "fact "+id+" repeated "+id {
		t.Fatalf("case/byte spelling lost: %s", decoded)
	}
	for _, invented := range []string{view.prefix + "999]]", view.prefix + "1", view.prefix + "1]]extra" + view.prefix + "nope]]"} {
		if _, ok := view.decode(invented); ok {
			t.Fatalf("invented/ambiguous alias decoded: %q", invented)
		}
	}
}

// 即使后端返回完整 supported JSON，阶段已取消也不能把迟到响应记成语义成功。
func TestCrossVerifierRejectsResponseAfterContextCancellation(t *testing.T) {
	t.Parallel()
	res, docs, sources := crossFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &crossFixtureClient{reply: func(req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		cancel()
		return crossReply(supportedCrossVerdict())(req)
	}}
	rpt := NewExtractor(client, 4096).ValidateCrossRelations(ctx, res, docs, sources, CrossRelationOptions{})
	if client.calls.Load() != 1 || rpt.Accepted != 0 || rpt.Decisions[0].Disposition != "execution_error" || len(res.Domains[0].Subdomains[0].Relations) != 0 {
		t.Fatalf("late verifier response accepted after cancellation: %+v", rpt)
	}
}

// 纯 seam 不能从假的 Complete 调用反推请求；请求逐字段等于既有完整原文邻域。
// The logical request remains byte-for-byte identical to the existing prompt and source view.
func TestCrossRelationPlanRequestAndLegacyParity(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"supported", "rejected-direction", "bad-quote", "malformed", "truncated", "nil-response", "execution-error", "missing-original", "overflow", "unresolved"} {
		t.Run(mode, func(t *testing.T) {
			base, docs, originals := crossFixture()
			opts := CrossRelationOptions{}
			v := supportedCrossVerdict()
			var executionErr error
			var response *llm.CompleteResponse
			switch mode {
			case "rejected-direction":
				b := false
				v.DirectionSupported = &b
			case "bad-quote":
				v.Evidence[0].Quote = "generated prose is not original evidence"
			case "missing-original":
				originals = originals[:1]
			case "overflow":
				opts.MaxEvidenceBytes = 16
			case "unresolved":
				base.Domains[0].Subdomains[0].Relations[0].Target = "missing"
			}
			response, _ = crossReply(v)(llm.CompleteRequest{})
			switch mode {
			case "malformed":
				response.Text = "{broken"
			case "truncated":
				response.FinishReason = "length"
			case "nil-response":
				response = nil
			case "execution-error":
				executionErr = errors.New("offline verifier unavailable")
			}
			plan := BuildCrossRelationPlan(base, docs, originals, opts, 8192)
			works := plan.Works()
			if len(works) != 1 {
				t.Fatal("lost actual candidate")
			}
			work := works[0]
			request, requestable := work.Request()
			if mode == "supported" {
				expected := crossNeighborhood{Source: CrossEndpoint{"engineering", "admission", "admission", "Admission", []string{"member-admission"}}, Target: CrossEndpoint{"engineering", "capacity", "quota", "Quota", []string{"member-quota"}}, Relation: base.Domains[0].Subdomains[0].Relations[0], Sources: []crossOriginal{{CrossSource: CrossSource{"s1", "system", docs[0].FilePath, originals[0].SHA256}, Content: originals[0].Content, Members: []string{"member-admission"}}, {CrossSource: CrossSource{"s2", "system", docs[1].FilePath, originals[1].SHA256}, Content: originals[1].Content, Members: []string{"member-quota"}}}}
				payload, _ := json.Marshal(expected)
				want := llm.CompleteRequest{System: crossRelationSystemPrompt, User: string(payload), MaxTokens: 8192}
				if !requestable || !reflect.DeepEqual(request, want) {
					t.Fatal("request/prompt/full source recipe changed")
				}
				if work.Descriptor().RequestSHA256 != crossIdentityHash(want) || work.Descriptor().NeighborhoodSHA256 != crossIdentityHash(expected) {
					t.Fatal("logical request/neighborhood identity mismatch")
				}
			}
			eval := work.Evaluate(response, executionErr)
			pure, report, err := plan.Apply(base, []*CrossRelationEvaluation{eval})
			if err != nil {
				t.Fatal(err)
			}
			legacy := cloneCrossResult(base)
			client := &crossFixtureClient{reply: func(req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if !requestable || !reflect.DeepEqual(req, request) {
					t.Error("legacy used a different request")
				}
				return response, executionErr
			}}
			legacyReport := NewExtractor(client, 8192).ValidateCrossRelations(context.Background(), legacy, docs, originals, opts)
			if !reflect.DeepEqual(pure, legacy) || !reflect.DeepEqual(report, legacyReport) {
				t.Fatalf("pure/wrapper behavior differs: pure=%+v legacy=%+v", report, legacyReport)
			}
			if requestable != (client.calls.Load() == 1) {
				t.Fatal("terminal preflight reached client or requestable work was skipped")
			}
		})
	}
}

func TestCrossRelationPlanCompleteInventoryAndIndependentDuplicateSlots(t *testing.T) {
	t.Parallel()
	base, docs, originals := crossFixture()
	sd := &base.Domains[0].Subdomains[0]
	sd.Entities = append(sd.Entities, Node{ID: "local", Members: []string{"member-local"}})
	local := Relation{Source: "admission", Target: "local", Kind: "references", Description: "noncandidate local relation"}
	sd.Relations = append(sd.Relations, sd.Relations[0], Relation{Source: "admission", Target: "local", Kind: "contradicts"}, Relation{Source: "admission", Target: "missing", Kind: "depends_on"}, Relation{Source: "admission", Target: "quota", Kind: "unknown"}, local)
	plan := BuildCrossRelationPlan(base, docs, originals, CrossRelationOptions{}, 4096)
	works := plan.Works()
	if len(works) != 5 {
		t.Fatalf("inventory=%d", len(works))
	}
	ids := map[string]bool{}
	var evaluations []*CrossRelationEvaluation
	var requestable int
	for _, w := range works {
		d := w.Descriptor()
		if ids[d.ID] {
			t.Fatal("identical relation slots collided")
		}
		ids[d.ID] = true
		response := (*llm.CompleteResponse)(nil)
		if _, ok := w.Request(); ok {
			requestable++
			response, _ = crossReply(supportedCrossVerdict())(llm.CompleteRequest{})
		}
		evaluations = append(evaluations, w.Evaluate(response, nil))
	}
	if requestable != 2 {
		t.Fatalf("terminal inventory not explicit: %d", requestable)
	}
	for i, j := 0, len(evaluations)-1; i < j; i, j = i+1, j-1 {
		evaluations[i], evaluations[j] = evaluations[j], evaluations[i]
	}
	out, report, err := plan.Apply(base, evaluations)
	if err != nil {
		t.Fatal(err)
	}
	if report.Candidates != 5 || report.Accepted != 2 || len(out.Domains[0].Subdomains[0].Relations) != 3 || !reflect.DeepEqual(out.Domains[0].Subdomains[0].Relations[2], local) {
		t.Fatal("candidate coverage or noncandidate local relation changed")
	}
	want := []string{"accepted", "accepted", "insufficient_evidence", "rejected_endpoint", "rejected_relation"}
	for i, d := range report.Decisions {
		if d.Disposition != want[i] {
			t.Fatalf("report ordering/classification %d: %+v", i, d)
		}
	}
	for _, bad := range [][]*CrossRelationEvaluation{evaluations[:4], append(crossCopySlice(evaluations[:4]), evaluations[0]), append(crossCopySlice(evaluations[:4]), &CrossRelationEvaluation{})} {
		before := crossIdentityHash(base)
		if _, _, err = plan.Apply(base, bad); err == nil || crossIdentityHash(base) != before {
			t.Fatal("invalid coverage mutated/adopted base")
		}
	}
}

func TestCrossRelationPlanFrozenRootsAndDefensiveCopies(t *testing.T) {
	t.Parallel()
	base, docs, originals := crossFixture()
	base.Domains[0].Provenance = "extraction"
	base.Domains[0].SourceSkills = []string{"system"}
	base.Domains[0].Subdomains[0].Entities[0].Synonyms = []string{"initial alias"}
	docs[0].Items[0].SourceSpan = &dktypes.SourceSpan{StartLine: 1, EndLine: 2, Quote: "frozen annotation span"}
	frozenBase := cloneCrossResult(base)
	plan := BuildCrossRelationPlan(base, docs, originals, CrossRelationOptions{}, 4096)
	work := plan.Works()[0]
	request, _ := work.Request()
	id := plan.Identity()
	d := work.Descriptor()
	d.Sources[0].Path = "mutated descriptor"
	d.Relation.Description = "mutated relation"
	outer := plan.Works()
	outer[0] = nil
	base.Domains[0].Slug = "mutated source graph"
	docs[0].Items[0].SourceSpan.Quote = "mutated original docs"
	docs[0].Items[0].ID = "mutated member"
	originals[0].Content = "mutated caller originals"
	again, _ := work.Request()
	if !reflect.DeepEqual(request, again) || plan.Identity() != id || plan.Works()[0] == nil || work.Descriptor().Sources[0].Path == "mutated descriptor" {
		t.Fatal("caller mutation escaped into immutable plan")
	}
	response, _ := crossReply(supportedCrossVerdict())(request)
	evaluation := work.Evaluate(response, nil)
	copyDecision := evaluation.Decision()
	copyDecision.Source.Members[0] = "mutated member"
	copyDecision.Sources[0].Path = "mutated source"
	copyDecision.Evidence[0].Quote = "mutated quote"
	*copyDecision.Verifier.DirectionSupported = false
	copyDecision.Verifier.Evidence[0].Quote = "mutated verdict quote"
	out, report, err := plan.Apply(frozenBase, []*CrossRelationEvaluation{evaluation})
	if err != nil {
		t.Fatal(err)
	}
	if report.Accepted != 1 || out.Domains[0].Slug != "engineering" || out.Domains[0].Provenance != "extraction" || out.Domains[0].Subdomains[1].Slug != "capacity" {
		t.Fatal("hidden hierarchy identity or sealed decision was lost")
	}
	out.Domains[0].SourceSkills[0] = "mutated output"
	out.Domains[0].Subdomains[0].Entities[0].Synonyms[0] = "mutated output"
	out.Domains[0].Subdomains[0].Entities[0].Members[0] = "mutated output"
	report.Decisions[0].Evidence[0].Quote = "mutated report"
	out2, report2, err := plan.Apply(frozenBase, []*CrossRelationEvaluation{evaluation})
	if err != nil || report2.Accepted != 1 || report2.Decisions[0].Evidence[0].Quote == "mutated report" || out2.Domains[0].SourceSkills[0] != "system" || frozenBase.Domains[0].Subdomains[0].Entities[0].Synonyms[0] != "initial alias" {
		t.Fatal("Apply did not return independent copies")
	}
	for _, field := range []string{"domain-slug", "subdomain-slug", "domain-provenance", "label", "member", "relation"} {
		t.Run(field, func(t *testing.T) {
			changed := cloneCrossResult(frozenBase)
			switch field {
			case "domain-slug":
				changed.Domains[0].Slug = "other"
			case "subdomain-slug":
				changed.Domains[0].Subdomains[0].Slug = "other"
			case "domain-provenance":
				changed.Domains[0].Provenance = "human_curated"
			case "label":
				changed.Domains[0].Subdomains[0].Entities[0].Label = "Concept"
			case "member":
				changed.Domains[0].Subdomains[0].Entities[0].Members[0] = "other"
			case "relation":
				changed.Domains[0].Subdomains[0].Relations[0].Description = "other"
			}
			before := crossIdentityHash(changed)
			if _, _, e := plan.Apply(changed, []*CrossRelationEvaluation{evaluation}); e == nil || crossIdentityHash(changed) != before {
				t.Fatal("wrong graph root adopted or mutated")
			}
		})
	}
}

func TestCrossRelationPlanDeterministicRebuildAndForeignSourceBinding(t *testing.T) {
	t.Parallel()
	base, docs, originals := crossFixture()
	a := BuildCrossRelationPlan(base, docs, originals, CrossRelationOptions{}, 4096)
	b := BuildCrossRelationPlan(cloneCrossResult(base), cloneCrossDocuments(docs), crossCopySlice(originals), CrossRelationOptions{}, 4096)
	response, _ := crossReply(supportedCrossVerdict())(llm.CompleteRequest{})
	evaluation := a.Works()[0].Evaluate(response, nil)
	if a.Identity() != b.Identity() || a.Works()[0].Descriptor().ID != b.Works()[0].Descriptor().ID {
		t.Fatal("equivalent reconstructed plans depend on process/pointer identity")
	}
	if _, r, e := b.Apply(base, []*CrossRelationEvaluation{evaluation}); e != nil || r.Accepted != 1 {
		t.Fatal("equivalent immutable plan could not reuse sealed work result")
	}
	for _, mode := range []string{"annotation-detail", "ownership", "original-content", "budget", "tokens"} {
		t.Run(mode, func(t *testing.T) {
			otherDocs := cloneCrossDocuments(docs)
			otherSources := crossCopySlice(originals)
			opts := CrossRelationOptions{}
			tokens := 4096
			switch mode {
			case "annotation-detail":
				otherDocs[0].Items[0].Detail = "different complete annotation"
			case "ownership":
				otherDocs[0].FilePath = "different source owner"
			case "original-content":
				otherSources[0] = FreezeSource(otherSources[0].Skill, otherSources[0].Path, otherSources[0].Content+"additional original bytes")
			case "budget":
				opts.MaxEvidenceBytes = 128 * 1024
			case "tokens":
				tokens = 8192
			}
			other := BuildCrossRelationPlan(base, otherDocs, otherSources, opts, tokens)
			if other.Identity().PlanSHA256 == a.Identity().PlanSHA256 {
				t.Fatal("changed input recipe/root retained old plan identity")
			}
			if _, _, e := other.Apply(base, []*CrossRelationEvaluation{evaluation}); e == nil {
				t.Fatal("foreign-source/recipe evaluation accepted")
			}
		})
	}
}

func TestCrossRelationPlanConcurrentPureEvaluationAndLocalContradiction(t *testing.T) {
	t.Parallel()
	base, docs, originals := crossFixture()
	base.Domains[0].Subdomains[0].Concepts = base.Domains[0].Subdomains[1].Concepts
	base.Domains[0].Subdomains[1].Concepts = nil
	base.Domains[0].Subdomains[0].Relations[0].Kind = "contradicts"
	originals[0] = FreezeSource(originals[0].Skill, originals[0].Path, "In version 2 under condition X, admission is enabled.\n")
	originals[1] = FreezeSource(originals[1].Skill, originals[1].Path, "In version 2 under condition X, admission is disabled.\n")
	v := supportedCrossVerdict()
	yes := true
	v.Contradiction = &crossContradiction{&yes, &yes, &yes, &yes, &yes}
	v.Evidence[0].Quote = originals[0].Content
	v.Evidence[1].Quote = originals[1].Content
	response, _ := crossReply(v)(llm.CompleteRequest{})
	plan := BuildCrossRelationPlan(base, docs, originals, CrossRelationOptions{}, 4096)
	work := plan.Works()[0]
	results := make(chan *CrossRelationEvaluation, 16)
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() { defer group.Done(); results <- work.Evaluate(response, nil) }()
	}
	group.Wait()
	close(results)
	var first *CrossRelationEvaluation
	for result := range results {
		if first == nil {
			first = result
		}
		if !reflect.DeepEqual(first.Decision(), result.Decision()) || result.Decision().Scope != CrossRelationScopeLocal || result.Decision().Disposition != "accepted" {
			t.Fatal("pure evaluation mutated shared work or lost local contradiction")
		}
	}
	if _, report, e := plan.Apply(base, []*CrossRelationEvaluation{first}); e != nil || report.Candidates != 1 || report.Accepted != 1 {
		t.Fatal("local contradiction inventory/apply failed")
	}
}

type crossFinalCancellation struct{ calls atomic.Int64 }

func (*crossFinalCancellation) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*crossFinalCancellation) Done() <-chan struct{}       { return nil }
func (c *crossFinalCancellation) Err() error {
	if c.calls.Add(1) >= 3 {
		return context.Canceled
	}
	return nil
}
func (*crossFinalCancellation) Value(any) any { return nil }

func TestCrossRelationPlanLegacyFinalCancellationPreservesParsedEvidence(t *testing.T) {
	t.Parallel()
	base, docs, originals := crossFixture()
	ctx := &crossFinalCancellation{}
	client := &crossFixtureClient{reply: crossReply(supportedCrossVerdict())}
	r := NewExtractor(client, 4096).ValidateCrossRelations(ctx, base, docs, originals, CrossRelationOptions{})
	if client.calls.Load() != 1 || r.Decisions[0].Disposition != "execution_error" || r.Decisions[0].Reason != context.Canceled.Error() || r.Decisions[0].Verifier == nil || len(r.Decisions[0].Evidence) != 2 || len(base.Domains[0].Subdomains[0].Relations) != 0 {
		t.Fatal("final cancellation changed the legacy report/filter semantics")
	}
}

func TestCrossRelationPlanTypedRootsPreserveNonJSONMetadata(t *testing.T) {
	t.Parallel()
	base, docs, originals := crossFixture()
	base.Domains[0].Subdomains[0].Entities[0].Confidence = math.NaN()
	base.Domains[0].Provenance = "extraction"
	a := BuildCrossRelationPlan(base, docs, originals, CrossRelationOptions{}, 4096)
	b := BuildCrossRelationPlan(cloneCrossResult(base), cloneCrossDocuments(docs), crossCopySlice(originals), CrossRelationOptions{}, 4096)
	if a.Identity() != b.Identity() {
		t.Fatal("typed root lost nonfinite metadata or hidden provenance")
	}
	response, _ := crossReply(supportedCrossVerdict())(llm.CompleteRequest{})
	out, report, e := a.Apply(base, []*CrossRelationEvaluation{a.Works()[0].Evaluate(response, nil)})
	if e != nil || report.Accepted != 1 || !math.IsNaN(out.Domains[0].Subdomains[0].Entities[0].Confidence) {
		t.Fatal("pure factoring changed legacy nonrendered metadata behavior")
	}
	docs[0].Items[0].Detail = string([]byte{0xff})
	c := BuildCrossRelationPlan(base, docs, originals, CrossRelationOptions{}, 4096)
	docs[0].Items[0].Detail = string([]byte{0xfe})
	d := BuildCrossRelationPlan(base, docs, originals, CrossRelationOptions{}, 4096)
	if c.Identity().SourcesSHA256 == d.Identity().SourcesSHA256 {
		t.Fatal("source identity silently normalized distinct invalid UTF8 metadata")
	}
}

// 旧报告签名无法返回 Apply error；不支持的调用方改写必须有完整失败清单，不能伪装成功或覆盖未知改动。
// The new bounded orchestrator must stop on Apply errors; the compatibility wrapper only reports this violation.
func TestCrossRelationPlanLegacyCallerMutationHasCompleteFailureInventory(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"graph", "annotation-owner", "original-source"} {
		t.Run(mode, func(t *testing.T) {
			base, docs, originals := crossFixture()
			sd := &base.Domains[0].Subdomains[0]
			sd.Relations = append(sd.Relations, sd.Relations[0])
			before := cloneCrossResult(base)
			var once sync.Once
			client := &crossFixtureClient{reply: func(req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				once.Do(func() {
					switch mode {
					case "graph":
						base.Domains[0].Name = "caller-edited graph"
						base.Domains[0].Subdomains[0].Relations = append(base.Domains[0].Subdomains[0].Relations, Relation{Source: "caller-source", Target: "caller-target", Kind: "references", Description: "unknown caller edit"})
					case "annotation-owner":
						docs[0].FilePath = "caller-changed owner"
					case "original-source":
						originals[0] = FreezeSource(originals[0].Skill, originals[0].Path, "caller changed original bytes")
					}
				})
				return crossReply(supportedCrossVerdict())(req)
			}}
			report := NewExtractor(client, 4096).ValidateCrossRelations(context.Background(), base, docs, originals, CrossRelationOptions{})
			if report.Candidates != 2 || report.Accepted != 0 || len(report.Decisions) != 2 || client.calls.Load() != 2 {
				t.Fatal("caller mutation hid the frozen candidate denominator or claimed success")
			}
			for _, decision := range report.Decisions {
				if decision.Disposition != "execution_error" || decision.Reason != "verifier input identity changed during validation; exclusive caller ownership required" {
					t.Fatal("identity violation misclassified")
				}
			}
			if mode == "graph" {
				if base.Domains[0].Name != "caller-edited graph" || len(base.Domains[0].Subdomains[0].Relations) != 3 || base.Domains[0].Subdomains[0].Relations[2].Description != "unknown caller edit" {
					t.Fatal("wrapper reverted or deleted unknown caller edits")
				}
			} else if !reflect.DeepEqual(base, before) {
				t.Fatal("source-root mismatch modified the caller graph")
			}
		})
	}
}
