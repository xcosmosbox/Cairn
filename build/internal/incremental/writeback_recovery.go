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
