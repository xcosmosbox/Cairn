package extract

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

// CoverageOptions.MaxRetries 是失败范围共享的额外恢复次数：0 默认3，负值禁用。
// The initial mapping call does not consume this allowance; only unresolved members retry.
type CoverageOptions struct{ MaxRetries int }

type CoverageAttempt struct {
	Number      int      `json:"number"`
	MemberIDs   []string `json:"member_ids"`
	CandidateID string   `json:"candidate_id,omitempty"`
	Outcome     string   `json:"outcome"`
	Reason      string   `json:"reason,omitempty"`
}

type CoverageReport struct {
	ExtractAttempts       int
	InputUnits            int
	DuplicateNodesMerged  int
	InvalidMembersRemoved int
	InvalidMemberIDs      []string
	EmptyNodesFound       int
	EmptyNodesFilled      int
	FirstPassCovered      int
	FirstPassRate         float64
	Assigned              int
	Created               int
	Explained             int
	ExplainedNotes        []string
	FinalCovered          int
	FinalRate             float64
	TrulyMissing          []string
	Outcome               string
	Reason                string
	Attempts              []CoverageAttempt
	Exhausted             bool
	FinalCoverage         FinalCoverageReport
	NeedsDescription      []string
}

func coverageRate(covered, input int) float64 {
	if input <= 0 {
		return 1
	}
	return float64(covered) / float64(input)
}
func coverageAllowance(n int) int {
	if n == 0 {
		return annotation.DefaultAnnotateMaxRetries
	}
	if n < 0 {
		return 0
	}
	return n
}

func (e *Extractor) ExtractWithCoverage(ctx context.Context, docs []*dktypes.AnnotatedDocument) (*Result, *CoverageReport, error) {
	return e.ExtractWithCoverageOptions(ctx, docs, CoverageOptions{})
}
func (e *Extractor) ExtractWithCoverageOptions(ctx context.Context, docs []*dktypes.AnnotatedDocument, options CoverageOptions) (*Result, *CoverageReport, error) {
	return e.recoverCoverage(ctx, &Result{}, docs, options, true)
}

// RepairFinalCoverage 仅修复过滤后丢失的映射；调用方须重新描述新增节点，再重算最终覆盖。
// RepairFinalCoverage keeps accepted siblings; recovered nodes still require semantic description.
func (e *Extractor) RepairFinalCoverage(ctx context.Context, res *Result, docs []*dktypes.AnnotatedDocument, options CoverageOptions) (*Result, *CoverageReport, error) {
	if res == nil {
		res = &Result{}
	}
	return e.recoverCoverage(ctx, res, docs, options, false)
}

func (e *Extractor) recoverCoverage(ctx context.Context, res *Result, docs []*dktypes.AnnotatedDocument, options CoverageOptions, initial bool) (*Result, *CoverageReport, error) {
	rpt := &CoverageReport{InputUnits: len(collectUnitIDs(docs)), Attempts: []CoverageAttempt{}}
	update := func() {
		cov := ComputeFinalCoverage(res, docs)
		rpt.FinalCoverage = cov
		rpt.FinalCovered = cov.CoveredUnits
		rpt.FinalRate = coverageRate(cov.CoveredUnits, rpt.InputUnits)
		rpt.TrulyMissing = cov.UncoveredIDs
		rpt.Outcome = cov.Outcome
		rpt.Reason = cov.Reason
	}
	update()
	if ctx.Err() != nil {
		rpt.Outcome = "cancelled"
		rpt.Reason = ctx.Err().Error()
		return res, rpt, ctx.Err()
	}
	if rpt.FinalCoverage.InputIncidences == 0 && rpt.InputUnits == 0 {
		return res, rpt, nil
	}
	if !initial && rpt.Outcome == "complete" {
		return res, rpt, nil
	}
	if e == nil || e.client == nil || (reflect.ValueOf(e.client).Kind() == reflect.Ptr && reflect.ValueOf(e.client).IsNil()) {
		rpt.Outcome = "unavailable"
		rpt.Reason = "extract: invalid client or maxTokens"
		return res, rpt, fmt.Errorf("%s", rpt.Reason)
	}
	remaining := coverageAllowance(options.MaxRetries)
	for {
		if err := ctx.Err(); err != nil {
			rpt.Outcome = "cancelled"
			rpt.Reason = err.Error()
			return res, rpt, err
		}
		target := docs
		if !initial || rpt.ExtractAttempts > 0 {
			target = uncoveredDocuments(docs, rpt.TrulyMissing)
		}
		if len(target) == 0 {
			update()
			return res, rpt, nil
		}
		ids := orderedUnitIDs(target)
		attempt := CoverageAttempt{Number: len(rpt.Attempts) + 1, MemberIDs: ids}
		retained, prompt := coverageRetainedContext(res, docs)
		candidate, err := e.extractAnnotatedWithContext(ctx, target, prompt)
		rpt.ExtractAttempts++
		if err == nil {
			wire, _ := json.Marshal(candidate.Domains)
			attempt.CandidateID = fmt.Sprintf("%x", sha256.Sum256(wire))
			err = ValidateFusionCompleteness(candidate, target, 0, false)
		}
		if err == nil {
			touched := attachRetainedMembers(candidate, retained)
			rpt.DuplicateNodesMerged += DedupFusionNodes(candidate.Domains)
			rpt.NeedsDescription = unionStrings(rpt.NeedsDescription, touched)
			mergeCoverageDomains(&res.Domains, candidate.Domains)
			for _, d := range candidate.Domains {
				for _, sd := range d.Subdomains {
					for _, nodes := range [][]Node{sd.Entities, sd.Concepts} {
						for _, n := range nodes {
							rpt.NeedsDescription = unionStrings(rpt.NeedsDescription, []string{n.ID})
						}
					}
				}
			}
			res.Rounds += candidate.Rounds
			update()
			attempt.Outcome = rpt.Outcome
			attempt.Reason = rpt.Reason
		} else {
			attempt.Outcome = "unavailable"
			attempt.Reason = err.Error()
			rpt.Outcome = "unavailable"
			rpt.Reason = err.Error()
		}
		if ctx.Err() != nil {
			attempt.Outcome = "cancelled"
			rpt.Outcome = "cancelled"
			rpt.Reason = ctx.Err().Error()
		}
		rpt.Attempts = append(rpt.Attempts, attempt)
		if rpt.ExtractAttempts == 1 {
			rpt.FirstPassCovered = rpt.FinalCovered
			rpt.FirstPassRate = rpt.FinalRate
		}
		if ctx.Err() != nil {
			return res, rpt, ctx.Err()
		}
		if err == nil && rpt.Outcome == "complete" {
			return res, rpt, nil
		}
		if err != nil && annotation.IsPermanentModelError(err) {
			return res, rpt, err
		}
		if remaining == 0 {
			rpt.Exhausted = true
			return res, rpt, nil
		}
		remaining--
	}
}

func orderedUnitIDs(docs []*dktypes.AnnotatedDocument) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range docs {
		if d == nil {
			continue
		}
		for _, it := range d.Items {
			if !seen[it.ID] {
				seen[it.ID] = true
				out = append(out, it.ID)
			}
		}
	}
	return out
}
func uncoveredDocuments(docs []*dktypes.AnnotatedDocument, missing []string) []*dktypes.AnnotatedDocument {
	want := map[string]bool{}
	for _, id := range missing {
		want[id] = true
	}
	var out []*dktypes.AnnotatedDocument
	for _, d := range docs {
		if d == nil {
			continue
		}
		nd := &dktypes.AnnotatedDocument{Skill: d.Skill, FilePath: d.FilePath}
		for _, it := range d.Items {
			if want[it.ID] {
				nd.Items = append(nd.Items, it)
			}
		}
		if len(nd.Items) > 0 {
			out = append(out, nd)
		}
	}
	return out
}

// mergeCoverageDomains 合并已验证映射，不用缺失响应替换成功兄弟；新临时引用避免覆盖既有引用。
// Recovery adds only unresolved members and preserves each existing node and its provenance.
func mergeCoverageDomains(dst *[]Domain, added []Domain) {
	for _, d := range added {
		di := -1
		for i := range *dst {
			if (*dst)[i].Name == d.Name {
				di = i
				break
			}
		}
		if di < 0 {
			*dst = append(*dst, d)
			continue
		}
		existing := &(*dst)[di]
		existing.SourceSkills = unionStrings(existing.SourceSkills, d.SourceSkills)
		for _, sd := range d.Subdomains {
			si := -1
			for i := range existing.Subdomains {
				if existing.Subdomains[i].Name == sd.Name {
					si = i
					break
				}
			}
			if si < 0 {
				existing.Subdomains = append(existing.Subdomains, sd)
				continue
			}
			target := &existing.Subdomains[si]
			used := map[string]bool{}
			for _, ns := range [][]Node{target.Entities, target.Concepts} {
				for _, n := range ns {
					used[n.ID] = true
				}
			}
			aliases := map[string]string{}
			for _, ns := range [][]Node{sd.Entities, sd.Concepts} {
				for i := range ns {
					if used[ns[i].ID] {
						old := ns[i].ID
						wire, _ := json.Marshal(ns[i].Members)
						ns[i].ID = fmt.Sprintf("%s-recovered-%x", old, sha256.Sum256(wire))
						aliases[old] = ns[i].ID
					}
					used[ns[i].ID] = true
				}
			}
			for i := range sd.Relations {
				if id := aliases[sd.Relations[i].Source]; id != "" {
					sd.Relations[i].Source = id
				}
				if id := aliases[sd.Relations[i].Target]; id != "" {
					sd.Relations[i].Target = id
				}
			}
			target.Entities = append(target.Entities, sd.Entities...)
			target.Concepts = append(target.Concepts, sd.Concepts...)
			target.Relations = append(target.Relations, sd.Relations...)
		}
	}
	// Name normalization collisions must not make distinct layer identities overwrite each other.
	usedDomains := map[string]string{}
	for di := range *dst {
		d := &(*dst)[di]
		if name, ok := usedDomains[d.Slug]; ok && name != d.Name {
			d.Slug = collisionSlug(d.Slug, d.Name)
		}
		usedDomains[d.Slug] = d.Name
		usedSubs := map[string]string{}
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			if name, ok := usedSubs[sd.Slug]; ok && name != sd.Name {
				sd.Slug = collisionSlug(sd.Slug, sd.Name)
			}
			usedSubs[sd.Slug] = sd.Name
		}
	}
}
func collisionSlug(prefix, name string) string {
	hash := sha256.Sum256([]byte(name))
	return strings.Trim(prefix, "-") + fmt.Sprintf("-%x", hash[:8])
}

// coverageRetainedContext 的 alias 只在本次调用有效，持久 UUID 不能进入模型消息。
// Read-only retained context lets missing members request attachment without reprocessing siblings.
func coverageRetainedContext(res *Result, docs []*dktypes.AnnotatedDocument) (map[string]*Node, string) {
	aliases := map[string]*Node{}
	var nodes []*Node
	for di := range res.Domains {
		for si := range res.Domains[di].Subdomains {
			sd := &res.Domains[di].Subdomains[si]
			for _, ns := range [][]Node{sd.Entities, sd.Concepts} {
				for i := range ns {
					nodes = append(nodes, &ns[i])
				}
			}
		}
	}
	if len(nodes) == 0 {
		return aliases, ""
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	var text strings.Builder
	text.WriteString("\n## 已保留对象（只读，不重新输出其既有成员）\n仅当新增成员属于同一真实对象时，输出节点 id 为下列 retained alias、name/label 与目标逐字一致，members 仅包含本次新增成员。否则创建新对象。不能仅凭同名判断等价。挂接后仍须核验全部语义。\n")
	for i, n := range nodes {
		alias := fmt.Sprintf("retained#%d", i+1)
		aliases[alias] = n
		fmt.Fprintf(&text, "- alias=%s [%s] %s\n  summary: %s\n  description: %s\n", alias, n.Label, n.Name, n.Summary, n.Description)
		for _, c := range LookupMemberContributions(docs, n.Members) {
			fmt.Fprintf(&text, "  source=%s/%s content: %s\n  detail: %s\n", c.Skill, c.FilePath, c.Item.Content, c.Item.Detail)
		}
	}
	return aliases, text.String()
}

func attachRetainedMembers(candidate *Result, aliases map[string]*Node) []string {
	var touched []string
	redirects := map[string]string{}
	filter := func(nodes []Node) []Node {
		kept := make([]Node, 0, len(nodes))
		for _, n := range nodes {
			if !strings.HasPrefix(n.ID, "retained#") {
				kept = append(kept, n)
				continue
			}
			target := aliases[n.ID]
			// 显式本次 alias 声明仅建立待复核映射；不是同名即语义合并的启发式。
			// An exact requested alias is a mapping proposal; full semantic recheck remains required.
			if target == nil || n.Name != target.Name || n.Label != target.Label {
				continue
			}
			target.Members = unionStrings(target.Members, n.Members)
			target.SourceSkills = unionStrings(target.SourceSkills, n.SourceSkills)
			redirects[n.ID] = target.ID
			touched = unionStrings(touched, []string{target.ID})
		}
		return kept
	}
	for di := range candidate.Domains {
		for si := range candidate.Domains[di].Subdomains {
			sd := &candidate.Domains[di].Subdomains[si]
			sd.Entities = filter(sd.Entities)
			sd.Concepts = filter(sd.Concepts)
		}
	}
	for di := range candidate.Domains {
		for si := range candidate.Domains[di].Subdomains {
			sd := &candidate.Domains[di].Subdomains[si]
			for ri := range sd.Relations {
				if id := redirects[sd.Relations[ri].Source]; id != "" {
					sd.Relations[ri].Source = id
				}
				if id := redirects[sd.Relations[ri].Target]; id != "" {
					sd.Relations[ri].Target = id
				}
			}
		}
	}
	return touched
}
