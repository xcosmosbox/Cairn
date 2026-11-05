package annotation

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

func v016SubjectRows(items ...dktypes.AnnotatedItem) string {
	b, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func v016SubjectItem(name, detail string) dktypes.AnnotatedItem {
	return dktypes.AnnotatedItem{Tag: dktypes.TagEntity, Name: name, Content: detail, Detail: detail, Confidence: .9}
}

// 原故障形态：数值仍在独立属性/日期单元中，真实主体却只剩定义，后续成员融合无从补回。
// Scripted judgments verify existing finite revision/data flow, not model efficacy of the prompts.
func TestV016SubjectAttributeFeedbackRevisesWholeDocument(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, original, subject, definition, attribute, fact string }{
		{"ticket_duration", "霜羽票签是纸岚展馆夜场使用的票签。2026年1月版的有效期为9分钟。过期票签无效。", "霜羽票签", "纸岚展馆夜场使用的票签。", "霜羽票签2026年1月版有效期", "霜羽票签2026年1月版的有效期为9分钟；过期票签无效。"},
		{"count_quantity", "檐铃清点只负责闭馆后清点纸鹤，不参与夜场入厅。檐铃清点在2026年1月版每次清点4只纸鹤。", "檐铃清点", "只负责闭馆后清点纸鹤，不参与夜场入厅。", "2026年1月版", "檐铃清点只负责闭馆后清点纸鹤，不参与夜场入厅；在2026年1月版每次清点4只纸鹤。"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, exhausted := range []bool{false, true} {
				name := "repaired"
				if exhausted {
					name = "exhausted"
				}
				t.Run(name, func(t *testing.T) {
					bad := v016SubjectRows(v016SubjectItem(tc.subject, tc.definition), v016SubjectItem(tc.attribute, tc.fact))
					good := v016SubjectRows(v016SubjectItem(tc.subject, tc.definition+" "+tc.fact))
					feedback := "主体 " + tc.subject + " 只有定义；把其直接属性连同时间、数量、否定限定保留在该主体单元，不能只留在 " + tc.attribute
					verdict, err := json.Marshal(map[string]any{"supported": true, "reason": feedback, "unsupported_items": []int{}, "omissions": []string{feedback}})
					if err != nil {
						t.Fatal(err)
					}
					revision, finalVerdict := good, v012AcceptanceSupported
					if exhausted {
						revision, finalVerdict = bad, string(verdict)
					}
					client := &v012AcceptanceClient{t: t, replies: []string{bad, string(verdict), revision, finalVerdict}}
					result := NewDocumentProcessor(client, 0, 1, 0).Process(context.Background(), "mini", "references/"+tc.name+".md", tc.original)
					if len(client.calls) != 4 || len(result.Attempts) != 4 {
						t.Fatalf("shared allowance reset: %+v", result)
					}
					for i, step := range []string{"annotate", "assess", "revise", "assess"} {
						if result.Attempts[i].Step != step || result.Attempts[i].Number != i+1 {
							t.Fatalf("wrong failed-step recovery: %+v", result.Attempts)
						}
					}
					for _, text := range []string{tc.original, feedback, tc.attribute, tc.subject} {
						if !strings.Contains(client.calls[2].User, text) {
							t.Fatalf("revision lost original/candidate/ownership critique: %s", text)
						}
					}
					if !strings.Contains(client.calls[3].User, tc.original) || !strings.Contains(client.calls[3].User, tc.fact) {
						t.Fatal("revision was not assessed as a whole candidate against the original")
					}
					if exhausted {
						if result.Outcome != DocumentPartial || !result.Exhausted || !strings.Contains(result.Reason, tc.subject) {
							t.Fatalf("unrepaired ownership promoted: %+v", result)
						}
						return
					}
					if result.Outcome != DocumentComplete || result.Exhausted || result.Document == nil || len(result.Document.Items) != 1 || result.Document.Items[0].Name != tc.subject || result.Document.Items[0].Detail != tc.definition+" "+tc.fact || result.Attempts[1].CandidateID == result.CandidateID {
						t.Fatalf("subject fact was not retained in the accepted replacement: %+v", result)
					}
				})
			}
		})
	}
}

// 真正独立的版本对象、不同主体、不同日期值和否定不能因属性归属修订而被硬并。
// No programmatic name/date merge is introduced; accepted distinct source identities survive verbatim.
func TestV016SubjectAttributesPreserveDistinctObjectsAndDatedFacts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, original string
		items          []dktypes.AnnotatedItem
	}{
		{"distinct_versions", "Lark v1 and Lark v2 are separate deployed services. Lark v1 permits 9 minutes; Lark v2 permits 6 minutes and cannot use v1 tickets.", []dktypes.AnnotatedItem{v016SubjectItem("Lark v1", "A separate deployed service permitting 9 minutes."), v016SubjectItem("Lark v2", "A separate deployed service permitting 6 minutes; it cannot use v1 tickets.")}},
		{"different_subjects", "Atlas ticket expires after 9 minutes. Birch ticket expires after 4 minutes.", []dktypes.AnnotatedItem{v016SubjectItem("Atlas ticket", "Expires after 9 minutes."), v016SubjectItem("Birch ticket", "Expires after 4 minutes.")}},
		{"dated_values_and_negation", "Atlas ticket permits 9 minutes in January and 6 minutes in February. January's value must not be applied in February.", []dktypes.AnnotatedItem{v016SubjectItem("Atlas ticket", "Permits 9 minutes in January and 6 minutes in February. January's value must not be applied in February.")}},
		{"speaker_and_condition", "An auditor claims Cedar cache retains records for 9 minutes only while its seal is closed. The author does not confirm that claim. Hazel cache retains records for 4 minutes.", []dktypes.AnnotatedItem{v016SubjectItem("Cedar cache", "An auditor claims it retains records for 9 minutes only while its seal is closed; the author does not confirm this claim."), v016SubjectItem("Hazel cache", "Retains records for 4 minutes.")}},
		{"known_subject_unknown_value", "Atlas ticket has a validity period whose value is explicitly unknown.", []dktypes.AnnotatedItem{v016SubjectItem("Atlas ticket", "Its validity period is explicitly unknown.")}},
		{"faithfully_unattributed_fact", "Atlas and Birch are separate tickets. A loose note says a ticket expires in 9 minutes, without identifying which one.", []dktypes.AnnotatedItem{v016SubjectItem("Atlas ticket", "Atlas and Birch are separate tickets."), v016SubjectItem("Birch ticket", "Atlas and Birch are separate tickets."), v016SubjectItem("Unattributed note", "A ticket expires in 9 minutes; the note does not identify which one.")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &v012AcceptanceClient{t: t, replies: []string{v016SubjectRows(tc.items...), v012AcceptanceSupported}}
			result := NewDocumentProcessor(client, 0, 1, 0).Process(context.Background(), "subjects", tc.name+".md", tc.original)
			if result.Outcome != DocumentComplete || result.Document == nil || len(client.calls) != 2 || !reflect.DeepEqual(result.Document.Items, tc.items) {
				t.Fatalf("distinct source meaning changed: %+v", result)
			}
		})
	}
}

// 核验者无法判定候选归属是否忠实时，只能重核同一候选；原文含混本身并非失败条件。
// An assessor's inability to verify attribution is distinct from faithfully preserved source uncertainty.
func TestV016UnknownSubjectAssessmentRechecksSameCandidate(t *testing.T) {
	t.Parallel()
	for _, original := range []string{
		"Atlas and Birch are separate tickets. A loose note says it expires in 9 minutes, without identifying which ticket.",
		"Two separate tickets share the name Cedar. An unsigned note says Cedar expires in 9 minutes, without identifying which ticket.",
	} {
		t.Run(original, func(t *testing.T) {
			candidate := v016SubjectRows(v016SubjectItem("Unattributed note", "It expires in 9 minutes; the source does not identify which ticket."))
			unknown := `{"status":"unknown","reason":"this assessment could not determine whether the candidate faithfully preserves attribution and uncertainty"}`
			client := &v012AcceptanceClient{t: t, replies: []string{candidate, unknown, unknown}}
			result := NewDocumentProcessor(client, 0, 1, 0).Process(context.Background(), "subjects", "ambiguous.md", original)
			if result.Outcome != DocumentUnavailable || !result.Exhausted || len(client.calls) != 3 || len(result.Attempts) != 3 || result.Attempts[1].Step != "assess" || result.Attempts[2].Step != "assess" || result.Attempts[1].CandidateID != result.Attempts[2].CandidateID || !reflect.DeepEqual(client.calls[1], client.calls[2]) {
				t.Fatalf("ambiguous attribution was revised or accepted: %+v", result)
			}
		})
	}
}

// 真实无日期反例中，依赖句在票签和独立命题中，却没有夜场入厅主体自己的单元。
// Scripted feedback adds subject-bearing material without merging away a separate proposition.
func TestV016UndatedSubjectPredicateOmissionRevisesWholeDocument(t *testing.T) {
	t.Parallel()
	original := "# 夜场记录\n本页的“票签”指霜羽票签。夜场入厅依赖霜羽票签。\n檐铃清点在2026年1月版每次清点4只纸鹤。"
	statement := v016SubjectItem("夜场入厅依赖霜羽票签", "夜场入厅依赖霜羽票签。")
	statement.Tag = dktypes.TagConcept
	items := []dktypes.AnnotatedItem{
		v016SubjectItem("霜羽票签", "本页的“票签”指霜羽票签。夜场入厅依赖霜羽票签。"),
		statement,
		v016SubjectItem("檐铃清点", "檐铃清点在2026年1月版每次清点4只纸鹤。"),
	}
	entry := v016SubjectItem("夜场入厅", "夜场入厅依赖霜羽票签。")
	entry.Tag = dktypes.TagConcept
	correct := append(append([]dktypes.AnnotatedItem{}, items...), entry)
	feedback := "无日期的直接依赖句缺少被陈述主体夜场入厅自己的单元；补回其依赖霜羽票签的完整断言，保留独立命题、票签指代和清点的日期数量，不交换关系方向。"
	verdict, err := json.Marshal(map[string]any{"supported": true, "reason": feedback, "unsupported_items": []int{}, "omissions": []string{feedback}})
	if err != nil {
		t.Fatal(err)
	}
	for _, exhausted := range []bool{false, true} {
		name := "repaired"
		if exhausted {
			name = "exhausted"
		}
		t.Run(name, func(t *testing.T) {
			revised, final := v016SubjectRows(correct...), v012AcceptanceSupported
			if exhausted {
				revised, final = v016SubjectRows(items...), string(verdict)
			}
			client := &v012AcceptanceClient{t: t, replies: []string{v016SubjectRows(items...), string(verdict), revised, final}}
			result := NewDocumentProcessor(client, 0, 1, 0).Process(context.Background(), "paperhall-mini", "mini/references/log.md", original)
			if len(client.calls) != 4 || len(result.Attempts) != 4 || result.Attempts[2].Step != "revise" || result.Attempts[3].Step != "assess" {
				t.Fatalf("wrong finite recovery: %+v", result)
			}
			if !strings.Contains(client.calls[2].User, original) || !strings.Contains(client.calls[2].User, feedback) || !strings.Contains(client.calls[3].User, original) {
				t.Fatal("subject repair lost original/whole-candidate feedback")
			}
			if exhausted {
				if result.Outcome != DocumentPartial || !result.Exhausted {
					t.Fatalf("subject omission promoted: %+v", result)
				}
				return
			}
			if result.Outcome != DocumentComplete || result.Exhausted || result.Document == nil || !reflect.DeepEqual(result.Document.Items, correct) {
				t.Fatalf("subject or successful statement material changed: %+v", result)
			}
		})
	}
}

// 主体承载不能变成方向翻转或把受争议的引述关系升级为作者确认。
// Repair the exact attribution/direction defect while preserving the quoted stance and negation.
func TestV016DirectPredicateRepairPreservesRolesAndQuotedStance(t *testing.T) {
	t.Parallel()
	original := "An auditor alleges Entry requires ticket A, not ticket B. The operator disputes this allegation; the dependency is not confirmed."
	bad := v016SubjectRows(v016SubjectItem("Ticket A", "Ticket A requires Entry; the dependency is confirmed."))
	good := v016SubjectRows(v016SubjectItem("Entry", "An auditor alleges Entry requires ticket A, not ticket B. The operator disputes this allegation; the dependency is not confirmed."))
	feedback := "The candidate reverses Entry and ticket A, loses the auditor's attribution, and contradicts the operator's disputed/unconfirmed stance; preserve ticket B negation."
	verdict, err := json.Marshal(map[string]any{"supported": false, "reason": feedback, "unsupported_items": []int{0}, "omissions": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	client := &v012AcceptanceClient{t: t, replies: []string{bad, string(verdict), good, v012AcceptanceSupported}}
	result := NewDocumentProcessor(client, 0, 1, 0).Process(context.Background(), "subjects", "quoted-rule.md", original)
	if result.Outcome != DocumentComplete || result.Document == nil || len(result.Document.Items) != 1 || result.Document.Items[0].Name != "Entry" || result.Document.Items[0].Detail != original || len(client.calls) != 4 || !strings.Contains(client.calls[2].User, feedback) || !strings.Contains(client.calls[3].User, original) {
		t.Fatalf("quoted directed predicate was changed: %+v", result)
	}
}
