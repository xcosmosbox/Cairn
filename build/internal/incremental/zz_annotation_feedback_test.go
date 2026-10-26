package incremental

import (
	"context"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

type feedbackClient func(llm.CompleteRequest) (*llm.CompleteResponse, error)

func (f feedbackClient) Complete(_ context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
	return f(r)
}
func (feedbackClient) ProviderName() string { return "one-pass-test" }

// A rejected assessment used to spend more calls rewriting the full annotation.
func TestAnnotationSingleAssessmentNoRevision(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "rejected"}[reject], func(t *testing.T) {
			annotations, gates := 0, 0
			client := feedbackClient(func(r llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if !strings.Contains(r.User, "original source") {
					t.Fatal("original source lost")
				}
				if strings.Contains(r.System, "你是知识标注的语义核验专家") {
					gates++
					if reject {
						return &llm.CompleteResponse{Text: `{"supported":false,"reason":"unisolatable unsupported claim","unsupported_items":[],"omissions":[]}`}, nil
					}
					return &llm.CompleteResponse{Text: `{"supported":true,"reason":"retained facts are supported by the original source","unsupported_items":[],"omissions":[]}`}, nil
				}
				annotations++
				return &llm.CompleteResponse{Text: `{"items":[{"tag":"entity","name":"unit","content":"summary","detail":"original source","confidence":0.69}]}`}, nil
			})
			doc, reason := newAnnotator(client, 71, 1, 0).annotateOne(context.Background(), "skill", "ref.md", "original source")
			if annotations != 1 || gates != 1 {
				t.Fatalf("annotations=%d gates=%d", annotations, gates)
			}
			if reject && (doc != nil || reason == "") {
				t.Fatal("unsupported document accepted")
			}
			if !reject && (doc == nil || reason != "" || doc.Items[0].Confidence != .69) {
				t.Fatalf("doc=%+v reason=%s", doc, reason)
			}
		})
	}
}

// Existing human blocks stay outside both requests, while C4 line positions remain exact.
func TestC4SingleAnnotationKeepsHumanBlocksOutsideSource(t *testing.T) {
	const humanID = "11111111-1111-5111-8111-111111111111"
	full := "<!-- kg:uuid=" + humanID + " tag=entity -->\n## Human unit\nHuman curated text\n<!-- /kg:uuid=" + humanID + " -->\n\nNew outside prose.\n"
	blocks, segments, _ := parseKGBlocks(full)
	c4 := buildC4Text(full, blocks)
	if len(blocks) != 1 || strings.Count(c4, "\n") != strings.Count(full, "\n") {
		t.Fatal("invalid C4 boundary")
	}
	calls := 0
	client := feedbackClient(func(r llm.CompleteRequest) (*llm.CompleteResponse, error) {
		calls++
		if strings.Contains(r.User, humanID) || strings.Contains(r.User, "Human curated") {
			t.Fatal("human block reached model")
		}
		if strings.Contains(r.System, "你是知识标注的语义核验专家") {
			return &llm.CompleteResponse{Text: `{"supported":true,"reason":"retained facts are supported by the original source","unsupported_items":[],"omissions":[]}`}, nil
		}
		return &llm.CompleteResponse{Text: `{"items":[{"tag":"entity","name":"new unit","content":"summary","detail":"New outside prose.","confidence":0.69}]}`}, nil
	})
	pp := &PipelinePlan{C4ByDoc: map[string][]TextSegment{"ref.md": segments}, SkillByDoc: map[string]string{"ref.md": "skill"}}
	out := newAnnotator(client, 71, 1, 0).annotateC4Docs(context.Background(), pp, map[string]string{"ref.md": c4})
	if calls != 2 || len(out.Skipped) != 0 || len(out.Docs) != 1 || out.Docs[0].Items[0].ID == "" {
		t.Fatalf("calls=%d output=%+v", calls, out)
	}
}
