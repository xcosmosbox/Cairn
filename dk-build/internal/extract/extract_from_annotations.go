// Package extract 实现 LLM 驱动的领域知识提取器。
//
// 本文件实现新 LLM 流水线的「提取阶段」入口 ExtractFromAnnotations：它接收 repo 级
// 全部「已标注」的 entity/concept 单元（来自标注阶段 + 双门校验后的 AnnotatedDocument），
// 一次性带标签投喂给掌握全局视野的 LLM，要求其归纳出「可跨 skill 复用的领域层级」
// （domain → subdomain → entity/concept + entity↔entity 语义关系），返回严格 JSON。
//
// 与旧 Extract（输入原始 Markdown、按 skill 覆盖重试）的关键差异：
//   - 输入是「已分类」的 entity/concept 单元（tag + name + content），而非原始全文；
//     领域层级由本阶段 LLM 从全局视野归纳，标注阶段不涉及 domain/subdomain。
//   - 全量一次性投喂（1M 上下文足够），不做按 skill 覆盖的分轮重试。
//   - 原地重试（契约第 5 点）：LLM 调用失败 / 返回非法 JSON / schema 校验不通过，
//     都以全新 session 原地重试整篇提取，最多 maxExtractRounds 次。
//
// This file implements ExtractFromAnnotations, the extraction-stage entry of the new
// LLM pipeline. It takes the repo-wide set of already-annotated entity/concept items
// and feeds them (with tags) in a single large-context request, asking the LLM to
// infer the cross-skill reusable domain hierarchy and return strict JSON. Unlike the
// legacy Extract (raw Markdown, per-skill coverage retry), the input is pre-classified
// items, fed all at once, with whole-extraction in-place retry on call/JSON/schema
// failure (contract §5).
package extract

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/llm"
	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
)

// ExtractFromAnnotations 从 repo 级全部标注文档提取领域知识层。
//
// 行为：
//   - docs 为空 → 返回空 Result，不消耗 LLM。
//   - 否则把所有文档的 items（带 tag/name/content，按 skill 分组）拼成单个请求，
//     一次性投喂；解析 + 校验通过则返回，失败则以全新 session 原地重试整篇提取。
//   - 达到 maxExtractRounds 仍失败 → 返回 error（由上层状态机决定后续处理）。
//
// ExtractFromAnnotations extracts the domain-knowledge layer from the repo-wide set of
// annotated documents, feeding all items in one request and retrying the whole
// extraction in place on failure, up to maxExtractRounds.
func (e *Extractor) ExtractFromAnnotations(ctx context.Context, docs []*dktypes.AnnotatedDocument) (*Result, error) {
	if e.client == nil {
		return nil, fmt.Errorf("extract: llm client is nil")
	}
	if len(docs) == 0 {
		return &Result{}, nil
	}

	user := assembleAnnotatedMessage(docs)
	// 04 id → skill 索引：提取阶段不再让 LLM 产出 source_skills，由 members 的 04 id 倒推。
	skillByUnit := buildSkillIndex(docs)

	var lastErr error
	for round := 1; round <= maxExtractRounds; round++ {
		resp, err := e.client.Complete(ctx, llm.CompleteRequest{
			System:    annotatedSystemPrompt,
			User:      user,
			MaxTokens: e.maxTokens,
		})
		if err != nil {
			lastErr = fmt.Errorf("第 %d 次提取调用: %w", round, err)
			log.Printf("[extract-annot] 第 %d/%d 次调用失败，原地重试: %v", round, maxExtractRounds, err)
			continue
		}

		// 复用既有 parseAndValidate：解析 JSON、生成 domain/subdomain slug、
		// 过滤非法 relation.kind、填充 provenance。
		domains, perr := parseAndValidate(resp.Text)
		if perr != nil {
			lastErr = fmt.Errorf("第 %d 次解析: %w", round, perr)
			log.Printf("[extract-annot] 第 %d/%d 次解析失败，原地重试: %v", round, maxExtractRounds, perr)
			continue
		}

		// 融合映射后处理（B 阶段）：用 members 倒推 source_skills；第二期 description 留空
		// （待第三期按 subdomain 融合重写）。
		backfillSourceSkills(domains, skillByUnit)

		// schema 校验：结构完整性（至少一个 domain；entity/concept 必备 id+name）。
		// 不通过 → 原地重试重新提取（契约第 5 点）。
		if verr := validateExtractSchema(domains); verr != nil {
			lastErr = fmt.Errorf("第 %d 次 schema 校验: %w", round, verr)
			log.Printf("[extract-annot] 第 %d/%d 次 schema 校验失败，原地重试: %v", round, maxExtractRounds, verr)
			continue
		}

		log.Printf("[extract-annot] ✓ 第 %d 轮提取成功：%d 个 domain", round, len(domains))
		return &Result{Domains: domains, Rounds: round}, nil
	}

	return nil, fmt.Errorf("extract from annotations: 原地重试 %d 次后仍失败: %w", maxExtractRounds, lastErr)
}

// ——————————————————————————————————————————————————————————————————————————————
// 输入组装 / Input assembly
// ——————————————————————————————————————————————————————————————————————————————

// assembleAnnotatedMessage 把（已刷 04 id、同 skill 合并后的）标注单元拼装为单个 user 消息：
// 按来源 skill 分组，每条形如 "- id=<04id> [tag] name: content"。
// 刻意只投 04 id / name / content（**不投 detail**）：提取阶段只做「组织与融合映射」，
// 用 04 id 做稳定引用，detail 的长文留给后续阶段消费（B 阶段：提取端对 detail 无感）。
//
// assembleAnnotatedMessage assembles the id-assigned, merged items into one user message,
// grouped by skill; each line is "- id=<04id> [tag] name: content" (no detail). The 04 id
// gives the extraction stage a stable handle for fusion members.
func assembleAnnotatedMessage(docs []*dktypes.AnnotatedDocument) string {
	order := make([]string, 0)
	bySkill := make(map[string][]*dktypes.AnnotatedDocument)
	for _, d := range docs {
		if d == nil {
			continue
		}
		if _, ok := bySkill[d.Skill]; !ok {
			order = append(order, d.Skill)
		}
		bySkill[d.Skill] = append(bySkill[d.Skill], d)
	}

	var sb strings.Builder
	sb.WriteString("# 领域知识融合任务\n\n")
	sb.WriteString("以下是来自多个 skill 的、已经标注并带有【唯一 id】的 entity / concept 知识单元" +
		"（每条形如 “- id=<唯一id> [tag] 名称: 内容”）。请从全局视野出发：\n" +
		"1）归纳「可跨 skill 复用的领域」（domain）及其子域（subdomain）；\n" +
		"2）把语义等价 / 同义的输入单元融合为一个「融合节点」，用 members 列出它由哪些输入单元 id 组成（可跨 skill 融合）；\n" +
		"3）把每个融合节点归入最合适的子域；\n" +
		"4）识别融合节点之间的 entity↔entity 语义关系。\n" +
		"你只需输出「组织与融合的映射操作」，不要重写正文，严格按要求返回 JSON。\n\n")

	for _, skill := range order {
		fmt.Fprintf(&sb, "## skill: %s\n", skill)
		// 同一 skill 下不同文档可能出现相同 04 id（同名单元）：投喂时按 id 去重，
		// 避免重复行干扰 LLM（dump 的 04_extract_input 仍保留完整 per-document 结构）。
		seen := make(map[string]bool)
		for _, d := range bySkill[skill] {
			for _, it := range d.Items {
				if it.ID != "" && seen[it.ID] {
					continue
				}
				seen[it.ID] = true
				fmt.Fprintf(&sb, "- id=%s [%s] %s: %s\n", it.ID, it.Tag, it.Name, it.Content)
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// buildSkillIndex 构造 04 id → 来源 skill 的索引，供 backfillSourceSkills 倒推。
// buildSkillIndex maps each 04 id to its source skill.
func buildSkillIndex(docs []*dktypes.AnnotatedDocument) map[string]string {
	idx := make(map[string]string)
	for _, d := range docs {
		if d == nil {
			continue
		}
		for _, it := range d.Items {
			if strings.TrimSpace(it.ID) != "" {
				idx[it.ID] = d.Skill
			}
		}
	}
	return idx
}

// backfillSourceSkills 用融合节点的 members（04 id）倒推 entity/concept 与 domain 的
// source_skills，并把 entity/concept 的 description 显式置空（第二期：正文留待第三期
// 按 subdomain 融合重写）。domain 的 source_skills = 其下所有融合节点 source_skills 并集。
//
// backfillSourceSkills backfills source_skills from Members and clears entity/concept
// descriptions (phase 2: prose is written later in phase 3).
func backfillSourceSkills(domains []Domain, skillByUnit map[string]string) {
	for di := range domains {
		d := &domains[di]
		var domainSkills []string
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			for ni := range sd.Entities {
				skills := skillsOfMembers(sd.Entities[ni].Members, skillByUnit)
				sd.Entities[ni].SourceSkills = skills
				sd.Entities[ni].Description = "" // 第二期留空，待第三期融合重写
				domainSkills = unionStrings(domainSkills, skills)
			}
			for ni := range sd.Concepts {
				skills := skillsOfMembers(sd.Concepts[ni].Members, skillByUnit)
				sd.Concepts[ni].SourceSkills = skills
				sd.Concepts[ni].Description = ""
				domainSkills = unionStrings(domainSkills, skills)
			}
		}
		d.SourceSkills = domainSkills
	}
}

// skillsOfMembers 返回一组 04 id（members）对应的去重、稳定顺序的 skill 列表。
// 无法在索引中解析的 id（不存在的 member）被安全忽略——覆盖度校准由 repair 阶段处理。
//
// skillsOfMembers returns the de-duplicated skills for a set of 04 ids (members).
func skillsOfMembers(members []string, skillByUnit map[string]string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, m := range members {
		s, ok := skillByUnit[m]
		if !ok || s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// annotatedSystemPrompt 是「融合映射」提取阶段的 system 提示（重构第二期 B 阶段）。
// 提取阶段输出「组织与融合的映射操作」而非重写正文：归纳 domain/subdomain，把输入单元
// 融合为融合节点并用 members 声明来源，识别关系。不产出 description / source_skills
// （前者留待第三期融合重写，后者由 members 倒推）。
//
// annotatedSystemPrompt instructs the fusion-mapping extraction stage (phase 2 part B):
// infer domains/subdomains, fuse input items into nodes declared via members, and detect
// relations — emitting mapping operations rather than rewriting prose (no description /
// source_skills).
const annotatedSystemPrompt = `你是领域知识融合专家。用户会给你来自多个 skill 的、已经分类并带有【唯一 id】的 ` +
	`entity（实体）与 concept（概念）知识单元。你的任务是从全局视野输出「组织与融合的映射操作」，而不是重写正文：
(1) 归纳出「可跨 skill 复用的领域」（domain）及其子域（subdomain）；
(2) 把语义等价 / 同义的输入单元融合为一个「融合节点」，用 members 列出该融合节点由哪些输入单元 id 组成（可跨 skill 融合）；
(3) 把每个融合节点归入最合适的子域；
(4) 识别融合节点之间的 entity↔entity 语义关系。

严格要求：
1. 只返回一个 JSON 对象（json 格式），不要输出任何解释性文字、Markdown 代码围栏或前后缀。
2. domain 与 subdomain 的 name 使用中文。
3. 每个 domain 必须给出 reusability_rationale，自证「为何这是一个可跨 skill 复用的领域」。
4. 每个融合节点（entity/concept）必须给出：
   - id：子域内唯一的 kebab-case 短标识（可按融合后的含义命名，不必等于输入 id）；
   - label："Entity" 或 "Concept"；
   - name：中文名；
   - summary：一句话摘要；
   - confidence：0.0~1.0；
   - members：该融合节点融合的【输入单元 id】列表，必须逐字引用输入中真实存在的 id，不得编造、不得改写 id。
5. **不要输出 description**：融合节点的正文详述由后续阶段依据 members 生成，你只需给出 summary 与 members。
6. **不要输出 source_skills**：来源 skill 由 members 自动倒推，你无需给出。
7. **必须覆盖全部输入**：每一个输入单元 id 都必须被某个融合节点的 members 收纳，绝不允许遗漏。
   融合是「把全部输入单元归类到融合节点」，不是「挑几个代表建骨架」——若某单元与已有节点语义等价就并入其 members，
   若自成一类就为它新建融合节点。语义等价的多个输入单元应合并进同一节点的 members。
8. relations 仅表达融合节点之间的真实语义关系，source/target 必须是你上面给出的【融合节点 id】；
   kind 只能取以下 6 个枚举之一，不得自造："triggers"、"depends_on"、"references"、"generalizes"、"composes"、"contradicts"。
9. 层级组成关系（domain→subdomain→节点）无需在 relations 中给出，由后续流程自动物化。

返回的 JSON 必须严格符合以下样例结构（字段名保持一致；注意融合节点没有 description 与 source_skills）：
{
  "domains": [
    {
      "name": "订单管理",
      "summary": "一句话概括",
      "reusability_rationale": "为何这是可复用领域",
      "subdomains": [
        {
          "name": "订单生命周期",
          "summary": "...",
          "entities": [
            {
              "id": "order-aggregate",
              "label": "Entity",
              "name": "订单聚合根",
              "summary": "...",
              "confidence": 0.9,
              "members": ["order-skill-entity-订单聚合根", "pay-skill-entity-订单根"]
            }
          ],
          "concepts": [
            {
              "id": "idempotency-key",
              "label": "Concept",
              "name": "幂等键",
              "summary": "...",
              "confidence": 0.85,
              "members": ["order-skill-concept-幂等键"]
            }
          ],
          "relations": [
            {
              "source": "order-aggregate",
              "target": "idempotency-key",
              "kind": "depends_on",
              "description": "...",
              "confidence": 0.85
            }
          ]
        }
      ]
    }
  ]
}`

// ——————————————————————————————————————————————————————————————————————————————
// schema 校验 / Schema validation
// ——————————————————————————————————————————————————————————————————————————————

// validateExtractSchema 校验提取结果的结构完整性（parseAndValidate 之上的补充）：
//   - 至少存在一个 domain（输入非空却提取出 0 个 domain 视为提取失败）；
//   - 每个 domain / subdomain 都有 slug（由 parseAndValidate 生成，防御性再校验）；
//   - 每个 entity / concept 都有非空 id 与 name（入库时依赖 id 非空，作为 AssignNodeUUIDs 的 members 派生前提）。
//
// 不通过时返回 error，由 ExtractFromAnnotations 触发原地重试。
//
// validateExtractSchema checks structural integrity beyond parseAndValidate and returns
// an error (triggering an in-place retry) when the result is unusable.
func validateExtractSchema(domains []Domain) error {
	if len(domains) == 0 {
		return fmt.Errorf("提取结果为空：未归纳出任何 domain")
	}
	for i := range domains {
		d := &domains[i]
		if strings.TrimSpace(d.Slug) == "" {
			return fmt.Errorf("domain %q 缺 slug", d.Name)
		}
		for j := range d.Subdomains {
			sd := &d.Subdomains[j]
			if strings.TrimSpace(sd.Slug) == "" {
				return fmt.Errorf("domain %q 下 subdomain %q 缺 slug", d.Name, sd.Name)
			}
			for _, n := range sd.Entities {
				if strings.TrimSpace(n.ID) == "" || strings.TrimSpace(n.Name) == "" {
					return fmt.Errorf("subdomain %q 下存在缺 id/name 的 entity", sd.Name)
				}
			}
			for _, n := range sd.Concepts {
				if strings.TrimSpace(n.ID) == "" || strings.TrimSpace(n.Name) == "" {
					return fmt.Errorf("subdomain %q 下存在缺 id/name 的 concept", sd.Name)
				}
			}
		}
	}
	return nil
}
