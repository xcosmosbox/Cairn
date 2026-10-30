package pipeline

import (
	"context"
	"fmt"
	"github.com/xcosmosbox/cairn/core/storage"
	"log"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/repoidentity"
)

// MaterializeCompletedExtraction runs the production materialization gates for
// a completed extraction, including a verified checkpoint. It makes no LLM
// requests. Existing exact source/member identities stay pinned; callers verify checkpoint origin.
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
	// 早期悬空候选要留给有限修复；终端节点过滤之后，两个输出必须使用同一有效端点集合。
	// Repair keeps early candidates; terminal selection reconciles the actual surviving graph.
	candidate, _, err = extract.SelectMaterializationCandidates(candidate, o.minConf)
	if err != nil {
		return report, fmt.Errorf("pipeline: final materialization candidate: %w", err)
	}
	// Selection and output counts refer to the same validated subset.
	nodes, relations := 0, 0
	for _, d := range candidate.Domains {
		for _, sd := range d.Subdomains {
			nodes += len(sd.Entities) + len(sd.Concepts)
			relations += len(sd.Relations)
		}
	}
	selection.NodesFiltered += selection.NodesSelected - nodes
	selection.NodesSelected = nodes
	selection.RelationsFiltered += selection.RelationsSelected - relations
	selection.RelationsSelected = relations
	finalCoverage := extract.ComputeFinalCoverage(candidate, docs)
	report.FinalCoverage = &finalCoverage
	o.dumpJSON("05h_final_coverage.json", finalCoverage)
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
	represented := map[string]bool{}
	for _, doc := range docs {
		if doc != nil {
			represented[doc.FilePath] = true
		}
	}
	var existing *fullExistingOutcome
	report.Ingest, existing, err = o.fullRebuildIngestScoped(ctx, dbPath, candidate, skillMetas, memberSources, identity, repoPath, represented)
	if err != nil {
		return report, fmt.Errorf("pipeline: ingest: %w", err)
	}
	if existing != nil {
		report.UUIDCorrespondence = nodeCorrespondence(candidate)
		for i := range report.UUIDCorrespondence {
			id := report.UUIDCorrespondence[i].Alias
			report.UUIDCorrespondence[i].UUID = id
			if target := existing.remap[id]; target != "" {
				report.UUIDCorrespondence[i].UUID = target
			}
		}
		report.Reasons = append(report.Reasons, existing.conflicts...)
	}
	o.dumpJSON("06_ingest_result.json", report.Ingest)
	log.Printf("[pipeline] 入库完成: %d 节点, %d 边 (跳过 %d), node_sources %d 行",
		report.Ingest.NodesInserted, report.Ingest.EdgesInserted, report.Ingest.EdgesSkipped, report.Ingest.NodeSourcesInserted)
	logStage("4.5", "结构化回写 / write-back（md + sidecar + _shared）")
	persistedDB, openErr := storage.NewDB(storage.DBOptions{Path: dbPath})
	if openErr != nil {
		return report, fmt.Errorf("pipeline: persisted relation readback: %w", openErr)
	}
	persistedRelations, readErr := storage.NewEdgeRepo(persistedDB).ListAll(ctx)
	closeErr := persistedDB.Close()
	if readErr != nil {
		return report, fmt.Errorf("pipeline: persisted relation readback: %w", readErr)
	}
	if closeErr != nil {
		return report, closeErr
	}
	// nil 代表未读取；成功读到零行必须传非 nil 空集合。
	// An empty successful read is known-empty, never unknown relation context.
	if persistedRelations == nil {
		persistedRelations = []*dktypes.Edge{}
	}
	if existing != nil {
		report.Writeback, err = writeback.WritebackAuthoritativeContext(ctx, existing.updates, existing.docs, existing.refresh, existing.empty, repoPath)
	} else {
		report.Writeback, err = writeback.WritebackContextWithRelations(ctx, candidate, toWritebackMemberSources(memberSources), persistedRelations, repoPath)
	}
	o.dumpJSON("06b_writeback_report.json", report.Writeback)
	if err != nil {
		report.finish(ctx, err)
		return report, fmt.Errorf("pipeline: writeback incomplete (candidate not publishable): %w", err)
	}
	if report.Writeback != nil {
		wb := report.Writeback
		log.Printf("[pipeline] 回写统计: 文档 %d, 节点块 %d, shared %d, 镜像块 %d, primary %d, sidecar %d",
			wb.DocsWritten, wb.NodesWritten, wb.SharedNodes, wb.MirrorBlocks, wb.PrimaryFiles, wb.SidecarsWritten)
	}
	if existing != nil && len(existing.conflicts) > 0 {
		report.Status = "partial"
		return report, fmt.Errorf("pipeline: human_curated relation conflict: %s", strings.Join(existing.conflicts, "; "))
	}
	return report, nil
}
