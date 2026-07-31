// Package writeback 的本文件实现回写主入口 Writeback：
// 遍历所有 entity/concept node → 判 shared → 按 file_path 分组 →
// 对每个来源文档整篇覆盖写 .md + 写 sidecar；对每个 shared node 写 _shared primary + sidecar。
//
// 铁律：
//   - R4：不回写 relation（渲染代码不含任何 relation 信息）。
//   - R6：.md 整篇覆盖写入（os.WriteFile，非追加）；原文由 git 历史留档。
//   - R7：只 summary/description 可编辑；其余只读展示；镜像块整体只读。
//
// 回写失败不阻断流水线：Writeback 返回 (report, error)，error 非 nil 时 report 仍含已完成的
// 部分统计，调用方（orchestrator）记录错误但继续（KG 已入库的正确性不受回写影响）。
//
// This file implements the write-back entry Writeback: walks all entity/concept
// nodes, detects shared, groups by file_path, writes each source doc (whole-file
// replace) + sidecar, and writes _shared primaries for shared nodes. R4: no
// relation; R6: whole-file replace; R7: only summary/description editable.
package writeback

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/extract"
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
// 已完成统计。调用方应容忍 error（回写是产物输出，不影响 KG 已入库的正确性）。
//
// Writeback materializes the KG into structured md + sidecar. Returns (report, err);
// err is non-nil on any failure but report still carries partial stats. Callers
// should tolerate err (write-back is an output, not a KG-correctness concern).
func Writeback(res *extract.Result, memberSources map[string][]MemberSource, repoRoot string) (*Report, error) {
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
	// 但收集为返回 error 以便可观测（回写失败不阻断流水线，但要让调用方看得见）。
	// errs accumulates non-fatal per-doc/per-primary failures: each is degraded
	// (continue) but collected into the returned error for observability.
	var errs []error

	// 3. 逐来源文档：渲染其所有 node 块 → 整篇覆盖写 .md + 写 sidecar。
	//    shared node 在文档中渲染为镜像块；非 shared 渲染为完整块。
	for _, g := range groups {
		mdAbsPath := filepath.Join(repoRoot, g.FilePath)
		// 渲染整篇 md：shared node 在该文档渲染为镜像块，非 shared 渲染完整块。
		mdContent := renderDoc(g.Nodes, isMirrorForDoc)

		// 整篇覆盖写入（R6：非追加）。
		if err := os.MkdirAll(filepath.Dir(mdAbsPath), 0o755); err != nil {
			log.Printf("[writeback] mkdir %s 失败（跳过该文档）: %v", filepath.Dir(mdAbsPath), err)
			errs = append(errs, fmt.Errorf("mkdir %s: %w", filepath.Dir(mdAbsPath), err))
			continue
		}
		if err := os.WriteFile(mdAbsPath, []byte(mdContent), 0o644); err != nil {
			log.Printf("[writeback] 写文档 %s 失败（跳过）: %v", mdAbsPath, err)
			errs = append(errs, fmt.Errorf("write doc %s: %w", g.FilePath, err))
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

		// 写该文档的 sidecar：含该文档全部 node（镜像块也算，供 diff 定位）。
		sf := buildSidecar(g.FilePath, mdContent, g.Nodes, generatedAt)
		if err := writeSidecar(mdAbsPath, sf); err != nil {
			log.Printf("[writeback] 写 sidecar %s 失败（跳过）: %v", mdAbsPath, err)
			errs = append(errs, fmt.Errorf("write sidecar %s: %w", g.FilePath, err))
			continue
		}
		report.SidecarsWritten++
	}

	// 4. 逐 shared node：写 _shared/<domain-slug>/<uuid>.md primary + sidecar。
	for _, v := range primaries {
		primaryAbs := primaryAbsPath(repoRoot, v)
		// primary 渲染为完整可编辑块（与非 shared 同结构）。
		primaryContent := docHeaderComment + "\n\n" + renderFullBlock(v) + "\n"
		if err := os.MkdirAll(filepath.Dir(primaryAbs), 0o755); err != nil {
			log.Printf("[writeback] mkdir _shared %s 失败（跳过该 primary）: %v", filepath.Dir(primaryAbs), err)
			errs = append(errs, fmt.Errorf("mkdir primary dir %s: %w", filepath.Dir(primaryAbs), err))
			continue
		}
		if err := os.WriteFile(primaryAbs, []byte(primaryContent), 0o644); err != nil {
			log.Printf("[writeback] 写 primary %s 失败（跳过）: %v", primaryAbs, err)
			errs = append(errs, fmt.Errorf("write primary %s: %w", primaryFilePath(v), err))
			continue
		}
		report.PrimaryFiles++
		report.SharedNodes++

		// primary 的 sidecar：primary 文档只含这一个 node（完整块）。
		primaryRel := primaryFilePath(v) // 相对 repoRoot
		sf := buildSidecar(primaryRel, primaryContent, []nodeView{v}, generatedAt)
		if err := writeSidecar(primaryAbs, sf); err != nil {
			log.Printf("[writeback] 写 primary sidecar %s 失败（跳过）: %v", primaryAbs, err)
			errs = append(errs, fmt.Errorf("write primary sidecar %s: %w", primaryRel, err))
			continue
		}
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
