package pipeline

import (
	"context"
	"fmt"
	"log"

	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/repoidentity"
)

// MaterializeCompletedExtraction runs the production materialization gates for
// a completed extraction, including a verified checkpoint. It makes no LLM
// requests and does not reassign UUIDs. Callers must verify checkpoint origin.
func (o *Orchestrator) MaterializeCompletedExtraction(ctx context.Context, repoPath, dbPath string, res *extract.Result, docs []*dktypes.AnnotatedDocument, skillMetas []ingest.SkillMeta) (*RunReport, error) {
	report := &RunReport{}
	if err := store.CheckLease(ctx); err != nil {
		return report, err
	}
	candidate, selection, err := extract.SelectMaterializationCandidates(res, o.minConf)
	report.Selection = selection
	o.dumpJSON("05h_candidate_selection.json", selection)
	if err != nil {
		return report, err
	}
	if err := extract.ValidateFusionCompleteness(candidate, docs, o.minConf, true); err != nil {
		return report, fmt.Errorf("pipeline: materialization candidate incomplete: %w", err)
	}
	o.dumpJSON("05h_materialization_candidate.json", candidate)
	log.Printf("[pipeline] 候选筛选: 节点保留 %d/%d，置信度过滤 %d（entity=%d concept=%d）；关系保留 %d/%d，端点过滤 %d",
		selection.NodesSelected, selection.NodesInput, selection.NodesFiltered, selection.EntitiesFiltered, selection.ConceptsFiltered,
		selection.RelationsSelected, selection.RelationsInput, selection.RelationsFiltered)
	identity, err := repoidentity.Ensure(ctx, repoPath, o.repositoryIdentity)
	if err != nil {
		return report, fmt.Errorf("pipeline: repository identity: %w", err)
	}
	// Both output paths consume this exact confidence-selected graph.
	memberSources := buildMemberSources(docs)
	logStage("4", "全量重建入库 / ingest（物化双层图）")
	s4Ctx, cancel := stageCtx(ctx, o.stageTimeout)
	report.Ingest, err = o.fullRebuildIngest(s4Ctx, dbPath, candidate, skillMetas, memberSources, identity)
	cancel()
	if err != nil {
		return report, fmt.Errorf("pipeline: ingest: %w", err)
	}
	o.dumpJSON("06_ingest_result.json", report.Ingest)
	log.Printf("[pipeline] 入库完成: %d 节点, %d 边 (跳过 %d), node_sources %d 行",
		report.Ingest.NodesInserted, report.Ingest.EdgesInserted, report.Ingest.EdgesSkipped, report.Ingest.NodeSourcesInserted)
	logStage("4.5", "结构化回写 / write-back（md + sidecar + _shared）")
	report.Writeback, err = writeback.WritebackContext(ctx, candidate, toWritebackMemberSources(memberSources), repoPath)
	o.dumpJSON("06b_writeback_report.json", report.Writeback)
	if err != nil {
		return report, fmt.Errorf("pipeline: writeback incomplete (candidate not publishable): %w", err)
	}
	if report.Writeback != nil {
		wb := report.Writeback
		log.Printf("[pipeline] 回写统计: 文档 %d, 节点块 %d, shared %d, 镜像块 %d, primary %d, sidecar %d",
			wb.DocsWritten, wb.NodesWritten, wb.SharedNodes, wb.MirrorBlocks, wb.PrimaryFiles, wb.SidecarsWritten)
	}
	return report, nil
}
