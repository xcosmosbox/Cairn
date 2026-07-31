// Package extract 的本文件实现「悬空边修正」阶段：在 05 落盘后、ingest 前，
// 扫描所有 relation 的端点是否在已声明的 entity/concept 集合内；对悬空 relation
// 先清洗（从 sd.Relations 移除），再通过伪 session（把 05 结构 + 悬空列表 + 04↔05
// 差集喂回 LLM）请求 LLM 返回增量 patch（补声明实体 / 修正端点 / 确认丢弃），
// 应用 patch 后重新校验；仍悬空的最终丢弃。修正耗尽降级，不重跑 extract。
//
// 设计要点（与用户确认的方案）：
//   - C2 伪 session：不改 llm.Client 接口，把"上轮产物 + 悬空 + 差集"拼进 User 字段
//   - patch 协议：add_entity/add_concept/restore_relation/fix_relation/drop_relation
//   - 定位用 domain_slug/subdomain_slug（05 里 slug 是确定值，无歧义）
//   - 04↔05 name 归一化（trim+lowercase）差集 = 可能被漏掉/改名的实体
//   - maxRepairRounds=2，耗尽降级（清洗后 05 继续 ingest），不重跑 extract
//
// This file implements the dangling-edge repair stage: after 05 is persisted and
// before ingest, scan relations for endpoints missing from declared entities/concepts;
// clean dangling relations, then ask the LLM (via a pseudo-session that feeds back the
// 05 structure + dangling list + 04↔05 diff) to return incremental patches; apply and
// re-validate; still-dangling ones are finally dropped. Exhaustion degrades, no extract rerun.
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/llm"
)

// maxRepairRounds 是悬空边修正阶段的原地重试上限。修正比 extract 轻量，2 轮足够。
// maxRepairRounds is the in-place retry cap for the repair stage.
const maxRepairRounds = 2

// ——————————————————————————————————————————————————————————————————————————————
// 悬空扫描 / Dangling scan
// ——————————————————————————————————————————————————————————————————————————————

// danglingRelation 是一条悬空 relation 及其定位信息与缺失端点标记。
// danglingRelation is a relation whose source/target is not a declared node.
type danglingRelation struct {
	DomainSlug    string
	SubdomainSlug string
	Relation      Relation
	MissingSource bool // true 表示 source 端点未声明
	MissingTarget bool // true 表示 target 端点未声明
}

// scanDangling 扫描所有 subdomain 的 relations，返回端点未声明的悬空 relation。
// 端点存在性 = source/target 是否出现在同 subdomain 的 entities+concepts 的 ID 集合内。
//
// scanDangling scans all subdomain relations and returns those with undeclared endpoints.
func scanDangling(domains []Domain) []danglingRelation {
	var out []danglingRelation
	for i := range domains {
		d := &domains[i]
		for j := range d.Subdomains {
			sd := &d.Subdomains[j]
			declared := make(map[string]bool, len(sd.Entities)+len(sd.Concepts))
			for _, n := range sd.Entities {
				declared[n.ID] = true
			}
			for _, n := range sd.Concepts {
				declared[n.ID] = true
			}
			for _, r := range sd.Relations {
				ms := !declared[r.Source]
				mt := !declared[r.Target]
				if ms || mt {
					out = append(out, danglingRelation{
						DomainSlug:    d.Slug,
						SubdomainSlug: sd.Slug,
						Relation:      r,
						MissingSource: ms,
						MissingTarget: mt,
					})
				}
			}
		}
	}
	return out
}

// cleanDangling 从 sd.Relations 中移除所有悬空 relation（端点未声明）。
// 返回移除的条数。清洗后的 05 即便修正失败也可安全进入 ingest（无悬空）。
//
// cleanDangling removes dangling relations from each subdomain in-place.
func cleanDangling(domains []Domain) int {
	removed := 0
	for i := range domains {
		d := &domains[i]
		for j := range d.Subdomains {
			sd := &d.Subdomains[j]
			declared := make(map[string]bool, len(sd.Entities)+len(sd.Concepts))
			for _, n := range sd.Entities {
				declared[n.ID] = true
			}
			for _, n := range sd.Concepts {
				declared[n.ID] = true
			}
			kept := sd.Relations[:0]
			for _, r := range sd.Relations {
				if declared[r.Source] && declared[r.Target] {
					kept = append(kept, r)
				} else {
					removed++
				}
			}
			sd.Relations = kept
		}
	}
	return removed
}

// ——————————————————————————————————————————————————————————————————————————————
// 04↔05 差集 / Missing-from-04 diff
// ——————————————————————————————————————————————————————————————————————————————

// missingItem 是未被任何融合节点 members 覆盖的 04 输入单元（漏融合）。
// missingItem is a 04 input unit not covered by any fusion node's members.
type missingItem struct {
	ID      string
	Tag     dktypes.AnnotationTag
	Name    string
	Content string
}

// computeMissing 计算未被任何融合节点 members 覆盖的 04 输入单元。
// 重构第二期：从「04↔05 name 差集」改为「04 id 未覆盖」——融合节点 name 是融合命名、
// 与 04 name 不再一致，唯有 04 id 的覆盖关系才是可靠对齐依据。供修正 prompt 提示 LLM
// 「这些输入单元还没被融合进任何节点」。
//
// computeMissing returns 04 input units not covered by any fusion node's members.
func computeMissing(docs []*dktypes.AnnotatedDocument, domains []Domain) []missingItem {
	unitIDs := collectUnitIDs(docs)
	covered := buildCovered(domains, unitIDs)
	seen := make(map[string]bool)
	var missing []missingItem
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		for _, it := range doc.Items {
			if it.ID == "" || covered[it.ID] || seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			missing = append(missing, missingItem{ID: it.ID, Tag: it.Tag, Name: it.Name, Content: it.Content})
		}
	}
	return missing
}

// ——————————————————————————————————————————————————————————————————————————————
// Patch 数据结构 / Patch structures
// ——————————————————————————————————————————————————————————————————————————————

// patchSet 是 LLM 修正调用返回的顶层容器。
// patchSet is the top-level container of the LLM repair response.
type patchSet struct {
	Patches []patch `json:"patches"`
}

// patch 是一条增量修正指令。Action 决定其余字段的含义。
// patch is one incremental repair instruction.
type patch struct {
	Action string `json:"action"` // add_entity | add_concept | restore_relation | fix_relation | drop_relation

	// add_entity / add_concept 用：要补声明的节点。
	Node patchNode `json:"node,omitempty"`

	// restore_relation / drop_relation / fix_relation 用：定位与关系内容。
	DomainSlug    string `json:"domain_slug,omitempty"`
	SubdomainSlug string `json:"subdomain_slug,omitempty"`

	// restore_relation 用：要恢复的关系。
	Relation Relation `json:"relation,omitempty"`

	// fix_relation 用：原端点 + 修正后端点。
	OriginalSource string `json:"original_source,omitempty"`
	OriginalTarget string `json:"original_target,omitempty"`
	FixedSource    string `json:"fixed_source,omitempty"`
	FixedTarget    string `json:"fixed_target,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Description    string `json:"description,omitempty"`

	// drop_relation 用：定位原关系（source+target+kind 三元组）。
	Source string `json:"source,omitempty"`
	Target string `json:"target,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// patchNode 是 patch 里 add_entity/add_concept 的节点载体。
// patchNode carries a node to add in add_entity/add_concept patches.
type patchNode struct {
	ID           string   `json:"id"`
	Label        string   `json:"label,omitempty"` // "Entity" | "Concept"；悬空修正用 action 区分、忽略此字段，增量补融合(new_nodes)用它决定归入 entities/concepts
	Name         string   `json:"name"`
	Summary      string   `json:"summary"`
	Description  string   `json:"description"`
	Confidence   float64  `json:"confidence"`
	SourceSkills []string `json:"source_skills"`
	// Members 是补声明融合节点时其融合来源的 04 id 列表（重构第二期 B）：
	// 修正阶段补的节点同样要带 members，才能被 backfillSourceSkills 倒推 source_skills 并计入覆盖。
	// Members carries the 04 ids fused into a node added during repair (phase 2 part B).
	Members []string `json:"members"`
}

// ——————————————————————————————————————————————————————————————————————————————
// Patch 应用 / Patch application
// ——————————————————————————————————————————————————————————————————————————————

// applyPatches 把 patchSet 增量应用到 domains 上（就地修改）。
// 返回 (applied, errs)：applied = 成功应用的条数；errs = 每条应用错误的描述。
// 应用规则：
//   - add_entity/add_concept：按 domain_slug/subdomain_slug 定位，追加到 entities/concepts
//   - restore_relation：定位 subdomain，追加到 relations（端点应已被 add 补齐）
//   - fix_relation：定位 subdomain，把 original source/target 的 relation 改成 fixed
//   - drop_relation：无需操作（悬空已在清洗阶段移除，drop 仅记录确认）
//
// applyPatches applies a patchSet to domains in place.
func applyPatches(domains []Domain, patches []patch) (int, []string) {
	applied := 0
	var errs []string

	for i, p := range patches {
		switch p.Action {
		case "add_entity", "add_concept":
			sd := findSubdomain(domains, p.DomainSlug, p.SubdomainSlug)
			if sd == nil {
				errs = append(errs, fmt.Sprintf("patch#%d add: subdomain %s/%s not found", i, p.DomainSlug, p.SubdomainSlug))
				continue
			}
			if strings.TrimSpace(p.Node.ID) == "" || strings.TrimSpace(p.Node.Name) == "" {
				errs = append(errs, fmt.Sprintf("patch#%d add: node id/name empty", i))
				continue
			}
			n := Node{
				ID:          p.Node.ID,
				Name:        p.Node.Name,
				Summary:     p.Node.Summary,
				Description: p.Node.Description,
				Confidence:  p.Node.Confidence,
				SourceSkills: p.Node.SourceSkills,
				Members:     p.Node.Members,
			}
			if p.Action == "add_entity" {
				n.Label = string(dktypes.LabelEntity)
				sd.Entities = append(sd.Entities, n)
			} else {
				n.Label = string(dktypes.LabelConcept)
				sd.Concepts = append(sd.Concepts, n)
			}
			applied++

		case "restore_relation":
			sd := findSubdomain(domains, p.DomainSlug, p.SubdomainSlug)
			if sd == nil {
				errs = append(errs, fmt.Sprintf("patch#%d restore: subdomain not found", i))
				continue
			}
			if !validRelationKinds[p.Relation.Kind] {
				errs = append(errs, fmt.Sprintf("patch#%d restore: invalid kind %q", i, p.Relation.Kind))
				continue
			}
			sd.Relations = append(sd.Relations, p.Relation)
			applied++

		case "fix_relation":
			sd := findSubdomain(domains, p.DomainSlug, p.SubdomainSlug)
			if sd == nil {
				errs = append(errs, fmt.Sprintf("patch#%d fix: subdomain not found", i))
				continue
			}
			fixed := false
			for k := range sd.Relations {
				r := &sd.Relations[k]
				if r.Source == p.OriginalSource && r.Target == p.OriginalTarget && r.Kind == p.Kind {
					if p.FixedSource != "" {
						r.Source = p.FixedSource
					}
					if p.FixedTarget != "" {
						r.Target = p.FixedTarget
					}
					if p.Description != "" {
						r.Description = p.Description
					}
					fixed = true
					break
				}
			}
			if !fixed {
				errs = append(errs, fmt.Sprintf("patch#%d fix: relation %s→%s(%s) not found", i, p.OriginalSource, p.OriginalTarget, p.Kind))
				continue
			}
			applied++

		case "drop_relation":
			// 悬空 relation 已在 cleanDangling 阶段移除；drop 仅是 LLM 的确认，无需操作。
			applied++

		default:
			errs = append(errs, fmt.Sprintf("patch#%d: unknown action %q", i, p.Action))
		}
	}
	return applied, errs
}

// findSubdomain 按 domain/subdomain slug 定位 subdomain 指针。
// findSubdomain locates a subdomain by domain/subdomain slug.
func findSubdomain(domains []Domain, domainSlug, subdomainSlug string) *Subdomain {
	for i := range domains {
		if domains[i].Slug != domainSlug {
			continue
		}
		for j := range domains[i].Subdomains {
			if domains[i].Subdomains[j].Slug == subdomainSlug {
				return &domains[i].Subdomains[j]
			}
		}
	}
	return nil
}

// ——————————————————————————————————————————————————————————————————————————————
// RepairReport / 修正报告
// ——————————————————————————————————————————————————————————————————————————————

// RepairReport 是「融合映射校准 + 悬空边修正」阶段的结果摘要（可观测，写盘到 05c）。
// RepairReport summarizes the fusion-calibration + dangling-repair stage (written to 05c).
type RepairReport struct {
	DanglingFound  int      // 扫描到的悬空 relation 总数
	Cleaned        int      // 清洗阶段移除的条数（= DanglingFound，清洗是第一步）
	PatchesApplied int      // 成功应用的 patch 条数
	PatchErrors    []string // 应用失败的 patch 错误描述
	Rounds         int      // 实际执行的修正轮数
	StillDangling  int      // patch 应用后重新扫描仍悬空的条数（最终丢弃）
	Degraded       bool     // 是否降级（修正耗尽，用清洗后 05 继续 ingest）
	MissingFrom04  int      // 修正提示用：未被融合覆盖的 04 单元数（漏融合，供 prompt 提示）
}

// ——————————————————————————————————————————————————————————————————————————————
// RepairDanglingRelations — 悬空边修正主入口
// ——————————————————————————————————————————————————————————————————————————————

// RepairDanglingRelations 在覆盖治理之后、ingest 之前，对 res.Domains 做「悬空边修正」
// （就地修改 res）。融合映射校准（合并重复节点 / 剔非法 member）与覆盖闭环已由上游
// ExtractWithCoverage 完成，本阶段只专注 relations 的悬空端点（关注点分离）：
//   1. 扫描悬空 relation（端点不在已声明融合节点内）→ 清洗 → 伪 session LLM 增量 patch 修正（耗尽降级）；
//   2. 修正可能补声明了融合节点 → 重新倒推 source_skills（含清空 description，第二期契约）。
//
// docs 是刷了 04 id、同 skill 合并后的标注文档（AssignIDsAndMerge 产物）。client 不可为 nil。
// 返回 *RepairReport，不返回 error（修正失败不阻断流水线，降级继续）。
//
// RepairDanglingRelations calibrates the fusion mapping and repairs dangling relations
// in res in place (phase 2).
func (e *Extractor) RepairDanglingRelations(ctx context.Context, res *Result, docs []*dktypes.AnnotatedDocument) *RepairReport {
	rpt := &RepairReport{}
	if res == nil || len(res.Domains) == 0 {
		return rpt
	}

	// 融合映射校准（合并重复节点 / 剔非法 member）与覆盖治理已由上游 ExtractWithCoverage 完成，
	// 本阶段专注悬空边：扫描 → 清洗 → 伪 session LLM 增量 patch 修正。
	dangling := scanDangling(res.Domains)
	rpt.DanglingFound = len(dangling)
	if len(dangling) > 0 {
		rpt.Cleaned = cleanDangling(res.Domains)
		rpt.MissingFrom04 = len(computeMissing(docs, res.Domains))
		log.Printf("[extract-repair] ▶ 扫描到 %d 条悬空 relation，已清洗；开始 LLM 修正（最多 %d 轮）",
			rpt.DanglingFound, maxRepairRounds)
		e.runRepairLoop(ctx, res, docs, dangling, rpt)
	} else {
		log.Printf("[extract-repair] 未发现悬空 relation")
	}

	// 修正可能补声明了融合节点 → 重新倒推 source_skills（含清空 description，第二期契约）。
	backfillSourceSkills(res.Domains, buildSkillIndex(docs))
	return rpt
}

// runRepairLoop 执行悬空边的伪 session LLM 增量修正循环（就地修改 res，更新 rpt）。
// 通过（无残留悬空）即返回；耗尽则降级（用清洗后 05 继续 ingest）。
//
// runRepairLoop runs the pseudo-session incremental repair loop for dangling relations.
func (e *Extractor) runRepairLoop(ctx context.Context, res *Result, docs []*dktypes.AnnotatedDocument, dangling []danglingRelation, rpt *RepairReport) {
	var lastErrs []string
	for round := 1; round <= maxRepairRounds; round++ {
		rpt.Rounds = round

		userMsg := buildRepairPrompt(res.Domains, dangling, docs, lastErrs)
		resp, err := e.client.Complete(ctx, llm.CompleteRequest{
			System:    repairSystemPrompt,
			User:      userMsg,
			MaxTokens: e.maxTokens,
		})
		if err != nil {
			log.Printf("[extract-repair] 第 %d/%d 轮 LLM 调用失败，降级: %v", round, maxRepairRounds, err)
			rpt.Degraded = true
			return
		}

		var ps patchSet
		if perr := json.Unmarshal([]byte(stripCodeFence(strings.TrimSpace(resp.Text))), &ps); perr != nil {
			lastErrs = []string{fmt.Sprintf("第 %d 轮 patch JSON 解析失败: %v", round, perr)}
			log.Printf("[extract-repair] 第 %d/%d 轮 patch 解析失败，重试: %v", round, maxRepairRounds, perr)
			continue
		}

		applied, errs := applyPatches(res.Domains, ps.Patches)
		rpt.PatchesApplied += applied
		rpt.PatchErrors = append(rpt.PatchErrors, errs...)

		if verr := validateExtractSchema(res.Domains); verr != nil {
			lastErrs = []string{fmt.Sprintf("第 %d 轮 patch 应用后 schema 校验失败: %v", round, verr)}
			log.Printf("[extract-repair] 第 %d/%d 轮 schema 校验失败，重试: %v", round, maxRepairRounds, verr)
			continue
		}

		stillDangling := scanDangling(res.Domains)
		if len(stillDangling) == 0 {
			log.Printf("[extract-repair] ✓ 第 %d 轮修正成功：应用 %d 条 patch，无残留悬空", round, applied)
			return
		}

		rpt.StillDangling = len(stillDangling)
		lastErrs = []string{fmt.Sprintf("第 %d 轮应用后仍有 %d 条悬空 relation", round, len(stillDangling))}
		log.Printf("[extract-repair] 第 %d/%d 轮后仍有 %d 条悬空，重试", round, maxRepairRounds, len(stillDangling))
		cleanDangling(res.Domains) // 清洗残留，下一轮基于干净状态
		dangling = stillDangling
	}

	rpt.Degraded = true
	log.Printf("[extract-repair] ⚠ 修正 %d 轮耗尽，降级：用清洗后 05 继续 ingest（残留悬空 %d 条已丢弃）",
		maxRepairRounds, rpt.StillDangling)
}

// ——————————————————————————————————————————————————————————————————————————————
// 修正 prompt 组装 / Repair prompt assembly
// ——————————————————————————————————————————————————————————————————————————————

// repairSystemPrompt 是悬空边修正阶段的 system 提示。
// repairSystemPrompt is the system instruction for the repair stage.
const repairSystemPrompt = `你是领域知识图谱的修正助手。用户会给你一次领域归纳的产出（05 阶段）及其悬空 relation 列表，
以及上游标注阶段（04）中存在但 05 未出现的 entity/concept。你的任务是判断每条悬空 relation 该如何处理，
只返回增量 patch 指令（不返回完整结构）。

严格要求：
1. 只返回一个 JSON 对象（json 格式），形如 {"patches":[...]}，不要输出任何解释性文字或代码围栏。
2. 对每条悬空 relation，从以下四种 action 中选择一种：
   (a) add_entity：source 是漏声明的真实实体，需补声明（优先用 04 差集中匹配项的 name/content），
       随后对该 relation 用 restore_relation 恢复。
   (b) add_concept：同上，但补声明的是 concept。
   (c) fix_relation：source/target 名字写错了，实际指向 05 中已存在的某 entity/concept 的 id。
       给出 original_source/original_target/kind 定位原 relation，fixed_source/fixed_target 给出修正后的 id。
   (d) drop_relation：source 是笔误或无效，relation 应丢弃。给出 source/target/kind 定位与 reason。
3. add_entity/add_concept 的 node 必须有非空 id（kebab-case）与 name，并给出 members
   （该融合节点融合的 04 输入单元 id 列表，逐字取自【三】中真实存在的 id，不得编造）。
   node 无需 description 与 source_skills（前者由后续阶段生成，后者由 members 自动倒推）。
4. restore_relation 的 relation 必须与原悬空 relation 的 source/target/kind 对应（source 用补声明后的 id）。
5. patch 中的 domain_slug/subdomain_slug 用于定位，必须与给出的结构一致。`

// buildRepairPrompt 组装发给 LLM 的修正 user 消息（伪 session：含 05 产物 + 悬空 + 差集）。
// buildRepairPrompt assembles the repair user message (pseudo-session).
func buildRepairPrompt(domains []Domain, dangling []danglingRelation, docs []*dktypes.AnnotatedDocument, prevErrs []string) string {
	var sb strings.Builder

	sb.WriteString("你刚才对一批 reference 文档做了领域归纳（05 阶段），产出了 domain/subdomain/entity/concept/relation。\n")
	sb.WriteString("其中部分 relation 的端点不在已声明的 entity/concept 列表里（悬空）。请修正。\n\n")

	// 一、05 的 domain/subdomain 结构（不含其下 entity/concept，仅结构详情）。
	sb.WriteString("## 一、你归纳的完整 domain/subdomain 结构（不含其下 entity/concept，仅结构详情）\n")
	for i := range domains {
		d := &domains[i]
		fmt.Fprintf(&sb, "- domain「%s」(slug: %s)\n", d.Name, d.Slug)
		if d.Summary != "" {
			fmt.Fprintf(&sb, "  summary: %s\n", d.Summary)
		}
		if len(d.SourceSkills) > 0 {
			fmt.Fprintf(&sb, "  source_skills: %s\n", strings.Join(d.SourceSkills, ", "))
		}
		for j := range d.Subdomains {
			sd := &d.Subdomains[j]
			fmt.Fprintf(&sb, "  - subdomain「%s」(slug: %s)\n", sd.Name, sd.Slug)
			if sd.Summary != "" {
				fmt.Fprintf(&sb, "    summary: %s\n", sd.Summary)
			}
		}
	}
	sb.WriteString("\n")

	// 二、悬空 relation 列表。
	sb.WriteString("## 二、悬空 relation 列表（每条标明缺哪端）\n")
	for i, dr := range dangling {
		miss := ""
		if dr.MissingSource && dr.MissingTarget {
			miss = "缺 source+target"
		} else if dr.MissingSource {
			miss = "缺 source"
		} else {
			miss = "缺 target"
		}
		fmt.Fprintf(&sb, "%d. [%s/%s] (%s) %s --%s--> %s\n",
			i+1, dr.DomainSlug, dr.SubdomainSlug, miss,
			dr.Relation.Source, dr.Relation.Kind, dr.Relation.Target)
	}
	sb.WriteString("\n")

	// 三、还未被任何融合节点覆盖的 04 输入单元（可用作补声明节点的 members 来源）。
	missing := computeMissing(docs, domains)
	if len(missing) > 0 {
		sb.WriteString("## 三、还未被任何融合节点覆盖的 04 输入单元（可用作补声明节点的 members 来源）\n")
		for _, m := range missing {
			fmt.Fprintf(&sb, "- id=%s [%s] %s：%s\n", m.ID, m.Tag, m.Name, m.Content)
		}
		sb.WriteString("\n")
	}

	// 四、修正要求。
	sb.WriteString("## 修正要求\n")
	sb.WriteString("对【二】中的每条悬空 relation，判断属于以下哪种并返回 patch：\n")
	sb.WriteString("(a) source 是漏声明的真实融合节点，对应【三】中一个或多个 04 输入单元 → add_entity(给 id/name/summary + members 引用这些 04 id) + restore_relation\n")
	sb.WriteString("(b) source 名字写错了，实际指向 05 中已存在的某融合节点 id → fix_relation\n")
	sb.WriteString("(c) source 是笔误/无效 → drop_relation(给 reason)\n")
	sb.WriteString("只返回 patch JSON。\n")

	// 上一轮错误反馈（伪 session 的"纠错"上下文）。
	if len(prevErrs) > 0 {
		sb.WriteString("\n## 上一轮修正的问题（请修正后重试）\n")
		for _, e := range prevErrs {
			fmt.Fprintf(&sb, "- %s\n", e)
		}
	}

	return sb.String()
}
