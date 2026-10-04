// Command cairn-rebalance 是全量重整流水线的统一入口（第三块地基，数据闭环最后一块）。
//
// 它在全量构建（cairn-ingest）+ 多轮增量（cairn-incremental）之后运行：当人机协同增量
// 导致 KG 结构漂移（碎片化 / 重复 / 归属错乱），用「复杂度哨兵触发 → Louvain 社区
// 检测出先验骨架 → LLM 在骨架上语义微调输出结构 patch → 旧图+patch=新图（局部
// upsert，UUID 最大稳定）」做一次保面积的全局化简：
//
//		cairn-rebalance --repo PATH --db PATH [--check] [--force] [flags]
//
//	  - 默认：哨兵三指标（现状 Q / 单例率 / 边节点比）+ 累计改动占比任一越阈值才重整；
//	  - --check：只算指标+建议，零 LLM、零改图；
//	  - --force：跳过哨兵强制重整。
//
// LLM API Key 从环境变量 CAIRN_LLM_API_KEY 读取（与 cairn-ingest / cairn-incremental 一致）。
//
// Command cairn-rebalance is the entry point of the rebalance pipeline (third block):
// sentinel-triggered, Louvain-prior, LLM-refined global simplification of the KG
// via local upserts (never a rebuild).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/evolve"
	"github.com/xcosmosbox/cairn/core/metrics"
)

func main() {
	repo := flag.String("repo", "", "仓库工作区路径 / workspace repo path")
	dbPath := flag.String("db", "knowledge.db", "既有 SQLite 库路径（局部 upsert，不重建）/ existing .db path")
	repositoryIdentity := flag.String("repository-identity", "", "稳定仓库身份（默认 Git origin / 本地持久标记）/ stable repository identity")
	adoptLegacy := flag.Bool("adopt-legacy", false, "显式导入无身份旧库，要求完整原始 sidecar 来源证明 / explicitly adopt a legacy KG with complete sidecar proof")
	model := flag.String("model", dkconfig.DeepSeekFlashModel, "LLM 模型 / LLM model")
	endpoint := flag.String("endpoint", dkconfig.DeepSeekEndpoint, "LLM endpoint")
	maxTokens := flag.Int("max-tokens", dkconfig.DeepSeekDefaultThinkingTokens, "单次 LLM 最大输出 token（DeepSeek 上限 393216）/ max output tokens per call")
	timeout := flag.String("timeout", "1800s", "单请求超时 / per-request timeout")
	maxRetries := flag.Int("max-retries", 3, "LLM 原地重试上限 / in-place retry cap")
	check := flag.Bool("check", false, "只算指标+建议（零 LLM 零改图）/ check only")
	force := flag.Bool("force", false, "跳过哨兵强制重整 / force rebalance")
	qFloor := flag.Float64("q-floor", 0, "现状模块度下限（默认 0.3）/ modularity floor")
	sCeil := flag.Float64("s-ceil", 0, "单例子域占比上限（默认 0.4）/ singleton-ratio ceiling")
	eCeil := flag.Float64("e-ceil", 0, "语义边/节点比上限（默认 3.0）/ edge-node-ratio ceiling")
	cCeil := flag.Float64("cumulative-ceil", 0, "累计改动占比上限（默认 0.20）/ cumulative-change ceiling")
	evolutionDir := flag.String("evolution-dir", "", "演化产物目录（默认 <db 同级>/evolution）/ evolution workspace dir")
	noEvolve := flag.Bool("no-evolve", false, "关闭演化记录旁路（默认开启）/ disable evolution sidecar recording")
	flag.Parse()

	if *repo == "" || *dbPath == "" {
		fmt.Fprintln(os.Stderr, "用法: cairn-rebalance --repo PATH --db PATH [--check] [--force] [flags]")
		flag.PrintDefaults()
		os.Exit(2)
	}

	// 构建 LLM 客户端（与 cairn-incremental 同一套配置；API Key 从 CAIRN_LLM_API_KEY 读取）。
	client, err := llm.NewOpenAICompatClient(llm.Config{
		Provider:   "openai_compatible",
		APIKeyEnv:  "CAIRN_LLM_API_KEY",
		Model:      *model,
		Endpoint:   *endpoint,
		MaxTokens:  *maxTokens,
		Timeout:    *timeout,
		MaxRetries: *maxRetries,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建 LLM 客户端失败: %v\n", err)
		os.Exit(1)
	}

	orch, err := incremental.NewRebalanceOrchestrator(client, *maxTokens)
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建重整编排器失败: %v\n", err)
		os.Exit(1)
	}

	report, runErr := orch.Run(context.Background(), *repo, *dbPath, incremental.RebalanceRunOpts{
		RepositoryIdentity: *repositoryIdentity,
		AdoptLegacy:        *adoptLegacy,
		Force:              *force,
		CheckOnly:          *check,
		Thresholds: metrics.Thresholds{
			QFloor: *qFloor, SCeil: *sCeil, ECeil: *eCeil, CumulativeCeil: *cCeil,
		},
	})

	// 打印重整摘要（部分失败也打印——可观测优先）。
	fmt.Fprintf(os.Stderr, "══════════ 重整运行摘要 / Rebalance Run Summary ══════════\n")
	if report != nil {
		m := report.Metrics
		fmt.Fprintf(os.Stderr, "[cairn-rebalance] 哨兵指标: 现状Q=%.3f 单例率=%.3f 边节点比=%.3f 累计改动=%.3f\n",
			m.ModularityQ, m.SingletonRatio, m.EdgeNodeRatio, report.CumulativeRatio)
		if report.CheckOnly {
			fmt.Fprintf(os.Stderr, "  模式: --check（只读，零 LLM 零改图）\n")
		}
		if report.Triggered {
			fmt.Fprintf(os.Stderr, "  触发: 是（%d 条原因）\n", len(report.TriggerReasons))
			for _, r := range report.TriggerReasons {
				fmt.Fprintf(os.Stderr, "    - %s\n", r)
			}
		} else {
			fmt.Fprintf(os.Stderr, "  触发: 否（哨兵未越阈值）\n")
		}
		if report.NoOp {
			fmt.Fprintf(os.Stderr, "  结果: no-op（%s）\n", report.NoOpReason)
		} else {
			fmt.Fprintf(os.Stderr, "  Louvain: 非单例社区 %d 个，划分 Q=%.3f\n", report.Communities, report.LouvainQ)
			fmt.Fprintf(os.Stderr, "  patch 操作: %d  %v\n", report.PatchOps, report.OpsByKind)
			fmt.Fprintf(os.Stderr, "  明细: merge消失 %d / split新增 %d / remigrate %d / 删边 %d / 重命名 %d\n",
				report.NodesMerged, report.NodesSplitNew, report.NodesRemigrated, report.EdgesDropped, report.Renames)
			fmt.Fprintf(os.Stderr, "  脏子域: %d  受影响文档: %d  重写文档: %d  lineage: %d\n",
				report.SubdomainsDirty, report.DocsAffected, report.DocsRewritten, report.LineageWritten)
			fmt.Fprintf(os.Stderr, "  版本: %s\n", report.Version)
		}
		if len(report.Warnings) > 0 {
			fmt.Fprintf(os.Stderr, "  告警: %d 条\n", len(report.Warnings))
			for _, w := range report.Warnings {
				log.Printf("[cairn-rebalance] ⚠ %s", w)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "══════════════════════════════════════════════════════════\n")

	// E-3 演化记录旁路收尾（R-ev-1：失败仅告警，绝不影响构建退出码与产物）。
	// --check 是只读观测模式（零改图、不 Bump 版本），不是构建事件 → 不产生 changeset。
	switch {
	case *check:
		fmt.Fprintf(os.Stderr, "[evolve] --check 只读模式，不记录演化\n")
	case !*noEvolve:
		evDir := *evolutionDir
		if evDir == "" {
			evDir = evolve.DefaultDir(*dbPath)
		}
		if summary, err := evolve.Record(context.Background(), *dbPath, evDir, "rebalance"); err != nil {
			log.Printf("[evolve] 演化记录失败（不影响构建）: %v", err)
		} else {
			fmt.Fprintf(os.Stderr, "\n══ 本次变更 / Changeset ══\n%s\n", summary)
		}
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "重整流水线部分失败（成功的部分已收敛，失败部分下轮可重试）: %v\n", runErr)
		os.Exit(1)
	}
}
