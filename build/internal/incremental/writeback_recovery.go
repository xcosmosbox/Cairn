package incremental

import (
	"context"
	"fmt"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// collectOwnedWritebacks is read-only. Verify ownership and every journal path
// before recovering any pair; foreign pending documents cannot become new nodes.
func collectOwnedWritebacks(ctx context.Context, st *stores, root, identity string) ([]string, error) {
	allowed := map[string]bool{}
	sources, err := st.sources.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range sources {
		allowed[s.FilePath] = true
	}
	files, err := st.files.ListByRepo(ctx, identity)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		allowed[f.FilePath] = true
	}
	primaries, _, err := listExpectedSharedPrimaryDocs(ctx, st)
	if err != nil {
		return nil, err
	}
	for _, p := range primaries {
		allowed[p] = true
	}
	var journals []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !strings.HasSuffix(path, ".kg-writeback.json") {
			return nil
		}
		rel, err := filepath.Rel(root, strings.TrimSuffix(path, ".kg-writeback.json"))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !allowed[rel] {
			return fmt.Errorf("incremental: pending writeback has no owned ledger path: %s", rel)
		}
		for _, p := range []string{rel, rel + ".kg.yaml", rel + ".kg-writeback.json"} {
			if _, err := writeback.ValidateRepositoryPath(root, p); err != nil {
				return err
			}
		}
		journals = append(journals, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(journals)
	return journals, nil
}
func recoverOwnedWritebacks(ctx context.Context, st *stores, root, identity string) error {
	journals, err := collectOwnedWritebacks(ctx, st, root, identity)
	if err != nil {
		return err
	}
	for _, doc := range journals {
		if err := writeback.RecoverDocPairContext(ctx, root, doc); err != nil {
			return fmt.Errorf("incremental: recover owned writeback %s: %w", doc, err)
		}
	}
	return nil
}

// recoverIndependentWritebacks 保留冲突 journal 与后来人工改动，仅标记已有来源依赖。
// A conflicted pair blocks its source-sharing documents, not unrelated documents.
func recoverIndependentWritebacks(ctx context.Context, st *stores, root, identity string, rs *runState) error {
	journals, err := collectOwnedWritebacks(ctx, st, root, identity)
	if err != nil {
		return err
	}
	for _, doc := range journals {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeback.RecoverDocPairContext(ctx, root, doc); err != nil {
			rs.failDoc(doc)
			rs.warnf(fmt.Sprintf("recover owned writeback %s: %v", doc, err))
		}
	}
	// Journal conflicts also block sharing siblings before any metadata refresh.
	if err := expandFailedSourceDocs(ctx, st, rs); err != nil {
		return err
	}
	if err := recoverRelationSnapshots(ctx, st, root, identity, rs); err != nil {
		return err
	}
	return expandFailedSourceDocs(ctx, st, rs)
}

// recoverRelationSnapshots 修复已提交图谱之后失败的只读关系缓存写入，保留人工基线。
// Run-only owned recovery: detection stays read-only, and core identity/hash guards stay exact.
func recoverRelationSnapshots(ctx context.Context, st *stores, root, identity string, rs *runState) error {
	paths := map[string]bool{}
	sources, err := st.sources.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, source := range sources {
		paths[source.FilePath] = true
	}
	files, err := st.files.ListByRepo(ctx, identity)
	if err != nil {
		return err
	}
	for _, file := range files {
		paths[file.FilePath] = true
	}
	_, primaryByPath, err := listExpectedSharedPrimaryDocs(ctx, st)
	if err != nil {
		return err
	}
	for path := range primaryByPath {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		if rs.failedDocsSnapshot()[path] {
			continue
		}
		abs, err := writeback.ValidateRepositoryPath(root, path)
		if err != nil {
			return err
		}
		if _, err := writeback.ValidateRepositoryPath(root, path+".kg.yaml"); err != nil {
			return err
		}
		if _, err := os.ReadFile(abs); err != nil {
			continue // Missing/deleted or unreadable files retain their existing detection path.
		}
		sc, err := writeback.ReadSidecar(abs)
		if err != nil {
			continue // Corrupt/missing sidecars cannot prove a trusted editable baseline.
		}
		primaryID := primaryByPath[path]
		views, err := authoritativeSidecarViews(ctx, st, path, primaryID != "", primaryID)
		if err != nil {
			return err
		}
		if primaryID != "" {
			// Legacy primary views omit relations; recovery has authoritative DB context.
			edges, err := st.edges.ListAll(ctx)
			if err != nil {
				return err
			}
			for i := range views {
				views[i].Relations = writeback.IncidentRelations(edges, views[i].UUID)
				if views[i].Relations == nil {
					views[i].Relations = []*dktypes.Edge{}
				}
			}
		}
		if writeback.SidecarMetadataMatches(*sc, path, views) {
			continue
		}
		// 只允许派生的关系缓存过期：路径/UUID/member/原始来源/只读字段/可编辑
		// hash 仍须与 SQLite 精确一致；绝不据此接受新的人工来源或伪造基线。
		// Compare every existing identity and baseline guard before refreshing relations.
		withoutRelations := append([]writeback.NodeUpdate(nil), views...)
		for i := range withoutRelations {
			withoutRelations[i].Relations = nil
		}
		if !writeback.SidecarMetadataMatches(*sc, path, withoutRelations) {
			continue
		}
		refreshErr := writeback.RefreshGraphMetadataContext(ctx, root, path, views)
		if refreshErr == nil {
			fresh, err := writeback.ReadSidecar(abs)
			refreshErr = err
			if err == nil && !writeback.SidecarMetadataMatches(*fresh, path, views) {
				refreshErr = fmt.Errorf("refreshed relation metadata still differs from authoritative graph")
			}
		}
		if refreshErr != nil {
			rs.failDoc(path)
			rs.warnf(fmt.Sprintf("recover relation snapshot %s: %v", path, refreshErr))
			continue
		}
		log.Printf("[incremental] refreshed derived relation snapshot, preserving editable baseline: %s", path)
	}
	return nil
}

// expandFailedSourceDocs 使用 node_sources 已有共享关系闭包；不引入恢复状态。
// Reuse source ownership to retain baselines of documents sharing failed nodes.
func expandFailedSourceDocs(ctx context.Context, st *stores, rs *runState) error {
	if len(rs.failedDocsSnapshot()) == 0 {
		return nil
	}
	sources, err := st.sources.ListAll(ctx)
	if err != nil {
		return err
	}
	_, primaryByPath, err := listExpectedSharedPrimaryDocs(ctx, st)
	if err != nil {
		return err
	}
	failed := rs.failedDocsSnapshot()
	for changed := true; changed; {
		changed = false
		nodes := map[string]bool{}
		for path := range failed {
			if id := primaryByPath[path]; id != "" {
				nodes[id] = true
			}
		}
		for _, src := range sources {
			if failed[src.FilePath] {
				nodes[src.NodeUUID] = true
			}
		}
		for path, id := range primaryByPath {
			if nodes[id] && !failed[path] {
				failed[path] = true
				changed = true
			}
		}
		for _, src := range sources {
			if nodes[src.NodeUUID] && !failed[src.FilePath] {
				failed[src.FilePath] = true
				changed = true
			}
		}
	}
	for path := range failed {
		rs.failDoc(path)
	}
	return nil
}

func excludeFailedChanges(changes []*DocChangeSet, sr *scanResult, rs *runState) []*DocChangeSet {
	failed := rs.failedDocsSnapshot()
	out := changes[:0]
	for _, cs := range changes {
		if !failed[cs.Path] {
			out = append(out, cs)
		}
	}
	moves := sr.moves[:0]
	for _, move := range sr.moves {
		if !failed[move.OldPath] && !failed[move.NewPath] {
			moves = append(moves, move)
		}
	}
	sr.moves = moves
	return out
}

func omitBlockedPending(ctx context.Context, st *stores, ao *alignOutcome, rs *runState) error {
	if ao == nil || len(rs.failedDocsSnapshot()) == 0 {
		return nil
	}
	failed := rs.failedDocsSnapshot()
	for uuid := range ao.pending {
		sources, err := st.sources.ListByNode(ctx, uuid)
		if err != nil {
			return err
		}
		blocked := false
		for _, src := range sources {
			blocked = blocked || failed[src.FilePath]
		}
		if !blocked {
			continue
		}
		for _, row := range ao.sourceRows {
			if row.NodeUUID == uuid {
				rs.failDoc(row.FilePath)
			}
		}
		delete(ao.pending, uuid)
		rs.warnf(fmt.Sprintf("pending node %s shares a conflicted source; leaving node unchanged", uuid))
	}
	return nil
}
