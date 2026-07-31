// Package main 的本文件实现 cairn timeline —— 演化时间线（E-6）：
// 读演化产物目录下的 evolution.db，按 seq 打印每次 changeset 的一行摘要。
package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/xcosmosbox/cairn/core/evolve"
)

// runTimeline 打印 changeset 时间线。
//
//	cairn timeline [--evolution-dir DIR]
func runTimeline(dbPath string, args []string) error {
	fs := flag.NewFlagSet("timeline", flag.ExitOnError)
	evolutionDir := fs.String("evolution-dir", "", "演化产物目录（默认 <db 同级>/evolution）/ evolution workspace dir")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析参数失败 / failed to parse arguments: %w", err)
	}

	evDir := *evolutionDir
	if evDir == "" {
		evDir = evolve.DefaultDir(dbPath)
	}
	m, err := evolve.ReadManifest(context.Background(), evDir)
	if err != nil {
		return fmt.Errorf("读取演化记录失败（先运行 cairn-ingest / cairn-incremental / cairn-rebalance）: %w", err)
	}
	if len(m.Changesets) == 0 {
		fmt.Println("演化记录为空：尚无 changeset。")
		return nil
	}

	fmt.Printf("━━━ 演化时间线 / Evolution Timeline（%d 条，%s）━━━\n", len(m.Changesets), evDir)
	for _, c := range m.Changesets {
		ver := c.KBVersion
		if c.ParentVersion != "" {
			ver = c.ParentVersion + " → " + c.KBVersion
		}
		fmt.Printf("\n#%d  %s  %s\n", c.Seq, c.Ts, c.Tool)
		fmt.Printf("    版本: %s\n", ver)
		fmt.Printf("    节点: +%d -%d 合并 %d 拆分 %d 迁移 %d 改名 %d 内容 %d | 边: +%d -%d\n",
			c.NAdded, c.NDeleted, c.NMerged, c.NSplit, c.NMigrated, c.NRenamed, c.NContent,
			c.NEdgeAdded, c.NEdgeDropped)
		if c.TriggerReason != "" {
			fmt.Printf("    触发: %s\n", c.TriggerReason)
		}
		if len(c.DocsAffected) > 0 {
			fmt.Printf("    受影响文档: %d\n", len(c.DocsAffected))
		}
	}
	fmt.Printf("\n（cairn show <seq> 查看某次的完整事件流）\n")
	return nil
}
