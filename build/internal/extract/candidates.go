package extract

import (
	"fmt"
	"log"
	"math"
)

// CandidateSelectionReport counts the same valid graph used for SQLite and writeback.
type CandidateSelectionReport struct {
	MinConfidence     float64
	NodesInput        int
	NodesSelected     int
	NodesFiltered     int
	EntitiesFiltered  int
	ConceptsFiltered  int
	RelationsInput    int
	RelationsSelected int
	RelationsFiltered int
}

// SelectMaterializationCandidates returns an independent graph view without
// changing the diagnostic extraction result. UUIDs must already be assigned.
// Ambiguous identities and their incident edges are omitted locally.
func SelectMaterializationCandidates(res *Result, minConfidence float64) (*Result, *CandidateSelectionReport, error) {
	report := &CandidateSelectionReport{}
	_ = minConfidence // legacy argument, never an admission threshold
	if res == nil {
		return nil, report, fmt.Errorf("candidate selection: missing extraction result")
	}
	counts := map[string]int{}
	for _, d := range res.Domains {
		for _, sd := range d.Subdomains {
			for _, nodes := range [][]Node{sd.Entities, sd.Concepts} {
				for _, n := range nodes {
					if n.ID != "" {
						counts[n.ID]++
					}
				}
			}
		}
	}
	selectedIDs := map[string]string{}
	selectNodes := func(nodes []Node, label string) ([]Node, error) {
		selected := make([]Node, 0, len(nodes))
		for _, node := range nodes {
			report.NodesInput++
			if node.ID == "" || counts[node.ID] > 1 || math.IsNaN(node.Confidence) || math.IsInf(node.Confidence, 0) || node.Confidence < 0 || node.Confidence > 1 {
				log.Printf("[extract] omitted invalid candidate %q", node.Name)
				report.NodesFiltered++
				if label == "Entity" {
					report.EntitiesFiltered++
				} else {
					report.ConceptsFiltered++
				}
				continue
			}
			selectedIDs[node.ID] = node.Name
			selected = append(selected, node)
			report.NodesSelected++
		}
		return selected, nil
	}
	candidate := *res
	candidate.Domains = make([]Domain, len(res.Domains))
	for di, domain := range res.Domains {
		candidate.Domains[di] = domain
		candidate.Domains[di].Subdomains = make([]Subdomain, len(domain.Subdomains))
		for si, subdomain := range domain.Subdomains {
			out := &candidate.Domains[di].Subdomains[si]
			*out = subdomain
			var err error
			if out.Entities, err = selectNodes(subdomain.Entities, "Entity"); err != nil {
				return nil, report, err
			}
			if out.Concepts, err = selectNodes(subdomain.Concepts, "Concept"); err != nil {
				return nil, report, err
			}
		}
	}
	// Use the retained UUID set, not the excluded set: a low-confidence alias
	// can share a UUID with a retained node without invalidating its relations.
	for di := range candidate.Domains {
		for si := range candidate.Domains[di].Subdomains {
			out := &candidate.Domains[di].Subdomains[si]
			relations := make([]Relation, 0, len(out.Relations))
			for _, relation := range out.Relations {
				report.RelationsInput++
				_, sourceOK := selectedIDs[relation.Source]
				_, targetOK := selectedIDs[relation.Target]
				if !sourceOK || !targetOK {
					report.RelationsFiltered++
					continue
				}
				relations = append(relations, relation)
				report.RelationsSelected++
			}
			out.Relations = relations
		}
	}
	return &candidate, report, nil
}
