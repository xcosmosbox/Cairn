package extract

import (
	"fmt"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"log"
	"math"
	"strings"
)

// ValidateFusionCompleteness validates source association per node. Missing
// descriptions and unknown members remove only that node and incident edges;
// coverage/annotation detail length/confidence are not admission thresholds.
func ValidateFusionCompleteness(res *Result, docs []*dktypes.AnnotatedDocument, _ float64, requireDescriptions bool) error {
	if res == nil {
		return fmt.Errorf("fusion: missing extraction result")
	}
	members := map[string]bool{}
	for _, doc := range docs {
		if doc != nil {
			for _, it := range doc.Items {
				if it.ID != "" && doc.FilePath != "" {
					members[it.ID] = true
				}
			}
		}
	}
	filter := func(nodes []Node) []Node {
		kept := make([]Node, 0, len(nodes))
		for _, node := range nodes {
			reason := ""
			if node.ID == "" || strings.TrimSpace(node.Name) == "" || len(node.Members) == 0 {
				reason = "missing identity or source members"
			}
			if math.IsNaN(node.Confidence) || math.IsInf(node.Confidence, 0) || node.Confidence < 0 || node.Confidence > 1 {
				reason = "invalid confidence"
			}
			for _, m := range node.Members {
				if !members[m] {
					reason = "unknown source member"
				}
			}
			if requireDescriptions && strings.TrimSpace(node.Description) == "" {
				reason = "missing description response"
			}
			if reason != "" {
				log.Printf("[extract] omitted node %q: %s", node.Name, reason)
				continue
			}
			kept = append(kept, node)
		}
		return kept
	}
	for di := range res.Domains {
		for si := range res.Domains[di].Subdomains {
			sd := &res.Domains[di].Subdomains[si]
			sd.Entities = filter(sd.Entities)
			sd.Concepts = filter(sd.Concepts)
		}
	}
	cleanDangling(res.Domains)
	return nil
}
