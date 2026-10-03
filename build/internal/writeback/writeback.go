// Package writeback 的本文件实现回写主入口 Writeback：
// 遍历所有 entity/concept node → 判 shared → 按 file_path 分组 →
// 对每个来源文档整篇覆盖写 .md + 写 sidecar；对每个 shared node 写 _shared primary + sidecar。
//
// 铁律：
//   - R4：不回写 relation（渲染代码不含任何 relation 信息）。
//   - R6：.md 整篇覆盖写入（os.WriteFile，非追加）；原文由 git 历史留档。
//   - R7：只 summary/description 可编辑；其余只读展示；镜像块整体只读。
//
// 部分失败返回诊断统计及 error；流水线必须阻止发布未闭环的 MD / sidecar / KG。
//
// This file implements the write-back entry Writeback: walks all entity/concept
// nodes, detects shared, groups by file_path, writes each source doc (whole-file
// replace) + sidecar, and writes _shared primaries for shared nodes. R4: no
// relation; R6: whole-file replace; R7: only summary/description editable.
package writeback

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/extract"
)

// Writeback 把全量构建产出的 KG 结构化回写为 md + sidecar。
//
// 参数：
//   - res：提取器产出（含 domain/subdomain/entity/concept，node.ID 已是 UUID）。
//   - memberSources：04 id → 来源文档位置列表的映射（由 orchestrator 从 AssignIDs 后的
//     []*dktypes.AnnotatedDocument 构建）。
//   - repoRoot：仓库根目录的绝对路径，回写的 .md 相对它定位。
//
// 返回 (report, error)：error 非 nil 表示回写过程中有错误（可能是部分错误），report 含
// 已完成统计。调用方必须阻止发布，不能把部分回写结果提升为 stable。
//
// Writeback materializes the KG into structured md + sidecar. Returns (report, err);
// err is non-nil on any failure but report still carries partial stats. Callers
// must not publish a partially materialized candidate.
func Writeback(res *extract.Result, memberSources map[string][]MemberSource, repoRoot string) (*Report, error) {
	return WritebackContext(context.Background(), res, memberSources, repoRoot)
}

// WritebackContext fences every managed pair write with the caller lease.
func WritebackContext(ctx context.Context, res *extract.Result, memberSources map[string][]MemberSource, repoRoot string) (*Report, error) {
	report := &Report{}
	if res == nil {
		return report, fmt.Errorf("writeback: result is nil")
	}
	generatedAt := time.Now()

	// 1. 派生全部 node 的回写视图（含 shared 判定 + 来源文档聚合）。
	views := buildNodeViews(res, memberSources)
	if len(views) == 0 {
		return report, nil
	}

	// 2. 按来源文档分组。
	groups, primaries := groupByDoc(views)

	// errs 累积所有非致命的部分失败：单文档/单 primary 写失败被降级（继续处理其余），
	// 并返回 error，阻止把部分产物发布为 stable。
	// errs accumulates non-fatal per-doc/per-primary failures: each is degraded
	// (continue) but collected into the returned error for observability.
	var errs []error

	// 3. 逐来源文档：渲染其所有 node 块 → 整篇覆盖写 .md + 写 sidecar。
	//    shared node 在文档中渲染为镜像块；非 shared 渲染为完整块。
	for _, g := range groups {
		mdAbsPath, pathErr := safeRepoPath(repoRoot, g.FilePath)
		if pathErr != nil {
			errs = append(errs, pathErr)
			continue
		}
		// 渲染整篇 md：shared node 在该文档渲染为镜像块，非 shared 渲染完整块。
		mdContent := renderDoc(g.Nodes, isMirrorForDoc)

		// 先验证并准备两份内容；普通错误回滚，崩溃由持久化 journal 恢复。
		sf := buildSidecar(g.FilePath, mdContent, g.Nodes, generatedAt)
		if err := writeDocPairContext(ctx, mdAbsPath, []byte(mdContent), sf); err != nil {
			errs = append(errs, fmt.Errorf("write doc pair %s: %w", g.FilePath, err))
			continue
		}
		report.DocsWritten++
		// 统计：非 shared 计 NodesWritten；shared 计 MirrorBlocks。
		for _, v := range g.Nodes {
			if v.Shared {
				report.MirrorBlocks++
			} else {
				report.NodesWritten++
			}
		}

		report.SidecarsWritten++
	}

	// 4. 逐 shared node：写 _shared/<domain-slug>/<uuid>.md primary + sidecar。
	for _, v := range primaries {
		primaryAbs, pathErr := safeRepoPath(repoRoot, primaryFilePath(v))
		if pathErr != nil {
			errs = append(errs, pathErr)
			continue
		}
		// primary 渲染为完整可编辑块（与非 shared 同结构）。
		primaryContent := docHeaderComment + "\n\n" + renderFullBlock(v) + "\n"
		primaryRel := primaryFilePath(v)
		sf := buildSidecar(primaryRel, primaryContent, []nodeView{v}, generatedAt)
		if err := writeDocPairContext(ctx, primaryAbs, []byte(primaryContent), sf); err != nil {
			errs = append(errs, fmt.Errorf("write primary pair %s: %w", primaryRel, err))
			continue
		}
		report.PrimaryFiles++
		report.SharedNodes++

		report.SidecarsWritten++
	}

	// 聚合部分失败为一个 error（nil 表示全部成功）。report 始终携带已完成的部分统计。
	// Aggregate partial failures into one error (nil if all succeeded); report always
	// carries the partial stats already accomplished.
	if len(errs) > 0 {
		return report, fmt.Errorf("writeback: %d 处部分失败: %w", len(errs), errors.Join(errs...))
	}
	return report, nil
}
