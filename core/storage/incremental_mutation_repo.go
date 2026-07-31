package storage

// 本文件提供增量旁路所需的原子复合写操作。旁路 C1/C2 不能由多个独立 Repo
// 调用拼接：任一步失败都必须回滚此前的来源行、边、节点或 provenance 对账修改。

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// IncrementalMutationRepo 封装增量旁路的事务化复合操作。
type IncrementalMutationRepo struct {
	db *DB
}

func NewIncrementalMutationRepo(db *DB) *IncrementalMutationRepo {
	return &IncrementalMutationRepo{db: db}
}

// ContentEditResult 是一次 C1 原子更新的提交结果。
type ContentEditResult struct {
	Node       *dktypes.Node
	SourceDocs []string
}

// ApplyContentEdit 在同一事务内读取 node/来源并更新可编辑字段。
// node 不存在时返回空结果，不产生写入。
func (r *IncrementalMutationRepo) ApplyContentEdit(ctx context.Context, id string,
	summaryChanged bool, summary string, descriptionChanged bool, description string,
	updatedAt time.Time) (*ContentEditResult, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	tx, err := r.db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplyContentEdit begin: %w", err)
	}
	defer tx.Rollback()

	n := &dbNode{}
	if err := scanNodeRow(tx.QueryRowContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes WHERE id = ?`, id), n); err != nil {
		if err == sql.ErrNoRows {
			return &ContentEditResult{}, nil
		}
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplyContentEdit load %s: %w", id, err)
	}
	node := n.ToPublic()
	docs, err := distinctSourceDocsTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if summaryChanged {
		node.Summary = summary
	}
	if descriptionChanged {
		node.Description = description
	}
	node.Provenance = dktypes.ProvenanceHumanCurated
	node.UpdatedAt = updatedAt
	result, err := tx.ExecContext(ctx,
		`UPDATE nodes SET summary = ?, description = ?, provenance = ?, updated_at = ? WHERE id = ?`,
		node.Summary, nullString(node.Description), string(node.Provenance), node.UpdatedAt, node.ID)
	if err != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplyContentEdit update %s: %w", id, err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplyContentEdit update %s: affected %d rows", id, rows)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplyContentEdit commit %s: %w", id, err)
	}
	return &ContentEditResult{Node: node, SourceDocs: docs}, nil
}

// SourceDeletionResult 是一次 C2 引用计数删除的原子提交结果。
type SourceDeletionResult struct {
	Node          *dktypes.Node
	SourceDocs    []string
	RemainingDocs int
	Deleted       bool
	Phantom       bool
	// NoContribution means the node exists but the requested document did not
	// contribute any node_sources row.  This is a fail-closed C2 replay result:
	// the node, edges, FTS, and provenance remain untouched.
	NoContribution bool
	RowID          int64
}

// ApplySourceDeletion 在一个事务内完成：来源快照、删除本文件贡献、引用计数，
// 归零时的出入边/节点/残余来源清理，以及 node/domain source_refs 和 stale
// provides 对账。任一步失败整个事务回滚。
func (r *IncrementalMutationRepo) ApplySourceDeletion(ctx context.Context,
	nodeUUID, filePath string) (*SourceDeletionResult, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	tx, err := r.db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion begin: %w", err)
	}
	defer tx.Rollback()

	preDocs, err := distinctSourceDocsTx(ctx, tx, nodeUUID)
	if err != nil {
		return nil, err
	}
	n := &dbNode{}
	loadErr := scanNodeRow(tx.QueryRowContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes WHERE id = ?`, nodeUUID), n)
	if loadErr == sql.ErrNoRows {
		// The node has already disappeared, so this is a retry/repair of a
		// partially committed legacy deletion.  node_sources has no FK and edges
		// are protected at the application layer, therefore limiting cleanup to
		// the requested file would leave other source rows and in/out edges
		// permanently orphaned.  Converge every artifact that can still be
		// identified by UUID in this transaction; preDocs is returned so I-9 can
		// rewrite every formerly contributing document.
		//
		// FTS cannot be cleaned safely here: nodes_fts stores only the deleted
		// nodes.rowid, while no durable UUID -> rowid mapping remains after the
		// node row is gone.  Guessing a rowid could delete an unrelated node's
		// index entry after SQLite rowid reuse.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM node_sources WHERE node_uuid = ?`, nodeUUID); err != nil {
			return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion phantom source: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM edges WHERE source_id = ? OR target_id = ?`, nodeUUID, nodeUUID); err != nil {
			return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion phantom edges: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion phantom commit: %w", err)
		}
		return &SourceDeletionResult{SourceDocs: preDocs, Phantom: true}, nil
	}
	if loadErr != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion load %s: %w", nodeUUID, loadErr)
	}
	node := n.ToPublic()
	now := time.Now().UTC()
	var rowID int64
	if err := tx.QueryRowContext(ctx, `SELECT rowid FROM nodes WHERE id = ?`, nodeUUID).Scan(&rowID); err != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion rowid %s: %w", nodeUUID, err)
	}
	deleteResult, err := tx.ExecContext(ctx,
		`DELETE FROM node_sources WHERE node_uuid = ? AND file_path = ?`, nodeUUID, filePath)
	if err != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion source %s/%s: %w", nodeUUID, filePath, err)
	}
	deletedSources, err := deleteResult.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion source count %s/%s: %w", nodeUUID, filePath, err)
	}
	var remaining int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT file_path) FROM node_sources WHERE node_uuid = ?`, nodeUUID).Scan(&remaining); err != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion count %s: %w", nodeUUID, err)
	}
	if deletedSources == 0 {
		// A stale sidecar can replay a C2 UUID after its source row was already
		// removed (or for a node that never had source rows).  Never infer a true
		// delete from the current reference count in that case: doing so would
		// destroy an otherwise valid node and its graph edges.
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion no-contribution commit %s: %w", nodeUUID, err)
		}
		return &SourceDeletionResult{
			Node: node, SourceDocs: preDocs, RemainingDocs: remaining,
			NoContribution: true, RowID: rowID,
		}, nil
	}
	deleted := remaining == 0
	if deleted {
		// FTS 与 node/边/来源同事务删除。若只留给后续 I-8 清理，进程在旁路提交后
		// 崩溃会丢失 rowid 快照，形成永久 FTS 幽灵。
		if _, err := tx.ExecContext(ctx, `DELETE FROM nodes_fts WHERE rowid = ?`, rowID); err != nil {
			return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion fts %s: %w", nodeUUID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE source_id = ? OR target_id = ?`, nodeUUID, nodeUUID); err != nil {
			return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion edges %s: %w", nodeUUID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, nodeUUID); err != nil {
			return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion node %s: %w", nodeUUID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM node_sources WHERE node_uuid = ?`, nodeUUID); err != nil {
			return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion residual sources %s: %w", nodeUUID, err)
		}
	} else {
		// The source mutation is already durable when this method returns. Keep the
		// surviving node's denormalized provenance in the same transaction so a
		// later I-8 failure cannot publish a stale source_refs value.
		if err := reconcileNodeProvenanceTx(ctx, tx, nodeUUID, now); err != nil {
			return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion node provenance %s: %w", nodeUUID, err)
		}
	}
	// A true delete must also reconcile the owning domain before the transaction
	// commits. This is deliberately independent of the caller's in-memory
	// runState: a process crash after this commit must not leave stale
	// domain.source_refs or skill->domain provides edges for a later phantom C2.
	if node.Domain != "" {
		if err := reconcileDomainProvenanceTx(ctx, tx, node.Domain, now); err != nil {
			return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion domain provenance %s: %w", node.Domain, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo.ApplySourceDeletion commit %s: %w", nodeUUID, err)
	}
	return &SourceDeletionResult{
		Node: node, SourceDocs: preDocs, RemainingDocs: remaining,
		Deleted: deleted, RowID: rowID,
	}, nil
}

// reconcileNodeProvenanceTx refreshes a node's denormalized source_refs from
// node_sources. The caller must hold db.mu and pass a transaction that has
// already removed the source contribution being applied.
func reconcileNodeProvenanceTx(ctx context.Context, tx *sql.Tx, nodeUUID string, updatedAt time.Time) error {
	skills, err := distinctSkillsByNodeTx(ctx, tx, nodeUUID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET source_refs = ?, updated_at = ? WHERE id = ?`,
		nullString(strings.Join(skills, ",")), updatedAt, nodeUUID); err != nil {
		return fmt.Errorf("update node source_refs %s: %w", nodeUUID, err)
	}
	return nil
}

// reconcileDomainProvenanceTx refreshes a domain's source_refs and removes
// provides edges whose skill no longer contributes any surviving node in the
// domain. It intentionally only deletes stale identities; authoritative edge
// creation remains in incremental ingest where skill-node materialization is
// available. All reads and writes happen on the same transaction snapshot.
func reconcileDomainProvenanceTx(ctx context.Context, tx *sql.Tx, domain string, updatedAt time.Time) error {
	skills, err := distinctSkillsByDomainTx(ctx, tx, domain)
	if err != nil {
		return err
	}
	contributing := make(map[string]struct{}, len(skills))
	for _, skill := range skills {
		contributing[skill] = struct{}{}
	}
	domainID := "domain::" + domain
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET source_refs = ?, updated_at = ?
		   WHERE id = ? AND label = 'Domain'`,
		nullString(strings.Join(skills, ",")), updatedAt, domainID); err != nil {
		return fmt.Errorf("update domain source_refs %s: %w", domain, err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT source_id FROM edges WHERE target_id = ? AND kind = 'provides'`, domainID)
	if err != nil {
		return fmt.Errorf("list provides %s: %w", domain, err)
	}
	var sourceIDs []string
	for rows.Next() {
		var sourceID string
		if err := rows.Scan(&sourceID); err != nil {
			rows.Close()
			return fmt.Errorf("scan provides %s: %w", domain, err)
		}
		sourceIDs = append(sourceIDs, sourceID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read provides %s: %w", domain, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close provides %s: %w", domain, err)
	}
	for _, sourceID := range sourceIDs {
		skill := strings.TrimPrefix(sourceID, "skill::")
		if _, ok := contributing[skill]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM edges WHERE source_id = ? AND target_id = ? AND kind = 'provides'`,
			sourceID, domainID); err != nil {
			return fmt.Errorf("delete stale provides %s->%s: %w", sourceID, domain, err)
		}
	}
	return nil
}

func distinctSkillsByNodeTx(ctx context.Context, tx *sql.Tx, nodeUUID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT skill FROM node_sources WHERE node_uuid = ? ORDER BY skill`, nodeUUID)
	if err != nil {
		return nil, fmt.Errorf("list node skills %s: %w", nodeUUID, err)
	}
	defer rows.Close()
	var skills []string
	for rows.Next() {
		var skill string
		if err := rows.Scan(&skill); err != nil {
			return nil, fmt.Errorf("scan node skills %s: %w", nodeUUID, err)
		}
		skills = append(skills, skill)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read node skills %s: %w", nodeUUID, err)
	}
	return skills, nil
}

func distinctSkillsByDomainTx(ctx context.Context, tx *sql.Tx, domain string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT s.skill FROM node_sources s
		   JOIN nodes n ON n.id = s.node_uuid
		  WHERE n.domain = ? ORDER BY s.skill`, domain)
	if err != nil {
		return nil, fmt.Errorf("list domain skills %s: %w", domain, err)
	}
	defer rows.Close()
	var skills []string
	for rows.Next() {
		var skill string
		if err := rows.Scan(&skill); err != nil {
			return nil, fmt.Errorf("scan domain skills %s: %w", domain, err)
		}
		skills = append(skills, skill)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read domain skills %s: %w", domain, err)
	}
	return skills, nil
}

func distinctSourceDocsTx(ctx context.Context, tx *sql.Tx, nodeUUID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT file_path FROM node_sources WHERE node_uuid = ? ORDER BY file_path`, nodeUUID)
	if err != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo source docs %s: %w", nodeUUID, err)
	}
	defer rows.Close()
	var docs []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("IncrementalMutationRepo source docs scan: %w", err)
		}
		if p != "" {
			docs = append(docs, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("IncrementalMutationRepo source docs rows: %w", err)
	}
	sort.Strings(docs)
	return docs, nil
}
