package extract

import (
	"context"
	"encoding/json"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"strings"
	"testing"
)

// Missing/malformed members and omitted descriptions used to abort the whole graph.
func TestExtractionRetainsValidSiblingAndReportsOmissionWithoutRepair(t *testing.T) {
	c := llm.NewMockClient()
	c.Response = &llm.CompleteResponse{Text: `{"domains":[{"name":"D","subdomains":[{"name":"S","entities":[{"id":"good","label":"Entity","name":"Good","confidence":0.69,"members":["known"]},{"id":"bad-type","label":"Entity","name":"Bad","confidence":"unknown","members":["known"]},{"id":"bad-member","label":"Entity","name":"Bad member","confidence":0.9,"members":["known","invented"]}],"concepts":[],"relations":[{"source":"bad-member","target":"good","kind":"references"}]}]}]}`, FinishReason: "stop"}
	docs := []*dktypes.AnnotatedDocument{{Skill: "s", FilePath: "a.md", Items: []dktypes.AnnotatedItem{{ID: "known", Content: "fact"}, {ID: "omitted", Content: "another fact"}}}}
	res, rpt, err := NewExtractor(c, 0).ExtractWithCoverage(context.Background(), docs)
	if err != nil {
		t.Fatal(err)
	}
	sd := res.Domains[0].Subdomains[0]
	if len(sd.Entities) != 1 || sd.Entities[0].ID != "good" || sd.Entities[0].Confidence != .69 || len(sd.Relations) != 0 || len(c.Requests) != 1 || len(rpt.TrulyMissing) != 1 || rpt.FinalRate != .5 {
		t.Fatalf("wrong local result: %+v %+v calls%d", sd, rpt, len(c.Requests))
	}
	sd.Entities[0].Description = "good description"
	sd.Entities = append(sd.Entities, Node{ID: "missing", Name: "Missing", Confidence: .9, Members: []string{"known"}})
	res.Domains[0].Subdomains[0] = sd
	if err := ValidateFusionCompleteness(res, docs, .99, true); err != nil || len(res.Domains[0].Subdomains[0].Entities) != 1 {
		t.Fatalf("missing description affected good sibling: %v", err)
	}
	repair := NewExtractor(c, 0).RepairDanglingRelations(context.Background(), res, docs)
	GenerateFileSlugs(context.Background(), res.Domains, c)
	if repair.Rounds != 0 || len(c.Requests) != 1 || res.Domains[0].Subdomains[0].Entities[0].FileSlug == "" {
		t.Fatal("repair/filename issued extra paid call")
	}
}

func TestAllSameNameSourceIncidencesReachExtractionAndDescriptions(t *testing.T) {
	docs := AssignIDs([]*dktypes.AnnotatedDocument{
		{Skill: "s", FilePath: "a.md", Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagEntity, Name: "same", Content: "2020 condition", Detail: "2020 detail"}}},
		{Skill: "s", FilePath: "b.md", Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagEntity, Name: "same", Content: "2024 condition", Detail: "2024 detail"}}},
	})
	input := assembleAnnotatedMessage(docs)
	if !strings.Contains(input, "2020 condition") || !strings.Contains(input, "2024 condition") {
		t.Fatal("same-name input was winner-deduplicated")
	}
	detail := aggregateMemberDetails([]string{docs[0].Items[0].ID}, buildDetailIndex(docs))
	if !strings.Contains(detail, "2020 detail") || !strings.Contains(detail, "2024 detail") {
		t.Fatal("description lost a source incidence")
	}
}

// Evidence size is bounded by actual provider context, never an internal byte cap.
func TestCrossEvidenceOldMicrobudgetsDoNotRejectValidRequest(t *testing.T) {
	res, docs, sources := crossFixture()
	long := strings.Repeat("full exact original evidence ", 3000)
	sources[0] = FreezeSource(sources[0].Skill, sources[0].Path, sources[0].Content+long)
	v := supportedCrossVerdict()
	v.Evidence[0].Quote = long
	c := &crossFixtureClient{reply: func(req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if !strings.Contains(req.User, long) {
			t.Error("full evidence truncated")
		}
		wire, _ := json.Marshal(v)
		return &llm.CompleteResponse{Text: string(wire), FinishReason: "stop"}, nil
	}}
	rpt := NewExtractor(c, 0).ValidateCrossRelations(context.Background(), res, docs, sources, CrossRelationOptions{MaxEvidenceBytes: 1})
	if rpt.Accepted != 1 || c.calls.Load() != 1 {
		t.Fatalf("old microbudget rejected valid source: %+v", rpt)
	}
}
