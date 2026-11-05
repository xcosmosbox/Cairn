package extract

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
)

func v016ScopeInput(material string) NodeDescriptionInput {
	return NodeDescriptionInput{ID: "fixed-node-identity", Label: "Concept", Name: "Beacon tally", Summary: "Beacon tally", Material: material}
}

// 来源广否定被名词中心语挪成宾语后缩窄；另一个字段正确也不能抵消它。
// Scripted feedback tests the existing finite node flow, not model success at detecting scope errors.
func TestV016NodePredicateScopeFeedbackRepair(t *testing.T) {
	t.Parallel()
	broad := "Beacon tally只负责闭馆后清点纸鸢，不参与夜场入厅。每次清点4只纸鸢。"
	narrow := "Beacon tally只负责闭馆后清点纸鸢，不参与夜场入厅的清点事项。每次清点4只纸鸢。"
	limited := "Beacon tally不参与夜场入厅的清点事项；原文未说明是否参与其他入厅活动。"
	for _, tc := range []struct{ name, source, badSummary, badDetail, goodSummary, goodDetail, reason string }{
		{"narrowed_detail", broad, "Beacon tally不参与夜场入厅。", narrow, "Beacon tally不参与夜场入厅。", broad, "完整来源排除参与整个夜场入厅；description把中心语清点事项移成否定宾语，只排除入厅中的清点事项。"},
		{"narrowed_summary", broad, "Beacon tally不参与夜场入厅的清点。", broad, "Beacon tally不参与夜场入厅。", broad, "summary缩窄了来源的全入厅否定，正确description不能抵消summary的范围错误。"},
		{"broadened_subclass", limited, "Beacon tally不参与夜场入厅。", "Beacon tally不参与夜场入厅。", "Beacon tally不参与夜场入厅的清点事项。", limited, "来源仅排除入厅的清点子类且其他活动未知，不能扩大为排除整个入厅。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feedback, _ := json.Marshal(map[string]any{"supported": false, "reason": tc.reason, "unsupported_items": []int{0}, "omissions": []string{}})
			client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("scope#1", tc.badSummary, tc.badDetail)}, {text: string(feedback)}, {text: nodeWire("scope#1", tc.goodSummary, tc.goodDetail)}, {text: nodeSupported}}}
			out := FuseNodeDescriptions(context.Background(), client, 4096, 1, "operations", "scope", []NodeDescriptionInput{v016ScopeInput(tc.source)}).Nodes[0]
			if out.Outcome != annotation.DocumentComplete || out.Exhausted || out.ID != "fixed-node-identity" || out.Summary != tc.goodSummary || out.Description != tc.goodDetail || len(out.Attempts) != 4 || len(client.requests) != 4 {
				t.Fatalf("scope not repaired within original allowance: %+v", out)
			}
			for i, step := range []string{"generate", "assess", "revise", "assess"} {
				if out.Attempts[i].Step != step || out.Attempts[i].Number != i+1 {
					t.Fatalf("wrong failed-step flow: %+v", out.Attempts)
				}
			}
			for _, text := range []string{tc.source, tc.reason, tc.badSummary, tc.badDetail} {
				if !strings.Contains(client.requests[2].User, text) {
					t.Fatalf("revision lost full material/candidate/concrete scope feedback: %s", text)
				}
			}
			if !strings.Contains(client.requests[3].User, tc.goodSummary) || !strings.Contains(client.requests[3].User, tc.goodDetail) || !strings.Contains(client.requests[3].User, tc.source) || out.Attempts[1].CandidateID == out.CandidateID {
				t.Fatal("replacement did not get a full new assessment")
			}
		})
	}
}

// 耗尽只影响错误节点，已完成兄弟不重跑；未知核验保持同一候选，不能借别处正确通过。
func TestV016NodePredicateScopeExhaustionPreservesSibling(t *testing.T) {
	t.Parallel()
	source := "Beacon tally只负责闭馆后清点纸鸢，不参与夜场入厅。"
	bad := "Beacon tally不参与夜场入厅的清点事项。"
	reject := `{"supported":false,"reason":"the negation object was narrowed","unsupported_items":[0],"omissions":[]}`
	client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("scope#1", "Independent logger", "Logger records inventory only.")}, {text: nodeSupported}, {text: nodeWire("scope#2", bad, bad)}, {text: reject}, {text: nodeWire("scope#2", bad, bad)}, {text: reject}}}
	good := NodeDescriptionInput{ID: "independent", Label: "Entity", Name: "Logger", Material: "Logger records inventory only."}
	result := FuseNodeDescriptions(context.Background(), client, 4096, 1, "operations", "scope", []NodeDescriptionInput{good, v016ScopeInput(source)})
	if len(result.Nodes) != 2 || result.Nodes[0].Outcome != annotation.DocumentComplete || len(result.Nodes[0].Attempts) != 2 || result.Nodes[1].Outcome != annotation.DocumentPartial || !result.Nodes[1].Exhausted || len(result.Nodes[1].Attempts) != 4 || len(client.requests) != 6 {
		t.Fatalf("exhaustion lost sibling or reset allowance: %+v", result)
	}
}

func TestV016NodePredicateScopeAssessmentUnknownIsNotSourceUncertainty(t *testing.T) {
	t.Parallel()
	source := "Whether Beacon tally participates in night admission is explicitly unknown."
	client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("scope#1", "Participation is unknown.", source)}, {text: nodeUnknown}, {text: nodeUnknown}}}
	out := FuseNodeDescriptions(context.Background(), client, 4096, 1, "operations", "scope", []NodeDescriptionInput{v016ScopeInput(source)}).Nodes[0]
	if out.Outcome != annotation.DocumentUnavailable || !out.Exhausted || len(out.Attempts) != 3 || out.Attempts[1].Step != "assess" || out.Attempts[2].Step != "assess" || out.Attempts[1].CandidateID != out.Attempts[2].CandidateID || !reflect.DeepEqual(client.requests[1], client.requests[2]) {
		t.Fatalf("unknown assessment revised or promoted candidate: %+v", out)
	}
}

// 广/窄否定、真实未知和真实分歧可各自忠实保留，不把Detail当万能胜者。
func TestV016NodePredicateScopeFaithfulBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, source, summary, detail string }{
		{"broad_exclusion", "Beacon tally counts paper kites after closing. It does not participate in admission.", "Beacon tally does not participate in admission.", "Beacon tally counts paper kites after closing. It does not participate in admission."},
		{"subclass_only", "Beacon tally does not participate in admission's inventory task; other admission participation is unspecified.", "Only inventory-task participation is excluded.", "Beacon tally does not participate in admission's inventory task; other admission participation is unspecified."},
		{"responsibility_is_not_blanket_nonparticipation", "Beacon tally is only responsible for after-closing inventory; its participation in other activities is not specified.", "Its responsibility is limited to after-closing inventory.", "Beacon tally is only responsible for after-closing inventory; its participation in other activities is not specified."},
		{"faithful_unknown", "Whether Beacon tally participates in admission is explicitly unknown.", "Participation is unknown.", "Whether Beacon tally participates in admission is explicitly unknown."},
		{"attributed_disagreement", "Source A Content says Beacon tally participates in admission; Source B Detail denies that participation. The disagreement is unresolved.", "Participation is disputed between sources.", "Source A Content says Beacon tally participates in admission; Source B Detail denies that participation. The disagreement is unresolved."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("scope#1", tc.summary, tc.detail)}, {text: nodeSupported}}}
			out := FuseNodeDescriptions(context.Background(), client, 4096, 1, "operations", "scope", []NodeDescriptionInput{v016ScopeInput(tc.source)}).Nodes[0]
			if out.Outcome != annotation.DocumentComplete || out.Exhausted || out.Summary != tc.summary || out.Description != tc.detail || len(client.requests) != 2 {
				t.Fatalf("faithful scope/uncertainty/disagreement changed: %+v", out)
			}
		})
	}
}
