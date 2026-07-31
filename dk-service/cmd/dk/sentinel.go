// Package main 是领域知识层（Domain Knowledge Layer）命令行工具 dk 的入口。
//
// 本文件实现 dk sentinel 子命令：查询端的复杂度哨兵采样（W-B）。
// 纯 CPU、零 LLM、只读 KG；--record 时把快照追加进 sentinel.history 时序，
// 供 graph-viewer 趋势图与运营观察图谱质量走向、决策何时 rebalance。
//
// This file implements the dk sentinel subcommand (W-B): on-demand LLM-free
// sentinel sampling from the query side, with optional --record persistence.
package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/xcosmosbox/domain-knowledge-layer/dk-service/internal/service"
)

// runSentinel 执行 dk sentinel 子命令：打印当前三指标 + 累计改动占比 + 是否越阈值。
// --record 时把本次快照写入 sentinel.history（默认只读不写）。
//
// runSentinel executes the dk sentinel subcommand: prints the three signals,
// the cumulative change ratio, and threshold breach status.
func runSentinel(svc service.KnowledgeService, args []string) error {
	fs := flag.NewFlagSet("sentinel", flag.ExitOnError)
	record := fs.Bool("record", false, "把本次快照写入 sentinel.history 时序 / append snapshot to sentinel.history")
	_ = fs.Parse(args)

	s, err := svc.Sentinel(context.Background(), *record)
	if err != nil {
		return fmt.Errorf("哨兵采样失败 / sentinel sampling failed: %w", err)
	}

	fmt.Printf("复杂度哨兵 / Complexity Sentinel（%s）\n", s.Timestamp.Format("2006-01-02 15:04:05 UTC"))
	fmt.Printf("  现状模块度 Q = %.3f（下限 0.300）\n", s.ModularityQ)
	fmt.Printf("  单例子域占比 = %.3f（上限 0.400）\n", s.SingletonRatio)
	fmt.Printf("  边/节点比    = %.3f（上限 3.000）\n", s.EdgeNodeRatio)
	fmt.Printf("  累计改动占比 = %.3f（上限 0.200）\n", s.CumulativeRatio)

	if len(s.BreachReasons) == 0 {
		fmt.Println("✓ 未越阈值，暂无需重整 / within thresholds, no rebalance needed")
	} else {
		fmt.Printf("⚠ 越阈值 %d 项，建议执行 dk-rebalance / %d breach(es), consider dk-rebalance:\n", len(s.BreachReasons), len(s.BreachReasons))
		for _, r := range s.BreachReasons {
			fmt.Printf("  - %s\n", r)
		}
	}
	if *record {
		fmt.Println("已写入 sentinel.history 时序 / snapshot recorded to sentinel.history")
	}
	return nil
}
