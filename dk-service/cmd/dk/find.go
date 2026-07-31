// Package main 是领域知识层 CLI 工具 dk 的入口。
//
// 本文件实现 dk find 子命令：按名称搜索知识图谱中的实体。
// 它只负责「命令行参数解析」与「文本展示格式化」这两项前端职责，
// 检索本身（查询改写 + FTS5 + BFS + BM25/图深度混合排序）全部委托给
// service.KnowledgeService 元能力抽象——CLI 不再直接写任何 SQL。
//
// This file implements the dk find subcommand. It only handles CLI flag parsing
// and text formatting; all retrieval is delegated to the KnowledgeService
// meta-capability abstraction, so the CLI writes no SQL.
package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/xcosmosbox/domain-knowledge-layer/dk-service/internal/service"
)

// runFind 执行 dk find：解析参数 → 调用 service.Search → 格式化输出。
// runFind parses flags, calls service.Search, and formats the output.
func runFind(svc service.KnowledgeService, args []string) error {
	fs := flag.NewFlagSet("find", flag.ExitOnError)
	limit := fs.Int("limit", 10, "最大返回结果数 / max results to return")
	domain := fs.String("domain", "", "按业务域过滤 / filter by business domain")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析参数失败 / failed to parse arguments: %w", err)
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("缺少实体名称参数 / missing entity name argument\n用法: dk find <entity-name> [--limit <n>] [--domain <name>]")
	}
	entityName := fs.Arg(0)

	var scope []string
	if *domain != "" {
		scope = []string{*domain}
	}

	// 委托元能力：检索由 service 层完成，CLI 不碰 SQL。
	// Delegate to the meta-capability; the CLI does not touch SQL.
	res, err := svc.Search(context.Background(), entityName, service.SearchOptions{
		Scope: scope,
		Limit: *limit,
	})
	if err != nil {
		return fmt.Errorf("搜索失败 / search failed: %w", err)
	}

	// —— 以下纯展示层格式化 / pure presentation formatting below ——
	if len(res.Hits) == 0 {
		fmt.Printf("未找到匹配实体: %s\n", entityName)
		fmt.Printf("No matching entities found: %s\n", entityName)
		return nil
	}

	for i, hit := range res.Hits {
		n := hit.Node
		fmt.Printf("\n━━━ 匹配 #%d (综合 %.3f | BM25 %.2f 图 %.2f) ━━━\n",
			i+1, hit.FinalScore, hit.BM25Score, hit.GraphScore)
		fmt.Printf("  ID:         %s\n", n.ID)
		fmt.Printf("  名称:       %s\n", n.Name)
		fmt.Printf("  类型:       %s\n", n.Label)
		fmt.Printf("  摘要:       %s\n", n.Summary)
		fmt.Printf("  域:         %s / %s\n", n.Domain, n.Subdomain)
		fmt.Printf("  置信度:     %.2f\n", n.Confidence)
		fmt.Printf("  来源:       %s\n", n.Provenance)

		if len(hit.Incoming) > 0 {
			fmt.Printf("  ── 入边（被引用关系）:\n")
			for _, e := range hit.Incoming {
				desc := ""
				if strings.TrimSpace(e.Description) != "" {
					desc = " — " + e.Description
				}
				fmt.Printf("    ← %s [%s]%s\n", e.SourceID, e.Kind, desc)
			}
		} else {
			fmt.Printf("  (无入边)\n")
		}
	}

	fmt.Printf("\n共找到 %d 个匹配（查询改写: %q，耗时 %dms）\n",
		len(res.Hits), res.RewrittenQuery, res.QueryMs)
	return nil
}
