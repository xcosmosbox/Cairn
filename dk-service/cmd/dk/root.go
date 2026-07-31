// Package main 是领域知识层（Domain Knowledge Layer）命令行工具 dk 的入口。
//
// 本文件定义命令路由和帮助系统。使用 Go 标准库 flag 包实现子命令解析，
// 无需外部 CLI 框架依赖。全局 --db-path 标志控制 SQLite 数据库文件位置。
//
// Package main is the entry point for the Domain Knowledge Layer CLI tool dk.
//
// This file defines command routing and the help system. Uses Go's standard
// library flag package for subcommand parsing, with no external CLI framework
// dependency. The global --db-path flag controls the SQLite database file location.
package main

import (
	"fmt"
	"os"

	_ "modernc.org/sqlite" // SQLite driver (纯 Go 实现 / pure Go implementation)

	"github.com/xcosmosbox/domain-knowledge-layer/core/storage"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-service/internal/service"
)

// defaultDBPath 是未通过 --db-path 指定时的默认 SQLite 数据库文件路径。
// defaultDBPath is the default SQLite database file path when --db-path is not specified.
const defaultDBPath = "./domain-knowledge.db"

// Run 解析命令行参数，打开数据库连接，并分发到对应的子命令处理器。
// 返回值：nil 表示成功，非 nil 表示执行过程中发生错误。
//
// Run parses command-line arguments, opens the database connection,
// and dispatches to the appropriate subcommand handler.
// Returns nil on success, non-nil if an error occurred during execution.
func Run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	cmd := args[0]
	cmdArgs := args[1:]

	// 全局标志：从 cmdArgs 中提取 --db-path / Global flag: extract --db-path from cmdArgs
	dbPath := extractDBPath(&cmdArgs)

	// 处理帮助请求 / Handle help requests
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		if len(cmdArgs) > 0 {
			printSubcommandHelp(cmdArgs[0])
		} else {
			printUsage()
		}
		return nil
	}

	// 打开数据库连接 / Open database connection
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return fmt.Errorf("数据库文件不存在 / database file not found: %s (使用 --db-path 指定路径)", dbPath)
	}

	// 通过 storage.DB 打开知识库，并组装 KnowledgeService 元能力抽象。
	// CLI 只依赖 service.KnowledgeService，不直接写任何 SQL。
	// Open via storage.DB and wire the KnowledgeService meta-capability abstraction.
	// The CLI depends only on service.KnowledgeService and never writes SQL.
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		return fmt.Errorf("打开数据库失败 / failed to open database: %w", err)
	}
	defer db.Close()

	svc := service.NewKnowledgeService(db, nil, nil)

	// 分发子命令 / Dispatch subcommand
	switch cmd {
	case "find":
		return runFind(svc, cmdArgs)
	case "impact":
		return runImpact(svc, cmdArgs)
	case "status":
		return runStatus(svc, cmdArgs)
	case "sentinel":
		return runSentinel(svc, cmdArgs)
	case "why":
		return runWhy(svc, db, cmdArgs)
	case "timeline":
		return runTimeline(dbPath, cmdArgs)
	case "show":
		return runShow(dbPath, cmdArgs)
	default:
		fmt.Fprintf(os.Stderr, "未知命令 / Unknown command: %s\n\n", cmd)
		printUsage()
		return fmt.Errorf("未知命令: %s", cmd)
	}
}

// extractDBPath 从参数列表中提取 --db-path 标志的值。
// 支持 --db-path=<path> 和 --db-path <path> 两种格式。
// 提取后会从 args 切片中移除该标志及其值（修改原始切片指针）。
//
// extractDBPath extracts the --db-path flag value from the argument list.
// Supports both --db-path=<path> and --db-path <path> formats.
// The flag and its value are removed from the args slice (modifies the original pointer).
func extractDBPath(args *[]string) string {
	dbPath := defaultDBPath
	a := *args
	for i := 0; i < len(a); i++ {
		if a[i] == "--db-path" {
			if i+1 < len(a) {
				dbPath = a[i+1]
				// 移除标志和值 / Remove flag and value
				*args = append(a[:i], a[i+2:]...)
				return dbPath
			}
		} else if len(a[i]) > 10 && a[i][:10] == "--db-path=" {
			dbPath = a[i][10:]
			// 移除标志 / Remove flag
			*args = append(a[:i], a[i+1:]...)
			return dbPath
		}
	}
	return dbPath
}

// printUsage 打印 dk CLI 的总体使用说明。
// printUsage prints the overall usage instructions for the dk CLI.
func printUsage() {
	fmt.Println(`dk — 领域知识层命令行工具 / Domain Knowledge Layer CLI

用法 / Usage:
  dk find <entity-name>   按名称搜索实体 / Search entities by name
  dk impact <entity-name> 正向 BFS 影响分析 / Forward BFS impact analysis
  dk status               知识库统计摘要 / Knowledge base status summary
  dk sentinel [--record]  复杂度哨兵指标（Q/单例率/边节点比/累计改动）/ Complexity sentinel signals
  dk why <uuid|--name>    追溯节点来源与血缘史 / Provenance & lineage tracing
  dk timeline             演化时间线（changeset 历史）/ Evolution changeset timeline
  dk show <seq>           某次 changeset 的完整事件流 / Full event stream of one changeset
  dk help [command]       显示帮助信息 / Show help information

全局标志 / Global Flags:
  --db-path <path>  SQLite 数据库文件路径（默认 ./domain-knowledge.db）
                    SQLite database file path (default ./domain-knowledge.db)

示例 / Examples:
  dk find "订单退款"                    # 搜索"订单退款"相关实体 / Search for "订单退款" related entities
  dk impact "OrderService" --depth 3   # 分析 OrderService 的影响范围，深度 3 / Impact analysis with depth 3
  dk --db-path /data/kg.db find "押金" # 指定数据库路径搜索 / Search with custom DB path`)
}

// printSubcommandHelp 打印指定子命令的详细帮助信息。
// printSubcommandHelp prints detailed help information for the specified subcommand.
func printSubcommandHelp(cmd string) {
	switch cmd {
	case "find":
		fmt.Println(`dk find <entity-name> — 按名称搜索实体 / Search entities by name

用法 / Usage:
  dk find <entity-name> [flags]

标志 / Flags:
  --limit <n>    最大返回结果数，默认 10 / Max results to return, default 10
  --domain <name> 按业务域过滤 / Filter by business domain

说明 / Description:
  使用 FTS5 全文索引在 name、summary、synonyms、tags、description、
  domain、subdomain 列中进行模糊匹配。结果按 BM25 相关性排序，
  并展示每个匹配节点的入边关系。

  Uses FTS5 full-text index for fuzzy matching across name, summary,
  synonyms, tags, description, domain, and subdomain columns. Results
  are ranked by BM25 relevance, with incoming edge relationships displayed.`)
	case "impact":
		fmt.Println(`dk impact <entity-name> — 正向 BFS 影响分析 / Forward BFS impact analysis

用法 / Usage:
  dk impact <entity-name> [flags]

标志 / Flags:
  --depth <n>    BFS 遍历最大深度，默认 2，最大 5 / Max BFS traversal depth, default 2, max 5
  --domain <name> 按业务域过滤起始节点 / Filter starting nodes by business domain

说明 / Description:
  从指定实体出发执行正向 BFS（广度优先）图遍历，展示所有从该实体
  可达的节点及它们之间的边关系。适用于变更影响评估：
  了解修改某个实体时可能波及的范围。

  Performs forward BFS graph traversal from the specified entity,
  displaying all reachable nodes and edges. Useful for change impact
  assessment: understand the potential blast radius when modifying an entity.`)
	case "sentinel":
		fmt.Println(`dk sentinel [--record] — 复杂度哨兵指标采样 / Complexity sentinel sampling

用法 / Usage:
  dk sentinel [--record]

标志 / Flags:
  --record       把本次快照写入 sentinel.history 时序（默认只读不写）
                 Append this snapshot to the sentinel.history time series

说明 / Description:
  采样图谱质量三指标（现状模块度 Q / 单例子域占比 / 边节点比）与
  累计改动占比，并判定是否越阈值。纯 CPU、零 LLM、只读 KG 结构；
  --record 写入的时序供 graph-viewer 趋势图可视化，辅助决策何时
  执行 dk-rebalance。

  Samples the three graph-quality signals (current modularity Q,
  singleton ratio, edge/node ratio) plus the cumulative change ratio,
  and reports threshold breaches. Pure CPU, LLM-free, read-only on KG
  structure; --record persists a time series visualized by graph-viewer.`)
	case "why":
		fmt.Println(`dk why <uuid|--name> — 追溯节点来源与血缘史 / Provenance & lineage tracing

用法 / Usage:
  dk why <uuid>
  dk why --name <entity-name>

说明 / Description:
  正查 node_sources（来源 skill / file / span 行号区间），并展示
  uuid_lineage 血缘史（谁合并/拆分成了它、它合并/拆分成了谁、
  终端存活后继），附 W-A 存活判定：已消亡 uuid 自动解析到最新存活节点。

  Shows the node's source spans (skill/file/line ranges), its lineage
  history (merged/split from and into), terminal living successors,
  and liveness with automatic redirect for restructured uuids.`)
	case "timeline":
		fmt.Println(`dk timeline — 演化时间线 / Evolution changeset timeline

用法 / Usage:
  dk timeline [--evolution-dir DIR]

说明 / Description:
  读取演化产物目录（默认 <db 同级>/evolution）下的 evolution.db，
  按 seq 打印每次 dk-ingest/incremental/rebalance 运行产生的
  changeset 摘要（版本变迁 + 血缘感知计数）。

  Prints one summary line per recorded changeset from evolution.db.`)
	case "show":
		fmt.Println(`dk show <seq> — 某次 changeset 的完整事件流 / Full event stream of one changeset

用法 / Usage:
  dk show <seq> [--evolution-dir DIR]

说明 / Description:
  渲染该次 changeset 的 diff_json：合并/拆分/迁移/改名/内容变更/
  新增/删除的完整节点事件与边增删（血缘感知，非朴素 diff）。

  Renders the full lineage-aware event stream stored in diff_json.`)
	default:
		fmt.Fprintf(os.Stderr, "未知命令 / Unknown command: %s\n", cmd)
		printUsage()
	}
}
