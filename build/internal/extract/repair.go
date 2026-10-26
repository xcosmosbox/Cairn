// Unresolved relation endpoints fail locally; no model repair is performed.
package extract

import (
	"context"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"log"
)

type danglingRelation struct {
	DomainSlug    string
	SubdomainSlug string
	Relation      Relation
	MissingSource bool // true 表示 source 端点未声明
	MissingTarget bool // true 表示 target 端点未声明
	SourceStatus  string
	TargetStatus  string
}

// scanDangling 扫描所有 subdomain 的 relations，返回端点未声明的悬空 relation。
// 端点存在性 = 局部唯一优先，否则全局唯一；跨子域仅保留候选，不代表语义验证通过。
//
// scanDangling scans all subdomain relations and returns those with undeclared endpoints.
func scanDangling(domains []Domain) []danglingRelation {
	index := indexEndpoints(domains)
	var out []danglingRelation
	for i := range domains {
		d := &domains[i]
		for j := range d.Subdomains {
			sd := &d.Subdomains[j]
			for _, r := range sd.Relations {
				_, _, ss, ts := index.relation(endpointScope{d.Slug, sd.Slug}, r)
				if ss != "resolved" || ts != "resolved" {
					out = append(out, danglingRelation{DomainSlug: d.Slug, SubdomainSlug: sd.Slug,
						Relation: r, MissingSource: ss != "resolved", MissingTarget: ts != "resolved", SourceStatus: ss, TargetStatus: ts})
				}
			}
		}
	}
	return out
}

// cleanDangling 保留可唯一解析的跨子域候选；它们还须通过独立原文验证才会进入 UUID/入库。
// Endpoint resolution preserves candidates, not semantic approval.
func cleanDangling(domains []Domain) int {
	index := indexEndpoints(domains)
	removed := 0
	for i := range domains {
		d := &domains[i]
		for j := range d.Subdomains {
			sd := &d.Subdomains[j]
			kept := sd.Relations[:0]
			for _, r := range sd.Relations {
				_, _, ss, ts := index.relation(endpointScope{d.Slug, sd.Slug}, r)
				if ss == "resolved" && ts == "resolved" {
					kept = append(kept, r)
				} else {
					log.Printf("[extract-repair] 拒绝端点: %s/%s %s --%s--> %s (source=%s target=%s)", d.Slug, sd.Slug, r.Source, r.Kind, r.Target, ss, ts)
					removed++
				}
			}
			sd.Relations = kept
		}
	}
	return removed
}

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

type RepairReport struct {
	EndpointFailures []danglingRelation // 原始 missing/ambiguous 明细，不能只报计数 / original endpoint dispositions
	DanglingFound    int                // 扫描到的悬空 relation 总数
	Cleaned          int                // 清洗阶段移除的条数（= DanglingFound，清洗是第一步）
	PatchesApplied   int                // 成功应用的 patch 条数
	PatchErrors      []string           // 应用失败的 patch 错误描述
	Rounds           int                // 实际执行的修正轮数
	StillDangling    int                // patch 应用后重新扫描仍悬空的条数（最终丢弃）
	Degraded         bool               // 是否降级（修正耗尽，用清洗后 05 继续 ingest）
	MissingFrom04    int                // 修正提示用：未被融合覆盖的 04 单元数（漏融合，供 prompt 提示）
}

// ——————————————————————————————————————————————————————————————————————————————
// RepairDanglingRelations — 悬空边修正主入口
// ——————————————————————————————————————————————————————————————————————————————

// RepairDanglingRelations 在覆盖治理之后、ingest 之前，对 res.Domains 做「悬空边修正」
// （就地修改 res）。融合映射校准（合并重复节点 / 剔非法 member）与覆盖闭环已由上游
// ExtractWithCoverage 完成，本阶段只专注 relations 的悬空端点（关注点分离）：
//  1. 扫描悬空 relation（端点不在已声明融合节点内）→ 清洗 → 伪 session LLM 增量 patch 修正（耗尽降级）；
//  2. 修正可能补声明了融合节点 → 重新倒推 source_skills（含清空 description，第二期契约）。
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
	rpt.EndpointFailures = append(rpt.EndpointFailures, dangling...)
	if len(dangling) > 0 {
		rpt.Cleaned = cleanDangling(res.Domains)
		rpt.StillDangling = rpt.DanglingFound
		rpt.Degraded = true
		log.Printf("[extract] dropped %d relations with unresolved endpoints; no paid repair", rpt.Cleaned)
	}

	// 修正可能补声明了融合节点 → 重新倒推 source_skills（含清空 description，第二期契约）。
	backfillSourceSkills(res.Domains, buildSkillIndex(docs))
	return rpt
}
