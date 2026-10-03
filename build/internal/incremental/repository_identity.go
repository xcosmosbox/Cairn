package incremental

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/repoidentity"
)

// verifyRepositoryIdentity 在只读连接上验证归属。版本存在与部分 UUID 相交都
// 不能证明整个数据库属于当前 repo；旧库必须显式导入并覆盖验证全部来源。
// Legacy adoption requires complete source-path and UUID/member attestation.
func verifyRepositoryIdentity(ctx context.Context, st *stores, repoPath, explicit string, adopt bool) (string, bool, error) {
	stored, err := st.identity.Get(ctx)
	if err != nil {
		return "", false, fmt.Errorf("incremental: read repository identity: %w", err)
	}
	identity, err := repoidentity.Resolve(ctx, repoPath, explicit)
	if err != nil && !(stored == "" && adopt && errors.Is(err, repoidentity.ErrNoIdentity)) {
		return "", false, fmt.Errorf("incremental: resolve repository identity: %w", err)
	}
	if stored != "" {
		if stored != identity {
			return "", false, fmt.Errorf("incremental: repository identity mismatch: KG=%q repo=%q; refusing any mutation", stored, identity)
		}
		if err := verifyManagedRepositoryPaths(ctx, st, repoPath, identity); err != nil {
			return "", false, err
		}
		return identity, false, nil
	}
	if !adopt {
		return "", false, fmt.Errorf("incremental: legacy KG has no repository identity; full rebuild or explicit --adopt-legacy with complete original sidecars is required")
	}
	sources, err := st.sources.ListAll(ctx)
	if err != nil {
		return "", false, fmt.Errorf("incremental: verify legacy sources: %w", err)
	}
	if len(sources) == 0 {
		return "", false, fmt.Errorf("incremental: legacy KG has no source ownership proof; full rebuild required")
	}
	// Cache valid source sidecars, but never sample or tolerate missing documents.
	proof := make(map[string]map[string]map[string]bool)
	for _, source := range sources {
		rel := filepath.ToSlash(source.FilePath)
		clean := filepath.ToSlash(filepath.Clean(source.FilePath))
		if rel == "" || filepath.IsAbs(source.FilePath) || clean != rel || clean == ".." || strings.HasPrefix(clean, "../") {
			return "", false, fmt.Errorf("incremental: invalid legacy source path %q", source.FilePath)
		}
		byID, ok := proof[rel]
		if !ok {
			for _, path := range []string{source.FilePath, source.FilePath + ".kg.yaml"} {
				if err := verifyConfinedLegacyFile(repoPath, path); err != nil {
					return "", false, fmt.Errorf("incremental: legacy ownership unproven: %w", err)
				}
			}
			sc, err := writeback.ReadSidecar(filepath.Join(repoPath, source.FilePath))
			if err != nil {
				return "", false, fmt.Errorf("incremental: legacy ownership unproven for %s: %w", rel, err)
			}
			if sc.Doc != rel {
				return "", false, fmt.Errorf("incremental: legacy sidecar doc mismatch for %s", rel)
			}
			byID = make(map[string]map[string]bool)
			for _, node := range sc.Nodes {
				members := make(map[string]bool)
				for _, member := range node.Members {
					members[member] = true
				}
				byID[node.UUID] = members
			}
			proof[rel] = byID
		}
		members, present := byID[source.NodeUUID]
		if !present || !members[source.MemberID] {
			return "", false, fmt.Errorf("incremental: legacy ownership unproven for %s UUID/member %s/%s", rel, source.NodeUUID, source.MemberID)
		}
	}
	if err := verifyManagedRepositoryPaths(ctx, st, repoPath, identity); err != nil {
		return "", false, err
	}
	return identity, true, nil
}

// Matching ownership is not a license to trust arbitrary ledger paths. Missing
// documents are valid C2 deletions, while traversal/symlinks must stop before I-2.
func verifyManagedRepositoryPaths(ctx context.Context, st *stores, repoPath, identity string) error {
	sources, err := st.sources.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("incremental: validate source paths: %w", err)
	}
	paths := make(map[string]bool)
	for _, source := range sources {
		paths[source.FilePath] = true
	}
	states, err := st.files.ListByRepo(ctx, identity)
	if err != nil {
		return fmt.Errorf("incremental: validate file ledger paths: %w", err)
	}
	for _, state := range states {
		paths[state.FilePath] = true
	}
	primaries, _, err := listExpectedSharedPrimaryDocs(ctx, st)
	if err != nil {
		return fmt.Errorf("incremental: validate primary paths: %w", err)
	}
	for _, path := range primaries {
		paths[path] = true
	}
	for rel := range paths {
		if rel == "." {
			return fmt.Errorf("incremental: managed document path cannot be the repository root")
		}
		for _, path := range []string{rel, rel + ".kg.yaml"} {
			if _, err := writeback.ValidateRepositoryPath(repoPath, path); err != nil {
				return fmt.Errorf("incremental: unsafe managed path %q: %w", path, err)
			}
		}
	}
	return nil
}

func verifyConfinedLegacyFile(repoPath, rel string) error {
	root, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		return err
	}
	current := root
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("source/sidecar %s: %w", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return fmt.Errorf("source/sidecar %s is not a confined regular file (symlinks are not ownership proof)", rel)
		}
	}
	return nil
}
