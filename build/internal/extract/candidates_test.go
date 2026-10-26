package extract

import "testing"

// Ambiguous identity invalidates its nodes and incident edges, not the good sibling.
func TestCandidateSelectionDropsOnlyAmbiguousOrMalformedNodes(t *testing.T) {
	res := &Result{Domains: []Domain{{Subdomains: []Subdomain{{Concepts: []Node{{ID: "same", Name: "first", Confidence: .9}, {ID: "same", Name: "second", Confidence: .8}, {ID: "good", Name: "good", Confidence: .69}, {ID: "also", Name: "also", Confidence: .1}}, Relations: []Relation{{Source: "same", Target: "good"}, {Source: "good", Target: "also"}}}}}}}
	candidate, rpt, err := SelectMaterializationCandidates(res, .7)
	if err != nil {
		t.Fatal(err)
	}
	if rpt.NodesSelected != 2 || rpt.NodesFiltered != 2 || rpt.RelationsSelected != 1 || rpt.RelationsFiltered != 1 {
		t.Fatalf("wrong local result: %+v", rpt)
	}
	if len(candidate.Domains[0].Subdomains[0].Concepts) != 2 || len(res.Domains[0].Subdomains[0].Concepts) != 4 {
		t.Fatal("candidate mutated diagnostic graph")
	}
}
