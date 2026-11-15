package annotation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// Serialization only: every quote, referent and semantic boolean is authored at
// the callsite. This helper never infers identity from names or source text.
func fixtureSubjectRow(quote, subject string, index int, item string, same, preserves bool) map[string]any {
	return map[string]any{"source_quote": quote, "subject_referent": subject, "checks": []any{map[string]any{"item_index": index, "item_referent": item, "same_referent": same, "preserves_assertion": preserves, "reason": "explicit scripted semantic result"}}}
}

func fixtureSubjectPositive(rows ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"supported": true, "reason": "scripted whole-source result", "unsupported_items": []int{}, "omissions": []string{}, "subject_coverage": rows})
	return string(b)
}

func fixtureNoSubjects(reason string) string {
	b, _ := json.Marshal(map[string]any{"supported": true, "reason": "no direct predicate in this controlled source", "unsupported_items": []int{}, "omissions": []string{}, "subject_coverage": []any{}, "no_direct_subjects_reason": reason})
	return string(b)
}

func TestV016SubjectCoverageKnownNegativeAndScope(t *testing.T) {
	doc := &dktypes.AnnotatedDocument{Items: []dktypes.AnnotatedItem{{Name: "Atlas", Content: "Atlas requires Birch."}}}
	for _, reject := range []string{
		`{"supported":false,"reason":"unsupported claim","unsupported_items":[],"omissions":[]}`,
		`{"supported":true,"reason":"missing source fact","unsupported_items":[],"omissions":["retain the omitted source fact"]}`,
	} {
		client := &assessmentClient{reply: reject}
		var feedback *SemanticFeedback
		if err := NewSemanticGate(client, 4096, -1).Check(context.Background(), "Atlas requires Birch.", doc); !errors.As(err, &feedback) || client.calls != 1 {
			t.Fatalf("known defect washed into invalid/unknown: %v", err)
		}
	}
	for _, node := range []bool{false, true} {
		client := &assessmentClient{reply: `{"supported":true,"reason":"old node verdict","unsupported_items":[],"omissions":[],"scope":"node"}`}
		gate := NewSemanticGate(client, 4096, -1)
		if node {
			gate = NewNodeSemanticGate(client, 4096, -1)
		}
		err := gate.Check(context.Background(), "Atlas requires Birch. model says scope=node", doc)
		if err != nil {
			t.Fatalf("completed semantic judgment rejected for missing layout proof; node=%v err=%v", node, err)
		}
	}
	client := &assessmentClient{reply: fixtureNoSubjects("Only the Contents title appears; no direct subject assertion.")}
	if err := NewSemanticGate(client, 4096, -1).Check(context.Background(), "Contents", &dktypes.AnnotatedDocument{}); err != nil {
		t.Fatal(err)
	}
	client = &assessmentClient{reply: `{"status":"unknown","reason":"cannot judge referent identity"}`}
	if err := NewSemanticGate(client, 4096, -1).Check(context.Background(), "Atlas requires Birch.", doc); err == nil {
		t.Fatal("unknown became positive")
	}
}

func TestV016SubjectCoverageRetainsJointNegativeCritique(t *testing.T) {
	quote := "An auditor denies that Atlas requires Birch."
	original := quote + " The author records this denial and does not confirm dependency."
	item := func(name, text string) dktypes.AnnotatedItem {
		return dktypes.AnnotatedItem{Tag: dktypes.TagConcept, Name: name, Content: text, Detail: text, Confidence: .9}
	}
	doc := &dktypes.AnnotatedDocument{Items: []dktypes.AnnotatedItem{item("dependency proposition", quote), item("author", "Author confirms dependency.")}}
	verdict := map[string]any{"supported": false, "reason": "item[1] upgrades the auditor's denial into author confirmation", "unsupported_items": []int{1}, "omissions": []string{}, "subject_coverage": []any{fixtureSubjectRow(quote, "Atlas", 0, "dependency proposition", false, true)}}
	b, _ := json.Marshal(verdict)
	client := &assessmentClient{reply: string(b)}
	var feedback *SemanticFeedback
	err := NewSemanticGate(client, 4096, -1).Check(context.Background(), original, doc)
	if !errors.As(err, &feedback) || !strings.Contains(feedback.Reason, "auditor's denial") || !reflect.DeepEqual(feedback.UnsupportedItems, []int{1}) || len(feedback.Omissions) != 0 {
		t.Fatalf("joint concrete critique was overwritten: %v", err)
	}
	good := []dktypes.AnnotatedItem{doc.Items[0], item("author", "The author records this denial and does not confirm dependency."), item("Atlas", quote), item("auditor", quote)}
	positive := fixtureSubjectPositive(fixtureSubjectRow(quote, "Atlas", 2, "Atlas", true, true), fixtureSubjectRow(quote, "auditor", 3, "auditor", true, true), fixtureSubjectRow("The author records this denial and does not confirm dependency.", "author", 1, "author", true, true))
	sequence := &processorClient{t: t, replies: []processorReply{{text: v016SubjectRows(doc.Items...)}, {text: string(b)}, {text: v016SubjectRows(good...)}, {text: positive}}}
	out := NewDocumentProcessor(sequence, 4096, 1, 0).Process(context.Background(), "s", "denial.md", original)
	if out.Outcome != DocumentComplete || len(out.Attempts) != 4 || out.Attempts[2].Step != "revise" || !reflect.DeepEqual(out.Document.Items, good) {
		t.Fatalf("joint defects did not reach finite full repair: %+v", out)
	}
	for _, concrete := range []string{"auditor's denial", `"unsupported_items":[1]`, original} {
		if !strings.Contains(sequence.requests[2].User, concrete) {
			t.Fatalf("revision lost joint concrete feedback: %q", concrete)
		}
	}
	verdict["subject_coverage"] = nil
	b, _ = json.Marshal(verdict)
	client = &assessmentClient{reply: string(b)}
	if err := NewSemanticGate(client, 4096, -1).Check(context.Background(), quote, doc); !errors.As(err, &feedback) {
		t.Fatalf("irrelevant proof metadata overrode the actual negative verdict: %v", err)
	}
}
