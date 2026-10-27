// 缺原文的兼容入口保留未决端点；来源感知的有限修复在 ProcessRelations。
// Legacy inspection keeps unresolved candidates available for source-aware repair.
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
	Cleaned          int                // 兼容入口不删除候选，保持为零 / no deletion without originals
	PatchesApplied   int                // 成功应用的 patch 条数
	PatchErrors      []string           // 应用失败的 patch 错误描述
	Rounds           int                // 实际执行的修正轮数
	StillDangling    int                // patch 应用后重新扫描仍悬空的条数（最终丢弃）
	Degraded         bool               // 缺少原文时明确不可用 / missing original evidence
	MissingFrom04    int                // 修正提示用：未被融合覆盖的 04 单元数（漏融合，供 prompt 提示）
}

// ——————————————————————————————————————————————————————————————————————————————
// RepairDanglingRelations — 悬空边修正主入口
// ——————————————————————————————————————————————————————————————————————————————

// RepairDanglingRelations 的旧签名缺少冻结原文，不能猜测端点或删除修复上下文。
// Inspect and report unresolved candidates without changing nodes or relations.
func (e *Extractor) RepairDanglingRelations(ctx context.Context, res *Result, docs []*dktypes.AnnotatedDocument) *RepairReport {
	rpt := &RepairReport{}
	if res == nil || len(res.Domains) == 0 {
		return rpt
	}

	dangling := scanDangling(res.Domains)
	rpt.DanglingFound = len(dangling)
	rpt.EndpointFailures = append(rpt.EndpointFailures, dangling...)
	if len(dangling) > 0 {
		// 缺少冻结原文时不能判定或修复，保留候选供 source-aware helper。
		// Preserve evidence rather than deleting the only record of a repairable edge.
		rpt.PatchErrors = append(rpt.PatchErrors, "relation repair unavailable: frozen originals required; use ProcessRelations")
		rpt.StillDangling = rpt.DanglingFound
		rpt.Degraded = true
		log.Printf("[extract] retained %d unresolved relation candidates: frozen originals unavailable", rpt.DanglingFound)
	}

	// Relation-only legacy entry must not rewrite node provenance/descriptions.
	return rpt
}
