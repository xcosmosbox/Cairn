// Command cairn-ingest 是新 LLM 流水线的统一入口（取代旧的 ci-* 五段命令与 _e2e-domains）。
//
// 它编排完整的全量重建流程：
//
//	discovery 扫描 repo → 每篇 reference 文档 per-doc LLM 标注（仅 entity/concept）→
//	双门校验（Gate A 代码规则 + Gate B LLM 语义幂等，失败回退重标注）→
//	repo 级一次性提取领域层级 → 全量重建入库 SQLite 双层图。
//
// 用法：
//
//	cairn-ingest --repo PATH --db PATH [flags]
//
// LLM API Key 从环境变量 CAIRN_LLM_API_KEY 读取。
//
// Command cairn-ingest is the unified entry point of the new LLM pipeline, replacing the
// legacy ci-* commands and _e2e-domains. It runs the full-rebuild flow: scan → per-doc
// annotate → two-gate validation → repo-wide extraction → full-rebuild ingest.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/pipeline"
	"github.com/xcosmosbox/cairn/core/evolve"
)

func main() {
	repositoryIdentity := flag.String("repository-identity", "", "稳定仓库身份（默认 Git origin / 本地持久标记）/ stable repository identity")
	repo := flag.String("repo", "", "待扫描的仓库工作区路径 / workspace repo path to scan")
	dbPath := flag.String("db", "knowledge.db", "输出 SQLite 库路径 / output .db path")
	model := flag.String("model", "deepseek-v4-pro", "LLM 模型 / LLM model")
	endpoint := flag.String("endpoint", "https://api.deepseek.com/chat/completions", "LLM endpoint")
	maxTokens := flag.Int("max-tokens", 384000, "单次 LLM 最大输出 token / max output tokens per call")
	timeout := flag.String("timeout", "1800s", "单请求超时 / per-request timeout")
	maxRetries := flag.Int("max-retries", 3, "各 LLM 阶段原地重试上限 / in-place retry cap per LLM stage")
	maxRollbacks := flag.Int("max-rollbacks", 3, "单篇文档回退重标注上限 / per-doc rollback budget")
	minConf := flag.Float64("min-confidence", 0.0, "entity/concept 入库置信度门控 / ingest confidence gate")
	dumpDir := flag.String("dump-dir", "", "把每个阶段的中间产物(标注/双门/提取/入库 JSON)写到该目录，便于逐环节观察真实产出 / dump intermediate artifacts for inspection")
	evolutionDir := flag.String("evolution-dir", "", "演化产物目录（默认 <db 同级>/evolution）/ evolution workspace dir")
	noEvolve := flag.Bool("no-evolve", false, "关闭演化记录旁路（默认开启）/ disable evolution sidecar recording")
	flag.Parse()

	if *repo == "" || *dbPath == "" {
		fmt.Fprintln(os.Stderr, "用法: cairn-ingest --repo PATH --db PATH [--dump-dir DIR] [flags]")
		flag.PrintDefaults()
		os.Exit(2)
	}

	// 构建 LLM 客户端（OpenAI 兼容端点，API Key 从 CAIRN_LLM_API_KEY 读取）。
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

	orch, err := pipeline.NewOrchestrator(pipeline.Options{
		RepositoryIdentity: *repositoryIdentity,
		Client:             client,
		MaxTokens:          *maxTokens,
		MaxRetries:         *maxRetries,
		MaxRollbacks:       *maxRollbacks,
		MinConfidence:      *minConf,
		DumpDir:            *dumpDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建编排器失败: %v\n", err)
		os.Exit(1)
	}

	report, err := orch.RunFullRebuild(context.Background(), *repo, *dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "流水线执行失败: %v\n", err)
		os.Exit(1)
	}

	// 打印运行摘要。
	fmt.Fprintf(os.Stderr, "══════════ 运行摘要 / Run Summary ══════════\n")
	fmt.Fprintf(os.Stderr, "[cairn-ingest] 完成 → %s\n", *dbPath)
	fmt.Fprintf(os.Stderr, "  skills 扫描: %d\n", report.SkillsScanned)
	fmt.Fprintf(os.Stderr, "  文档总数: %d  通过双门: %d  降级跳过: %d\n",
		report.DocsTotal, report.DocsRewritten, len(report.Skipped))
	if report.Ingest != nil {
		fmt.Fprintf(os.Stderr, "  节点入库: skill=%d domain=%d subdomain=%d entity=%d concept=%d (共 %d)\n",
			report.Ingest.SkillNodes, report.Ingest.DomainNodes, report.Ingest.SubdomainNodes,
			report.Ingest.EntityNodes, report.Ingest.ConceptNodes, report.Ingest.NodesInserted)
		fmt.Fprintf(os.Stderr, "  边入库: provides=%d composes=%d semantic=%d (共 %d, 跳过 %d)\n",
			report.Ingest.ProvidesEdges, report.Ingest.ComposesEdges, report.Ingest.SemanticEdges,
			report.Ingest.EdgesInserted, report.Ingest.EdgesSkipped)
	}
	if *dumpDir != "" {
		fmt.Fprintf(os.Stderr, "  中间产物已 dump 到: %s\n", *dumpDir)
	}
	if report.Repair != nil && report.Repair.DanglingFound > 0 {
		fmt.Fprintf(os.Stderr, "  悬空边修正: 发现 %d, 应用 patch %d, 残留丢弃 %d, 降级=%v\n",
			report.Repair.DanglingFound, report.Repair.PatchesApplied, report.Repair.StillDangling, report.Repair.Degraded)
	}
	for _, sk := range report.Skipped {
		log.Printf("[cairn-ingest] 降级跳过 %s (%s): %s", sk.Path, sk.Skill, sk.Reason)
	}
	fmt.Fprintf(os.Stderr, "══════════════════════════════════════════\n")

	// E-3 演化记录旁路收尾（R-ev-1：失败仅告警，绝不影响构建退出码与产物）。
	if !*noEvolve {
		evDir := *evolutionDir
		if evDir == "" {
			evDir = evolve.DefaultDir(*dbPath)
		}
		if summary, err := evolve.Record(context.Background(), *dbPath, evDir, "ingest"); err != nil {
			log.Printf("[evolve] 演化记录失败（不影响构建）: %v", err)
		} else {
			fmt.Fprintf(os.Stderr, "\n══ 本次变更 / Changeset ══\n%s\n", summary)
		}
	}
}
