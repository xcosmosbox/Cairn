// Package incremental 的本文件实现 I-4 增量对齐（增量流水线最需要新造的组件）：
// 把 I-3 产出的新标注单元（04 id）对齐到既有 KG 结构——按序判归属：
//
//  1. 融入已有 node（新单元与某已有 node 语义等价）→ 加入其 members，
//     **uuid 钉死不变（R2），不写 uuid_lineage**；
//  2. 归入已有 subdomain（不等价但属某子域）→ 在该子域新建 node；
//  3. batch 内互聚（多个新单元彼此应融合）→ 先小融合成新 node，再挂已有子域；
//  4. 新建 subdomain / domain。
//
// 召回用 FTS5（R10：不引 embedding）：每个新单元以 name+content 为 query 召回
// top-K 已有 node 候选。
//
// R1 铁律（UUID 零 prompt）：LLM 只见 **alias**（<subdomain-slug>#<序号> + name，
// call-scoped、不持久化、不入库），候选 node 的 uuid 绝不进入 prompt；
// LLM 返回也用 alias，代码负责 alias↔uuid 翻译（参照 AssignNodeUUIDs 的翻译模式）。
//
// This file implements I-4 incremental alignment: FTS5 recall of top-K existing
// nodes, then one LLM call deciding, per new unit, merge-into / attach / fuse /
// new-subdomain. UUIDs NEVER enter the prompt (R1): the LLM sees call-scoped
// aliases only, and the code translates alias↔uuid.
package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"unicode"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
)

// maxAlignRounds 是增量对齐 LLM 调用的原地重试上限（JSON 非法 / 校验不过时重试）。
// maxAlignRounds is the in-place retry cap for the alignment LLM call.
const maxAlignRounds = 2

// defaultRecallK 是 FTS5 召回的默认 top-K（设计定稿 K=10~20）。
// defaultRecallK is the default FTS5 recall size (spec: 10~20).
const defaultRecallK = 15

// ——————————————————————————————————————————————————————————————————————————————
// 输入 / 输出类型 / Input & output types
// ——————————————————————————————————————————————————————————————————————————————

// alignUnit 是一个待对齐的新标注单元（04 id + 内容 + 来源）。
// alignUnit is one new annotated unit awaiting alignment.
type alignUnit struct {
	ID         string  // 04 id / 04 id
	Tag        string  // entity | concept
	Name       string  // 标准化名称 / normalized name
	Content    string  // 提炼摘要 / distilled summary
	Detail     string  // 详述（I-6 重融合素材）/ detail (I-6 fusion material)
	Confidence float64 // 置信度 / confidence
	Skill      string  // 来源 skill / source skill
	FilePath   string  // 来源文档 / source doc
	StartLine  int     // span（与原档行号对齐；0=无）/ span start (0 = none)
	EndLine    int     // span 止 / span end
}

// pendingNode 是 I-4 判归属后产生的一个「待落库 node」（融入已有 / 新建）。
// I-6 为其重融合 description（human_curated 跳过），I-8 将其 upsert 入库。
//
// pendingNode is a node to be upserted after alignment (merged-into existing or
// newly created). I-6 re-fuses its description; I-8 upserts it.
type pendingNode struct {
	UUID       string   // 已有 node（融入）或 NodeUUID 首派生（新建）/ existing or first-derived uuid
	IsNew      bool     // 是否新建 / whether newly created
	Label      string   // "Entity" | "Concept"（新建时有效）
	Name       string   // 新建 node 的名称 / new node name
	Summary    string   // 新建 node 的摘要 / new node summary
	Confidence float64  // 新建 node 的置信度 / new node confidence
	Domain     string   // 归属 domain slug / domain slug
	Subdomain  string   // 归属 subdomain slug / subdomain slug
	NewMembers []string // 本轮新增的 member（04 id）/ members added this round
	// NewSubdomain / NewDomain 标记归属层是否需新建（I-8 物化层节点与 composes/provides 边）。
	// NewSubdomain / NewDomain mark whether the owning layers must be materialized.
	NewSubdomain  bool
	NewDomain     bool
	DomainName    string // 新建 domain/subdomain 的展示名 / display names for new layers
	SubdomainName string
	SourceSkills  []string // 新建 node 的来源 skills（source_refs 与 provides 边用）
}

// alignOutcome 是 I-4 的产出：待落库 node 集合 + 单元索引 + 来源行。
// alignOutcome is I-4's output: pending nodes + unit index + source rows.
type alignOutcome struct {
	// pending 按 uuid 索引的待落库 node（融入的、新建的）。
	// pending indexes nodes to upsert by uuid.
	pending map[string]*pendingNode
	// unitsByID 是 04 id → 单元（含 detail，I-6 素材）。
	// unitsByID maps 04 id → unit (detail for I-6).
	unitsByID map[string]*alignUnit
	// sourceRows 是 I-8 要写入 node_sources 的行（新来源贡献）。
	// sourceRows are the node_sources rows to insert at I-8.
	sourceRows []storage.NodeSource
	// llFailed 标记 LLM 判归属整体失败（重试耗尽降级）：
	// 此时全部 C4 文档都不得被当作「已解决」（问题 2：保留人类散文 + 不推进 baseline）。
	// llFailed marks total LLM placement failure: every C4 doc stays unresolved.
	llFailed bool
}

// ——————————————————————————————————————————————————————————————————————————————
// aligner — I-4 增量对齐器
// ——————————————————————————————————————————————————————————————————————————————

// aligner 是 I-4 增量对齐器：FTS5 召回 + LLM 判归属（alias 呈现，R1）。
// aligner implements I-4: FTS5 recall + LLM placement (aliased, R1).
type aligner struct {
	client    llm.Client
	maxTokens int
	recallK   int
	st        *stores
}

// newAligner 构造 I-4 对齐器。recallK ≤0 取 defaultRecallK。
// newAligner constructs the aligner; recallK ≤0 falls back to default.
func newAligner(client llm.Client, maxTokens int, st *stores, recallK int) *aligner {
	if recallK <= 0 {
		recallK = defaultRecallK
	}
	return &aligner{client: client, maxTokens: maxTokens, recallK: recallK, st: st}
}

// flattenUnits 把 I-3 标注文档拍平为单元列表（保留来源；按 04 id 去重后供 prompt）。
// 同一 04 id 出现在多篇文档（同 skill 同名单元）时：prompt 只出现一次，
// 但全部来源都保留（node_sources 每个文档一行）。
//
// flattenUnits flattens annotated docs into units, de-duplicating by 04 id for the
// prompt while keeping every source doc for node_sources.
func flattenUnits(docs []*dktypes.AnnotatedDocument) (units []*alignUnit, sourcesByID map[string][]*alignUnit) {
	sourcesByID = make(map[string][]*alignUnit)
	var order []string
	byID := make(map[string]*alignUnit)
	for _, d := range docs {
		if d == nil {
			continue
		}
		for _, it := range d.Items {
			if it.ID == "" {
				continue
			}
			u := &alignUnit{
				ID: it.ID, Tag: strings.ToLower(string(it.Tag)), Name: it.Name,
				Content: it.Content, Detail: it.Detail, Confidence: it.Confidence,
				Skill: d.Skill, FilePath: d.FilePath,
			}
			if it.SourceSpan != nil {
				u.StartLine = it.SourceSpan.StartLine
				u.EndLine = it.SourceSpan.EndLine
			}
			if existing, ok := byID[u.ID]; !ok {
				byID[u.ID] = u
				order = append(order, u.ID)
			} else {
				// 同一 04 id 可由多篇文档贡献互补素材。prompt/I-6 只保留一条
				// alignUnit，但 content/detail 必须做稳定去重聚合，不能只取首篇后
				// 就回写替换其余原文。置信度取各来源最小值（保守门控，与融合 node
				// minUnitConfidence 语义一致）。
				existing.Content = mergeDistinctText(existing.Content, u.Content)
				existing.Detail = mergeDistinctText(existing.Detail, u.Detail)
				if u.Confidence > 0 && (existing.Confidence <= 0 || u.Confidence < existing.Confidence) {
					existing.Confidence = u.Confidence
				}
			}
			sourcesByID[u.ID] = append(sourcesByID[u.ID], u)
		}
	}
	for _, id := range order {
		units = append(units, byID[id])
	}
	return units, sourcesByID
}

func mergeDistinctText(existing, incoming string) string {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)
	if incoming == "" || incoming == existing || strings.Contains(existing, incoming) {
		return existing
	}
	if existing == "" {
		return incoming
	}
	if strings.Contains(incoming, existing) {
		return incoming
	}
	return existing + "\n\n" + incoming
}

// ——————————————————————————————————————————————————————————————————————————————
// alias 机制（R1：call-scoped，绝不持久化、绝不入库）
// ——————————————————————————————————————————————————————————————————————————————

// aliasBook 是本次 LLM 调用的 alias ↔ 真实标识映射（call-scoped，用完即弃）。
// aliasBook is the call-scoped alias ↔ real-identity mapping (discarded after use).
type aliasBook struct {
	nodeByAlias map[string]*dktypes.Node // node alias → 已有 node（含 uuid，仅代码侧可见）
	subByAlias  map[string]*dktypes.Node // subdomain alias → 子域层节点
	domByAlias  map[string]*dktypes.Node // domain alias → 域层节点
}

// ——————————————————————————————————————————————————————————————————————————————
// 主流程 / Main flow
// ——————————————————————————————————————————————————————————————————————————————

// align 执行 I-4 增量对齐：召回 → LLM 判归属 → 物化为 alignOutcome。
// 返回 nil（空 outcome）当无新单元；LLM 彻底失败时降级为空 outcome + 告警
// （新单元丢失由告警可观测，不阻断旁路已完成的修改——与全量降级风格一致）。
//
// align runs I-4: recall → LLM placement → materialize. Degrades to an empty
// outcome + warning on total LLM failure (full-pipeline degrade style).
func (a *aligner) align(ctx context.Context, docs []*dktypes.AnnotatedDocument, rs *runState) (*alignOutcome, error) {
	outcome := &alignOutcome{
		pending:   make(map[string]*pendingNode),
		unitsByID: make(map[string]*alignUnit),
	}
	units, sourcesByID := flattenUnits(docs)
	if len(units) == 0 {
		return outcome, nil
	}
	for _, u := range units {
		outcome.unitsByID[u.ID] = u
	}

	// —— 1. FTS5 召回 top-K 已有 node 候选（R10：不引 embedding）——
	candidates := a.recall(ctx, units, rs)

	// —— 2. 载入已有 subdomain / domain 清单（attach/new_subdomain 的目标）——
	subs, doms, err := a.loadLayerNodes(ctx)
	if err != nil {
		return nil, err
	}

	// —— 3. 分配 call-scoped alias（R1：uuid 绝不进 prompt）——
	book, promptSections := buildAliases(candidates, subs, doms)

	// —— 4. LLM 判归属（一次调用，含 batch 内互聚全局视野）——
	decisions, groups, err := a.judge(ctx, units, promptSections, book, rs)
	if err != nil {
		// 重试耗尽：降级为空 outcome + 告警（不阻断旁路已完成的修改）。
		// llFailed=true：调用方必须把全部 C4 文档标记为未解决（问题 2）。
		rs.warnf(fmt.Sprintf("I-4 增量对齐 LLM 失败（新单元未对齐，降级跳过）: %v", err))
		return &alignOutcome{pending: map[string]*pendingNode{}, unitsByID: outcome.unitsByID, llFailed: true}, nil
	}

	// —— 5. 物化：alias→uuid 翻译 + 新 node 首派生 uuid ——
	a.materialize(ctx, outcome, decisions, groups, book, sourcesByID, rs)
	return outcome, nil
}

// recall 对每个新单元做 FTS5 召回，合并去重候选（仅 entity/concept 节点）。
// 查询用 OR 语义（name OR content 片段）——FTS5 空白分隔是隐式 AND，过严会把
// 本该召回的候选滤掉；召回失败（FTS 语法错误等）降级为无候选（不阻断）。
//
// recall runs FTS5 per unit and merges deduped entity/concept candidates. The
// query uses OR semantics (FTS5 whitespace is an implicit AND — too strict).
func (a *aligner) recall(ctx context.Context, units []*alignUnit, rs *runState) []*dktypes.Node {
	seen := make(map[string]bool)
	var out []*dktypes.Node
	for _, u := range units {
		query := buildRecallQuery(u)
		if query == "" {
			continue
		}
		hits, err := a.st.fts.Search(ctx, query, nil, "", a.recallK)
		if err != nil {
			rs.warnf(fmt.Sprintf("FTS 召回失败（单元 %s，降级无候选）: %v", u.ID, err))
			continue
		}
		for _, h := range hits {
			if h.Node == nil || seen[h.Node.ID] {
				continue
			}
			if h.Node.Label != dktypes.LabelEntity && h.Node.Label != dktypes.LabelConcept {
				continue // 只要融合节点（层节点不作融入目标）/ fusion nodes only
			}
			seen[h.Node.ID] = true
			out = append(out, h.Node)
		}
	}
	return out
}

// buildRecallQuery 构造一个单元的 FTS5 召回查询：name 为主信号，content 前 40 字符
// 为辅信号，OR 连接（FTS5 空白 = 隐式 AND，OR 才能「命中任一即召回」）。
// buildRecallQuery builds the FTS5 recall query: name OR content snippet.
func buildRecallQuery(u *alignUnit) string {
	var parts []string
	if q := sanitizeFTSQuery(u.Name); q != "" {
		parts = append(parts, q)
	}
	if q := sanitizeFTSQuery(truncateRunes(u.Content, 40)); q != "" {
		parts = append(parts, q)
	}
	return strings.Join(parts, " OR ")
}

// sanitizeFTSQuery 清洗 FTS5 查询串：去掉会引发语法错误的引号与操作符字符。
// sanitizeFTSQuery strips characters that would break FTS5 MATCH syntax.
func sanitizeFTSQuery(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case '"', '(', ')', ':', '^', '*', '{', '}', '~':
			sb.WriteRune(' ')
		default:
			sb.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

// loadLayerNodes 载入全部 subdomain / domain 层节点（attach 与 new_subdomain 的目标清单）。
// loadLayerNodes loads all subdomain/domain layer nodes.
func (a *aligner) loadLayerNodes(ctx context.Context) (subs, doms []*dktypes.Node, err error) {
	all, err := a.st.nodes.ListAll(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("load layer nodes: %w", err)
	}
	for _, n := range all {
		switch n.Label {
		case dktypes.LabelSubdomain:
			subs = append(subs, n)
		case dktypes.LabelDomain:
			doms = append(doms, n)
		}
	}
	return subs, doms, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// alias 分配与 prompt 构造 / Alias assignment & prompt construction
// ——————————————————————————————————————————————————————————————————————————————

// promptSections 是喂给 LLM 的三段候选清单文本（仅 alias + name + 摘要，零 uuid）。
// promptSections holds the three candidate-listing prompt sections (uuid-free).
type promptSections struct {
	Nodes string // 已有 node 候选（融入目标）/ existing node candidates
	Subs  string // 已有 subdomain 清单（挂接目标）/ existing subdomains
	Doms  string // 已有 domain 清单（新子域的挂靠）/ existing domains
}

// buildAliases 为候选 node / subdomain / domain 分配 call-scoped alias 并构造
// prompt 候选清单段。alias 规则（设计定稿）：node = `<subdomain-slug>#<序号>`，
// 序号在同一 subdomain 内唯一；subdomain = `sd#<序号>`；domain = `dom#<序号>`。
//
// R1 铁律自查点：本函数拼进 prompt 的只有 alias/label/name/summary/slug，
// 绝无 node uuid（uuid 只存在 book 的代码侧映射里）。
//
// buildAliases assigns call-scoped aliases and renders the candidate sections.
// R1: only alias/label/name/summary/slug enter the prompt — never a node uuid.
func buildAliases(candidates, subs, doms []*dktypes.Node) (*aliasBook, *promptSections) {
	book := &aliasBook{
		nodeByAlias: make(map[string]*dktypes.Node),
		subByAlias:  make(map[string]*dktypes.Node),
		domByAlias:  make(map[string]*dktypes.Node),
	}
	ps := &promptSections{}

	// node 候选：<subdomain-slug>#<序号>（序号同一 subdomain 内唯一，坑点 10）。
	counterBySub := make(map[string]int)
	var nb strings.Builder
	for _, n := range candidates {
		counterBySub[n.Subdomain]++
		alias := fmt.Sprintf("%s#%d", n.Subdomain, counterBySub[n.Subdomain])
		book.nodeByAlias[alias] = n
		fmt.Fprintf(&nb, "- alias=%s [%s] %s（域 %s / 子域 %s）：%s\n",
			alias, n.Label, n.Name, n.Domain, n.Subdomain, truncateRunes(n.Summary, 120))
	}
	ps.Nodes = nb.String()

	var sb strings.Builder
	for i, n := range subs {
		alias := fmt.Sprintf("sd#%d", i+1)
		book.subByAlias[alias] = n
		fmt.Fprintf(&sb, "- alias=%s %s（所属域 slug：%s）：%s\n",
			alias, n.Name, n.Domain, truncateRunes(n.Summary, 120))
	}
	ps.Subs = sb.String()

	var db strings.Builder
	for i, n := range doms {
		alias := fmt.Sprintf("dom#%d", i+1)
		book.domByAlias[alias] = n
		fmt.Fprintf(&db, "- alias=%s %s：%s\n", alias, n.Name, truncateRunes(n.Summary, 120))
	}
	ps.Doms = db.String()
	return book, ps
}

// truncateRunes 截断超长文本（prompt 体量控制）。
// truncateRunes truncates long text for prompt-size control.
func truncateRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}

// ——————————————————————————————————————————————————————————————————————————————
// LLM 判归属 / LLM placement
// ——————————————————————————————————————————————————————————————————————————————

// alignDecision 是 LLM 对单个新单元的归属判定。
// alignDecision is the LLM's placement decision for one new unit.
type alignDecision struct {
	Unit      string `json:"unit"`      // 04 id
	Action    string `json:"action"`    // merge_into | attach | fuse | new_subdomain
	Target    string `json:"target"`    // node alias（merge_into）或 subdomain alias（attach）
	Group     string `json:"group"`     // fuse 的组号 / fuse group id
	Domain    string `json:"domain"`    // new_subdomain：domain alias 或新 domain 中文名
	Subdomain string `json:"subdomain"` // new_subdomain：新子域中文名
	NodeName  string `json:"node_name"` // 新建 node 的中文名（attach/new_subdomain）
	Summary   string `json:"summary"`   // 新建 node 的一句话摘要
	Tag       string `json:"tag"`       // 新建 node 的 tag（entity|concept；缺省沿用单元 tag）
}

// alignGroup 是 LLM 对 batch 内互聚组的定义。
// alignGroup defines one in-batch fusion group.
type alignGroup struct {
	Group     string `json:"group"`
	NodeName  string `json:"node_name"`
	Summary   string `json:"summary"`
	Tag       string `json:"tag"`
	Target    string `json:"target"`    // subdomain alias（空 → 新建子域）
	Domain    string `json:"domain"`    // target 为空时：domain alias 或新 domain 中文名
	Subdomain string `json:"subdomain"` // target 为空时：新子域中文名
}

// alignLLMOutput 是 LLM 返回的顶层 JSON。
// alignLLMOutput is the LLM's top-level JSON.
type alignLLMOutput struct {
	Decisions []alignDecision `json:"decisions"`
	Groups    []alignGroup    `json:"groups"`
}

// judge 调用 LLM 对全部新单元判归属（含校验与原地重试）。
// judge calls the LLM to place every new unit, with validation and in-place retry.
func (a *aligner) judge(ctx context.Context, units []*alignUnit, ps *promptSections,
	book *aliasBook, rs *runState) ([]alignDecision, map[string]*alignGroup, error) {
	user := buildAlignPrompt(units, ps, "")
	var lastErr error
	for round := 1; round <= maxAlignRounds; round++ {
		resp, err := a.client.Complete(ctx, llm.CompleteRequest{
			System:    alignSystemPrompt,
			User:      user,
			MaxTokens: a.maxTokens,
		})
		if err != nil {
			lastErr = fmt.Errorf("第 %d 次对齐调用: %w", round, err)
			continue
		}
		var out alignLLMOutput
		if perr := json.Unmarshal([]byte(stripAlignFence(strings.TrimSpace(resp.Text))), &out); perr != nil {
			// Do not echo parser text into the next prompt. Although the standard
			// JSON parser currently reports only an offset, keeping feedback as a
			// fixed category prevents future parser changes from reflecting raw LLM
			// values (including a node UUID).
			lastErr = fmt.Errorf("第 %d 次解析失败", round)
			user = buildAlignPrompt(units, ps, "JSON 格式无效，请仅返回符合 schema 的对象")
			continue
		}
		groups, verr := validateAlignOutput(&out, units, book)
		if verr != nil {
			lastErr = verr
			// Validation feedback is deliberately coarse. Never put any value
			// supplied by the model (unit/target/domain/group/tag) into a prompt.
			user = buildAlignPrompt(units, ps, "输出结构校验失败，请按 schema 修正")
			continue
		}
		return out.Decisions, groups, nil
	}
	return nil, nil, fmt.Errorf("增量对齐 %d 轮仍未通过: %w", maxAlignRounds, lastErr)
}

// validateAlignOutput 校验 LLM 输出：每个单元恰好一条合法 decision；新 node
// ownership/tag 完整；fuse 组有定义；alias 形态的 domain 必须真实命中本次 alias book。
// book 为可选参数以便纯结构单测复用；生产调用始终传入。
// validateAlignOutput validates coverage, ownership, tags, and group definitions.
func validateAlignOutput(out *alignLLMOutput, units []*alignUnit, books ...*aliasBook) (map[string]*alignGroup, error) {
	var book *aliasBook
	if len(books) > 0 {
		book = books[0]
	}
	want := make(map[string]bool, len(units))
	for _, u := range units {
		want[u.ID] = true
	}
	seen := make(map[string]bool)
	groups := make(map[string]*alignGroup)
	for i := range out.Groups {
		g := &out.Groups[i]
		var ok bool
		g.Target = strings.TrimSpace(g.Target)
		if g.NodeName, ok = normalizePersistedName(g.NodeName); !ok {
			return nil, fmt.Errorf("互聚组 node_name 非法")
		}
		g.Summary = normalizePersistedSummary(g.Summary)
		if g.Group == "" {
			return nil, fmt.Errorf("groups 存在空 group 名")
		}
		if _, duplicate := groups[g.Group]; duplicate {
			return nil, fmt.Errorf("groups 存在重复 group")
		}
		if strings.TrimSpace(g.NodeName) == "" {
			return nil, fmt.Errorf("互聚组缺 node_name")
		}
		if _, ok := normalizeNewNodeTag(g.Tag); !ok {
			return nil, fmt.Errorf("互聚组 tag 非法（必须 entity/concept）")
		}
		if g.Target == "" {
			g.Domain = strings.TrimSpace(g.Domain)
			if g.Domain == "" {
				return nil, fmt.Errorf("互聚组新建子域时缺 domain/subdomain")
			}
			if unknownDomainAlias(g.Domain, book) {
				return nil, fmt.Errorf("互聚组 domain alias 未命中")
			}
			if !strings.HasPrefix(g.Domain, "dom#") {
				if g.Domain, ok = normalizePersistedName(g.Domain); !ok {
					return nil, fmt.Errorf("互聚组 domain 名称非法")
				}
			}
			if g.Subdomain, ok = normalizePersistedName(g.Subdomain); !ok {
				return nil, fmt.Errorf("互聚组 subdomain 名称非法")
			}
		}
		groups[g.Group] = g
	}
	validActions := map[string]bool{"merge_into": true, "attach": true, "fuse": true, "new_subdomain": true}
	for i := range out.Decisions {
		d := &out.Decisions[i]
		d.Target = strings.TrimSpace(d.Target)
		if !want[d.Unit] {
			return nil, fmt.Errorf("decision 指向未知单元（必须逐字引用输入 unit_id）")
		}
		if seen[d.Unit] {
			return nil, fmt.Errorf("decision 存在重复 unit")
		}
		seen[d.Unit] = true
		if !validActions[d.Action] {
			return nil, fmt.Errorf("action 非法（必须 merge_into/attach/fuse/new_subdomain 之一）")
		}
		switch d.Action {
		case "merge_into":
			if d.Target == "" {
				return nil, fmt.Errorf("merge_into 缺 target")
			}
		case "attach":
			var ok bool
			if d.NodeName, ok = normalizePersistedName(d.NodeName); !ok {
				return nil, fmt.Errorf("attach node_name 非法")
			}
			d.Summary = normalizePersistedSummary(d.Summary)
			if d.Target == "" {
				return nil, fmt.Errorf("attach 缺 target 或 node_name")
			}
			if _, ok := normalizeNewNodeTag(d.Tag); !ok {
				return nil, fmt.Errorf("attach tag 非法（必须 entity/concept）")
			}
		case "fuse":
			if _, ok := groups[d.Group]; d.Group == "" || !ok {
				return nil, fmt.Errorf("fuse group 未定义")
			}
		case "new_subdomain":
			var ok bool
			d.Domain = strings.TrimSpace(d.Domain)
			if d.Domain == "" {
				return nil, fmt.Errorf("new_subdomain 缺 domain/subdomain/node_name")
			}
			if !strings.HasPrefix(d.Domain, "dom#") {
				if d.Domain, ok = normalizePersistedName(d.Domain); !ok {
					return nil, fmt.Errorf("new_subdomain domain 名称非法")
				}
			}
			if d.Subdomain, ok = normalizePersistedName(d.Subdomain); !ok {
				return nil, fmt.Errorf("new_subdomain subdomain 名称非法")
			}
			if d.NodeName, ok = normalizePersistedName(d.NodeName); !ok {
				return nil, fmt.Errorf("new_subdomain node_name 非法")
			}
			d.Summary = normalizePersistedSummary(d.Summary)
			if _, ok := normalizeNewNodeTag(d.Tag); !ok {
				return nil, fmt.Errorf("new_subdomain tag 非法（必须 entity/concept）")
			}
			if unknownDomainAlias(d.Domain, book) {
				return nil, fmt.Errorf("new_subdomain domain alias 未命中")
			}
		}
	}
	if len(seen) != len(want) {
		return nil, fmt.Errorf("%d 个单元缺 decision（每个单元必须恰好一条）",
			len(want)-len(seen))
	}
	return groups, nil
}

func normalizeNewNodeTag(tag string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(tag))
	return normalized, normalized == "entity" || normalized == "concept"
}

// normalizePersistedName canonicalizes an LLM-supplied identity/display name
// before it can reach nodes or layer metadata. Names are single-line values:
// surrounding whitespace is presentation noise, while empty/control-bearing
// values are rejected. Unicode format and line-separator characters are also
// rejected because they can make a rendered heading ambiguous.
func normalizePersistedName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
			unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return "", false
		}
	}
	return name, true
}

// normalizePersistedSummary shares writeback's editable-text normalization so
// trailing blank/whitespace-only lines do not create a false C1 on the next run.
// It intentionally preserves leading whitespace and all legitimate multiline
// content.
func normalizePersistedSummary(raw string) string {
	return writeback.NormalizeEditable(raw)
}

// unknownDomainAlias distinguishes a new domain name from a fabricated
// call-scoped alias. Any dom# token is transport syntax and may never become a
// persisted domain slug unless it resolves in this exact call's alias book.
func unknownDomainAlias(ref string, book *aliasBook) bool {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "dom#") {
		return false
	}
	if book == nil {
		return true
	}
	_, ok := book.domByAlias[ref]
	return !ok
}

// ——————————————————————————————————————————————————————————————————————————————
// 物化：alias→uuid 翻译 + uuid 首派生（R2）
// ——————————————————————————————————————————————————————————————————————————————

// materialize 把校验通过的 LLM 判定物化为 pendingNode 集合与 node_sources 行。
//   - merge_into：unit 加入已有 node 的 members——**uuid 钉死不变（R2），绝不调用
//     NodeUUID 重派生**；alias 未命中（LLM 编造）降级告警跳过该单元；
//   - attach / fuse / new_subdomain：新建 node，NodeUUID(members) 首次派生 uuid；
//     防御：派生出的 uuid 与已有 node 碰撞（members 全同）→ 转为融入（uuid 沿用）。
//
// materialize turns validated decisions into pending nodes and source rows.
// merge_into NEVER re-derives the uuid (R2); fresh nodes get NodeUUID(members).
func (a *aligner) materialize(ctx context.Context, outcome *alignOutcome, decisions []alignDecision,
	groups map[string]*alignGroup, book *aliasBook, sourcesByID map[string][]*alignUnit, rs *runState) {

	// groupUnits 聚合 fuse 组的成员单元（按 decision 出现顺序）。
	groupUnits := make(map[string][]string)
	for _, d := range decisions {
		if d.Action == "fuse" {
			groupUnits[d.Group] = append(groupUnits[d.Group], d.Unit)
		}
	}

	for _, d := range decisions {
		unit := outcome.unitsByID[d.Unit]
		if unit == nil {
			continue // 防御（validate 已保证覆盖）/ defensive
		}
		switch d.Action {
		case "merge_into":
			target, ok := book.nodeByAlias[d.Target]
			if !ok {
				rs.warnf(fmt.Sprintf("单元 %s merge_into 的 alias %q 未命中（LLM 编造，降级跳过该单元）", d.Unit, d.Target))
				continue
			}
			pn := outcome.pending[target.ID]
			if pn == nil {
				pn = &pendingNode{
					UUID:      target.ID, // R2：uuid 钉死，绝不重派生 / pinned, never re-derived
					IsNew:     false,
					Domain:    target.Domain,
					Subdomain: target.Subdomain,
				}
				outcome.pending[target.ID] = pn
			}
			pn.NewMembers = append(pn.NewMembers, d.Unit)
			a.appendSourceRows(outcome, target.ID, d.Unit, sourcesByID)

		case "attach":
			sub, ok := book.subByAlias[d.Target]
			if !ok {
				rs.warnf(fmt.Sprintf("单元 %s attach 的 alias %q 未命中（降级跳过）", d.Unit, d.Target))
				continue
			}
			a.createPendingNode(ctx, outcome, rs,
				[]string{d.Unit}, d.NodeName, d.Summary, d.Tag,
				sub.Domain, sub.Subdomain, false, "", "", []string{unit.Skill}, sourcesByID)

		case "fuse":
			// 互聚组由组级统一物化（见下方 groups 循环），单元级不重复建。
			continue

		case "new_subdomain":
			dSlug, dName, newDom, ok := a.resolveDomain(d.Domain, book)
			if !ok {
				rs.warnf(fmt.Sprintf("单元 %s new_subdomain 的 domain alias %q 未命中（降级跳过）", d.Unit, d.Domain))
				continue
			}
			sSlug := extract.Slugify(d.Subdomain)
			// createPendingNode is the single place that derives a fresh UUID (R2).
			// Reuse its returned pending node instead of deriving NodeUUID again;
			// collision handling may intentionally return an existing pinned UUID.
			pn := a.createPendingNode(ctx, outcome, rs,
				[]string{d.Unit}, d.NodeName, d.Summary, d.Tag,
				dSlug, sSlug, true, dName, d.Subdomain, []string{unit.Skill}, sourcesByID)
			if pn != nil {
				pn.NewDomain = newDom
			}
		}
	}

	// 互聚组统一物化：组内全部单元融合为一个新 node（members = 组内全部 04 id）。
	for gid, memberIDs := range groupUnits {
		g := groups[gid]
		if g == nil || g.NodeName == "" {
			rs.warnf(fmt.Sprintf("互聚组 %s 定义缺失或缺 node_name（组内单元降级跳过）", gid))
			continue
		}
		tag := g.Tag
		var skills []string
		skillSeen := make(map[string]bool)
		for _, id := range memberIDs {
			if u := outcome.unitsByID[id]; u != nil && !skillSeen[u.Skill] {
				skillSeen[u.Skill] = true
				skills = append(skills, u.Skill)
			}
		}
		if g.Target != "" {
			sub, ok := book.subByAlias[g.Target]
			if !ok {
				rs.warnf(fmt.Sprintf("互聚组 %s 的 subdomain alias %q 未命中（降级跳过）", gid, g.Target))
				continue
			}
			a.createPendingNode(ctx, outcome, rs, memberIDs, g.NodeName, g.Summary, tag,
				sub.Domain, sub.Subdomain, false, "", "", skills, sourcesByID)
			continue
		}
		dSlug, dName, newDom, ok := a.resolveDomain(g.Domain, book)
		if !ok {
			rs.warnf(fmt.Sprintf("互聚组 %s 的 domain alias %q 未命中（组内单元降级跳过）", gid, g.Domain))
			continue
		}
		pn := a.createPendingNode(ctx, outcome, rs, memberIDs, g.NodeName, g.Summary, tag,
			dSlug, extract.Slugify(g.Subdomain), true, dName, g.Subdomain, skills, sourcesByID)
		if pn != nil {
			pn.NewDomain = newDom
		}
	}
}

// resolveDomain 把 LLM 给的 domain 引用（alias 或新中文名）解析为 (slug, name, 是否新建)。
// alias 命中 → 复用已有 domain；否则按名 Slugify，若与已有 slug 碰撞也复用（防御）。
// resolveDomain resolves an LLM domain reference (alias or new Chinese name).
func (a *aligner) resolveDomain(ref string, book *aliasBook) (slug, name string, isNew, ok bool) {
	ref = strings.TrimSpace(ref)
	if book != nil {
		if n, exists := book.domByAlias[ref]; exists {
			return n.Domain, n.Name, false, true
		}
	}
	if unknownDomainAlias(ref, book) {
		return "", "", false, false
	}
	name, ok = normalizePersistedName(ref)
	if !ok {
		return "", "", false, false
	}
	slug = extract.Slugify(name)
	if strings.TrimSpace(slug) == "" {
		return "", "", false, false
	}
	if book != nil {
		for _, n := range book.domByAlias {
			if n.Domain == slug { // 名变体碰撞已有 domain → 复用（防御）/ slug collision → reuse
				return n.Domain, n.Name, false, true
			}
		}
	}
	return slug, name, true, true
}

// createPendingNode 为新建 node 首派生 uuid（NodeUUID(members)，R2 允许的唯一时机）
// 并登记 pending + 来源行。防御：uuid 与已有 node 碰撞 → 转融入（沿用已有 uuid）。
// createPendingNode derives a fresh uuid for a new node (the only R2-legal time)
// and registers it; uuid collisions degrade to a merge.
func (a *aligner) createPendingNode(ctx context.Context, outcome *alignOutcome, rs *runState,
	members []string, name, summary, tag, dSlug, sSlug string, newSub bool,
	dName, sName string, skills []string, sourcesByID map[string][]*alignUnit) *pendingNode {

	canonicalName, validName := normalizePersistedName(name)
	if !validName {
		rs.warnf(fmt.Sprintf("新 node（members=%s）的 node_name 非法，降级跳过", strings.Join(members, ",")))
		return nil
	}
	name = canonicalName
	summary = normalizePersistedSummary(summary)
	if newSub {
		var ok bool
		if sName, ok = normalizePersistedName(sName); !ok {
			rs.warnf(fmt.Sprintf("新 node（members=%s）的 subdomain 名称非法，降级跳过", strings.Join(members, ",")))
			return nil
		}
		if dName, ok = normalizePersistedName(dName); !ok {
			rs.warnf(fmt.Sprintf("新 node（members=%s）的 domain 名称非法，降级跳过", strings.Join(members, ",")))
			return nil
		}
	}
	normalizedTag, ok := normalizeNewNodeTag(tag)
	if !ok {
		rs.warnf(fmt.Sprintf("新 node（members=%s）的 tag %q 非法，降级跳过", strings.Join(members, ","), tag))
		return nil
	}
	if strings.TrimSpace(dSlug) == "" || strings.TrimSpace(sSlug) == "" {
		rs.warnf(fmt.Sprintf("新 node（members=%s）缺 domain/subdomain ownership，降级跳过", strings.Join(members, ",")))
		return nil
	}
	uuid := extract.NodeUUID(members) // 首派生（R2 唯一合法时机）/ first-time derivation
	label := "Entity"
	if normalizedTag == "concept" {
		label = "Concept"
	}
	// 防御：与已有 node 碰撞（同一批 members 已有 node）→ 转融入，沿用其钉死 uuid。
	if existing, err := a.st.nodes.GetByID(ctx, uuid); err == nil && existing != nil {
		pn := outcome.pending[uuid]
		if pn == nil {
			pn = &pendingNode{UUID: uuid, IsNew: false, Domain: existing.Domain, Subdomain: existing.Subdomain}
			outcome.pending[uuid] = pn
		}
		pn.NewMembers = append(pn.NewMembers, members...)
		for _, m := range members {
			a.appendSourceRows(outcome, uuid, m, sourcesByID)
		}
		rs.warnf(fmt.Sprintf("新 node（members=%s）与已有 node 碰撞，转为融入（uuid 沿用）", strings.Join(members, ",")))
		return pn
	}
	pn := &pendingNode{
		UUID: uuid, IsNew: true, Label: label, Name: name, Summary: summary,
		Confidence: minUnitConfidence(members, outcome.unitsByID), Domain: dSlug, Subdomain: sSlug,
		NewMembers:   append([]string(nil), members...),
		NewSubdomain: newSub, DomainName: dName, SubdomainName: sName,
		SourceSkills: skills,
	}
	// summary 为空时以首单元 content 兜底（渲染与 FTS 需要非空摘要）。
	if strings.TrimSpace(pn.Summary) == "" {
		for _, m := range members {
			if u := outcome.unitsByID[m]; u != nil && u.Content != "" {
				pn.Summary = normalizePersistedSummary(u.Content)
				break
			}
		}
	}
	outcome.pending[uuid] = pn
	for _, m := range members {
		a.appendSourceRows(outcome, uuid, m, sourcesByID)
	}
	return pn
}

// appendSourceRows 为一个 member（04 id）的全部来源文档追加 node_sources 行。
// appendSourceRows appends node_sources rows for every source doc of a member.
func (a *aligner) appendSourceRows(outcome *alignOutcome, uuid, unitID string, sourcesByID map[string][]*alignUnit) {
	for _, src := range sourcesByID[unitID] {
		outcome.sourceRows = append(outcome.sourceRows, storage.NodeSource{
			NodeUUID: uuid, MemberID: unitID, Skill: src.Skill,
			FilePath: src.FilePath, StartLine: src.StartLine, EndLine: src.EndLine,
		})
	}
}

// firstNonEmptyStr 返回首个非空字符串。
// firstNonEmptyStr returns the first non-empty string.
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// minUnitConfidence 取若干 member 单元的最小置信度（新建 node 的置信度——
// 与全量 ingest 的置信度门控语义衔接：node 置信度不高于其来源单元）。
// minUnitConfidence returns the minimum confidence across member units.
func minUnitConfidence(members []string, unitsByID map[string]*alignUnit) float64 {
	min := 1.0
	for _, m := range members {
		if u := unitsByID[m]; u != nil && u.Confidence < min {
			min = u.Confidence
		}
	}
	if min <= 0 {
		return 0.8 // 缺省兜底（单元缺置信度时）/ fallback
	}
	return min
}

// stripAlignFence 去除 LLM 可能包裹的 ```json 代码围栏。
// stripAlignFence removes an optional json code fence.
func stripAlignFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "\n"), "```")
	return strings.TrimSpace(s)
}

// ——————————————————————————————————————————————————————————————————————————————
// prompt（R1 自查：本文件拼进 prompt 的只有 04 id / alias / name / summary / slug，
// 绝无 node uuid——uuid 变量绝不流入 prompt 字符串）
// ——————————————————————————————————————————————————————————————————————————————

// alignSystemPrompt 是增量对齐的 system 提示（严格 JSON、alias 引用、顺序判归属）。
// alignSystemPrompt instructs the alignment task (strict JSON, alias references).
const alignSystemPrompt = `你是领域知识图谱的增量对齐专家。用户会给你一批「新增知识单元」（带 04 id），
以及既有图谱中可能相关的「已有节点候选」「已有子域」「已有领域」清单（都以 alias 标识）。
请按以下优先级顺序，为每一个新增单元判定归属：

1. 融入已有节点（merge_into）：新单元与某候选节点语义等价/同义 → 并入该节点（target 填节点 alias）；
2. 归入已有子域（attach）：不等价、但明显属于某个已有子域 → 在该子域下新建节点
   （target 填子域 alias，并给出 node_name/summary/tag）；
3. 批内互聚（fuse）：多个新单元彼此语义等价、应融合为一个新节点 → 填 group 组号，
   并在 groups 里给出该组的 node_name/summary/tag 与归属（target=子域 alias，
   或留空 target 改给 domain+subdomain 新建子域）；
4. 新建子域（new_subdomain）：不属于任何已有子域 → 给出 domain（已有领域 alias 或新领域中文名）、
   subdomain（新子域中文名）、node_name/summary/tag。

严格要求：
1. 只返回一个 JSON 对象（json 格式），不要输出任何解释性文字、Markdown 代码围栏或前后缀。
2. 每个新增单元必须且只能出现一次 decision；unit 必须逐字引用输入的 04 id。
3. target 只能逐字引用输入清单中给出的 alias，绝不编造、绝不改写。
4. node_name 使用中文；summary 一句话；tag 只能是 "entity" 或 "concept"。
5. 判定要保守：只有语义确实等价才 merge_into；不确定时优先 attach 或 new_subdomain。

返回 JSON 结构（字段名保持一致）：
{
  "decisions": [
    {"unit":"<04id>","action":"merge_into","target":"<节点alias>"},
    {"unit":"<04id>","action":"attach","target":"<子域alias>","node_name":"...","summary":"...","tag":"entity"},
    {"unit":"<04id>","action":"fuse","group":"G1"},
    {"unit":"<04id>","action":"new_subdomain","domain":"<领域alias或新领域名>","subdomain":"新子域名","node_name":"...","summary":"...","tag":"concept"}
  ],
  "groups": [
    {"group":"G1","node_name":"...","summary":"...","tag":"entity","target":"<子域alias或空>","domain":"<领域alias或新领域名>","subdomain":"<新子域名>"}
  ]
}`

// buildAlignPrompt 组装增量对齐的 user 消息（新增单元 + 三段候选清单 + 上轮纠错反馈）。
// buildAlignPrompt assembles the alignment user message.
func buildAlignPrompt(units []*alignUnit, ps *promptSections, prevErr string) string {
	var sb strings.Builder
	sb.WriteString("# 增量对齐任务\n\n## 新增知识单元（为每一个判定归属）\n")
	for _, u := range units {
		fmt.Fprintf(&sb, "- unit_id=%s [%s] %s: %s\n", u.ID, u.Tag, u.Name, u.Content)
	}
	sb.WriteString("\n## 已有节点候选（merge_into 的可选目标；以 alias 引用）\n")
	if ps.Nodes == "" {
		sb.WriteString("（无候选）\n")
	} else {
		sb.WriteString(ps.Nodes)
	}
	sb.WriteString("\n## 已有子域（attach 的可选目标；以 alias 引用）\n")
	if ps.Subs == "" {
		sb.WriteString("（无候选）\n")
	} else {
		sb.WriteString(ps.Subs)
	}
	sb.WriteString("\n## 已有领域（new_subdomain 的挂靠；以 alias 引用）\n")
	if ps.Doms == "" {
		sb.WriteString("（无候选）\n")
	} else {
		sb.WriteString(ps.Doms)
	}
	sb.WriteString("\n请按 merge_into → attach → fuse → new_subdomain 的优先级顺序判定，只返回 decisions+groups JSON。\n")
	if strings.TrimSpace(prevErr) != "" {
		fmt.Fprintf(&sb, "\n## 上一轮的问题（请修正后重试）\n- %s\n", prevErr)
	}
	return sb.String()
}

// logAlignStats 输出对齐统计（可观测）。
// logAlignStats logs alignment stats (observability).
func logAlignStats(outcome *alignOutcome) {
	var merges, news int
	for _, pn := range outcome.pending {
		if pn.IsNew {
			news++
		} else {
			merges++
		}
	}
	log.Printf("[incremental-align] 对齐完成: 融入已有 %d, 新建 node %d, node_sources 新行 %d",
		merges, news, len(outcome.sourceRows))
}
