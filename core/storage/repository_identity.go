package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

const repositoryIdentityKey = "repository_identity"

// RepositoryIdentityRepo 保存 KG 的稳定仓库归属，而不是短命 worktree 绝对路径。
// RepositoryIdentityRepo persists ownership independently of checkout location.
type RepositoryIdentityRepo struct{ manifest *ManifestRepo }

func NewRepositoryIdentityRepo(db *DB) *RepositoryIdentityRepo {
	return &RepositoryIdentityRepo{manifest: NewManifestRepo(db)}
}

func (r *RepositoryIdentityRepo) Get(ctx context.Context) (string, error) {
	raw, err := r.manifest.Get(ctx, repositoryIdentityKey)
	if err != nil || raw == "" {
		return "", err
	}
	return decodeRepositoryIdentity(raw)
}

func decodeRepositoryIdentity(raw string) (string, error) {
	var record struct {
		Version  int    `json:"version"`
		Identity string `json:"identity"`
	}
	if err := json.Unmarshal([]byte(raw), &record); err != nil || record.Version != 1 || strings.TrimSpace(record.Identity) == "" || strings.TrimSpace(record.Identity) != record.Identity || strings.ContainsAny(record.Identity, "\r\n\x00") {
		return "", fmt.Errorf("RepositoryIdentityRepo.Get: invalid identity record")
	}
	return record.Identity, nil
}

func (r *RepositoryIdentityRepo) Set(ctx context.Context, identity string) error {
	if identity == "" || strings.TrimSpace(identity) != identity || strings.ContainsAny(identity, "\r\n\x00") {
		return fmt.Errorf("RepositoryIdentityRepo.Set: invalid empty/untrimmed identity")
	}
	value, err := json.Marshal(struct {
		Version  int    `json:"version"`
		Identity string `json:"identity"`
	}{Version: 1, Identity: identity})
	if err != nil {
		return err
	}
	db := r.manifest.db
	db.mu.Lock()
	defer db.mu.Unlock()
	// INSERT OR REPLACE would allow concurrent adopters to change ownership.
	// The unique manifest key is the cross-connection compare-and-set boundary.
	if _, err := db.Conn().ExecContext(ctx, "INSERT INTO kg_manifest(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING", repositoryIdentityKey, string(value)); err != nil {
		return fmt.Errorf("RepositoryIdentityRepo.Set: %w", err)
	}
	var raw string
	if err := db.Conn().QueryRowContext(ctx, "SELECT value FROM kg_manifest WHERE key=?", repositoryIdentityKey).Scan(&raw); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("RepositoryIdentityRepo.Set: verify ownership: %w", err)
	}
	previous, err := decodeRepositoryIdentity(raw)
	if err != nil {
		return err
	}
	if previous != identity {
		return fmt.Errorf("RepositoryIdentityRepo.Set: ownership cannot change from %q to %q", previous, identity)
	}
	return nil
}
