package writeback

import (
	"context"
	"fmt"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// AuthoritativeNodeUpdates从已落库图建立与I-9相同的视图，保留人工正文及真实来源。
// Full-over-existing must not render the pre-preservation machine candidate.
func AuthoritativeNodeUpdates(ctx context.Context, db *storage.DB) ([]NodeUpdate, error) {
	nodes, err := storage.NewNodeRepo(db).ListAll(ctx)
	if err != nil {
		return nil, err
	}
	sources, err := storage.NewNodeSourceRepo(db).ListAll(ctx)
	if err != nil {
		return nil, err
	}
	edges, err := storage.NewEdgeRepo(db).ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if edges == nil {
		edges = []*dktypes.Edge{}
	}
	byID := map[string]*dktypes.Node{}
	byNode := map[string][]storage.NodeSource{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, s := range sources {
		byNode[s.NodeUUID] = append(byNode[s.NodeUUID], s)
	}
	var updates []NodeUpdate
	for _, n := range nodes {
		if n.Label != dktypes.LabelEntity && n.Label != dktypes.LabelConcept {
			continue
		}
		u := NodeUpdate{UUID: n.ID, FileSlug: n.FileSlug, Tag: string(n.Label), Name: n.Name, Domain: n.Domain, Subdomain: n.Subdomain, DomainSlug: n.Domain, SubdomainSlug: n.Subdomain, Summary: n.Summary, Description: n.Description, Provenance: string(n.Provenance), Sources: []SourceReference{}, Relations: IncidentRelations(edges, n.ID)}
		if layer := byID["domain::"+n.Domain]; layer != nil {
			u.Domain = layer.Name
		}
		if layer := byID["subdomain::"+n.Domain+"::"+n.Subdomain]; layer != nil {
			u.Subdomain = layer.Name
		}
		members, files := map[string]bool{}, map[string]bool{}
		for _, s := range byNode[n.ID] {
			members[s.MemberID] = true
			files[s.FilePath] = true
			u.Sources = append(u.Sources, SourceReference{MemberID: s.MemberID, Skill: s.Skill, FilePath: s.FilePath, StartLine: s.StartLine, EndLine: s.EndLine, CoordinateSpace: "source_document"})
			if u.SpanStart == 0 && s.StartLine > 0 && s.EndLine >= s.StartLine {
				u.SpanStart = s.StartLine
				u.SpanEnd = s.EndLine
			}
		}
		for m := range members {
			u.Members = append(u.Members, m)
		}
		sort.Strings(u.Members)
		for f := range files {
			u.SourceFiles = append(u.SourceFiles, f)
		}
		sort.Strings(u.SourceFiles)
		u.Shared = IsSharedSourceFiles(u.SourceFiles)
		updates = append(updates, u)
	}
	return updates, nil
}

// WritebackAuthoritativeContext复用Full的pair写入，只处理实际受影响的来源文档。
// Keep every surviving node in a touched document, including retained human nodes.
func WritebackAuthoritativeContext(ctx context.Context, updates []NodeUpdate, docs, refresh, empty map[string]bool, repoRoot string) (*Report, error) {
	views := make([]nodeView, 0, len(updates))
	for _, u := range updates {
		views = append(views, u.toNodeView())
	}
	report, err := writebackViewsContext(ctx, views, docs, repoRoot)
	if err != nil {
		return report, err
	}
	// Only an explicitly accepted formerly-owned source may clear its stale sidecar.
	// Preserve its exact current input/frontmatter rather than rendering an empty managed body.
	for doc := range empty {
		path, err := safeRepoPath(repoRoot, doc)
		if err != nil {
			return report, err
		}
		current, err := os.ReadFile(path)
		if err != nil {
			return report, fmt.Errorf("writeback: accepted empty source unavailable %s: %w", doc, err)
		}
		if err := WriteDocContentContext(ctx, repoRoot, doc, string(current), []NodeUpdate{}, time.Now()); err != nil {
			return report, err
		}
		report.DocsWritten++
		report.SidecarsWritten++
	}
	for doc := range refresh {
		var selected []NodeUpdate
		for _, u := range updates {
			for _, file := range u.SourceFiles {
				if file == doc {
					selected = append(selected, u)
					break
				}
			}
		}
		// Metadata-only refresh cannot safely change mirror roles/member ownership.
		// Keep pending edits and report this dependent file partial instead of forging a new baseline.
		sf, err := ReadSidecar(filepath.Join(repoRoot, doc))
		if err != nil {
			return report, fmt.Errorf("writeback: retained source metadata unavailable %s: %w", doc, err)
		}
		known := map[string]SidecarNode{}
		for _, n := range sf.Nodes {
			known[n.UUID] = n
		}
		for _, u := range selected {
			n, ok := known[u.UUID]
			members := append([]string(nil), n.Members...)
			sort.Strings(members)
			if !ok || n.Shared != u.Shared || n.FileSlug != u.FileSlug || !stringSlicesEqual(members, u.Members) {
				return report, fmt.Errorf("writeback: retained source %s changed member/shared identity; prior editable baseline retained", doc)
			}
		}
		// Preserve pending human Markdown and its editable baseline on neighbor-only refresh.
		if err := RefreshGraphMetadataContext(ctx, repoRoot, doc, selected); err != nil {
			return report, err
		}
		report.SidecarsWritten++
	}
	return report, nil
}
