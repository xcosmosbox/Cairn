// Package main 是领域知识层 CLI 工具 dk 的入口。
//
// 本文件实现 cairn impact 子命令：从指定实体出发做正向 BFS 影响分析。
// 它只负责命令行参数解析与文本展示；FTS5 起点定位与 BFS 遍历全部委托给
// service.KnowledgeService 元能力抽象，CLI 不再直接写任何 SQL。
//
// This file implements the cairn impact subcommand. FTS5 entry lookup and BFS
// traversal are delegated to the KnowledgeService meta-capability abstraction.
package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/xcosmosbox/cairn/service/internal/service"
)

// runImpact 执行 cairn impact：解析参数 → 调用 service.Impact → 按深度分层格式化。
// runImpact parses flags, calls service.Impact, and formats results by depth.
func runImpact(svc service.KnowledgeService, args []string) error {
	fs := flag.NewFlagSet("impact", flag.ExitOnError)
	maxDepth := fs.Int("depth", 2, "BFS 遍历最大深度 / maximum BFS traversal depth")
	domain := fs.String("domain", "", "按业务域过滤起始节点 / filter starting nodes by business domain")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析参数失败 / failed to parse arguments: %w", err)
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("缺少实体名称参数 / missing entity name argument\n用法: cairn impact <entity-name> [--depth <n>] [--domain <name>]")
	}
	entityName := fs.Arg(0)

	var scope []string
	if *domain != "" {
		scope = []string{*domain}
	}

	res, err := svc.Impact(context.Background(), entityName, service.ImpactOptions{
		Scope:    scope,
		MaxDepth: *maxDepth,
	})
	if err != nil {
		return fmt.Errorf("影响分析失败 / impact analysis failed: %w", err)
	}

	// —— 纯展示层格式化 / pure presentation formatting ——
	if len(res.StartNodes) == 0 {
		fmt.Printf("未找到匹配实体: %s\n", entityName)
		fmt.Printf("No matching entities found: %s\n", entityName)
		return nil
	}

	// 起点信息 / start nodes
	for _, sn := range res.StartNodes {
		fmt.Printf("\n━━━ 影响分析起点: %s (%s, %s) ━━━\n", sn.Name, sn.ID, sn.Label)
	}
	if *maxDepth > 1 {
		fmt.Printf("  遍历深度: %d 级\n", *maxDepth)
	}

	// 构建 nodeID → name 映射，供边展示可读名称。
	nameByID := make(map[string]string, len(res.Nodes))
	for _, in := range res.Nodes {
		nameByID[in.Node.ID] = in.Node.Name
	}

	// 按深度分层展示可达节点。
	fmt.Printf("\n  ── 可达节点（按深度）:\n")
	for _, in := range res.Nodes {
		if in.Depth == 0 {
			continue // 起点已单独展示
		}
		indent := strings.Repeat("  ", in.Depth+1)
		fmt.Printf("%s↳ [d%d] %s (%s, %s)\n", indent, in.Depth, in.Node.Name, in.Node.ID, in.Node.Label)
	}

	// 边关系展示。
	if len(res.Edges) > 0 {
		fmt.Printf("\n  ── 关系边:\n")
		for _, e := range res.Edges {
			srcName := nameByID[e.SourceID]
			if srcName == "" {
				srcName = e.SourceID
			}
			tgtName := nameByID[e.TargetID]
			if tgtName == "" {
				tgtName = e.TargetID
			}
			desc := ""
			if strings.TrimSpace(e.Description) != "" {
				desc = " — " + e.Description
			}
			fmt.Printf("    %s --[%s]--> %s%s\n", srcName, e.Kind, tgtName, desc)
		}
	}

	fmt.Printf("\n  ── 统计: %d 个可达节点, %d 条边（耗时 %dms）\n",
		len(res.Nodes), len(res.Edges), res.QueryMs)
	return nil
}
