package incremental

import (
	"context"
	"fmt"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	return expandFailedSourceDocs(ctx, st, rs)
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
