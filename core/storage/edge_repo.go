// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含 EdgeRepo 仓储，提供 edges 表的完整 CRUD 操作。
// Package storage implements the persistence layer for the Domain Knowledge Layer,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the EdgeRepo repository, providing full CRUD operations
// for the edges table.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
)

// EdgeRepo 封装对 edges 表的所有 CRUD 操作。
// 所有方法均通过嵌入的 *DB 访问数据库，并使用互斥锁保证线程安全。
//
// EdgeRepo encapsulates all CRUD operations on the edges table.
// All methods access the database through the embedded *DB and
// use mutex locks to ensure thread safety.
type EdgeRepo struct {
	db *DB
}

// NewEdgeRepo 创建新的 EdgeRepo 实例。
//
// NewEdgeRepo creates a new EdgeRepo instance.
func NewEdgeRepo(db *DB) *EdgeRepo {
	return &EdgeRepo{db: db}
}

// CountByKind 一次性聚合统计各关系类型（kind）的边数量，返回 kind→count 映射。
// 相比逐节点调用 GetOutgoing 累加，本方法用单条 GROUP BY 查询，O(1) 次往返完成全表边统计。
//
// CountByKind aggregates edge counts grouped by kind in a single GROUP BY query,
// avoiding the O(N) per-node accumulation pattern.
func (r *EdgeRepo) CountByKind(ctx context.Context) (map[string]int, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT kind, COUNT(*) FROM edges GROUP BY kind`)
	if err != nil {
		return nil, fmt.Errorf("EdgeRepo.CountByKind: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var kind string
		var cnt int
		if err := rows.Scan(&kind, &cnt); err != nil {
			return nil, fmt.Errorf("EdgeRepo.CountByKind scan: %w", err)
		}
		counts[kind] = cnt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("EdgeRepo.CountByKind rows: %w", err)
	}
	return counts, nil
}

// ListAll 返回 edges 表全部边（按 id 升序，确定性顺序），供演化 diff 等
// 需要全量比对的旁路只读场景使用；单条查询完成，避免逐节点 GetOutgoing 的 O(N) 往返。
//
// ListAll returns every edge in ascending id order (deterministic), for
// read-only full-compare use cases such as evolution diffing.
func (r *EdgeRepo) ListAll(ctx context.Context) ([]*dktypes.Edge, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT id, source_id, target_id, kind, description, properties,
		        provenance, confidence, source_refs, bidirectional,
		        cardinality, created_at
		   FROM edges
		  ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("EdgeRepo.ListAll: %w", err)
	}
	defer rows.Close()

	return scanEdges(rows)
}

// GetOutgoing 查询从指定源节点出发的所有边，按置信度过滤并按关系类型优先级排序。
// 排序优先级：triggers(0) > depends_on(1) > references(2) > 其他(3)。
//
// GetOutgoing retrieves all edges originating from the specified source node,
// filtered by minimum confidence and ordered by relationship-type priority.
// Priority order: triggers(0) > depends_on(1) > references(2) > others(3).
func (r *EdgeRepo) GetOutgoing(ctx context.Context, sourceID string, minConfidence float64) ([]*dktypes.Edge, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT id, source_id, target_id, kind, description, properties,
		        provenance, confidence, source_refs, bidirectional,
		        cardinality, created_at
		   FROM edges
		  WHERE source_id = ? AND confidence >= ?
		  ORDER BY CASE kind
		           WHEN 'triggers'   THEN 0
		           WHEN 'depends_on' THEN 1
		           WHEN 'references' THEN 2
		           ELSE 3 END`, sourceID, minConfidence)
	if err != nil {
		return nil, fmt.Errorf("EdgeRepo.GetOutgoing %s: %w", sourceID, err)
	}
	defer rows.Close()

	return scanEdges(rows)
}

// GetIncoming 查询指向指定目标节点的所有边，按置信度过滤。
//
// GetIncoming retrieves all edges pointing to the specified target node,
// filtered by minimum confidence.
func (r *EdgeRepo) GetIncoming(ctx context.Context, targetID string, minConfidence float64) ([]*dktypes.Edge, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT id, source_id, target_id, kind, description, properties,
		        provenance, confidence, source_refs, bidirectional,
		        cardinality, created_at
		   FROM edges
		  WHERE target_id = ? AND confidence >= ?`, targetID, minConfidence)
	if err != nil {
		return nil, fmt.Errorf("EdgeRepo.GetIncoming %s: %w", targetID, err)
	}
	defer rows.Close()

	return scanEdges(rows)
}

// GetBetween 查询两个指定节点之间的所有边。
//
// GetBetween retrieves all edges between the specified source and target nodes.
func (r *EdgeRepo) GetBetween(ctx context.Context, sourceID, targetID string) ([]*dktypes.Edge, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT id, source_id, target_id, kind, description, properties,
		        provenance, confidence, source_refs, bidirectional,
		        cardinality, created_at
		   FROM edges
		  WHERE source_id = ? AND target_id = ?`, sourceID, targetID)
	if err != nil {
		return nil, fmt.Errorf("EdgeRepo.GetBetween %s->%s: %w", sourceID, targetID, err)
	}
	defer rows.Close()

	return scanEdges(rows)
}

// InsertIfAbsent 以边身份 (source_id, target_id, kind) 做权威幂等 upsert：
// 不存在时插入并返回 true；存在时保留最早 id 的一行、用传入边覆盖全部可变字段、
// 删除其余存量重复行并返回 false。这样重放不增行，同时 relation 重算得到的新
// description/confidence/provenance 等不会被旧值吞掉。
//
// 本方法在 DB 互斥锁内用单事务完成「定位/插入或更新/重复收敛」，并发安全由
// 单写者串行化保证；刻意不增加裸唯一索引，避免旧库因已有重复边而启动失败。
//
// InsertIfAbsent performs an authoritative identity upsert. It inserts and
// returns true when absent; otherwise it updates the oldest row, removes legacy
// duplicates atomically, and returns false.
func (r *EdgeRepo) InsertIfAbsent(ctx context.Context, edge *dktypes.Edge) (bool, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	tx, err := r.db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("EdgeRepo.InsertIfAbsent begin tx: %w", err)
	}
	defer tx.Rollback() // no-op after commit

	var survivorID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM edges
		  WHERE source_id = ? AND target_id = ? AND kind = ?
		  ORDER BY id LIMIT 1`,
		edge.SourceID, edge.TargetID, string(edge.Kind),
	).Scan(&survivorID)
	if err == sql.ErrNoRows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO edges (source_id, target_id, kind, description, properties,
			                    provenance, confidence, source_refs, bidirectional,
			                    cardinality, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			edge.SourceID, edge.TargetID, string(edge.Kind),
			nullString(edge.Description), nullString(edge.Properties),
			string(edge.Provenance), edge.Confidence,
			nullString(edge.SourceRefs), edge.Bidirectional,
			nullString(edge.Cardinality), edge.CreatedAt,
		); err != nil {
			return false, fmt.Errorf("EdgeRepo.InsertIfAbsent insert %s->%s: %w", edge.SourceID, edge.TargetID, err)
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("EdgeRepo.InsertIfAbsent commit insert: %w", err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("EdgeRepo.InsertIfAbsent lookup %s->%s: %w", edge.SourceID, edge.TargetID, err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE edges
		    SET description = ?, properties = ?, provenance = ?, confidence = ?,
		        source_refs = ?, bidirectional = ?, cardinality = ?
		  WHERE id = ?`,
		nullString(edge.Description), nullString(edge.Properties),
		string(edge.Provenance), edge.Confidence, nullString(edge.SourceRefs),
		edge.Bidirectional, nullString(edge.Cardinality), survivorID,
	); err != nil {
		return false, fmt.Errorf("EdgeRepo.InsertIfAbsent update %s->%s: %w", edge.SourceID, edge.TargetID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM edges
		  WHERE source_id = ? AND target_id = ? AND kind = ? AND id <> ?`,
		edge.SourceID, edge.TargetID, string(edge.Kind), survivorID,
	); err != nil {
		return false, fmt.Errorf("EdgeRepo.InsertIfAbsent collapse duplicates %s->%s: %w", edge.SourceID, edge.TargetID, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("EdgeRepo.InsertIfAbsent commit update: %w", err)
	}
	return false, nil
}

// DeleteBetween 删除指定边身份 (source_id, target_id, kind) 的边，返回删除行数。
// 增量流水线精确同步「skill→domain provides」时使用（问题 9：
// 该 skill 不再贡献该 domain 时删除对应 provides 边，不误删其它）。
//
// DeleteBetween deletes edges with the exact identity (source, target, kind).
func (r *EdgeRepo) DeleteBetween(ctx context.Context, sourceID, targetID string, kind dktypes.RelationKind) (int64, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM edges WHERE source_id = ? AND target_id = ? AND kind = ?`,
		sourceID, targetID, string(kind))
	if err != nil {
		return 0, fmt.Errorf("EdgeRepo.DeleteBetween %s->%s (%s): %w", sourceID, targetID, kind, err)
	}
	return result.RowsAffected()
}

// Insert 向 edges 表插入一条新边记录。
//
// Insert inserts a new edge record into the edges table.
func (r *EdgeRepo) Insert(ctx context.Context, edge *dktypes.Edge) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	_, err := r.db.Conn().ExecContext(ctx,
		`INSERT INTO edges (source_id, target_id, kind, description, properties,
		                    provenance, confidence, source_refs, bidirectional,
		                    cardinality, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		edge.SourceID, edge.TargetID, string(edge.Kind),
		nullString(edge.Description), nullString(edge.Properties),
		string(edge.Provenance), edge.Confidence,
		nullString(edge.SourceRefs), edge.Bidirectional,
		nullString(edge.Cardinality), edge.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("EdgeRepo.Insert %s->%s: %w", edge.SourceID, edge.TargetID, err)
	}
	return nil
}

// InsertBatch 在一个事务内批量插入边。
// 若任一条插入失败，整个事务将回滚。
//
// InsertBatch inserts multiple edges within a single transaction.
// If any insert fails, the entire transaction is rolled back.
func (r *EdgeRepo) InsertBatch(ctx context.Context, edges []*dktypes.Edge) error {
	if len(edges) == 0 {
		return nil
	}

	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	tx, err := r.db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("EdgeRepo.InsertBatch begin tx: %w", err)
	}
	defer tx.Rollback() // 无操作（若已提交）/ no-op if already committed

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO edges (source_id, target_id, kind, description, properties,
		                    provenance, confidence, source_refs, bidirectional,
		                    cardinality, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("EdgeRepo.InsertBatch prepare: %w", err)
	}
	defer stmt.Close()

	for _, edge := range edges {
		_, err := stmt.ExecContext(ctx,
			edge.SourceID, edge.TargetID, string(edge.Kind),
			nullString(edge.Description), nullString(edge.Properties),
			string(edge.Provenance), edge.Confidence,
			nullString(edge.SourceRefs), edge.Bidirectional,
			nullString(edge.Cardinality), edge.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("EdgeRepo.InsertBatch %s->%s: %w", edge.SourceID, edge.TargetID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("EdgeRepo.InsertBatch commit: %w", err)
	}
	return nil
}

// DeleteBySubdomain 删除源节点属于指定域和子域的所有边，返回被删除的行数。
// 通过 nodes 表的 domain/subdomain 归属列子查询定位源节点（entity/concept 已改用 UUID
// 主键，不再适用 source_id LIKE 模式匹配）。
//
// DeleteBySubdomain deletes all edges whose source node belongs to the
// specified domain and subdomain, returning the number of rows affected.
// Resolves source nodes via a subquery on the nodes table's domain/subdomain
// ownership columns (entity/concept now use UUID primary keys; LIKE matching
// on source_id no longer applies).
func (r *EdgeRepo) DeleteBySubdomain(ctx context.Context, domain, subdomain string) (int64, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM edges WHERE source_id IN (
		     SELECT id FROM nodes WHERE domain = ? AND subdomain = ?
		 )`, domain, subdomain)
	if err != nil {
		return 0, fmt.Errorf("EdgeRepo.DeleteBySubdomain: %w", err)
	}
	return result.RowsAffected()
}

// DeleteSemanticBySubdomain 只删除「源节点是指定子域内 entity/concept」的语义边，
// 返回被删除的行数。与 DeleteBySubdomain 的区别：子域层节点（label=Subdomain）
// 的 domain/subdomain 归属列与子域内节点相同，DeleteBySubdomain 会把层节点发出的
// composes 层级边（subdomain→entity/concept）一并删掉；增量重算 relation 时
// 只需要「语义边先删后插」，层级边必须保留——故按源节点 label 过滤。
//
// DeleteSemanticBySubdomain deletes only semantic edges whose source node is an
// entity/concept of the given subdomain. Unlike DeleteBySubdomain it preserves
// the subdomain layer node's composes hierarchy edges (the layer node shares the
// same domain/subdomain ownership columns). Used by the incremental pipeline's
// delete-then-reinsert of semantic relations.
func (r *EdgeRepo) DeleteSemanticBySubdomain(ctx context.Context, domain, subdomain string) (int64, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM edges WHERE source_id IN (
		     SELECT id FROM nodes
		      WHERE domain = ? AND subdomain = ?
		        AND label IN ('Entity', 'Concept')
		 )`, domain, subdomain)
	if err != nil {
		return 0, fmt.Errorf("EdgeRepo.DeleteSemanticBySubdomain: %w", err)
	}
	return result.RowsAffected()
}

// ReplaceSemanticBySubdomain atomically replaces the semantic edge set emitted
// for one subdomain.  The operation deliberately lives in storage rather than
// being composed from DeleteSemanticBySubdomain plus InsertIfAbsent calls:
// SQLite must be able to roll back the delete and every insert when any one
// edge fails (for example, an injected trigger or a constraint violation).
//
// Only edges whose source is an Entity/Concept in the requested subdomain are
// touched.  Layer-node hierarchy edges (Domain/Subdomain/Skill sources) are
// therefore left alone.  Endpoints are checked in the same transaction; a
// replacement with a missing endpoint is skipped, matching the incremental
// ingest fail-safe of never creating dangling edges.  Existing rows with the
// same authoritative identity (source, target, kind) are updated in place so
// only the LLM-schema fields description/confidence change; id, created_at and
// every non-LLM metadata field remain stable. Duplicate legacy rows collapse
// to the oldest row.
func (r *EdgeRepo) ReplaceSemanticBySubdomain(ctx context.Context, domain, subdomain string, replacements []*dktypes.Edge) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	tx, err := r.db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain begin: %w", err)
	}
	defer tx.Rollback() // no-op after commit

	// Snapshot the complete old rows before mutating anything.  Keeping the
	// complete Edge values is important for identity-preserving updates and for
	// deterministic duplicate collapse in legacy databases.
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id, e.source_id, e.target_id, e.kind, e.description, e.properties,
		       e.provenance, e.confidence, e.source_refs, e.bidirectional,
		       e.cardinality, e.created_at
		  FROM edges e
		  JOIN nodes n ON n.id = e.source_id
		 WHERE n.domain = ? AND n.subdomain = ?
		   AND n.label IN ('Entity', 'Concept')
		 ORDER BY e.id`, domain, subdomain)
	if err != nil {
		return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain snapshot: %w", err)
	}
	oldByKey := make(map[string]*dktypes.Edge)
	oldIDsByKey := make(map[string][]int64)
	for rows.Next() {
		row := &dbEdge{}
		if err := rows.Scan(
			&row.ID, &row.SourceID, &row.TargetID, &row.Kind,
			&row.Description, &row.Properties, &row.Provenance,
			&row.Confidence, &row.SourceRefs, &row.Bidirectional,
			&row.Cardinality, &row.CreatedAt,
		); err != nil {
			rows.Close()
			return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain snapshot scan: %w", err)
		}
		edge := row.ToPublic()
		key := edgeIdentityKey(edge.SourceID, edge.TargetID, edge.Kind)
		if _, exists := oldByKey[key]; !exists {
			oldByKey[key] = edge
		}
		oldIDsByKey[key] = append(oldIDsByKey[key], edge.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain snapshot rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain snapshot close: %w", err)
	}

	// Normalize/deduplicate the proposed set before deleting old rows.  Endpoint
	// checks happen inside this transaction so a concurrent mutation cannot make
	// validation and replacement observe different databases.
	prepared := make([]*dktypes.Edge, 0, len(replacements))
	seen := make(map[string]bool, len(replacements))
	for _, candidate := range replacements {
		if candidate == nil {
			continue
		}
		e := *candidate
		if e.SourceID == "" || e.TargetID == "" || e.SourceID == e.TargetID {
			continue
		}
		key := edgeIdentityKey(e.SourceID, e.TargetID, e.Kind)
		if seen[key] {
			continue
		}

		var sourceExists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id = ?`, e.SourceID).Scan(&sourceExists); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain source %s: %w", e.SourceID, err)
		}
		var targetExists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id = ?`, e.TargetID).Scan(&targetExists); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain target %s: %w", e.TargetID, err)
		}
		if e.Confidence <= 0 || e.Confidence > 1 {
			e.Confidence = 0.8
		}
		if !e.Provenance.IsValid() {
			e.Provenance = dktypes.ProvenanceLLMInferred
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = time.Now().UTC()
		}
		seen[key] = true
		prepared = append(prepared, &e)
	}

	// Delete rows that are no longer in the authoritative replacement set and
	// collapse any pre-existing duplicate identities, retaining the oldest id.
	for key, ids := range oldIDsByKey {
		if len(ids) == 0 {
			continue
		}
		if !seen[key] {
			for _, id := range ids {
				if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE id = ?`, id); err != nil {
					return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain delete %d: %w", id, err)
				}
			}
			continue
		}
		for _, id := range ids[1:] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE id = ?`, id); err != nil {
				return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain collapse %d: %w", id, err)
			}
		}
	}

	// Update retained identities in place.  The LLM relation schema only carries
	// description and confidence, so those are the only authoritative fields it
	// may change on an existing identity.  Properties, provenance, source_refs,
	// bidirectional, cardinality, id, and created_at remain from the complete old
	// snapshot.  Genuinely new identities get the candidate's default fields.
	// Any insert/update error bubbles out, causing the deferred rollback to restore
	// the old set.
	for _, edge := range prepared {
		key := edgeIdentityKey(edge.SourceID, edge.TargetID, edge.Kind)
		if old := oldByKey[key]; old != nil {
			// A kept edge is commonly passed through as an exact copy.  Avoiding
			// a needless UPDATE also preserves the distinction between legacy SQL
			// NULLs and empty strings in all untouched payload columns.
			if old.Description == edge.Description && old.Confidence == edge.Confidence {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE edges
				   SET description = ?, confidence = ?
				 WHERE id = ?`,
				nullString(edge.Description), edge.Confidence, old.ID); err != nil {
				return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain update %s->%s: %w", edge.SourceID, edge.TargetID, err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO edges (source_id, target_id, kind, description, properties,
			                    provenance, confidence, source_refs, bidirectional,
			                    cardinality, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			edge.SourceID, edge.TargetID, string(edge.Kind),
			nullString(edge.Description), nullString(edge.Properties),
			string(edge.Provenance), edge.Confidence, nullString(edge.SourceRefs),
			edge.Bidirectional, nullString(edge.Cardinality), edge.CreatedAt); err != nil {
			return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain insert %s->%s: %w", edge.SourceID, edge.TargetID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("EdgeRepo.ReplaceSemanticBySubdomain commit: %w", err)
	}
	return nil
}

// edgeIdentityKey is the authoritative edge identity used by incremental
// replacement and InsertIfAbsent.  The NUL separator prevents ambiguous
// concatenations when ids contain ordinary punctuation.
func edgeIdentityKey(sourceID, targetID string, kind dktypes.RelationKind) string {
	return sourceID + "\x00" + targetID + "\x00" + string(kind)
}

// DeleteBySource 删除从指定源节点出发的所有边，返回被删除的行数。
//
// DeleteBySource deletes all edges originating from the specified source node,
// returning the number of rows affected.
func (r *EdgeRepo) DeleteBySource(ctx context.Context, sourceID string) (int64, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM edges WHERE source_id = ?`, sourceID)
	if err != nil {
		return 0, fmt.Errorf("EdgeRepo.DeleteBySource %s: %w", sourceID, err)
	}
	return result.RowsAffected()
}

// DeleteByTarget 删除指向指定目标节点的所有入边，返回被删除的行数。
// 与 DeleteBySource 配对使用：node 真删时同步清出边与入边，绝不留下悬空边（R5）。
//
// DeleteByTarget deletes all edges pointing at the specified target node,
// returning the number of rows affected. Pairs with DeleteBySource so that a
// node deletion never leaves dangling edges (R5).
func (r *EdgeRepo) DeleteByTarget(ctx context.Context, targetID string) (int64, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM edges WHERE target_id = ?`, targetID)
	if err != nil {
		return 0, fmt.Errorf("EdgeRepo.DeleteByTarget %s: %w", targetID, err)
	}
	return result.RowsAffected()
}

// ——————————————————————————————————————————————————————————————————————————————
// 内部辅助函数 / Internal helper functions
// ——————————————————————————————————————————————————————————————————————————————

// scanEdges 从 sql.Rows 中扫描全部行为 []*dktypes.Edge。
// scanEdges scans all rows from sql.Rows into a []*dktypes.Edge slice.
func scanEdges(rows *sql.Rows) ([]*dktypes.Edge, error) {
	var edges []*dktypes.Edge
	for rows.Next() {
		e := &dbEdge{}
		if err := rows.Scan(
			&e.ID, &e.SourceID, &e.TargetID, &e.Kind,
			&e.Description, &e.Properties, &e.Provenance,
			&e.Confidence, &e.SourceRefs, &e.Bidirectional,
			&e.Cardinality, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanEdges: %w", err)
		}
		edges = append(edges, e.ToPublic())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanEdges rows: %w", err)
	}
	return edges, nil
}
