// Package store 实现 cairnd 控制器的持久化层（产品化 Prompt §6）。
//
// controller.db 使用 SQLite WAL + 迁移版本 + 单连接写策略。控制器重启后从数据库恢复，
// 不依赖进程内 map。所有状态迁移在事务中写入。
//
// 表：managed_repositories / runs / pull_requests / candidates /
//     external_effects（outbox 幂等去重）/ repo_leases / webhook_deliveries。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/xcosmosbox/cairn/build/internal/controller/model"
)

// schemaVersion 是 controller.db 的迁移版本。
// v2：managed_repositories 增加 last_stable_fingerprint 列（fingerprint 变化触发全量重建）。
const schemaVersion = 2

// schemaSQL 是建表 DDL（IF NOT EXISTS，幂等）。
const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_version (
  version INTEGER PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS managed_repositories (
  id                      TEXT PRIMARY KEY,
  github_owner            TEXT NOT NULL,
  github_name             TEXT NOT NULL,
  github_repository_id    INTEGER NOT NULL DEFAULT 0,
  source_url              TEXT NOT NULL,
  branch                  TEXT NOT NULL,
  kg_group                TEXT NOT NULL,
  enabled                 INTEGER NOT NULL DEFAULT 1,
  config_digest           TEXT NOT NULL DEFAULT '',
  last_seen_source_sha    TEXT NOT NULL DEFAULT '',
  last_stable_source_sha  TEXT NOT NULL DEFAULT '',
  last_stable_bundle_digest TEXT NOT NULL DEFAULT '',
  last_stable_fingerprint TEXT NOT NULL DEFAULT '',
  created_at              TEXT NOT NULL,
  updated_at              TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
  run_id                TEXT PRIMARY KEY,
  repo_id               TEXT NOT NULL,
  desired_source_sha    TEXT NOT NULL DEFAULT '',
  base_bundle_digest    TEXT NOT NULL DEFAULT '',
  builder_fingerprint   TEXT NOT NULL DEFAULT '',
  state                 TEXT NOT NULL,
  attempt               INTEGER NOT NULL DEFAULT 0,
  reason                TEXT NOT NULL DEFAULT '',
  report_path           TEXT NOT NULL DEFAULT '',
  error_code            TEXT NOT NULL DEFAULT '',
  error_message         TEXT NOT NULL DEFAULT '',
  started_at            TEXT NOT NULL,
  finished_at           TEXT NOT NULL DEFAULT '',
  next_retry_at         TEXT NOT NULL DEFAULT '',
  FOREIGN KEY (repo_id) REFERENCES managed_repositories(id)
);
CREATE INDEX IF NOT EXISTS idx_runs_repo ON runs(repo_id);
CREATE INDEX IF NOT EXISTS idx_runs_state ON runs(state);

CREATE TABLE IF NOT EXISTS pull_requests (
  run_id            TEXT NOT NULL,
  kind              TEXT NOT NULL,
  owner             TEXT NOT NULL,
  repo              TEXT NOT NULL,
  number            INTEGER NOT NULL,
  branch            TEXT NOT NULL DEFAULT '',
  head_sha          TEXT NOT NULL DEFAULT '',
  base_branch       TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL DEFAULT 'open',
  last_observed_at  TEXT NOT NULL,
  PRIMARY KEY (run_id, kind)
);

CREATE TABLE IF NOT EXISTS candidates (
  candidate_id              TEXT PRIMARY KEY,
  run_id                    TEXT NOT NULL,
  source_sha                TEXT NOT NULL,
  expected_worktree_digest  TEXT NOT NULL DEFAULT '',
  bundle_path               TEXT NOT NULL DEFAULT '',
  bundle_digest             TEXT NOT NULL DEFAULT '',
  release_id                INTEGER NOT NULL DEFAULT 0,
  release_tag               TEXT NOT NULL DEFAULT '',
  status                    TEXT NOT NULL DEFAULT 'building'
);
CREATE INDEX IF NOT EXISTS idx_candidates_run ON candidates(run_id);

CREATE TABLE IF NOT EXISTS external_effects (
  effect_key          TEXT PRIMARY KEY,
  effect_type         TEXT NOT NULL,
  target_repo         TEXT NOT NULL,
  request_fingerprint TEXT NOT NULL,
  external_id         TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL DEFAULT 'pending',
  response_summary    TEXT NOT NULL DEFAULT '',
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS repo_leases (
  repo_id      TEXT PRIMARY KEY,
  holder_id    TEXT NOT NULL,
  acquired_at  TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  heartbeat_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  delivery_id  TEXT PRIMARY KEY,
  event_type   TEXT NOT NULL,
  repo_id      TEXT NOT NULL DEFAULT '',
  received_at  TEXT NOT NULL,
  processed_at TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'received'
);
`

// Store 包装 controller.db 连接。
type Store struct {
	db *sql.DB
}

// Open 打开（或创建）controller.db 并运行迁移。
func Open(path string) (*Store, error) {
	// 确保父目录存在（SQLite CANTOPEN=14 在目录不存在时报误导性 OOM）。
	if dir := filepath.Dir(path); dir != "" {
		os.MkdirAll(dir, 0o755)
	}
	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("controller store: open %s: %w", path, err)
	}
	conn.SetMaxOpenConns(1) // SQLite 单写。
	s := &Store{db: conn}
	if err := s.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭连接。
func (s *Store) Close() error { return s.db.Close() }

// columnExists 检查表中是否已存在某列（用于幂等的 ADD COLUMN 迁移）。
func (s *Store) columnExists(table, col string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// DB 暴露底层连接（供高级用法；调用方自负并发纪律）。
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("controller store: 建表: %w", err)
	}
	// 版本化列迁移：对已存在的旧库补列（CREATE TABLE IF NOT EXISTS 不会加列）。
	// 按列存在性判断，兼容「新建库（schemaSQL 已含该列）」与「旧库升级」两种情况，幂等。
	if ok, err := s.columnExists("managed_repositories", "last_stable_fingerprint"); err != nil {
		return fmt.Errorf("controller store: 检查 last_stable_fingerprint 列: %w", err)
	} else if !ok {
		if _, err := s.db.Exec(`ALTER TABLE managed_repositories ADD COLUMN last_stable_fingerprint TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("controller store: 迁移 last_stable_fingerprint: %w", err)
		}
	}
	// 记录迁移版本。
	var cur int
	err := s.db.QueryRow(`SELECT COALESCE((SELECT version FROM schema_version ORDER BY version DESC LIMIT 1),0)`).Scan(&cur)
	if err != nil {
		return fmt.Errorf("controller store: 读 schema_version: %w", err)
	}
	if cur < schemaVersion {
		_, err := s.db.Exec(`INSERT INTO schema_version(version) VALUES(?)`, schemaVersion)
		if err != nil {
			return fmt.Errorf("controller store: 写 schema_version: %w", err)
		}
	}
	return nil
}

// ─── ManagedRepo ────────────────────────────────────────────────

// UpsertRepo 插入或更新一个受管 repo。
func (s *Store) UpsertRepo(ctx context.Context, r model.ManagedRepo) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_repositories
		(id, github_owner, github_name, github_repository_id, source_url, branch, kg_group,
		 enabled, config_digest, last_seen_source_sha, last_stable_source_sha,
		 last_stable_bundle_digest, last_stable_fingerprint, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		 github_owner=excluded.github_owner, github_name=excluded.github_name,
		 github_repository_id=excluded.github_repository_id, source_url=excluded.source_url,
		 branch=excluded.branch, kg_group=excluded.kg_group, enabled=excluded.enabled,
		 config_digest=excluded.config_digest, updated_at=?`,
		r.ID, r.GitHubOwner, r.GitHubName, r.GitHubRepositoryID, r.SourceURL, r.Branch, r.KGGroup,
		btoi(r.Enabled), r.ConfigDigest, r.LastSeenSourceSHA, r.LastStableSourceSHA,
		r.LastStableBundleDigest, r.LastStableFingerprint, r.CreatedAt, now, now)
	return err
}

// GetRepo 按 id 读取受管 repo。
func (s *Store) GetRepo(ctx context.Context, id string) (*model.ManagedRepo, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, github_owner, github_name, github_repository_id,
		source_url, branch, kg_group, enabled, config_digest, last_seen_source_sha,
		last_stable_source_sha, last_stable_bundle_digest, last_stable_fingerprint, created_at, updated_at
		FROM managed_repositories WHERE id=?`, id)
	return scanRepo(row)
}

// ListRepos 返回全部受管 repo。
func (s *Store) ListRepos(ctx context.Context) ([]model.ManagedRepo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, github_owner, github_name, github_repository_id,
		source_url, branch, kg_group, enabled, config_digest, last_seen_source_sha,
		last_stable_source_sha, last_stable_bundle_digest, last_stable_fingerprint, created_at, updated_at
		FROM managed_repositories ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ManagedRepo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// UpdateRepoStable 更新 repo 的 stable 指针（含 builder fingerprint）。
func (s *Store) UpdateRepoStable(ctx context.Context, id, sourceSHA, bundleDigest, fingerprint string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `UPDATE managed_repositories
		SET last_stable_source_sha=?, last_stable_bundle_digest=?, last_stable_fingerprint=?,
		    last_seen_source_sha=?, updated_at=?
		WHERE id=?`, sourceSHA, bundleDigest, fingerprint, sourceSHA, now, id)
	return err
}

// UpdateRunFingerprint 更新 run 的 builder fingerprint。
func (s *Store) UpdateRunFingerprint(ctx context.Context, runID, fingerprint string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET builder_fingerprint=? WHERE run_id=?`, fingerprint, runID)
	return err
}

// UpdateRepoSeen 更新 last_seen_source_sha。
func (s *Store) UpdateRepoSeen(ctx context.Context, id, sourceSHA string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `UPDATE managed_repositories
		SET last_seen_source_sha=?, updated_at=? WHERE id=?`, sourceSHA, now, id)
	return err
}

// ─── Run ────────────────────────────────────────────────────────

// CreateRun 插入一条 run。
func (s *Store) CreateRun(ctx context.Context, r model.Run) error {
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO runs
		(run_id, repo_id, desired_source_sha, base_bundle_digest, builder_fingerprint,
		 state, attempt, reason, report_path, error_code, error_message, started_at, finished_at, next_retry_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RunID, r.RepoID, r.DesiredSourceSHA, r.BaseBundleDigest, r.BuilderFingerprint,
		string(r.State), r.Attempt, r.Reason, r.ReportPath, r.ErrorCode, r.ErrorMessage,
		r.StartedAt, ts(r.FinishedAt), ts(r.NextRetryAt))
	return err
}

// GetRun 读取 run。
func (s *Store) GetRun(ctx context.Context, runID string) (*model.Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT run_id, repo_id, desired_source_sha, base_bundle_digest,
		builder_fingerprint, state, attempt, reason, report_path, error_code, error_message,
		started_at, finished_at, next_retry_at FROM runs WHERE run_id=?`, runID)
	return scanRun(row)
}

// ListRuns 列出某 repo 的 run（可限数量）。
func (s *Store) ListRuns(ctx context.Context, repoID string, limit int) ([]model.Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, repo_id, desired_source_sha, base_bundle_digest,
		builder_fingerprint, state, attempt, reason, report_path, error_code, error_message,
		started_at, finished_at, next_retry_at FROM runs WHERE repo_id=? ORDER BY started_at DESC LIMIT ?`,
		repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ActiveRuns 返回非终态的活跃 run（调度器用）。
func (s *Store) ActiveRuns(ctx context.Context) ([]model.Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, repo_id, desired_source_sha, base_bundle_digest,
		builder_fingerprint, state, attempt, reason, report_path, error_code, error_message,
		started_at, finished_at, next_retry_at FROM runs
		WHERE state NOT IN ('Stable','Blocked','FailedPermanent','Stale')
		ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// TransitionRun 在事务中校验并执行状态迁移（§6：非法迁移直接拒绝）。
func (s *Store) TransitionRun(ctx context.Context, runID string, to model.RunState, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from string
	err = tx.QueryRowContext(ctx, `SELECT state FROM runs WHERE run_id=?`, runID).Scan(&from)
	if err != nil {
		return fmt.Errorf("controller store: 读取 run %s 状态: %w", runID, err)
	}
	if !model.CanTransition(model.RunState(from), to) {
		return fmt.Errorf("controller store: 非法状态迁移 %s → %s（run %s）", from, to, runID)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	finished := ""
	if to.IsTerminal() {
		finished = now
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET state=?, reason=?, finished_at=? WHERE run_id=?`,
		string(to), reason, finished, runID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SetRunError 记录 run 的错误信息与重试时间。
func (s *Store) SetRunError(ctx context.Context, runID, code, msg string, nextRetry time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET error_code=?, error_message=?, next_retry_at=? WHERE run_id=?`,
		code, msg, nextRetry.UTC().Format(time.RFC3339), runID)
	return err
}

// UpdateRunDesiredSHA 持久化 run 的 desired_source_sha（fetch 阶段获取后调用）。
func (s *Store) UpdateRunDesiredSHA(ctx context.Context, runID, sha string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET desired_source_sha=? WHERE run_id=?`, sha, runID)
	return err
}

// ─── PullRequest ────────────────────────────────────────────────

// UpsertPR 插入或更新 PR。
func (s *Store) UpsertPR(ctx context.Context, pr model.PullRequest) error {
	if pr.LastObservedAt.IsZero() {
		pr.LastObservedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO pull_requests
		(run_id, kind, owner, repo, number, branch, head_sha, base_branch, status, last_observed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(run_id, kind) DO UPDATE SET
		 owner=excluded.owner, repo=excluded.repo, number=excluded.number, branch=excluded.branch,
		 head_sha=excluded.head_sha, base_branch=excluded.base_branch, status=excluded.status,
		 last_observed_at=excluded.last_observed_at`,
		pr.RunID, string(pr.Kind), pr.Owner, pr.Repo, pr.Number, pr.Branch, pr.HeadSHA, pr.BaseBranch,
		string(pr.Status), ts(pr.LastObservedAt))
	return err
}

// GetPR 读取某 run 的某类 PR。
func (s *Store) GetPR(ctx context.Context, runID string, kind model.PRKind) (*model.PullRequest, error) {
	row := s.db.QueryRowContext(ctx, `SELECT run_id, kind, owner, repo, number, branch, head_sha,
		base_branch, status, last_observed_at FROM pull_requests WHERE run_id=? AND kind=?`,
		runID, string(kind))
	pr := &model.PullRequest{}
	var kindStr, statusStr, observed string
	err := row.Scan(&pr.RunID, &kindStr, &pr.Owner, &pr.Repo, &pr.Number, &pr.Branch, &pr.HeadSHA,
		&pr.BaseBranch, &statusStr, &observed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pr.Kind = model.PRKind(kindStr)
	pr.Status = model.PRStatus(statusStr)
	pr.LastObservedAt = parseTS(observed)
	return pr, nil
}

// ─── Candidate ──────────────────────────────────────────────────

// UpsertCandidate 插入或更新 candidate。
func (s *Store) UpsertCandidate(ctx context.Context, c model.Candidate) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO candidates
		(candidate_id, run_id, source_sha, expected_worktree_digest, bundle_path, bundle_digest,
		 release_id, release_tag, status)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(candidate_id) DO UPDATE SET
		 run_id=excluded.run_id, source_sha=excluded.source_sha,
		 expected_worktree_digest=excluded.expected_worktree_digest, bundle_path=excluded.bundle_path,
		 bundle_digest=excluded.bundle_digest, release_id=excluded.release_id,
		 release_tag=excluded.release_tag, status=excluded.status`,
		c.CandidateID, c.RunID, c.SourceSHA, c.ExpectedWorktreeDigest, c.BundlePath, c.BundleDigest,
		c.ReleaseID, c.ReleaseTag, string(c.Status))
	return err
}

// GetCandidate 读取 candidate。
func (s *Store) GetCandidate(ctx context.Context, candidateID string) (*model.Candidate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT candidate_id, run_id, source_sha, expected_worktree_digest,
		bundle_path, bundle_digest, release_id, release_tag, status FROM candidates WHERE candidate_id=?`,
		candidateID)
	c := &model.Candidate{}
	var statusStr string
	err := row.Scan(&c.CandidateID, &c.RunID, &c.SourceSHA, &c.ExpectedWorktreeDigest, &c.BundlePath,
		&c.BundleDigest, &c.ReleaseID, &c.ReleaseTag, &statusStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Status = model.CandidateStatus(statusStr)
	return c, nil
}

// ─── ExternalEffect（outbox 幂等去重）─────────────────────────

// ClaimEffect 声明一个外部动作：若 effect_key 已存在且已 applied，返回已存在的 externalID
// 与 applied=true（幂等跳过）；否则插入 pending 行返回 applied=false。
func (s *Store) ClaimEffect(ctx context.Context, e model.ExternalEffect) (externalID string, applied bool, err error) {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var existingID, existingStatus string
	err = tx.QueryRowContext(ctx, `SELECT external_id, status FROM external_effects WHERE effect_key=?`,
		e.EffectKey).Scan(&existingID, &existingStatus)
	if err == nil {
		// 已存在。
		if existingStatus == string(model.EffectApplied) {
			return existingID, true, tx.Commit()
		}
		return existingID, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO external_effects
		(effect_key, effect_type, target_repo, request_fingerprint, external_id, status, response_summary, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		e.EffectKey, e.EffectType, e.TargetRepo, e.RequestFingerprint, e.ExternalID,
		string(model.EffectPending), e.ResponseSummary, now, now)
	if err != nil {
		return "", false, err
	}
	return "", false, tx.Commit()
}

// MarkEffectApplied 标记外部动作为已应用，记录 externalID。
func (s *Store) MarkEffectApplied(ctx context.Context, effectKey, externalID, summary string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `UPDATE external_effects
		SET external_id=?, status=?, response_summary=?, updated_at=? WHERE effect_key=?`,
		externalID, string(model.EffectApplied), summary, now, effectKey)
	return err
}

// MarkEffectFailed 标记外部动作失败（可重试）。
func (s *Store) MarkEffectFailed(ctx context.Context, effectKey, summary string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `UPDATE external_effects
		SET status=?, response_summary=?, updated_at=? WHERE effect_key=?`,
		string(model.EffectFailed), summary, now, effectKey)
	return err
}

// ─── Lease ──────────────────────────────────────────────────────

// AcquireLease 尝试获取 repo 的租约。若已有未过期租约且 holder 不同则失败；
// 过期租约可被新 holder 接管（§14 崩溃恢复）。
func (s *Store) AcquireLease(ctx context.Context, repoID, holderID string, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	expires := now.Add(ttl)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var curHolder, curExpires string
	err = tx.QueryRowContext(ctx, `SELECT holder_id, expires_at FROM repo_leases WHERE repo_id=?`, repoID).
		Scan(&curHolder, &curExpires)
	if err == nil {
		exp, _ := time.Parse(time.RFC3339, curExpires)
		if exp.After(now) && curHolder != holderID {
			return false, tx.Commit() // 仍被占用。
		}
		// 过期或同一 holder：接管/续约。
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO repo_leases (repo_id, holder_id, acquired_at, expires_at, heartbeat_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(repo_id) DO UPDATE SET holder_id=excluded.holder_id, acquired_at=excluded.acquired_at,
		 expires_at=excluded.expires_at, heartbeat_at=excluded.heartbeat_at`,
		repoID, holderID, now.Format(time.RFC3339), expires.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// RenewLease 续约（心跳）。仅 holder 匹配时续约。
func (s *Store) RenewLease(ctx context.Context, repoID, holderID string, ttl time.Duration) error {
	now := time.Now().UTC()
	expires := now.Add(ttl)
	res, err := s.db.ExecContext(ctx, `UPDATE repo_leases
		SET expires_at=?, heartbeat_at=? WHERE repo_id=? AND holder_id=?`,
		expires.Format(time.RFC3339), now.Format(time.RFC3339), repoID, holderID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("controller store: lease 续约失败（holder 不匹配或已释放）: repo=%s", repoID)
	}
	return nil
}

// ReleaseLease 释放租约。
func (s *Store) ReleaseLease(ctx context.Context, repoID, holderID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM repo_leases WHERE repo_id=? AND holder_id=?`, repoID, holderID)
	return err
}

// ─── Webhook delivery 去重 ─────────────────────────────────────

// ClaimDelivery 声明一个 webhook delivery：已存在则返回 true（重复，不重复入队）。
func (s *Store) ClaimDelivery(ctx context.Context, deliveryID, eventType, repoID string) (duplicate bool, err error) {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT delivery_id FROM webhook_deliveries WHERE delivery_id=?`, deliveryID).Scan(&existing)
	if err == nil {
		return true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO webhook_deliveries (delivery_id, event_type, repo_id, received_at, status)
		VALUES (?,?,?,?,?)`, deliveryID, eventType, repoID, now, "received")
	if err != nil {
		return false, err
	}
	return false, tx.Commit()
}

// MarkDeliveryProcessed 标记 delivery 已处理。
func (s *Store) MarkDeliveryProcessed(ctx context.Context, deliveryID, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_deliveries SET processed_at=?, status=? WHERE delivery_id=?`,
		now, status, deliveryID)
	return err
}

// ─── 扫描工具 ────────────────────────────────────────────────────

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanRepo(s scanner) (*model.ManagedRepo, error) {
	r := &model.ManagedRepo{}
	var enabled int
	var created, updated string
	err := s.Scan(&r.ID, &r.GitHubOwner, &r.GitHubName, &r.GitHubRepositoryID, &r.SourceURL,
		&r.Branch, &r.KGGroup, &enabled, &r.ConfigDigest, &r.LastSeenSourceSHA,
		&r.LastStableSourceSHA, &r.LastStableBundleDigest, &r.LastStableFingerprint, &created, &updated)
	if err != nil {
		return nil, err
	}
	r.Enabled = enabled != 0
	r.CreatedAt = parseTS(created)
	r.UpdatedAt = parseTS(updated)
	return r, nil
}

func scanRun(s scanner) (*model.Run, error) {
	r := &model.Run{}
	var stateStr, started, finished, nextRetry string
	err := s.Scan(&r.RunID, &r.RepoID, &r.DesiredSourceSHA, &r.BaseBundleDigest,
		&r.BuilderFingerprint, &stateStr, &r.Attempt, &r.Reason, &r.ReportPath,
		&r.ErrorCode, &r.ErrorMessage, &started, &finished, &nextRetry)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("controller store: run 不存在")
		}
		return nil, err
	}
	r.State = model.RunState(stateStr)
	r.StartedAt = parseTS(started)
	r.FinishedAt = parseTS(finished)
	r.NextRetryAt = parseTS(nextRetry)
	return r, nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
