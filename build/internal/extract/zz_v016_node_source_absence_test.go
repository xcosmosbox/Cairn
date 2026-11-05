package extract

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
)

func v016AbsenceInput(material string) NodeDescriptionInput {
	return NodeDescriptionInput{ID: "source-scoped-node", Label: "Concept", Name: "Birch pass", Material: material}
}

// Fixed verdicts test concrete feedback, material and finite handling; they do
// not prove that either old or new prompts detect unsupported absence claims.
func TestV016NodeSourceAbsenceFeedbackKeepsScope(t *testing.T) {
	for _, tc := range []struct{ name, material, bad, good, reason string }{
		{"whole_source_absence", "Member m1 in manual.md: Birch pass January 2026 validity is 9 minutes. Member m2 in policy.md:9 minutes applies only to January 2026.", "The sources state no applicability scope or other conditions.", "Birch pass January 2026 validity is 9 minutes; policy member m2 limits9 minutes to January 2026.", "The candidate promotes a local omission into whole-source absence despite member m2's explicit limit; retain both provided contributions."},
		{"invented_unknown", "Provided member m1: Birch pass January 2026 validity is 9 minutes.", "Birch pass validity is 9 minutes. Whether any conditions exist is unknown.", "Birch pass January 2026 validity is 9 minutes.", "Missing condition information in provided member m1 does not establish a source assertion of uncertainty or unrestricted applicability; remove the invented unknown and retain the dated fact."},
	} {
		for _, repair := range []bool{true, false} {
			name := tc.name + "/exhaustion"
			if repair {
				name = tc.name + "/repair"
			}
			t.Run(name, func(t *testing.T) {
				reject, _ := json.Marshal(map[string]any{"supported": false, "reason": tc.reason, "unsupported_items": []int{0}, "omissions": []string{}})
				final, verdict := tc.bad, string(reject)
				if repair {
					final, verdict = tc.good, nodeSupported
				}
				client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("absence#1", tc.bad, tc.bad)}, {text: string(reject)}, {text: nodeWire("absence#1", final, final)}, {text: verdict}}}
				out := FuseNodeDescriptions(context.Background(), client, 4096, 1, "operations", "absence", []NodeDescriptionInput{v016AbsenceInput(tc.material)}).Nodes[0]
				want := annotation.DocumentPartial
				if repair {
					want = annotation.DocumentComplete
				}
				if out.ID != "source-scoped-node" || out.Outcome != want || out.Exhausted == repair || out.Summary != final || out.Description != final || len(out.Attempts) != 4 || len(client.requests) != 4 {
					t.Fatalf("source-scope defect escaped or allowance changed: %+v", out)
				}
				for i, step := range []string{"generate", "assess", "revise", "assess"} {
					if out.Attempts[i].Step != step || out.Attempts[i].Number != i+1 {
						t.Fatalf("wrong finite flow: %+v", out.Attempts)
					}
				}
				for _, text := range []string{tc.material, tc.bad, tc.reason} {
					if !strings.Contains(client.requests[2].User, text) {
						t.Fatalf("revision lost material/candidate/scoped feedback %q", text)
					}
				}
				if !strings.Contains(client.requests[3].User, tc.material) || !strings.Contains(client.requests[3].User, final) {
					t.Fatal("replacement was not assessed against the same provided material")
				}
				if repair && out.Attempts[1].CandidateID == out.CandidateID {
					t.Fatal("replacement candidate inherited an old assessment identity")
				}
			})
		}
	}
}

func TestV016NodeSourceAbsenceFaithfulBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, material, summary, detail string }{
		{"missing_is_not_unknown", "Provided member m1 states Birch pass January 2026 validity is 9 minutes.", "Birch pass January 2026 validity is 9 minutes.", "Birch pass January 2026 validity is 9 minutes."},
		{"source_explicit_unknown", "Policy member m1 explicitly says Birch pass validity start time is unknown.", "The policy leaves the start time unknown.", "Policy member m1 explicitly says Birch pass validity start time is unknown."},
		{"source_explicit_unregulated", "Policy member m1 explicitly states that this policy does not regulate the validity start time.", "This policy does not regulate the validity start time.", "Policy member m1 explicitly states that this policy does not regulate the validity start time."},
		{"necessary_member_observation", "Provided member m1 states Birch pass January 2026 validity is 9 minutes.", "Birch pass January 2026 validity is 9 minutes.", "Member m1 states Birch pass January 2026 validity is 9 minutes. This provided member m1 does not state a start trigger; this observation is confined to m1."},
		{"attributed_disagreement", "Member m1 Content explicitly says start time is unknown; member m1 Detail explicitly says start is at issuance. Their disagreement is unresolved.", "Member m1 gives conflicting start-time statements.", "Member m1 Content explicitly says start time is unknown; its Detail explicitly says start is at issuance. The disagreement is unresolved."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("absence#1", tc.summary, tc.detail)}, {text: nodeSupported}}}
			out := FuseNodeDescriptions(context.Background(), client, 4096, 1, "operations", "absence", []NodeDescriptionInput{v016AbsenceInput(tc.material)}).Nodes[0]
			if out.Outcome != annotation.DocumentComplete || out.Exhausted || out.Summary != tc.summary || out.Description != tc.detail || len(client.requests) != 2 {
				t.Fatalf("faithful material scope or source uncertainty was changed: %+v", out)
			}
			if !strings.Contains(client.requests[0].System, nodePredicateScopeContract) || !strings.Contains(client.requests[1].System, nodePredicateScopeContract) || !strings.Contains(client.requests[1].User, tc.material) {
				t.Fatal("generation/assessment did not share the existing contract or complete member material")
			}
		})
	}
}

func TestV016NodeSourceAbsenceUnknownAssessmentStaysUnknown(t *testing.T) {
	material := "Member m1 explicitly states that the validity start time is unknown."
	client := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("absence#1", "Start time is unknown.", material)}, {text: nodeUnknown}, {text: nodeUnknown}}}
	out := FuseNodeDescriptions(context.Background(), client, 4096, 1, "operations", "absence", []NodeDescriptionInput{v016AbsenceInput(material)}).Nodes[0]
	if out.Outcome != annotation.DocumentUnavailable || !out.Exhausted || len(out.Attempts) != 3 || out.Attempts[1].Step != "assess" || out.Attempts[2].Step != "assess" || out.Attempts[1].CandidateID != out.Attempts[2].CandidateID || !reflect.DeepEqual(client.requests[1], client.requests[2]) {
		t.Fatalf("unknown fidelity assessment was converted or regenerated: %+v", out)
	}
}
