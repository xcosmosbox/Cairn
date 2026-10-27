package incremental

import (
	"context"
	"fmt"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
)

// fuseDescriptionsWithVerifiedContent 为支持成对写入的调用方提供完整核验结果。
// The caller must carry both maps into I-8; legacy callers retain their truthful deferral.
func (r *reflower) fuseDescriptionsWithVerifiedContent(ctx context.Context, dirty *DirtySet, ao *alignOutcome, rs *runState) (map[string]string, map[string]extract.NodeDescriptionOutcome) {
	descriptions := r.fuseDescriptionsForPersistence(ctx, dirty, ao, rs, true)
	contents := make(map[string]extract.NodeDescriptionOutcome)
	for _, out := range r.nodeDescriptionOutcomes() {
		if out.Outcome == annotation.DocumentComplete {
			if description, ok := descriptions[out.ID]; ok && description == out.Description {
				contents[out.ID] = out
			}
		}
	}
	return descriptions, contents
}

// validateVerifiedNodeContent 在任何写入前拒绝不完整或错配的 pair，防止旧摘要配新正文。
// This checks the opt-in persistence boundary, not the semantics already assessed by .3.
func validateVerifiedNodeContent(ao *alignOutcome, rr *reflowResult) error {
	if rr == nil {
		return nil
	}
	for id, content := range rr.verifiedNodeContent {
		if ao == nil || ao.pending[id] == nil {
			return fmt.Errorf("I-8 verified node %s has no pending identity", id)
		}
		if id != content.ID || content.Outcome != annotation.DocumentComplete || strings.TrimSpace(content.Summary) == "" || strings.TrimSpace(content.Description) == "" {
			return fmt.Errorf("I-8 verified node %s has no complete content pair", id)
		}
		if description, ok := rr.descriptions[id]; !ok || description != content.Description {
			return fmt.Errorf("I-8 verified node %s description does not match assessed pair", id)
		}
	}
	return nil
}
