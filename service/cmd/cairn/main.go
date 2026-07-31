// Package main 是领域知识层（Cairn）命令行工具 dk 的入口。
//
// dk 提供对知识图谱数据库的只读查询能力，支持以下子命令：
//   - cairn find <entity-name>：按名称搜索实体，展示匹配节点及其入边关系
//   - cairn impact <entity-name>：从前向 BFS 遍历实体，展示可达的影响范围
//
// Package main is the entry point for the Cairn CLI tool dk.
//
// dk provides read-only query capabilities over the knowledge graph database
// with the following subcommands:
//   - cairn find <entity-name>: search entities by name, displaying matched nodes
//     and their incoming edge relationships
//   - cairn impact <entity-name>: perform forward BFS traversal from the entity,
//     displaying the reachable impact scope
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := Run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "dk: %v\n", err)
		os.Exit(1)
	}
}
