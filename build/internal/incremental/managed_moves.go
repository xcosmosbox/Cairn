package incremental

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/storage"
)

// DocumentMove reports a proven relocation without inventing new UUIDs or
// counting the old path as deleted knowledge. Recovered means the DB move was
// committed in an earlier interrupted run and only its sidecar needed repair.
type DocumentMove struct {
	From, To   string
	SourceRows int
	Recovered  bool
}

type managedMove struct {
	storage.SourceDocumentMove
	recovered bool
}

// planManagedMoves runs before ANY C2 classification or mutation. The sidecar
// must attest the complete old ledger, not merely intersect a few shared UUIDs.
// Renames with concurrent content edits, cross-skill moves and ambiguous copies
// are intentionally rejected; they require an explicit separate operation.
func planManagedMoves(ctx context.Context, st *stores, root string, sr *scanResult,
	sources []storage.NodeSource, states []*storage.FileState) ([]managedMove, error) {
	byDoc := make(map[string][]storage.NodeSource)
	for _, source := range sources {
		byDoc[source.FilePath] = append(byDoc[source.FilePath], source)
	}
	stateByDoc := make(map[string]*storage.FileState)
	for _, state := range states {
		stateByDoc[state.FilePath] = state
	}
	missing := make(map[string]bool)
	for doc := range byDoc {
		if _, err := os.Lstat(filepath.Join(root, doc)); os.IsNotExist(err) {
			missing[doc] = true
		} else if err != nil {
			return nil, fmt.Errorf("incremental: inspect managed path %s: %w", doc, err)
		}
	}
	var moves []managedMove
	claimed := make(map[string]string)
	paths := append([]string(nil), sr.all...)
	sort.Strings(paths)
	for _, path := range paths {
		if IsSharedPrimaryPath(path) {
			continue // primary identity is fixed by KG, never inferred as a source move
		}
		data, err := os.ReadFile(filepath.Join(root, path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("incremental: inspect possible move %s: %w", path, err)
		}
		sc, scErr := writeback.ReadSidecar(filepath.Join(root, path))
		owned := len(byDoc[path]) > 0
		managedMarker := strings.Contains(string(data), kgOpenPrefix) || strings.Contains(string(data), docHeaderLine)
		if scErr != nil || sc == nil {
			if !owned && len(missing) > 0 && managedMarker {
				return nil, fmt.Errorf("incremental: unproven managed move at %s while source documents are missing: valid original sidecar required; refusing C2 deletion", path)
			}
			continue
		}
		if sc.Doc == path {
			if !owned && len(missing) > 0 && (len(sc.Nodes) > 0 || managedMarker) {
				return nil, fmt.Errorf("incremental: unowned managed document %s has no original-path attestation; refusing C2 deletion", path)
			}
			continue
		}
		old := sc.Doc
		if _, err := writeback.ValidateRepositoryPath(root, old); err != nil {
			return nil, fmt.Errorf("incremental: unsafe move origin %s: %w", old, err)
		}
		if IsSharedPrimaryPath(old) {
			return nil, fmt.Errorf("incremental: cannot relocate shared primary %s as source %s", old, path)
		}
		oldRows := byDoc[old]
		recovered := owned && len(oldRows) == 0
		if !recovered && len(oldRows) == 0 {
			if len(missing) > 0 {
				return nil, fmt.Errorf("incremental: unproven move %s -> %s; refusing C2 deletion", old, path)
			}
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, old)); !os.IsNotExist(err) {
			return nil, fmt.Errorf("incremental: move origin %s is still present or unreadable; %s is not a unique relocation", old, path)
		}
		if prior := claimed[old]; prior != "" {
			return nil, fmt.Errorf("incremental: ambiguous managed move %s has destinations %s and %s; refusing C2 deletion", old, prior, path)
		}
		claimed[old] = path
		ledgerPath := old
		if recovered {
			ledgerPath = path
			oldRows = byDoc[path]
		} else if owned || stateByDoc[path] != nil {
			return nil, fmt.Errorf("incremental: move destination %s already has ledger records", path)
		}
		for _, row := range oldRows {
			if skill := sr.skillByDoc[path]; skill != "" {
				if skill != row.Skill {
					return nil, fmt.Errorf("incremental: cross-skill move %s -> %s is not supported", old, path)
				}
			} else if filepath.Dir(old) != filepath.Dir(path) {
				return nil, fmt.Errorf("incremental: cannot attest destination skill for move %s -> %s", old, path)
			}
		}
		if err := validateMoveEvidence(ctx, st, ledgerPath, sc, string(data)); err != nil {
			return nil, fmt.Errorf("incremental: unproven managed move %s -> %s: %w", old, path, err)
		}
		previousHash := ""
		if state := stateByDoc[ledgerPath]; state != nil {
			previousHash = state.ContentHash
			if previousHash != sc.DocHash {
				return nil, fmt.Errorf("incremental: move %s has an unresolved file_state baseline; refusing C2 deletion", path)
			}
		}
		moves = append(moves, managedMove{SourceDocumentMove: storage.SourceDocumentMove{
			OldPath: old, NewPath: path, Sources: oldRows, PreviousHash: previousHash, ContentHash: sc.DocHash,
		}, recovered: recovered})
	}
	return moves, nil
}

func validateMoveEvidence(ctx context.Context, st *stores, ledgerPath string, sc *writeback.SidecarFile, content string) error {
	rows, err := st.sources.ListByFile(ctx, ledgerPath)
	if err != nil {
		return err
	}
	wantIDs, gotIDs := make(map[string]bool), make(map[string]bool)
	for _, row := range rows {
		wantIDs[row.NodeUUID] = true
	}
	for _, node := range sc.Nodes {
		gotIDs[node.UUID] = true
	}
	if !sameUUIDSet(wantIDs, gotIDs) {
		return fmt.Errorf("sidecar UUIDs do not match complete source ledger")
	}
	views, err := authoritativeSidecarViews(ctx, st, ledgerPath, false, "")
	if err != nil {
		return err
	}
	adapted := *sc
	adapted.Nodes = append([]writeback.SidecarNode(nil), sc.Nodes...)
	for i := range adapted.Nodes {
		if g := adapted.Nodes[i].GraphMetadata; g != nil && ledgerPath != sc.Doc {
			copyGraph := *g
			// Optional snapshots may still name several old paths after one batch
			// commit. Move identity is proven by the complete UUID/member/readonly
			// baseline below, not this stale cache; refresh it from SQLite afterward.
			copyGraph.SourcesKnown = false
			copyGraph.Sources = []writeback.SourceReference{}
			adapted.Nodes[i].GraphMetadata = &copyGraph
		}
	}
	adapted.Doc = ledgerPath
	if len(views) == 0 || !writeback.SidecarMetadataMatches(adapted, ledgerPath, views) {
		return fmt.Errorf("sidecar UUID/member/metadata does not match complete source ledger")
	}
	if writeback.HashEditable(content) != sc.DocHash || DiffDoc(sc.Doc, sc, content, false).HasChanges() {
		return fmt.Errorf("document differs from its attested baseline; rename and edit must be separate")
	}
	return nil
}

func applyManagedMoves(ctx context.Context, st *stores, root, repoURL string, moves []managedMove, report *RunReport) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	var pending []storage.SourceDocumentMove
	for _, move := range moves {
		if !move.recovered {
			pending = append(pending, move.SourceDocumentMove)
		}
	}
	if err := st.mutations.MoveSourceDocuments(ctx, repoURL, pending); err != nil {
		return fmt.Errorf("incremental: commit document moves: %w", err)
	}
	for _, move := range moves {
		report.DocumentMoves = append(report.DocumentMoves, DocumentMove{From: move.OldPath, To: move.NewPath, SourceRows: len(move.Sources), Recovered: move.recovered})
		// DB ownership already protects the moved nodes from deletion. Recheck
		// the physical evidence before rebinding the sidecar; a changed file is
		// an explicit error and remains intact. A retry uses the new ledger.
		data, err := os.ReadFile(filepath.Join(root, move.NewPath))
		if err != nil {
			return err
		}
		sc, err := writeback.ReadSidecar(filepath.Join(root, move.NewPath))
		if err != nil {
			return err
		}
		if sc.Doc != move.OldPath || sc.DocHash != move.ContentHash {
			return fmt.Errorf("incremental: move sidecar changed during migration: %s", move.NewPath)
		}
		if err := validateMoveEvidence(ctx, st, move.NewPath, sc, string(data)); err != nil {
			return fmt.Errorf("incremental: move changed during migration %s: %w", move.NewPath, err)
		}
	}
	// Attest the whole committed move batch before refreshing any destination:
	// refreshing a sharing sibling must not erase another move's old Doc proof.
	if err := refreshMovedSourceMetadata(ctx, st, root, moves); err != nil {
		return err
	}
	report.DocsRewritten += len(moves)
	return nil
}

// refreshMovedSourceMetadata 修复共享来源与 primary 的只读来源快照，保留人工改动 baseline。
// Refresh siblings first; keep moved-path attestation retryable until all succeed.
func refreshMovedSourceMetadata(ctx context.Context, st *stores, root string, moves []managedMove) error {
	paths := map[string]bool{}
	_, primaryByPath, err := listExpectedSharedPrimaryDocs(ctx, st)
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	destinations := map[string]bool{}
	for _, move := range moves {
		destinations[move.NewPath] = true
		for _, src := range move.Sources {
			ids[src.NodeUUID] = true
			rows, err := st.sources.ListByNode(ctx, src.NodeUUID)
			if err != nil {
				return err
			}
			for _, row := range rows {
				paths[row.FilePath] = true
			}
		}
	}
	for path, id := range primaryByPath {
		if ids[id] {
			paths[path] = true
		}
	}
	ordered := []string{}
	for path := range paths {
		if !destinations[path] {
			ordered = append(ordered, path)
		}
	}
	sort.Strings(ordered)
	var movedPaths []string
	for path := range destinations {
		movedPaths = append(movedPaths, path)
	}
	sort.Strings(movedPaths)
	ordered = append(ordered, movedPaths...)
	for _, path := range ordered {
		primaryID := primaryByPath[path]
		views, err := authoritativeSidecarViews(ctx, st, path, primaryID != "", primaryID)
		if err != nil {
			return err
		}
		if err := writeback.RefreshGraphMetadataContext(ctx, root, path, views); err != nil {
			return fmt.Errorf("incremental: refresh moved-source metadata %s: %w", path, err)
		}
	}
	return nil
}
