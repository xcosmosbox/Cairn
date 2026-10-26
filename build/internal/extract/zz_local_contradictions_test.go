package extract

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

func localContradictionFixture() (*Result, []*dktypes.AnnotatedDocument, []FrozenSource, crossVerdict) {
	res, docs, sources := crossFixture()
	sd := &res.Domains[0].Subdomains[0]
	sd.Concepts = append(sd.Concepts, res.Domains[0].Subdomains[1].Concepts...)
	res.Domains[0].Subdomains = res.Domains[0].Subdomains[:1]
	sd.Relations[0].Kind = "contradicts"
	sources[0] = FreezeSource(sources[0].Skill, sources[0].Path, "In version 2 under condition X, admission is enabled.\n")
	sources[1] = FreezeSource(sources[1].Skill, sources[1].Path, "In version 2 under condition X, admission is disabled.\n")
	v := supportedCrossVerdict()
	v.Reason = "The original claims disagree about the same admission property, version and condition."
	b := true
	v.Contradiction = &crossContradiction{&b, &b, &b, &b, &b}
	v.Evidence[0].Quote, v.Evidence[1].Quote = sources[0].Content, sources[1].Content
	return res, docs, sources, v
}

// 原故障：端点同属一个子域就跳过原文核验，伪矛盾可直接派生 UUID 并落库。
// Same-scope candidates formerly bypassed every source and contradiction check.
func TestLocalContradictionsUseOriginalTypedVerifier(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, want string
		change     func(*crossVerdict, []FrozenSource)
	}{
		{"same-context-incompatible", "accepted", func(*crossVerdict, []FrozenSource) {}},
		{"compatible-dimensions", "rejected_semantic", func(v *crossVerdict, sources []FrozenSource) {
			sources[0] = FreezeSource(sources[0].Skill, sources[0].Path, "Buying a refurbished Mac can save money.\n")
			sources[1] = FreezeSource(sources[1].Skill, sources[1].Path, "Avoid older Intel Macs when choosing a refurbished Mac.\n")
			v.Reason = "Purchase savings and avoiding an older CPU are compatible dimensions."
			f := false
			v.Contradiction.SameAttribute, v.Contradiction.Incompatible = &f, &f
		}},
		{"changed-conditions", "rejected_semantic", func(v *crossVerdict, sources []FrozenSource) {
			sources[0] = FreezeSource(sources[0].Skill, sources[0].Path, "Meditation can relieve anxiety with suitable guidance.\n")
			sources[1] = FreezeSource(sources[1].Skill, sources[1].Path, "Meditation can worsen anxiety under other conditions.\n")
			v.Reason = "The two outcomes concern different conditions."
			f := false
			v.Contradiction.SameConditions = &f
		}},
		{"missing-predicate", "invalid_verdict", func(v *crossVerdict, _ []FrozenSource) { v.Contradiction.SameTime = nil }},
		{"generated-quote", "rejected_evidence", func(v *crossVerdict, _ []FrozenSource) {
			v.Evidence[0].Quote = "Generated admission detail is not original evidence."
		}},
		{"wrong-member-owner", "rejected_evidence", func(v *crossVerdict, _ []FrozenSource) { v.Evidence[0].MemberID = "member-quota" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, docs, sources, verdict := localContradictionFixture()
			tc.change(&verdict, sources)
			client := &crossFixtureClient{reply: func(req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				for _, src := range sources {
					if !strings.Contains(req.User, strings.TrimSpace(src.Content)) {
						t.Errorf("complete frozen original absent: %s", src.Path)
					}
				}
				return crossReply(verdict)(req)
			}}
			rpt := NewExtractor(client, 4096).ValidateCrossRelations(context.Background(), res, docs, sources, CrossRelationOptions{})
			if rpt.ValidationPolicy != CrossRelationPolicyV2 || rpt.Candidates != 1 || client.calls.Load() != 1 || rpt.Decisions[0].Disposition != tc.want {
				t.Fatalf("local verifier bypassed: report=%+v calls=%d", rpt, client.calls.Load())
			}
			d := rpt.Decisions[0]
			if d.Scope != CrossRelationScopeLocal || d.Kind != "contradicts" {
				t.Fatalf("wrong explicit local identity: %+v", d)
			}
			kept := len(res.Domains[0].Subdomains[0].Relations)
			if tc.want == "accepted" {
				if rpt.Accepted != 1 || kept != 1 || len(d.Evidence) != 2 {
					t.Fatalf("true contradiction lost: %+v", rpt)
				}
			} else if rpt.Accepted != 0 || kept != 0 {
				t.Fatalf("rejected local contradiction retained or relabeled: %+v", res)
			}
		})
	}
}

// 本域其他关系不扩大验证，跨域 contradicts 不因同时满足两种规则被重复调用。
// Keep other local kinds unchanged and verify a cross contradiction exactly once.
func TestLocalContradictionScopeDoesNotWidenOrDoubleVerify(t *testing.T) {
	t.Parallel()
	res, docs, sources, verdict := localContradictionFixture()
	sd := &res.Domains[0].Subdomains[0]
	var localOthers []Relation
	for _, kind := range []string{"references", "triggers", "depends_on", "generalizes", "composes"} {
		localOthers = append(localOthers, Relation{Source: "admission", Target: "quota", Kind: kind, Description: "preserve original local behavior", Confidence: .87})
	}
	sd.Relations = append(sd.Relations, localOthers...)
	client := &crossFixtureClient{reply: crossReply(verdict)}
	rpt := NewExtractor(client, 4096).ValidateCrossRelations(context.Background(), res, docs, sources, CrossRelationOptions{})
	if client.calls.Load() != 1 || rpt.Candidates != 1 || !reflect.DeepEqual(sd.Relations[1:], localOthers) {
		t.Fatalf("other local kinds changed: %+v", rpt)
	}
	res, docs, sources, verdict = localContradictionFixture()
	first := &res.Domains[0].Subdomains[0]
	remote := Subdomain{Name: "Capacity", Slug: "capacity", Concepts: first.Concepts}
	first.Concepts = nil
	res.Domains[0].Subdomains = append(res.Domains[0].Subdomains, remote)
	client = &crossFixtureClient{reply: crossReply(verdict)}
	rpt = NewExtractor(client, 4096).ValidateCrossRelations(context.Background(), res, docs, sources, CrossRelationOptions{})
	if rpt.Candidates != 1 || rpt.Accepted != 1 || client.calls.Load() != 1 || rpt.Decisions[0].Scope != CrossRelationScopeCross {
		t.Fatalf("cross contradiction counted or verified twice: %+v", rpt)
	}
}

// 本域候选也必须遵守原文完整性、容量与取消边界，不能借同子域绕过失败关闭。
func TestLocalContradictionsFailClosedOnSourceCapacityAndCancellation(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{"missing-source", "cancelled"} {
		t.Run(tc, func(t *testing.T) {
			res, docs, sources, verdict := localContradictionFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts := CrossRelationOptions{}
			want := "insufficient_evidence"
			switch tc {
			case "missing-source":
				sources = sources[:1]
			case "cancelled":
				cancel()
				want = "execution_error"
			}
			client := &crossFixtureClient{reply: crossReply(verdict)}
			rpt := NewExtractor(client, 4096).ValidateCrossRelations(ctx, res, docs, sources, opts)
			if rpt.Candidates != 1 || rpt.Accepted != 0 || rpt.Decisions[0].Disposition != want || client.calls.Load() != 0 || len(res.Domains[0].Subdomains[0].Relations) != 0 {
				t.Fatalf("unsafe local fallback: %+v", rpt)
			}
		})
	}
}
