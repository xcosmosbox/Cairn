// pr_summary.go — 可选的 LLM PR 摘要生成器（产品化增强）。
//
// 使用 LLM 根据 build 结果 + git diff 生成自然语言的 PR 描述。
// 通过 config `rewrite.pr_summary_llm: true` 启用。
// 未启用时回退到纯结构化 PR body（不含 LLM 摘要）。
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

// PRSummaryInput 是生成 PR 摘要的输入。
type PRSummaryInput struct {
	KGGroup      string `json:"kg_group"`
	SourceRepo   string `json:"source_repo"`
	SourceCommit string `json:"source_commit"`
	// NodesTotal / EdgesTotal 是 candidate KG 的**全图规模**，不是本次新增量。
	//
	// 这两个字段曾命名为 nodes_created/edges_created 并在 prompt 中写作「新增节点」，
	// 而调用方传入的一直是 countKG 的全图总数。结果增量 run 的 PR 标题出现「新增 54
	// 节点及 45 条边」这类描述，实际只新增了 1 个节点——PR 描述与 diff 严重不符，
	// reviewer 因此失去判断依据。本次仅使命名与语义一致；本次真实改动量以 DiffStat
	// （git diff --stat）为准。
	NodesTotal    int      `json:"nodes_total"`
	EdgesTotal    int      `json:"edges_total"`
	DocsRewritten int      `json:"docs_rewritten"`
	DiffStat      string   `json:"diff_stat"`
	Warnings      []string `json:"warnings,omitempty"`

	// Catalog PR 特有字段（Source PR 不填）。
	// IsCatalog=true 时使用 catalog 专用 prompt（侧重 bundle 发布与 stable 提升，
	// 而非文件回写审查）。
	IsCatalog            bool   `json:"is_catalog,omitempty"`
	BundleDigest         string `json:"bundle_digest,omitempty"`
	ReleaseTag           string `json:"release_tag,omitempty"`
	PreviousStableDigest string `json:"previous_stable_digest,omitempty"`
}

// PRSummary 是 LLM 生成的 PR 摘要。
type PRSummary struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// PRSummarizer 是可选的 PR 摘要生成接口。
type PRSummarizer interface {
	GeneratePRSummary(ctx context.Context, input PRSummaryInput) (PRSummary, error)
}

// LLMSummarizer 使用 LLM 生成 PR 摘要。
type LLMSummarizer struct {
	client llm.Client
}

// NewLLMSummarizer 创建 LLM PR 摘要器。
func NewLLMSummarizer(client llm.Client) *LLMSummarizer {
	return &LLMSummarizer{client: client}
}

// GeneratePRSummary 调用 LLM 生成自然语言 PR 摘要。
// 根据 IsCatalog 字段选择 Source PR 或 Catalog PR 专用 prompt。
func (s *LLMSummarizer) GeneratePRSummary(ctx context.Context, input PRSummaryInput) (PRSummary, error) {
	if input.IsCatalog {
		return s.generateCatalogSummary(ctx, input)
	}
	return s.generateSourceSummary(ctx, input)
}

// generateSourceSummary 为 Source PR（结构化回写）生成 LLM 摘要。
func (s *LLMSummarizer) generateSourceSummary(ctx context.Context, input PRSummaryInput) (PRSummary, error) {
	log.Printf("[pr-summary] 开始生成 Source PR 摘要: kg=%s 图规模 nodes=%d edges=%d 回写 docs=%d",
		input.KGGroup, input.NodesTotal, input.EdgesTotal, input.DocsRewritten)

	prompt := fmt.Sprintf(`你是一个知识图谱构建管道的 PR 撰写助手。根据以下构建信息，生成一个简洁的 PR 标题和描述。

## 构建信息
- 知识库: %s
- 源仓库: %s
- 源提交: %s
- 构建后知识图谱总规模: %d 个节点 / %d 条边（这是全图累计总量，**不是**本次新增数量）
- 本次回写文档: %d
- 警告: %v

## 文件变更
%s

## 要求
1. 标题：一句话概括本次构建的主要变更（不超过 50 字）
2. 描述：2-3 段自然语言，说明：
   - 构建了什么知识（哪些领域/实体）
   - 文件变更概况
   - 需要审查者关注的点（如有警告）
3. 用中文撰写
4. **严禁**把「知识图谱总规模」当作本次新增量来描述（例如不得写成「新增 N 个节点」）。
   本次实际改动规模只能依据上面的「文件变更」判断；如需提及总规模，请明确表述为
   「构建后图谱共 N 个节点」。增量构建通常只改动少量节点，务必如实反映。
5. 输出 JSON 格式：{"title":"...","description":"..."}

只输出 JSON，不要其他内容。`, input.KGGroup, input.SourceRepo, input.SourceCommit[:12],
		input.NodesTotal, input.EdgesTotal, input.DocsRewritten, input.Warnings, input.DiffStat)

	return s.callLLM(ctx, prompt, "你是知识图谱构建管道的 PR 撰写助手。根据构建信息生成简洁的 PR 描述。只输出 JSON。", input.KGGroup)
}

// generateCatalogSummary 为 Catalog PR（bundle 发布 stable 提升）生成 LLM 摘要。
func (s *LLMSummarizer) generateCatalogSummary(ctx context.Context, input PRSummaryInput) (PRSummary, error) {
	log.Printf("[pr-summary] 开始生成 Catalog PR 摘要: kg=%s bundle=%s release=%s",
		input.KGGroup, input.BundleDigest, input.ReleaseTag)

	prevInfo := "首次发布（无历史 stable）"
	if input.PreviousStableDigest != "" {
		prevInfo = fmt.Sprintf("上一个 stable: %s", input.PreviousStableDigest[:19])
	}

	prompt := fmt.Sprintf(`你是一个知识图谱发布管道的 PR 撰写助手。根据以下发布信息，生成一个简洁的 PR 标题和描述。

## 发布信息
- 知识库: %s
- 源仓库: %s
- 源提交: %s
- Bundle Digest: %s
- Release Tag: %s
- %s
- KG 节点数: %d
- KG 边数: %d

## 要求
1. 标题：一句话概括本次发布（不超过 50 字）
2. 描述：2-3 段自然语言，说明：
   - 本次发布的知识库包含什么内容
   - 相比上一个 stable 的变化（如果是首次发布则说明）
   - 消费者如何使用此 bundle
3. 用中文撰写
4. 输出 JSON 格式：{"title":"...","description":"..."}

只输出 JSON，不要其他内容。`, input.KGGroup, input.SourceRepo, input.SourceCommit[:12],
		input.BundleDigest, input.ReleaseTag, prevInfo,
		input.NodesTotal, input.EdgesTotal)

	return s.callLLM(ctx, prompt, "你是知识图谱发布管道的 PR 撰写助手。根据发布信息生成简洁的 PR 描述。只输出 JSON。", input.KGGroup)
}

// callLLM 是 Source/Catalog 共用的 LLM 调用 + JSON 解析逻辑。
func (s *LLMSummarizer) callLLM(ctx context.Context, prompt, system, kgGroup string) (PRSummary, error) {
	resp, err := s.client.Complete(ctx, llm.CompleteRequest{
		System:    system,
		User:      prompt,
		MaxTokens: 2000,
	})
	if err != nil {
		log.Printf("[pr-summary] LLM 调用失败: %v", err)
		return PRSummary{}, fmt.Errorf("pr_summary: LLM 调用失败: %w", err)
	}
	log.Printf("[pr-summary] LLM 返回: tokens(in=%d out=%d) content_len=%d", resp.InputTokens, resp.OutputTokens, len(resp.Text))

	// 解析 LLM 输出的 JSON。
	content := strings.TrimSpace(resp.Text)
	// 去掉可能的 markdown code fence。
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var summary PRSummary
	if err := json.Unmarshal([]byte(content), &summary); err != nil {
		// LLM 输出不是合法 JSON，回退到纯文本。
		log.Printf("[pr-summary] JSON 解析失败，回退纯文本: %v", err)
		return PRSummary{
			Title:       fmt.Sprintf("chore(kg): %s 知识图谱构建", kgGroup),
			Description: resp.Text,
		}, nil
	}
	log.Printf("[pr-summary] 摘要生成成功: title=%q", summary.Title)
	return summary, nil
}

// FakePRSummarizer 返回固定摘要（测试用）。
type FakePRSummarizer struct{}

func (f *FakePRSummarizer) GeneratePRSummary(ctx context.Context, input PRSummaryInput) (PRSummary, error) {
	if input.IsCatalog {
		return PRSummary{
			Title:       fmt.Sprintf("chore(kb): publish %s stable @ %s", input.KGGroup, input.SourceCommit[:12]),
			Description: fmt.Sprintf("本次发布将 %s 的知识库 Bundle 提升为 stable。包含 %d 个节点、%d 条关系。", input.KGGroup, input.NodesTotal, input.EdgesTotal),
		}, nil
	}
	return PRSummary{
		Title:       fmt.Sprintf("chore(kg): %s 知识图谱构建 @ %s", input.KGGroup, input.SourceCommit[:12]),
		Description: fmt.Sprintf("本次构建回写 %d 篇文档；构建后图谱共 %d 个节点、%d 条关系。", input.DocsRewritten, input.NodesTotal, input.EdgesTotal),
	}, nil
}
