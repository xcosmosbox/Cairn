package extract

import (
	"context"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
)

const oldPermitInvalid = "Expired Beacon permits are invalid."
const newPermitRejection = "Evening entry still rejects expired Beacon permits."

func oldMaterialNode() NodeDescriptionInput {
	return NodeDescriptionInput{ID: "stable-expired-permit", Label: "Concept", Name: "Expired Beacon permits", ExistingDescription: oldPermitInvalid + " Evening entry rejects expired Beacon permits.", Material: "member new-1 (rules.md): " + newPermitRejection}
}

// The source denominator is the same provided old+new material in generation,
// assessment and revision. Supplied verdicts test flow, not model efficacy.
func TestV016OldMachineMaterialMissingFactRepairAndExhaustion(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		name := "repaired"
		if exhausted {
			name = "exhausted"
		}
		t.Run(name, func(t *testing.T) {
			n := oldMaterialNode()
			feedback := `{"supported":false,"reason":"provided old machine description explicitly states invalidity; the new member's omission does not retract it","unsupported_items":[],"omissions":["retain Expired Beacon permits are invalid from the supplied old machine description"]}`
			good := oldPermitInvalid + " " + newPermitRejection
			revised, verdict := good, nodeSupported
			if exhausted {
				revised, verdict = newPermitRejection, feedback
			}
			client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("scope#1", newPermitRejection, newPermitRejection)}, {text: feedback}, {text: nodeWire("scope#1", revised, revised)}, {text: verdict}}}
			out := FuseNodeDescriptions(context.Background(), client, 4096, 1, "operations", "scope", []NodeDescriptionInput{n}).Nodes[0]
			if len(out.Attempts) != 4 || len(client.requests) != 4 || out.Attempts[2].Step != "revise" {
				t.Fatalf("old fact did not use the original finite flow: %+v", out)
			}
			for _, req := range client.requests {
				if !strings.Contains(req.User, n.ExistingDescription) || !strings.Contains(req.User, n.Material) {
					t.Fatal("a stage lost supplied old or new material")
				}
			}
			for _, text := range []string{newPermitRejection, "provided old machine description explicitly states invalidity", "omissions", oldPermitInvalid} {
				if !strings.Contains(client.requests[2].User, text) {
					t.Fatalf("revision lost complete candidate or concrete old-fact critique: %s", text)
				}
			}
			if exhausted {
				if out.Outcome != annotation.DocumentPartial || !out.Exhausted {
					t.Fatalf("omitted old fact authorized replacement: %+v", out)
				}
			} else if out.Outcome != annotation.DocumentComplete || out.Exhausted || out.ID != n.ID || out.Summary != good || out.Description != good {
				t.Fatalf("old+new facts not retained: %+v", out)
			}
		})
	}
}

func TestV016OldMachineMaterialPairedPresenceAndCandidateIsNotSource(t *testing.T) {
	for _, supplied := range []bool{true, false} {
		name := "provided_old_supports_fact"
		if !supplied {
			name = "candidate_cannot_invent_absent_old_fact"
		}
		t.Run(name, func(t *testing.T) {
			n := oldMaterialNode()
			n.Summary, n.Description = oldPermitInvalid+" "+newPermitRejection, oldPermitInvalid+" "+newPermitRejection
			verdict := nodeSupported
			if !supplied {
				n.ExistingDescription = ""
				verdict = `{"supported":false,"reason":"neither supplied material section states invalidity; candidate text is not evidence","unsupported_items":[0],"omissions":[]}`
			}
			client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: verdict}}}
			out := FuseNodeDescriptions(context.Background(), client, 4096, -1, "operations", "scope", []NodeDescriptionInput{n}).Nodes[0]
			if len(client.requests) != 1 || out.Attempts[0].Step != "assess" || (out.Outcome == annotation.DocumentComplete) != supplied {
				t.Fatalf("provided-old/candidate evidence boundary changed: %+v", out)
			}
			parts := strings.SplitN(client.requests[0].User, "## 标注单元（数组下标从0开始） / annotated items\n", 2)
			if len(parts) != 2 || strings.Contains(parts[0], oldPermitInvalid) != supplied || !strings.Contains(parts[1], oldPermitInvalid) || !strings.Contains(parts[0], n.Material) {
				t.Fatal("old material and candidate assertions were not kept separate")
			}
		})
	}
}

func TestV016OldMachineMaterialWrongFeedbackCannotReplaceEvidence(t *testing.T) {
	n := oldMaterialNode()
	good := oldPermitInvalid + " " + newPermitRejection
	wrongCritique := `{"supported":false,"reason":"new member does not repeat invalidity, so remove it","unsupported_items":[0],"omissions":[]}`
	client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("scope#1", good, good)}, {text: wrongCritique}, {text: nodeWire("scope#1", good, good)}, {text: nodeSupported}}}
	out := FuseNodeDescriptions(context.Background(), client, 4096, 1, "operations", "scope", []NodeDescriptionInput{n}).Nodes[0]
	if out.Outcome != annotation.DocumentComplete || out.Description != good || len(client.requests) != 4 || out.Attempts[2].Step != "revise" || out.Attempts[0].CandidateID != out.Attempts[2].CandidateID {
		t.Fatalf("faithful unchanged candidate could not be rechecked: %+v", out)
	}
	for _, value := range []string{n.ExistingDescription, n.Material, good, "new member does not repeat invalidity, so remove it"} {
		if !strings.Contains(client.requests[2].User, value) {
			t.Fatalf("revision scope lost evidence/candidate/critique: %s", value)
		}
	}
}

func TestV016OldMachineMaterialExplicitChangesAndDisagreement(t *testing.T) {
	for _, tc := range []struct{ name, nodeName, old, incoming, detail string }{
		{"qualified_update", "Beacon permit", "In January, Beacon permit validity is9minutes.", "In February, validity is6minutes;9minutes remains January-only.", "In January, Beacon permit validity is9minutes. In February, validity is6minutes;9minutes remains January-only."},
		{"explicit_correction", "Beacon permit", "The old report recorded Beacon permit validity as9minutes but did not identify a version.", "Correction: the earlier unqualified9minute Beacon permit validity report was mistaken; the actual Beacon permit validity is6minutes.", "The old report's9minute Beacon permit validity did not identify a version and is explicitly corrected as mistaken; the actual Beacon permit validity is6minutes."},
		{"unresolved_conflict", "Expired Beacon permits", "Source A says expired Beacon permits are invalid; no date is supplied.", "Source B says expired Beacon permits remain valid; no date or resolution is supplied.", "Source A says expired Beacon permits are invalid; Source B says they remain valid. Neither gives a date; the disagreement is unresolved."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := oldMaterialNode()
			n.Name = tc.nodeName
			n.ExistingDescription, n.Material = tc.old, tc.incoming
			client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("scope#1", tc.detail, tc.detail)}, {text: nodeSupported}}}
			out := FuseNodeDescriptions(context.Background(), client, 4096, -1, "operations", "scope", []NodeDescriptionInput{n}).Nodes[0]
			if out.Outcome != annotation.DocumentComplete || out.Description != tc.detail || len(client.requests) != 2 || !strings.Contains(client.requests[1].User, tc.old) || !strings.Contains(client.requests[1].User, tc.incoming) {
				t.Fatalf("old material was mechanically preferred or real limits lost: %+v", out)
			}
		})
	}
}

func TestV016OldMachineMaterialExhaustionKeepsSuccessfulSibling(t *testing.T) {
	logger := NodeDescriptionInput{ID: "independent-logger", Label: "Entity", Name: "Logger", Material: "Logger records inventory only."}
	n := oldMaterialNode()
	feedback := `{"supported":false,"reason":"supplied old invalidity omitted","unsupported_items":[],"omissions":["retain old invalidity"]}`
	client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("scope#1", "Logger records inventory only.", "Logger records inventory only.")}, {text: nodeSupported}, {text: nodeWire("scope#2", newPermitRejection, newPermitRejection)}, {text: feedback}, {text: nodeWire("scope#2", newPermitRejection, newPermitRejection)}, {text: feedback}}}
	out := FuseNodeDescriptions(context.Background(), client, 4096, 1, "operations", "scope", []NodeDescriptionInput{logger, n})
	if len(out.Nodes) != 2 || out.Nodes[0].Outcome != annotation.DocumentComplete || len(out.Nodes[0].Attempts) != 2 || out.Nodes[1].Outcome != annotation.DocumentPartial || !out.Nodes[1].Exhausted || len(client.requests) != 6 {
		t.Fatalf("failed old-fact revision erased or reran sibling: %+v", out)
	}
	if out.Nodes[0].Description != "Logger records inventory only." {
		t.Fatal("completed sibling changed")
	}
}
