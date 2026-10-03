// file_slug.go — LLM 生成 shared 节点 primary 文件的可读 slug（方案 B）。
//
// 流程：
//  1. 在 AssignNodeUUIDs 之后调用 GenerateFileSlugs。
//  2. 对每个 entity/concept 节点，用 LLM 从 name + domain 生成 kebab-case 英文 slug。
//  3. slug 不可变：首次创建时生成，存入 KG nodes.file_slug，后续构建复用。
//  4. LLM 失败时原地重试 3 次（与 LLMAnnotator 一致），耗尽后回退到确定性 slugify。
//
// 文件名格式：_shared/<domain-slug>/<file-slug>.md
// 例：_shared/order-management/payment-order-flow.md（而非 _shared/order-management/7f3a2b1c....md）
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

// fileSlugMaxConcurrency 限制并发 LLM 调用数，避免触发 API rate limit。
const fileSlugMaxConcurrency = 8

// fileSlugTimeout 是 file_slug 阶段的最长耗时；仍遵守父 context 取消与截止时间。
// 取消或超时后使用确定性 fallback，不继续发起付费调用。
const fileSlugTimeout = 120 * time.Second

// fileSlugMaxRetries 是单个节点 LLM 调用的原地重试上限。
// 与 LLMAnnotator（DefaultAnnotateMaxRetries=3）保持一致：
// 每次重试都是全新 Complete 调用，耗尽后才回退 fallbackSlugify。
const fileSlugMaxRetries = 3

// fileSlugNodeRef 标记一个待生成 slug 的节点及其所属领域。
type fileSlugNodeRef struct {
	domain string
	node   *Node
}

// GenerateFileSlugs 为所有 entity/concept 节点生成 file_slug。
// 已有 slug 的节点（增量场景从 KG 读回）不会被覆盖。
// client 为 nil 时回退到确定性 slugify（不调 LLM）。
//
// 在父 context 内以 120 秒上限和 8 路有界并发逐节点调用 LLM。
func GenerateFileSlugs(ctx context.Context, domains []Domain, client llm.Client) {
	if client == nil {
		// 无 LLM client（如 fake 模式）：用确定性 slugify。
		generateFileSlugsFallback(domains)
		return
	}

	// 收集所有需要生成 slug 的节点（FileSlug 为空的）。
	var pending []fileSlugNodeRef
	seen := make(map[string]bool) // slug 去重

	for di := range domains {
		for si := range domains[di].Subdomains {
			sd := &domains[di].Subdomains[si]
			for ni := range sd.Entities {
				if sd.Entities[ni].FileSlug == "" {
					pending = append(pending, fileSlugNodeRef{domains[di].Name, &sd.Entities[ni]})
				} else {
					seen[sd.Entities[ni].FileSlug] = true
				}
			}
			for ni := range sd.Concepts {
				if sd.Concepts[ni].FileSlug == "" {
					pending = append(pending, fileSlugNodeRef{domains[di].Name, &sd.Concepts[ni]})
				} else {
					seen[sd.Concepts[ni].FileSlug] = true
				}
			}
		}
	}

	if len(pending) == 0 {
		return
	}

	log.Printf("[file-slug] 为 %d 个节点生成 file_slug（LLM 并发 %d 路）", len(pending), fileSlugMaxConcurrency)

	slugCtx, cancel := context.WithTimeout(ctx, fileSlugTimeout)
	defer cancel()

	// 有界并发生成：每个节点独立调用 LLM，失败互不影响。
	slugs := generateSlugsConcurrent(slugCtx, client, pending)

	for i, ref := range pending {
		slug := slugs[i]
		if slug == "" {
			slug = fallbackSlugify(ref.node.Name)
		}
		// 去重：若 slug 已存在，加短后缀。
		base := slug
		counter := 1
		for seen[slug] {
			counter++
			slug = fmt.Sprintf("%s-%d", base, counter)
		}
		seen[slug] = true
		ref.node.FileSlug = slug
	}

	log.Printf("[file-slug] 生成完成: %d 个 slug", len(pending))
}

// generateSlugsConcurrent 有界并发调用 LLM 为每个节点生成 slug。
// 返回与 pending 等长的 slug 切片；LLM 失败的条目返回空串（调用方回退 slugify）。
// 单个调用失败不影响其他调用——这正是并发相对于串行/批量的核心优势。
func generateSlugsConcurrent(ctx context.Context, client llm.Client, pending []fileSlugNodeRef) []string {
	slugs := make([]string, len(pending))
	sem := make(chan struct{}, fileSlugMaxConcurrency)
	var wg sync.WaitGroup

	for i, ref := range pending {
		wg.Add(1)
		go func(idx int, name, domain string) {
			defer wg.Done()
			// 信号量限流：最多 fileSlugMaxConcurrency 个并发。
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			slugs[idx] = generateSingleSlug(ctx, client, name, domain)
		}(i, ref.node.Name, ref.domain)
	}
	wg.Wait()

	// 统计成功数。
	got := 0
	for _, s := range slugs {
		if s != "" {
			got++
		}
	}
	log.Printf("[file-slug] LLM 成功 %d/%d，其余回退 slugify", got, len(pending))

	return slugs
}

// generateSingleSlug 调 LLM 为单个节点生成 slug，带原地重试。
// MaxTokens=0 继承 client 的模型预算；thinking 输出不能假定只占少量 token。
// 不把截断内容当 slug，耗尽后使用确定性 fallback。
// 重试耗尽后返回空串，由调用方 fallback；不能将 fallback 计为 LLM 成功。
func generateSingleSlug(ctx context.Context, client llm.Client, nodeName, domain string) string {
	var lastErr string
	for attempt := 1; attempt <= fileSlugMaxRetries; attempt++ {
		if ctx.Err() != nil {
			return ""
		}
		slug, errMsg := trySingleSlug(ctx, client, nodeName, domain)
		if errMsg == "" {
			return slug
		}
		lastErr = errMsg
		log.Printf("[file-slug] %q 第 %d/%d 次失败: %s", nodeName, attempt, fileSlugMaxRetries, lastErr)
	}
	log.Printf("[file-slug] %q 重试 %d 次耗尽，回退 slugify: %s", nodeName, fileSlugMaxRetries, lastErr)
	return ""
}

// trySingleSlug 尝试一次 LLM 调用生成 slug。
// 返回 (slug, "") 成功；返回 ("", errMsg) 失败（调用方决定是否重试）。
func trySingleSlug(ctx context.Context, client llm.Client, nodeName, domain string) (string, string) {
	prompt := fmt.Sprintf(`为知识图谱节点生成一个英文文件名 slug。

节点名称: %s
所属领域: %s

要求:
1. kebab-case（全小写，单词用连字符分隔）
2. 不超过 40 个字符
3. 只含 a-z, 0-9, -
4. 语义化：反映节点名称的核心含义
5. 中文请翻译成英文再 slugify

输出 JSON 格式：{"slug": "your-slug-here"}`, nodeName, domain)

	resp, err := client.Complete(ctx, llm.CompleteRequest{
		System:    "你是一个文件命名助手。根据节点名称生成简洁的英文 kebab-case slug。输出 JSON。",
		User:      prompt,
		MaxTokens: 0, // Inherit the model's budget, including reasoning tokens.
	})
	if err != nil {
		return "", fmt.Sprintf("LLM 调用失败: %v", err)
	}
	if resp == nil {
		return "", "LLM 返回空响应"
	}
	if resp.FinishReason == "length" || resp.FinishReason == "max_tokens" {
		return "", fmt.Sprintf("LLM 输出未完成: finish_reason=%q", resp.FinishReason)
	}

	// 尝试解析 JSON，失败则直接 sanitize。
	slug := ""
	if strings.HasPrefix(strings.TrimSpace(resp.Text), "{") {
		var result struct {
			Slug string `json:"slug"`
		}
		if json.Unmarshal([]byte(resp.Text), &result) == nil && result.Slug != "" {
			slug = sanitizeSlug(result.Slug)
		}
	}
	if slug == "" {
		slug = sanitizeSlug(resp.Text)
	}
	if slug == "" {
		return "", fmt.Sprintf("LLM 返回空内容（content_len=%d，可能 thinking 耗尽 token）", len(resp.Text))
	}
	return slug, ""
}

// generateFileSlugsFallback 无 LLM 时用确定性 slugify。
func generateFileSlugsFallback(domains []Domain) {
	for di := range domains {
		for si := range domains[di].Subdomains {
			sd := &domains[di].Subdomains[si]
			for ni := range sd.Entities {
				if sd.Entities[ni].FileSlug == "" {
					sd.Entities[ni].FileSlug = fallbackSlugify(sd.Entities[ni].Name)
				}
			}
			for ni := range sd.Concepts {
				if sd.Concepts[ni].FileSlug == "" {
					sd.Concepts[ni].FileSlug = fallbackSlugify(sd.Concepts[ni].Name)
				}
			}
		}
	}
}

// sanitizeSlug 清理 LLM 输出为合法 slug。
func sanitizeSlug(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	// 去掉非 [a-z0-9-] 字符。
	reg := regexp.MustCompile(`[^a-z0-9-]+`)
	s = reg.ReplaceAllString(s, "-")
	// 去掉开头/结尾的连字符。
	s = strings.Trim(s, "-")
	// 压缩连续连字符。
	reg2 := regexp.MustCompile(`-+`)
	s = reg2.ReplaceAllString(s, "-")
	if len(s) > 40 {
		s = s[:40]
		s = strings.Trim(s, "-")
	}
	return s
}

// fallbackSlugify 确定性 slugify（无 LLM 时用）。
// 中文直接用 ASCII 音译近似（简化版：取 name 的 hash 或直接转 ASCII）。
func fallbackSlugify(name string) string {
	// 尝试直接 sanitize（对英文名有效）。
	slug := sanitizeSlug(name)
	if slug != "" {
		return slug
	}
	// 纯中文名：用 name 的 hash 作 slug（保证确定性 + 唯一）。
	return fmt.Sprintf("node-%x", hashString(name))
}

func hashString(s string) uint32 {
	h := uint32(2166136261)
	for _, c := range s {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}
