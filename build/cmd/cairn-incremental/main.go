// Command cairn-incremental 是增量流水线的统一入口（第二块地基）。
//
// 它在全量构建（cairn-ingest）+ 结构化回写之后运行：检测人对回写文档的编辑，
// 只对变化的部分做最小化 KG 更新（而非全量重跑），并把结果重新回写：
//
//	人编辑 reference 文档 / _shared primary → cairn-incremental →
//	I-1 变化检测（四分类）→ I-2 分派（旁路零 LLM / 管道）→
//	I-3 标注 → I-4 增量对齐 → I-5 脏集收敛 → I-6 重融合 → I-7 relation 重算 →
//	I-8 局部 upsert → I-9 增量回写 + 累计改动量记录。
//
// 用法：
//
//	cairn-incremental --repo PATH --db PATH [flags]
//
// LLM API Key 从环境变量 CAIRN_LLM_API_KEY 读取（与 cairn-ingest 一致）。
//
// Command cairn-incremental is the entry point of the incremental pipeline: after a
// full build + structured write-back, it detects human edits to the written-back
// docs and applies minimal KG updates (no full rerun), then writes back.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/evolve"
)

func main() {
	repo := flag.String("repo", "", "仓库工作区路径 / workspace repo path")
	dbPath := flag.String("db", "knowledge.db", "既有 SQLite 库路径（局部 upsert，不重建）/ existing .db path")
	repositoryIdentity := flag.String("repository-identity", "", "稳定仓库身份（默认 Git origin / 本地持久标记）/ stable repository identity")
	adoptLegacy := flag.Bool("adopt-legacy", false, "显式导入无身份旧库，要求完整原始 sidecar 来源证明 / explicitly adopt a legacy KG with complete sidecar proof")
	model := flag.String("model", "deepseek-v4-pro", "LLM 模型 / LLM model")
	endpoint := flag.String("endpoint", "https://api.deepseek.com/chat/completions", "LLM endpoint")
	maxTokens := flag.Int("max-tokens", 384000, "单次 LLM 最大输出 token / max output tokens per call")
	timeout := flag.String("timeout", "1800s", "单请求超时 / per-request timeout")
	maxRetries := flag.Int("max-retries", 3, "标注/语义门原地重试上限 / in-place retry cap")
	maxRollbacks := flag.Int("max-rollbacks", 3, "单篇文档回退重标注上限 / per-doc rollback budget")
	minConf := flag.Float64("min-confidence", 0.0, "新建 entity/concept 置信度门控 / confidence gate")
	recallK := flag.Int("recall-k", 15, "FTS5 召回 top-K / FTS5 recall size")
	evolutionDir := flag.String("evolution-dir", "", "演化产物目录（默认 <db 同级>/evolution）/ evolution workspace dir")
	noEvolve := flag.Bool("no-evolve", false, "关闭演化记录旁路（默认开启）/ disable evolution sidecar recording")
	flag.Parse()

	if *repo == "" || *dbPath == "" {
		fmt.Fprintln(os.Stderr, "用法: cairn-incremental --repo PATH --db PATH [flags]")
		flag.PrintDefaults()
		os.Exit(2)
	}

	// 构建 LLM 客户端（与 cairn-ingest 同一套配置；API Key 从 CAIRN_LLM_API_KEY 读取）。
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

	orch, err := incremental.NewIncrementalOrchestrator(incremental.Options{
		RepositoryIdentity: *repositoryIdentity,
		AdoptLegacy:        *adoptLegacy,
		Client:             client,
		MaxTokens:          *maxTokens,
		MaxRetries:         *maxRetries,
		MaxRollbacks:       *maxRollbacks,
		MinConfidence:      *minConf,
		RecallK:            *recallK,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建增量编排器失败: %v\n", err)
		os.Exit(1)
	}

	report, runErr := orch.Run(context.Background(), *repo, *dbPath)

	// 打印运行摘要（部分失败也打印——可观测优先）。
	fmt.Fprintf(os.Stderr, "══════════ 增量运行摘要 / Incremental Run Summary ══════════\n")
	if report != nil {
		fmt.Fprintf(os.Stderr, "[cairn-incremental] 完成 → %s (版本 %s)\n", *dbPath, report.Version)
		fmt.Fprintf(os.Stderr, "  候选文档: %d  实质变更: %d\n", report.DocsScanned, report.DocsChanged)
		fmt.Fprintf(os.Stderr, "  变更分类: C1=%d C2=%d C3=%d C4=%d\n",
			report.C1Edits, report.C2Deletions, report.C3Violations, report.C4Segments)
		fmt.Fprintf(os.Stderr, "  标注单元: %d  融入 node: %d  新建 node: %d  真删 node: %d\n",
			report.UnitsAnnotated, report.NodesMerged, report.NodesCreated, report.NodesDeleted)
		fmt.Fprintf(os.Stderr, "  relation 重算子域: %d  重写文档: %d  primary 写/删: %d/%d\n",
			report.SubdomainsReflow, report.DocsRewritten, report.PrimariesWritten, report.PrimariesDeleted)
		if len(report.UnresolvedDocs) > 0 {
			fmt.Fprintf(os.Stderr, "  ⚠ C4 未解决文档（散文已保留，下轮可重试）: %d\n", len(report.UnresolvedDocs))
			for _, u := range report.UnresolvedDocs {
				log.Printf("[cairn-incremental] ⚠ 未解决 %s: %s", u.Path, u.Reason)
			}
		}
		if len(report.Warnings) > 0 {
			fmt.Fprintf(os.Stderr, "  告警: %d 条\n", len(report.Warnings))
			for _, w := range report.Warnings {
				log.Printf("[cairn-incremental] ⚠ %s", w)
			}
		}
		if len(report.AnnotateSkipped) > 0 {
			for _, s := range report.AnnotateSkipped {
				log.Printf("[cairn-incremental] 标注降级跳过: %s", s)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "══════════════════════════════════════════════════════════\n")

	// E-3 演化记录旁路收尾（R-ev-1：失败仅告警，绝不影响构建退出码与产物；
	// 部分失败的运行其已收敛部分照样记录——changeset 反映真实落库状态）。
	if !*noEvolve {
		evDir := *evolutionDir
		if evDir == "" {
			evDir = evolve.DefaultDir(*dbPath)
		}
		if summary, err := evolve.Record(context.Background(), *dbPath, evDir, "incremental"); err != nil {
			log.Printf("[evolve] 演化记录失败（不影响构建）: %v", err)
		} else {
			fmt.Fprintf(os.Stderr, "\n══ 本次变更 / Changeset ══\n%s\n", summary)
		}
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "增量流水线部分失败（成功的部分已收敛，失败部分下轮可重试）: %v\n", runErr)
		os.Exit(1)
	}
}
