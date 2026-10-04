package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SourceDocumentMove is an attested physical rename, not a new contribution.
// Sources is the exact preflight ledger snapshot; UUIDs, members, skills and
// spans remain unchanged. An empty PreviousHash means no old file_state existed.
type SourceDocumentMove struct {
	OldPath, NewPath string
	Sources          []NodeSource
	PreviousHash     string
	ContentHash      string
}

// MoveSourceDocuments commits the entire validated rename batch atomically.
// Never merge with an occupied destination or silently overwrite source rows.
// Filesystem sidecars are rebound afterwards; a crash leaves the authoritative
// ledger at the new path so deletion detection cannot destroy these nodes.
func (r *IncrementalMutationRepo) MoveSourceDocuments(ctx context.Context, repoURL string, moves []SourceDocumentMove) error {
	if len(moves) == 0 {
		return nil
	}
	r.db.mu.Lock()
	defer r.db.mu.Unlock()
	tx, err := r.db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	used := make(map[string]bool)
	for _, move := range moves {
		if move.OldPath == "" || move.NewPath == "" || move.OldPath == move.NewPath || len(move.Sources) == 0 || move.ContentHash == "" || used[move.OldPath] || used[move.NewPath] {
			return fmt.Errorf("source move: invalid or overlapping paths %q -> %q", move.OldPath, move.NewPath)
		}
		used[move.OldPath], used[move.NewPath] = true, true
		var occupied int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM node_sources WHERE file_path=?) +
			(SELECT count(*) FROM file_states WHERE repo_url=? AND file_path=?)`, move.NewPath, repoURL, move.NewPath).Scan(&occupied); err != nil {
			return err
		}
		if occupied != 0 {
			return fmt.Errorf("source move: destination %q already has ledger records", move.NewPath)
		}
		rows, err := tx.QueryContext(ctx, `SELECT node_uuid, member_id, skill, file_path, start_line, end_line
			FROM node_sources WHERE file_path=? ORDER BY node_uuid, member_id`, move.OldPath)
		if err != nil {
			return err
		}
		actual, scanErr := scanNodeSources(rows)
		closeErr := rows.Close()
		if scanErr != nil {
			return scanErr
		}
		if closeErr != nil {
			return closeErr
		}
		want := make(map[NodeSource]bool, len(move.Sources))
		for _, source := range move.Sources {
			if source.FilePath != move.OldPath {
				return fmt.Errorf("source move: snapshot contains foreign path %q", source.FilePath)
			}
			want[source] = true
		}
		if len(actual) != len(move.Sources) || len(want) != len(move.Sources) {
			return fmt.Errorf("source move: source ledger changed for %q", move.OldPath)
		}
		for _, source := range actual {
			if !want[source] {
				return fmt.Errorf("source move: source ledger changed for %q", move.OldPath)
			}
		}
		var previous string
		err = tx.QueryRowContext(ctx, `SELECT content_hash FROM file_states WHERE repo_url=? AND file_path=?`, repoURL, move.OldPath).Scan(&previous)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if previous != move.PreviousHash || (err == nil && move.PreviousHash == "") {
			return fmt.Errorf("source move: file_state changed for %q", move.OldPath)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE node_sources SET file_path=? WHERE file_path=?`, move.NewPath, move.OldPath); err != nil {
			return fmt.Errorf("source move: migrate sources: %w", err)
		}
		if move.PreviousHash != "" {
			_, err = tx.ExecContext(ctx, `UPDATE file_states SET file_path=? WHERE repo_url=? AND file_path=?`, move.NewPath, repoURL, move.OldPath)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO file_states(repo_url,file_path,content_hash) VALUES(?,?,?)`, repoURL, move.NewPath, move.ContentHash)
		}
		if err != nil {
			return fmt.Errorf("source move: migrate file_state: %w", err)
		}
	}
	version := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO kb_version(version,updated_at) VALUES(?,?)`, version, version); err != nil {
		return err
	}
	return tx.Commit()
}
