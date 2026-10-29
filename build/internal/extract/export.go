// Package extract 的本文件提供给增量流水线复用的公开包装：
// 把包内既有的 slugify 与 relation kind 枚举校验暴露出去，避免增量包另造一套
// （DRY：slug 规则与 6 枚举是跨阶段必须一致的单一事实源）。
//
// This file exposes small public wrappers around the package's existing slugify
// and relation-kind validation so the incremental pipeline reuses one source of
// truth instead of re-implementing them.
package extract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Slugify 把（中文）名称转为 kebab-case 标识（包内 slugify 的公开包装）。
// 增量流水线在「新建 domain/subdomain」时用它派生 slug，与全量流水线完全一致。
//
// Slugify converts a (Chinese) name into a kebab-case identifier; public wrapper
// around the internal slugify so the incremental pipeline derives slugs exactly
// like the full pipeline.
func Slugify(s string) string {
	return slugify(s)
}

// IsValidRelationKind 校验 relation.kind 是否为允许的 6 个语义枚举之一
// （validRelationKinds 的公开包装）。增量 relation 重算（I-7）用它做越界校验，
// 与全量 relate 阶段同一枚举集。
//
// IsValidRelationKind reports whether kind is one of the 6 allowed semantic
// relation kinds; public wrapper so incremental relation recomputation shares
// the full pipeline's enum set.
func IsValidRelationKind(kind string) bool {
	return validRelationKinds[kind]
}

// ApplySelectedRelationAssertions 在 UUID 翻译前把本轮明示证据接入已有持久化表示。
// Only current complete Selected facets receive explicit-source credit; historical Decisions do not.
func ApplySelectedRelationAssertions(res *Result, report *RelationProcessReport) {
	if res == nil || report == nil {
		return
	}
	type assertion struct {
		Classification string          `json:"classification"`
		Description    string          `json:"description"`
		Sources        []CrossSource   `json:"sources,omitempty"`
		Evidence       []CrossEvidence `json:"evidence,omitempty"`
	}
	type envelope struct {
		Assertions []assertion `json:"cairn_relation_assertions_v1"`
	}
	index := indexEndpoints(res.Domains)
	key := func(scope endpointScope, relation Relation) string {
		source, target, ss, ts := index.relation(scope, relation)
		if ss != "resolved" || ts != "resolved" {
			return ""
		}
		return qualifiedRelationAlias(source) + "\x00" + relation.Kind + "\x00" + qualifiedRelationAlias(target)
	}
	facets := map[string]map[string]assertion{}
	for _, selected := range report.Selected {
		if selected.Classification != "explicit" || selected.Outcome != "complete" {
			continue
		}
		k := key(endpointScope{selected.OwnerDomain, selected.OwnerSubdomain}, selected.Relation)
		if k == "" {
			continue
		}
		a := assertion{Classification: "explicit", Description: selected.Relation.Description, Sources: append([]CrossSource(nil), selected.Sources...), Evidence: append([]CrossEvidence(nil), selected.Evidence...)}
		sort.Slice(a.Sources, func(i, j int) bool {
			x, _ := json.Marshal(a.Sources[i])
			y, _ := json.Marshal(a.Sources[j])
			return string(x) < string(y)
		})
		sort.Slice(a.Evidence, func(i, j int) bool {
			x, _ := json.Marshal(a.Evidence[i])
			y, _ := json.Marshal(a.Evidence[j])
			return string(x) < string(y)
		})
		wire, _ := json.Marshal(a)
		if facets[k] == nil {
			facets[k] = map[string]assertion{}
		}
		facets[k][string(wire)] = a
	}
	for di := range res.Domains {
		d := &res.Domains[di]
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			kept := make([]Relation, 0, len(sd.Relations))
			seen := map[string]bool{}
			for _, relation := range sd.Relations {
				k := key(endpointScope{d.Slug, sd.Slug}, relation)
				values := facets[k]
				if len(values) == 0 || seen[k] {
					continue
				}
				seen[k] = true
				keys := make([]string, 0, len(values))
				for value := range values {
					keys = append(keys, value)
				}
				sort.Strings(keys)
				out := envelope{}
				for _, value := range keys {
					out.Assertions = append(out.Assertions, values[value])
				}
				wire, _ := json.Marshal(out)
				relation.Description = string(wire)
				kept = append(kept, relation)
			}
			sd.Relations = kept
		}
	}
}

// LoadSourceContext 读取既有 00/04 私有产物；没有 selector 时不猜测原文来源。
// Missing or conflicting selected snapshots are configuration errors, never generated-text fallbacks.
func LoadSourceContext(frozenPath, documentPath string) (*SourceContext, error) {
	if frozenPath == "" && documentPath == "" {
		return nil, nil
	}
	if frozenPath == "" || documentPath == "" {
		return nil, fmt.Errorf("source context: both frozen-sources and source-documents are required")
	}
	out := &SourceContext{}
	read := func(path string, value any) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("source context: read %s: %w", path, err)
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(value); err != nil {
			return fmt.Errorf("source context: decode %s: %w", path, err)
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			return fmt.Errorf("source context: trailing data in %s", path)
		}
		return nil
	}
	if err := read(frozenPath, &out.Sources); err != nil {
		return nil, err
	}
	if err := read(documentPath, &out.Documents); err != nil {
		return nil, err
	}
	if err := ValidateSourceContext(out.Sources, out.Documents); err != nil {
		return nil, err
	}
	return out, nil
}

// ValidateSourceContext 校验明确原文的身份和成员绑定，绝不修补缺失原文或猜测 hash。
// One (skill,path) key has exactly one authoritative current raw body.
func ValidateSourceContext(sources []FrozenSource, docs []*dktypes.AnnotatedDocument) error {
	originals := map[string]FrozenSource{}
	safe := func(path string) bool {
		return path != "" && path != "." && !strings.Contains(path, "\\") && !filepath.IsAbs(path) && filepath.ToSlash(filepath.Clean(path)) == path && path != ".." && !strings.HasPrefix(path, "../")
	}
	for _, source := range sources {
		if !safe(source.Path) || !utf8.ValidString(source.Content) || strings.ContainsRune(source.Content, 0) || FreezeSource(source.Skill, source.Path, source.Content).SHA256 != source.SHA256 {
			return fmt.Errorf("source context: invalid frozen identity %s", source.Path)
		}
		key := source.Skill + "\x00" + source.Path
		if prior, ok := originals[key]; ok && prior.SHA256 != source.SHA256 {
			return fmt.Errorf("source context: conflicting current originals %s", source.Path)
		}
		originals[key] = source
	}
	for _, doc := range docs {
		if doc == nil {
			return fmt.Errorf("source context: nil accepted document")
		}
		if !safe(doc.FilePath) {
			return fmt.Errorf("source context: invalid document path %s", doc.FilePath)
		}
		if _, ok := originals[doc.Skill+"\x00"+doc.FilePath]; !ok {
			return fmt.Errorf("source context: missing original for accepted members %s", doc.FilePath)
		}
		for _, item := range doc.Items {
			if item.ID == "" {
				return fmt.Errorf("source context: blank accepted member in %s", doc.FilePath)
			}
		}
	}
	return nil
}
