package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/bejix/upstream-ops/backend/sub2pool"
	"github.com/gin-gonic/gin"
)

func TestSub2PoolRoutesExposeOnlySafeSnapshotFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := newSub2PoolAPIStub()
	targets := sub2PoolTargetsStub{items: []storage.UpstreamSyncTarget{{
		ID:                1,
		Name:              "Sub2 main",
		BaseURL:           "https://must-not-leak.example",
		AdminAPIKeyCipher: "must-not-leak",
		Enabled:           true,
	}}}
	router := gin.New()
	RegisterSub2Pool(router.Group("/api"), service, targets)

	targetReq := httptest.NewRequest(http.MethodGet, "/api/sub2-pool/targets", nil)
	targetRec := httptest.NewRecorder()
	router.ServeHTTP(targetRec, targetReq)
	if targetRec.Code != http.StatusOK {
		t.Fatalf("targets status=%d body=%s", targetRec.Code, targetRec.Body.String())
	}
	if strings.Contains(targetRec.Body.String(), "must-not-leak") || strings.Contains(targetRec.Body.String(), "base_url") {
		t.Fatalf("target response leaked target connection data: %s", targetRec.Body.String())
	}

	snapshotReq := httptest.NewRequest(http.MethodGet, "/api/sub2-pool/targets/1/snapshot", nil)
	snapshotRec := httptest.NewRecorder()
	router.ServeHTTP(snapshotRec, snapshotReq)
	if snapshotRec.Code != http.StatusOK {
		t.Fatalf("snapshot status=%d body=%s", snapshotRec.Code, snapshotRec.Body.String())
	}
	body := snapshotRec.Body.String()
	for _, forbidden := range []string{"credentials", "base_url", "admin_api_key", "must-not-leak"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("snapshot response leaked %q: %s", forbidden, body)
		}
	}
	var response struct {
		Data struct {
			SnapshotSignature string `json:"snapshot_signature"`
			Summary           struct {
				TotalAccounts  int `json:"total_accounts"`
				HealthCoverage struct {
					Healthy int `json:"healthy"`
				} `json:"health_coverage"`
			} `json:"summary"`
			Accounts []struct {
				ID                 int64  `json:"id"`
				HealthStatus       string `json:"health_status"`
				RateLimitStatus    string `json:"rate_limit_status"`
				TodayRequests      *int   `json:"today_requests"`
				CurrentConcurrency int    `json:"current_concurrency"`
				MaxConcurrency     int    `json:"max_concurrency"`
				SchedulableReason  string `json:"schedulable_reason"`
			} `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(snapshotRec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if response.Data.SnapshotSignature != "preview-signature" ||
		response.Data.Summary.TotalAccounts != 1 ||
		response.Data.Summary.HealthCoverage.Healthy != 0 ||
		len(response.Data.Accounts) != 1 {
		t.Fatalf("snapshot response = %#v", response.Data)
	}
	account := response.Data.Accounts[0]
	if account.HealthStatus != "limited" ||
		account.RateLimitStatus != "rate_limited" ||
		account.TodayRequests == nil || *account.TodayRequests != 17 ||
		account.CurrentConcurrency != 3 ||
		account.MaxConcurrency != 8 {
		t.Fatalf("safe account dto = %#v", account)
	}
}

func TestSub2PoolApplyDelegatesSingleSnapshotValidationToService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := newSub2PoolAPIStub()
	router := gin.New()
	RegisterSub2Pool(router.Group("/api"), service, sub2PoolTargetsStub{items: []storage.UpstreamSyncTarget{{ID: 1, Name: "Sub2"}}})

	previewReq := httptest.NewRequest(http.MethodPost, "/api/sub2-pool/targets/1/preview", strings.NewReader(`{}`))
	previewReq.Header.Set("Content-Type", "application/json")
	previewRec := httptest.NewRecorder()
	router.ServeHTTP(previewRec, previewReq)
	if previewRec.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewRec.Code, previewRec.Body.String())
	}

	applyReq := httptest.NewRequest(http.MethodPost, "/api/sub2-pool/targets/1/apply", strings.NewReader(`{"snapshot_signature":"preview-signature"}`))
	applyReq.Header.Set("Content-Type", "application/json")
	applyRec := httptest.NewRecorder()
	router.ServeHTTP(applyRec, applyReq)
	if applyRec.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", applyRec.Code, applyRec.Body.String())
	}
	if service.applyCalls != 1 || service.lastApply.Signature != "preview-signature" ||
		len(service.lastApply.Proposals) != 0 || service.snapshotCalls != 1 {
		t.Fatalf("apply input = %#v calls=%d", service.lastApply, service.applyCalls)
	}

	conflictReq := httptest.NewRequest(http.MethodPost, "/api/sub2-pool/targets/1/apply", strings.NewReader(`{"snapshot_signature":"stale"}`))
	conflictReq.Header.Set("Content-Type", "application/json")
	conflictRec := httptest.NewRecorder()
	router.ServeHTTP(conflictRec, conflictReq)
	if conflictRec.Code != http.StatusConflict || service.applyCalls != 2 || service.snapshotCalls != 1 {
		t.Fatalf("conflict status=%d calls=%d body=%s", conflictRec.Code, service.applyCalls, conflictRec.Body.String())
	}
}

func TestSub2PoolSchedulableAndAutomationRoutesAcceptStringTargetIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := newSub2PoolAPIStub()
	router := gin.New()
	RegisterSub2Pool(router.Group("/api"), service, sub2PoolTargetsStub{items: []storage.UpstreamSyncTarget{{ID: 1, Name: "Sub2"}}})

	scheduleReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/sub2-pool/accounts/11/schedulable",
		strings.NewReader(`{"target_id":"1","schedulable":false}`),
	)
	scheduleReq.Header.Set("Content-Type", "application/json")
	scheduleRec := httptest.NewRecorder()
	router.ServeHTTP(scheduleRec, scheduleReq)
	if scheduleRec.Code != http.StatusOK || service.schedulableTarget != 1 || service.schedulableAccount != 11 || service.schedulableValue {
		t.Fatalf("schedulable status=%d service=%#v body=%s", scheduleRec.Code, service, scheduleRec.Body.String())
	}

	automationReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/sub2-pool/automation",
		strings.NewReader(`{"target_id":"1","enabled":true}`),
	)
	automationReq.Header.Set("Content-Type", "application/json")
	automationRec := httptest.NewRecorder()
	router.ServeHTTP(automationRec, automationReq)
	if automationRec.Code != http.StatusOK || service.automation == nil || !service.automation.Enabled {
		t.Fatalf("automation status=%d service=%#v body=%s", automationRec.Code, service.automation, automationRec.Body.String())
	}

	getAutomationReq := httptest.NewRequest(http.MethodGet, "/api/sub2-pool/automation?target_id=1", nil)
	getAutomationRec := httptest.NewRecorder()
	router.ServeHTTP(getAutomationRec, getAutomationReq)
	if getAutomationRec.Code != http.StatusOK || !strings.Contains(getAutomationRec.Body.String(), `"enabled":true`) {
		t.Fatalf("automation get status=%d body=%s", getAutomationRec.Code, getAutomationRec.Body.String())
	}
}

func TestSub2PoolAutomationPreparedErrorAndPartialAreNotSuccessful(t *testing.T) {
	now := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		run  sub2pool.RunRecord
	}{
		{
			name: "prepared",
			run:  sub2pool.RunRecord{Status: "prepared", CreatedAt: now},
		},
		{
			name: "error",
			run:  sub2pool.RunRecord{Status: "error_unavailable", CreatedAt: now},
		},
		{
			name: "partial",
			run:  sub2pool.RunRecord{Status: "partial", FailedCount: 1, CreatedAt: now},
		},
		{
			name: "manual_partial",
			run:  sub2pool.RunRecord{Status: "manual_partial", FailedCount: 1, CreatedAt: now},
		},
		{
			name: "recovered_partial",
			run:  sub2pool.RunRecord{Status: "recovered_partial", FailedCount: 1, CreatedAt: now},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := &sub2pool.AutomationStatus{
				AutomationState: sub2pool.AutomationState{TargetID: 1, Enabled: true, UpdatedAt: now},
				LastRun:         &tt.run,
			}
			dto := automationDTO(status)
			if dto.LastResult == nil {
				t.Fatalf("last result missing for status %q", tt.run.Status)
			}
			if dto.LastResult.Success {
				t.Fatalf("status %q reported successful: %#v", tt.run.Status, dto.LastResult)
			}
		})
	}
}

type sub2PoolTargetsStub struct {
	items []storage.UpstreamSyncTarget
	err   error
}

func (s sub2PoolTargetsStub) List() ([]storage.UpstreamSyncTarget, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]storage.UpstreamSyncTarget(nil), s.items...), nil
}

type sub2PoolAPIStub struct {
	snapshot           *sub2pool.Snapshot
	preview            *sub2pool.PriorityPreview
	snapshotCalls      int
	lastApply          sub2pool.ApplyInput
	applyCalls         int
	schedulableTarget  uint
	schedulableAccount int64
	schedulableValue   bool
	automation         *sub2pool.AutomationStatus
}

func newSub2PoolAPIStub() *sub2PoolAPIStub {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	rate := 0.1
	balance := 25.0
	requests := 17
	snapshot := &sub2pool.Snapshot{
		TargetID:    1,
		GeneratedAt: now,
		Summary: sub2pool.SnapshotSummary{
			AccountCount:         1,
			SchedulableCount:     1,
			HealthyCount:         0,
			MatchedCount:         1,
			BalanceReadyCount:    1,
			TodayStatsReadyCount: 1,
			RateReadyCount:       1,
			HealthCoverage:       sub2pool.HealthCoverage{Matched: 1, BalanceReady: 1, TodayStatsReady: 1, RateReady: 1, Healthy: 0},
		},
		Accounts: []sub2pool.AccountSnapshot{{
			ID:                 11,
			Name:               "account name",
			Platform:           "openai",
			Type:               "api_key",
			Status:             "active",
			Schedulable:        true,
			CurrentPriority:    30,
			CurrentConcurrency: 3,
			MaxConcurrency:     8,
			LowestGroups:       []sub2pool.GroupRef{{ID: 1, Name: "PLUS", Ratio: 0.1}},
			Channel:            sub2pool.ChannelPLUS,
			UpstreamRate:       &rate,
			Balance:            &balance,
			TodayStats:         sub2pool.TodayStats{Requests: &requests, Available: true},
			Availability: sub2pool.Availability{
				Matched: true, BalanceAvailable: true, TodayStatsReady: true, RateAvailable: true, Healthy: true,
			},
			Health: sub2pool.AccountHealth{RateLimited: true},
		}},
	}
	preview := &sub2pool.PriorityPreview{
		TargetID:  1,
		Signature: "preview-signature",
		Proposals: []sub2pool.PriorityProposal{{
			AccountID: 11, CurrentPriority: 30, TargetPriority: 10, Channel: sub2pool.ChannelPLUS,
		}},
		Changes: []sub2pool.PriorityProposal{{
			AccountID: 11, CurrentPriority: 30, TargetPriority: 10, Channel: sub2pool.ChannelPLUS,
		}},
	}
	return &sub2PoolAPIStub{
		snapshot: snapshot,
		preview:  preview,
		automation: &sub2pool.AutomationStatus{
			AutomationState: sub2pool.AutomationState{TargetID: 1, UpdatedAt: now},
		},
	}
}

func (s *sub2PoolAPIStub) SnapshotPreview(context.Context, uint) (*sub2pool.Snapshot, *sub2pool.PriorityPreview, error) {
	s.snapshotCalls++
	return s.snapshot, s.preview, nil
}

func (s *sub2PoolAPIStub) Apply(_ context.Context, targetID uint, input sub2pool.ApplyInput) (*sub2pool.ApplyResult, error) {
	s.applyCalls++
	s.lastApply = input
	if input.Signature != s.preview.Signature {
		return nil, sub2pool.ErrPreviewConflict
	}
	return &sub2pool.ApplyResult{
		TargetID: targetID,
		Applied:  []sub2pool.ApplyItem{{AccountID: 11, TargetPriority: 10, Status: "applied"}},
		Preview:  *s.preview,
	}, nil
}

func (s *sub2PoolAPIStub) SetSchedulable(_ context.Context, targetID uint, accountID int64, schedulable bool) (*sub2pool.SchedulableResult, error) {
	s.schedulableTarget = targetID
	s.schedulableAccount = accountID
	s.schedulableValue = schedulable
	return &sub2pool.SchedulableResult{TargetID: targetID, AccountID: accountID, Schedulable: schedulable}, nil
}

func (s *sub2PoolAPIStub) GetAutomation(uint) (*sub2pool.AutomationStatus, error) {
	return s.automation, nil
}

func (s *sub2PoolAPIStub) SetAutomation(targetID uint, enabled bool) (*sub2pool.AutomationStatus, error) {
	s.automation = &sub2pool.AutomationStatus{
		AutomationState: sub2pool.AutomationState{TargetID: targetID, Enabled: enabled, UpdatedAt: time.Now()},
	}
	return s.automation, nil
}
