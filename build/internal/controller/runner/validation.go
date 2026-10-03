package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// 发布验证要覆盖源闭环；quick_check 只能证明 SQLite 页结构完整，不能证明
// 来源账本、Markdown 和 sidecar 与图谱一致。
// validateCandidate checks database structure and source materialization together.
func validateCandidate(ctx context.Context, req ValidateRequest) (ValidationReport, error) {
	vr, err := validateDB(req.DBPath)
	if err != nil {
		return vr, err
	}
	db, err := storage.OpenReadOnly(req.DBPath)
	if err != nil {
		return vr, err
	}
	defer db.Close()
	if err := storage.ValidateQueryContract(ctx, db); err != nil {
		vr.Errors = append(vr.Errors, "query contract: "+err.Error())
	}
	version, err := db.SchemaVersion()
	if err != nil {
		return vr, err
	}
	if version != storage.LatestSchemaVersion() {
		vr.Errors = append(vr.Errors, fmt.Sprintf("schema version: actual %d expected %d", version, storage.LatestSchemaVersion()))
	}
	for _, check := range []struct{ name, sql string }{
		{"edge endpoints", `SELECT COUNT(*) FROM edges e LEFT JOIN nodes s ON s.id=e.source_id LEFT JOIN nodes t ON t.id=e.target_id WHERE s.id IS NULL OR t.id IS NULL`},
		{"node sources", `SELECT COUNT(*) FROM node_sources s LEFT JOIN nodes n ON n.id=s.node_uuid WHERE n.id IS NULL OR n.label NOT IN ('Entity','Concept')`},
	} {
		var count int
		if err := db.Conn().QueryRowContext(ctx, check.sql).Scan(&count); err != nil {
			vr.Errors = append(vr.Errors, check.name+": "+err.Error())
		} else if count > 0 {
			vr.Errors = append(vr.Errors, fmt.Sprintf("%s: %d invalid rows", check.name, count))
		}
		vr.Checks = append(vr.Checks, check.name)
	}
	if req.RepoPath != "" {
		if err := validateMaterialization(ctx, db, req.RepoPath); err != nil {
			vr.Errors = append(vr.Errors, "source closure: "+err.Error())
		}
		vr.Checks = append(vr.Checks, "source_closure")
	}
	vr.OK = len(vr.Errors) == 0
	return vr, nil
}

func validateMaterialization(ctx context.Context, db *storage.DB, root string) error {
	nodes, err := storage.NewNodeRepo(db).ListAll(ctx)
	if err != nil {
		return err
	}
	byID := map[string]*dktypes.Node{}
	domainNames, subdomainNames := map[string]string{}, map[string]string{}
	for _, node := range nodes {
		byID[node.ID] = node
		switch node.Label {
		case dktypes.LabelDomain:
			domainNames[node.Domain] = node.Name
		case dktypes.LabelSubdomain:
			subdomainNames[node.Domain+"\x00"+node.Subdomain] = node.Name
		}
	}
	sources, err := storage.NewNodeSourceRepo(db).ListAll(ctx)
	if err != nil {
		return err
	}
	sourcesByNode := map[string][]storage.NodeSource{}
	docs := map[string]map[string]bool{}
	for _, source := range sources {
		if node := byID[source.NodeUUID]; node == nil || (node.Label != dktypes.LabelEntity && node.Label != dktypes.LabelConcept) {
			return fmt.Errorf("source ledger references invalid node %s", source.NodeUUID)
		}
		if docs[source.FilePath] == nil {
			docs[source.FilePath] = map[string]bool{}
		}
		docs[source.FilePath][source.NodeUUID] = true
		sourcesByNode[source.NodeUUID] = append(sourcesByNode[source.NodeUUID], source)
	}
	updates := map[string]writeback.NodeUpdate{}
	primaries := map[string]string{}
	for _, node := range nodes {
		if node.Label != dktypes.LabelEntity && node.Label != dktypes.LabelConcept {
			continue
		}
		srcs := sourcesByNode[node.ID]
		if len(srcs) == 0 {
			return fmt.Errorf("node %s has no source ledger", node.ID)
		}
		files, members := map[string]bool{}, map[string]bool{}
		for _, source := range srcs {
			files[source.FilePath] = true
			if source.MemberID != "" {
				members[source.MemberID] = true
			}
		}
		domain, subdomain := domainNames[node.Domain], subdomainNames[node.Domain+"\x00"+node.Subdomain]
		if domain == "" {
			domain = node.Domain
		}
		if subdomain == "" {
			subdomain = node.Subdomain
		}
		u := writeback.NodeUpdate{
			UUID: node.ID, FileSlug: node.FileSlug, Tag: string(node.Label), Name: node.Name,
			Domain: domain, Subdomain: subdomain,
			DomainSlug: node.Domain, SubdomainSlug: node.Subdomain,
			Summary: node.Summary, Description: node.Description,
			Provenance: string(node.Provenance),
			Members:    sortedKeys(members), SourceFiles: sortedKeys(files),
		}
		u.Shared = writeback.IsSharedSourceFiles(u.SourceFiles)
		updates[node.ID] = u
		if u.Shared {
			primary := writeback.PrimaryRelPath(node.Domain, node.FileSlug, node.ID)
			if primary == "" {
				return fmt.Errorf("node %s has no valid primary path", node.ID)
			}
			if _, source := docs[primary]; source {
				return fmt.Errorf("primary %s appears in source ledger", primary)
			}
			if previous, exists := primaries[primary]; exists && previous != node.ID {
				return fmt.Errorf("primary %s collides for nodes %s/%s", primary, previous, node.ID)
			}
			primaries[primary] = node.ID
			docs[primary] = map[string]bool{node.ID: true}
		}
	}
	// 原子文件对未完成不得成为 stable；这里只验证，不在发布门禁中回放写入。
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".kg-writeback.json") {
			return fmt.Errorf("unfinished writeback journal: %s", path)
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".md.kg.yaml") {
			rel, err := filepath.Rel(root, strings.TrimSuffix(path, ".kg.yaml"))
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if _, ok := docs[rel]; !ok {
				docs[rel] = nil
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for doc, expected := range docs {
		path, err := writeback.ValidateRepositoryPath(root, doc)
		if err != nil {
			return fmt.Errorf("unsafe source path %q: %w", doc, err)
		}
		if _, err := writeback.ValidateRepositoryPath(root, doc+".kg.yaml"); err != nil {
			return fmt.Errorf("unsafe sidecar path %q: %w", doc, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("source %s: %w", doc, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source %s is not regular", doc)
		}
		md, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sf, err := writeback.ReadSidecar(path)
		if err != nil {
			return fmt.Errorf("source %s: %w", doc, err)
		}
		if sf.Doc != doc || sf.DocHash != writeback.HashEditable(string(md)) {
			return fmt.Errorf("source %s baseline differs from document", doc)
		}
		changes := incremental.DiffDoc(doc, sf, string(md), false)
		if changes.HasChanges() {
			return fmt.Errorf("source %s has unmaterialized C1/C2/C3/C4 changes", doc)
		}
		actual := map[string]bool{}
		var expectedUpdates []writeback.NodeUpdate
		for _, sn := range sf.Nodes {
			actual[sn.UUID] = true
			u, exists := updates[sn.UUID]
			if !exists {
				return fmt.Errorf("source %s references absent node %s", doc, sn.UUID)
			}
			// Full rebuilds use the first global source span, while incremental
			// source rewrites use a document-local span. Both must be ledger-backed.
			positive := false
			spanMatches := sn.Span == nil
			for _, source := range sourcesByNode[sn.UUID] {
				if source.StartLine > 0 {
					positive = true
					if sn.Span != nil && sn.Span.StartLine == source.StartLine && sn.Span.EndLine == source.EndLine {
						spanMatches = true
					}
				}
			}
			if (sn.Span == nil && positive) || !spanMatches {
				return fmt.Errorf("source %s node %s span differs from source ledger", doc, sn.UUID)
			}
			if sn.Span != nil {
				u.SpanStart, u.SpanEnd = sn.Span.StartLine, sn.Span.EndLine
			}
			expectedUpdates = append(expectedUpdates, u)
		}
		if !writeback.SidecarMetadataMatches(*sf, doc, expectedUpdates) {
			return fmt.Errorf("source %s sidecar metadata differs from KG/source ledger", doc)
		}
		if expected == nil {
			if incremental.IsSharedPrimaryPath(doc) || len(actual) != 0 {
				return fmt.Errorf("sidecar %s has no authoritative source/primary ledger", doc)
			}
			continue // Canonical empty source after a complete C2 block deletion.
		}
		if len(expected) != len(actual) {
			return fmt.Errorf("source %s node set differs from source ledger", doc)
		}
		for id := range expected {
			if !actual[id] {
				return fmt.Errorf("source %s misses ledger node %s", doc, id)
			}
		}
	}
	return nil
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
