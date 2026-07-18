package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/bejix/upstream-ops/backend/sub2pool"
	"github.com/gin-gonic/gin"
)

// Sub2PoolService is the narrow, safe surface needed by account-pool HTTP
// handlers. Keeping it separate from Deps lets main wire the isolated module
// without changing api.go while tests use a small stub.
type Sub2PoolService interface {
	SnapshotPreview(ctx context.Context, targetID uint) (*sub2pool.Snapshot, *sub2pool.PriorityPreview, error)
	CachedSnapshotPreview(ctx context.Context, targetID uint) (*sub2pool.Snapshot, *sub2pool.PriorityPreview, error)
	Apply(ctx context.Context, targetID uint, input sub2pool.ApplyInput) (*sub2pool.ApplyResult, error)
	SetSchedulable(ctx context.Context, targetID uint, accountID int64, schedulable bool) (*sub2pool.SchedulableResult, error)
	GetAutomation(targetID uint) (*sub2pool.AutomationStatus, error)
	SetAutomation(targetID uint, enabled bool) (*sub2pool.AutomationStatus, error)
}

type sub2PoolTargetLister interface {
	List() ([]storage.UpstreamSyncTarget, error)
}

// RegisterSub2Pool is called by the main integration point after it creates
// the isolated sub2pool.Service. It deliberately does not register itself in
// api.go, which is shared with the existing upstream-sync wiring.
func RegisterSub2Pool(g *gin.RouterGroup, service Sub2PoolService, targets sub2PoolTargetLister) {
	if service == nil || targets == nil {
		return
	}
	group := g.Group("/sub2-pool")
	group.GET("/targets", func(c *gin.Context) { listSub2PoolTargets(c, targets) })
	group.GET("/targets/:id/snapshot/cached", func(c *gin.Context) { getCachedSub2PoolSnapshot(c, service, targets) })
	group.GET("/targets/:id/snapshot", func(c *gin.Context) { getSub2PoolSnapshot(c, service, targets) })
	group.POST("/targets/:id/preview", func(c *gin.Context) { previewSub2PoolPriorities(c, service) })
	group.POST("/targets/:id/apply", func(c *gin.Context) { applySub2PoolPriorities(c, service) })
	group.PATCH("/accounts/:id/schedulable", func(c *gin.Context) { setSub2PoolSchedulable(c, service) })
	group.GET("/automation", func(c *gin.Context) { getSub2PoolAutomation(c, service) })
	group.PATCH("/automation", func(c *gin.Context) { setSub2PoolAutomation(c, service) })
}

type sub2PoolTargetDTO struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Enabled     bool       `json:"enabled"`
	RefreshedAt *time.Time `json:"refreshed_at,omitempty"`
}

func listSub2PoolTargets(c *gin.Context, targets sub2PoolTargetLister) {
	items, err := targets.List()
	if err != nil {
		failSub2Pool(c, err)
		return
	}
	out := make([]sub2PoolTargetDTO, 0, len(items))
	for _, item := range items {
		out = append(out, sub2PoolTargetDTO{
			ID:          strconv.FormatUint(uint64(item.ID), 10),
			Name:        item.Name,
			Enabled:     item.Enabled,
			RefreshedAt: item.LastCheckAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

type sub2PoolSnapshotDTO struct {
	TargetID          string                     `json:"target_id"`
	TargetName        string                     `json:"target_name,omitempty"`
	RefreshedAt       time.Time                  `json:"refreshed_at"`
	SnapshotSignature string                     `json:"snapshot_signature"`
	Summary           sub2PoolSnapshotSummaryDTO `json:"summary"`
	Accounts          []sub2PoolAccountDTO       `json:"accounts"`
}

type sub2PoolSnapshotSummaryDTO struct {
	TotalAccounts             int                     `json:"total_accounts"`
	SchedulableAccounts       int                     `json:"schedulable_accounts"`
	HealthyAccounts           int                     `json:"healthy_accounts"`
	DebtAccounts              int                     `json:"debt_accounts"`
	MissingMultiplierAccounts int                     `json:"missing_multiplier_accounts"`
	MissingDataAccounts       int                     `json:"missing_data_accounts"`
	MatchedAccounts           int                     `json:"matched_accounts"`
	BalanceReadyAccounts      int                     `json:"balance_ready_accounts"`
	TodayStatsReadyAccounts   int                     `json:"today_stats_ready_accounts"`
	RateReadyAccounts         int                     `json:"rate_ready_accounts"`
	HealthCoverage            sub2pool.HealthCoverage `json:"health_coverage"`
}

type sub2PoolAccountDTO struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	Platform           string    `json:"platform"`
	Type               string    `json:"type"`
	BusinessChannel    string    `json:"business_channel,omitempty"`
	MinGroup           string    `json:"min_group,omitempty"`
	CurrentPriority    int       `json:"current_priority"`
	SuggestedPriority  *int      `json:"suggested_priority,omitempty"`
	UpstreamMultiplier *float64  `json:"upstream_multiplier,omitempty"`
	Balance            *float64  `json:"balance,omitempty"`
	BalanceStatus      string    `json:"balance_status"`
	HealthStatus       string    `json:"health_status"`
	RateLimitStatus    string    `json:"rate_limit_status,omitempty"`
	Schedulable        bool      `json:"schedulable"`
	SchedulableReason  string    `json:"schedulable_reason,omitempty"`
	TodayRequests      *int      `json:"today_requests,omitempty"`
	CurrentConcurrency int       `json:"current_concurrency"`
	MaxConcurrency     int       `json:"max_concurrency"`
	MissingData        []string  `json:"missing_data"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func getSub2PoolSnapshot(c *gin.Context, service Sub2PoolService, targets sub2PoolTargetLister) {
	targetID, err := uintParam(c, "id")
	if err != nil {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	snapshot, preview, err := service.SnapshotPreview(c.Request.Context(), targetID)
	if err != nil {
		failSub2Pool(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": snapshotDTO(snapshot, preview, targetName(targets, targetID))})
}

func getCachedSub2PoolSnapshot(c *gin.Context, service Sub2PoolService, targets sub2PoolTargetLister) {
	targetID, err := uintParam(c, "id")
	if err != nil {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	snapshot, preview, err := service.CachedSnapshotPreview(c.Request.Context(), targetID)
	if errors.Is(err, sub2pool.ErrSnapshotCacheMissing) {
		c.JSON(http.StatusOK, gin.H{"data": nil})
		return
	}
	if err != nil {
		failSub2Pool(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": snapshotDTO(snapshot, preview, targetName(targets, targetID))})
}

func snapshotDTO(snapshot *sub2pool.Snapshot, preview *sub2pool.PriorityPreview, name string) sub2PoolSnapshotDTO {
	suggestions := make(map[int64]int, len(preview.Proposals))
	for _, proposal := range preview.Proposals {
		suggestions[proposal.AccountID] = proposal.TargetPriority
	}
	accounts := make([]sub2PoolAccountDTO, 0, len(snapshot.Accounts))
	debtCount := 0
	missingCount := 0
	missingMultiplierCount := 0
	for _, account := range snapshot.Accounts {
		item := accountDTO(account, suggestions, snapshot.GeneratedAt)
		if item.BalanceStatus == "debt" {
			debtCount++
		}
		if len(item.MissingData) > 0 {
			missingCount++
		}
		if !account.Availability.RateAvailable {
			missingMultiplierCount++
		}
		accounts = append(accounts, item)
	}
	return sub2PoolSnapshotDTO{
		TargetID:          strconv.FormatUint(uint64(snapshot.TargetID), 10),
		TargetName:        name,
		RefreshedAt:       snapshot.GeneratedAt,
		SnapshotSignature: preview.Signature,
		Summary: sub2PoolSnapshotSummaryDTO{
			TotalAccounts:             snapshot.Summary.AccountCount,
			SchedulableAccounts:       snapshot.Summary.SchedulableCount,
			HealthyAccounts:           snapshot.Summary.HealthyCount,
			DebtAccounts:              debtCount,
			MissingMultiplierAccounts: missingMultiplierCount,
			MissingDataAccounts:       missingCount,
			MatchedAccounts:           snapshot.Summary.MatchedCount,
			BalanceReadyAccounts:      snapshot.Summary.BalanceReadyCount,
			TodayStatsReadyAccounts:   snapshot.Summary.TodayStatsReadyCount,
			RateReadyAccounts:         snapshot.Summary.RateReadyCount,
			HealthCoverage:            snapshot.Summary.HealthCoverage,
		},
		Accounts: accounts,
	}
}

func accountDTO(account sub2pool.AccountSnapshot, suggestions map[int64]int, updatedAt time.Time) sub2PoolAccountDTO {
	item := sub2PoolAccountDTO{
		ID:                 account.ID,
		Name:               account.Name,
		Platform:           account.Platform,
		Type:               account.Type,
		BusinessChannel:    account.Channel,
		MinGroup:           groupNames(account.LowestGroups),
		CurrentPriority:    account.CurrentPriority,
		UpstreamMultiplier: clonePoolFloat(account.UpstreamRate),
		Balance:            clonePoolFloat(account.Balance),
		BalanceStatus:      poolBalanceStatus(account.Balance),
		HealthStatus:       poolHealthStatus(account),
		RateLimitStatus:    poolRateLimitStatus(account),
		Schedulable:        account.Schedulable,
		SchedulableReason:  account.SkipReason,
		TodayRequests:      clonePoolInt(account.TodayStats.Requests),
		CurrentConcurrency: account.CurrentConcurrency,
		MaxConcurrency:     account.MaxConcurrency,
		MissingData:        poolMissingData(account),
		UpdatedAt:          updatedAt,
	}
	if priority, exists := suggestions[account.ID]; exists {
		value := priority
		item.SuggestedPriority = &value
	}
	return item
}

func groupNames(groups []sub2pool.GroupRef) string {
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		if strings.TrimSpace(group.Name) != "" {
			names = append(names, group.Name)
		}
	}
	return strings.Join(names, " / ")
}

func poolBalanceStatus(balance *float64) string {
	if balance == nil {
		return "missing"
	}
	if *balance <= 0 {
		return "debt"
	}
	if *balance <= 10 {
		return "low"
	}
	return "ok"
}

func poolHealthStatus(account sub2pool.AccountSnapshot) string {
	switch {
	case account.Health.RateLimited:
		return "limited"
	case account.Health.TemporarilyUnschedulable:
		return "temporarily_unschedulable"
	case account.Health.Overloaded:
		return "overloaded"
	case !account.Availability.Matched:
		return "unknown"
	case strings.EqualFold(account.Status, "disabled"), strings.EqualFold(account.Status, "inactive"):
		return "disabled"
	case strings.TrimSpace(account.Status) == "":
		return "unknown"
	default:
		return "healthy"
	}
}

func poolRateLimitStatus(account sub2pool.AccountSnapshot) string {
	switch {
	case account.Health.RateLimited:
		return "rate_limited"
	case account.Health.TemporarilyUnschedulable:
		return "temporarily_unschedulable"
	case account.Health.Overloaded:
		return "overloaded"
	default:
		return ""
	}
}

func poolMissingData(account sub2pool.AccountSnapshot) []string {
	missing := make([]string, 0, 5)
	if len(account.LowestGroups) == 0 {
		missing = append(missing, "group")
	}
	if !account.Availability.Matched {
		missing = append(missing, "upstream_match")
	}
	if !account.Availability.BalanceAvailable {
		missing = append(missing, "balance")
	}
	if !account.Availability.RateAvailable {
		missing = append(missing, "upstream_multiplier")
	}
	if !account.Availability.TodayStatsReady {
		missing = append(missing, "today_stats")
	}
	if strings.TrimSpace(account.Status) == "" {
		missing = append(missing, "health_status")
	}
	return missing
}

func clonePoolFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func clonePoolInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

type sub2PoolPreviewInput struct {
	SnapshotSignature string `json:"snapshot_signature"`
}

type sub2PoolPreviewDTO struct {
	TargetID          string                       `json:"target_id"`
	SnapshotSignature string                       `json:"snapshot_signature"`
	SnapshotAt        time.Time                    `json:"snapshot_at"`
	Items             []sub2PoolPriorityPreviewDTO `json:"items"`
	Summary           sub2PoolPreviewSummaryDTO    `json:"summary"`
	Guards            []sub2pool.GuardViolation    `json:"guards,omitempty"`
}

type sub2PoolPriorityPreviewDTO struct {
	AccountID        int64    `json:"account_id"`
	AccountName      string   `json:"account_name"`
	BeforePriority   int      `json:"before_priority"`
	TargetPriority   *int     `json:"target_priority,omitempty"`
	SkipReason       string   `json:"skip_reason,omitempty"`
	MultiplierBefore *float64 `json:"multiplier_before,omitempty"`
	MultiplierTarget *float64 `json:"multiplier_target,omitempty"`
}

type sub2PoolPreviewSummaryDTO struct {
	Total   int `json:"total"`
	Changed int `json:"changed"`
	Skipped int `json:"skipped"`
}

func previewSub2PoolPriorities(c *gin.Context, service Sub2PoolService) {
	targetID, err := uintParam(c, "id")
	if err != nil {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	var input sub2PoolPreviewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	snapshot, preview, err := service.CachedSnapshotPreview(c.Request.Context(), targetID)
	if err != nil {
		failSub2Pool(c, err)
		return
	}
	if input.SnapshotSignature != "" && input.SnapshotSignature != preview.Signature {
		failSub2Pool(c, sub2pool.ErrPreviewConflict)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": previewDTO(snapshot, preview)})
}

func previewDTO(snapshot *sub2pool.Snapshot, preview *sub2pool.PriorityPreview) sub2PoolPreviewDTO {
	proposals := make(map[int64]sub2pool.PriorityProposal, len(preview.Proposals))
	for _, proposal := range preview.Proposals {
		proposals[proposal.AccountID] = proposal
	}
	items := make([]sub2PoolPriorityPreviewDTO, 0, len(snapshot.Accounts))
	changed := 0
	for _, account := range snapshot.Accounts {
		item := sub2PoolPriorityPreviewDTO{
			AccountID:        account.ID,
			AccountName:      account.Name,
			BeforePriority:   account.CurrentPriority,
			MultiplierBefore: clonePoolFloat(account.UpstreamRate),
			MultiplierTarget: clonePoolFloat(account.UpstreamRate),
			SkipReason:       account.SkipReason,
		}
		if proposal, exists := proposals[account.ID]; exists {
			target := proposal.TargetPriority
			item.TargetPriority = &target
			if proposal.CurrentPriority != proposal.TargetPriority {
				changed++
			} else if item.SkipReason == "" {
				item.SkipReason = "already_at_target"
			}
		} else if item.SkipReason == "" {
			item.SkipReason = "not_ranked"
		}
		items = append(items, item)
	}
	return sub2PoolPreviewDTO{
		TargetID:          strconv.FormatUint(uint64(preview.TargetID), 10),
		SnapshotSignature: preview.Signature,
		SnapshotAt:        snapshot.GeneratedAt,
		Items:             items,
		Summary: sub2PoolPreviewSummaryDTO{
			Total:   len(items),
			Changed: changed,
			Skipped: len(items) - changed,
		},
		Guards: append([]sub2pool.GuardViolation(nil), preview.Guards...),
	}
}

type sub2PoolApplyInput struct {
	SnapshotSignature string `json:"snapshot_signature"`
}

type sub2PoolApplyDTO struct {
	TargetID          string                  `json:"target_id"`
	SnapshotSignature string                  `json:"snapshot_signature"`
	AppliedAt         time.Time               `json:"applied_at"`
	Message           string                  `json:"message"`
	Summary           sub2PoolApplySummaryDTO `json:"summary"`
	Applied           []sub2pool.ApplyItem    `json:"applied,omitempty"`
	Failed            []sub2pool.ApplyItem    `json:"failed,omitempty"`
}

type sub2PoolApplySummaryDTO struct {
	PriorityChanges   int    `json:"priority_changes"`
	MultiplierChanges int    `json:"multiplier_changes"`
	Skipped           int    `json:"skipped"`
	CombinedResult    string `json:"combined_result,omitempty"`
}

func applySub2PoolPriorities(c *gin.Context, service Sub2PoolService) {
	targetID, err := uintParam(c, "id")
	if err != nil {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	var input sub2PoolApplyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	if input.SnapshotSignature == "" {
		failSub2Pool(c, sub2pool.ErrPreviewConflict)
		return
	}
	result, err := service.Apply(c.Request.Context(), targetID, sub2pool.ApplyInput{
		Signature: input.SnapshotSignature,
	})
	if err != nil {
		failSub2Pool(c, err)
		return
	}
	combined := "applied"
	if len(result.Failed) > 0 {
		combined = "partial"
	}
	c.JSON(http.StatusOK, gin.H{"data": sub2PoolApplyDTO{
		TargetID:          strconv.FormatUint(uint64(result.TargetID), 10),
		SnapshotSignature: result.Preview.Signature,
		AppliedAt:         time.Now(),
		Message:           "priority updates processed",
		Summary: sub2PoolApplySummaryDTO{
			PriorityChanges:   len(result.Applied),
			MultiplierChanges: result.RateChangeCount,
			Skipped:           len(result.Failed),
			CombinedResult:    combined,
		},
		Applied: append([]sub2pool.ApplyItem(nil), result.Applied...),
		Failed:  append([]sub2pool.ApplyItem(nil), result.Failed...),
	}})
}

type sub2PoolSchedulableInput struct {
	TargetID    json.RawMessage `json:"target_id"`
	Schedulable *bool           `json:"schedulable"`
}

func setSub2PoolSchedulable(c *gin.Context, service Sub2PoolService) {
	accountID, err := int64Param(c, "id")
	if err != nil || accountID <= 0 {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	var input sub2PoolSchedulableInput
	if err := c.ShouldBindJSON(&input); err != nil || input.Schedulable == nil {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	targetID, err := parseSub2PoolTargetID(input.TargetID)
	if err != nil {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	result, err := service.SetSchedulable(c.Request.Context(), targetID, accountID, *input.Schedulable)
	if err != nil {
		failSub2Pool(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"account_id":  result.AccountID,
		"schedulable": result.Schedulable,
		"message":     "schedulable updated",
		"updated_at":  time.Now(),
		"account": gin.H{
			"id":          result.AccountID,
			"schedulable": result.Schedulable,
			"updated_at":  time.Now(),
		},
	}})
}

type sub2PoolAutomationInput struct {
	TargetID json.RawMessage `json:"target_id"`
	Enabled  *bool           `json:"enabled"`
}

func getSub2PoolAutomation(c *gin.Context, service Sub2PoolService) {
	targetID, err := parseSub2PoolTargetText(c.Query("target_id"))
	if err != nil {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	status, err := service.GetAutomation(targetID)
	if err != nil {
		failSub2Pool(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": automationDTO(status)})
}

func setSub2PoolAutomation(c *gin.Context, service Sub2PoolService) {
	var input sub2PoolAutomationInput
	if err := c.ShouldBindJSON(&input); err != nil || input.Enabled == nil {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	targetID, err := parseSub2PoolTargetID(input.TargetID)
	if err != nil {
		failSub2Pool(c, sub2pool.ErrInvalidInput)
		return
	}
	status, err := service.SetAutomation(targetID, *input.Enabled)
	if err != nil {
		failSub2Pool(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"enabled":    status.Enabled,
			"message":    "automation updated",
			"updated_at": status.UpdatedAt,
			"status":     automationDTO(status),
		},
	})
}

type sub2PoolAutomationDTO struct {
	TargetID          string                           `json:"target_id"`
	Enabled           bool                             `json:"enabled"`
	LastRunAt         *time.Time                       `json:"last_run_at,omitempty"`
	LastResult        *sub2PoolAutomationLastResultDTO `json:"last_result,omitempty"`
	GuardBlocked      bool                             `json:"guard_blocked"`
	GuardBlockReasons []string                         `json:"guard_block_reasons,omitempty"`
	UpdatedAt         time.Time                        `json:"updated_at"`
}

type sub2PoolAutomationLastResultDTO struct {
	At                time.Time `json:"at"`
	Success           bool      `json:"success"`
	Summary           string    `json:"summary"`
	PriorityChanges   int       `json:"priority_changes"`
	MultiplierChanges int       `json:"multiplier_changes"`
	Skipped           int       `json:"skipped"`
	GuardBlocked      bool      `json:"guard_blocked"`
	GuardReason       string    `json:"guard_reason,omitempty"`
}

func automationDTO(status *sub2pool.AutomationStatus) sub2PoolAutomationDTO {
	out := sub2PoolAutomationDTO{
		TargetID:  strconv.FormatUint(uint64(status.TargetID), 10),
		Enabled:   status.Enabled,
		UpdatedAt: status.UpdatedAt,
	}
	if status.LastRun == nil {
		return out
	}
	last := status.LastRun
	at := last.CreatedAt
	out.LastRunAt = &at
	out.GuardBlocked = len(last.GuardCodes) > 0
	out.GuardBlockReasons = append([]string(nil), last.GuardCodes...)
	result := &sub2PoolAutomationLastResultDTO{
		At:                at,
		Success:           successfulPoolRun(last),
		Summary:           last.Status,
		PriorityChanges:   last.AppliedCount,
		MultiplierChanges: last.RateChangeCount,
		Skipped:           last.FailedCount,
		GuardBlocked:      out.GuardBlocked,
	}
	if len(last.GuardCodes) > 0 {
		result.GuardReason = strings.Join(last.GuardCodes, ",")
	}
	out.LastResult = result
	return out
}

func successfulPoolRun(run *sub2pool.RunRecord) bool {
	if run == nil || run.FailedCount > 0 || len(run.GuardCodes) > 0 {
		return false
	}
	switch run.Status {
	case "applied", "no_change", "manual_applied", "recovered":
		return true
	default:
		return false
	}
}

func targetName(targets sub2PoolTargetLister, targetID uint) string {
	items, err := targets.List()
	if err != nil {
		return ""
	}
	for _, item := range items {
		if item.ID == targetID {
			return item.Name
		}
	}
	return ""
}

func parseSub2PoolTargetID(raw json.RawMessage) (uint, error) {
	if len(raw) == 0 {
		return 0, errors.New("target id is required")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return parseSub2PoolTargetText(text)
	}
	var number uint64
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, err
	}
	if number == 0 {
		return 0, errors.New("target id is required")
	}
	return uint(number), nil
}

func parseSub2PoolTargetText(value string) (uint, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed == 0 {
		return 0, errors.New("target id is required")
	}
	return uint(parsed), nil
}

func failSub2Pool(c *gin.Context, err error) {
	status := http.StatusServiceUnavailable
	code := sub2pool.ErrUnavailable.Code
	var public *sub2pool.PublicError
	if errors.As(err, &public) {
		code = public.Code
		switch public.Code {
		case sub2pool.ErrInvalidInput.Code:
			status = http.StatusBadRequest
		case sub2pool.ErrNotFound.Code:
			status = http.StatusNotFound
		case sub2pool.ErrPreviewConflict.Code, sub2pool.ErrGuardBlocked.Code, sub2pool.ErrBusy.Code:
			status = http.StatusConflict
		}
	}
	c.JSON(status, gin.H{"error": code})
}
