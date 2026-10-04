package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/xcosmosbox/cairn/core/storage"
)

type indexBinding struct {
	IndexProfile   string
	ManifestSHA256 string
	DatabaseSHA256 string
	DatabasePath   string
}

// validateIndexBinding prevents accidentally using Han query normalization on
// the frozen literal index. The manifest is produced by the offline copy tool;
// it is an audit binding to exact bytes, not a signature or trust mechanism.
func validateIndexBinding(dbPath, manifestPath string, profile storage.FTSTextProfile) (indexBinding, error) {
	var binding indexBinding
	if _, err := storage.NormalizeFTSText(profile, ""); err != nil {
		return binding, err
	}
	if manifestPath == "" {
		if profile == storage.FTSTextHanV1 {
			return binding, fmt.Errorf("han-v1 requires --index-manifest for the matching experimental index copy")
		}
		return binding, nil
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return binding, fmt.Errorf("read index manifest: %w", err)
	}
	var manifest struct {
		Format       string                 `json:"format"`
		IndexProfile string                 `json:"index_profile"`
		TextProfile  storage.FTSTextProfile `json:"text_profile"`
		SourceSHA256 string                 `json:"source_db_sha256"`
		OutputSHA256 string                 `json:"output_db_sha256"`
		Unchanged    bool                   `json:"all_non_fts_tables_unchanged"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return binding, fmt.Errorf("parse index manifest: %w", err)
	}
	if manifest.Format != "cairn-fts-index-copy/v1" || !manifest.Unchanged {
		return binding, fmt.Errorf("index manifest must be a verified cairn-fts-index-copy/v1 receipt")
	}
	if manifest.TextProfile != profile {
		return binding, fmt.Errorf("index/query profile mismatch: manifest=%q query=%q", manifest.TextProfile, profile)
	}
	if (profile == storage.FTSTextHanV1 && manifest.IndexProfile != "han-v1") ||
		(profile == storage.FTSTextLiteral && manifest.IndexProfile != "literal" && manifest.IndexProfile != "trigram") {
		return binding, fmt.Errorf("index profile %q is incompatible with text profile %q", manifest.IndexProfile, profile)
	}
	for _, sum := range []string{manifest.SourceSHA256, manifest.OutputSHA256} {
		decoded, err := hex.DecodeString(sum)
		if err != nil || len(decoded) != sha256.Size {
			return binding, fmt.Errorf("index manifest has an invalid database SHA256")
		}
	}
	if manifest.SourceSHA256 == manifest.OutputSHA256 {
		return binding, fmt.Errorf("index manifest must identify a rebuilt copy, not the unmodified source database")
	}
	resolved, err := filepath.EvalSymlinks(dbPath)
	if err != nil {
		return binding, fmt.Errorf("resolve manifest-bound database: %w", err)
	}
	if err := requireStandaloneDB(resolved); err != nil {
		return binding, err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return binding, fmt.Errorf("open database for index binding: %w", err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return binding, fmt.Errorf("hash database for index binding: %w", err)
	}
	actual := hex.EncodeToString(digest.Sum(nil))
	if actual != manifest.OutputSHA256 {
		return binding, fmt.Errorf("index manifest database SHA256 mismatch: refusing an unmatched database")
	}
	if err := requireStandaloneDB(resolved); err != nil {
		return binding, err
	}
	manifestDigest := sha256.Sum256(data)
	return indexBinding{manifest.IndexProfile, hex.EncodeToString(manifestDigest[:]), actual, resolved}, nil
}

// A main-file hash does not bind committed data still in SQLite's WAL. Static
// experimental copies must be checkpointed and closed before retrieval; never
// checkpoint or discard somebody else's active journal from a read-only tool.
func requireStandaloneDB(resolvedPath string) error {
	for _, suffix := range []string{"-wal", "-journal"} {
		info, err := os.Stat(resolvedPath + suffix)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect manifest-bound database journal: %w", err)
		}
		if info.Size() != 0 {
			return fmt.Errorf("manifest-bound database has a nonempty %s sidecar; requires a closed, checkpointed static copy", suffix)
		}
	}
	return nil
}
