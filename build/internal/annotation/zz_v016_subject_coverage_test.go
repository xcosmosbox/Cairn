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

func TestV016SubjectCoverageMissingCarrierUsesExistingRevision(t *testing.T) {
	for _, preserves := range []bool{false, true} {
		for _, exhausted := range []bool{false, true} {
			quote := "索引重建仅在获批窗口中执行。"
			item := func(name, content string) dktypes.AnnotatedItem {
				return dktypes.AnnotatedItem{Tag: dktypes.TagConcept, Name: name, Content: content, Detail: content, Confidence: .9}
			}
			first := item("索引重建条件命题", quote)
			if !preserves {
				first = item("索引重建", "索引重建是一项动作。")
			}
			rows := []dktypes.AnnotatedItem{first, item("获批窗口", "窗口是执行条件。")}
			bad := v016SubjectRows(rows...)
			good := v016SubjectRows(append(append([]dktypes.AnnotatedItem(nil), rows...), item("索引重建", quote))...)
			row := fixtureSubjectRow(quote, "索引重建动作", 0, "完整条件命题", false, true)
			if !preserves {
				row = fixtureSubjectRow(quote, "索引重建动作", 0, "索引重建动作", true, false)
			}
			missing := fixtureSubjectPositive(row)
			accepted := fixtureSubjectPositive(fixtureSubjectRow(quote, "索引重建动作", 2, "索引重建动作", true, true))
			if exhausted {
				good, accepted = bad, missing
			}
			client := &processorClient{t: t, replies: []processorReply{{text: bad}, {text: missing}, {text: good}, {text: accepted}}}
			out := NewDocumentProcessor(client, 4096, 1, 0).Process(context.Background(), "s", "rebuild.md", quote)
			if len(out.Attempts) != 4 || len(client.requests) != 4 || out.Attempts[2].Step != "revise" || !strings.Contains(client.requests[2].User, "索引重建动作") || !strings.Contains(client.requests[2].User, quote) {
				t.Fatalf("missing carrier did not follow existing semantic repair: %+v", out)
			}
			if exhausted {
				if out.Outcome != DocumentPartial || !out.Exhausted {
					t.Fatalf("missing carrier accepted: %+v", out)
				}
			} else if out.Outcome != DocumentComplete || len(out.Document.Items) != 3 || !reflect.DeepEqual(out.Document.Items[:2], rows) || out.Document.Items[2].Content != quote {
				t.Fatalf("valid proposition/other endpoint lost: %+v", out)
			}
		}
	}
}

func TestV016SubjectCoverageMalformedReassessesSameCandidate(t *testing.T) {
	valid := fixtureSubjectPositive(fixtureSubjectRow("Atlas requires Birch.", "Atlas", 0, "Atlas", true, true))
	bad := []string{
		`{"supported":true,"reason":"generic pass","unsupported_items":[],"omissions":[]}`,
		strings.Replace(valid, `"subject_coverage":[`, `"subject_coverage":null,"ignored":[`, 1),
		strings.Replace(valid, `"same_referent":true`, `"same_referent":null`, 1),
		strings.Replace(valid, `"same_referent":true`, `"same_referent":true,"same_referent":false`, 1),
		strings.Replace(valid, `"preserves_assertion":true`, `"preserves_assertion":"true"`, 1),
		strings.Replace(valid, `"item_index":0`, `"item_index":null`, 1),
		strings.Replace(valid, `"item_index":0`, `"item_index":1`, 1),
		strings.Replace(valid, `"item_index":0`, `"item_index":-1`, 1),
		strings.Replace(valid, `"item_index":0`, `"item_index":0.5`, 1),
		strings.Replace(valid, `"source_quote":"Atlas requires Birch."`, `"source_quote":"unquoted paraphrase"`, 1),
		strings.Replace(valid, `"subject_referent":"Atlas"`, `"subject_referent":""`, 1),
		strings.Replace(valid, `"item_referent":"Atlas"`, `"item_referent":null`, 1),
		strings.Replace(valid, `"checks":[`, `"checks":null,"ignored":[`, 1),
		strings.TrimSuffix(strings.Replace(valid, `"supported":true`, `"supported":false`, 1), "}") + `,"no_direct_subjects_reason":"conflicting"}`,
		`{"supported":true,"reason":"empty","unsupported_items":[],"omissions":[],"subject_coverage":[]}`,
		`{"status":"unknown","reason":"cannot assess","subject_coverage":[]}`,
	}
	for _, reply := range bad {
		client := &processorClient{t: t, replies: []processorReply{{text: annotationRows("Atlas requires Birch.")}, {text: reply}, {text: valid}}}
		out := NewDocumentProcessor(client, 4096, 1, 0).Process(context.Background(), "s", "one.md", "Atlas requires Birch.")
		if out.Outcome != DocumentComplete || len(out.Attempts) != 3 || out.Attempts[1].Outcome != "invalid" || !reflect.DeepEqual(client.requests[1], client.requests[2]) {
			t.Fatalf("invalid evidence not reassessed on same candidate: %s: %+v", reply, out)
		}
	}
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
		if (err == nil) != node {
			t.Fatalf("trusted scope lost; node=%v err=%v", node, err)
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

func TestV016SubjectCoverageRowAnchorsAndConflictReasons(t *testing.T) {
	quote := "Atlas requires Birch."
	doc := &dktypes.AnnotatedDocument{Items: []dktypes.AnnotatedItem{{Name: "Atlas", Content: quote}}}
	row := fixtureSubjectRow(quote, "Atlas", 0, "Atlas", true, true)
	checks := row["checks"].([]any)
	row["checks"] = append(checks, checks[0])
	duplicate := fixtureSubjectPositive(row)
	valid := fixtureSubjectPositive(fixtureSubjectRow(quote, "Atlas", 0, "Atlas", true, true))
	conflict := strings.TrimSuffix(strings.Replace(valid, `"supported":true`, `"supported":false`, 1), "}") + `,"no_direct_subjects_reason":"conflicting"}`
	for _, tc := range []struct{ reply, reason string }{{duplicate, "duplicate item_index"}, {conflict, "conflicts with no_direct_subjects_reason"}} {
		if !json.Valid([]byte(tc.reply)) {
			t.Fatal("malformed test must reach contract validation as valid JSON")
		}
		client := &assessmentClient{reply: tc.reply}
		err := NewSemanticGate(client, 4096, -1).Check(context.Background(), quote, doc)
		if err == nil || !strings.Contains(err.Error(), tc.reason) {
			t.Fatalf("wrong rejection: %v, want %s", err, tc.reason)
		}
	}
	// The same source quote/index can support several separately declared rows.
	client := &assessmentClient{reply: fixtureSubjectPositive(fixtureSubjectRow(quote, "Atlas", 0, "Atlas", true, true), fixtureSubjectRow(quote, "Atlas", 0, "Atlas", true, true))}
	if err := NewSemanticGate(client, 4096, -1).Check(context.Background(), quote, doc); err != nil {
		t.Fatal(err)
	}
	missing := fixtureSubjectPositive(fixtureSubjectRow(quote, "Atlas", 0, "whole dependency proposition", false, true))
	client = &assessmentClient{reply: missing}
	var feedback *SemanticFeedback
	if err := NewSemanticGate(client, 4096, -1).Check(context.Background(), quote, doc); !errors.As(err, &feedback) || len(feedback.UnsupportedItems) != 0 || len(feedback.Omissions) != 1 || !strings.Contains(feedback.Omissions[0], "whole dependency proposition") || !strings.Contains(feedback.Omissions[0], "same=false preserves=true") {
		t.Fatalf("missing-carrier feedback lost lawful candidate comparisons: %v", err)
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
	if !errors.As(err, &feedback) || !strings.Contains(feedback.Reason, "auditor's denial") || !strings.Contains(feedback.Reason, "without same-referent carriers") || !reflect.DeepEqual(feedback.UnsupportedItems, []int{1}) || len(feedback.Omissions) != 1 {
		t.Fatalf("joint concrete critique was overwritten: %v", err)
	}
	good := []dktypes.AnnotatedItem{doc.Items[0], item("author", "The author records this denial and does not confirm dependency."), item("Atlas", quote), item("auditor", quote)}
	positive := fixtureSubjectPositive(fixtureSubjectRow(quote, "Atlas", 2, "Atlas", true, true), fixtureSubjectRow(quote, "auditor", 3, "auditor", true, true), fixtureSubjectRow("The author records this denial and does not confirm dependency.", "author", 1, "author", true, true))
	sequence := &processorClient{t: t, replies: []processorReply{{text: v016SubjectRows(doc.Items...)}, {text: string(b)}, {text: v016SubjectRows(good...)}, {text: positive}}}
	out := NewDocumentProcessor(sequence, 4096, 1, 0).Process(context.Background(), "s", "denial.md", original)
	if out.Outcome != DocumentComplete || len(out.Attempts) != 4 || out.Attempts[2].Step != "revise" || !reflect.DeepEqual(out.Document.Items, good) {
		t.Fatalf("joint defects did not reach finite full repair: %+v", out)
	}
	for _, concrete := range []string{"auditor's denial", "without same-referent carriers", "dependency proposition", "same=false preserves=true", `"unsupported_items":[1]`, original} {
		if !strings.Contains(sequence.requests[2].User, concrete) {
			t.Fatalf("revision lost joint concrete feedback: %q", concrete)
		}
	}
	verdict["subject_coverage"] = nil
	b, _ = json.Marshal(verdict)
	client = &assessmentClient{reply: string(b)}
	if err := NewSemanticGate(client, 4096, -1).Check(context.Background(), quote, doc); err == nil || !strings.Contains(err.Error(), "missing/null subject_coverage") {
		t.Fatalf("known negative ignored a supplied malformed map: %v", err)
	}
}
