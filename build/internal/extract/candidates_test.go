package extract

import (
	"strings"
	"testing"
)

func TestCandidateSelectionPreservesEligibleDuplicateIdentityAndItsRelations(t *testing.T) {
	t.Parallel()
	res := &Result{Domains: []Domain{{Subdomains: []Subdomain{
		{Concepts: []Node{{ID: "shared", Name: "authoritative", Confidence: 0.9}, {ID: "other", Confidence: 0.9}}},
		{Concepts: []Node{{ID: "shared", Name: "excluded alias", Confidence: 0}, {ID: "excluded", Confidence: 0}}, Relations: []Relation{
			{Source: "shared", Target: "other"},
			{Source: "excluded", Target: "other"},
		}},
	}}}}
	candidate, report, err := SelectMaterializationCandidates(res, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if report.NodesInput != 4 || report.NodesSelected != 2 || report.NodesFiltered != 2 || report.ConceptsFiltered != 2 || report.RelationsInput != 2 || report.RelationsSelected != 1 || report.RelationsFiltered != 1 {
		t.Fatalf("incorrect filtering report: %+v", report)
	}
	if len(candidate.Domains[0].Subdomains[1].Concepts) != 0 || len(candidate.Domains[0].Subdomains[1].Relations) != 1 || candidate.Domains[0].Subdomains[1].Relations[0].Source != "shared" {
		t.Fatal("excluded alias removed a valid global UUID relationship")
	}
	if candidate.Domains[0].Subdomains[0].Concepts[0].Name != "authoritative" {
		t.Fatal("excluded alias replaced retained node metadata")
	}
	if len(res.Domains[0].Subdomains[1].Concepts) != 2 || len(res.Domains[0].Subdomains[1].Relations) != 2 {
		t.Fatal("candidate selection changed the diagnostic extraction")
	}
}

func TestCandidateSelectionRejectsEligibleUUIDCollisionAcrossLabelsAndDomains(t *testing.T) {
	t.Parallel()
	res := &Result{Domains: []Domain{
		{Subdomains: []Subdomain{{Concepts: []Node{{ID: "same", Name: "first", Confidence: 0.9}}}}},
		{Subdomains: []Subdomain{{Entities: []Node{{ID: "same", Name: "second", Confidence: 0.8}}}}},
	}}
	if candidate, _, err := SelectMaterializationCandidates(res, 0.7); err == nil || candidate != nil || !strings.Contains(err.Error(), "duplicate eligible UUID") {
		t.Fatalf("ambiguous eligible UUID accepted: candidate=%+v err=%v", candidate, err)
	}
}
