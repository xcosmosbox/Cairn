// Extraction omissions are observations, never a paid completion loop.
package extract

import (
	"context"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"log"
)

type CoverageReport struct {
	ExtractAttempts       int      // 实际执行的整轮提取次数
	InputUnits            int      // 04 输入单元总数（按 id 去重）
	DuplicateNodesMerged  int      // 合并的重复融合节点数
	InvalidMembersRemoved int      // 剔除的非法 member 引用数
	InvalidMemberIDs      []string // 被剔除的非法 member id（诊断用）
	EmptyNodesFound       int      // 校准后 members 为空的孤儿节点数（补前，最后一轮）
	EmptyNodesFilled      int      // 经伪 session 补上 members 的空节点数
	FirstPassCovered      int      // 首轮（增量补之前）被覆盖的单元数
	FirstPassRate         float64  // 首轮覆盖率
	Assigned              int      // 增量补：融入已有节点的单元数
	Created               int      // 增量补：新建融合节点数
	Explained             int      // 增量补：给出非空解释而放过的单元数
	ExplainedNotes        []string // 解释内容（"id: 理由"，记入日志）
	FinalCovered          int      // 最终被 members 覆盖的单元数
	FinalRate             float64  // 最终覆盖率（把已解释单元视为已处理）
	TrulyMissing          []string // 两轮后仍未覆盖且无解释的单元（真正遗漏）
	Outcome               string   // pass_p99 | pass_p95 | degraded
}

// coverageRate 计算覆盖率；输入为 0 时视为全覆盖（1.0），避免除零。
// coverageRate computes the coverage ratio; empty input counts as fully covered.
func coverageRate(covered, input int) float64 {
	if input <= 0 {
		return 1.0
	}
	return float64(covered) / float64(input)
}

// ExtractWithCoverage extracts once and reports omissions without paid repair.
func (e *Extractor) ExtractWithCoverage(ctx context.Context, docs []*dktypes.AnnotatedDocument) (*Result, *CoverageReport, error) {
	rpt := &CoverageReport{ExtractAttempts: 1, InputUnits: len(collectUnitIDs(docs))}
	res, err := e.ExtractFromAnnotations(ctx, docs)
	if err != nil {
		return nil, rpt, err
	}
	// Unknown members invalidate their node; never turn a partly invented mapping
	// into a validated node by stripping only its invented member.
	if err := ValidateFusionCompleteness(res, docs, 0, false); err != nil {
		return nil, rpt, err
	}
	cov := ComputeCoverage(res.Domains, docs)
	rpt.FirstPassCovered = cov.CoveredUnits
	rpt.FinalCovered = cov.CoveredUnits
	rpt.FirstPassRate = coverageRate(cov.CoveredUnits, rpt.InputUnits)
	rpt.FinalRate = rpt.FirstPassRate
	rpt.TrulyMissing = cov.UncoveredIDs
	rpt.Outcome = "observed"
	if len(cov.UncoveredIDs) > 0 {
		log.Printf("[extract-coverage] omitted %d/%d source members; retaining validated material", len(cov.UncoveredIDs), rpt.InputUnits)
	}
	return res, rpt, nil
}
