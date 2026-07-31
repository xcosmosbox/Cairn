// Package extract 实现 LLM 驱动的领域知识提取器（Task #6）。
//
// 它接收一批 skill 及其 reference/*.md 全文，拼装为单次大上下文请求喂给 LLM，
// 要求 LLM 识别"可跨 skill 复用的领域"并返回严格 JSON（契约第三节 schema），
// 再解析、校验、映射为中间数据结构；核心是一个 JSON 容错重试循环（契约第六节）：
// 记录「已解析 domains[]」+「未解析 reference 文件列表」，最多 3 轮，每轮以全新
// LLM session 重试未解析文件，解析成功并入、失败留待下轮，超限则记录警告并返回
// 已解析部分（不静默丢弃）。
//
// Package extract implements the LLM-driven domain knowledge extractor (Task #6).
// It takes a batch of skills with their reference/*.md contents, assembles a single
// large-context request, asks the LLM to identify cross-skill reusable domains and
// return strict JSON (contract §3), then parses/validates/maps into intermediate
// structs. The core is a JSON fault-tolerant retry loop (contract §6): it tracks
// the parsed domains[] plus the unresolved reference-file list, retries up to 3
// rounds with a fresh LLM session each time, merges successes and keeps failures
// for the next round, and on exhaustion logs a warning and returns the parsed part.
//
// 设计原则：依赖注入 llm.Client（便于 mock，SOLID）；本包不触碰存储/schema，
// source_skills 作为新概念仅落在中间结构，落库映射（写入 source_refs、物化
// skill→domain provides 边）交给 Task #7（关注点分离）。
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"unicode"

	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/llm"
	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
)

// maxExtractRounds 是 JSON 容错重试循环的硬上限（契约第六节：3 次）。
// 避免反复消耗 token 的死循环。
// maxExtractRounds is the hard cap on the retry loop (contract §6: 3 rounds).
const maxExtractRounds = 3

// ——————————————————————————————————————————————————————————————————————————————
// 输入类型 / Input types
// ——————————————————————————————————————————————————————————————————————————————

// ReferenceFile 是喂给提取器的单个 reference 文档：内容 + 其来源 skill。
// 每个文件标注来源 skill，是为了让 LLM 输出 domain 时能回填 source_skills，
// 从而支撑 skill↔domain 双向查询（契约第二节）。
//
// ReferenceFile is a single reference document fed to the extractor: its content
// plus the skill it came from. Labeling each file with its source skill lets the
// LLM populate source_skills on the domains it emits (contract §2).
type ReferenceFile struct {
	// Skill 是该 reference 文件所属的 skill 名称。
	// Skill is the name of the skill this reference file belongs to.
	Skill string
	// Path 是 reference 文件的相对路径（用于溯源与日志）。
	// Path is the reference file's relative path (for provenance and logging).
	Path string
	// Content 是 reference 文件全文。
	// Content is the full text of the reference file.
	Content string
}

// ——————————————————————————————————————————————————————————————————————————————
// 中间数据结构 / Intermediate structures（镜像契约第三节 JSON schema）
// ——————————————————————————————————————————————————————————————————————————————
//
// 这些结构直接用于 json.Unmarshal，同时也是提取器的产出。之所以不直接复用
// dktypes（DomainDef/EntityDef/…），是因为契约新增了 source_skills 字段而
// dktypes 尚无对应字段——"先解析到中间结构，落库映射交给 Task #7"（见任务）。
// 每个结构都提供 ToXxxDef() 转换方法，把能对应上的部分映射回 dktypes 供落库复用（DRY）。

// llmOutput 是 LLM 返回 JSON 的顶层容器（契约第三节：顶层 domains[]）。
// llmOutput is the top-level container of the LLM's JSON (contract §3: top-level domains[]).
type llmOutput struct {
	Domains []Domain `json:"domains"`
}

// Domain 是一个"可跨 skill 复用的领域"的中间表示。
// Domain is the intermediate representation of a cross-skill reusable domain.
type Domain struct {
	// Name 是领域的中文名称（契约决策 2：中文 name + kebab slug ID）。
	// Name is the domain's (Chinese) name.
	Name string `json:"name"`
	// Summary 一句话概括。
	Summary string `json:"summary"`
	// Description 详细描述。
	Description string `json:"description"`
	// ReusabilityRationale 是 LLM 对"为何这是可复用领域"的自证（落实粒度原则）。
	// ReusabilityRationale is the LLM's justification that this is a reusable domain.
	ReusabilityRationale string `json:"reusability_rationale"`
	// SourceSkills 是贡献了此领域的 skill 名列表——契约新增的核心字段，支撑双向查询。
	// 落库时由 Task #7 写入既有的 source_refs 字段。
	// SourceSkills lists the skills that contributed to this domain — the new core
	// field supporting bidirectional queries; Task #7 maps it into source_refs.
	SourceSkills []string `json:"source_skills"`
	// Subdomains 是该领域下的子域列表。
	Subdomains []Subdomain `json:"subdomains"`

	// ---- 以下为解析后派生填充的字段，非 LLM 直接输出（json:"-"）----
	// Slug 是由 Name 生成的 kebab-case ID（domain 节点 ID）。
	// Slug is the kebab-case ID derived from Name (the domain node ID).
	Slug string `json:"-"`
	// Provenance 固定为 llm_inferred（契约决策 4）。
	// Provenance is fixed to llm_inferred (contract decision 4).
	Provenance string `json:"-"`
}

// Subdomain 是 domain 因体量过大分出的子分支的中间表示。
// Subdomain is the intermediate representation of a subdomain branched off a domain.
type Subdomain struct {
	Name        string     `json:"name"`
	Summary     string     `json:"summary"`
	Description string     `json:"description"`
	Entities    []Node     `json:"entities"`
	Concepts    []Node     `json:"concepts"`
	Relations   []Relation `json:"relations"`

	// Slug 是由 Name 生成的 kebab-case ID（subdomain 节点 ID），派生字段。
	Slug string `json:"-"`
}

// Node 是 entity / concept 的统一中间表示（二者字段结构一致，仅 Label 不同）。
// Node is the unified intermediate representation of an entity/concept
// (identical fields; only Label differs).
type Node struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"` // "Entity" | "Concept"
	Name        string   `json:"name"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Synonyms    []string `json:"synonyms,omitempty"`
	Confidence  float64  `json:"confidence"`
	// SourceSkills 是 entity 级来源 skill（契约第三节 entity 也带 source_skills）。
	// 重构第二期 B 阶段起：提取 LLM 不再产出 source_skills，改由 Members 的 04 id 倒推填充
	// （见 extract_from_annotations.go 的 backfillSourceSkills）。
	// SourceSkills is the entity-level source skills; from phase 2 it is backfilled from
	// Members (the LLM no longer emits it).
	SourceSkills []string `json:"source_skills"`
	// Members 是该融合节点由哪些 04 标注单元（04 id）融合而成——重构第二期 B 阶段的核心。
	// 提取阶段输出「操作/映射」而非正文：LLM 用 members 声明每个融合节点的输入来源，
	// 供代码倒推 source_skills、做覆盖度校准，并支撑第三期按 subdomain 融合重写 description。
	// Members lists the 04 item ids fused into this node (phase 2 part B): the extraction
	// stage emits mapping operations, not prose.
	Members []string `json:"members,omitempty"`
	// FileSlug 是 shared 节点 primary 文件的可读 slug（LLM 生成，首次创建后不可变）。
	// FileSlug is a human-readable slug for the shared node's primary file path.
	FileSlug string `json:"file_slug,omitempty"`
}

// Relation 表达 entity↔entity 的真实语义关系（契约第三节）。
// 层级 composes 边由 ingest 自动物化，不在此产出。
// Relation expresses a real entity↔entity semantic relation (contract §3).
// Hierarchical composes edges are auto-materialized by ingest, not produced here.
type Relation struct {
	Source      string  `json:"source"`
	Target      string  `json:"target"`
	Kind        string  `json:"kind"`
	Description string  `json:"description"`
	Confidence  float64 `json:"confidence"`
}

// ——————————————————————————————————————————————————————————————————————————————
// 提取结果 / Result
// ——————————————————————————————————————————————————————————————————————————————

// Result 是一次完整提取的产出。
// Result is the output of one full extraction.
type Result struct {
	// Domains 是已解析并校验通过的领域列表。
	// Domains is the list of parsed-and-validated domains.
	Domains []Domain
	// UnresolvedFiles 是超过重试上限后仍未被任何领域覆盖的 reference 文件。
	// 不为空 → 提取未完全成功（已记录警告），供上层可观测，不静默丢弃。
	// UnresolvedFiles are reference files still uncovered after the retry cap.
	UnresolvedFiles []ReferenceFile
	// Rounds 是实际执行的重试轮数（可观测 / 调试）。
	// Rounds is the number of retry rounds actually executed.
	Rounds int
}

// ——————————————————————————————————————————————————————————————————————————————
// Extractor — 领域提取器
// ——————————————————————————————————————————————————————————————————————————————

// Extractor 是 LLM 驱动的领域提取器。llm.Client 通过依赖注入传入（SOLID：便于 mock）。
// Extractor is the LLM-driven domain extractor. The llm.Client is injected (SOLID).
type Extractor struct {
	client    llm.Client
	maxTokens int // 单次请求 max_tokens；0 表示交由 client 默认。
}

// NewExtractor 创建一个 Extractor。client 不可为 nil。
// maxTokens ≤0 时不覆盖，交由 llm.Client 的配置默认值决定。
//
// NewExtractor creates an Extractor. client must not be nil. When maxTokens ≤ 0,
// it is left unset and the llm.Client's configured default applies.
func NewExtractor(client llm.Client, maxTokens int) *Extractor {
	return &Extractor{client: client, maxTokens: maxTokens}
}

// Extract 执行完整的领域提取：输入组装 → LLM → JSON 解析/校验 → 容错重试循环。
//
// 契约第六节的重试循环：
//   状态 = 已解析 domains[]（按 slug 去重合并）+ 未解析 reference 文件列表
//   循环（最多 maxExtractRounds 次）：
//     1. 用「未解析文件」组装输入
//     2. 启用全新 LLM session（每轮新建 CompleteRequest，仅含 system+user，
//        不累积历史——llm.Client.Complete 本就无会话状态，天然满足"上下文干净"）
//     3. 开启 JSON mode 要求返回完整 JSON
//     4. 解析成功的并入 domains[]；其 source_skills 覆盖到的文件视为"已解析"
//     5. 未解析（skill 未被任何领域覆盖）的文件留待下轮；为空则提前结束
//   超过上限仍有未解析 → 记录警告（可观测，不静默丢弃），返回已解析部分
//
// Extract runs the full extraction with the contract §6 retry loop (see Chinese above).
func (e *Extractor) Extract(ctx context.Context, files []ReferenceFile) (*Result, error) {
	if e.client == nil {
		return nil, fmt.Errorf("extract: llm client is nil")
	}
	if len(files) == 0 {
		return &Result{}, nil
	}

	// 循环状态：已解析领域（按 slug 索引以便跨轮合并）+ 未解析文件列表。
	// Loop state: parsed domains (indexed by slug for cross-round merge) + unresolved files.
	parsedBySlug := make(map[string]*Domain)
	var parsedOrder []string // 保持稳定输出顺序 / preserve stable output order
	unresolved := append([]ReferenceFile(nil), files...)

	result := &Result{}

	for round := 1; round <= maxExtractRounds && len(unresolved) > 0; round++ {
		result.Rounds = round

		// 1. 用「未解析文件」组装单个 user 消息（全新 session 的输入）。
		userMsg := assembleUserMessage(unresolved)

		// 2 & 3. 全新 LLM session：仅 system+user，开启 JSON mode（由 openai.go 设置
		//        response_format:json_object）。每轮都是独立 Complete 调用，无历史累积。
		req := llm.CompleteRequest{
			System:    systemPrompt,
			User:      userMsg,
			MaxTokens: e.maxTokens,
		}
		resp, err := e.client.Complete(ctx, req)
		if err != nil {
			// LLM 调用本身失败（网络/限流已在 client 内重试耗尽）：不视作可恢复，
			// 直接返回错误——与"JSON 解析失败可重试"区分开。
			// A hard call failure (client already exhausted its own retries) is fatal.
			return nil, fmt.Errorf("extract: round %d llm call: %w", round, err)
		}

		// 4. 解析 + 校验本轮返回的 JSON。解析失败（非法 JSON）不返回错误，
		//    而是让本轮所有文件留在 unresolved，进入下一轮全新 session 重试。
		domains, perr := parseAndValidate(resp.Text)
		if perr != nil {
			log.Printf("[extract] round %d: JSON 解析失败，将重试未解析文件: %v", round, perr)
			// domains 为空 → 本轮无覆盖 → unresolved 原样进入下一轮。
		}

		// 并入已解析领域（按 slug 合并跨轮重复领域）。
		for i := range domains {
			mergeDomain(parsedBySlug, &parsedOrder, &domains[i])
		}

		// 5. 重算未解析文件：skill 已被任一已解析领域覆盖 → 视为已解析。
		unresolved = filterUnresolved(files, coveredSkills(parsedBySlug))
	}

	// 组装稳定顺序的输出。
	for _, slug := range parsedOrder {
		result.Domains = append(result.Domains, *parsedBySlug[slug])
	}
	result.UnresolvedFiles = unresolved

	// 超过上限仍有未解析 → 记录警告，不静默丢弃（契约第六节）。
	if len(unresolved) > 0 {
		paths := make([]string, len(unresolved))
		for i, f := range unresolved {
			paths[i] = f.Path
		}
		log.Printf("[extract] 警告：达到重试上限 %d 轮后仍有 %d 个 reference 文件未解析，返回已解析的 %d 个领域。未解析文件: %s",
			maxExtractRounds, len(unresolved), len(result.Domains), strings.Join(paths, ", "))
	}

	return result, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// 输入组装 / Input assembly（契约第二节格式）
// ——————————————————————————————————————————————————————————————————————————————

// assembleUserMessage 把一批 reference 文件拼装为单个 user 消息，
// 按来源 skill 分组，每个文件标注 skill 与相对路径（契约第二节）。
//
// assembleUserMessage assembles reference files into a single user message,
// grouped by source skill, each file labeled with its skill and path (contract §2).
func assembleUserMessage(files []ReferenceFile) string {
	// 按 skill 分组，同时保持稳定顺序（首次出现顺序）。
	// Group by skill while preserving first-seen order for determinism.
	order := make([]string, 0)
	bySkill := make(map[string][]ReferenceFile)
	for _, f := range files {
		if _, ok := bySkill[f.Skill]; !ok {
			order = append(order, f.Skill)
		}
		bySkill[f.Skill] = append(bySkill[f.Skill], f)
	}

	var sb strings.Builder
	sb.WriteString("# 领域知识提取任务\n\n")
	fmt.Fprintf(&sb, "以下是来自 %d 个 skill 的 reference 文档。请识别其中可跨 skill 复用的领域知识，"+
		"并严格按要求返回 JSON。\n\n", len(order))

	for _, skill := range order {
		fmt.Fprintf(&sb, "## skill: %s\n", skill)
		for _, f := range bySkill[skill] {
			fmt.Fprintf(&sb, "### reference: %s\n", f.Path)
			sb.WriteString(f.Content)
			// 保证文件间有空行分隔，避免相邻文件内容粘连。
			if !strings.HasSuffix(f.Content, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// systemPrompt 是提取任务的 system 提示。
// 要点：
//   - 要求识别"可跨 skill 复用的领域"，并用 reusability_rationale 自证粒度；
//   - 强制返回严格 JSON（含 "json" 字样与样例，满足 DeepSeek JSON mode 要求）；
//   - relations.kind 限定 6 个语义枚举，不得自造，也不得输出层级 composes 之外的编造值；
//   - domain/subdomain 用中文 name。
//
// systemPrompt is the system instruction. It requires identifying cross-skill
// reusable domains (self-justified via reusability_rationale), returning strict
// JSON (the word "json" and a sample are present to satisfy DeepSeek JSON mode),
// restricting relations.kind to the 6 semantic enums, and using Chinese names.
const systemPrompt = `你是领域知识抽取专家。请阅读用户提供的、来自多个 skill 的 reference 文档，` +
	`识别其中「可跨 skill 复用的领域」（domain）及其子域（subdomain）、实体（entity）、` +
	`概念（concept）与实体间的语义关系（relation）。

严格要求：
1. 只返回一个 JSON 对象（json 格式），不要输出任何解释性文字、Markdown 代码围栏或前后缀。
2. domain 与 subdomain 的 name 使用中文。
3. 每个 domain 必须给出 reusability_rationale，自证「为何这是一个可跨 skill 复用的领域」（粒度自证）。
4. 每个 domain 的 source_skills、每个 entity/concept 的 source_skills 必须回填其来源 skill 名（取自输入中的 "## skill:" 标注）。
5. relations 仅表达 entity↔entity 的真实语义关系；relations.kind 只能取以下 6 个枚举之一，不得自造：
   "triggers"、"depends_on"、"references"、"generalizes"、"composes"、"contradicts"。
6. 层级组成关系（domain→subdomain→entity）无需在 relations 中给出，由后续流程自动物化。

返回的 JSON 必须严格符合以下样例结构（字段名保持一致）：
{
  "domains": [
    {
      "name": "ClickHouse 查询",
      "summary": "一句话概括",
      "description": "详细描述",
      "reusability_rationale": "为何这是可复用领域",
      "source_skills": ["analytics-sql", "data-quality"],
      "subdomains": [
        {
          "name": "EC 产品查询",
          "summary": "...",
          "description": "...",
          "entities": [
            {
              "id": "ch-ec-product-query",
              "label": "Entity",
              "name": "EC 产品查询",
              "summary": "...",
              "description": "...",
              "synonyms": ["产品查询"],
              "confidence": 0.9,
              "source_skills": ["analytics-sql"]
            }
          ],
          "concepts": [
            {
              "id": "ch-ec-product-table",
              "label": "Concept",
              "name": "EC 产品表",
              "summary": "...",
              "description": "...",
              "confidence": 0.85,
              "source_skills": ["analytics-sql"]
            }
          ],
          "relations": [
            {
              "source": "ch-ec-product-query",
              "target": "ch-ec-product-table",
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
// 解析与校验 / Parsing and validation（契约第三节 schema）
// ——————————————————————————————————————————————————————————————————————————————

// validRelationKinds 是 relations.kind 允许的 6 个语义枚举（契约第三节）。
// 注意：provides 是 skill→domain 的来源边、由 ingest 物化，不属于 LLM 可输出的
// entity↔entity 关系，故不在此集合内。
// validRelationKinds are the 6 allowed semantic kinds for relations (contract §3).
// "provides" is excluded: it is a skill→domain source edge materialized by ingest.
var validRelationKinds = map[string]bool{
	string(dktypes.KindTriggers):    true,
	string(dktypes.KindDependsOn):   true,
	string(dktypes.KindReferences):  true,
	string(dktypes.KindGeneralizes): true,
	string(dktypes.KindComposes):    true,
	string(dktypes.KindContradicts): true,
}

// parseAndValidate 解析 LLM 返回的 JSON，校验并规整为中间 Domain 列表。
//   - JSON 非法 → 返回 error（触发重试循环的"未解析"路径）。
//   - JSON 合法但个别领域缺关键字段（name 为空）→ 跳过该领域（记录日志），不整体失败。
//   - relations.kind 非法 → 丢弃该 relation（记录日志），保留领域其余部分。
//   - 为每个 domain/subdomain 生成 kebab slug ID；provenance=llm_inferred。
//
// parseAndValidate parses and validates the LLM JSON into intermediate Domains.
func parseAndValidate(text string) ([]Domain, error) {
	trimmed := stripCodeFence(strings.TrimSpace(text))
	if trimmed == "" {
		return nil, fmt.Errorf("空响应")
	}

	var out llmOutput
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	domains := make([]Domain, 0, len(out.Domains))
	for i := range out.Domains {
		d := out.Domains[i]
		if strings.TrimSpace(d.Name) == "" {
			log.Printf("[extract] 跳过一个缺少 name 的领域（索引 %d）", i)
			continue
		}
		// domain/subdomain：中文 name + kebab slug ID；provenance=llm_inferred。
		d.Slug = slugify(d.Name)
		d.Provenance = string(dktypes.ProvenanceLLMInferred)

		// 规整子域并校验 relations.kind。
		for j := range d.Subdomains {
			sd := &d.Subdomains[j]
			sd.Slug = slugify(sd.Name)
			sd.Relations = filterRelations(sd.Relations, d.Name, sd.Name)
		}
		domains = append(domains, d)
	}
	return domains, nil
}

// filterRelations 丢弃 kind 非法（不在 6 枚举内）或缺 source/target 的关系。
// filterRelations drops relations with an invalid kind or missing source/target.
func filterRelations(rels []Relation, domainName, subName string) []Relation {
	if len(rels) == 0 {
		return rels
	}
	kept := make([]Relation, 0, len(rels))
	for _, r := range rels {
		if !validRelationKinds[r.Kind] {
			log.Printf("[extract] 丢弃非法 relation.kind=%q（domain=%s subdomain=%s source=%s target=%s）",
				r.Kind, domainName, subName, r.Source, r.Target)
			continue
		}
		if strings.TrimSpace(r.Source) == "" || strings.TrimSpace(r.Target) == "" {
			log.Printf("[extract] 丢弃缺 source/target 的 relation（domain=%s subdomain=%s kind=%s）",
				domainName, subName, r.Kind)
			continue
		}
		kept = append(kept, r)
	}
	return kept
}

// stripCodeFence 去除 LLM 偶尔包裹的 ```json ... ``` 代码围栏（JSON mode 下通常没有，
// 但作为容错保留）。仅当整体被围栏包裹时才剥离。
// stripCodeFence removes an optional ```json fence the LLM may wrap around output.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// 去掉首行 ``` 或 ```json
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "\n"), "```")
	return strings.TrimSpace(s)
}

// ——————————————————————————————————————————————————————————————————————————————
// 重试循环辅助 / Retry-loop helpers
// ——————————————————————————————————————————————————————————————————————————————

// mergeDomain 把新解析出的领域并入 parsed 集合（按 slug 去重合并）。
// 跨轮出现同名领域时：合并 source_skills（并集）与 subdomains（追加），
// 保留首见的 summary/description/rationale。
//
// mergeDomain merges a newly parsed domain into the parsed set, keyed by slug.
func mergeDomain(parsed map[string]*Domain, order *[]string, d *Domain) {
	existing, ok := parsed[d.Slug]
	if !ok {
		cp := *d
		parsed[d.Slug] = &cp
		*order = append(*order, d.Slug)
		return
	}
	existing.SourceSkills = unionStrings(existing.SourceSkills, d.SourceSkills)
	existing.Subdomains = append(existing.Subdomains, d.Subdomains...)
}

// coveredSkills 返回已被任一已解析领域覆盖的 skill 集合（取 domain 级 source_skills）。
// coveredSkills returns the set of skills covered by any parsed domain.
func coveredSkills(parsed map[string]*Domain) map[string]bool {
	covered := make(map[string]bool)
	for _, d := range parsed {
		for _, s := range d.SourceSkills {
			covered[s] = true
		}
	}
	return covered
}

// filterUnresolved 返回其来源 skill 尚未被任何已解析领域覆盖的原始文件列表。
// 这是重试循环判定"哪些 reference 文件仍未解析"的依据（契约第六节）。
//
// filterUnresolved returns the original files whose source skill is not yet covered.
func filterUnresolved(all []ReferenceFile, covered map[string]bool) []ReferenceFile {
	var out []ReferenceFile
	for _, f := range all {
		if !covered[f.Skill] {
			out = append(out, f)
		}
	}
	return out
}

// unionStrings 返回两个字符串切片的并集，保持稳定顺序、去重。
// unionStrings returns the de-duplicated union of two string slices (stable order).
func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ——————————————————————————————————————————————————————————————————————————————
// slug 生成 / Slug generation
// ——————————————————————————————————————————————————————————————————————————————

// slugify 将（中文）name 转换为 kebab-case 标识符，与 annotation 包的 toKebabCase
// 及校验规则 reKebabCase(`^[\p{Ll}\p{Lo}\p{N}][\p{Ll}\p{Lo}\p{N}-]*$`) 保持一致：
// 字母（含中文等无大小写文字）与数字保留、ASCII 字母转小写、其余分隔符折叠为单个连字符、
// 去除首尾连字符。
//
// slugify converts a (Chinese) name into a kebab-case identifier consistent with
// the annotation package's toKebabCase and the reKebabCase validation rule.
func slugify(s string) string {
	var sb strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r):
			sb.WriteRune(unicode.ToLower(r))
			prevHyphen = false
		case unicode.IsDigit(r):
			sb.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && sb.Len() > 0 {
				sb.WriteRune('-')
				prevHyphen = true
			}
		}
	}
	return strings.TrimRight(sb.String(), "-")
}

// ——————————————————————————————————————————————————————————————————————————————
// 中间结构 → dktypes 映射 / Intermediate → dktypes conversions
// ——————————————————————————————————————————————————————————————————————————————
//
// 这些转换供 Task #7（ingest 落库）复用：把能对应上的字段映射回既有 dktypes 结构。
// 注意 source_skills 无对应 dktypes 字段——本包不预先写入 source_refs，保留在中间
// 结构由 Task #7 决定落库策略（关注点分离，见契约第三节"落库时写入 source_refs"）。

// ToEntityDef 把中间 Node 映射为 dktypes.EntityDef（Label=Entity，Provenance 语义为
// llm_inferred，但 EntityDef 无 Provenance 字段，故仅设置可对应字段）。
// source_skills 不写入 SourceRefs——交由 Task #7 落库时处理。
func (n Node) ToEntityDef() dktypes.EntityDef {
	return dktypes.EntityDef{
		ID:          n.ID,
		Label:       string(dktypes.LabelEntity),
		Name:        n.Name,
		Summary:     n.Summary,
		Synonyms:    n.Synonyms,
		Description: n.Description,
		Confidence:  n.Confidence,
		SourceRefs:  []string{},
	}
}

// ToConceptDef 把中间 Node 映射为 dktypes.ConceptDef（Label=Concept）。
func (n Node) ToConceptDef() dktypes.ConceptDef {
	return dktypes.ConceptDef{
		ID:          n.ID,
		Label:       string(dktypes.LabelConcept),
		Name:        n.Name,
		Summary:     n.Summary,
		Synonyms:    n.Synonyms,
		Description: n.Description,
		Confidence:  n.Confidence,
		SourceRefs:  []string{},
	}
}

// ToRelationDef 把中间 Relation 映射为 dktypes.RelationDef，provenance=llm_inferred。
func (r Relation) ToRelationDef() dktypes.RelationDef {
	return dktypes.RelationDef{
		Source:      r.Source,
		Kind:        r.Kind,
		Target:      r.Target,
		Description: r.Description,
		Confidence:  r.Confidence,
		Provenance:  string(dktypes.ProvenanceLLMInferred),
		SourceRefs:  []string{},
	}
}

// ToDomainDef 把中间 Domain 映射为 dktypes.DomainDef（不含 source_skills/rationale，
// 后二者由 Task #7 决定如何落库到 description/properties/source_refs）。
func (d Domain) ToDomainDef() dktypes.DomainDef {
	dd := dktypes.DomainDef{
		Name:        d.Name,
		Summary:     d.Summary,
		Description: d.Description,
	}
	for _, sd := range d.Subdomains {
		sub := dktypes.SubdomainDef{
			Name:        sd.Name,
			Summary:     sd.Summary,
			Description: sd.Description,
		}
		for _, e := range sd.Entities {
			sub.Entities = append(sub.Entities, e.ToEntityDef())
		}
		for _, c := range sd.Concepts {
			sub.Concepts = append(sub.Concepts, c.ToConceptDef())
		}
		for _, r := range sd.Relations {
			sub.Relations = append(sub.Relations, r.ToRelationDef())
		}
		dd.Subdomains = append(dd.Subdomains, sub)
	}
	return dd
}
