// Package writeback 的本文件实现 sidecar（.md.kg.yaml）的读写。
//
// sidecar schema（设计定稿 §5.4）：
//
//	# 由系统维护，请勿手动编辑本文件。人工创作请在对应 .md 的「摘要/详述」可编辑区进行。
//	schema_version: 1
//	doc: reference/order-lifecycle.md
//	doc_hash: sha256:ab12...           # 回写后 md 正文整体 baseline
//	generated_at: 2026-07-09T18:00:00Z
//	nodes:
//	  - uuid: 7f3a...e21
//	    tag: entity                      # 只读
//	    name: 订单聚合根                 # 只读
//	    domain: 订单管理                 # 只读（派生）
//	    subdomain: 订单生命周期          # 只读（派生）
//	    shared: false                    # 只读（系统计算）
//	    members: [order-skill-entity-订单聚合根]  # 只读（血缘）
//	    span: { start_line: 12, end_line: 20, quote: "..." }
//	    summary_hash: sha256:...         # 可编辑 baseline
//	    description_hash: sha256:...     # 可编辑 baseline
//	    provenance: llm_inferred         # 人改后升级 human_curated
//
// 铁律：sidecar 不存 summary/description 正文（正文在 md），只存 hash baseline
// （避免双写真相冲突）。
//
// This file implements sidecar (.md.kg.yaml) read/write. sidecar stores only
// read-only metadata + hash baselines (no summary/description prose — R7).
package writeback

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// sidecarFile 是 sidecar 的内存表示，对应 <doc>.md.kg.yaml。
// sidecarFile is the in-memory representation of a sidecar file.
type sidecarFile struct {
	SchemaVersion int           `yaml:"schema_version"`
	Doc           string        `yaml:"doc"`
	DocHash       string        `yaml:"doc_hash"`
	GeneratedAt   string        `yaml:"generated_at"`
	Nodes         []sidecarNode `yaml:"nodes"`
}

// sidecarNode 是 sidecar 中单个 node 的条目。
// sidecarNode is one node entry in the sidecar.
type sidecarNode struct {
	UUID            string       `yaml:"uuid"`
	FileSlug        string       `yaml:"file_slug,omitempty"`
	Tag             string       `yaml:"tag"`
	Name            string       `yaml:"name"`
	Domain          string       `yaml:"domain"`
	Subdomain       string       `yaml:"subdomain"`
	DomainSlug      string       `yaml:"domain_slug,omitempty"`
	SubdomainSlug   string       `yaml:"subdomain_slug,omitempty"`
	Shared          bool         `yaml:"shared"`
	Members         []string     `yaml:"members"`
	Span            *sidecarSpan `yaml:"span,omitempty"`
	SummaryHash     string       `yaml:"summary_hash"`
	DescriptionHash string       `yaml:"description_hash"`
	Provenance      string       `yaml:"provenance"`
}

// sidecarSpan 是 sidecar 中 node 的 span（只读元数据，1-based 闭区间）。
// sidecarSpan is a node's span in the sidecar (read-only, 1-based inclusive).
type sidecarSpan struct {
	StartLine int    `yaml:"start_line"`
	EndLine   int    `yaml:"end_line"`
	Quote     string `yaml:"quote,omitempty"`
}

// The pointer fields below let validation distinguish an omitted required YAML
// key from an explicitly supplied zero value (notably shared: false).
type sidecarPresence struct {
	SchemaVersion *int                   `yaml:"schema_version"`
	Doc           *string                `yaml:"doc"`
	DocHash       *string                `yaml:"doc_hash"`
	GeneratedAt   *string                `yaml:"generated_at"`
	Nodes         *[]sidecarNodePresence `yaml:"nodes"`
}

type sidecarNodePresence struct {
	UUID            *string      `yaml:"uuid"`
	Tag             *string      `yaml:"tag"`
	Name            *string      `yaml:"name"`
	Domain          *string      `yaml:"domain"`
	Subdomain       *string      `yaml:"subdomain"`
	DomainSlug      *string      `yaml:"domain_slug"`
	SubdomainSlug   *string      `yaml:"subdomain_slug"`
	Shared          *bool        `yaml:"shared"`
	Members         *[]string    `yaml:"members"`
	Span            *sidecarSpan `yaml:"span"`
	SummaryHash     *string      `yaml:"summary_hash"`
	DescriptionHash *string      `yaml:"description_hash"`
	Provenance      *string      `yaml:"provenance"`
}

// sidecarHeaderComment 是 sidecar 文件顶部的说明注释。
// sidecarHeaderComment is the header comment atop every sidecar file.
const sidecarHeaderComment = "# 由系统维护，请勿手动编辑本文件。人工创作请在对应 .md 的「摘要/详述」可编辑区进行。"

// sha256Hex 计算 s 的 sha256 十六进制摘要，返回 "sha256:<hex>" 前缀格式。
// sha256Hex returns "sha256:<hex>" of s.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validSHA256Hash(s string) bool {
	if len(s) != len("sha256:")+64 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(s, "sha256:"))
	return err == nil
}

func validDocRelPath(s string) bool {
	if s == "" || strings.TrimSpace(s) != s || filepath.IsAbs(s) ||
		strings.Contains(s, "\\") || !strings.HasSuffix(s, ".md") {
		return false
	}
	clean := path.Clean(s)
	return clean == s && clean != "." && clean != ".." &&
		!strings.HasPrefix(clean, "../")
}

func validScalar(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validText(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validUUID(s string) bool {
	if !validScalar(s) {
		return false
	}
	return !strings.ContainsAny(s, "/\\")
}

func validSlug(s string) bool {
	if !validScalar(s) {
		return false
	}
	return !strings.ContainsAny(s, "/\\")
}

// validateSidecarValue performs semantic validation after YAML decoding.  It
// intentionally accepts missing domain_slug/subdomain_slug for old sidecars;
// those fields were added later and the diff layer will fail closed until a
// canonical rewrite upgrades the baseline.
func validateSidecarValue(sf sidecarFile, p *sidecarPresence) error {
	if p != nil {
		if p.SchemaVersion == nil || p.Doc == nil || p.DocHash == nil ||
			p.GeneratedAt == nil || p.Nodes == nil {
			return fmt.Errorf("missing required top-level field")
		}
	}
	if sf.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version %d", sf.SchemaVersion)
	}
	if !validDocRelPath(sf.Doc) {
		return fmt.Errorf("invalid doc path %q", sf.Doc)
	}
	if !validSHA256Hash(sf.DocHash) {
		return fmt.Errorf("invalid doc_hash")
	}
	if strings.TrimSpace(sf.GeneratedAt) == "" {
		return fmt.Errorf("missing generated_at")
	}
	if _, err := time.Parse(time.RFC3339, sf.GeneratedAt); err != nil {
		return fmt.Errorf("invalid generated_at: %w", err)
	}
	if p != nil && p.Nodes != nil && len(*p.Nodes) != len(sf.Nodes) {
		return fmt.Errorf("nodes is not a sequence")
	}
	seen := make(map[string]struct{}, len(sf.Nodes))
	for i, n := range sf.Nodes {
		if p != nil {
			if i >= len(*p.Nodes) {
				return fmt.Errorf("nodes entry %d missing", i)
			}
			pn := (*p.Nodes)[i]
			if pn.UUID == nil || pn.Tag == nil || pn.Name == nil || pn.Domain == nil ||
				pn.Subdomain == nil || pn.Shared == nil || pn.Members == nil ||
				pn.SummaryHash == nil || pn.DescriptionHash == nil || pn.Provenance == nil {
				return fmt.Errorf("node %d missing required field", i)
			}
		}
		if !validUUID(n.UUID) {
			return fmt.Errorf("node %d has invalid uuid", i)
		}
		if _, ok := seen[n.UUID]; ok {
			return fmt.Errorf("duplicate node uuid %q", n.UUID)
		}
		seen[n.UUID] = struct{}{}
		if n.Tag != "entity" && n.Tag != "concept" {
			return fmt.Errorf("node %q has invalid tag %q", n.UUID, n.Tag)
		}
		if !validText(n.Name) || !validText(n.Domain) || !validText(n.Subdomain) {
			return fmt.Errorf("node %q has missing read-only identity", n.UUID)
		}
		if n.DomainSlug != "" && !validSlug(n.DomainSlug) {
			return fmt.Errorf("node %q has invalid domain_slug", n.UUID)
		}
		if n.SubdomainSlug != "" && !validSlug(n.SubdomainSlug) {
			return fmt.Errorf("node %q has invalid subdomain_slug", n.UUID)
		}
		memberSeen := make(map[string]struct{}, len(n.Members))
		for _, member := range n.Members {
			if !validUUID(member) {
				return fmt.Errorf("node %q has invalid member", n.UUID)
			}
			if _, ok := memberSeen[member]; ok {
				return fmt.Errorf("node %q has duplicate member %q", n.UUID, member)
			}
			memberSeen[member] = struct{}{}
		}
		if !validSHA256Hash(n.SummaryHash) || !validSHA256Hash(n.DescriptionHash) {
			return fmt.Errorf("node %q has invalid editable hash", n.UUID)
		}
		if n.Provenance != "llm_inferred" && n.Provenance != "human_curated" && n.Provenance != "extraction" {
			return fmt.Errorf("node %q has invalid provenance %q", n.UUID, n.Provenance)
		}
		if n.Span != nil {
			if n.Span.StartLine < 1 || n.Span.EndLine < n.Span.StartLine {
				return fmt.Errorf("node %q has invalid span", n.UUID)
			}
		}
	}
	return nil
}

// ValidateSidecar validates an already decoded sidecar value.  ReadSidecar
// additionally checks required-key presence while decoding YAML; this exported
// value-level check protects callers such as DiffDoc that receive a struct
// directly (and makes malformed baselines fail closed rather than fake C1).
func ValidateSidecar(sf SidecarFile) error {
	return validateSidecarValue(sf, nil)
}

// buildSidecar 从已渲染的 md 正文 + node 视图集合构造 sidecar。
// docRelPath 是该 md 相对 repoRoot 的路径（写入 sidecar.doc 字段）。
// mdContent 是已整篇覆盖写入的 md 正文（计算 doc_hash baseline 用）。
//
// buildSidecar builds a sidecar from the rendered md content + node views.
func buildSidecar(docRelPath, mdContent string, nodes []nodeView, generatedAt time.Time) sidecarFile {
	sf := sidecarFile{
		SchemaVersion: 1,
		Doc:           docRelPath,
		DocHash:       sha256Hex(mdContent),
		GeneratedAt:   generatedAt.UTC().Format(time.RFC3339),
	}
	for _, v := range nodes {
		// provenance：nodeView 携带权威值（增量回写 human_curated 保护）；
		// 空串回退 llm_inferred（全量首建语义，人改后由增量升级）。
		// provenance: the view's authoritative value (incremental human_curated
		// protection); empty falls back to llm_inferred (full-build semantics).
		provenance := v.Provenance
		if provenance == "" {
			provenance = "llm_inferred"
		}
		sn := sidecarNode{
			UUID:            v.UUID,
			Tag:             strings.ToLower(v.Tag),
			Name:            v.Name,
			Domain:          v.Domain,
			Subdomain:       v.Subdomain,
			DomainSlug:      v.DomainSlug,
			SubdomainSlug:   v.SubdomainSlug,
			Shared:          v.Shared,
			Members:         append([]string(nil), v.Members...),
			SummaryHash:     sha256Hex(v.Summary),
			DescriptionHash: sha256Hex(v.Description),
			Provenance:      provenance,
		}
		if v.Span != nil {
			sn.Span = &sidecarSpan{
				StartLine: v.Span.StartLine,
				EndLine:   v.Span.EndLine,
			}
		}
		if len(sn.Members) == 0 {
			sn.Members = []string{} // yaml 空列表渲染为 []，而非 null
		}
		sf.Nodes = append(sf.Nodes, sn)
	}
	return sf
}

// writeSidecar 把 sidecar 写到 <mdPath>.kg.yaml（整篇覆盖）。
// writeSidecar writes the sidecar to <mdPath>.kg.yaml (whole-file replace, R6).
func writeSidecar(mdPath string, sf sidecarFile) error {
	if err := validateSidecarValue(sf, nil); err != nil {
		return fmt.Errorf("writeback: refuse invalid sidecar for %s: %w", mdPath, err)
	}
	var sb strings.Builder
	sb.WriteString(sidecarHeaderComment)
	sb.WriteString("\n")
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(sf); err != nil {
		return fmt.Errorf("writeback: encode sidecar yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("writeback: close sidecar yaml encoder: %w", err)
	}
	sidecarPath := mdPath + ".kg.yaml"
	if err := os.MkdirAll(filepath.Dir(sidecarPath), 0o755); err != nil {
		return fmt.Errorf("writeback: mkdir sidecar dir %s: %w", filepath.Dir(sidecarPath), err)
	}
	if err := atomicWriteFile(sidecarPath, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("writeback: write sidecar %s: %w", sidecarPath, err)
	}
	return nil
}

// readSidecar 读取 <mdPath>.kg.yaml 并解析为 sidecarFile。
// 本批实现供增量批次用，本批不消费。
// readSidecar reads and parses <mdPath>.kg.yaml. Provided for the incremental
// batch; not consumed in this batch.
func readSidecar(mdPath string) (*sidecarFile, error) {
	sidecarPath := mdPath + ".kg.yaml"
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		return nil, fmt.Errorf("writeback: read sidecar %s: %w", sidecarPath, err)
	}
	content := string(data)
	var sf sidecarFile
	if err := yaml.Unmarshal([]byte(content), &sf); err != nil {
		return nil, fmt.Errorf("writeback: unmarshal sidecar %s: %w", sidecarPath, err)
	}
	var presence sidecarPresence
	if err := yaml.Unmarshal([]byte(content), &presence); err != nil {
		return nil, fmt.Errorf("writeback: unmarshal sidecar %s: %w", sidecarPath, err)
	}
	if err := validateSidecarValue(sf, &presence); err != nil {
		return nil, fmt.Errorf("writeback: invalid sidecar %s: %w", sidecarPath, err)
	}
	return &sf, nil
}

// WriteSidecar 是公开的 sidecar 写入入口（供外部测试/复用）。
// mdPath 是 md 文件绝对路径；sidecar 写到 mdPath + ".kg.yaml"。
// WriteSidecar is the public sidecar write entry (for tests/reuse).
func WriteSidecar(mdPath string, sf sidecarFile) error {
	return writeSidecar(mdPath, sf)
}

// ReadSidecar 是公开的 sidecar 读取入口（供增量批次用）。
// ReadSidecar is the public sidecar read entry (for the incremental batch).
func ReadSidecar(mdPath string) (*sidecarFile, error) {
	return readSidecar(mdPath)
}

// NewSidecarFile 构造一个 sidecarFile（供外部测试构造）。
// NewSidecarFile constructs a sidecarFile (for external tests).
func NewSidecarFile(docRelPath, mdContent string, nodes []nodeView, generatedAt time.Time) sidecarFile {
	return buildSidecar(docRelPath, mdContent, nodes, generatedAt)
}

// SidecarNode 是公开的 sidecar node 类型（供外部测试断言字段）。
// SidecarNode is the public sidecar node type (for external test assertions).
type SidecarNode = sidecarNode

// SidecarFile 是公开的 sidecar 文件类型（供外部测试断言字段）。
// SidecarFile is the public sidecar file type (for external test assertions).
type SidecarFile = sidecarFile
