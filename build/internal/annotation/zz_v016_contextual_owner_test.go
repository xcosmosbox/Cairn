package annotation_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

// Scripted semantic rows exercise existing finite revision and preservation;
// neither these tests nor their parent passes establish model efficacy.
func TestV016ContextualOwnerRevisionPreservesFiveItems(t *testing.T) {
	const original = "Cobalt permit is used for evening entry. The January configuration has a validity of9minutes. The February configuration has a validity of6minutes. Expired permits are invalid. Evening entry rejects expired permits."
	items := []dktypes.AnnotatedItem{
		subjectCoreItem(dktypes.TagEntity, "Cobalt permit", "Cobalt permit is used for evening entry. Its validity is6minutes."),
		subjectCoreItem(dktypes.TagEntity, "January configuration", "Cobalt permit's January configuration has a validity of9minutes."),
		subjectCoreItem(dktypes.TagConcept, "Expired permits", "Expired permits are invalid."),
		subjectCoreItem(dktypes.TagConcept, "Evening entry", "Evening entry rejects expired permits."),
		subjectCoreItem(dktypes.TagEntity, "February configuration", "Cobalt permit's February configuration has a validity of6minutes."),
	}
	good := append([]dktypes.AnnotatedItem(nil), items...)
	good[0] = subjectCoreItem(dktypes.TagEntity, "Cobalt permit", "Cobalt permit is used for evening entry. Its validity is9minutes in January and6minutes in February.")
	reject := subjectCoreJSON(map[string]any{"supported": false, "reason": "the unqualified6minutes claim incorrectly applies February's value to all configurations", "unsupported_items": []int{0}, "omissions": []string{}})
	for _, exhausted := range []bool{false, true} {
		revision, verdict := subjectCoreRows(good), subjectCoreSupported
		if exhausted {
			revision, verdict = subjectCoreRows(items), reject
		}
		client := &subjectCoreScript{t: t, replies: []string{subjectCoreRows(items), reject, revision, verdict}}
		out := annotation.NewDocumentProcessor(client, 4096, 1, 0).Process(context.Background(), "operations", "references/permit.md", original)
		if len(out.Attempts) != 4 || len(client.calls) != 4 || out.Attempts[2].Step != "revise" || !strings.Contains(client.calls[2].User, original) {
			t.Fatalf("semantic correction lost finite whole-source flow: %+v", out)
		}
		if exhausted {
			if out.Outcome != annotation.DocumentPartial || !out.Exhausted {
				t.Fatalf("unqualified false value accepted: %+v", out)
			}
			continue
		}
		if out.Outcome != annotation.DocumentComplete || out.Exhausted || out.Document == nil || !reflect.DeepEqual(out.Document.Items, good) || !reflect.DeepEqual(out.Document.Items[1:], items[1:]) {
			t.Fatalf("semantic repair changed healthy material: %+v", out)
		}
		if !strings.Contains(client.calls[3].User, original) || !strings.Contains(client.calls[3].User, good[0].Detail) {
			t.Fatal("replacement was not wholly reassessed")
		}
	}
}

func TestV016ContextualOwnerIndependentAndUnknownBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, original string
		items          []dktypes.AnnotatedItem
		proof          []map[string]any
	}{
		{
			name:     "independent_deployments_not_platform_attributes",
			original: "Cobalt is a platform. Cobalt v1 and Cobalt v2 are independently deployed services with separate identities. Cobalt v1 permits9minutes; Cobalt v2 permits6minutes.",
			items:    []dktypes.AnnotatedItem{subjectCoreItem(dktypes.TagEntity, "Cobalt", "Cobalt is a platform."), subjectCoreItem(dktypes.TagEntity, "Cobalt v1", "Cobalt v1 is an independently deployed service, distinct from Cobalt v2. It permits9minutes."), subjectCoreItem(dktypes.TagEntity, "Cobalt v2", "Cobalt v2 is an independently deployed service, distinct from Cobalt v1. It permits6minutes.")},
			proof:    []map[string]any{subjectCoreRow("Cobalt is a platform.", "Cobalt", 0, "Cobalt", true, true), subjectCoreRow("Cobalt v1 and Cobalt v2 are independently deployed services with separate identities.", "Cobalt v1", 1, "Cobalt v1", true, true), subjectCoreRow("Cobalt v1 and Cobalt v2 are independently deployed services with separate identities.", "Cobalt v2", 2, "Cobalt v2", true, true), subjectCoreRow("Cobalt v1 permits9minutes", "Cobalt v1", 1, "Cobalt v1", true, true), subjectCoreRow("Cobalt v2 permits6minutes.", "Cobalt v2", 2, "Cobalt v2", true, true)},
		},
		{
			name:     "two_owners_no_guessed_attribution",
			original: "Cobalt permit and Birch permit are distinct. The January configuration has a validity of9minutes; this note does not identify its owner.",
			items:    []dktypes.AnnotatedItem{subjectCoreItem(dktypes.TagEntity, "Cobalt permit", "Cobalt permit and Birch permit are distinct."), subjectCoreItem(dktypes.TagEntity, "Birch permit", "Cobalt permit and Birch permit are distinct."), subjectCoreItem(dktypes.TagConcept, "Unattributed January configuration", "The January configuration has a validity of9minutes; this note does not identify its owner."), subjectCoreItem(dktypes.TagConcept, "This note", "This note does not identify the owner of the January configuration.")},
			proof:    []map[string]any{subjectCoreRow("Cobalt permit and Birch permit are distinct.", "Cobalt permit", 0, "Cobalt permit", true, true), subjectCoreRow("Cobalt permit and Birch permit are distinct.", "Birch permit", 1, "Birch permit", true, true), subjectCoreRow("The January configuration has a validity of9minutes; this note does not identify its owner.", "Unattributed January configuration", 2, "Unattributed January configuration", true, true), subjectCoreRow("The January configuration has a validity of9minutes; this note does not identify its owner.", "This note", 3, "This note", true, true)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &subjectCoreScript{t: t, replies: []string{subjectCoreRows(tc.items), subjectCoreEvidence(tc.proof...)}}
			out := annotation.NewDocumentProcessor(client, 4096, -1, 0).Process(context.Background(), "operations", "references/boundary.md", tc.original)
			if out.Outcome != annotation.DocumentComplete || len(client.calls) != 2 || !reflect.DeepEqual(out.Document.Items, tc.items) {
				t.Fatalf("independent/unknown identity was forced: %+v", out)
			}
		})
	}
}

func TestV016ContextualOwnerNodeDoesNotImportAbsentWholeSource(t *testing.T) {
	purpose := "Cobalt permit is used for evening entry."
	material := "member a (permit.md): " + purpose + "\nmember b (guide.md): Evening entry requires a valid Cobalt permit.\nmember c (log.md): This page's permit means Cobalt permit."
	detail := purpose + " Evening entry requires a valid Cobalt permit. This page's permit means Cobalt permit."
	client := &subjectCoreScript{t: t, replies: []string{subjectCoreSupported}}
	result := extract.FuseNodeDescriptions(context.Background(), client, 4096, -1, "operations", "scope", []extract.NodeDescriptionInput{{ID: "existing-owner", Label: "Entity", Name: "Cobalt permit", Summary: purpose, Description: detail, Material: material}})
	if len(result.Nodes) != 1 || result.Nodes[0].Outcome != annotation.DocumentComplete || result.Nodes[0].Description != detail || len(client.calls) != 1 {
		t.Fatalf("node scope imported missing owner attribute or retried: %+v", result)
	}
	if !strings.Contains(client.calls[0].User, material) || strings.Contains(client.calls[0].User, "9minutes") || strings.Contains(client.calls[0].User, "January") {
		t.Fatal("node assessment included an unprovided whole-document attribute")
	}
}
