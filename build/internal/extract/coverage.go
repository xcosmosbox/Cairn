// Package extract 的本文件实现「覆盖保障闭环」（重构第二期核心的漏融合治理）。
//
// 提取阶段要求 LLM 把**全部** 04 输入单元归类到融合节点的 members；但大任务下 LLM 常把
// 「融合归类」做成「挑代表搭骨架」，导致大量单元漏融合。本文件用两道覆盖率门控 + 增量补 +
// 兜底重提取，把漏融合收敛到可接受范围：
//
//	① 强化 prompt 保底（见 annotatedSystemPrompt 第 7 条）
//	② 首轮提取后算覆盖率：≥ P99 直接放行；
//	③ 否则进入「增量补融合」：把已提取结构 + 未覆盖清单一次性喂回伪 session，要求 LLM 对每个
//	   未覆盖单元给出「融入已有节点 / 新建节点 / 解释放过」三选一的**操作**；规则化扫描先应用
//	   融入 / 新建并从清单剔除，再剔除「给出非空解释」的单元（解释记入日志），剩余即真正遗漏；
//	④ 重算覆盖率：≥ P95 进入悬空修正；否则回退到提取阶段整轮重提取（上限 maxExtractAttempts）；
//	⑤ 耗尽仍不达标则降级（用当前最好结果继续），不无界消耗。
//
// 好处：进入 repair（悬空修正）时，融合映射已是一份「覆盖完整、无重复节点、无非法 member」的
// 干净清单，repair 只需专注悬空边（关注点分离）。
//
// This file implements the coverage-guarantee loop: two coverage gates (P99 to pass
// directly, P95 after incremental supplementation) plus a bounded whole re-extraction
// fallback, so the fusion mapping handed to the repair stage is a complete, clean list.
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

// 覆盖率门控阈值与整轮提取上限。
const (
	// coverageGateHigh（P99）：首轮提取覆盖率达到即直接放行，漏掉的少量单元放过。
	coverageGateHigh = 0.99
	// coverageGateFloor（P95）：增量补融合后覆盖率达到即进入悬空修正。
	coverageGateFloor = 0.95
	// maxExtractAttempts：含首轮在内的整轮提取上限（兜底重提取次数 = 该值 - 1）。
	maxExtractAttempts = 2
	// maxSupplementRounds：单次增量补融合的 LLM 解析原地重试上限。
	maxSupplementRounds = 2
	// maxBackfillRounds：单次「空节点补 members」的 LLM 解析原地重试上限。
	maxBackfillRounds = 2
)

// CoverageReport 是覆盖保障阶段的结果摘要（可观测，写盘到 05c1）。
// CoverageReport summarizes the coverage-guarantee stage (written to 05c1).
type CoverageReport struct {
	ExtractAttempts       int      // 实际执行的整轮提取次数
	InputUnits            int      // 04 输入单元总数（按 id 去重）
	DuplicateNodesMerged  int      // 合并的重复融合节点数
	InvalidMembersRemoved int      // 剔除的非法 member 引用数
	InvalidMemberIDs      []string // 被剔除的非法 member id（诊断用）
	EmptyNodesFound       int      // 校准后 members 为空的孤儿节点数（补前，最后一轮）
	EmptyNodesFilled      int      // 经伪 session 补上 members 的空节点数
	FirstPassCovered      int      // 首轮（增量补之前）被覆盖的单元数
	FirstPassRate         float64  // 首轮覆盖率
	Assigned              int      // 增量补：融入已有节点的单元数
	Created               int      // 增量补：新建融合节点数
	Explained             int      // 增量补：给出非空解释而放过的单元数
	ExplainedNotes        []string // 解释内容（"id: 理由"，记入日志）
	FinalCovered          int      // 最终被 members 覆盖的单元数
	FinalRate             float64  // 最终覆盖率（把已解释单元视为已处理）
	TrulyMissing          []string // 两轮后仍未覆盖且无解释的单元（真正遗漏）
	Outcome               string   // pass_p99 | pass_p95 | degraded
}

// coverageRate 计算覆盖率；输入为 0 时视为全覆盖（1.0），避免除零。
// coverageRate computes the coverage ratio; empty input counts as fully covered.
func coverageRate(covered, input int) float64 {
	if input <= 0 {
		return 1.0
	}
	return float64(covered) / float64(input)
}

// ExtractWithCoverage 执行「提取 + 覆盖保障」的完整闭环，返回最终结果与覆盖报告。
// 它是提取阶段对外的高层入口（orchestrator 只需调用它 + RepairDanglingRelations）。
//
// ExtractWithCoverage runs the full extraction-with-coverage loop.
func (e *Extractor) ExtractWithCoverage(ctx context.Context, docs []*dktypes.AnnotatedDocument) (res *Result, rpt *CoverageReport, err error) {
	rpt = &CoverageReport{}
	unitIDs := collectUnitIDs(docs)
	rpt.InputUnits = len(unitIDs)
	skillIdx := buildSkillIndex(docs)

	// 统一在返回前倒推 source_skills（含增量补 / repair 新建的节点），并清空 description
	// （第二期契约：正文留待第三期融合重写）。
	defer func() {
		if res != nil {
			backfillSourceSkills(res.Domains, skillIdx)
		}
	}()

	for attempt := 1; attempt <= maxExtractAttempts; attempt++ {
		rpt.ExtractAttempts = attempt

		r, xerr := e.ExtractFromAnnotations(ctx, docs)
		if xerr != nil {
			return nil, rpt, xerr
		}
		res = r

		// 确定性校准：合并重复融合节点 + 剔除非法 member（累积到报告）。
		rpt.DuplicateNodesMerged += DedupFusionNodes(res.Domains)
		invalid := CleanFusionMapping(res.Domains, unitIDs)
		rpt.InvalidMemberIDs = invalid
		rpt.InvalidMembersRemoved = len(invalid)

		// 空节点补 members（孤儿节点治理）：CleanFusionMapping 后可能有节点 members 变空，
		// 或 LLM 直接产出了空壳骨架节点。对这些无来源节点做伪 session 补全——把「已构建域图 +
		// 全部输入单元清单 + 空节点集合」一次性喂回，让 LLM 为每个空节点挑回应收纳的 members，
		// 脚本过滤为真实 04 id 后刷回。补上的 members 顺带提升覆盖率（下方 ComputeCoverage 反映），
		// 与后续「增量补未覆盖单元」互补：前者从空节点出发找成员，后者从未覆盖单元出发找归属。
		if empties := collectEmptyMemberNodes(res.Domains); len(empties) > 0 {
			rpt.EmptyNodesFound = len(empties)
			rpt.EmptyNodesFilled = e.backfillEmptyNodeMembers(ctx, res, docs, empties)
			log.Printf("[extract-coverage] 空节点补 members：发现 %d 个空节点，补上 %d 个",
				rpt.EmptyNodesFound, rpt.EmptyNodesFilled)
		}

		cov := ComputeCoverage(res.Domains, docs)
		rate := coverageRate(cov.CoveredUnits, rpt.InputUnits)
		if attempt == 1 {
			rpt.FirstPassCovered = cov.CoveredUnits
			rpt.FirstPassRate = rate
		}
		log.Printf("[extract-coverage] 第 %d/%d 轮提取：覆盖 %d/%d (%.2f%%)，合并重复节点 %d，剔除非法 member %d",
			attempt, maxExtractAttempts, cov.CoveredUnits, rpt.InputUnits, rate*100, rpt.DuplicateNodesMerged, rpt.InvalidMembersRemoved)

		// —— 第一道门控 P99：首轮达标直接放行 ——
		if rate >= coverageGateHigh {
			rpt.FinalCovered = cov.CoveredUnits
			rpt.FinalRate = rate
			rpt.Outcome = "pass_p99"
			log.Printf("[extract-coverage] ✓ 覆盖率 %.2f%% ≥ P99(%.0f%%)，直接放行", rate*100, coverageGateHigh*100)
			return res, rpt, nil
		}

		// —— 增量补融合：对未覆盖单元让 LLM 给「融入/新建/解释」操作 ——
		explained := e.supplementCoverage(ctx, res, docs, cov.UncoveredIDs, rpt)

		// 重算覆盖：把「已解释」单元视为已处理，剩余为真正遗漏。
		cov2 := ComputeCoverage(res.Domains, docs)
		trulyMissing := subtractIDs(cov2.UncoveredIDs, explained)
		finalRate := coverageRate(rpt.InputUnits-len(trulyMissing), rpt.InputUnits)
		rpt.FinalCovered = cov2.CoveredUnits
		rpt.FinalRate = finalRate
		rpt.TrulyMissing = trulyMissing
		log.Printf("[extract-coverage] 增量补后：融入 %d，新建 %d，解释放过 %d，真正遗漏 %d，覆盖率 %.2f%%",
			rpt.Assigned, rpt.Created, rpt.Explained, len(trulyMissing), finalRate*100)

		// —— 第二道门控 P95：增量补后达标进入悬空修正 ——
		if finalRate >= coverageGateFloor {
			rpt.Outcome = "pass_p95"
			log.Printf("[extract-coverage] ✓ 增量补后覆盖率 %.2f%% ≥ P95(%.0f%%)，进入悬空修正", finalRate*100, coverageGateFloor*100)
			return res, rpt, nil
		}

		// 未达 P95：若还有重提取额度，回退到提取阶段整轮重提取。
		if attempt < maxExtractAttempts {
			log.Printf("[extract-coverage] ⚠ 覆盖率 %.2f%% < P95(%.0f%%)，回退整轮重提取（第 %d 次）",
				finalRate*100, coverageGateFloor*100, attempt+1)
		}
	}

	// 兜底降级：整轮提取额度耗尽仍未达 P95，用当前最好结果继续（记告警）。
	rpt.Outcome = "degraded"
	log.Printf("[extract-coverage] ⚠ 整轮提取 %d 次后覆盖率仍为 %.2f%% < P95，降级：用当前结果继续（真正遗漏 %d 个）",
		maxExtractAttempts, rpt.FinalRate*100, len(rpt.TrulyMissing))
	return res, rpt, nil
}

// subtractIDs 返回 a 中不在 b 集合内的元素（保持 a 的顺序）。
// subtractIDs returns elements of a not present in b (stable order).
func subtractIDs(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	rm := make(map[string]bool, len(b))
	for _, x := range b {
		rm[x] = true
	}
	out := make([]string, 0, len(a))
	for _, x := range a {
		if !rm[x] {
			out = append(out, x)
		}
	}
	return out
}

// ——————————————————————————————————————————————————————————————————————————————
// 增量补融合 / Incremental coverage supplementation
// ——————————————————————————————————————————————————————————————————————————————

// supplementCoverage 把「已提取结构 + 未覆盖清单」喂回伪 session，请求 LLM 返回补融合操作
// （assignments 融入 / new_nodes 新建 / explanations 解释放过），规则化扫描后就地应用。
// 返回被「非空解释」放过的 04 id 列表（供 P95 覆盖率计算把它们视为已处理）。
//
// supplementCoverage asks the LLM (pseudo-session) to fold each uncovered unit into an
// existing node, create a new node, or explain why it should be skipped, then applies the
// ops in place and returns the explained ids.
func (e *Extractor) supplementCoverage(ctx context.Context, res *Result, docs []*dktypes.AnnotatedDocument, uncovered []string, rpt *CoverageReport) []string {
	if len(uncovered) == 0 {
		return nil
	}
	unitIDs := collectUnitIDs(docs)
	contentByID := buildUnitContentIndex(docs)

	var lastErr string
	for round := 1; round <= maxSupplementRounds; round++ {
		userMsg := buildSupplementPrompt(res.Domains, uncovered, contentByID, lastErr)
		resp, err := e.client.Complete(ctx, llm.CompleteRequest{
			System:    supplementSystemPrompt,
			User:      userMsg,
			MaxTokens: e.maxTokens,
		})
		if err != nil {
			log.Printf("[extract-coverage] 增量补第 %d/%d 轮 LLM 调用失败: %v", round, maxSupplementRounds, err)
			return nil
		}
		var sp supplementPatch
		if perr := json.Unmarshal([]byte(stripCodeFence(strings.TrimSpace(resp.Text))), &sp); perr != nil {
			lastErr = fmt.Sprintf("上一轮返回非法 JSON: %v", perr)
			log.Printf("[extract-coverage] 增量补第 %d/%d 轮 patch 解析失败，重试: %v", round, maxSupplementRounds, perr)
			continue
		}
		explained := applySupplement(res.Domains, &sp, unitIDs, rpt)
		return explained
	}
	log.Printf("[extract-coverage] 增量补 %d 轮均解析失败，跳过（未覆盖单元将计入真正遗漏）", maxSupplementRounds)
	return nil
}

// unitContent 是 04 输入单元的展示信息（供增量补 prompt 呈现未覆盖单元）。
type unitContent struct {
	Tag     dktypes.AnnotationTag
	Name    string
	Content string
}

// buildUnitContentIndex 构造 04 id → 展示信息 的索引（同 id 多来源取首个 content）。
// buildUnitContentIndex maps each 04 id to display info for the supplement prompt.
func buildUnitContentIndex(docs []*dktypes.AnnotatedDocument) map[string]unitContent {
	idx := make(map[string]unitContent)
	for _, d := range docs {
		if d == nil {
			continue
		}
		for _, it := range d.Items {
			if it.ID == "" {
				continue
			}
			if _, ok := idx[it.ID]; !ok {
				idx[it.ID] = unitContent{Tag: it.Tag, Name: it.Name, Content: it.Content}
			}
		}
	}
	return idx
}

// supplementPatch 是增量补融合 LLM 返回的顶层容器。
// supplementPatch is the top-level container of the supplementation response.
type supplementPatch struct {
	Assignments  []supplementAssign  `json:"assignments"`  // 把未覆盖单元融入某已有融合节点
	NewNodes     []supplementNew     `json:"new_nodes"`    // 为未覆盖单元新建融合节点
	Explanations []supplementExplain `json:"explanations"` // 说明某未覆盖单元为何不融合（放过）
}

type supplementAssign struct {
	MemberID      string `json:"member_id"`      // 未覆盖的 04 id
	DomainSlug    string `json:"domain_slug"`    // 目标节点定位
	SubdomainSlug string `json:"subdomain_slug"` //
	NodeID        string `json:"node_id"`        // 目标融合节点 id
}

type supplementNew struct {
	DomainSlug    string    `json:"domain_slug"`
	SubdomainSlug string    `json:"subdomain_slug"`
	Node          patchNode `json:"node"` // 复用 patchNode（含 members）
}

type supplementExplain struct {
	MemberID string `json:"member_id"`
	Reason   string `json:"reason"`
}

// applySupplement 就地应用增量补操作，更新 rpt 计数，返回被「非空解释」放过的 04 id。
// 规则：
//   - assignments：把 member_id（须为真实 04 id）加入定位到的融合节点 members（去重）；
//   - new_nodes：按 label 追加新融合节点，members 仅保留真实 04 id；
//   - explanations：reason 非空即记录并放过该单元（记入日志）。
//
// applySupplement applies the supplement ops in place and returns the explained ids.
func applySupplement(domains []Domain, sp *supplementPatch, unitIDs map[string]bool, rpt *CoverageReport) []string {
	// assignments：融入已有节点。
	for _, a := range sp.Assignments {
		if !unitIDs[a.MemberID] {
			continue // 非真实 04 id，跳过
		}
		n := findNodeInSubdomain(domains, a.DomainSlug, a.SubdomainSlug, a.NodeID)
		if n == nil {
			continue
		}
		if !containsString(n.Members, a.MemberID) {
			n.Members = append(n.Members, a.MemberID)
			rpt.Assigned++
		}
	}

	// new_nodes：新建融合节点。
	for _, nn := range sp.NewNodes {
		sd := findSubdomain(domains, nn.DomainSlug, nn.SubdomainSlug)
		if sd == nil {
			continue
		}
		if strings.TrimSpace(nn.Node.ID) == "" || strings.TrimSpace(nn.Node.Name) == "" {
			continue
		}
		members := filterValidMembers(nn.Node.Members, unitIDs)
		if len(members) == 0 {
			continue // 新建节点却没有有效成员，无意义，跳过
		}
		node := Node{
			ID:         nn.Node.ID,
			Name:       nn.Node.Name,
			Summary:    nn.Node.Summary,
			Confidence: nn.Node.Confidence,
			Members:    members,
		}
		if strings.EqualFold(nn.Node.Label, string(dktypes.LabelConcept)) {
			node.Label = string(dktypes.LabelConcept)
			sd.Concepts = append(sd.Concepts, node)
		} else {
			node.Label = string(dktypes.LabelEntity)
			sd.Entities = append(sd.Entities, node)
		}
		rpt.Created++
	}

	// explanations：非空理由 → 放过并记录。
	var explained []string
	for _, ex := range sp.Explanations {
		if ex.MemberID == "" || strings.TrimSpace(ex.Reason) == "" {
			continue
		}
		explained = append(explained, ex.MemberID)
		rpt.Explained++
		rpt.ExplainedNotes = append(rpt.ExplainedNotes, fmt.Sprintf("%s: %s", ex.MemberID, strings.TrimSpace(ex.Reason)))
		log.Printf("[extract-coverage] 放过未覆盖单元 %s，理由: %s", ex.MemberID, strings.TrimSpace(ex.Reason))
	}
	return explained
}

// findNodeInSubdomain 按 domain/subdomain slug + node id 定位融合节点指针（entities/concepts 均查）。
// findNodeInSubdomain locates a fusion node pointer by domain/subdomain slug and node id.
func findNodeInSubdomain(domains []Domain, domainSlug, subdomainSlug, nodeID string) *Node {
	sd := findSubdomain(domains, domainSlug, subdomainSlug)
	if sd == nil {
		return nil
	}
	for i := range sd.Entities {
		if sd.Entities[i].ID == nodeID {
			return &sd.Entities[i]
		}
	}
	for i := range sd.Concepts {
		if sd.Concepts[i].ID == nodeID {
			return &sd.Concepts[i]
		}
	}
	return nil
}

// filterValidMembers 仅保留真实存在于 unitIDs 的 04 id（去重）。
// filterValidMembers keeps only real 04 ids (de-duplicated).
func filterValidMembers(members []string, unitIDs map[string]bool) []string {
	var out []string
	seen := make(map[string]bool)
	for _, m := range members {
		if unitIDs[m] && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// containsString 判断切片是否含目标字符串。
func containsString(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

// ——————————————————————————————————————————————————————————————————————————————
// 增量补 prompt / Supplement prompt
// ——————————————————————————————————————————————————————————————————————————————

// supplementSystemPrompt 是增量补融合阶段的 system 提示。
const supplementSystemPrompt = `你是领域知识融合的补全助手。用户会给你一次融合归纳的产出（已有 domain/subdomain/融合节点）
以及一批「还没有被任何融合节点收纳」的输入单元。你的任务是为每个未覆盖单元决定归属，只返回增量操作，不重写正文。

严格要求：
1. 只返回一个 JSON 对象（json 格式），形如 {"assignments":[...],"new_nodes":[...],"explanations":[...]}，不要输出任何解释性文字或代码围栏。
2. 对每个未覆盖单元，从以下三种操作中选择一种：
   (a) assignments：该单元与某个已有融合节点语义等价 → 给出 member_id（未覆盖单元 id）与该节点的 domain_slug/subdomain_slug/node_id，把它融入该节点。
   (b) new_nodes：该单元自成一类、无合适的已有节点 → 新建融合节点，给出 domain_slug/subdomain_slug 定位与 node（含 id、label（Entity/Concept）、name、summary、confidence、members（至少含该单元 id））。
   (c) explanations：该单元确实不适合进入领域知识层（如过于琐碎/无复用价值）→ 给出 member_id 与非空 reason 说明理由。
3. member_id 必须逐字取自给定的「未覆盖单元」列表，不得编造或改写。
4. assignments 的 domain_slug/subdomain_slug/node_id 必须与给定结构中的真实节点一致。
5. 优先 assignments（并入已有节点），其次 new_nodes；只有在确实不该收录时才用 explanations。尽量让每个未覆盖单元都有归属。`

// buildSupplementPrompt 组装增量补融合的 user 消息（伪 session）。
func buildSupplementPrompt(domains []Domain, uncovered []string, contentByID map[string]unitContent, prevErr string) string {
	var sb strings.Builder
	sb.WriteString("你刚才对一批知识单元做了融合归纳，但下面这些输入单元还没有被任何融合节点收纳（漏融合）。请为它们补全归属。\n\n")

	// 一、已有 domain/subdomain/融合节点（id + name），供 assignments 定位。
	sb.WriteString("## 一、已有的融合结构（domain / subdomain / 融合节点 id + 名称）\n")
	writeStructureOutline(&sb, domains)
	sb.WriteString("\n")

	// 二、未覆盖单元清单（id + tag + name + content）。
	sb.WriteString("## 二、未覆盖的输入单元（请为每一个决定归属）\n")
	for _, id := range uncovered {
		uc := contentByID[id]
		fmt.Fprintf(&sb, "- id=%s [%s] %s: %s\n", id, uc.Tag, uc.Name, uc.Content)
	}
	sb.WriteString("\n")

	sb.WriteString("## 操作要求\n")
	sb.WriteString("对【二】中的每个未覆盖单元，返回 assignments（融入已有节点）/ new_nodes（新建节点）/ explanations（给非空理由放过）之一。只返回操作 JSON。\n")

	if strings.TrimSpace(prevErr) != "" {
		fmt.Fprintf(&sb, "\n## 上一轮的问题（请修正后重试）\n- %s\n", prevErr)
	}
	return sb.String()
}

// ——————————————————————————————————————————————————————————————————————————————
// 空节点补 members / Empty-node member backfill
// ——————————————————————————————————————————————————————————————————————————————

// backfillEmptyNodeMembers 对 members 为空的融合节点（孤儿节点）做伪 session 补全：
// 把「已构建域图 + 全部输入单元清单 + 空节点集合」一次性喂给 LLM，要求为每个空节点挑出
// 它应收纳的 04 id 作为 members；规则化过滤为真实 04 id、且仅补仍为空的节点后刷回。
// 返回成功补上 members（由空变非空）的节点数。解析失败原地重试，耗尽则跳过（保持空）。
//
// backfillEmptyNodeMembers asks the LLM (pseudo-session) to pick members for each empty
// (orphan) fusion node from the full unit list, then writes the valid picks back.
func (e *Extractor) backfillEmptyNodeMembers(ctx context.Context, res *Result, docs []*dktypes.AnnotatedDocument, empties []emptyNodeRef) int {
	unitIDs := collectUnitIDs(docs)
	var lastErr string
	for round := 1; round <= maxBackfillRounds; round++ {
		userMsg := buildBackfillPrompt(res.Domains, empties, docs, lastErr)
		resp, err := e.client.Complete(ctx, llm.CompleteRequest{
			System:    backfillSystemPrompt,
			User:      userMsg,
			MaxTokens: e.maxTokens,
		})
		if err != nil {
			log.Printf("[extract-coverage] 空节点补 members 第 %d/%d 轮 LLM 调用失败: %v", round, maxBackfillRounds, err)
			return 0
		}
		var bp backfillPatch
		if perr := json.Unmarshal([]byte(stripCodeFence(strings.TrimSpace(resp.Text))), &bp); perr != nil {
			lastErr = fmt.Sprintf("上一轮返回非法 JSON: %v", perr)
			log.Printf("[extract-coverage] 空节点补 members 第 %d/%d 轮解析失败，重试: %v", round, maxBackfillRounds, perr)
			continue
		}
		return applyBackfill(res.Domains, &bp, unitIDs)
	}
	log.Printf("[extract-coverage] 空节点补 members %d 轮均解析失败，跳过", maxBackfillRounds)
	return 0
}

// backfillPatch 是空节点补 members LLM 返回的顶层容器。
type backfillPatch struct {
	Fills []backfillFill `json:"fills"` // 每个空节点的 members 补全
}

// backfillFill 是对单个空节点的 members 补全操作。
type backfillFill struct {
	DomainSlug    string   `json:"domain_slug"`
	SubdomainSlug string   `json:"subdomain_slug"`
	NodeID        string   `json:"node_id"`
	Members       []string `json:"members"` // 该节点应收纳的 04 id 列表
}

// applyBackfill 就地把补的 members 写回对应空节点，返回成功补上的节点数。
// 规则：仅定位得到、且当前 members 仍为空的节点才补（防止误改已有节点）；members 过滤为
// 真实 04 id 并去重；过滤后仍为空则不补（视为未补上）。
//
// applyBackfill writes the picked members back to still-empty nodes, returning the count filled.
func applyBackfill(domains []Domain, bp *backfillPatch, unitIDs map[string]bool) int {
	filled := 0
	for _, f := range bp.Fills {
		n := findNodeInSubdomain(domains, f.DomainSlug, f.SubdomainSlug, f.NodeID)
		if n == nil || len(n.Members) > 0 {
			continue // 节点不存在，或已非空（只补真正空的孤儿节点）
		}
		members := filterValidMembers(f.Members, unitIDs)
		if len(members) == 0 {
			continue
		}
		n.Members = members
		filled++
	}
	return filled
}

// writeStructureOutline 把已构建的域图（domain/subdomain/融合节点 id+name）写成缩进大纲，
// 供 supplement / backfill 两个伪 session 复用（DRY）。
//
// writeStructureOutline renders the domain-graph outline shared by supplement and backfill.
func writeStructureOutline(sb *strings.Builder, domains []Domain) {
	for i := range domains {
		d := &domains[i]
		fmt.Fprintf(sb, "- domain「%s」(slug: %s)\n", d.Name, d.Slug)
		for j := range d.Subdomains {
			sd := &d.Subdomains[j]
			fmt.Fprintf(sb, "  - subdomain「%s」(slug: %s)\n", sd.Name, sd.Slug)
			for _, n := range sd.Entities {
				fmt.Fprintf(sb, "    - [Entity] node_id=%s: %s\n", n.ID, n.Name)
			}
			for _, n := range sd.Concepts {
				fmt.Fprintf(sb, "    - [Concept] node_id=%s: %s\n", n.ID, n.Name)
			}
		}
	}
}

// backfillSystemPrompt 是空节点补 members 阶段的 system 提示。
const backfillSystemPrompt = `你是领域知识融合的补全助手。用户会给你一次融合归纳的产出（已有 domain/subdomain/融合节点）、
全部可用的输入单元清单，以及一批「还没有绑定任何来源单元（members 为空）」的融合节点。
这些空节点在融合时建立了名称与类别，却漏了标注来源。你的任务是为每个空节点，从输入单元清单中挑出
语义上应当归属于它（由它收纳）的输入单元 id，作为它的 members。只返回绑定操作，不重写正文。

严格要求：
1. 只返回一个 JSON 对象（json 格式），形如 {"fills":[{"domain_slug":"...","subdomain_slug":"...","node_id":"...","members":["<04 id>", ...]}]}，不要输出任何解释性文字或代码围栏。
2. domain_slug/subdomain_slug/node_id 必须与给定「空节点列表」中的条目逐字一致。
3. members 里的 id 必须逐字取自给定「输入单元清单」，不得编造或改写；只填真正语义归属于该节点的单元。
4. 尽量为每个空节点补出至少一个 member；确实找不到任何归属单元的空节点可以不返回它（留待后续处理）。`

// buildBackfillPrompt 组装空节点补 members 的 user 消息（伪 session）。
func buildBackfillPrompt(domains []Domain, empties []emptyNodeRef, docs []*dktypes.AnnotatedDocument, prevErr string) string {
	var sb strings.Builder
	sb.WriteString("你刚才做了融合归纳，但下面这些融合节点还没有绑定任何来源单元（members 为空）。\n" +
		"请从「全部输入单元」中，为每个空节点挑出语义上应归属于它的单元 id 作为 members。\n\n")

	// 一、已有域图（供理解上下文、区分同名节点）。
	sb.WriteString("## 一、已有的融合结构（domain / subdomain / 融合节点 id + 名称）\n")
	writeStructureOutline(&sb, domains)
	sb.WriteString("\n")

	// 二、待补 members 的空节点。
	sb.WriteString("## 二、待补 members 的空节点（请为每个补出应收纳的 members）\n")
	for _, en := range empties {
		fmt.Fprintf(&sb, "- domain_slug=%s subdomain_slug=%s node_id=%s [%s]: %s",
			en.DomainSlug, en.SubdomainSlug, en.Node.ID, en.Node.Label, en.Node.Name)
		if s := strings.TrimSpace(en.Node.Summary); s != "" {
			fmt.Fprintf(&sb, "（摘要：%s）", s)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	// 三、全部可用输入单元清单（去重、稳定顺序）。
	sb.WriteString("## 三、可用的输入单元清单（从中为空节点挑选 members）\n")
	seen := make(map[string]bool)
	for _, d := range docs {
		if d == nil {
			continue
		}
		for _, it := range d.Items {
			if it.ID == "" || seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			fmt.Fprintf(&sb, "- id=%s [%s] %s: %s\n", it.ID, it.Tag, it.Name, it.Content)
		}
	}

	sb.WriteString("\n只返回 fills JSON。\n")
	if strings.TrimSpace(prevErr) != "" {
		fmt.Fprintf(&sb, "\n## 上一轮的问题（请修正后重试）\n- %s\n", prevErr)
	}
	return sb.String()
}
