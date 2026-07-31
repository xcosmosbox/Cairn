// Package main 的本文件实现 cairn show —— 单次 changeset 的完整事件流（E-6）：
// 从 evolution.db 读 diff_json，渲染血缘感知的变更事件（合并/拆分/迁移/改名/内容/增删）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strconv"

	"github.com/xcosmosbox/cairn/core/evolve"
	"github.com/xcosmosbox/cairn/core/observe"
)

// runShow 打印某次 changeset 的完整事件流。
//
//	cairn show <seq> [--evolution-dir DIR]
func runShow(dbPath string, args []string) error {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	evolutionDir := fs.String("evolution-dir", "", "演化产物目录（默认 <db 同级>/evolution）/ evolution workspace dir")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析参数失败 / failed to parse arguments: %w", err)
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("用法: cairn show <seq> [--evolution-dir DIR]")
	}
	seq, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("seq 必须是整数: %w", err)
	}

	evDir := *evolutionDir
	if evDir == "" {
		evDir = evolve.DefaultDir(dbPath)
	}
	m, err := evolve.ReadManifest(context.Background(), evDir)
	if err != nil {
		return fmt.Errorf("读取演化记录失败: %w", err)
	}

	var found *evolve.ChangesetRow
	for i := range m.Changesets {
		if m.Changesets[i].Seq == seq {
			found = &m.Changesets[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("changeset #%d 不存在（共 %d 条）", seq, len(m.Changesets))
	}

	fmt.Printf("━━━ Changeset #%d · %s · %s ━━━\n", found.Seq, found.Tool, found.Ts)
	if found.ParentVersion != "" {
		fmt.Printf("版本: %s → %s\n", found.ParentVersion, found.KBVersion)
	} else {
		fmt.Printf("版本: %s（首次记录）\n", found.KBVersion)
	}
	fmt.Printf("快照: snapshots/%s\n", found.SnapshotFile)
	if found.TriggerReason != "" {
		fmt.Printf("触发: %s\n", found.TriggerReason)
	}
	if len(found.DocsAffected) > 0 {
		fmt.Printf("受影响文档（%d）:\n", len(found.DocsAffected))
		for _, d := range found.DocsAffected {
			fmt.Printf("    %s\n", d)
		}
	}

	if len(found.Diff) == 0 {
		fmt.Printf("\n（无 diff 数据）\n")
		return nil
	}
	var diff observe.DiffResult
	if err := json.Unmarshal(found.Diff, &diff); err != nil {
		return fmt.Errorf("解析 diff_json 失败: %w", err)
	}
	fmt.Printf("\n%s\n", diff.Markdown())
	return nil
}
