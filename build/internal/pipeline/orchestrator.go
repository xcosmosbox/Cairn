// Package pipeline builds a source-bound graph in an isolated database.
// Each document is annotated and factually assessed once; valid independent
// material continues after local failures, without repair or coverage loops.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/discovery"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/repoidentity"
	"github.com/xcosmosbox/cairn/core/storage"
)

// Options 配置 Orchestrator 的依赖与参数。
// Options configures the orchestrator's dependencies and parameters.
type Options struct {
	// RepositoryIdentity 可显式提供稳定 origin 身份；空时从 Git 或本地持久标记解析。
	RepositoryIdentity string
	// Client 是共享的 LLM 客户端，供标注 / 语义门 / 提取三处使用。不可为 nil。
	// Client is the shared LLM client used by annotation, the semantic gate, and extraction.
	Client llm.Client
	// MaxTokens 是单次 LLM 请求的 max_tokens；≤0 交由 client 默认。
	MaxTokens int
	// MaxRetries is retained for caller compatibility; annotation/assessment run once.
	MaxRetries int
	// MaxRollbacks is retired and ignored.
	MaxRollbacks int
	// MinConfidence is retired and ignored; finite confidence is descriptive.
	MinConfidence float64
	// MaxNameRunes is retired and ignored.
	MaxNameRunes int
	// Rules 是 discovery 扫描规则；零值使用其内置默认（SKILL.md + references/reference + **/*.md）。
	Rules discovery.DiscoveryRules
	// DumpDir 非空时，每个阶段的中间产物（标注 JSON、双门结果、提取输入/输出、入库统计）
	// 会写到该目录下，便于用真实 LLM 跑完后逐环节比对产出。空则不 dump。
	// When non-empty, intermediate artifacts of each stage are written under this
	// directory for inspection with a real LLM run.
	DumpDir string
	// CrossRelationEvidenceBytes is retired and ignored; complete evidence is sent.
	CrossRelationEvidenceBytes int
}

// Orchestrator 是新 LLM 流水线的内存态状态机编排器。
// Orchestrator is the in-memory state-machine orchestrator of the new LLM pipeline.
type Orchestrator struct {
	repositoryIdentity         string
	scanner                    *discovery.FSScanner
	annotator                  *annotation.LLMAnnotator
	formatGate                 *annotation.FormatGate
	semanticGate               *annotation.SemanticGate
	extractor                  *extract.Extractor
	client                     llm.Client // 供 GenerateFileSlugs 使用 / for GenerateFileSlugs
	minConf                    float64
	rules                      discovery.DiscoveryRules
	dumpDir                    string
	crossRelationEvidenceBytes int
}

// NewOrchestrator 组装 Orchestrator 及其全部阶段组件。Client 不可为 nil。
// NewOrchestrator wires the orchestrator and all stage components. Client must not be nil.
func NewOrchestrator(opts Options) (*Orchestrator, error) {
	if opts.Client == nil {
		return nil, fmt.Errorf("pipeline: llm client is nil")
	}
	return &Orchestrator{
		repositoryIdentity:         opts.RepositoryIdentity,
		scanner:                    discovery.NewFSScanner(),
		annotator:                  annotation.NewLLMAnnotator(opts.Client, opts.MaxTokens, opts.MaxRetries),
		formatGate:                 annotation.NewFormatGate(opts.MaxNameRunes),
		semanticGate:               annotation.NewSemanticGate(opts.Client, opts.MaxTokens, opts.MaxRetries),
		extractor:                  extract.NewExtractor(opts.Client, opts.MaxTokens),
		client:                     opts.Client,
		minConf:                    0,
		rules:                      opts.Rules,
		dumpDir:                    opts.DumpDir,
		crossRelationEvidenceBytes: opts.CrossRelationEvidenceBytes,
	}, nil
}

// SkippedDoc 记录一篇因回退耗尽而降级跳过的文档（可观测）。
// SkippedDoc records a document degraded/skipped after exhausting its rollback budget.
type SkippedDoc struct {
	Skill  string
	Path   string
	Reason string
}

// RunReport 是一次全量重建运行的结果摘要（可观测）。
// RunReport summarizes one full-rebuild run.
type RunReport struct {
	SkillsScanned  int
	DocsTotal      int
	DocsRewritten  int          // 通过双门、进入提取的文档数
	Skipped        []SkippedDoc // 降级跳过的文档
	ExtractRounds  int
	Selection      *extract.CandidateSelectionReport // 同一候选集用于入库与回写
	Ingest         *ingest.IngestDomainsResult
	Coverage       *extract.CoverageReport      // 覆盖保障报告（05c1 阶段）；nil 表示未执行
	Repair         *extract.RepairReport        // 悬空边修正报告（05b/05c 阶段）；nil 表示未执行修正
	Describe       *extract.DescribeReport      // description 融合报告（05d 阶段）；nil 表示未执行
	CrossRelations *extract.CrossRelationReport // 原文验证的完整候选结果 / source-grounded cross-candidate outcomes
	Relate         *extract.RelateReport        // relation 补充报告（05e 阶段）；nil 表示未执行
	// Writeback 是 stage 4.5 结构化回写报告（06b 阶段）；nil 表示未执行回写。
	// 回写失败保留诊断统计，但阻止发布不完整的源闭环。
	// Writeback is the stage 4.5 write-back report; nil if not executed.
	// Write-back failure aborts publication while retaining diagnostic stats.
	Writeback *writeback.Report
}

// RunFullRebuild scans source documents, assesses each once, and builds the
// validated subset. Temporary DB creation, WAL checks and atomic publication
// preserve an existing database if materialization fails.
func (o *Orchestrator) RunFullRebuild(ctx context.Context, repoPath, dbPath string) (*RunReport, error) {
	if err := store.CheckLease(ctx); err != nil {
		return nil, err
	}
	report := &RunReport{}
	_, err := repoidentity.Ensure(ctx, repoPath, o.repositoryIdentity)
	if err != nil {
		return nil, fmt.Errorf("pipeline: repository identity: %w", err)
	}

	// ——1. 扫描——
	logStage("1", "扫描 / discovery")
	s1Ctx := ctx
	skills, err := o.scanner.Scan(s1Ctx, repoPath, o.rules)
	if err != nil {
		return nil, fmt.Errorf("pipeline: scan %s: %w", repoPath, err)
	}
	report.SkillsScanned = len(skills)
	o.dumpJSON("00_scan.json", skills)
	log.Printf("[pipeline] 扫描完成: %d 个 skill, %d 篇 reference 文档", len(skills), countRefFiles(skills))

	// ——2. 并发处理每篇文档：标注 + 双门校验（文档间隔离，互不阻塞）——
	logStage("2", "标注 + 双门校验 / annotate + Gate A/B（并发）")
	// skillMetas 不依赖文档处理结果，先单独建好。
	skillMetas := make([]ingest.SkillMeta, 0, len(skills))
	type docTask struct{ skill, path string }
	var tasks []docTask
	for _, sk := range skills {
		skillMetas = append(skillMetas, ingest.SkillMeta{
			Name:    sk.Name,
			Summary: sk.DomainHint,
		})
		for _, rel := range sk.ReferenceFiles {
			tasks = append(tasks, docTask{skill: sk.Name, path: rel})
		}
	}
	report.DocsTotal = len(tasks)

	// Stage 2 独立 context：标注阶段的 LLM 调用不受前面阶段剩余预算影响。
	s2Ctx := ctx

	// 每篇文档一个 goroutine，内部完整走「标注→Gate A→Gate B」（含回退重试）。
	// processDocument 无共享可变状态（dump 写不同文件路径），天然并发安全。
	type docResult struct {
		doc      *dktypes.AnnotatedDocument
		skill    string
		path     string
		original extract.FrozenSource
		reason   string // 空表示成功
	}
	resultsCh := make(chan docResult, len(tasks))
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func(skill, rel string) {
			defer wg.Done()
			content, rerr := os.ReadFile(filepath.Join(repoPath, rel))
			if rerr != nil {
				log.Printf("[pipeline] 跳过无法读取的文档 %s: %v", rel, rerr)
				resultsCh <- docResult{skill: skill, path: rel, reason: fmt.Sprintf("读取失败: %v", rerr)}
				return
			}
			original := extract.FreezeSource(skill, rel, string(content))
			doc, reason := o.processDocument(s2Ctx, skill, rel, string(content))
			resultsCh <- docResult{doc: doc, skill: skill, path: rel, reason: reason, original: original}
		}(t.skill, t.path)
	}
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	// 等所有并发完成，收集结果。
	var passed []*dktypes.AnnotatedDocument
	var originals []extract.FrozenSource
	for r := range resultsCh {
		if r.doc == nil {
			report.Skipped = append(report.Skipped, SkippedDoc{Skill: r.skill, Path: r.path, Reason: r.reason})
		} else {
			passed = append(passed, r.doc)
			originals = append(originals, r.original)
			report.DocsRewritten++
		}
	}
	// Concurrent completion order must not choose which summary represents a
	// shared 04 member. Keep source order stable; description fusion retains all
	// source details for each member ID.
	sort.SliceStable(passed, func(i, j int) bool {
		if passed[i].Skill != passed[j].Skill {
			return passed[i].Skill < passed[j].Skill
		}
		return passed[i].FilePath < passed[j].FilePath
	})

	// 冻结标注实际输入，供跨边校验与审计；后续绝不 reread 已回写的生成文档。
	// Persist the exact bytes seen by annotation, not reconstructed annotation detail.
	sort.Slice(originals, func(i, j int) bool {
		if originals[i].Skill != originals[j].Skill {
			return originals[i].Skill < originals[j].Skill
		}
		return originals[i].Path < originals[j].Path
	})
	o.dumpJSON("00_frozen_sources.json", originals)

	// 汇总失败文档，输出到终端与日志。
	if len(report.Skipped) > 0 {
		log.Printf("[pipeline] %d/%d 篇文档失败或降级跳过:", len(report.Skipped), report.DocsTotal)
		for _, sk := range report.Skipped {
			log.Printf("[pipeline]   ✗ %s (%s): %s", sk.Path, sk.Skill, sk.Reason)
		}
	}
	log.Printf("[pipeline] 标注阶段完成: %d/%d 篇通过双门, %d 篇跳过", report.DocsRewritten, report.DocsTotal, len(report.Skipped))

	// ——3. 提取 + 覆盖保障闭环（repo 级一次性投喂）——
	logStage("3", "领域提取 + 覆盖保障 / extract with coverage")
	// 提取前：为标注单元刷规则化 04 id（第二期 D），保留 per-document 结构（FilePath 完整）；
	// 供提取阶段用稳定 id 引用做融合映射。
	withIDs := extract.AssignIDs(passed)
	o.dumpJSON("04_extract_input.json", withIDs)
	// ExtractWithCoverage makes one extraction and reports omitted members.
	s3Ctx := ctx
	res, covRpt, err := o.extractor.ExtractWithCoverage(s3Ctx, withIDs)
	if err != nil {
		// 提取器已内部原地重试并耗尽；这是本次运行无法产出 KG 的实质失败，向上报告。
		return report, fmt.Errorf("pipeline: extract: %w", err)
	}
	report.ExtractRounds = res.Rounds
	report.Coverage = covRpt
	o.dumpJSON("05_extract_result.json", res)
	o.dumpJSON("05c1_coverage_report.json", covRpt)
	log.Printf("[pipeline] 提取+覆盖: %d 轮提取, %d 个 domain; 覆盖 %d/%d (%.2f%%), 结果=%s; 空节点补 %d/%d, 合并重复 %d, 剔非法 member %d",
		res.Rounds, len(res.Domains), covRpt.FinalCovered, covRpt.InputUnits, covRpt.FinalRate*100,
		covRpt.Outcome, covRpt.EmptyNodesFilled, covRpt.EmptyNodesFound, covRpt.DuplicateNodesMerged, covRpt.InvalidMembersRemoved)

	// ——3.5 悬空边修正（覆盖治理之后、ingest 前）——
	logStage("3.5", "悬空边修正 / repair dangling relations")
	// 融合校准与覆盖闭环已在 stage 3 完成；本阶段只专注 relations 悬空端点：
	// 仅清洗端点无效的边；不发起修复调用。
	s35Ctx := ctx
	repairRpt := o.extractor.RepairDanglingRelations(s35Ctx, res, withIDs)
	report.Repair = repairRpt
	o.dumpJSON("05b_extract_repaired.json", res)
	o.dumpJSON("05c_repair_report.json", repairRpt)
	if repairRpt.DanglingFound > 0 {
		log.Printf("[pipeline] 悬空边修正: 发现 %d, 清洗 %d, 应用 patch %d, 残留丢弃 %d, 降级=%v",
			repairRpt.DanglingFound, repairRpt.Cleaned, repairRpt.PatchesApplied, repairRpt.StillDangling, repairRpt.Degraded)
	} else {
		log.Printf("[pipeline] 悬空边修正: 未发现悬空 relation")
	}
	if err := extract.ValidateFusionCompleteness(res, withIDs, o.minConf, false); err != nil {
		return report, fmt.Errorf("pipeline: repaired source material incomplete (candidate not publishable): %w", err)
	}

	// ——3.6 description 融合重写（悬空修正之后、relation 识别之前）——
	logStage("3.6", "description 融合 / fuse descriptions（按节点并发）")
	// 第二期融合节点 description 留空；此处为每个节点综合其 members 的 04 detail 用 LLM 重写出
	// description，写回有效节点。缺少响应的节点及关联边局部省略。
	s36Ctx := ctx
	describeRpt := o.extractor.FuseDescriptions(s36Ctx, res, withIDs)
	report.Describe = describeRpt
	o.dumpJSON("05d_extract_described.json", res)
	o.dumpJSON("05d_describe_report.json", describeRpt)
	log.Printf("[pipeline] description 融合: subdomain %d/%d 成功, 节点重写 %d/%d（有素材 %d）",
		describeRpt.SubdomainsOK, describeRpt.SubdomainsTotal, describeRpt.NodesRewritten, describeRpt.NodesTotal, describeRpt.NodesWithDetail)
	if err := extract.ValidateFusionCompleteness(res, withIDs, o.minConf, true); err != nil {
		return report, fmt.Errorf("pipeline: description fusion incomplete (candidate not publishable): %w", err)
	}

	// ——3.7 relation 识别（description 之后、ingest 之前，按 subdomain 并发）——
	logStage("3.7", "relation 识别 / identify relations（按 subdomain 并发）")
	// 提取阶段刻意弱化了关系识别；此处以 subdomain 为隔离视角，输入其内全部融合节点（含完整 description），
	// 让 LLM 识别节点间语义关系（kind ∈ 6 枚举，越界局部省略，允许为空），写回 sd.Relations。
	s37Ctx := ctx
	relateRpt := o.extractor.IdentifyRelations(s37Ctx, res)
	report.Relate = relateRpt
	o.dumpJSON("05e_extract_related.json", res)
	o.dumpJSON("05e_relate_report.json", relateRpt)
	log.Printf("[pipeline] relation 识别: subdomain %d/%d 成功, 新增关系 %d, 过滤越界/自环 %d",
		relateRpt.SubdomainsOK, relateRpt.SubdomainsTotal, relateRpt.RelationsAdded, relateRpt.InvalidDropped)

	// ——3.75 原文验证跨子域候选；端点存在并不代表语义真实。——
	// Keep all validation before UUID translation (R1); only accepted candidates survive.
	s375Ctx := ctx
	report.CrossRelations = o.extractor.ValidateCrossRelations(s375Ctx, res, withIDs, originals,
		extract.CrossRelationOptions{MaxEvidenceBytes: o.crossRelationEvidenceBytes})
	o.dumpJSON("05e_cross_relation_report.json", report.CrossRelations)
	o.dumpJSON("05e_cross_validated.json", res)

	// ——3.8 UUID 分配（所有 LLM 阶段之后、ingest 之前）——
	// R1 铁律：AssignNodeUUIDs 必须在所有 LLM 阶段之后执行——它把 node.ID 从 kebab 改为 uuid，
	// 早于此会让 uuid 进入 supplement/repair/describe/relate 的 prompt，违反"UUID 零 prompt"。
	// 此处是 stage 3.75（原文验证）返回之后、stage 4（ingest）之前，是唯一合法接线点。
	//
	// R1 invariant: AssignNodeUUIDs runs ONLY after all LLM stages — it overwrites node.ID
	// from kebab to uuid; running it earlier would leak uuid into prompts (repair/describe/relate).
	// This is the legal wiring point: after source-grounded cross validation, before stage 4 (ingest).
	extract.AssignNodeUUIDs(res.Domains)
	o.dumpJSON("05f_uuid_assigned.json", res)

	// 为 shared 节点生成确定性 file_slug，已有名称保持不变。
	extract.GenerateFileSlugs(ctx, res.Domains, o.client) //nolint:staticcheck // o.client is set in NewOrchestrator
	o.dumpJSON("05g_file_slugs.json", res)

	// Select once for both outputs; resume uses the same production entry point.
	materialized, err := o.MaterializeCompletedExtraction(ctx, repoPath, dbPath, res, withIDs, skillMetas)
	report.Selection, report.Ingest, report.Writeback = materialized.Selection, materialized.Ingest, materialized.Writeback
	if err != nil {
		return report, err
	}

	logStage("✓", "流水线完成 / pipeline done")
	log.Printf("[pipeline] 完成: skills=%d docs=%d rewritten=%d skipped=%d domains=%d nodes=%d edges=%d (悬空修正: 发现%d/残留%d)",
		report.SkillsScanned, report.DocsTotal, report.DocsRewritten, len(report.Skipped),
		len(res.Domains), report.Ingest.NodesInserted, report.Ingest.EdgesInserted,
		repairRpt.DanglingFound, repairRpt.StillDangling)
	return report, nil
}

// buildMemberSources 从 AssignIDs 后的 []*AnnotatedDocument 构建 04 id → 来源文档位置列表
// 的映射，供 ingest 写 node_sources + 供 stage 4.5 回写分组。
// 遍历所有文档的所有 item，map[it.ID] = append(..., MemberSource{Skill, FilePath, StartLine/EndLine})。
//
// buildMemberSources builds the 04 id → source-document-location map from the
// AssignIDs output, for both ingest (node_sources) and stage 4.5 (write-back).
func buildMemberSources(docs []*dktypes.AnnotatedDocument) map[string][]ingest.MemberSource {
	m := make(map[string][]ingest.MemberSource)
	for _, d := range docs {
		if d == nil {
			continue
		}
		for _, it := range d.Items {
			if it.ID == "" {
				continue
			}
			ms := ingest.MemberSource{
				Skill:    d.Skill,
				FilePath: d.FilePath,
			}
			if it.SourceSpan != nil {
				ms.StartLine = it.SourceSpan.StartLine
				ms.EndLine = it.SourceSpan.EndLine
			}
			m[it.ID] = append(m[it.ID], ms)
		}
	}
	return m
}

// toWritebackMemberSources 把 ingest.MemberSource 映射转换为 writeback.MemberSource 映射。
// 两类型同构但分属两包（ingest ↔ writeback，避免 import 环），故在 orchestrator 层转换。
//
// toWritebackMemberSources converts the ingest MemberSource map to a writeback one.
// The two types are isomorphic but live in separate packages (no import cycle).
func toWritebackMemberSources(m map[string][]ingest.MemberSource) map[string][]writeback.MemberSource {
	out := make(map[string][]writeback.MemberSource, len(m))
	for k, srcs := range m {
		wb := make([]writeback.MemberSource, 0, len(srcs))
		for _, s := range srcs {
			wb = append(wb, writeback.MemberSource{
				Skill:     s.Skill,
				FilePath:  s.FilePath,
				StartLine: s.StartLine,
				EndLine:   s.EndLine,
			})
		}
		out[k] = wb
	}
	return out
}

// processDocument annotates and assesses once; unknown/rejected material stays
// document-local and never causes another paid annotation request.
func (o *Orchestrator) processDocument(ctx context.Context, skill, path, content string) (*dktypes.AnnotatedDocument, string) {
	doc, err := o.annotator.Annotate(ctx, skill, path, content)
	if err != nil {
		return nil, fmt.Sprintf("annotation unavailable: %v", err)
	}
	o.dumpJSON(fmt.Sprintf("01_annotated/%s/%s.json", sanitizeDir(skill), sanitizeFilename(path)), doc)
	cleaned, err := o.formatGate.Check(doc)
	if err != nil {
		return nil, fmt.Sprintf("invalid annotation: %v", err)
	}
	if err := o.semanticGate.Check(ctx, content, cleaned); err != nil {
		return nil, fmt.Sprintf("factual assessment unavailable/rejected: %v", err)
	}
	o.dumpJSON(fmt.Sprintf("03_assessed/%s/%s.json", sanitizeDir(skill), sanitizeFilename(path)), cleaned)
	return cleaned, ""
}

// fullRebuildIngest 完整建立、checkpoint 并关闭临时库后，原子发布主文件。
// A failed ingest must preserve the previous DB, including its WAL and ownership.
func (o *Orchestrator) fullRebuildIngest(ctx context.Context, dbPath string, res *extract.Result, metas []ingest.SkillMeta, memberSources map[string][]ingest.MemberSource, identity string) (*ingest.IngestDomainsResult, error) {
	if err := store.CheckLease(ctx); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dbPath), ".cairn-full-*.db")
	if err != nil {
		return nil, fmt.Errorf("pipeline: create temporary DB: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer os.Remove(tmpPath + "-wal")
	defer os.Remove(tmpPath + "-shm")
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("pipeline: close temporary DB file: %w", err)
	}
	db, err := storage.NewDB(storage.DBOptions{Path: tmpPath})
	if err != nil {
		return nil, fmt.Errorf("open db %s: %w", dbPath, err)
	}
	defer db.Close()

	result, err := ingest.IngestDomains(ctx, db, ingest.IngestDomainsOptions{
		Result:        res,
		Skills:        metas,
		MinConfidence: 0,
		MemberSources: memberSources,
	})
	if err != nil {
		return result, err
	}
	// Missing slugs or duplicate UUIDs must not silently discard eligible nodes.
	// Check the temporary candidate before replacing an existing database.
	expectedLeaves := 0
	for _, domain := range res.Domains {
		for _, subdomain := range domain.Subdomains {
			for _, nodes := range [][]extract.Node{subdomain.Entities, subdomain.Concepts} {
				for range nodes {
					expectedLeaves++
				}
			}
		}
	}
	if actual := result.EntityNodes + result.ConceptNodes; actual != expectedLeaves {
		return result, fmt.Errorf("pipeline: incomplete materialization: %d/%d eligible knowledge nodes inserted", actual, expectedLeaves)
	}
	if err := storage.NewRepositoryIdentityRepo(db).Set(ctx, identity); err != nil {
		return result, fmt.Errorf("pipeline: stamp repository identity: %w", err)
	}
	var busy, frames, checkpointed int
	if err := db.Conn().QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
		return result, fmt.Errorf("pipeline: checkpoint candidate (busy=%d): %v", busy, err)
	}
	if err := db.Close(); err != nil {
		return result, fmt.Errorf("pipeline: close candidate: %w", err)
	}
	// Replacing a main file while old WAL/SHM connections remain would attach
	// old pages to a new graph. Never delete those sidecars to force publication.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(dbPath + suffix); !os.IsNotExist(err) {
			return result, fmt.Errorf("pipeline: cannot replace DB with existing %s sidecar; close/checkpoint existing connections first", suffix)
		}
	}
	if err := store.CheckLease(ctx); err != nil {
		return result, err
	}
	if err := os.Rename(tmpPath, dbPath); err != nil {
		return result, fmt.Errorf("pipeline: publish rebuilt DB: %w", err)
	}
	return result, nil
}

// logStage 输出显著的阶段分隔线，便于在连续日志中辨认阶段边界。
// step 是阶段编号（1/2/3/3.5/4/✓），name 是阶段中英文名。
//
// logStage prints a prominent stage separator for readability in the log stream.
func logStage(step, name string) {
	log.Printf("══════════ 阶段/Stage %s: %s ══════════", step, name)
}

// countRefFiles 统计 skills 下的 reference 文档总数。
// countRefFiles counts total reference files across skills.
func countRefFiles(skills []discovery.Skill) int {
	n := 0
	for _, sk := range skills {
		n += len(sk.ReferenceFiles)
	}
	return n
}

// dumpJSON 在 dumpDir 非空时把 v 序列化为带缩进的 JSON 写到 dumpDir/relPath。
// 目录自动创建；写失败仅记日志、不中断流水线（dump 是观测辅助，非核心路径）。
// dumpJSON writes v as indented JSON to dumpDir/relPath when dumpDir is non-empty.
// Directories are auto-created; write errors are logged but never fail the pipeline.
func (o *Orchestrator) dumpJSON(relPath string, v any) {
	if o.dumpDir == "" {
		return
	}
	full := filepath.Join(o.dumpDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		log.Printf("[pipeline] dump mkdir 失败 %s: %v", filepath.Dir(full), err)
		return
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Printf("[pipeline] dump 序列化失败 %s: %v", relPath, err)
		return
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		log.Printf("[pipeline] dump 写文件失败 %s: %v", full, err)
	}
}

// sanitizeFilename 把文档相对路径转成安全的文件名（/ → _，保留可读性）。
// sanitizeFilename converts a doc relative path to a safe filename (/ → _).
func sanitizeFilename(path string) string {
	s := strings.ReplaceAll(path, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.TrimSuffix(s, ".md")
	return s
}

// sanitizeDir 把 skill 名转成安全的目录名。
// sanitizeDir converts a skill name to a safe directory name.
func sanitizeDir(skill string) string {
	s := strings.ReplaceAll(skill, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}
