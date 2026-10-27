package extract

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// Build04ID 保留旧的可读前缀；名称归一化本身不再承担语义身份。
// Build04ID returns the legacy readable prefix, not a unique semantic identity.
func Build04ID(skill string, tag dktypes.AnnotationTag, name string) string {
	return slugify(skill) + "-" + string(tag) + "-" + slugify(name)
}

// AssignIDs 用逐项语义签名避免同名或 slug 碰撞；路径、行号、置信度和候选顺序不改变身份。
// Exact duplicates share an identity while their separate source contributions remain intact.
func AssignIDs(docs []*dktypes.AnnotatedDocument) []*dktypes.AnnotatedDocument {
	out := make([]*dktypes.AnnotatedDocument, 0, len(docs))
	for _, d := range docs {
		if d == nil {
			continue
		}
		nd := &dktypes.AnnotatedDocument{Skill: d.Skill, FilePath: d.FilePath}
		for _, it := range d.Items {
			ni := it
			signature, _ := json.Marshal([]string{d.Skill, string(it.Tag), it.Name, it.Content, it.Detail})
			ni.ID = fmt.Sprintf("%s-%x", Build04ID(d.Skill, it.Tag, it.Name), sha256.Sum256(signature))
			nd.Items = append(nd.Items, ni)
		}
		out = append(out, nd)
	}
	return out
}

// MemberContribution 是候选内的一次真实来源贡献；不能作为持久节点身份种子。
// MemberContribution identifies evidence, separately from persistent semantic identity.
type MemberContribution struct {
	Skill       string                `json:"skill"`
	FilePath    string                `json:"file_path"`
	CandidateID string                `json:"candidate_id"`
	ItemIndex   int                   `json:"item_index"`
	MemberID    string                `json:"member_id"`
	Item        dktypes.AnnotatedItem `json:"item"`
}

// LookupMemberContributions 保留每个匹配成员的全部来源，不以首见文本覆盖其他文档。
// LookupMemberContributions returns all matching incidences in deterministic evidence order.
func LookupMemberContributions(docs []*dktypes.AnnotatedDocument, members []string) []MemberContribution {
	want := map[string]bool{}
	for _, m := range members {
		want[m] = true
	}
	var out []MemberContribution
	for _, d := range docs {
		if d == nil {
			continue
		}
		wire, _ := json.Marshal(d.Items)
		candidate := fmt.Sprintf("%x", sha256.Sum256(wire))
		for i, it := range d.Items {
			if want[it.ID] {
				out = append(out, MemberContribution{d.Skill, d.FilePath, candidate, i, it.ID, it})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Skill != b.Skill {
			return a.Skill < b.Skill
		}
		if a.FilePath != b.FilePath {
			return a.FilePath < b.FilePath
		}
		if a.CandidateID != b.CandidateID {
			return a.CandidateID < b.CandidateID
		}
		return a.ItemIndex < b.ItemIndex
	})
	return out
}

func contributionKey(c MemberContribution) string {
	wire, _ := json.Marshal([]string{c.Skill, path.Clean(strings.ReplaceAll(c.FilePath, "\\", "/")), c.MemberID})
	return string(wire)
}
