// Package model 定义 dkd 控制器的领域类型与状态机契约（产品化 Prompt §6）。
//
// 状态迁移必须有允许表；非法迁移直接拒绝并记录错误。所有状态迁移在事务中写入。
package model

import "time"

// RunState 是一次 run 的 reconcile 状态（§6 状态迁移）。
type RunState string

const (
	StateIdle                 RunState = "Idle"
	StateFetchSource          RunState = "FetchSource"
	StateRestoreBase          RunState = "RestoreBase"
	StatePlan                 RunState = "Plan"
	StateFullBuild            RunState = "FullBuild"
	StateIncrementalBuild     RunState = "IncrementalBuild"
	StateRebalanceCheck       RunState = "RebalanceCheck"
	StateRebalance            RunState = "Rebalance"
	StateValidateCandidate    RunState = "ValidateCandidate"
	StateCreateSourcePR       RunState = "CreateSourcePR"
	StateAwaitSourcePR        RunState = "AwaitSourcePR"
	StateReconcileMergedSource RunState = "ReconcileMergedSource"
	StateUploadCandidate      RunState = "UploadCandidate"
	StateCreateCatalogPR      RunState = "CreateCatalogPR"
	StateAwaitCatalogPR       RunState = "AwaitCatalogPR"
	StateStable               RunState = "Stable"
	StateBlocked              RunState = "Blocked"
	StateFailedRetryable      RunState = "FailedRetryable"
	StateFailedPermanent      RunState = "FailedPermanent"
	StateStale                RunState = "Stale"
)

// IsTerminal 报告状态是否为终态（不再自动推进，需外部触发）。
func (s RunState) IsTerminal() bool {
	switch s {
	case StateStable, StateBlocked, StateFailedPermanent, StateStale:
		return true
	}
	return false
}

// IsActive 报告状态是否仍可被调度器推进。
func (s RunState) IsActive() bool {
	switch s {
	case StateStable, StateBlocked, StateFailedPermanent, StateStale, StateIdle:
		return false
	}
	return true
}

// allowedTransitions 是状态迁移允许表（§6）。非法迁移直接拒绝。
var allowedTransitions = map[RunState][]RunState{
	StateIdle:                  {StateFetchSource},
	StateFetchSource:           {StateRestoreBase, StateFullBuild, StatePlan, StateStable, StateStale, StateFailedRetryable},
	StateRestoreBase:           {StatePlan, StateFullBuild, StateFailedRetryable},
	StatePlan:                  {StateFullBuild, StateIncrementalBuild, StateStable, StateFailedRetryable},
	StateFullBuild:             {StateValidateCandidate, StateFailedRetryable},
	StateIncrementalBuild:      {StateRebalanceCheck, StateValidateCandidate, StateFailedRetryable},
	StateRebalanceCheck:        {StateRebalance, StateValidateCandidate, StateFailedRetryable},
	StateRebalance:             {StateValidateCandidate, StateFailedRetryable},
	StateValidateCandidate:     {StateCreateSourcePR, StateUploadCandidate, StateFailedRetryable, StateFailedPermanent},
	StateCreateSourcePR:        {StateAwaitSourcePR, StateFailedRetryable},
	StateAwaitSourcePR:         {StateReconcileMergedSource, StateBlocked, StateStale, StateAwaitSourcePR},
	StateReconcileMergedSource: {StateUploadCandidate, StateIncrementalBuild, StateFailedRetryable},
	StateUploadCandidate:       {StateCreateCatalogPR, StateFailedRetryable},
	StateCreateCatalogPR:       {StateAwaitCatalogPR, StateFailedRetryable},
	StateAwaitCatalogPR:        {StateStable, StateBlocked, StateAwaitCatalogPR},
	StateFailedRetryable:       {StateIdle, StateFetchSource, StateRestoreBase, StatePlan, StateFullBuild,
		StateIncrementalBuild, StateRebalanceCheck, StateRebalance, StateValidateCandidate,
		StateCreateSourcePR, StateAwaitSourcePR, StateReconcileMergedSource,
		StateUploadCandidate, StateCreateCatalogPR, StateAwaitCatalogPR, StateFailedPermanent},
	StateBlocked: {StateIdle},
}

// CanTransition 报告 from → to 是否为合法迁移。
func CanTransition(from, to RunState) bool {
	if from == to {
		return true // 幂等重入同态允许。
	}
	allowed, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == to {
			return true
		}
	}
	return false
}

// PRKind 是 pull request 的种类。
type PRKind string

const (
	PRKindSourceWriteback PRKind = "source_writeback"
	PRKindCatalogPublish  PRKind = "catalog_publish"
)

// PRStatus 是 PR 的观测状态。
type PRStatus string

const (
	PRStatusOpen    PRStatus = "open"
	PRStatusMerged  PRStatus = "merged"
	PRStatusClosed  PRStatus = "closed"
)

// CandidateStatus 是 candidate Bundle 的状态。
type CandidateStatus string

const (
	CandidateBuilding  CandidateStatus = "building"
	CandidateUploaded  CandidateStatus = "uploaded"
	CandidateStable    CandidateStatus = "stable"
	CandidateStale     CandidateStatus = "stale"
	CandidateRejected  CandidateStatus = "rejected"
)

// EffectStatus 是外部动作的执行状态（exactly-once 去重）。
type EffectStatus string

const (
	EffectPending   EffectStatus = "pending"
	EffectApplied   EffectStatus = "applied"
	EffectSkipped   EffectStatus = "skipped"
	EffectFailed    EffectStatus = "failed"
)

// ─── 持久化行类型 ────────────────────────────────────────────────

// ManagedRepo 对应 managed_repositories 表。
type ManagedRepo struct {
	ID                   string
	GitHubOwner          string
	GitHubName           string
	GitHubRepositoryID   int64
	SourceURL            string
	Branch               string
	KGGroup              string
	Enabled              bool
	ConfigDigest         string
	LastSeenSourceSHA    string
	LastStableSourceSHA  string
	LastStableBundleDigest string
	LastStableFingerprint  string // 上次 stable 对应的 builder fingerprint（变化 → 需全量重建）
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Run 对应 runs 表。
type Run struct {
	RunID              string
	RepoID             string
	DesiredSourceSHA   string
	BaseBundleDigest   string
	BuilderFingerprint string
	State              RunState
	Attempt            int
	Reason             string
	ReportPath         string
	ErrorCode          string
	ErrorMessage       string
	StartedAt          time.Time
	FinishedAt         time.Time
	NextRetryAt        time.Time
}

// PullRequest 对应 pull_requests 表。
type PullRequest struct {
	RunID          string
	Kind           PRKind
	Owner          string
	Repo           string
	Number         int
	Branch         string
	HeadSHA        string
	BaseBranch     string
	Status         PRStatus
	LastObservedAt time.Time
}

// Candidate 对应 candidates 表。
type Candidate struct {
	CandidateID           string
	RunID                 string
	SourceSHA             string
	ExpectedWorktreeDigest string
	BundlePath            string
	BundleDigest          string
	ReleaseID             int64
	ReleaseTag            string
	Status                CandidateStatus
}

// ExternalEffect 对应 external_effects 表（at-least-once + 幂等去重）。
type ExternalEffect struct {
	EffectKey        string // 幂等键（如 push:<repo>:<branch>:<fingerprint>）
	EffectType       string // push | create_pr | create_release | upload_asset
	TargetRepo       string
	RequestFingerprint string
	ExternalID       string
	Status           EffectStatus
	ResponseSummary  string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// RepoLease 对应 repo_leases 表。
type RepoLease struct {
	RepoID     string
	HolderID   string
	AcquiredAt time.Time
	ExpiresAt  time.Time
	HeartbeatAt time.Time
}

// WebhookDelivery 对应 webhook_deliveries 表。
type WebhookDelivery struct {
	DeliveryID  string
	EventType   string
	RepoID      string
	ReceivedAt  time.Time
	ProcessedAt time.Time
	Status      string
}
