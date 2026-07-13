package sub2pool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestGormOutboxDeduplicatesByRunNotIdenticalEventContent(t *testing.T) {
	db, store := newRecoveryGormStore(t)
	event := PoolEvent{
		TargetID:    1,
		GeneratedAt: time.Date(2026, 7, 12, 1, 2, 3, 0, time.UTC),
		RateChanges: []RateChange{{
			AccountID:   11,
			AccountName: "same event",
			CurrentRate: floatPtr(0.1),
		}},
	}

	firstRun := recordPreparedRun(t, store, 1, "sig-1", nil)
	firstOutbox, firstCreated, err := store.FinalizeCycle(
		1,
		firstRun,
		RunRecord{TargetID: 1, Status: "applied", PreviewSignature: "sig-1", NotificationStatus: "queued"},
		TargetState{LastHealthyCount: 1},
		&event,
	)
	if err != nil {
		t.Fatalf("finalize first run: %v", err)
	}
	if !firstCreated || firstOutbox == 0 {
		t.Fatalf("first outbox id=%d created=%v", firstOutbox, firstCreated)
	}

	secondRun := recordPreparedRun(t, store, 1, "sig-2", nil)
	secondOutbox, secondCreated, err := store.FinalizeCycle(
		1,
		secondRun,
		RunRecord{TargetID: 1, Status: "applied", PreviewSignature: "sig-2", NotificationStatus: "queued"},
		TargetState{LastHealthyCount: 1},
		&event,
	)
	if err != nil {
		t.Fatalf("finalize second run: %v", err)
	}
	if !secondCreated || secondOutbox == 0 || secondOutbox == firstOutbox {
		t.Fatalf("second outbox id=%d created=%v first=%d", secondOutbox, secondCreated, firstOutbox)
	}

	var rows []GormPoolOutbox
	if err := db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("list outbox rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("outbox row count=%d rows=%#v", len(rows), rows)
	}
	if rows[0].RunID == rows[1].RunID || rows[0].EventKey != rows[1].EventKey || rows[0].EventJSON != rows[1].EventJSON {
		t.Fatalf("identical event content should persist once per run: %#v", rows)
	}
}

func TestGormOutboxAllowsDistinctFailureAndSuccessEventsForOneRun(t *testing.T) {
	db, store := newRecoveryGormStore(t)
	runID := recordPreparedRun(t, store, 1, "sig", nil)
	failure := PoolEvent{
		EventID:  "failure",
		TargetID: 1,
		PriorityResult: &ApplyResult{Failed: []ApplyItem{{
			AccountID: 7,
			Status:    "failed",
			Stage:     "write",
			Code:      "upstream_write_failed",
		}}},
	}
	failureID, created, err := store.EnqueueRunOutbox(1, runID, failure)
	if err != nil || !created || failureID == 0 {
		t.Fatalf("enqueue failure id=%d created=%v err=%v", failureID, created, err)
	}
	failure.GeneratedAt = time.Now()
	failure.PriorityResult.Preview.GeneratedAt = time.Now()
	duplicateID, duplicateCreated, err := store.EnqueueRunOutbox(1, runID, failure)
	if err != nil || duplicateCreated || duplicateID != failureID {
		t.Fatalf("deduplicate failure id=%d created=%v err=%v", duplicateID, duplicateCreated, err)
	}

	success := PoolEvent{
		EventID:  "success",
		TargetID: 1,
		PriorityResult: &ApplyResult{Applied: []ApplyItem{{
			AccountID: 7,
			Status:    "recovered_applied",
		}}},
	}
	successID, successCreated, err := store.EnqueueRunOutbox(1, runID, success)
	if err != nil || !successCreated || successID == 0 || successID == failureID {
		t.Fatalf("enqueue success id=%d created=%v failure=%d err=%v", successID, successCreated, failureID, err)
	}

	var rows []GormPoolOutbox
	if err := db.Where("target_id = ? AND run_id = ?", 1, runID).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("list run outbox rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("outbox rows=%#v", rows)
	}
}

func TestGormFinalizePreparedRunIsCASAndDoesNotOverwriteAudit(t *testing.T) {
	db, store := newRecoveryGormStore(t)
	runID := recordPreparedRun(t, store, 1, "prepared-sig", nil)
	firstEvent := PoolEvent{
		TargetID:    1,
		RateChanges: []RateChange{{AccountID: 7, AccountName: "first", CurrentRate: floatPtr(0.1)}},
	}
	firstState := TargetState{
		LastHealthyCount:    3,
		MultiplierByAccount: map[int64]MultiplierState{7: {UpstreamRate: floatPtr(0.1)}},
	}
	if _, _, err := store.FinalizeCycle(
		1,
		runID,
		RunRecord{
			TargetID:           1,
			Status:             "applied",
			PreviewSignature:   "first-sig",
			AppliedCount:       1,
			GuardCodes:         []string{"first_guard"},
			NotificationStatus: "queued",
		},
		firstState,
		&firstEvent,
	); err != nil {
		t.Fatalf("finalize first time: %v", err)
	}

	secondEvent := PoolEvent{
		TargetID:    1,
		RateChanges: []RateChange{{AccountID: 8, AccountName: "second", CurrentRate: floatPtr(0.2)}},
	}
	_, _, err := store.FinalizeCycle(
		1,
		runID,
		RunRecord{
			TargetID:           1,
			Status:             "partial",
			PreviewSignature:   "second-sig",
			AppliedCount:       99,
			FailedCount:        99,
			GuardCodes:         []string{"overwritten_guard"},
			NotificationStatus: "overwritten",
		},
		TargetState{
			LastHealthyCount:    99,
			MultiplierByAccount: map[int64]MultiplierState{8: {UpstreamRate: floatPtr(0.2)}},
		},
		&secondEvent,
	)
	if err == nil || !strings.Contains(err.Error(), "prepared run not found") {
		t.Fatalf("second finalize err=%v, want prepared CAS failure", err)
	}

	runs, err := store.ListRuns(1, 1)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs=%#v", runs)
	}
	got := runs[0]
	if got.Status != "applied" ||
		got.PreviewSignature != "first-sig" ||
		got.AppliedCount != 1 ||
		got.FailedCount != 0 ||
		got.NotificationStatus != "queued" ||
		len(got.GuardCodes) != 1 ||
		got.GuardCodes[0] != "first_guard" {
		t.Fatalf("audit was overwritten: %#v", got)
	}
	state, err := store.Load(1)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.LastHealthyCount != 3 || state.MultiplierByAccount[8].UpstreamRate != nil {
		t.Fatalf("state was overwritten after CAS failure: %#v", state)
	}
	var outboxCount int64
	if err := db.Model(&GormPoolOutbox{}).Count(&outboxCount).Error; err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("outbox count=%d, want only first finalize row", outboxCount)
	}
}

func TestLostLeaseStopsRemoteWritesAndPreparedRunCanRecover(t *testing.T) {
	_, baseStore := newRecoveryGormStore(t)
	store := &expiringRecoveryStore{GormStateStore: baseStore, failAfterSuccessfulRenews: 0}
	service, admin := newTestServiceWithState(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
	}, Config{MinimumAccountCount: 1}, store)

	_, err := service.Run(context.Background(), 1)
	if !isPublicError(err, ErrBusy.Code) {
		t.Fatalf("first run err=%v, want lost lease busy error", err)
	}
	if admin.updateCount != 0 {
		t.Fatalf("remote priority update count=%d, want none after lost lease", admin.updateCount)
	}
	prepared, err := store.ListPreparedRuns(1, 10)
	if err != nil {
		t.Fatalf("list prepared runs: %v", err)
	}
	if len(prepared) != 1 || prepared[0].Status != "prepared" || len(prepared[0].Intent) == 0 {
		t.Fatalf("prepared run not recoverable: %#v", prepared)
	}

	store.failAfterSuccessfulRenews = -1
	recovered, err := service.Run(context.Background(), 1)
	if err != nil {
		t.Fatalf("recover prepared run: %v", err)
	}
	if recovered == nil || recovered.RunID != prepared[0].ID || recovered.Apply == nil || len(recovered.Apply.Applied) != 1 {
		t.Fatalf("recovered result=%#v", recovered)
	}
	if admin.updateCount != 2 || admin.priority(1) != 10 {
		t.Fatalf("recovery writes=%d priority=%d", admin.updateCount, admin.priority(1))
	}
	prepared, err = store.ListPreparedRuns(1, 10)
	if err != nil {
		t.Fatalf("list prepared after recovery: %v", err)
	}
	if len(prepared) != 0 {
		t.Fatalf("prepared run remained after recovery finalize: %#v", prepared)
	}
}

func TestGormPersistedIntentRetainsRecoveryGuards(t *testing.T) {
	_, store := newRecoveryGormStore(t)
	proposal := PriorityProposal{
		AccountID:              7,
		AccountName:            "guarded",
		CurrentPriority:        90,
		stagingPriority:        110,
		TargetPriority:         10,
		Channel:                ChannelPLUS,
		Reason:                 "rate_order",
		expectedGroupIDs:       []int64{3, 1},
		expectedStatus:         "active",
		expectedPoolManaged:    true,
		expectedIdentityDigest: "identity-digest",
	}
	runID := recordPreparedRun(t, store, 1, "guarded-sig", []PriorityProposal{proposal})
	proposal.expectedGroupIDs[0] = 99
	proposal.expectedStatus = "disabled"
	proposal.expectedPoolManaged = false
	proposal.expectedIdentityDigest = "mutated"

	runs, err := store.ListPreparedRuns(1, 1)
	if err != nil {
		t.Fatalf("list prepared runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != runID || len(runs[0].Intent) != 1 {
		t.Fatalf("prepared runs=%#v", runs)
	}
	got := runs[0].Intent[0]
	if got.expectedIdentityDigest != "identity-digest" ||
		got.expectedStatus != "active" ||
		!got.expectedPoolManaged ||
		got.stagingPriority != 110 ||
		!equalInt64Sets(got.expectedGroupIDs, []int64{1, 3}) {
		t.Fatalf("persisted intent guards=%#v", got)
	}
}

type expiringRecoveryStore struct {
	*GormStateStore
	failAfterSuccessfulRenews int
	successfulRenews          int
}

func (s *expiringRecoveryStore) RenewLease(targetID uint, owner string, expiresAt time.Time) (bool, error) {
	if s.failAfterSuccessfulRenews >= 0 && s.successfulRenews >= s.failAfterSuccessfulRenews {
		return false, nil
	}
	renewed, err := s.GormStateStore.RenewLease(targetID, owner, expiresAt)
	if err != nil {
		return false, err
	}
	if renewed {
		s.successfulRenews++
	}
	return renewed, nil
}

func newRecoveryGormStore(t *testing.T) (*gorm.DB, *GormStateStore) {
	t.Helper()
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_", ":", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil && !errors.Is(err, gorm.ErrInvalidDB) {
			t.Fatalf("close db: %v", err)
		}
	})
	store := NewGormStateStore(db)
	if err := store.AutoMigrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, store
}

func recordPreparedRun(t *testing.T, store *GormStateStore, targetID uint, signature string, intent []PriorityProposal) uint {
	t.Helper()
	runID, err := store.RecordRun(RunRecord{
		TargetID:           targetID,
		Status:             "prepared",
		PreviewSignature:   signature,
		ChangeCount:        len(intent),
		NotificationStatus: "pending",
		Intent:             intent,
	})
	if err != nil {
		t.Fatalf("record prepared run: %v", err)
	}
	return runID
}
