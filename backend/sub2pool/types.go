// Package sub2pool implements an isolated Sub2 account-pool control plane.
// It intentionally does not reuse upstream-sync apply behavior.
package sub2pool

import (
	"context"
	"errors"
	"time"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
)

const (
	ChannelKiro   = "Kiro"
	ChannelCC     = "CC"
	ChannelPro    = "Pro"
	ChannelPLUS   = "PLUS"
	ChannelGrok   = "Grok"
	ChannelGemini = "Gemini"
	ChannelImage  = "Image"
	ChannelCN     = "CN"
	ChannelOther  = "Other"
)

var channelOrder = []string{
	ChannelKiro,
	ChannelCC,
	ChannelPro,
	ChannelPLUS,
	ChannelGrok,
	ChannelGemini,
	ChannelImage,
	ChannelCN,
	ChannelOther,
}

// TargetStore intentionally reuses the encrypted target credentials already
// owned by upstream-sync. It never accepts a caller-supplied URL.
type TargetStore interface {
	FindByID(id uint) (*storage.UpstreamSyncTarget, error)
}

type ChannelStore interface {
	List() ([]storage.Channel, error)
}

// ChannelKeyReader is implemented by channel.Service. Revealed keys are used
// only to calculate a transient SHA-256 digest and are never persisted here.
type ChannelKeyReader interface {
	ListAPIKeys(ctx context.Context, channelID uint, query connector.APIKeyQuery) (*connector.APIKeyPage, error)
	RevealAPIKey(ctx context.Context, channelID uint, keyID int64) (string, error)
}

type AdminGateway interface {
	ListGroups(ctx context.Context, target sub2api.AdminTarget, includeInactive bool) ([]sub2api.AdminGroup, error)
	ListAllPoolAccounts(ctx context.Context, target sub2api.AdminTarget) ([]sub2api.PoolAccount, error)
	GetPoolAccount(ctx context.Context, target sub2api.AdminTarget, accountID int64) (*sub2api.PoolAccount, error)
	UpdatePoolAccountPriority(ctx context.Context, target sub2api.AdminTarget, accountID int64, priority int) (*sub2api.PoolAccount, error)
	SetPoolAccountSchedulable(ctx context.Context, target sub2api.AdminTarget, accountID int64, schedulable bool) (*sub2api.PoolAccount, error)
}

type todayStatsGateway interface {
	GetPoolTodayStatsBatch(ctx context.Context, target sub2api.AdminTarget, accountIDs []int64) (map[int64]sub2api.PoolTodayStats, error)
}

type EventDispatcher interface {
	DispatchPoolEvent(ctx context.Context, event PoolEvent) error
}

type AutomationStore interface {
	StateStore
	LoadAutomation(targetID uint) (AutomationState, error)
	SaveAutomation(state AutomationState) error
	EnqueueOutbox(targetID uint, event PoolEvent) (uint, bool, error)
	ListPendingOutbox(targetID uint, limit int) ([]OutboxItem, error)
	MarkOutboxDelivery(outboxID uint, delivered bool) error
	RecordRun(record RunRecord) (uint, error)
	FinalizeCycle(targetID, runID uint, record RunRecord, state TargetState, event *PoolEvent) (uint, bool, error)
	UpdateRunNotification(runID uint, status string) error
	ListRuns(targetID uint, limit int) ([]RunRecord, error)
	ListPreparedRuns(targetID uint, limit int) ([]RunRecord, error)
}

type LeaseStore interface {
	AcquireLease(targetID uint, owner string, expiresAt time.Time) (bool, error)
	RenewLease(targetID uint, owner string, expiresAt time.Time) (bool, error)
	ReleaseLease(targetID uint, owner string) error
}

type Config struct {
	MinimumAccountCount          int
	MaximumChanges               int
	LowBalanceThreshold          float64
	AccountRateMapImportPath     string
	AccountRateMapImportTargetID uint
}

type AccountRateMapping struct {
	TargetID   uint
	AccountID  int64
	SiteURL    string
	ModelName  string
	ManualRate *float64
	Enabled    bool
}

type AccountRateMappingStore interface {
	ListAccountRateMappings(targetID uint) ([]AccountRateMapping, error)
}

// KeyAttestation binds one current Sub2 API-key fingerprint to one monitored
// same-origin channel. It is explicit operator data, never a name heuristic.
type KeyAttestation struct {
	TargetID     uint      `json:"target_id"`
	AccountID    int64     `json:"account_id"`
	APIKeySHA256 string    `json:"-"`
	ChannelID    uint      `json:"channel_id"`
	Source       string    `json:"source"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type KeyAttestationInput struct {
	AccountID int64 `json:"account_id"`
	ChannelID uint  `json:"channel_id"`
}

type KeyAttestationStore interface {
	ListKeyAttestations(targetID uint) ([]KeyAttestation, error)
	UpsertKeyAttestations(items []KeyAttestation) error
// DiscoveryAccountStore identifies accounts created by the discovery review
// workflow. They remain routable, but are excluded from automated priority
// writes until an explicit promotion path exists.
type DiscoveryAccountStore interface {
	ListAppliedTargetAccountIDs(targetID uint) ([]int64, error)
}

type RateSnapshotStore interface {
	ListByChannel(channelID uint) ([]storage.RateSnapshot, error)
}

// SnapshotCacheStore persists the latest successful account-pool snapshot per
// target so the page can render cached data without hitting the upstream.
type SnapshotCacheStore interface {
	LoadCachedSnapshot(targetID uint) (*Snapshot, *PriorityPreview, error)
	SaveCachedSnapshot(snapshot *Snapshot, preview *PriorityPreview) error
}

func (c Config) withDefaults() Config {
	if c.MinimumAccountCount <= 0 {
		c.MinimumAccountCount = 20
	}
	if c.MaximumChanges <= 0 {
		c.MaximumChanges = 20
	}
	if c.LowBalanceThreshold <= 0 {
		c.LowBalanceThreshold = 10
	}
	return c
}

type Snapshot struct {
	TargetID    uint              `json:"target_id"`
	GeneratedAt time.Time         `json:"generated_at"`
	Summary     SnapshotSummary   `json:"summary"`
	Groups      []GroupSnapshot   `json:"groups"`
	Accounts    []AccountSnapshot `json:"accounts"`
}

type SnapshotSummary struct {
	AccountCount         int            `json:"account_count"`
	SchedulableCount     int            `json:"schedulable_count"`
	HealthyCount         int            `json:"healthy_count"`
	MatchedCount         int            `json:"matched_count"`
	BalanceReadyCount    int            `json:"balance_ready_count"`
	TodayStatsReadyCount int            `json:"today_stats_ready_count"`
	RateReadyCount       int            `json:"rate_ready_count"`
	HealthCoverage       HealthCoverage `json:"health_coverage"`
}

type HealthCoverage struct {
	Matched         int `json:"matched"`
	BalanceReady    int `json:"balance_ready"`
	TodayStatsReady int `json:"today_stats_ready"`
	RateReady       int `json:"rate_ready"`
	Healthy         int `json:"healthy"`
}

type GroupSnapshot struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	Ratio  float64 `json:"ratio"`
	Status string  `json:"status"`
}

type AccountSnapshot struct {
	ID                 int64         `json:"id"`
	Name               string        `json:"name"`
	Platform           string        `json:"platform"`
	Type               string        `json:"type"`
	Status             string        `json:"status"`
	Schedulable        bool          `json:"schedulable"`
	PoolManaged        bool          `json:"pool_managed"`
	CurrentPriority    int           `json:"current_priority"`
	CurrentConcurrency int           `json:"current_concurrency"`
	MaxConcurrency     int           `json:"max_concurrency"`
	GroupIDs           []int64       `json:"group_ids"`
	LowestGroups       []GroupRef    `json:"lowest_groups"`
	Channel            string        `json:"channel"`
	UpstreamURL        string        `json:"upstream_url,omitempty"`
	UpstreamRate       *float64      `json:"upstream_rate,omitempty"`
	Balance            *float64      `json:"balance,omitempty"`
	TodayStats         TodayStats    `json:"today_stats"`
	Availability       Availability  `json:"availability"`
	Health             AccountHealth `json:"health"`
	SkipReason         string        `json:"skip_reason,omitempty"`
	MatchStatus        string        `json:"match_status"`
	FingerprintState   string        `json:"fingerprint_state"`
	// MultiplierSource: key_exact | key_attested | account_mapping | display_only | "".
	// key_attested is an explicit same-origin fingerprint binding, not a name
	// or group fallback.
	MultiplierSource string `json:"multiplier_source,omitempty"`
	DiscoveryManaged bool   `json:"-"`
	IdentityDigest   string `json:"-"`
}

type GroupRef struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Ratio float64 `json:"ratio"`
}

type TodayStats struct {
	Requests  *int     `json:"requests,omitempty"`
	Cost      *float64 `json:"cost,omitempty"`
	Available bool     `json:"available"`
}

type Availability struct {
	Matched          bool `json:"matched"`
	BalanceAvailable bool `json:"balance_available"`
	TodayStatsReady  bool `json:"today_stats_ready"`
	RateAvailable    bool `json:"rate_available"`
	Healthy          bool `json:"healthy"`
}

type AccountHealth struct {
	RateLimited              bool `json:"rate_limited"`
	TemporarilyUnschedulable bool `json:"temporarily_unschedulable"`
	Overloaded               bool `json:"overloaded"`
}

type PriorityProposal struct {
	AccountID       int64  `json:"account_id"`
	AccountName     string `json:"account_name"`
	CurrentPriority int    `json:"current_priority"`
	TargetPriority  int    `json:"target_priority"`
	Channel         string `json:"channel"`
	Reason          string `json:"reason"`

	expectedGroupIDs       []int64
	expectedStatus         string
	expectedPoolManaged    bool
	expectedIdentityDigest string
	stagingPriority        int
}

type PriorityPreview struct {
	TargetID             uint               `json:"target_id"`
	GeneratedAt          time.Time          `json:"generated_at"`
	Signature            string             `json:"signature"`
	Proposals            []PriorityProposal `json:"proposals"`
	Changes              []PriorityProposal `json:"changes"`
	MissingMultiplierIDs []int64            `json:"missing_multiplier_ids"`
	MissingBalanceIDs    []int64            `json:"missing_balance_ids"`
	UnknownChannelIDs    []int64            `json:"unknown_channel_ids"`
	Guards               []GuardViolation   `json:"guards"`
}

type GuardViolation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

type ApplyInput struct {
	Signature string             `json:"signature"`
	Proposals []PriorityProposal `json:"proposals"`
}

type ApplyResult struct {
	TargetID        uint            `json:"target_id"`
	Applied         []ApplyItem     `json:"applied"`
	Failed          []ApplyItem     `json:"failed"`
	Remaining       int             `json:"remaining"`
	RateChangeCount int             `json:"rate_change_count"`
	StatePersisted  bool            `json:"state_persisted"`
	Preview         PriorityPreview `json:"preview"`
}

type ApplyItem struct {
	AccountID      int64  `json:"account_id"`
	AccountName    string `json:"account_name"`
	Channel        string `json:"channel"`
	BeforePriority int    `json:"before_priority"`
	TargetPriority int    `json:"target_priority"`
	AfterPriority  *int   `json:"after_priority,omitempty"`
	Status         string `json:"status"`
}

type SchedulableResult struct {
	TargetID    uint  `json:"target_id"`
	AccountID   int64 `json:"account_id"`
	Schedulable bool  `json:"schedulable"`
}

type PoolEvent struct {
	EventID              string           `json:"event_id"`
	TargetID             uint             `json:"target_id"`
	GeneratedAt          time.Time        `json:"generated_at"`
	RateChanges          []RateChange     `json:"rate_changes"`
	PriorityResult       *ApplyResult     `json:"priority_result,omitempty"`
	MissingMultiplierIDs []int64          `json:"missing_multiplier_ids"`
	MissingBalanceIDs    []int64          `json:"missing_balance_ids"`
	LowBalances          []LowBalance     `json:"low_balances"`
	Guards               []GuardViolation `json:"guards"`
}

type RateChange struct {
	AccountID         int64    `json:"account_id"`
	AccountName       string   `json:"account_name"`
	PreviousRate      *float64 `json:"previous_rate,omitempty"`
	CurrentRate       *float64 `json:"current_rate,omitempty"`
	PreviousGroupRate *float64 `json:"previous_group_rate,omitempty"`
	CurrentGroupRate  *float64 `json:"current_group_rate,omitempty"`
}

type LowBalance struct {
	AccountID   int64   `json:"account_id"`
	AccountName string  `json:"account_name"`
	Balance     float64 `json:"balance"`
}

type RunResult struct {
	Preview            PriorityPreview `json:"preview"`
	Apply              *ApplyResult    `json:"apply,omitempty"`
	RateChangeCount    int             `json:"rate_change_count"`
	RunID              uint            `json:"run_id,omitempty"`
	OutboxID           uint            `json:"outbox_id,omitempty"`
	NotificationStatus string          `json:"notification_status"`
	NotificationFailed bool            `json:"notification_failed"`
	StatePersisted     bool            `json:"state_persisted"`
}

type OutboxItem struct {
	ID        uint      `json:"id"`
	RunID     uint      `json:"run_id"`
	TargetID  uint      `json:"target_id"`
	Event     PoolEvent `json:"event"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"created_at"`
}

type RunRecord struct {
	ID                 uint               `json:"id"`
	TargetID           uint               `json:"target_id"`
	Status             string             `json:"status"`
	PreviewSignature   string             `json:"preview_signature"`
	ChangeCount        int                `json:"change_count"`
	RateChangeCount    int                `json:"rate_change_count"`
	AppliedCount       int                `json:"applied_count"`
	FailedCount        int                `json:"failed_count"`
	GuardCount         int                `json:"guard_count"`
	GuardCodes         []string           `json:"guard_codes,omitempty"`
	StatePersisted     bool               `json:"state_persisted"`
	NotificationStatus string             `json:"notification_status"`
	Intent             []PriorityProposal `json:"intent,omitempty"`
	CreatedAt          time.Time          `json:"created_at"`
}

type AutomationState struct {
	TargetID  uint      `json:"target_id"`
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AutomationStatus struct {
	AutomationState
	LastRun *RunRecord `json:"last_run,omitempty"`
}

type OutboxDispatchResult struct {
	Attempted int `json:"attempted"`
	Delivered int `json:"delivered"`
	Failed    int `json:"failed"`
}

// PublicError intentionally excludes the underlying remote error text. API
// callers must not receive credentials, target URLs, or unfiltered upstream
// response bodies.
type PublicError struct {
	Code string
}

func (e *PublicError) Error() string { return e.Code }

func isPublicError(err error, code string) bool {
	var public *PublicError
	return errors.As(err, &public) && public.Code == code
}

var (
	ErrPreviewConflict      = &PublicError{Code: "preview_conflict"}
	ErrGuardBlocked         = &PublicError{Code: "guard_blocked"}
	ErrNotFound             = &PublicError{Code: "target_not_found"}
	ErrUnavailable          = &PublicError{Code: "pool_unavailable"}
	ErrInvalidInput         = &PublicError{Code: "invalid_input"}
	ErrBusy                 = &PublicError{Code: "target_busy"}
	ErrSnapshotCacheMissing = &PublicError{Code: "snapshot_cache_missing"}
)
