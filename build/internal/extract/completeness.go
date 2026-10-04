package extract

import (
	"fmt"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// ValidateFusionCompleteness rejects missing source material before description
// generation, and missing descriptions before materialization. Only nodes that
// meet the configured ingest confidence threshold are required to be publishable.
// Inspect every member: one valid source must not hide another invented member.
func ValidateFusionCompleteness(res *Result, docs []*dktypes.AnnotatedDocument, minConfidence float64, requireDescriptions bool) error {
	if res == nil {
		return fmt.Errorf("fusion completeness: missing extraction result")
	}
	memberDetails := map[string]bool{}
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		for _, item := range doc.Items {
			if item.ID == "" {
				continue
			}
			complete := strings.TrimSpace(item.Detail) != "" && strings.TrimSpace(doc.FilePath) != ""
			if previous, exists := memberDetails[item.ID]; exists {
				complete = complete && previous
			}
			memberDetails[item.ID] = complete
		}
	}
	for _, domain := range res.Domains {
		for _, subdomain := range domain.Subdomains {
			for _, nodes := range [][]Node{subdomain.Entities, subdomain.Concepts} {
				for _, node := range nodes {
					if node.Confidence < minConfidence {
						continue
					}
					if len(node.Members) == 0 {
						return fmt.Errorf("fusion completeness: node %q has no source members", node.Name)
					}
					for _, member := range node.Members {
						complete, exists := memberDetails[member]
						if !exists {
							return fmt.Errorf("fusion completeness: node %q references unknown member %q", node.Name, member)
						}
						if !complete {
							return fmt.Errorf("fusion completeness: node %q member %q has missing detail or source path", node.Name, member)
						}
					}
					if requireDescriptions && strings.TrimSpace(node.Description) == "" {
						return fmt.Errorf("fusion completeness: node %q has no generated description", node.Name)
					}
				}
			}
		}
	}
	return nil
}
