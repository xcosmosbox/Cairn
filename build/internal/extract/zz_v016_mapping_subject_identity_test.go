package extract

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// 真实失败把同一主体的通用职责与带日期数量分到两个节点；覆盖全部成员并不证明身份正确。
// Scripted replies test input/parsing/member-source flow, not whether a model follows the identity contract.
func TestV016MappingSubjectIdentityMaterialFlow(t *testing.T) {
	t.Parallel()
	type input struct {
		name, content, detail string
		tag                   dktypes.AnnotationTag
	}
	cases := []struct {
		name   string
		input  []input
		groups [][]int
	}{
		{"complementary_subject_facts", []input{
			{"Beacon tally", "Beacon tally counts paper kites after closing; it does not grant admission.", "This installed tally process counts paper kites only after closing and never grants admission.", dktypes.TagEntity},
			{"Beacon tally (January edition)", "In January the same Beacon tally counts four paper kites per round.", "January is the applicability condition for the count of four; this describes the same installed tally process.", dktypes.TagConcept},
			{"Beacon tally", "An auditor reports five per round in February; the operator disputes that count.", "The February report and disputed stance are attributed to their speakers, not a replacement for January's count.", dktypes.TagEntity},
		}, [][]int{{0, 1, 2}}},
		{"same_sentence_different_participants", []input{
			{"Beacon tally", "The counting process.", "Beacon tally counts paper kites only after closing.", dktypes.TagEntity},
			{"Paper kites", "The objects being counted.", "Beacon tally counts paper kites only after closing.", dktypes.TagEntity},
		}, [][]int{{0}, {1}}},
		{"same_name_distinct_context", []input{
			{"Atlas", "An irrigation pump installed in the north field.", "Atlas here moves water and has no software interface.", dktypes.TagEntity},
			{"Atlas", "A software package maintained by Harbor Team.", "Atlas here is a library and is unrelated to the irrigation pump.", dktypes.TagEntity},
		}, [][]int{{0}, {1}}},
		{"explicit_independent_versions", []input{
			{"Lark v1", "An independently deployed service.", "Lark v1 and Lark v2 are separate deployed services; v1 permits nine minutes.", dktypes.TagEntity},
			{"Lark v2", "A separate independently deployed service.", "Lark v2 permits six minutes and does not accept v1 credentials.", dktypes.TagEntity},
		}, [][]int{{0}, {1}}},
		{"proposition_is_not_mentioned_subject", []input{
			{"Admission", "An action which requires a badge.", "Admission requires a badge only while the seal is closed.", dktypes.TagConcept},
			{"Admission requires a badge", "A separately named policy proposition.", "Admission requires a badge only while the seal is closed.", dktypes.TagConcept},
		}, [][]int{{0}, {1}}},
		{"identity_not_determined", []input{
			{"Cedar", "A counter named Cedar in an unsigned field note.", "The note supplies no location or identifier linking this counter to another Cedar.", dktypes.TagEntity},
			{"Cedar", "A counter named Cedar in a different unsigned note.", "Whether this is the same counter is unspecified; the shared name does not resolve identity.", dktypes.TagEntity},
		}, [][]int{{0}, {1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var docs []*dktypes.AnnotatedDocument
			for i, row := range tc.input {
				docs = append(docs, &dktypes.AnnotatedDocument{Skill: "field-notes", FilePath: []string{"notes/a.md", "notes/b.md", "notes/c.md"}[i], Items: []dktypes.AnnotatedItem{{Tag: row.tag, Name: row.name, Content: row.content, Detail: row.detail, Confidence: .9}}})
			}
			docs = AssignIDs(docs)
			before, _ := json.Marshal(docs)
			sd := Subdomain{Name: "Source objects"}
			for gi, group := range tc.groups {
				label := "Entity"
				if tc.input[group[0]].tag == dktypes.TagConcept {
					label = "Concept"
				}
				node := Node{ID: []string{"first", "second", "third"}[gi], Label: label, Name: tc.input[group[0]].name, Summary: tc.input[group[0]].content, Confidence: .9}
				for _, i := range group {
					node.Members = append(node.Members, docs[i].Items[0].ID)
				}
				if label == "Entity" {
					sd.Entities = append(sd.Entities, node)
				} else {
					sd.Concepts = append(sd.Concepts, node)
				}
			}
			wire, err := json.Marshal(map[string]any{"domains": []Domain{{Name: "Field operations", ReusabilityRationale: "Source-specific objects with attributed observations", Subdomains: []Subdomain{sd}}}})
			if err != nil {
				t.Fatal(err)
			}
			client := &identitySequence{responses: []string{string(wire)}}
			result, coverage, err := NewExtractor(client, 4096).ExtractWithCoverageOptions(context.Background(), docs, CoverageOptions{MaxRetries: -1})
			if err != nil || coverage.Outcome != "complete" || coverage.FinalCoverage.CoveredIncidences != len(tc.input) || len(client.requests) != 1 {
				t.Fatalf("mapping flow changed: %+v / %v / calls%d", coverage, err, len(client.requests))
			}
			after, _ := json.Marshal(docs)
			if string(before) != string(after) {
				t.Fatal("mapping mutated accepted source documents")
			}
			for i, row := range tc.input {
				for _, text := range []string{row.name, row.content, row.detail, docs[i].FilePath, docs[i].Items[0].ID} {
					if !strings.Contains(client.requests[0].User, text) {
						t.Fatalf("mapping input lost full identity context: %s", text)
					}
				}
			}
			actual := append(append([]Node{}, result.Domains[0].Subdomains[0].Entities...), result.Domains[0].Subdomains[0].Concepts...)
			if len(actual) != len(tc.groups) {
				t.Fatalf("group identities changed: %+v", actual)
			}
			byID := map[string]Node{}
			for _, node := range actual {
				byID[node.ID] = node
			}
			index := buildDetailIndex(docs)
			for gi, group := range tc.groups {
				node := byID[[]string{"first", "second", "third"}[gi]]
				var want []string
				for _, i := range group {
					want = append(want, docs[i].Items[0].ID)
				}
				got := append([]string{}, node.Members...)
				sort.Strings(got)
				sort.Strings(want)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("member ownership changed: %v want %v", got, want)
				}
				contributions := LookupMemberContributions(docs, node.Members)
				material := aggregateMemberDetails(node.Members, index)
				if len(contributions) != len(group) {
					t.Fatalf("lost source incidence: %+v", contributions)
				}
				for _, i := range group {
					for _, text := range []string{docs[i].FilePath, tc.input[i].name, tc.input[i].content, tc.input[i].detail} {
						if !strings.Contains(material, text) {
							t.Fatalf("description material lost source-qualified fact: %s", text)
						}
					}
				}
				owned := map[int]bool{}
				for _, i := range group {
					owned[i] = true
				}
				for i, doc := range docs {
					if !owned[i] && strings.Contains(material, doc.FilePath) {
						t.Fatalf("unrelated source entered this subject's material: %s", doc.FilePath)
					}
				}
			}
		})
	}
}
