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
			var proof []map[string]any
			switch tc.name {
			case "distinct_versions":
				proof = []map[string]any{fixtureSubjectRow("Lark v1 permits 9 minutes", "Lark v1", 0, "Lark v1", true, true), fixtureSubjectRow("Lark v2 permits 6 minutes and cannot use v1 tickets.", "Lark v2", 1, "Lark v2", true, true)}
				proof = append(proof,
					fixtureSubjectRow("Lark v1 and Lark v2 are separate deployed services.", "Lark v1", 0, "Lark v1", true, true),
					fixtureSubjectRow("Lark v1 and Lark v2 are separate deployed services.", "Lark v2", 1, "Lark v2", true, true))
			case "different_subjects":
				proof = []map[string]any{fixtureSubjectRow("Atlas ticket expires after 9 minutes.", "Atlas ticket", 0, "Atlas ticket", true, true), fixtureSubjectRow("Birch ticket expires after 4 minutes.", "Birch ticket", 1, "Birch ticket", true, true)}
			case "dated_values_and_negation", "known_subject_unknown_value":
				proof = []map[string]any{fixtureSubjectRow(tc.original, "Atlas ticket", 0, "Atlas ticket", true, true)}
			case "speaker_and_condition":
				tc.items = append(tc.items, v016SubjectItem("auditor", "An auditor claims Cedar cache retains records for 9 minutes only while its seal is closed."), v016SubjectItem("author", "The author does not confirm that claim."))
				proof = []map[string]any{fixtureSubjectRow("An auditor claims Cedar cache retains records for 9 minutes only while its seal is closed.", "Cedar cache", 0, "Cedar cache", true, true), fixtureSubjectRow("Hazel cache retains records for 4 minutes.", "Hazel cache", 1, "Hazel cache", true, true), fixtureSubjectRow("An auditor claims Cedar cache retains records for 9 minutes only while its seal is closed.", "auditor", 2, "auditor", true, true), fixtureSubjectRow("The author does not confirm that claim.", "author", 3, "author", true, true)}
			case "faithfully_unattributed_fact":
				proof = []map[string]any{fixtureSubjectRow("Atlas and Birch are separate tickets.", "Atlas ticket", 0, "Atlas ticket", true, true), fixtureSubjectRow("Atlas and Birch are separate tickets.", "Birch ticket", 1, "Birch ticket", true, true), fixtureSubjectRow("A loose note says a ticket expires in 9 minutes, without identifying which one.", "Unattributed note", 2, "Unattributed note", true, true)}
			}
			client := &v012AcceptanceClient{t: t, replies: []string{v016SubjectRows(tc.items...), fixtureSubjectPositive(proof...)}}
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

func TestV016DirectPredicateRepairPreservesRolesAndQuotedStance(t *testing.T) {
	t.Parallel()
	original := "An auditor alleges Entry requires ticket A, not ticket B. The operator disputes this allegation; the dependency is not confirmed."
	bad := v016SubjectRows(v016SubjectItem("Ticket A", "Ticket A requires Entry; the dependency is confirmed."))
	good := v016SubjectRows(v016SubjectItem("Entry", "An auditor alleges Entry requires ticket A, not ticket B. The operator disputes this allegation; the dependency is not confirmed."), v016SubjectItem("auditor", "An auditor alleges Entry requires ticket A, not ticket B."), v016SubjectItem("operator", "The operator disputes this allegation; the dependency is not confirmed."))
	feedback := "The candidate reverses Entry and ticket A, loses the auditor's attribution, and contradicts the operator's disputed/unconfirmed stance; preserve ticket B negation."
	verdict, err := json.Marshal(map[string]any{"supported": false, "reason": feedback, "unsupported_items": []int{0}, "omissions": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	client := &v012AcceptanceClient{t: t, replies: []string{bad, string(verdict), good, fixtureSubjectPositive(fixtureSubjectRow("An auditor alleges Entry requires ticket A, not ticket B.", "Entry", 0, "Entry", true, true), fixtureSubjectRow("An auditor alleges Entry requires ticket A, not ticket B.", "auditor", 1, "auditor", true, true), fixtureSubjectRow("The operator disputes this allegation; the dependency is not confirmed.", "operator", 2, "operator", true, true))}}
	result := NewDocumentProcessor(client, 0, 1, 0).Process(context.Background(), "subjects", "quoted-rule.md", original)
	if result.Outcome != DocumentComplete || result.Document == nil || len(result.Document.Items) != 3 || result.Document.Items[0].Name != "Entry" || result.Document.Items[0].Detail != original || len(client.calls) != 4 || !strings.Contains(client.calls[2].User, feedback) || !strings.Contains(client.calls[3].User, original) {
		t.Fatalf("quoted directed predicate was changed: %+v", result)
	}
}
