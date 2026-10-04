package extract

import (
	"fmt"
	"math"
)

// CandidateSelectionReport records the confidence gate once, before the same
// candidate graph is used for both SQLite and Markdown/sidecar materialization.
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
// A filtered duplicate must not remove an eligible node with the same UUID;
// multiple eligible definitions of one UUID are ambiguous and fail closed.
func SelectMaterializationCandidates(res *Result, minConfidence float64) (*Result, *CandidateSelectionReport, error) {
	report := &CandidateSelectionReport{MinConfidence: minConfidence}
	if res == nil {
		return nil, report, fmt.Errorf("candidate selection: missing extraction result")
	}
	if math.IsNaN(minConfidence) || math.IsInf(minConfidence, 0) {
		return nil, report, fmt.Errorf("candidate selection: invalid confidence threshold")
	}
	selectedIDs := map[string]string{}
	selectNodes := func(nodes []Node, label string) ([]Node, error) {
		selected := make([]Node, 0, len(nodes))
		for _, node := range nodes {
			report.NodesInput++
			if math.IsNaN(node.Confidence) || math.IsInf(node.Confidence, 0) {
				return nil, fmt.Errorf("candidate selection: node %q has invalid confidence", node.Name)
			}
			if node.Confidence < minConfidence {
				report.NodesFiltered++
				if label == "Entity" {
					report.EntitiesFiltered++
				} else {
					report.ConceptsFiltered++
				}
				continue
			}
			if node.ID == "" {
				return nil, fmt.Errorf("candidate selection: eligible node %q has no UUID", node.Name)
			}
			if previous, exists := selectedIDs[node.ID]; exists {
				return nil, fmt.Errorf("candidate selection: duplicate eligible UUID %q for nodes %q and %q", node.ID, previous, node.Name)
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
