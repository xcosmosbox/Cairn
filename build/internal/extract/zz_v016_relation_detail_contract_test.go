package extract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type v016DetailNode struct {
	Alias, Name, Summary, Description string
	Members                           []string
	MemberSources                     []struct {
		MemberID string `json:"member_id"`
		SourceID string `json:"source_id"`
	} `json:"member_sources"`
}

type v016DetailInput struct {
	Focus      string `json:"focus_source_id"`
	Nodes      []v016DetailNode
	Sources    []crossOriginal          `json:"original_sources"`
	Material   []relationMemberMaterial `json:"member_material"`
	Candidates []Relation               `json:"candidate_relations"`
	Feedback   string
}

// 原覆盖请求未携带实际正文，无法区分已保存的谓词与只留在原文/成员里的谓词。
// Scripted HTTP assessment checks the actual serialized graph and bindings, not claimed success.
// It establishes payload/state behavior; it is not evidence of a real model's judgment quality.
func TestV016RelationDetailContract(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"detail", "missing", "missing-retries", "redacted", "wrong-source", "wrong-node", "lost-negation", "contradictory-summary", "invented-detail", "false-reference", "unsupported-kind", "dependency", "missing-dependency", "reversed-dependency", "repair-dependency"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			dependency := strings.Contains(mode, "dependency")
			text := "Valve V feeds Atlas pump only while bypass is off. Valve V does not feed Atlas pump while bypass is on."
			a, b := "Valve V", "Atlas pump"
			if dependency {
				a, b = "Atlas pump", "Controller C"
				text = "Atlas pump depends on Controller C only while bypass is off. Atlas pump does not depend on Controller C while bypass is on."
			}
			sibling := "Controller D provides Cedar pump with a shutdown signal."
			res := &Result{Domains: []Domain{{Slug: "d", Subdomains: []Subdomain{{Slug: "s", Entities: []Node{
				{ID: "a", Name: a, Summary: "Qualified source assertion", Description: text, Members: []string{"ma"}},
				{ID: "b", Name: b, Description: "Endpoint", Members: []string{"mb"}},
				{ID: "logger", Name: "Logger", Description: "Records messages", Members: []string{"ml"}},
				{ID: "controller", Name: "Controller D", Description: sibling, Members: []string{"mc"}},
				{ID: "cedar", Name: "Cedar pump", Description: "Endpoint", Members: []string{"md"}},
			}}}}}}
			docs := []*dktypes.AnnotatedDocument{
				{Skill: "fixture", FilePath: "1.md", Items: []dktypes.AnnotatedItem{{ID: "ma", Name: a, Content: text, Detail: text}, {ID: "mb", Name: b}, {ID: "ml", Name: "Logger"}}},
				{Skill: "fixture", FilePath: "2.md", Items: []dktypes.AnnotatedItem{{ID: "mc", Name: "Controller D", Content: sibling, Detail: sibling}, {ID: "md", Name: "Cedar pump"}}},
			}
			originals := []FrozenSource{FreezeSource("fixture", "1.md", text), FreezeSource("fixture", "2.md", sibling)}
			nodes := res.Domains[0].Subdomains[0].Entities
			switch mode {
			case "missing", "missing-retries":
				nodes[0].Description = ""
			case "redacted":
				nodes[0].Name, nodes[0].Summary, nodes[0].Description = "protected endpoint", "", ""
			case "wrong-source":
				nodes[0].Members = []string{"mc"}
			case "wrong-node":
				nodes[0].Description, nodes[2].Description = "Endpoint", text
			case "lost-negation":
				nodes[0].Description = strings.Split(text, ". ")[0] + "."
			case "invented-detail":
				nodes[0].Description += " Valve V also powers Atlas pump."
			case "contradictory-summary":
				nodes[0].Summary = "Valve V feeds Atlas pump while bypass is on."
			}
			before := cloneCrossResult(res)
			var mu sync.Mutex
			calls := map[string][]string{}
			generated := map[string]int{}
			var prompts []string
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var wire struct {
					Messages []struct{ Role, Content string }
					Format   struct{ Type string } `json:"response_format"`
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil || len(wire.Messages) != 2 || wire.Format.Type != "json_object" {
					t.Errorf("invalid production JSON request: %v", err)
					http.Error(w, "invalid request", 400)
					return
				}
				req := llm.CompleteRequest{System: wire.Messages[0].Content, User: wire.Messages[1].Content}
				var in v016DetailInput
				if err := json.Unmarshal([]byte(req.User), &in); err != nil {
					t.Errorf("invalid input: %v", err)
				}
				stage := "verify"
				var reply *llm.CompleteResponse
				if strings.HasPrefix(req.System, "SOURCE-RELATION GENERATE") {
					stage = "generate"
					generated[in.Focus]++
					if generated[in.Focus] > 1 && !strings.Contains(in.Feedback, "raw/member text alone is insufficient") {
						t.Error("revision lost the actual coverage critique")
					}
					rows := []Relation{}
					if in.Focus == "s1" && ((dependency && mode != "missing-dependency") || mode == "false-reference" || mode == "unsupported-kind") {
						kind := "depends_on"
						if mode == "false-reference" {
							kind = "references"
						}
						if mode == "unsupported-kind" {
							kind = "feeds"
						}
						source, target := "r1", "r2"
						if mode == "reversed-dependency" {
							source, target = target, source
						}
						if mode != "repair-dependency" || generated[in.Focus] > 1 {
							rows = append(rows, Relation{Source: source, Target: target, Kind: kind, Description: text, Confidence: .9})
						}
					}
					reply = v014JSON(map[string]any{"relations": rows})
				} else if strings.HasPrefix(req.System, "SOURCE-RELATION COVERAGE") {
					stage = "coverage"
					ok := false
					var original crossOriginal
					for _, s := range in.Sources {
						if s.ID == in.Focus {
							original = s
						}
					}
					for _, n := range in.Nodes {
						// Word overlap on an unrelated node or a source-misbound participant is insufficient.
						if (in.Focus == "s1" && n.Alias != "r1" && n.Alias != "r2") || (in.Focus == "s2" && n.Alias != "r4" && n.Alias != "r5") {
							continue
						}
						for _, binding := range n.MemberSources {
							if binding.SourceID != in.Focus || !containsMember(n.Members, binding.MemberID) || !containsMember(original.Members, binding.MemberID) {
								continue
							}
							for _, material := range in.Material {
								if material.SourceID != in.Focus {
									continue
								}
								for _, item := range material.Items {
									if item.ID == binding.MemberID && n.Description == original.Content && n.Summary != "Valve V feeds Atlas pump while bypass is on." {
										ok = true
									}
								}
							}
						}
					}
					if in.Focus == "s1" && dependency {
						ok = len(in.Candidates) == 1 && in.Candidates[0].Source == "r1" && in.Candidates[0].Target == "r2" && in.Candidates[0].Kind == "depends_on" && in.Candidates[0].Description == original.Content
					} else if len(in.Candidates) != 0 {
						ok = false
					}
					critique := ""
					if !ok {
						critique = "preserve the complete correctly bound graph detail and required qualified dependency edge; raw/member text alone is insufficient"
					}
					reply = v014JSON(map[string]any{"complete": ok, "supported": ok, "reason": "actual graph content and source binding assessed", "critique": critique})
				} else {
					reply = v014Supported(req)
				}
				mu.Lock()
				calls[in.Focus] = append(calls[in.Focus], stage)
				if stage != "verify" {
					prompts = append(prompts, req.System)
				}
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": reply.Text}}}})
			}))
			defer provider.Close()
			client, err := llm.NewOpenAICompatClientWithTransport(llm.Config{Endpoint: provider.URL + "/chat/completions", APIKeyEnv: "CAIRN_V016_DETAIL_LOOPBACK_UNUSED", MaxRetries: -1, Timeout: "5s"}, provider.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			retries := -1
			if mode == "repair-dependency" || mode == "missing-retries" {
				retries = 1
			}
			report := NewExtractor(client, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: retries})
			complete := mode == "detail" || mode == "dependency" || mode == "repair-dependency"
			if len(report.Scopes) != 2 || report.Scopes[1].Outcome != annotation.DocumentComplete {
				t.Fatalf("independent source scope lost: %+v", report)
			}
			want := annotation.DocumentPartial
			if complete {
				want = annotation.DocumentComplete
			}
			if report.Scopes[0].Outcome != want || report.Scopes[0].Exhausted == complete {
				t.Fatalf("wrong dependent outcome for %s: %+v", mode, report)
			}
			selected := 0
			if complete && dependency {
				selected = 1
			}
			if len(report.Selected) != selected {
				t.Fatalf("false or missing selected edges: %+v", report.Selected)
			}
			if len(calls[""]) != selected {
				t.Fatalf("wrong per-edge verification calls: %v", calls)
			}
			// The relation helper must not silently repair missing nodes or corrupt their details.
			for i, n := range res.Domains[0].Subdomains[0].Entities {
				if !reflect.DeepEqual(n, before.Domains[0].Subdomains[0].Entities[i]) {
					t.Fatalf("relation helper changed node %d", i)
				}
			}
			if generated["s2"] != 1 {
				t.Fatalf("successful sibling regenerated: %v", generated)
			}
			if mode == "repair-dependency" && (generated["s1"] != 2 || len(report.Scopes[0].Attempts) != 5) {
				t.Fatalf("finite recovery changed: %+v", report.Scopes[0])
			}
			if mode == "missing-retries" && (generated["s1"] != 2 || len(report.Scopes[0].Attempts) != 4) {
				t.Fatalf("missing node detail must remain partial after finite edge revision: %+v", report.Scopes[0])
			}
			for _, prompt := range prompts {
				for _, term := range []string{"triggers", "depends_on", "references", "generalizes", "composes", "contradicts", "member_sources", "summary", "description"} {
					if !strings.Contains(prompt, term) {
						t.Errorf("shared relation/detail contract missing %q", term)
					}
				}
			}
		})
	}
}
