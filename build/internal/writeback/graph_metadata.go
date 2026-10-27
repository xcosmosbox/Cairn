package writeback

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// SourceReference 是原有来源账本位置，不由生成 Markdown 的行号推算。
// SourceReference preserves the original member/source association verbatim.
type SourceReference struct {
	MemberID        string `yaml:"member_id"`
	Skill           string `yaml:"skill"`
	FilePath        string `yaml:"file_path"`
	StartLine       int    `yaml:"start_line"`
	EndLine         int    `yaml:"end_line"`
	CoordinateSpace string `yaml:"coordinate_space"`
}

// RelationSnapshot 保留存储边全部字段；Description 中的证据/分类保持原样。
// RelationSnapshot is a lossless stored edge, without granting explicit credit.
type RelationSnapshot struct {
	ID            int64                `yaml:"id"`
	SourceID      string               `yaml:"source_id"`
	TargetID      string               `yaml:"target_id"`
	Kind          dktypes.RelationKind `yaml:"kind"`
	Description   string               `yaml:"description"`
	Properties    string               `yaml:"properties"`
	Provenance    dktypes.Provenance   `yaml:"provenance"`
	Confidence    float64              `yaml:"confidence"`
	SourceRefs    string               `yaml:"source_refs"`
	Bidirectional int                  `yaml:"bidirectional"`
	Cardinality   string               `yaml:"cardinality"`
	CreatedAt     time.Time            `yaml:"created_at"`
}

// Edge 返回完整边快照；调用者仍须使用真实选择结果判断明示信用。
// Edge reconstructs the exact persisted edge; it does not verify its assertions.
func (r RelationSnapshot) Edge() dktypes.Edge {
	return dktypes.Edge{ID: r.ID, SourceID: r.SourceID, TargetID: r.TargetID, Kind: r.Kind, Description: r.Description, Properties: r.Properties, Provenance: r.Provenance, Confidence: r.Confidence, SourceRefs: r.SourceRefs, Bidirectional: r.Bidirectional, Cardinality: r.Cardinality, CreatedAt: r.CreatedAt}
}

// GeneratedSpan 仅定位当前生成文档，绝不是原文证据。
// GeneratedSpan locates a generated block, never original source evidence.
type GeneratedSpan struct {
	CoordinateSpace string `yaml:"coordinate_space"`
	StartLine       int    `yaml:"start_line"`
	EndLine         int    `yaml:"end_line"`
}

// GraphMetadata 是 schema1 的可选只读扩展；缺省旧 sidecar 仍可读取。
// GraphMetadata adds source/edge readback without changing editable Markdown.
type GraphMetadata struct {
	Version        int                `yaml:"version"`
	SourcesKnown   bool               `yaml:"sources_known"`
	Sources        []SourceReference  `yaml:"sources"`
	RelationsKnown bool               `yaml:"relations_known"`
	Relations      []RelationSnapshot `yaml:"relations"`
	GeneratedSpan  *GeneratedSpan     `yaml:"generated_span,omitempty"`
}

type graphMetadataPresence struct {
	Version        *int                `yaml:"version"`
	SourcesKnown   *bool               `yaml:"sources_known"`
	Sources        *[]SourceReference  `yaml:"sources"`
	RelationsKnown *bool               `yaml:"relations_known"`
	Relations      *[]RelationSnapshot `yaml:"relations"`
}

// IncidentRelations 精确保留所有相邻行（包括相同三元组旧行）。nil 表示调用者未知。
// IncidentRelations retains every incident row, including duplicate triples.
func IncidentRelations(edges []*dktypes.Edge, uuid string) []*dktypes.Edge {
	if edges == nil {
		return nil
	}
	out := make([]*dktypes.Edge, 0)
	for _, e := range edges {
		if e != nil && (e.SourceID == uuid || e.TargetID == uuid) {
			cp := *e
			out = append(out, &cp)
		}
	}
	return out
}

func buildGraphMetadata(v nodeView, md string) *GraphMetadata {
	g := &GraphMetadata{Version: 1, SourcesKnown: v.Sources != nil, Sources: canonicalSourceReferences(v.Sources), RelationsKnown: v.Relations != nil, Relations: []RelationSnapshot{}}
	if g.Sources == nil {
		g.Sources = []SourceReference{}
	}
	for _, e := range v.Relations {
		if e == nil {
			continue
		}
		g.Relations = append(g.Relations, RelationSnapshot{e.ID, e.SourceID, e.TargetID, e.Kind, e.Description, e.Properties, e.Provenance, e.Confidence, e.SourceRefs, e.Bidirectional, e.Cardinality, e.CreatedAt})
	}
	sort.SliceStable(g.Relations, func(i, j int) bool {
		if g.Relations[i].ID != g.Relations[j].ID {
			return g.Relations[i].ID < g.Relations[j].ID
		}
		a, _ := json.Marshal(g.Relations[i])
		b, _ := json.Marshal(g.Relations[j])
		return string(a) < string(b)
	})
	for i, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "<!-- kg:uuid="+v.UUID+" ") {
			g.GeneratedSpan = &GeneratedSpan{CoordinateSpace: "generated_document", StartLine: i + 1}
		}
		if line == CanonicalFullCloseComment(v.UUID) && g.GeneratedSpan != nil {
			g.GeneratedSpan.EndLine = i + 1
			break
		}
	}
	return g
}
func validateGraphMetadataPresence(p *graphMetadataPresence) error {
	if p.Version == nil || p.SourcesKnown == nil || p.Sources == nil || p.RelationsKnown == nil || p.Relations == nil {
		return fmt.Errorf("incomplete graph_metadata")
	}
	return nil
}
func validateGraphMetadata(uuid string, g *GraphMetadata) error {
	if g == nil {
		return nil
	}
	if g.Sources == nil || g.Relations == nil {
		return fmt.Errorf("node %s: incomplete graph_metadata arrays", uuid)
	}
	if g.Version != 1 {
		return fmt.Errorf("node %s: unsupported graph_metadata version", uuid)
	}
	if !g.SourcesKnown && len(g.Sources) > 0 {
		return fmt.Errorf("node %s: unknown sources carry rows", uuid)
	}
	for _, s := range g.Sources {
		if s.MemberID == "" || s.FilePath == "" || s.CoordinateSpace != "source_document" {
			return fmt.Errorf("node %s: invalid source reference", uuid)
		}
	}
	if !g.RelationsKnown && len(g.Relations) > 0 {
		return fmt.Errorf("node %s: unknown relations carry rows", uuid)
	}
	for _, r := range g.Relations {
		if r.SourceID != uuid && r.TargetID != uuid {
			return fmt.Errorf("node %s: unrelated relation snapshot", uuid)
		}
	}
	if s := g.GeneratedSpan; s != nil && (s.CoordinateSpace != "generated_document" || s.StartLine < 1 || s.EndLine < s.StartLine) {
		return fmt.Errorf("node %s: invalid generated span", uuid)
	}
	return nil
}
func graphMetadataEqual(a, b *GraphMetadata) bool { return reflect.DeepEqual(a, b) }
func graphMetadataMatches(got, want *GraphMetadata) bool {
	// Legacy omission is accepted. A declared extension must remain complete.
	if got == nil {
		return true
	}
	if want == nil {
		return false
	}
	if got.Version != want.Version {
		return false
	}
	if got.SourcesKnown && want.SourcesKnown && !reflect.DeepEqual(got.Sources, want.Sources) {
		return false
	}
	// A caller lacking edges cannot certify or invalidate relation snapshots.
	if want.RelationsKnown && got.RelationsKnown && !reflect.DeepEqual(got.Relations, want.Relations) {
		return false
	}
	return true
}

// RefreshGraphMetadataContext 更新只读来源/关系快照，不吸收尚未处理的人工正文为 baseline。
// RefreshGraphMetadataContext preserves editable hashes/doc_hash, including later human edits.
func RefreshGraphMetadataContext(ctx context.Context, root, doc string, nodes []NodeUpdate) error {
	path, err := safeRepoPath(root, doc)
	if err != nil {
		return err
	}
	sc, err := ReadSidecar(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	views := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		views = append(views, n.toNodeView())
	}
	expected := buildSidecar(doc, string(data), views, time.Time{})
	byID := map[string]sidecarNode{}
	for _, n := range expected.Nodes {
		byID[n.UUID] = n
	}
	if len(byID) != len(sc.Nodes) {
		return fmt.Errorf("writeback: metadata refresh membership changed: %s", doc)
	}
	for i := range sc.Nodes {
		want, ok := byID[sc.Nodes[i].UUID]
		if !ok {
			return fmt.Errorf("writeback: metadata refresh node missing: %s", doc)
		}
		if !want.GraphMetadata.RelationsKnown && sc.Nodes[i].GraphMetadata != nil {
			want.GraphMetadata.RelationsKnown = sc.Nodes[i].GraphMetadata.RelationsKnown
			want.GraphMetadata.Relations = append([]RelationSnapshot{}, sc.Nodes[i].GraphMetadata.Relations...)
		}
		sc.Nodes[i].GraphMetadata = want.GraphMetadata
		sc.Nodes[i].Span = want.Span
	}
	sc.Doc = doc
	return writeSidecarContext(ctx, path, *sc)
}

func cloneSourceReferences(sources []SourceReference) []SourceReference {
	if sources == nil {
		return nil
	}
	return append([]SourceReference{}, sources...)
}

// Match node_sources' one association per member/file: repeated annotation
// occurrences use one genuine representative hint, never a fabricated range.
func canonicalSourceReferences(sources []SourceReference) []SourceReference {
	if sources == nil {
		return nil
	}
	type key struct{ member, file, skill string }
	byKey := map[key]SourceReference{}
	for _, source := range sources {
		k := key{source.MemberID, source.FilePath, source.Skill}
		previous, ok := byKey[k]
		if !ok || earlierSourceReference(source, previous) {
			byKey[k] = source
		}
	}
	out := make([]SourceReference, 0, len(byKey))
	for _, source := range byKey {
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.MemberID != b.MemberID {
			return a.MemberID < b.MemberID
		}
		if a.FilePath != b.FilePath {
			return a.FilePath < b.FilePath
		}
		return a.Skill < b.Skill
	})
	return out
}
func earlierSourceReference(a, b SourceReference) bool {
	rank := func(s SourceReference) int {
		if s.StartLine > 0 && s.EndLine >= s.StartLine {
			return 0
		}
		if s.StartLine > 0 || s.EndLine > 0 {
			return 1
		}
		return 2
	}
	if rank(a) != rank(b) {
		return rank(a) < rank(b)
	}
	if a.StartLine != b.StartLine {
		return a.StartLine < b.StartLine
	}
	return a.EndLine < b.EndLine
}
