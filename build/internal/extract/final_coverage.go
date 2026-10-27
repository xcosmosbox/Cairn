package extract

import (
	"fmt"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// FinalCoverageReport 只陈述当前存活图的成员/来源映射，不能证明语义保真。
// FinalCoverageReport accounts for actual survivors; semantic verification is separate.
type FinalCoverageReport struct {
	Outcome              string               `json:"outcome"`
	Reason               string               `json:"reason,omitempty"`
	InputUnits           int                  `json:"input_units"`
	CoveredUnits         int                  `json:"covered_units"`
	InputIncidences      int                  `json:"input_incidences"`
	CoveredIncidences    int                  `json:"covered_incidences"`
	UncoveredIDs         []string             `json:"uncovered_ids"`
	MissingContributions []MemberContribution `json:"missing_contributions,omitempty"`
}

// ComputeFinalCoverage 必须对描述、UUID、候选过滤后的实际图重新调用，不能复用早期计数。
// ComputeFinalCoverage recomputes member and source incidence coverage without changing the graph.
func ComputeFinalCoverage(res *Result, docs []*dktypes.AnnotatedDocument) FinalCoverageReport {
	rpt := FinalCoverageReport{Outcome: "complete", UncoveredIDs: []string{}}
	ids := collectUnitIDs(docs)
	rpt.InputUnits = len(ids)
	covered := map[string]bool{}
	if res != nil {
		covered = buildCovered(res.Domains, ids)
	}
	rpt.CoveredUnits = len(covered)
	seenIDs := map[string]bool{}
	seenInc := map[string]bool{}
	allIDs := make([]string, 0, len(ids)+1)
	allIDs = append(allIDs, "")
	for id := range ids {
		allIDs = append(allIDs, id)
	}
	for _, c := range LookupMemberContributions(docs, allIDs) {
		key := contributionKey(c)
		if seenInc[key] {
			continue
		}
		seenInc[key] = true
		rpt.InputIncidences++
		valid := c.MemberID != "" && strings.TrimSpace(c.FilePath) != "" && covered[c.MemberID]
		if valid {
			rpt.CoveredIncidences++
		} else {
			rpt.MissingContributions = append(rpt.MissingContributions, c)
			if !seenIDs[c.MemberID] {
				seenIDs[c.MemberID] = true
				rpt.UncoveredIDs = append(rpt.UncoveredIDs, c.MemberID)
			}
		}
	}
	if res == nil || len(rpt.MissingContributions) > 0 {
		rpt.Outcome = "partial"
		rpt.Reason = fmt.Sprintf("mapping retains %d/%d members and %d/%d source incidences", rpt.CoveredUnits, rpt.InputUnits, rpt.CoveredIncidences, rpt.InputIncidences)
	}
	return rpt
}
