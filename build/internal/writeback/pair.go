package writeback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"os"
	"path/filepath"
)

// 两个 rename 不能组成文件系统事务。journal 在首次替换之前 fsync，普通失败
// 回滚原文件；崩溃重入仅在文件仍为旧/新版本时完成整对，避免覆盖后来的人改。
// A durable pair journal completes interrupted writes without overwriting new edits.
type pairJournal struct {
	MD         []byte `json:"md"`
	Sidecar    []byte `json:"sidecar"`
	OldMD      []byte `json:"old_md"`
	OldSidecar []byte `json:"old_sidecar"`
	HadMD      bool   `json:"had_md"`
	HadSidecar bool   `json:"had_sidecar"`
}

func readRegular(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("writeback: target is not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	return data, true, err
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func writeDocPair(mdPath string, md []byte, sf sidecarFile) error {
	return writeDocPairContext(context.Background(), mdPath, md, sf)
}

func writeDocPairContext(ctx context.Context, mdPath string, md []byte, sf sidecarFile) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := recoverDocPairContext(ctx, mdPath); err != nil {
		return err
	}
	sidecar, err := encodeSidecar(sf)
	if err != nil {
		return err
	}
	if sf.DocHash != sha256Hex(string(md)) {
		return fmt.Errorf("writeback: sidecar document hash mismatch: %s", mdPath)
	}
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(mdPath), 0o755); err != nil {
		return err
	}
	oldMD, hadMD, err := readRegular(mdPath)
	if err != nil {
		return err
	}
	oldSC, hadSC, err := readRegular(mdPath + ".kg.yaml")
	if err != nil {
		return err
	}
	j := pairJournal{MD: md, Sidecar: sidecar, OldMD: oldMD, OldSidecar: oldSC, HadMD: hadMD, HadSidecar: hadSC}
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	journalPath := mdPath + ".kg-writeback.json"
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := atomicWriteFileContext(ctx, journalPath, data, 0o600); err != nil {
		return err
	}
	if err := completePairContext(ctx, mdPath, j); err != nil {
		if leaseErr := store.CheckLease(ctx); leaseErr != nil {
			return errors.Join(err, leaseErr)
		}
		rollback := errors.Join(restorePairFileContext(ctx, mdPath, oldMD, hadMD), restorePairFileContext(ctx, mdPath+".kg.yaml", oldSC, hadSC))
		if rollback == nil {
			rollback = os.Remove(journalPath)
		}
		return errors.Join(err, rollback)
	}
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := os.Remove(journalPath); err != nil {
		return err
	}
	return syncDir(filepath.Dir(mdPath))
}

func restorePairFileContext(ctx context.Context, path string, data []byte, existed bool) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if existed {
		return atomicWriteFileContext(ctx, path, data, 0o644)
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func completePairContext(ctx context.Context, mdPath string, j pairJournal) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := atomicWriteFileContext(ctx, mdPath, j.MD, 0o644); err != nil {
		return err
	}
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	return atomicWriteFileContext(ctx, mdPath+".kg.yaml", j.Sidecar, 0o644)
}

func recoverDocPair(mdPath string) error { return recoverDocPairContext(context.Background(), mdPath) }

func recoverDocPairContext(ctx context.Context, mdPath string) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	path := mdPath + ".kg-writeback.json"
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var j pairJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("writeback: invalid pair journal %s: %w", path, err)
	}
	var sf sidecarFile
	if err := unmarshalJournalSidecar(j.Sidecar, &sf); err != nil {
		return err
	}
	if sf.DocHash != sha256Hex(string(j.MD)) {
		return fmt.Errorf("writeback: corrupt pair journal %s", path)
	}
	for _, target := range []struct {
		path      string
		old, next []byte
		existed   bool
	}{
		{mdPath, j.OldMD, j.MD, j.HadMD}, {mdPath + ".kg.yaml", j.OldSidecar, j.Sidecar, j.HadSidecar},
	} {
		current, exists, err := readRegular(target.path)
		if err != nil {
			return err
		}
		oldMatches := exists == target.existed && string(current) == string(target.old)
		newMatches := exists && string(current) == string(target.next)
		if !oldMatches && !newMatches {
			return fmt.Errorf("writeback: interrupted pair has newer edits; refusing recovery: %s", target.path)
		}
	}
	if err := completePairContext(ctx, mdPath, j); err != nil {
		return err
	}
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(mdPath))
}

// RecoverDocPair completes a pending pair only after the caller has verified repository ownership.
// ReadSidecar and validation deliberately never invoke recovery.
func RecoverDocPair(repoRoot, docRel string) error {
	return RecoverDocPairContext(context.Background(), repoRoot, docRel)
}

func RecoverDocPairContext(ctx context.Context, repoRoot, docRel string) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	path, err := ValidateRepositoryPath(repoRoot, docRel)
	if err != nil {
		return err
	}
	if _, err := ValidateRepositoryPath(repoRoot, docRel+".kg.yaml"); err != nil {
		return err
	}
	if _, err := ValidateRepositoryPath(repoRoot, docRel+".kg-writeback.json"); err != nil {
		return err
	}
	return recoverDocPairContext(ctx, path)
}
