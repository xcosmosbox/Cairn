// Package main 是领域知识层 CLI 工具 dk 的入口。
//
// 本文件实现 dk status 子命令：展示知识库的统计摘要（节点/边总数、
// 按类型/域的分布、KB 版本）。统计委托给 service.KnowledgeService.Status。
//
// This file implements the dk status subcommand, delegating aggregation to
// service.KnowledgeService.Status.
package main

import (
	"context"
	"flag"
	"fmt"
	"sort"

	"github.com/xcosmosbox/domain-knowledge-layer/dk-service/internal/service"
)

// runStatus 执行 dk status：调用 service.Status → 格式化输出统计摘要。
// runStatus calls service.Status and formats the statistical summary.
func runStatus(svc service.KnowledgeService, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析参数失败 / failed to parse arguments: %w", err)
	}

	res, err := svc.Status(context.Background())
	if err != nil {
		return fmt.Errorf("获取状态失败 / failed to get status: %w", err)
	}

	fmt.Printf("━━━ 知识库状态 / Knowledge Base Status ━━━\n")
	fmt.Printf("  KB 版本:    %s\n", res.KBVersion)
	fmt.Printf("  节点总数:   %d\n", res.TotalNodes)
	fmt.Printf("  边总数:     %d\n", res.TotalEdges)

	fmt.Printf("\n  节点按类型:\n")
	printSortedCounts(res.NodesByLabel)

	fmt.Printf("\n  边按类型:\n")
	printSortedCounts(res.EdgesByKind)

	fmt.Printf("\n  节点按域（Top）:\n")
	printSortedCounts(res.NodesByDomain)

	return nil
}

// printSortedCounts 按计数降序打印 map。
// printSortedCounts prints a count map sorted by count descending.
func printSortedCounts(m map[string]int) {
	type kv struct {
		k string
		v int
	}
	pairs := make([]kv, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	for _, p := range pairs {
		fmt.Printf("    %-24s %d\n", p.k, p.v)
	}
}
