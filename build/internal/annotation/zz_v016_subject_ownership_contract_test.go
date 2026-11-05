package annotation_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type subjectCoreScript struct {
	t       *testing.T
	replies []string
	calls   []llm.CompleteRequest
}

func (*subjectCoreScript) ProviderName() string { return "offline-subject-core" }
func (s *subjectCoreScript) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := len(s.calls)
	s.calls = append(s.calls, req)
	if n >= len(s.replies) {
		s.t.Fatalf("unexpected dependent call %d", n+1)
	}
	return &llm.CompleteResponse{Text: s.replies[n], FinishReason: "stop"}, nil
}

func subjectCoreJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func subjectCoreItem(tag dktypes.AnnotationTag, name, detail string) dktypes.AnnotatedItem {
	return dktypes.AnnotatedItem{Tag: tag, Name: name, Content: detail, Detail: detail, Confidence: .9}
}

func subjectCoreRows(items []dktypes.AnnotatedItem) string {
	return subjectCoreJSON(map[string]any{"items": items})
}

// Explicit authored semantic results; this only serializes fixture inputs.
func subjectCoreRow(quote, subject string, index int, item string, same, preserves bool) map[string]any {
	return map[string]any{"source_quote": quote, "subject_referent": subject, "checks": []any{map[string]any{"item_index": index, "item_referent": item, "same_referent": same, "preserves_assertion": preserves, "reason": "authored fixture result"}}}
}
func subjectCoreEvidence(rows ...map[string]any) string {
	return subjectCoreJSON(map[string]any{"supported": true, "reason": "authored source ownership result", "unsupported_items": []int{}, "omissions": []string{}, "subject_coverage": rows})
}

const subjectCoreSupported = `{"supported":true,"reason":"source ownership, coverage and fields are faithful","unsupported_items":[],"omissions":[]}`

// The observed shape has four accepted items, including the whole proposition
// and its other endpoint, but no item referring to the predicated activity.
// Supplied verdicts test finite repair and retention, not prompt efficacy.
func TestV016SubjectCoreMissingCarrierFiniteRevision(t *testing.T) {
	t.Parallel()
	for _, subject := range []string{"索引重建", "Atlas重建器"} {
		for _, exhaust := range []bool{false, true} {
			name := subject + "/repair"
			if exhaust {
				name = subject + "/exhausted"
			}
			t.Run(name, func(t *testing.T) {
				predicate := subject + "仅在获批维护窗口中执行，且依赖维护窗口。"
				items := []dktypes.AnnotatedItem{
					subjectCoreItem(dktypes.TagEntity, "维护窗口", "本页的窗口指维护窗口。"+predicate),
					subjectCoreItem(dktypes.TagConcept, subject+"依赖维护窗口", predicate),
					subjectCoreItem(dktypes.TagConcept, "日志轮换", "日志轮换在一月版每轮处理4份日志，不负责重建。"),
					subjectCoreItem(dktypes.TagConcept, "一月版轮换数量", "日志轮换在一月版每轮处理4份日志。"),
				}
				original := "本页的窗口指维护窗口。" + predicate + "日志轮换在一月版每轮处理4份日志，不负责重建。"
				good := append(append([]dktypes.AnnotatedItem(nil), items...), subjectCoreItem(dktypes.TagConcept, subject, predicate))
				feedback := "缺少以" + subject + "本身为所指的单元；补回“" + predicate + "”，保留获批、仅在及依赖方向。独立完整命题和维护窗口不能替代主体。"
				reject := subjectCoreJSON(map[string]any{"supported": false, "reason": feedback, "unsupported_items": []int{}, "omissions": []string{feedback}})
				revision, verdict := subjectCoreRows(good), subjectCoreEvidence(subjectCoreRow(predicate, subject, 4, subject, true, true), subjectCoreRow("本页的窗口指维护窗口。", "维护窗口", 0, "维护窗口", true, true), subjectCoreRow("日志轮换在一月版每轮处理4份日志，不负责重建。", "日志轮换", 2, "日志轮换", true, true))
				if exhaust {
					revision, verdict = subjectCoreRows(items), reject
				}
				client := &subjectCoreScript{t: t, replies: []string{subjectCoreRows(items), reject, revision, verdict}}
				out := annotation.NewDocumentProcessor(client, 4096, 1, 0).Process(context.Background(), "operations", "references/rebuild.md", original)
				if len(client.calls) != 4 || len(out.Attempts) != 4 {
					t.Fatalf("allowance changed: %+v", out)
				}
				for i, step := range []string{"annotate", "assess", "revise", "assess"} {
					if out.Attempts[i].Step != step || out.Attempts[i].Number != i+1 {
						t.Fatalf("wrong finite flow: %+v", out.Attempts)
					}
				}
				for _, text := range []string{original, feedback, items[1].Name, items[2].Detail, items[3].Detail} {
					if !strings.Contains(client.calls[2].User, text) {
						t.Fatalf("revision lost original/candidate/feedback: %q", text)
					}
				}
				if exhaust {
					if out.Outcome != annotation.DocumentPartial || !out.Exhausted || !strings.Contains(out.Reason, subject) {
						t.Fatalf("missing carrier promoted: %+v", out)
					}
					return
				}
				if out.Outcome != annotation.DocumentComplete || out.Exhausted || out.Document == nil || !reflect.DeepEqual(out.Document.Items, good) {
					t.Fatalf("subject repair lost preexisting items or source limits: %+v", out)
				}
				if out.Attempts[1].CandidateID == out.CandidateID || !strings.Contains(client.calls[3].User, original) || !strings.Contains(client.calls[3].User, subjectCoreJSON(good)) {
					t.Fatal("replacement was not wholly rechecked with original source")
				}
			})
		}
	}
}

// Proper names are unnecessary, but mere discussion/uncertain attribution does
// not authorize inventing owners or converting quoted/negated claims to facts.
func TestV016SubjectCoreFaithfulOwnershipBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, original string
		items          []dktypes.AnnotatedItem
	}{
		{"ordinary_action", "索引重建依赖维护窗口。", []dktypes.AnnotatedItem{subjectCoreItem(dktypes.TagConcept, "索引重建", "索引重建依赖维护窗口。")}},
		{"named_subject", "Atlas重建器仅在维护窗口运行。", []dktypes.AnnotatedItem{subjectCoreItem(dktypes.TagEntity, "Atlas重建器", "Atlas重建器仅在维护窗口运行。")}},
		{"proposition_only_discussed", "命题‘索引重建依赖维护窗口’在此仅为待证命题，其真值未确定。", []dktypes.AnnotatedItem{subjectCoreItem(dktypes.TagConcept, "待证命题", "命题‘索引重建依赖维护窗口’在此仅为待证命题，其真值未确定。")}},
		{"unattributed_subject", "Atlas与Birch是不同任务。便签称某任务依赖窗口，但未说明是哪项。", []dktypes.AnnotatedItem{subjectCoreItem(dktypes.TagEntity, "Atlas", "Atlas与Birch是不同任务。"), subjectCoreItem(dktypes.TagEntity, "Birch", "Atlas与Birch是不同任务。"), subjectCoreItem(dktypes.TagConcept, "未归属便签", "便签称某任务依赖窗口，但未说明是哪项。")}},
		{"quoted_denial", "审计员否认索引重建依赖维护窗口；作者只记录这项否认，未确认实际依赖。", []dktypes.AnnotatedItem{
			subjectCoreItem(dktypes.TagConcept, "索引重建", "审计员否认索引重建依赖维护窗口；作者只记录这项否认，未确认实际依赖。"),
			subjectCoreItem(dktypes.TagEntity, "审计员", "审计员否认索引重建依赖维护窗口。"),
			subjectCoreItem(dktypes.TagEntity, "作者", "作者只记录审计员的这项否认，未确认实际依赖。"),
		}},
		{"generic_subject", "有些维护任务依赖窗口，本文未逐一指明任务。", []dktypes.AnnotatedItem{subjectCoreItem(dktypes.TagConcept, "部分维护任务", "有些维护任务依赖窗口，本文未逐一指明任务。")}},
		{"explicit_unknown_value", "索引重建的持续时间明确未知。", []dktypes.AnnotatedItem{subjectCoreItem(dktypes.TagConcept, "索引重建", "索引重建的持续时间明确未知。")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var proof []map[string]any
			switch tc.name {
			case "ordinary_action", "explicit_unknown_value":
				proof = []map[string]any{subjectCoreRow(tc.original, "索引重建", 0, "索引重建", true, true)}
			case "named_subject":
				proof = []map[string]any{subjectCoreRow(tc.original, "Atlas重建器", 0, "Atlas重建器", true, true)}
			case "proposition_only_discussed":
				proof = []map[string]any{subjectCoreRow(tc.original, "待证命题", 0, "待证命题", true, true)}
			case "unattributed_subject":
				proof = []map[string]any{subjectCoreRow("Atlas与Birch是不同任务。", "Atlas", 0, "Atlas", true, true), subjectCoreRow("Atlas与Birch是不同任务。", "Birch", 1, "Birch", true, true), subjectCoreRow("便签称某任务依赖窗口，但未说明是哪项。", "未归属便签", 2, "未归属便签", true, true)}
			case "quoted_denial":
				proof = []map[string]any{subjectCoreRow("审计员否认索引重建依赖维护窗口", "索引重建", 0, "索引重建", true, true), subjectCoreRow("审计员否认索引重建依赖维护窗口", "审计员", 1, "审计员", true, true), subjectCoreRow("作者只记录这项否认，未确认实际依赖。", "作者", 2, "作者", true, true)}
			case "generic_subject":
				proof = []map[string]any{subjectCoreRow(tc.original, "部分维护任务", 0, "部分维护任务", true, true)}
			}
			client := &subjectCoreScript{t: t, replies: []string{subjectCoreRows(tc.items), subjectCoreEvidence(proof...)}}
			out := annotation.NewDocumentProcessor(client, 4096, -1, 0).Process(context.Background(), "operations", "references/boundary.md", tc.original)
			if out.Outcome != annotation.DocumentComplete || out.Document == nil || !reflect.DeepEqual(out.Document.Items, tc.items) || len(client.calls) != 2 {
				t.Fatalf("faithful source boundary changed: %+v", out)
			}
			if !strings.Contains(client.calls[1].User, tc.original) {
				t.Fatal("assessment lost the original scope")
			}
		})
	}
}

// Use the real node caller and shared SemanticGate, not a simulated scope flag.
// Its one supplied proposition member does not grant authority to add an actor
// node or regroup members during single-node fusion.
func TestV016SubjectCoreSingleNodeProvidedMemberBoundary(t *testing.T) {
	t.Parallel()
	source := "member rule-1 (references/rules.md): 索引重建依赖维护窗口。"
	detail := "索引重建依赖维护窗口。"
	wire := subjectCoreJSON(map[string]any{"descriptions": []map[string]string{{"node_id": "scope#1", "summary": detail, "description": detail}}})
	client := &subjectCoreScript{t: t, replies: []string{wire, subjectCoreSupported}}
	result := extract.FuseNodeDescriptions(context.Background(), client, 4096, -1, "operations", "scope", []extract.NodeDescriptionInput{{ID: "fixed-rule-node", Label: "Concept", Name: "重建依赖命题", Material: source}})
	if len(result.Nodes) != 1 || result.Nodes[0].Outcome != annotation.DocumentComplete || result.Nodes[0].ID != "fixed-rule-node" || result.Nodes[0].Description != detail || len(client.calls) != 2 {
		t.Fatalf("single-node scope expanded or rejected: %+v", result)
	}
	if !strings.Contains(client.calls[1].System, "这是单个节点的融合候选") || !strings.Contains(client.calls[1].User, source) || !strings.Contains(client.calls[1].User, detail) {
		t.Fatal("real node assessment lost its caller scope or supplied member")
	}
}
