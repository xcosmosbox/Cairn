package incremental

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

func alignValidationBook() *aliasBook {
	return &aliasBook{
		nodeByAlias: map[string]*dktypes.Node{
			"cache#1": {ID: "node-entity", Label: dktypes.LabelEntity},
			"cache#2": {ID: "node-concept", Label: dktypes.LabelConcept},
		},
		subByAlias: map[string]*dktypes.Node{
			"sd#1": {ID: "sub", Label: dktypes.LabelSubdomain, Domain: "service", Subdomain: "cache"},
		},
		domByAlias: map[string]*dktypes.Node{
			"dom#1": {ID: "domain", Label: dktypes.LabelDomain, Domain: "service", Name: "服务领域"},
		},
	}
}

func TestAlignValidatesExactTargetAliasAndKind(t *testing.T) {
	t.Parallel()
	units := []*alignUnit{{ID: "u1"}}
	for _, action := range []string{"merge_into", "attach", "fuse"} {
		for _, target := range []string{"cache#1", "cache#2", "sd#1", "dom#1", "sd#1 缓存策略", "cache#1 缓存节点", "sd#99", "invented", "11111111-1111-4111-8111-111111111111"} {
			t.Run(action+"/"+target, func(t *testing.T) {
				out := alignLLMOutput{Decisions: []alignDecision{{Unit: "u1", Action: action, Target: target, NodeName: "名称", Tag: "concept"}}}
				if action == "fuse" {
					out.Decisions[0].Target = ""
					out.Decisions[0].Group = "g1"
					out.Groups = []alignGroup{{Group: "g1", Target: target, NodeName: "融合名称", Tag: "concept"}}
				}
				_, err := validateAlignOutput(&out, units, alignValidationBook())
				wantOK := (action == "merge_into" && (target == "cache#1" || target == "cache#2")) || (action != "merge_into" && target == "sd#1")
				if (err == nil) != wantOK {
					t.Fatalf("validation result: %v, want valid=%v", err, wantOK)
				}
				if err != nil {
					if _, fixed := err.(alignReferenceError); !fixed || strings.Contains(err.Error(), target) {
						t.Fatalf("reference feedback must be a fixed category, got %v", err)
					}
				}
			})
		}
	}
	// The lookup checks the mapped record, not only the spelling or map key.
	for _, target := range []*dktypes.Node{nil, {ID: "wrong-kind", Label: dktypes.LabelEntity}, {Label: dktypes.LabelSubdomain}} {
		book := alignValidationBook()
		book.subByAlias["sd#1"] = target
		out := alignLLMOutput{Decisions: []alignDecision{{Unit: "u1", Action: "attach", Target: "sd#1", NodeName: "名称", Tag: "concept"}}}
		if _, err := validateAlignOutput(&out, units, book); err == nil {
			t.Fatalf("invalid mapped record accepted: %+v", target)
		}
	}
	out := alignLLMOutput{Decisions: []alignDecision{{Unit: "u1", Action: "attach", Target: "sd#1", NodeName: "名称", Tag: "concept"}}}
	if _, err := validateAlignOutput(&out, units, nil); err == nil {
		t.Fatal("an explicitly missing production alias book accepted a target")
	}
	if _, err := validateAlignOutput(&out, units); err != nil {
		t.Fatalf("structure-only validation compatibility: %v", err)
	}
}

func TestAlignDomainReferencesCannotBecomeNewDomainNames(t *testing.T) {
	t.Parallel()
	for _, group := range []bool{false, true} {
		for _, ref := range []string{"dom#1", "新增服务领域", "C#开发领域", "C#8开发领域", "C#12语言", "dom#99", "dom#1 服务领域", "sd#1", "sd#99", "cache#1", "cache#99", "cache#99 节点"} {
			out := alignLLMOutput{Decisions: []alignDecision{{Unit: "u1", Action: "new_subdomain", Domain: ref, Subdomain: "新子域", NodeName: "名称", Tag: "concept"}}}
			if group {
				out.Decisions = []alignDecision{{Unit: "u1", Action: "fuse", Group: "g1"}}
				out.Groups = []alignGroup{{Group: "g1", Domain: ref, Subdomain: "新子域", NodeName: "名称", Tag: "concept"}}
			}
			_, err := validateAlignOutput(&out, []*alignUnit{{ID: "u1"}}, alignValidationBook())
			wantOK := ref == "dom#1" || ref == "新增服务领域" || strings.HasPrefix(ref, "C#")
			if (err == nil) != wantOK {
				t.Errorf("group=%v domain=%q: %v, want valid=%v", group, ref, err, wantOK)
			}
			_, _, _, resolved := (&aligner{}).resolveDomain(ref, alignValidationBook())
			if resolved != wantOK {
				t.Errorf("validator/materializer domain disagreement for %q", ref)
			}
		}
	}
	book := alignValidationBook()
	book.domByAlias["dom#1"].Label = dktypes.LabelSubdomain
	if !unknownDomainAlias("dom#1", book) {
		t.Fatal("domain alias pointing to a subdomain was accepted")
	}
	out := alignLLMOutput{Decisions: []alignDecision{{Unit: "u1", Action: "fuse", Group: "missing"}}}
	if _, err := validateAlignOutput(&out, []*alignUnit{{ID: "u1"}}, book); err == nil {
		t.Fatal("undefined fusion group accepted")
	}
}

type alignValidationSequence struct {
	responses []string
	requests  []llm.CompleteRequest
}

func (c *alignValidationSequence) ProviderName() string { return "offline-regression" }
func (c *alignValidationSequence) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.requests = append(c.requests, req)
	return &llm.CompleteResponse{Text: c.responses[len(c.requests)-1], FinishReason: "stop"}, nil
}

// Exercise the real align -> judge -> validate -> materialize boundary with a
// SQLite-backed alias book. Only provider responses are scripted: no live calls.
func TestAlignInvalidAliasRetriesBeforeAnyMaterialization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, repaired := range []bool{true, false} {
		t.Run(map[bool]string{true: "repaired", false: "exhausted"}[repaired], func(t *testing.T) {
			db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "kg.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			st := newStores(db)
			for _, n := range []*dktypes.Node{
				{ID: "hidden-sub-uuid", Label: dktypes.LabelSubdomain, Name: "缓存策略", Domain: "service", Subdomain: "cache"},
				{ID: "hidden-domain-uuid", Label: dktypes.LabelDomain, Name: "服务领域", Domain: "service"},
			} {
				n.Provenance = dktypes.ProvenanceExtraction
				if err := st.nodes.Insert(ctx, n); err != nil {
					t.Fatal(err)
				}
			}
			makeResponse := func(target string) string {
				out := alignLLMOutput{Decisions: []alignDecision{
					{Unit: "u1", Action: "attach", Target: "sd#1", NodeName: "查询编号", Tag: "entity"},
					{Unit: "u2", Action: "attach", Target: target, NodeName: "编号保留时间", Tag: "concept"},
				}}
				data, err := json.Marshal(out)
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
			// First reproduce the observed display-name suffix. The exhausted
			// case uses a fabricated UUID to prove feedback never reflects it.
			bad := makeResponse("sd#1 缓存策略")
			second := makeResponse("sd#1")
			if !repaired {
				bad = makeResponse("11111111-1111-4111-8111-111111111111")
				second = bad
			}
			client := &alignValidationSequence{responses: []string{bad, second}}
			docs := []*dktypes.AnnotatedDocument{{Skill: "service", FilePath: "service/references/cache.md", Items: []dktypes.AnnotatedItem{
				{ID: "u1", Tag: dktypes.TagEntity, Name: "查询编号", Content: "关联查询", Detail: "查询编号关联一次请求。", Confidence: 1},
				{ID: "u2", Tag: dktypes.TagConcept, Name: "编号保留时间", Content: "编号保留规则", Detail: "编号按固定周期保留。", Confidence: 1},
			}}}
			rs := newRunState()
			out, err := newAligner(client, 65536, st, 15).align(ctx, docs, rs)
			if err != nil || len(client.requests) != maxAlignRounds {
				t.Fatalf("expected validation retry: calls=%d err=%v", len(client.requests), err)
			}
			if !strings.Contains(client.requests[1].User, "attach.target 必须逐字引用") {
				t.Fatal("retry did not receive actionable fixed alias feedback")
			}
			for _, req := range client.requests {
				for _, private := range []string{"hidden-sub-uuid", "hidden-domain-uuid", "11111111-1111-4111-8111-111111111111"} {
					if strings.Contains(req.User+req.System, private) {
						t.Fatal("private identity or invalid raw model reference leaked into retry prompt")
					}
				}
			}
			if repaired {
				if out.llFailed || len(out.pending) != 2 || len(out.sourceRows) != 2 || len(rs.warningsSnapshot()) != 0 {
					t.Fatalf("corrected response lost units: %+v warnings=%v", out, rs.warningsSnapshot())
				}
			} else if !out.llFailed || len(out.pending) != 0 || len(out.sourceRows) != 0 || len(rs.warningsSnapshot()) != 1 {
				t.Fatalf("invalid batch partially materialized or unresolved failure hidden: %+v warnings=%v", out, rs.warningsSnapshot())
			}
		})
	}
}
