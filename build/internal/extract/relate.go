package extract

import (
	"context"
	"encoding/json"
)

// RelateReport 保留旧调用的可观测返回；解析成功不等于原文支持。
// Legacy callers must supply originals through ProcessRelations before adding edges.
type RelateReport struct {
	SubdomainsTotal int
	SubdomainsOK    int
	RelationsAdded  int
	InvalidDropped  int
	Failures        []string
}

// IdentifyRelations 的旧签名没有原文，因此只报告限制并保留已有内容。
// No model call or unverified addition is allowed from generated node descriptions.
func (e *Extractor) IdentifyRelations(ctx context.Context, res *Result) *RelateReport {
	rpt := &RelateReport{}
	if res == nil {
		return rpt
	}
	for _, d := range res.Domains {
		for _, sd := range d.Subdomains {
			if len(sd.Entities)+len(sd.Concepts) >= 2 {
				rpt.SubdomainsTotal++
			}
		}
	}
	reason := "relation identification unavailable: frozen originals/member material required; use ProcessRelations"
	if ctx.Err() != nil {
		reason = ctx.Err().Error()
	}
	rpt.Failures = append(rpt.Failures, reason)
	return rpt
}

// appendDedupRelations 保留同三元组的不同限定，只有完整断言相同时去重。
// Qualifier-sensitive identity prevents condition/time/source text from disappearing.
func appendDedupRelations(existing, added []Relation) []Relation {
	seen := make(map[string]bool, len(existing)+len(added))
	key := func(r Relation) string {
		wire, _ := json.Marshal([]string{r.Source, r.Target, r.Kind, r.Description})
		return string(wire)
	}
	for _, r := range existing {
		seen[key(r)] = true
	}
	out := existing
	for _, r := range added {
		k := key(r)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}
