// Package incremental 的本文件实现 I-3 标注（管道 P 的第一阶段）：
// 仅对「有块外新增（C4）」的文档，仅标注其块外新增片段（DocChangeSet.C4Text——
// 块区域已置空、行号与原档 1:1 对齐），绝不重标已有 uuid 块。
//
// 复用全量流水线的同一套组件与语义（契约一致）：
//   - annotation.DocumentProcessor：共享有限失败步骤重试与整篇语义修订；
//   - 标注/核验失败仅影响本篇，成功兄弟不重跑；
//   - extract.AssignIDs：刷 04 id（<slug(skill)>-<tag>-<slug(name)>）。
//
// 产出带 04 id + SourceSpan（与原档行号对齐）的新标注单元，供 I-4 增量对齐消费。
//
// This file implements I-3 annotation: only docs with C4 additions are annotated,
// and only their outside-block text (C4Text, line-aligned to the doc). It reuses
// the same finite document processor as the full helper, then mints 04 ids via extract.AssignIDs.
package incremental

import (
	"context"
	"fmt"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/parallel"
)

// annotateSkip 是一篇被降级跳过的文档及其原因（结构化，供 per-doc C4 状态判定）。
// annotateSkip is a degraded/skipped doc with its reason.
type annotateSkip struct {
	Path   string
	Reason string
}

// annotateOutput 是 I-3 的产出：通过双门的新标注文档（含 04 id）+ 降级跳过的文档。
// annotateOutput is I-3's output: gate-passed annotated docs (with 04 ids) + skips.
type annotateOutput struct {
	// Docs 是通过双门、刷了 04 id 的标注文档（每篇只含块外新增的单元）。
	// Docs are gate-passed annotated docs (only outside-block units) with 04 ids.
	Docs []*dktypes.AnnotatedDocument
	// Skipped 是标注失败（或空文本跳过）的文档及原因（可观测，不阻塞流水线）。
	// Skipped lists document-local annotation failures.
	Skipped []annotateSkip
	// Results 按路径保留所有文档的事实结果和实际尝试；只有 complete 候选可下传。
	// Results retain every source outcome and attempts; only complete candidates are accepted.
	Results []annotation.DocumentResult
}

// documentProcessor 让增量只依赖共享文档处理契约，不复制重试状态机。
// documentProcessor is the common processing contract, with no adapter retry loop.
type documentProcessor interface {
	Process(context.Context, string, string, string) annotation.DocumentResult
}

// annotator 复用全量标注处理器；接口便于确定性测试取消与终态汇合。
// annotator reuses the full document processor and permits deterministic join tests.
type annotator struct {
	processor documentProcessor
}

// newAnnotator 的 maxRetries 与共享处理器一致：正数为全篇共享的额外恢复次数，
// 0 使用默认 3，负数禁止额外恢复；初始生成与核验不消耗恢复次数。
// maxRetries is the shared EXTRA recovery allowance (0 defaults to 3; negative disables it).
func newAnnotator(client llm.Client, maxTokens, maxRetries, maxNameRunes int) *annotator {
	return &annotator{processor: annotation.NewDocumentProcessor(client, maxTokens, maxRetries, maxNameRunes)}
}

// annotateC4Docs 并发标注所有含 C4 的文档（仅块外新增片段）。
// c4TextByDoc 提供每篇文档的「块区域置空」标注输入（DocChangeSet.C4Text）。
// 文档间完全隔离、互不阻塞；单篇失败跳过。
//
// annotateC4Docs concurrently annotates every doc that has C4 additions, feeding
// only the blanked outside-block text.
func (a *annotator) annotateC4Docs(ctx context.Context, pp *PipelinePlan, c4TextByDoc map[string]string) *annotateOutput {
	out := &annotateOutput{}
	if len(pp.C4ByDoc) == 0 {
		return out
	}

	// 稳定序任务列表（输出确定性）。
	docs := make([]string, 0, len(pp.C4ByDoc))
	for path := range pp.C4ByDoc {
		docs = append(docs, path)
	}
	sortStrings(docs)

	results := make([]annotation.DocumentResult, len(docs))
	parallel.ForEachIndexed(parallel.MaxConcurrency, docs, func(i int, path string) {
		results[i] = a.processor.Process(ctx, pp.SkillByDoc[path], path, c4TextByDoc[path])
	})
	// 先等所有文档终态再汇合；失败候选只作证据，不能当作权威空替换。
	// Join every terminal outcome; failed candidates are trace evidence only.
	out.Results = results
	var passed []*dktypes.AnnotatedDocument
	for _, r := range results {
		if reason := annotationSkipReason(r); reason != "" {
			out.Skipped = append(out.Skipped, annotateSkip{Path: r.FilePath, Reason: reason})
			continue
		}
		passed = append(passed, r.Document)
	}
	// 刷 04 id（与全量同一函数：<slug(skill)>-<tag>-<slug(name)>）。
	out.Docs = extract.AssignIDs(passed)
	return out
}

// annotateOne 使用同一处理器；失败只影响当前文档，理由供既有跳过契约消费。
// annotateOne delegates to the same processor and preserves document-local skip reasons.
func (a *annotator) annotateOne(ctx context.Context, skill, path, text string) (*dktypes.AnnotatedDocument, string) {
	result := a.processor.Process(ctx, skill, path, text)
	if reason := annotationSkipReason(result); reason != "" {
		return nil, reason
	}
	return result.Document, ""
}

// annotationSkipReason 保留零产出散文和失败基线；未接受候选不能进入对齐。
// Zero-item prose and unsuccessful candidates remain unresolved under the existing contract.
func annotationSkipReason(result annotation.DocumentResult) string {
	if result.Outcome != annotation.DocumentComplete {
		return fmt.Sprintf("%s 标注%s: %s", result.FilePath, result.Outcome, result.Reason)
	}
	if result.Document == nil || len(result.Document.Items) == 0 {
		return fmt.Sprintf("%s: 标注零产出（新增文本未含可吸收的 entity/concept 单元）", result.FilePath)
	}
	return ""
}

// sortStrings 稳定排序（本包小工具，避免到处 import sort）。
// sortStrings sorts strings in place (small local helper).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
