// Package incremental 的本文件实现 I-3 标注（管道 P 的第一阶段）：
// 仅对「有块外新增（C4）」的文档，仅标注其块外新增片段（DocChangeSet.C4Text——
// 块区域已置空、行号与原档 1:1 对齐），绝不重标已有 uuid 块。
//
// 复用全量流水线的同一套组件与语义（契约一致）：
//   - annotation.LLMAnnotator：per-doc LLM 标注（内部原地重试）；
//   - annotation.FormatGate（Gate A 代码门）+ annotation.SemanticGate（Gate B LLM 语义门）；
//   - 回退重标注（Gate 不通过 → 重标，受 maxRollbacks 上限约束），耗尽降级跳过该文档
//     （不阻塞其它文档——与全量「无失败终态」一致）；
//   - extract.AssignIDs：刷 04 id（<slug(skill)>-<tag>-<slug(name)>）。
//
// 产出带 04 id + SourceSpan（与原档行号对齐）的新标注单元，供 I-4 增量对齐消费。
//
// This file implements I-3 annotation: only docs with C4 additions are annotated,
// and only their outside-block text (C4Text, line-aligned to the doc). It reuses
// the full pipeline's annotator and both gates with the same rollback/degrade
// semantics, then mints 04 ids via extract.AssignIDs.
package incremental

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/parallel"
	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
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
	// Skipped 是回退耗尽降级跳过（或空文本跳过）的文档及原因（可观测，不阻塞流水线）。
	// Skipped lists docs degraded after exhausting rollbacks (observability).
	Skipped []annotateSkip
}

// annotator 封装 I-3 的标注组件三元组（复用全量组件，依赖注入 llm.Client 便于 mock）。
// annotator bundles the I-3 annotation components (full-pipeline reuse).
type annotator struct {
	llm          *annotation.LLMAnnotator
	formatGate   *annotation.FormatGate
	semanticGate *annotation.SemanticGate
	maxRollbacks int
}

// newAnnotator 组装 I-3 标注器（与全量同一套组件与参数语义）。
// newAnnotator assembles the I-3 annotator (same components as the full pipeline).
func newAnnotator(client llm.Client, maxTokens, maxRetries, maxRollbacks, maxNameRunes int) *annotator {
	if maxRollbacks <= 0 {
		maxRollbacks = 3
	}
	return &annotator{
		llm:          annotation.NewLLMAnnotator(client, maxTokens, maxRetries),
		formatGate:   annotation.NewFormatGate(maxNameRunes),
		semanticGate: annotation.NewSemanticGate(client, maxTokens, maxRetries),
		maxRollbacks: maxRollbacks,
	}
}

// annotateC4Docs 并发标注所有含 C4 的文档（仅块外新增片段）。
// c4TextByDoc 提供每篇文档的「块区域置空」标注输入（DocChangeSet.C4Text）。
// 文档间完全隔离、互不阻塞；单篇回退耗尽降级跳过。
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

	type docResult struct {
		path   string
		doc    *dktypes.AnnotatedDocument
		reason string
	}
	results := make([]docResult, len(docs))
	parallel.ForEachIndexed(parallel.MaxConcurrency, docs, func(i int, path string) {
		text := c4TextByDoc[path]
		skill := pp.SkillByDoc[path]
		doc, reason := a.annotateOne(ctx, skill, path, text)
		results[i] = docResult{path: path, doc: doc, reason: reason}
	})

	var passed []*dktypes.AnnotatedDocument
	for _, r := range results {
		if r.doc == nil {
			out.Skipped = append(out.Skipped, annotateSkip{Path: r.path, Reason: r.reason})
			continue
		}
		if len(r.doc.Items) == 0 {
			// 标注成功但零产出（纯叙述新增）——也记为跳过，供 per-doc C4 状态判
			// 「未解决」（问题 2：零产出时人类散文必须保留，不得静默删除）。
			out.Skipped = append(out.Skipped, annotateSkip{Path: r.path,
				Reason: "标注零产出（新增文本未含可吸收的 entity/concept 单元）"})
			continue
		}
		passed = append(passed, r.doc)
	}
	// 刷 04 id（与全量同一函数：<slug(skill)>-<tag>-<slug(name)>）。
	out.Docs = extract.AssignIDs(passed)
	return out
}

// annotateOne 对单篇文档的块外片段执行「标注 → Gate A → Gate B」，失败回退重标，
// 耗尽降级跳过（与全量 processDocument 语义一致）。
// annotateOne runs annotate→GateA→GateB for one doc's outside-block text with
// rollback-on-failure and degrade-on-exhaustion (full-pipeline semantics).
func (a *annotator) annotateOne(ctx context.Context, skill, path, text string) (*dktypes.AnnotatedDocument, string) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Sprintf("%s: 块外新增为空（跳过）", path)
	}
	var lastReason string
	for attempt := 1; attempt <= a.maxRollbacks; attempt++ {
		doc, err := a.llm.Annotate(ctx, skill, path, text)
		if err != nil {
			lastReason = fmt.Sprintf("%s 标注失败: %v", path, err)
			continue
		}
		cleaned, err := a.formatGate.Check(doc)
		if err != nil {
			lastReason = fmt.Sprintf("%s Gate A 未通过: %v", path, err)
			if !errors.Is(err, annotation.ErrRollbackToAnnotate) {
				log.Printf("[incremental-annotate] %s Gate A 非回退错误，仍回退重标注: %v", path, err)
			}
			continue
		}
		// Gate B 核验「片段原文 ↔ 标注」语义幂等（原文 = C4Text，不是全档）。
		if err := a.semanticGate.Check(ctx, text, cleaned); err != nil {
			lastReason = fmt.Sprintf("%s Gate B 未通过: %v", path, err)
			continue
		}
		return cleaned, ""
	}
	log.Printf("[incremental-annotate] %s 回退 %d 次仍未通过，降级跳过（%s）", path, a.maxRollbacks, lastReason)
	return nil, lastReason
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
