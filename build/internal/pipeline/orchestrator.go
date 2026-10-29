// Package pipeline builds a source-bound graph in an isolated database.
// 有限语义修订只作用于失败范围，成功兄弟保留；最终状态覆盖全部来源。
// Finite semantic recovery retains successful siblings and reports every source outcome.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
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
	// MaxRetries>0 为每个失败范围共享的额外恢复次数；0 默认3，负值禁用。
	// Positive MaxRetries means EXTRA recoveries; provider HTTP retries are separate.
	MaxRetries int
	// MaxRollbacks is retired and ignored.
	MaxRollbacks int
	// MinConfidence is retired and ignored; finite confidence is descriptive.
	MinConfidence float64
	// MaxNameRunes 保留调用兼容；严格候选格式由共享处理器校验。
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
	processor                  *annotation.DocumentProcessor
	maxRetries                 int
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
		processor:                  annotation.NewDocumentProcessor(opts.Client, opts.MaxTokens, opts.MaxRetries, opts.MaxNameRunes),
		maxRetries:                 opts.MaxRetries,
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
// NodeCorrespondence 保留核验时的临时节点与实际 UUID 对应，不重算候选摘要冒充核验。
// The exact checked text and member set survive UUID translation unchanged.
type NodeCorrespondence struct {
	Domain, Subdomain, Alias, UUID string
	Members                        []string
	Summary, Description           string
}

type RunReport struct {
	Status             string
	Reasons            []string
	Discovery          *discovery.Report
	Documents          []annotation.DocumentResult
	Relations          *extract.RelationProcessReport
	FinalCoverage      *extract.FinalCoverageReport
	UUIDCorrespondence []NodeCorrespondence
	SkillsScanned      int
	DocsTotal          int
	DocsRewritten      int          // 通过双门、进入提取的文档数
	Skipped            []SkippedDoc // 降级跳过的文档
	ExtractRounds      int
	Selection          *extract.CandidateSelectionReport // 同一候选集用于入库与回写
	Ingest             *ingest.IngestDomainsResult
	Coverage           *extract.CoverageReport      // 覆盖保障报告（05c1 阶段）；nil 表示未执行
	Repair             *extract.RepairReport        // 悬空边修正报告（05b/05c 阶段）；nil 表示未执行修正
	Describe           *extract.DescribeReport      // description 融合报告（05d 阶段）；nil 表示未执行
	CrossRelations     *extract.CrossRelationReport // 原文验证的完整候选结果 / source-grounded cross-candidate outcomes
	Relate             *extract.RelateReport        // relation 补充报告（05e 阶段）；nil 表示未执行
	// Writeback 是 stage 4.5 结构化回写报告（06b 阶段）；nil 表示未执行回写。
	// 回写失败保留诊断统计，但阻止发布不完整的源闭环。
	// Writeback is the stage 4.5 write-back report; nil if not executed.
	// Write-back failure aborts publication while retaining diagnostic stats.
	Writeback *writeback.Report
}

// RunFullRebuild 使用来源清单和共享有限 helper；失败文档不擦除成功兄弟。
// RunFullRebuild materializes verified independent work and reports incomplete scopes.
func (o *Orchestrator) RunFullRebuild(ctx context.Context, repoPath, dbPath string) (report *RunReport, runErr error) {
	report = &RunReport{}
	defer func() {
		report.finish(ctx, runErr)
		o.dumpJSON("07_run_report.json", report)
		if runErr == nil && report.Status != "complete" {
			runErr = fmt.Errorf("pipeline: %s: %s", report.Status, strings.Join(report.Reasons, "; "))
		}
	}()
	if err := store.CheckLease(ctx); err != nil {
		return report, err
	}
	if _, err := repoidentity.Ensure(ctx, repoPath, o.repositoryIdentity); err != nil {
		return report, fmt.Errorf("pipeline: repository identity: %w", err)
	}
	logStage("1", "来源清单 / discovery")
	scan, scanErr := o.scanner.ScanReport(ctx, repoPath, o.rules)
	report.Discovery = &scan
	report.SkillsScanned = len(scan.Skills)
	o.dumpJSON("00_scan.json", scan)
	if scanErr != nil {
		report.Reasons = append(report.Reasons, "discovery unavailable: "+scanErr.Error())
	}
	skillMetas := make([]ingest.SkillMeta, 0, len(scan.Skills))
	for _, sk := range scan.Skills {
		skillMetas = append(skillMetas, ingest.SkillMeta{Name: sk.Name, Summary: sk.DomainHint})
	}
	// Sources 已按归一化路径去重；绝不再连接 ReferenceFiles 或重新读取内容。
	// Consume source bytes exactly once, including the complete SKILL body.
	type task struct {
		source discovery.Source
		skill  string
	}
	var tasks []task
	for _, source := range scan.Sources {
		if source.Disposition != discovery.Supported {
			if source.Disposition != discovery.Excluded && source.Disposition != discovery.Unsupported {
				report.Reasons = append(report.Reasons, fmt.Sprintf("discovery %s %s: %s", source.Disposition, source.Path, source.Reason))
			}
			continue
		}
		skill := ""
		longest := -1
		for _, sk := range scan.Skills {
			if source.Path == sk.MarkerFile || sk.Path == "." || strings.HasPrefix(source.Path, strings.TrimSuffix(sk.Path, "/")+"/") {
				if len(sk.Path) > longest {
					skill = sk.Name
					longest = len(sk.Path)
				}
			}
		}
		tasks = append(tasks, task{source, skill})
	}
	report.DocsTotal = len(tasks)
	report.Documents = make([]annotation.DocumentResult, len(tasks))
	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		go func(i int, t task) {
			defer wg.Done()
			report.Documents[i] = o.processor.Process(ctx, t.skill, t.source.Path, string(t.source.Content))
		}(i, t)
	}
	wg.Wait() // Terminal join preserves every actual attempt, even when another document fails.
	var passed []*dktypes.AnnotatedDocument
	var originals []extract.FrozenSource
	for i, r := range report.Documents {
		o.dumpJSON(fmt.Sprintf("03_assessed/%s/%s.json", sanitizeDir(r.Skill), sanitizeFilename(r.FilePath)), r)
		if r.Outcome != annotation.DocumentComplete || r.Document == nil {
			report.Skipped = append(report.Skipped, SkippedDoc{Skill: r.Skill, Path: r.FilePath, Reason: string(r.Outcome) + ": " + r.Reason})
			continue
		}
		passed = append(passed, r.Document)
		originals = append(originals, extract.FreezeSource(r.Skill, r.FilePath, string(tasks[i].source.Content)))
		report.DocsRewritten++
	}
	sort.SliceStable(passed, func(i, j int) bool {
		if passed[i].Skill != passed[j].Skill {
			return passed[i].Skill < passed[j].Skill
		}
		return passed[i].FilePath < passed[j].FilePath
	})
	o.dumpJSON("00_frozen_sources.json", originals)
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	if len(passed) == 0 && (report.DocsTotal > 0 || scanErr != nil) {
		return report, fmt.Errorf("pipeline: no complete source documents")
	}
	withIDs := extract.AssignIDs(passed)
	o.dumpJSON("04_extract_input.json", withIDs)
	res, cov, err := o.extractor.ExtractWithCoverageOptions(ctx, withIDs, extract.CoverageOptions{MaxRetries: o.maxRetries})
	report.Coverage = cov
	if err != nil {
		return report, fmt.Errorf("pipeline: extract: %w", err)
	}
	report.ExtractRounds = res.Rounds
	// Completeness filtering can expose missing material. Reuse the remaining mapping allowance.
	if err = extract.ValidateFusionCompleteness(res, withIDs, 0, false); err != nil {
		return report, err
	}
	if final := extract.ComputeFinalCoverage(res, withIDs); final.Outcome != "complete" && !cov.Exhausted {
		allowance := o.maxRetries
		if allowance == 0 {
			allowance = annotation.DefaultAnnotateMaxRetries
		}
		if allowance < 0 {
			allowance = 0
		}
		allowance -= max(0, cov.ExtractAttempts-1)
		if allowance > 0 {
			extra := allowance - 1
			if extra == 0 {
				extra = -1
			}
			repaired, repair, repairErr := o.extractor.RepairFinalCoverage(ctx, res, withIDs, extract.CoverageOptions{MaxRetries: extra})
			res = repaired
			report.Coverage = mergeCoverageReports(cov, repair)
			if repairErr != nil {
				return report, repairErr
			}
		}
	}
	report.Describe = o.extractor.FuseDescriptions(ctx, res, withIDs)
	retainVerifiedDescriptions(res, report.Describe)
	if err = extract.ValidateFusionCompleteness(res, withIDs, 0, true); err != nil {
		return report, err
	}
	report.Relations = o.extractor.ProcessRelations(ctx, res, withIDs, originals, extract.RelationProcessOptions{MaxRetries: o.maxRetries})
	// Preserve current selected classification, Sources and Evidence in the established envelope.
	extract.ApplySelectedRelationAssertions(res, report.Relations)
	o.dumpJSON("05e_relation_report.json", report.Relations)
	o.dumpJSON("05e_extract_related.json", res)
	report.UUIDCorrespondence = nodeCorrespondence(res)
	extract.AssignNodeUUIDs(res.Domains)
	if err = verifyNodeCorrespondence(res, report.UUIDCorrespondence); err != nil {
		return report, err
	}
	o.dumpJSON("05f_uuid_correspondence.json", report.UUIDCorrespondence)
	extract.GenerateFileSlugs(ctx, res.Domains, o.client)
	o.dumpJSON("05g_file_slugs.json", res)
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	materialized, err := o.MaterializeCompletedExtraction(ctx, repoPath, dbPath, res, withIDs, skillMetas)
	if materialized != nil {
		report.Selection = materialized.Selection
		report.Ingest = materialized.Ingest
		report.Writeback = materialized.Writeback
		report.FinalCoverage = materialized.FinalCoverage
	}
	return report, err
}

// mergeCoverageReports 不重置失败范围的 allowance，保留原始尝试和最终存活计数。
// Merge factual recovery receipts while retaining all earlier attempts.
func mergeCoverageReports(first, last *extract.CoverageReport) *extract.CoverageReport {
	if last == nil {
		return first
	}
	if first == nil {
		return last
	}
	last.Attempts = append(append([]extract.CoverageAttempt(nil), first.Attempts...), last.Attempts...)
	last.ExtractAttempts += first.ExtractAttempts
	return last
}

// retainVerifiedDescriptions 防止失败节点的旧非空候选通过结构门冒充已核验。
// Only the exact complete verified pair is eligible for materialization.
func retainVerifiedDescriptions(res *extract.Result, rpt *extract.DescribeReport) {
	verified := map[string]extract.NodeDescriptionOutcome{}
	for _, out := range rpt.Outcomes {
		if out.Outcome == annotation.DocumentComplete {
			verified[out.Domain+"\x00"+out.Subdomain+"\x00"+out.ID] = out
		}
	}
	for di := range res.Domains {
		d := &res.Domains[di]
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			keep := func(nodes []extract.Node) []extract.Node {
				out := make([]extract.Node, 0, len(nodes))
				for _, n := range nodes {
					v, ok := verified[d.Name+"\x00"+sd.Name+"\x00"+n.ID]
					if ok && v.Summary == n.Summary && v.Description == n.Description {
						out = append(out, n)
					}
				}
				return out
			}
			sd.Entities = keep(sd.Entities)
			sd.Concepts = keep(sd.Concepts)
		}
	}
}
func nodeCorrespondence(res *extract.Result) []NodeCorrespondence {
	var out []NodeCorrespondence
	for _, d := range res.Domains {
		for _, sd := range d.Subdomains {
			for _, nodes := range [][]extract.Node{sd.Entities, sd.Concepts} {
				for _, n := range nodes {
					out = append(out, NodeCorrespondence{Domain: d.Slug, Subdomain: sd.Slug, Alias: n.ID, UUID: extract.NodeUUID(n.Members), Members: append([]string(nil), n.Members...), Summary: n.Summary, Description: n.Description})
				}
			}
		}
	}
	return out
}
func verifyNodeCorrespondence(res *extract.Result, before []NodeCorrespondence) error {
	after := nodeCorrespondence(res)
	if len(after) != len(before) {
		return fmt.Errorf("pipeline: UUID translation changed node denominator")
	}
	for i, n := range after {
		a := before[i]
		if n.Alias != a.UUID || n.Domain != a.Domain || n.Subdomain != a.Subdomain || n.Summary != a.Summary || n.Description != a.Description || strings.Join(n.Members, "\x00") != strings.Join(a.Members, "\x00") {
			return fmt.Errorf("pipeline: UUID translation changed verified node %s/%s/%s", a.Domain, a.Subdomain, a.Alias)
		}
	}
	return nil
}

// finish 是 .6 唯一全量产品汇总；保留 unavailable/cancelled 的具体范围和原因。
// Stage receipts remain factual; only the ordinary orchestrator decides product status.
func (r *RunReport) finish(ctx context.Context, err error) {
	for _, doc := range r.Documents {
		if doc.Outcome != annotation.DocumentComplete {
			r.Reasons = append(r.Reasons, fmt.Sprintf("document %s %s: %s", doc.FilePath, doc.Outcome, doc.Reason))
		}
	}
	if r.Coverage != nil && r.Coverage.Outcome != "complete" {
		r.Reasons = append(r.Reasons, "mapping "+r.Coverage.Outcome+": "+r.Coverage.Reason)
	}
	if r.Describe != nil {
		for _, out := range r.Describe.Outcomes {
			if out.Outcome != annotation.DocumentComplete {
				r.Reasons = append(r.Reasons, fmt.Sprintf("node %s %s: %s", out.ID, out.Outcome, out.Reason))
			}
		}
	}
	if r.Relations != nil {
		for _, out := range r.Relations.Scopes {
			if out.Outcome != annotation.DocumentComplete {
				r.Reasons = append(r.Reasons, fmt.Sprintf("relations %s %s: %s", out.Scope, out.Outcome, out.Reason))
			}
		}
	}
	if r.FinalCoverage != nil && r.FinalCoverage.Outcome != "complete" {
		r.Reasons = append(r.Reasons, "final "+r.FinalCoverage.Outcome+": "+r.FinalCoverage.Reason)
	}
	if err != nil {
		reason := "unavailable: "
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			reason = "cancelled: "
		}
		r.Reasons = append(r.Reasons, reason+err.Error())
	}
	sort.Strings(r.Reasons)
	r.Reasons = compactReasons(r.Reasons)
	r.Status = "complete"
	if len(r.Reasons) > 0 {
		r.Status = "failed"
		if r.DocsRewritten > 0 || (r.Ingest != nil && r.Ingest.NodesInserted > 0) {
			r.Status = "partial"
		}
	}
}
func compactReasons(in []string) []string {
	var out []string
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
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

// processDocument 保留包内兼容入口，使用相同有限处理器且不叠加重试。
func (o *Orchestrator) processDocument(ctx context.Context, skill, path, content string) (*dktypes.AnnotatedDocument, string) {
	r := o.processor.Process(ctx, skill, path, content)
	if r.Outcome != annotation.DocumentComplete {
		return nil, string(r.Outcome) + ": " + r.Reason
	}
	return r.Document, ""
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

// MarshalJSON 只发布来源处置与实际尝试；原文/完整候选仍在已有私有00/03/04产物。
// Public controller status must not embed caller-owned raw source bodies.
func (r RunReport) MarshalJSON() ([]byte, error) {
	type reportJSON RunReport
	safe := reportJSON(r)
	safe.Documents = append([]annotation.DocumentResult(nil), r.Documents...)
	for i := range safe.Documents {
		safe.Documents[i].Document = nil
	}
	if r.Discovery != nil {
		copy := *r.Discovery
		copy.Sources = append([]discovery.Source(nil), r.Discovery.Sources...)
		for i := range copy.Sources {
			copy.Sources[i].Content = nil
		}
		safe.Discovery = &copy
	}
	return json.Marshal(safe)
}
