// Package storage 提供领域知识层的持久化存储层实现。
//
// 本文件包含 UUIDLineageRepo 仓储，提供 uuid_lineage 表的写入与解析操作。
// uuid_lineage 记录 node 身份血缘：仅在重整（rebalance）发生 merge / split 身份
// 重组时写入一行（new_uuid ← old_uuid，reason ∈ {merged, split}）；普通 member
// 增删、remigrate、rename 绝不写（决策总账 R2 / V3-a / R-lineage）。
//
// 主要用途：重整后把文档/审计中引用的旧 uuid 顺血缘链解析到最新存活 uuid
// （ResolveLatest），以及测试与审计导出（ListAll）。
//
// Package storage implements the persistence layer for the Domain Knowledge Layer.
// This file contains the UUIDLineageRepo repository: write and resolve operations
// over the uuid_lineage table (node identity lineage, written only on merge/split).
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// uuidLineageReasonMerged / uuidLineageReasonSplit 是 reason 列的全部合法值。
// 与 schema.go 的 CHECK(reason IN ('merged','split')) 一致；repo 层先行校验，
// 不依赖 DB CHECK 兜底（错误更可读、更早暴露）。
const (
	// uuidLineageReasonMerged 表示 new_uuid 由若干旧 node 合并而来（survivor ← dead）。
	uuidLineageReasonMerged = "merged"
	// uuidLineageReasonSplit 表示 new_uuid 由旧 node 拆分而来（part ← orig）。
	uuidLineageReasonSplit = "split"
)

// UUIDLineage 对应 uuid_lineage 表的一行：一次身份重组的血缘记录。
//
// UUIDLineage maps one row of uuid_lineage: new_uuid evolved from old_uuid.
type UUIDLineage struct {
	NewUUID   string    // 重组后存活/新生的 uuid / surviving or newly derived uuid
	OldUUID   string    // 被重组掉的旧 uuid / restructured-away old uuid
	Reason    string    // merged | split（其它值被 InsertBatch 拒绝）
	CreatedAt time.Time // 记录时间；零值由 DB 默认 datetime('now') 填充
}

// UUIDLineageRepo 封装对 uuid_lineage 表的所有操作。
// 所有方法均通过嵌入的 *DB 访问数据库，并使用互斥锁保证线程安全。
//
// UUIDLineageRepo encapsulates all operations on the uuid_lineage table.
type UUIDLineageRepo struct {
	db *DB
}

// NewUUIDLineageRepo 创建新的 UUIDLineageRepo 实例。
//
// NewUUIDLineageRepo creates a new UUIDLineageRepo instance.
func NewUUIDLineageRepo(db *DB) *UUIDLineageRepo {
	return &UUIDLineageRepo{db: db}
}

// InsertBatch 在一个事务内批量写入血缘记录。
// 主键 (new_uuid, old_uuid) 冲突时 INSERT OR REPLACE 幂等覆盖（重整可重入）；
// reason 只允许 "merged"/"split"，非法值立即返回错误（不依赖 DB CHECK 兜底）；
// new_uuid == old_uuid 的自环记录同样拒绝（血缘无意义且会污染解析链）。
//
// InsertBatch writes lineage rows in one transaction. Idempotent via
// INSERT OR REPLACE; rejects reasons other than merged/split and self-loops.
func (r *UUIDLineageRepo) InsertBatch(ctx context.Context, rows []UUIDLineage) error {
	if len(rows) == 0 {
		return nil
	}
	// 先校验全批次，再开启任何写入（一批非法整体拒绝，不半提交）。
	for _, row := range rows {
		if row.NewUUID == "" || row.OldUUID == "" {
			return fmt.Errorf("UUIDLineageRepo.InsertBatch: new/old uuid 均不可为空")
		}
		if row.NewUUID == row.OldUUID {
			return fmt.Errorf("UUIDLineageRepo.InsertBatch: 拒绝自环血缘 %s", row.NewUUID)
		}
		if row.Reason != uuidLineageReasonMerged && row.Reason != uuidLineageReasonSplit {
			return fmt.Errorf("UUIDLineageRepo.InsertBatch: 非法 reason %q（只允许 merged/split）", row.Reason)
		}
	}

	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	tx, err := r.db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("UUIDLineageRepo.InsertBatch begin tx: %w", err)
	}
	defer tx.Rollback() // 无操作（若已提交）/ no-op if already committed

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR REPLACE INTO uuid_lineage (new_uuid, old_uuid, reason) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("UUIDLineageRepo.InsertBatch prepare: %w", err)
	}
	defer stmt.Close()

	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx, row.NewUUID, row.OldUUID, row.Reason); err != nil {
			return fmt.Errorf("UUIDLineageRepo.InsertBatch (%s←%s): %w", row.NewUUID, row.OldUUID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("UUIDLineageRepo.InsertBatch commit: %w", err)
	}
	return nil
}

// ResolveLatest 顺血缘链把旧 uuid 解析到最新存活 uuid（回写锚更新 / 审计用）：
// 每跳取 old_uuid=当前值 的最新记录（created_at 降序、new_uuid 升序兜底确定性），
// 直到无记录为止返回当前值。无血缘记录时返回原值（调用方视为「未被重组」）。
//
// 注意：split 从一个旧 uuid 派生多个新 uuid，单一 ResolveLatest 无法完整表达
// 一对多后继——本方法按确定性规则取「最新一跳」继续前行，主要为 merge 链服务；
// 需要完整 split 后继的场景应使用 ListAll 自行展开。
// 环防御：血缘链理论上无环（uuid 只前进），仍用 visited 集合防脏数据成环死循环。
//
// ResolveLatest follows the lineage chain from an old uuid to its latest living
// successor (merge chains are the primary use case); returns the input unchanged
// when no lineage row exists. Cycle-guarded for dirty-data safety.
func (r *UUIDLineageRepo) ResolveLatest(ctx context.Context, oldUUID string) (string, error) {
	if oldUUID == "" {
		return "", nil
	}
	current := oldUUID
	visited := map[string]bool{oldUUID: true}
	for {
		r.db.mu.RLock()
		var next string
		err := r.db.Conn().QueryRowContext(ctx,
			`SELECT new_uuid FROM uuid_lineage
			  WHERE old_uuid = ?
			  ORDER BY created_at DESC, new_uuid ASC
			  LIMIT 1`, current).Scan(&next)
		r.db.mu.RUnlock()
		if err != nil {
			// sql.ErrNoRows：链到尽头，current 即最新存活 uuid。
			if errors.Is(err, sql.ErrNoRows) {
				return current, nil
			}
			return "", fmt.Errorf("UUIDLineageRepo.ResolveLatest %s: %w", current, err)
		}
		if visited[next] {
			// 脏数据成环：停在当前值（绝不死循环，也绝不返回已访问节点）。
			return current, nil
		}
		visited[next] = true
		current = next
	}
}

// ResolveSuccessors 展开 oldUUID 的全部「终端后继」（split 一对多的完整表达，
// 补齐 ResolveLatest 只能给单一主后继的缺口）：BFS 沿 old_uuid→new_uuid 前进，
// 收集所有再无出血缘边的终端 uuid。无血缘记录时返回 [oldUUID]（自身即终端）。
// 环防御：visited 集合保证脏数据成环不死循环；成环节点若无其它出边则不产生终端。
// 确定性：终端集按 uuid 升序返回（同输入同输出）。
//
// ResolveSuccessors expands ALL terminal successors of oldUUID via BFS (the full
// one-to-many split picture that ResolveLatest cannot express). Returns
// [oldUUID] when no lineage row exists; cycle-guarded; sorted for determinism.
func (r *UUIDLineageRepo) ResolveSuccessors(ctx context.Context, oldUUID string) ([]string, error) {
	if oldUUID == "" {
		return nil, nil
	}
	visited := map[string]bool{oldUUID: true}
	queue := []string{oldUUID}
	terminals := map[string]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		nexts, err := r.directSuccessors(ctx, cur)
		if err != nil {
			return nil, err
		}
		if len(nexts) == 0 {
			terminals[cur] = true // cur 无出血缘边 → 终端存活 uuid
			continue
		}
		for _, n := range nexts {
			if !visited[n] {
				visited[n] = true
				queue = append(queue, n)
			}
		}
	}
	out := make([]string, 0, len(terminals))
	for u := range terminals {
		out = append(out, u)
	}
	sort.Strings(out) // 确定性 / deterministic order
	return out, nil
}

// directSuccessors 返回 cur 的全部直接后继（new_uuid 升序，确定性）。
// 锁内完成查询与迭代（与 ListAll 同一锁纪律）。
//
// directSuccessors returns the direct successors of cur in ascending order;
// query and iteration run under the read lock (same discipline as ListAll).
func (r *UUIDLineageRepo) directSuccessors(ctx context.Context, cur string) ([]string, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT new_uuid FROM uuid_lineage WHERE old_uuid = ? ORDER BY new_uuid ASC`, cur)
	if err != nil {
		return nil, fmt.Errorf("UUIDLineageRepo.ResolveSuccessors %s: %w", cur, err)
	}
	defer rows.Close()

	var nexts []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("UUIDLineageRepo.ResolveSuccessors scan: %w", err)
		}
		nexts = append(nexts, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("UUIDLineageRepo.ResolveSuccessors rows: %w", err)
	}
	return nexts, nil
}

// ListAll 返回 uuid_lineage 表全部行（按 created_at、new_uuid 稳定序），测试与审计用。
//
// ListAll returns every lineage row in stable order (tests / audit).
func (r *UUIDLineageRepo) ListAll(ctx context.Context) ([]UUIDLineage, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT new_uuid, old_uuid, reason, created_at
		   FROM uuid_lineage
		  ORDER BY created_at, new_uuid, old_uuid`)
	if err != nil {
		return nil, fmt.Errorf("UUIDLineageRepo.ListAll: %w", err)
	}
	defer rows.Close()

	var out []UUIDLineage
	for rows.Next() {
		var l UUIDLineage
		var createdAt string
		if err := rows.Scan(&l.NewUUID, &l.OldUUID, &l.Reason, &createdAt); err != nil {
			return nil, fmt.Errorf("UUIDLineageRepo.ListAll scan: %w", err)
		}
		// created_at 由 DB 默认填充（TEXT）；解析失败仅留零值，不阻塞导出。
		if t, perr := time.Parse("2006-01-02 15:04:05", createdAt); perr == nil {
			l.CreatedAt = t
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("UUIDLineageRepo.ListAll rows: %w", err)
	}
	return out, nil
}
