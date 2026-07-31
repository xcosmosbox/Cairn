// Package main 的本文件实现 cairn why —— provenance 追溯（E-6）：
// 正查 node_sources（来源 skill/file/span）+ uuid_lineage 血缘史（来源反查 +
// ResolveSuccessors 去向）+ W-A GetNode 存活判定（已合并/拆分自动解析到最新存活）。
package main

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/xcosmosbox/cairn/core/storage"
	"github.com/xcosmosbox/cairn/service/internal/service"
)

// runWhy 追溯一个节点的来源与血缘史。
//
//	cairn why <uuid>
//	cairn why --name <entity-name>
func runWhy(svc service.KnowledgeService, db *storage.DB, args []string) error {
	fs := flag.NewFlagSet("why", flag.ExitOnError)
	name := fs.String("name", "", "按名称解析节点 uuid / resolve uuid by entity name")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析参数失败 / failed to parse arguments: %w", err)
	}
	ctx := context.Background()

	// ── 定位 uuid：位置参数直给，或 --name 经 Search 解析 ──
	id := fs.Arg(0)
	if id == "" && *name != "" {
		res, err := svc.Search(ctx, *name, service.SearchOptions{Limit: 10})
		if err != nil {
			return fmt.Errorf("按名称搜索失败 / search by name failed: %w", err)
		}
		for _, h := range res.Hits {
			if h.Node != nil && h.Node.Name == *name {
				id = h.Node.ID
				break
			}
		}
		if id == "" && len(res.Hits) > 0 && res.Hits[0].Node != nil {
			id = res.Hits[0].Node.ID
			fmt.Printf("（未找到精确同名节点，采用最相近命中: %s (%s)）\n\n", res.Hits[0].Node.Name, id)
		}
	}
	if id == "" {
		return fmt.Errorf("用法: cairn why <uuid> 或 cairn why --name <entity-name>")
	}

	// ── 存活判定（W-A：血缘兜底解析）──
	detail, err := svc.GetNode(ctx, id)
	if err != nil {
		return fmt.Errorf("查询节点失败 / get node failed: %w", err)
	}

	srcRepo := storage.NewNodeSourceRepo(db)
	linRepo := storage.NewUUIDLineageRepo(db)

	fmt.Printf("━━━ 追溯 / Why: %s ━━━\n", id)

	effID := id // 生效 uuid（被重定向时为存活后继）
	switch {
	case detail == nil || detail.Node == nil:
		fmt.Printf("\n  状态: 🔴 已消亡（当前库无此节点，血缘无存活后继）\n")
	default:
		n := detail.Node
		effID = n.ID
		fmt.Printf("\n  状态: 🟢 存活\n")
		fmt.Printf("  节点: %s [%s]  %s/%s  置信度 %.2f  来源 %s\n",
			n.Name, n.Label, n.Domain, n.Subdomain, n.Confidence, n.Provenance)
		fmt.Printf("  摘要: %s\n", n.Summary)
		if detail.ResolvedFrom != "" {
			fmt.Printf("  ↪ 血缘重定向: %s 已在重整中被合并/拆分 → 解析到最新存活节点\n", detail.ResolvedFrom)
			if len(detail.AlsoSplitInto) > 0 {
				fmt.Printf("  ↪ 同批拆分产物: %s\n", strings.Join(detail.AlsoSplitInto, ", "))
			}
		}
	}

	// ── 来源正查（node_sources：skill / file / span）──
	if err := printSources(ctx, srcRepo, effID); err != nil {
		return err
	}
	if effID != id {
		// 消亡前的旧 uuid 也可能留有历史来源行。
		if err := printSources(ctx, srcRepo, id); err != nil {
			return err
		}
	}

	// ── 血缘史：来源反查（谁并成了我）+ 去向（我变成了谁）──
	all, err := linRepo.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("读取血缘失败 / read lineage failed: %w", err)
	}
	var preds, succRows []storage.UUIDLineage
	for _, l := range all {
		if l.NewUUID == id {
			preds = append(preds, l)
		}
		if l.OldUUID == id {
			succRows = append(succRows, l)
		}
	}
	sort.Slice(preds, func(i, j int) bool { return preds[i].OldUUID < preds[j].OldUUID })
	sort.Slice(succRows, func(i, j int) bool { return succRows[i].NewUUID < succRows[j].NewUUID })

	fmt.Printf("\n  血缘史 / Lineage:\n")
	if len(preds) == 0 && len(succRows) == 0 {
		fmt.Printf("    （无血缘记录：未经历过合并/拆分）\n")
	}
	for _, l := range preds {
		fmt.Printf("    ← 由 %s %s 而来（%s）\n", l.OldUUID, reasonZH(l.Reason), l.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	for _, l := range succRows {
		fmt.Printf("    → %s 为 %s（%s）\n", reasonZH(l.Reason), l.NewUUID, l.CreatedAt.Format("2006-01-02 15:04:05"))
	}

	succ, err := linRepo.ResolveSuccessors(ctx, id)
	if err != nil {
		return fmt.Errorf("解析血缘后继失败 / resolve successors failed: %w", err)
	}
	var terminals []string
	for _, u := range succ {
		if u != id {
			terminals = append(terminals, u)
		}
	}
	if len(terminals) > 0 {
		fmt.Printf("    终端存活后继: %s\n", strings.Join(terminals, ", "))
	}
	return nil
}

// printSources 打印一个 uuid 的全部来源行（skill/file/span，确定性排序）。
func printSources(ctx context.Context, repo *storage.NodeSourceRepo, uuid string) error {
	sources, err := repo.ListByNode(ctx, uuid)
	if err != nil {
		return fmt.Errorf("读取来源失败 / read sources failed: %w", err)
	}
	if len(sources) == 0 {
		return nil
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Skill != sources[j].Skill {
			return sources[i].Skill < sources[j].Skill
		}
		if sources[i].FilePath != sources[j].FilePath {
			return sources[i].FilePath < sources[j].FilePath
		}
		return sources[i].StartLine < sources[j].StartLine
	})
	fmt.Printf("\n  来源 / Sources（%d）:\n", len(sources))
	for _, s := range sources {
		span := ""
		if s.StartLine > 0 {
			span = fmt.Sprintf(":%d-%d", s.StartLine, s.EndLine)
		}
		fmt.Printf("    [%s] %s%s  (member %s)\n", s.Skill, s.FilePath, span, s.MemberID)
	}
	return nil
}

// reasonZH 把血缘 reason 翻成人读动词。
func reasonZH(reason string) string {
	switch reason {
	case "merged":
		return "合并"
	case "split":
		return "拆分"
	default:
		return reason
	}
}
