package sub2pool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/crypto"
)

type Service struct {
	targets    TargetStore
	cipher     *crypto.Cipher
	admin      AdminGateway
	matcher    *matcher
	state      StateStore
	auto       AutomationStore
	leases     LeaseStore
	cfg        Config
	leaseOwner string

	dispatcher EventDispatcher
	locks      sync.Map
}

func New(
	targets TargetStore,
	cipher *crypto.Cipher,
	admin AdminGateway,
	channels ChannelStore,
	keys ChannelKeyReader,
	state StateStore,
	cfg Config,
) *Service {
	service := &Service{
		targets: targets,
		cipher:  cipher,
		admin:   admin,
		matcher: newMatcher(channels, keys),
		state:   state,
		cfg:     cfg.withDefaults(),
	}
	if auto, ok := state.(AutomationStore); ok {
		service.auto = auto
	}
	if leases, ok := state.(LeaseStore); ok {
		service.leases = leases
	}
	service.leaseOwner = fmt.Sprintf("upstream-ops-%d-%p", time.Now().UnixNano(), service)
	return service
}

func (s *Service) SetDispatcher(dispatcher EventDispatcher) {
	s.dispatcher = dispatcher
}

func (s *Service) Snapshot(ctx context.Context, targetID uint) (*Snapshot, error) {
	target, err := s.targetAccess(targetID)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.snapshot(ctx, targetID, target)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Service) Preview(ctx context.Context, targetID uint) (*PriorityPreview, error) {
	target, err := s.targetAccess(targetID)
	if err != nil {
		return nil, err
	}
	_, preview, err := s.snapshotPreview(ctx, targetID, target)
	if err != nil {
		return nil, err
	}
	return &preview, nil
}

// SnapshotPreview reads one account view for both display and preview
// generation, so a preview signature is not associated with another read.
func (s *Service) SnapshotPreview(ctx context.Context, targetID uint) (*Snapshot, *PriorityPreview, error) {
	target, err := s.targetAccess(targetID)
	if err != nil {
		return nil, nil, err
	}
	snapshot, preview, err := s.snapshotPreview(ctx, targetID, target)
	if err != nil {
		return nil, nil, err
	}
	return &snapshot, &preview, nil
}

func (s *Service) snapshotPreview(ctx context.Context, targetID uint, target sub2api.AdminTarget) (Snapshot, PriorityPreview, error) {
	snapshot, err := s.snapshot(ctx, targetID, target)
	if err != nil {
		return Snapshot{}, PriorityPreview{}, err
	}
	previous, err := s.loadState(targetID)
	if err != nil {
		return Snapshot{}, PriorityPreview{}, err
	}
	preview := buildPriorityPreview(snapshot)
	preview.Guards = validateGuards(snapshot, preview, previous, s.cfg)
	addPriorityTransitionGuard(snapshot, &preview)
	return snapshot, preview, nil
}

func (s *Service) Apply(ctx context.Context, targetID uint, input ApplyInput) (*ApplyResult, error) {
	lock := s.targetLock(targetID)
	lock.Lock()
	defer lock.Unlock()
	release, err := s.acquireTargetLease(targetID)
	if err != nil {
		return nil, err
	}
	defer release()

	target, err := s.targetAccess(targetID)
	if err != nil {
		return nil, err
	}
	if _, recovered, err := s.recoverPreparedRun(ctx, targetID, target); err != nil {
		return nil, err
	} else if recovered {
		return nil, ErrPreviewConflict
	}
	snapshot, err := s.snapshot(ctx, targetID, target)
	if err != nil {
		return nil, err
	}
	previous, err := s.loadState(targetID)
	if err != nil {
		return nil, err
	}
	preview := buildPriorityPreview(snapshot)
	preview.Guards = validateGuards(snapshot, preview, previous, s.cfg)
	addPriorityTransitionGuard(snapshot, &preview)
	if len(preview.Guards) > 0 {
		return nil, ErrGuardBlocked
	}
	if input.Signature == "" ||
		input.Signature != previewSignature(snapshot, input.Proposals) ||
		input.Signature != preview.Signature {
		return nil, ErrPreviewConflict
	}
	if s.auto == nil {
		return nil, ErrUnavailable
	}
	intent, err := preparePriorityTransitionIntent(snapshot.Accounts, preview.Changes)
	if err != nil {
		return nil, ErrGuardBlocked
	}
	prepared := RunRecord{
		TargetID:           targetID,
		Status:             "prepared",
		PreviewSignature:   preview.Signature,
		ChangeCount:        len(preview.Changes),
		NotificationStatus: "pending",
		Intent:             intent,
	}
	runID, err := s.auto.RecordRun(prepared)
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := s.renewTargetLease(targetID); err != nil {
		return nil, err
	}
	result, err := s.applyPreview(ctx, target, snapshot, preview, intent)
	if err != nil {
		return nil, err
	}
	event := composeEvent(snapshot, preview, previous, &result, s.cfg)
	result.RateChangeCount = len(event.RateChanges)
	nextState := stateForCycle(snapshot, preview, s.cfg)
	runStatus := "manual_applied"
	if len(result.Failed) > 0 {
		runStatus = "manual_partial"
	}
	runResult := &RunResult{
		Preview:            preview,
		Apply:              &result,
		RunID:              runID,
		NotificationStatus: "queued",
	}
	runResult.RateChangeCount = len(event.RateChanges)
	if err := s.finalizeRun(ctx, runResult, runStatus, nextState, event, intent); err != nil {
		return nil, err
	}
	return runResult.Apply, nil
}

// SetSchedulable uses the official Admin API endpoint and verifies the value
// both before and after the write. It does not accept a URL or an admin key.
func (s *Service) SetSchedulable(ctx context.Context, targetID uint, accountID int64, schedulable bool) (*SchedulableResult, error) {
	if accountID <= 0 {
		return nil, ErrInvalidInput
	}
	lock := s.targetLock(targetID)
	lock.Lock()
	defer lock.Unlock()
	release, err := s.acquireTargetLease(targetID)
	if err != nil {
		return nil, err
	}
	defer release()

	target, err := s.targetAccess(targetID)
	if err != nil {
		return nil, err
	}
	before, err := s.admin.GetPoolAccount(ctx, target, accountID)
	if err != nil {
		return nil, ErrUnavailable
	}
	if before.Account.ID != accountID ||
		!poolManagedAccount(before.Account) ||
		!strings.EqualFold(strings.TrimSpace(before.Account.Type), "apikey") {
		return nil, ErrInvalidInput
	}
	if err := s.renewTargetLease(targetID); err != nil {
		return nil, err
	}
	if _, err := s.admin.SetPoolAccountSchedulable(ctx, target, accountID, schedulable); err != nil {
		return nil, ErrUnavailable
	}
	if err := s.renewTargetLease(targetID); err != nil {
		return nil, err
	}
	after, err := s.admin.GetPoolAccount(ctx, target, accountID)
	if err != nil || after.Account.ID != accountID || after.Account.Schedulable != schedulable {
		return nil, ErrUnavailable
	}
	return &SchedulableResult{
		TargetID:    targetID,
		AccountID:   accountID,
		Schedulable: after.Account.Schedulable,
	}, nil
}

// Run is the scheduler-facing interface. Account writes, state persistence,
// and notification delivery are intentionally separate: delivery retries use
// the durable outbox and never replay priority updates.
func (s *Service) Run(ctx context.Context, targetID uint) (*RunResult, error) {
	lock := s.targetLock(targetID)
	lock.Lock()
	defer lock.Unlock()
	release, err := s.acquireTargetLease(targetID)
	if err != nil {
		return nil, err
	}
	defer release()

	target, err := s.targetAccess(targetID)
	if err != nil {
		return nil, err
	}
	if recovered, ok, err := s.recoverPreparedRun(ctx, targetID, target); ok || err != nil {
		return recovered, err
	}
	snapshot, err := s.snapshot(ctx, targetID, target)
	if err != nil {
		return nil, err
	}
	previous, err := s.loadState(targetID)
	if err != nil {
		return nil, err
	}
	preview := buildPriorityPreview(snapshot)
	preview.Guards = validateGuards(snapshot, preview, previous, s.cfg)
	addPriorityTransitionGuard(snapshot, &preview)
	result := &RunResult{Preview: preview, NotificationStatus: "skipped"}
	event := composeEvent(snapshot, preview, previous, nil, s.cfg)
	result.RateChangeCount = len(event.RateChanges)
	runStatus := "blocked"
	if s.auto == nil {
		return nil, ErrUnavailable
	}
	intent, intentErr := preparePriorityTransitionIntent(snapshot.Accounts, preview.Changes)
	if intentErr != nil {
		intent = nil
	}
	prepared := RunRecord{
		TargetID:           targetID,
		Status:             "prepared",
		PreviewSignature:   preview.Signature,
		ChangeCount:        len(preview.Changes),
		GuardCount:         len(preview.Guards),
		NotificationStatus: "pending",
		Intent:             intent,
	}
	for _, guard := range preview.Guards {
		prepared.GuardCodes = append(prepared.GuardCodes, guard.Code)
	}
	runID, err := s.auto.RecordRun(prepared)
	if err != nil {
		return nil, ErrUnavailable
	}
	result.RunID = runID
	if len(preview.Guards) == 0 {
		runStatus = "no_change"
		if len(preview.Changes) > 0 {
			apply, err := s.applyPreview(ctx, target, snapshot, preview, intent)
			if err != nil {
				return nil, err
			}
			apply.RateChangeCount = result.RateChangeCount
			result.Apply = &apply
			event.PriorityResult = &apply
			if len(apply.Failed) > 0 {
				runStatus = "partial"
			} else {
				runStatus = "applied"
			}
		}
	}
	nextState := stateForCycle(snapshot, preview, s.cfg)
	if len(preview.Guards) > 0 {
		nextState.LastHealthyCount = previous.LastHealthyCount
	}
	if err := s.finalizeRun(ctx, result, runStatus, nextState, event, intent); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) finalizeRun(
	ctx context.Context,
	result *RunResult,
	status string,
	state TargetState,
	event PoolEvent,
	intent []PriorityProposal,
) error {
	if s.auto == nil || result == nil || result.RunID == 0 {
		return ErrUnavailable
	}
	signal := hasEventSignal(event)
	result.NotificationStatus = "skipped"
	if signal {
		result.NotificationStatus = "queued"
		event.EventID = fmt.Sprintf("sub2-pool-%d-%d", result.Preview.TargetID, result.RunID)
	}
	result.StatePersisted = true
	if result.Apply != nil {
		result.Apply.StatePersisted = true
	}
	if err := s.renewTargetLease(result.Preview.TargetID); err != nil {
		result.StatePersisted = false
		if result.Apply != nil {
			result.Apply.StatePersisted = false
		}
		return err
	}
	record := runRecordFor(result, status)
	record.Intent = append([]PriorityProposal(nil), intent...)
	var eventPtr *PoolEvent
	if signal {
		eventCopy := event
		eventPtr = &eventCopy
	}
	outboxID, created, err := s.auto.FinalizeCycle(
		result.Preview.TargetID,
		result.RunID,
		record,
		state,
		eventPtr,
	)
	if err != nil {
		result.StatePersisted = false
		result.NotificationStatus = "persistence_failed"
		result.NotificationFailed = true
		if result.Apply != nil {
			result.Apply.StatePersisted = false
		}
		return ErrUnavailable
	}
	result.OutboxID = outboxID
	if !signal {
		return nil
	}
	if !created {
		result.NotificationStatus = "deduplicated"
		_ = s.auto.UpdateRunNotification(result.RunID, result.NotificationStatus)
		return nil
	}
	if s.dispatcher == nil {
		return nil
	}
	if err := s.dispatcher.DispatchPoolEvent(ctx, event); err != nil {
		result.NotificationStatus = "pending"
		result.NotificationFailed = true
		_ = s.auto.MarkOutboxDelivery(outboxID, false)
		_ = s.auto.UpdateRunNotification(result.RunID, result.NotificationStatus)
		return nil
	}
	if err := s.auto.MarkOutboxDelivery(outboxID, true); err != nil {
		result.NotificationStatus = "pending"
		result.NotificationFailed = true
		_ = s.auto.UpdateRunNotification(result.RunID, result.NotificationStatus)
		return nil
	}
	result.NotificationStatus = "sent"
	_ = s.auto.UpdateRunNotification(result.RunID, result.NotificationStatus)
	return nil
}

func (s *Service) recoverPreparedRun(
	ctx context.Context,
	targetID uint,
	target sub2api.AdminTarget,
) (*RunResult, bool, error) {
	if s.auto == nil {
		return nil, false, nil
	}
	runs, err := s.auto.ListPreparedRuns(targetID, 1)
	if err != nil {
		return nil, true, ErrUnavailable
	}
	if len(runs) == 0 {
		return nil, false, nil
	}
	prepared := runs[0]
	snapshot, err := s.snapshot(ctx, targetID, target)
	if err != nil {
		return nil, true, err
	}
	previous, err := s.loadState(targetID)
	if err != nil {
		return nil, true, err
	}
	preview := buildPriorityPreview(snapshot)
	preview.Guards = validateGuards(snapshot, preview, previous, s.cfg)
	addPriorityTransitionGuard(snapshot, &preview)
	if prepared.GuardCount > 0 {
		preview.Guards = mergePreparedGuards(preview.Guards, prepared.GuardCodes)
	}
	result := &RunResult{
		Preview:            preview,
		RunID:              prepared.ID,
		NotificationStatus: "queued",
	}
	var apply *ApplyResult
	blocked := prepared.GuardCount > 0
	if len(prepared.Intent) > 0 {
		phase, phaseErr := priorityIntentStateFor(snapshot.Accounts, prepared.Intent)
		if phaseErr != nil {
			return nil, true, ErrPreviewConflict
		}
		if len(preview.Guards) > 0 && phase == priorityIntentStateOld {
			blocked = true
		}
		reconciled, reconcileErr := s.recoverPreparedIntent(
			ctx,
			target,
			targetID,
			prepared.Intent,
			snapshot.Accounts,
			preview,
			blocked,
		)
		if reconcileErr != nil {
			return nil, true, reconcileErr
		}
		apply = &reconciled
		result.Apply = apply
	}
	event := composeEvent(snapshot, preview, previous, apply, s.cfg)
	result.RateChangeCount = len(event.RateChanges)
	if result.Apply != nil {
		result.Apply.RateChangeCount = result.RateChangeCount
	}
	nextState := stateForCycle(snapshot, preview, s.cfg)
	status := "recovered"
	if blocked {
		status = "recovered_blocked"
		nextState.LastHealthyCount = previous.LastHealthyCount
	}
	if apply != nil && len(apply.Failed) > 0 && status != "recovered_blocked" {
		status = "recovered_partial"
	}
	if err := s.finalizeRun(ctx, result, status, nextState, event, prepared.Intent); err != nil {
		return nil, true, err
	}
	return result, true, nil
}

func mergePreparedGuards(current []GuardViolation, preparedCodes []string) []GuardViolation {
	out := append([]GuardViolation(nil), current...)
	seen := make(map[string]struct{}, len(out))
	for _, guard := range out {
		seen[guard.Code] = struct{}{}
	}
	for _, code := range preparedCodes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		out = append(out, GuardViolation{
			Code:    code,
			Message: "the prepared cycle was blocked by this safety guard before restart",
		})
		seen[code] = struct{}{}
	}
	return out
}

func (s *Service) recoverPreparedIntent(
	ctx context.Context,
	target sub2api.AdminTarget,
	targetID uint,
	intent []PriorityProposal,
	accounts []AccountSnapshot,
	preview PriorityPreview,
	blocked bool,
) (ApplyResult, error) {
	phase, err := priorityIntentStateFor(accounts, intent)
	if err != nil {
		return ApplyResult{TargetID: targetID, Preview: preview}, ErrPreviewConflict
	}
	if !blocked {
		return s.executePriorityIntent(ctx, target, targetID, preview, intent, accounts, true)
	}

	result := ApplyResult{TargetID: targetID, Preview: preview}
	current := make(map[int64]AccountSnapshot, len(accounts))
	for _, account := range accounts {
		current[account.ID] = account
	}
	if phase != priorityIntentStateOld {
		return result, ErrPreviewConflict
	}
	for _, proposal := range sortedPriorityIntent(intent) {
		item := ApplyItem{
			AccountID:      proposal.AccountID,
			AccountName:    proposal.AccountName,
			Channel:        proposal.Channel,
			BeforePriority: proposal.CurrentPriority,
			TargetPriority: proposal.TargetPriority,
		}
		account, exists := current[proposal.AccountID]
		if !exists {
			item.Status = "recovery_account_missing"
			result.Failed = append(result.Failed, item)
			continue
		}
		actual := account.CurrentPriority
		item.AfterPriority = &actual
		if !preparedProposalMatchesAccount(proposal, account) {
			return result, ErrPreviewConflict
		}
		if actual != proposal.CurrentPriority {
			return result, ErrPreviewConflict
		}
		item.Status = "recovery_guard_blocked"
		result.Failed = append(result.Failed, item)
	}
	result.Remaining = len(result.Failed)
	return result, nil
}

func preparedProposalMatchesAccount(proposal PriorityProposal, account AccountSnapshot) bool {
	return proposal.expectedIdentityDigest != "" &&
		proposal.expectedIdentityDigest == account.IdentityDigest &&
		len(proposal.expectedGroupIDs) > 0 &&
		equalInt64Sets(proposal.expectedGroupIDs, account.GroupIDs) &&
		strings.EqualFold(strings.TrimSpace(proposal.expectedStatus), strings.TrimSpace(account.Status)) &&
		proposal.expectedPoolManaged == account.PoolManaged &&
		account.Schedulable
}

// DispatchPending delivers already-persisted notifications only. It never
// reads or writes Sub2 accounts, so an outbox retry cannot replay a pool apply.
func (s *Service) DispatchPending(ctx context.Context, targetID uint, limit int) (*OutboxDispatchResult, error) {
	if s.auto == nil || s.dispatcher == nil {
		return nil, ErrUnavailable
	}
	lock := s.targetLock(targetID)
	lock.Lock()
	defer lock.Unlock()
	release, err := s.acquireTargetLease(targetID)
	if err != nil {
		return nil, err
	}
	defer release()
	items, err := s.auto.ListPendingOutbox(targetID, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	result := &OutboxDispatchResult{}
	for _, item := range items {
		result.Attempted++
		if err := s.dispatcher.DispatchPoolEvent(ctx, item.Event); err != nil {
			result.Failed++
			_ = s.auto.MarkOutboxDelivery(item.ID, false)
			continue
		}
		if err := s.auto.MarkOutboxDelivery(item.ID, true); err != nil {
			result.Failed++
			continue
		}
		result.Delivered++
	}
	return result, nil
}

func (s *Service) ListRuns(targetID uint, limit int) ([]RunRecord, error) {
	if s.auto == nil {
		return nil, ErrUnavailable
	}
	runs, err := s.auto.ListRuns(targetID, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	return runs, nil
}

func (s *Service) RecordAutomationFailure(targetID uint, runErr error) {
	if s.auto == nil || targetID == 0 {
		return
	}
	code := ErrUnavailable.Code
	var public *PublicError
	if errors.As(runErr, &public) && public.Code != "" {
		code = public.Code
	}
	code = strings.NewReplacer(":", "_", "/", "_", " ", "_").Replace(code)
	if len(code) > 24 {
		code = code[:24]
	}
	_, _ = s.auto.RecordRun(RunRecord{
		TargetID:           targetID,
		Status:             "error_" + code,
		NotificationStatus: "skipped",
	})
}

// GetAutomation returns durable scheduler-facing state only. It never returns
// target connection details and treats a missing row as a disabled default.
func (s *Service) GetAutomation(targetID uint) (*AutomationStatus, error) {
	if err := s.targetExists(targetID); err != nil {
		return nil, err
	}
	if s.auto == nil {
		return nil, ErrUnavailable
	}
	state, err := s.auto.LoadAutomation(targetID)
	if err != nil {
		return nil, ErrUnavailable
	}
	runs, err := s.auto.ListRuns(targetID, 1)
	if err != nil {
		return nil, ErrUnavailable
	}
	status := &AutomationStatus{AutomationState: state}
	if len(runs) > 0 {
		last := runs[0]
		status.LastRun = &last
	}
	return status, nil
}

// SetAutomation persists a scheduler switch. Scheduler registration remains
// an integration concern and must check this value before calling Run.
func (s *Service) SetAutomation(targetID uint, enabled bool) (*AutomationStatus, error) {
	if err := s.targetExists(targetID); err != nil {
		return nil, err
	}
	if s.auto == nil {
		return nil, ErrUnavailable
	}
	state := AutomationState{
		TargetID:  targetID,
		Enabled:   enabled,
		UpdatedAt: time.Now(),
	}
	if err := s.auto.SaveAutomation(state); err != nil {
		return nil, ErrUnavailable
	}
	return s.GetAutomation(targetID)
}

func (s *Service) applyPreview(
	ctx context.Context,
	target sub2api.AdminTarget,
	snapshot Snapshot,
	preview PriorityPreview,
	intent []PriorityProposal,
) (ApplyResult, error) {
	return s.executePriorityIntent(ctx, target, preview.TargetID, preview, intent, snapshot.Accounts, false)
}

// executePriorityIntent uses a persisted old -> staging -> final plan. Every
// staging value is outside the old and final value ranges, so either phase can
// be interrupted and resumed without introducing a duplicate priority.
func (s *Service) executePriorityIntent(
	ctx context.Context,
	target sub2api.AdminTarget,
	targetID uint,
	preview PriorityPreview,
	intent []PriorityProposal,
	accounts []AccountSnapshot,
	recovered bool,
) (ApplyResult, error) {
	result := ApplyResult{
		TargetID: targetID,
		Preview:  preview,
	}
	phase, err := priorityIntentStateFor(accounts, intent)
	if err != nil {
		return result, ErrPreviewConflict
	}
	ordered := sortedPriorityIntent(intent)

	if phase == priorityIntentStateOld {
		for _, proposal := range ordered {
			actual, err := s.currentIntentPriority(ctx, target, targetID, proposal)
			if err != nil {
				return result, err
			}
			switch actual {
			case proposal.CurrentPriority:
				if err := s.writeIntentPriority(ctx, target, targetID, proposal, actual, proposal.stagingPriority); err != nil {
					return result, err
				}
			case proposal.stagingPriority:
				continue
			default:
				return result, ErrPreviewConflict
			}
		}
		phase = priorityIntentStateStaging
	}
	if phase == priorityIntentStateStaging {
		for _, proposal := range ordered {
			actual, err := s.currentIntentPriority(ctx, target, targetID, proposal)
			if err != nil {
				return result, err
			}
			switch actual {
			case proposal.stagingPriority:
				if err := s.writeIntentPriority(ctx, target, targetID, proposal, actual, proposal.TargetPriority); err != nil {
					return result, err
				}
			case proposal.TargetPriority:
				continue
			default:
				return result, ErrPreviewConflict
			}
		}
	}

	status := "applied"
	if recovered {
		status = "recovered_applied"
	}
	for _, proposal := range ordered {
		afterPriority := proposal.TargetPriority
		result.Applied = append(result.Applied, ApplyItem{
			AccountID:      proposal.AccountID,
			AccountName:    proposal.AccountName,
			Channel:        proposal.Channel,
			BeforePriority: proposal.CurrentPriority,
			TargetPriority: proposal.TargetPriority,
			AfterPriority:  &afterPriority,
			Status:         status,
		})
	}
	return result, nil
}

func (s *Service) currentIntentPriority(
	ctx context.Context,
	target sub2api.AdminTarget,
	targetID uint,
	proposal PriorityProposal,
) (int, error) {
	if err := s.renewTargetLease(targetID); err != nil {
		return 0, err
	}
	account, err := s.admin.GetPoolAccount(ctx, target, proposal.AccountID)
	if err != nil {
		return 0, ErrUnavailable
	}
	if !poolAccountMatchesIntent(account, proposal) {
		return 0, ErrPreviewConflict
	}
	return account.Account.Priority, nil
}

func (s *Service) writeIntentPriority(
	ctx context.Context,
	target sub2api.AdminTarget,
	targetID uint,
	proposal PriorityProposal,
	expectedPriority, nextPriority int,
) error {
	if expectedPriority == nextPriority {
		return ErrPreviewConflict
	}
	account, err := s.admin.GetPoolAccount(ctx, target, proposal.AccountID)
	if err != nil {
		return ErrUnavailable
	}
	if !poolAccountMatchesIntent(account, proposal) || account.Account.Priority != expectedPriority {
		return ErrPreviewConflict
	}
	if _, err := s.admin.UpdatePoolAccountPriority(ctx, target, proposal.AccountID, nextPriority); err != nil {
		return ErrUnavailable
	}
	if err := s.renewTargetLease(targetID); err != nil {
		return err
	}
	after, err := s.admin.GetPoolAccount(ctx, target, proposal.AccountID)
	if err != nil {
		return ErrUnavailable
	}
	if !poolAccountMatchesIntent(after, proposal) || after.Account.Priority != nextPriority {
		return ErrPreviewConflict
	}
	return nil
}

func poolAccountMatchesProposal(account *sub2api.PoolAccount, proposal PriorityProposal) bool {
	if account == nil ||
		account.Account.ID != proposal.AccountID ||
		!account.Account.Schedulable ||
		account.Account.Priority != proposal.CurrentPriority ||
		!strings.EqualFold(strings.TrimSpace(account.Account.Status), strings.TrimSpace(proposal.expectedStatus)) ||
		poolManagedAccount(account.Account) != proposal.expectedPoolManaged ||
		!equalInt64Sets(account.Account.GroupIDs, proposal.expectedGroupIDs) {
		return false
	}
	return poolAccountMatchesIntent(account, proposal)
}

func poolAccountMatchesIntent(account *sub2api.PoolAccount, proposal PriorityProposal) bool {
	if account == nil ||
		account.Account.ID != proposal.AccountID ||
		!account.Account.Schedulable ||
		!strings.EqualFold(strings.TrimSpace(account.Account.Status), strings.TrimSpace(proposal.expectedStatus)) ||
		poolManagedAccount(account.Account) != proposal.expectedPoolManaged ||
		!equalInt64Sets(account.Account.GroupIDs, proposal.expectedGroupIDs) {
		return false
	}
	identity := account.Identity()
	return identityDigest(normalizeURL(identity.BaseURL), identity.APIKeySHA256) == proposal.expectedIdentityDigest
}

type priorityIntentState uint8

const (
	priorityIntentStateOld priorityIntentState = iota + 1
	priorityIntentStateStaging
	priorityIntentStateFinal
)

const priorityStagingStep = 10

func addPriorityTransitionGuard(snapshot Snapshot, preview *PriorityPreview) {
	if preview == nil || len(preview.Changes) == 0 {
		return
	}
	if _, err := preparePriorityTransitionIntent(snapshot.Accounts, preview.Changes); err != nil {
		preview.Guards = append(preview.Guards, GuardViolation{
			Code:    "priority_transition_invalid",
			Message: "current priorities cannot be safely transitioned without a duplicate",
		})
	}
}

func preparePriorityTransitionIntent(
	accounts []AccountSnapshot,
	changes []PriorityProposal,
) ([]PriorityProposal, error) {
	if len(changes) == 0 {
		return nil, nil
	}
	if !uniqueCurrentPriorities(accounts) {
		return nil, errors.New("remote priority scope is already ambiguous")
	}
	accountByID := make(map[int64]AccountSnapshot, len(accounts))
	ceiling := 0
	for _, account := range accounts {
		accountByID[account.ID] = account
		ceiling = max(ceiling, account.CurrentPriority)
	}
	intent := append([]PriorityProposal(nil), changes...)
	sort.Slice(intent, func(i, j int) bool {
		return intent[i].AccountID < intent[j].AccountID
	})
	seen := make(map[int64]struct{}, len(intent))
	for _, proposal := range intent {
		if proposal.AccountID <= 0 ||
			proposal.CurrentPriority < 0 ||
			proposal.TargetPriority <= 0 ||
			proposal.TargetPriority%priorityStagingStep != 0 ||
			proposal.CurrentPriority == proposal.TargetPriority {
			return nil, errors.New("invalid priority transition")
		}
		if _, exists := seen[proposal.AccountID]; exists {
			return nil, errors.New("duplicate transition account")
		}
		seen[proposal.AccountID] = struct{}{}
		account, exists := accountByID[proposal.AccountID]
		if !exists ||
			account.CurrentPriority != proposal.CurrentPriority ||
			!preparedProposalMatchesAccount(proposal, account) {
			return nil, errors.New("transition account does not match snapshot")
		}
		ceiling = max(ceiling, proposal.TargetPriority)
	}
	maxInt := int(^uint(0) >> 1)
	if len(intent) > maxInt/priorityStagingStep {
		return nil, errors.New("too many priority transitions")
	}
	required := priorityStagingStep * len(intent)
	if remainder := ceiling % priorityStagingStep; remainder != 0 {
		if ceiling > maxInt-(priorityStagingStep-remainder) {
			return nil, errors.New("priority staging overflow")
		}
		ceiling += priorityStagingStep - remainder
	}
	if ceiling > maxInt-required {
		return nil, errors.New("priority staging overflow")
	}
	for index := range intent {
		intent[index].stagingPriority = ceiling + priorityStagingStep*(index+1)
	}
	return intent, nil
}

func priorityIntentStateFor(accounts []AccountSnapshot, intent []PriorityProposal) (priorityIntentState, error) {
	if len(intent) == 0 {
		return priorityIntentStateFinal, nil
	}
	if err := validatePriorityTransitionIntent(accounts, intent); err != nil {
		return 0, err
	}
	accountsByID := make(map[int64]AccountSnapshot, len(accounts))
	for _, account := range accounts {
		accountsByID[account.ID] = account
	}
	hasOld := false
	hasStaging := false
	hasFinal := false
	for _, proposal := range intent {
		account := accountsByID[proposal.AccountID]
		if !preparedProposalMatchesAccount(proposal, account) {
			return 0, errors.New("transition account no longer matches")
		}
		switch account.CurrentPriority {
		case proposal.CurrentPriority:
			hasOld = true
		case proposal.stagingPriority:
			hasStaging = true
		case proposal.TargetPriority:
			hasFinal = true
		default:
			return 0, errors.New("unexpected transition priority")
		}
	}
	if hasOld && hasFinal {
		return 0, errors.New("unreachable transition state")
	}
	if hasOld {
		return priorityIntentStateOld, nil
	}
	if hasStaging {
		return priorityIntentStateStaging, nil
	}
	return priorityIntentStateFinal, nil
}

func validatePriorityTransitionIntent(accounts []AccountSnapshot, intent []PriorityProposal) error {
	if !uniqueCurrentPriorities(accounts) {
		return errors.New("remote priority scope is already ambiguous")
	}
	accountsByID := make(map[int64]AccountSnapshot, len(accounts))
	intentByAccountID := make(map[int64]PriorityProposal, len(intent))
	ceiling := 0
	for _, account := range accounts {
		accountsByID[account.ID] = account
	}
	for _, proposal := range intent {
		if proposal.AccountID <= 0 ||
			proposal.CurrentPriority < 0 ||
			proposal.stagingPriority <= 0 ||
			proposal.stagingPriority%priorityStagingStep != 0 ||
			proposal.TargetPriority <= 0 ||
			proposal.TargetPriority%priorityStagingStep != 0 ||
			proposal.CurrentPriority == proposal.TargetPriority {
			return errors.New("invalid persisted transition")
		}
		if _, exists := intentByAccountID[proposal.AccountID]; exists {
			return errors.New("duplicate persisted transition account")
		}
		if _, exists := accountsByID[proposal.AccountID]; !exists {
			return errors.New("persisted transition account missing")
		}
		intentByAccountID[proposal.AccountID] = proposal
		ceiling = max(ceiling, proposal.CurrentPriority, proposal.TargetPriority)
	}
	for _, account := range accounts {
		if _, planned := intentByAccountID[account.ID]; !planned {
			ceiling = max(ceiling, account.CurrentPriority)
		}
	}
	seenStaging := make(map[int]struct{}, len(intent))
	for _, proposal := range intent {
		if proposal.stagingPriority <= ceiling {
			return errors.New("persisted staging priority is not isolated")
		}
		if _, exists := seenStaging[proposal.stagingPriority]; exists {
			return errors.New("duplicate persisted staging priority")
		}
		seenStaging[proposal.stagingPriority] = struct{}{}
	}
	return nil
}

func sortedPriorityIntent(intent []PriorityProposal) []PriorityProposal {
	out := append([]PriorityProposal(nil), intent...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].AccountID < out[j].AccountID
	})
	return out
}

func uniqueCurrentPriorities(accounts []AccountSnapshot) bool {
	seenChannels := make(map[string]struct{}, len(accounts))
	seenGroups := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		if account.CurrentPriority <= 0 {
			continue
		}
		channelKey := fmt.Sprintf("%s:%d", account.Channel, account.CurrentPriority)
		if _, exists := seenChannels[channelKey]; exists {
			return false
		}
		seenChannels[channelKey] = struct{}{}
		for _, groupID := range account.GroupIDs {
			if groupID <= 0 {
				continue
			}
			groupKey := fmt.Sprintf("%d:%d", groupID, account.CurrentPriority)
			if _, exists := seenGroups[groupKey]; exists {
				return false
			}
			seenGroups[groupKey] = struct{}{}
		}
	}
	return true
}

func equalInt64Sets(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]int64(nil), left...)
	rightCopy := append([]int64(nil), right...)
	sort.Slice(leftCopy, func(i, j int) bool { return leftCopy[i] < leftCopy[j] })
	sort.Slice(rightCopy, func(i, j int) bool { return rightCopy[i] < rightCopy[j] })
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func (s *Service) snapshot(ctx context.Context, targetID uint, target sub2api.AdminTarget) (Snapshot, error) {
	if s.admin == nil {
		return Snapshot{}, ErrUnavailable
	}
	groups, err := s.admin.ListGroups(ctx, target, true)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	accounts, err := s.admin.ListAllPoolAccounts(ctx, target)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	todayStats := map[int64]sub2api.PoolTodayStats{}
	if statsGateway, ok := s.admin.(todayStatsGateway); ok {
		accountIDs := make([]int64, 0, len(accounts))
		for _, account := range accounts {
			if account.Account.ID > 0 {
				accountIDs = append(accountIDs, account.Account.ID)
			}
		}
		if len(accountIDs) > 0 {
			if stats, statsErr := statsGateway.GetPoolTodayStatsBatch(ctx, target, accountIDs); statsErr == nil {
				todayStats = stats
			}
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Account.ID < accounts[j].Account.ID })

	groupByID := make(map[int64]GroupSnapshot, len(groups))
	snapshot := Snapshot{
		TargetID:    targetID,
		GeneratedAt: time.Now(),
		Groups:      make([]GroupSnapshot, 0, len(groups)),
		Accounts:    make([]AccountSnapshot, 0, len(accounts)),
	}
	for _, group := range groups {
		ratio := group.Ratio
		if ratio == 0 {
			ratio = group.RateMultiplier
		}
		item := GroupSnapshot{
			ID:     group.ID,
			Name:   group.Name,
			Ratio:  ratio,
			Status: group.Status,
		}
		groupByID[item.ID] = item
		snapshot.Groups = append(snapshot.Groups, item)
	}

	matches, matchErr := s.matcher.matchAccounts(ctx, accounts)
	if matchErr != nil {
		matches = make(map[int64]upstreamMatch, len(accounts))
		for _, account := range accounts {
			identity := account.Identity()
			state := "missing"
			if identity.FingerprintSeen {
				state = "present"
			}
			matches[account.Account.ID] = upstreamMatch{status: "upstream_unavailable", fingerprint: state}
		}
	}
	for _, raw := range accounts {
		account := raw.Account
		if stats, exists := todayStats[account.ID]; exists {
			requests := int(stats.Requests)
			cost := stats.Cost
			raw.Stats.TodayRequests = &requests
			raw.Stats.TodayCost = &cost
		}
		lowest := lowestGroups(account.GroupIDs, groupByID)
		channel := classifyLowestGroups(lowest)
		match := matches[account.ID]
		fingerprintState := match.fingerprint
		if fingerprintState == "" {
			fingerprintState = "missing"
		}
		identity := raw.Identity()
		identityStateDigest := identityDigest(normalizeURL(identity.BaseURL), identity.APIKeySHA256)
		if match.identityDigest != "" {
			identityStateDigest = match.identityDigest
		}
		item := AccountSnapshot{
			ID:                 account.ID,
			Name:               account.Name,
			Platform:           account.Platform,
			Type:               account.Type,
			Status:             account.Status,
			Schedulable:        account.Schedulable,
			PoolManaged:        poolManagedAccount(account),
			CurrentPriority:    account.Priority,
			CurrentConcurrency: raw.Health.CurrentConcurrency,
			MaxConcurrency:     account.Concurrency,
			GroupIDs:           append([]int64(nil), account.GroupIDs...),
			LowestGroups:       lowest,
			Channel:            channel,
			UpstreamRate:       cloneFloat(match.rate),
			Balance:            cloneFloat(match.balance),
			TodayStats: TodayStats{
				Requests:  cloneInt(raw.Stats.TodayRequests),
				Cost:      cloneFloat(raw.Stats.TodayCost),
				Available: raw.Stats.TodayRequests != nil || raw.Stats.TodayCost != nil,
			},
			Availability: Availability{
				Matched:          match.matched,
				BalanceAvailable: match.balance != nil,
				TodayStatsReady:  raw.Stats.TodayRequests != nil || raw.Stats.TodayCost != nil,
				RateAvailable:    match.rate != nil,
			},
			Health: AccountHealth{
				RateLimited:              raw.Health.RateLimited,
				TemporarilyUnschedulable: raw.Health.TemporarilyUnschedulable,
				Overloaded:               raw.Health.Overloaded,
			},
			MatchStatus:      match.status,
			FingerprintState: fingerprintState,
			IdentityDigest:   identityStateDigest,
		}
		item.Availability.Healthy = item.Schedulable &&
			item.Availability.Matched &&
			item.Availability.BalanceAvailable &&
			item.Availability.RateAvailable &&
			!item.Health.RateLimited &&
			!item.Health.TemporarilyUnschedulable &&
			!item.Health.Overloaded &&
			!strings.EqualFold(item.Status, "disabled") &&
			!strings.EqualFold(item.Status, "inactive")
		item.SkipReason = skipReason(item)
		snapshot.Summary.AccountCount++
		if item.Schedulable {
			snapshot.Summary.SchedulableCount++
		}
		if item.Availability.Matched {
			snapshot.Summary.MatchedCount++
		}
		if item.Availability.BalanceAvailable {
			snapshot.Summary.BalanceReadyCount++
		}
		if item.Availability.TodayStatsReady {
			snapshot.Summary.TodayStatsReadyCount++
		}
		if item.Availability.RateAvailable {
			snapshot.Summary.RateReadyCount++
		}
		if item.Availability.Healthy {
			snapshot.Summary.HealthyCount++
		}
		snapshot.Accounts = append(snapshot.Accounts, item)
	}
	snapshot.Summary.HealthCoverage = HealthCoverage{
		Matched:         snapshot.Summary.MatchedCount,
		BalanceReady:    snapshot.Summary.BalanceReadyCount,
		TodayStatsReady: snapshot.Summary.TodayStatsReadyCount,
		RateReady:       snapshot.Summary.RateReadyCount,
		Healthy:         snapshot.Summary.HealthyCount,
	}
	return snapshot, nil
}

func lowestGroups(ids []int64, groups map[int64]GroupSnapshot) []GroupRef {
	var ratio *float64
	out := make([]GroupRef, 0, len(ids))
	for _, id := range ids {
		group, exists := groups[id]
		if !exists {
			continue
		}
		if ratio == nil || group.Ratio < *ratio {
			value := group.Ratio
			ratio = &value
			out = out[:0]
		}
		if ratio != nil && group.Ratio == *ratio {
			out = append(out, GroupRef{ID: group.ID, Name: group.Name, Ratio: group.Ratio})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func skipReason(account AccountSnapshot) string {
	if !account.Schedulable {
		return "not_schedulable"
	}
	if !account.PoolManaged {
		return "not_pool_managed"
	}
	if !account.Availability.Matched {
		return account.MatchStatus
	}
	if !account.Availability.BalanceAvailable {
		return "balance_missing"
	}
	if account.Balance != nil && *account.Balance > 0 && !account.Availability.RateAvailable {
		return "multiplier_missing"
	}
	return ""
}

func poolManagedAccount(account sub2api.AdminAccount) bool {
	if !strings.EqualFold(strings.TrimSpace(account.Type), "apikey") {
		return false
	}
	value, exists := account.Credentials["pool_mode"]
	if !exists {
		return false
	}
	enabled, ok := value.(bool)
	return ok && enabled
}

func (s *Service) targetAccess(targetID uint) (sub2api.AdminTarget, error) {
	if targetID == 0 || s.targets == nil || s.cipher == nil {
		return sub2api.AdminTarget{}, ErrInvalidInput
	}
	target, err := s.targets.FindByID(targetID)
	if err != nil {
		return sub2api.AdminTarget{}, ErrNotFound
	}
	key, err := s.cipher.Decrypt(target.AdminAPIKeyCipher)
	if err != nil || key == "" || target.BaseURL == "" {
		return sub2api.AdminTarget{}, ErrUnavailable
	}
	return sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: key}, nil
}

func (s *Service) targetExists(targetID uint) error {
	if targetID == 0 || s.targets == nil {
		return ErrInvalidInput
	}
	if _, err := s.targets.FindByID(targetID); err != nil {
		return ErrNotFound
	}
	return nil
}

func (s *Service) loadState(targetID uint) (TargetState, error) {
	if s.state == nil {
		return emptyTargetState(), nil
	}
	state, err := s.state.Load(targetID)
	if err != nil {
		return TargetState{}, ErrUnavailable
	}
	normalizeTargetState(&state)
	return state, nil
}

func (s *Service) saveState(targetID uint, state TargetState) bool {
	if s.state == nil {
		return false
	}
	if err := s.state.Save(targetID, state); err != nil {
		return false
	}
	return true
}

func stateForCycle(snapshot Snapshot, preview PriorityPreview, cfg Config) TargetState {
	state := emptyTargetState()
	state.LastHealthyCount = snapshot.Summary.HealthyCount
	state.MissingMultiplierIDs = append([]int64(nil), preview.MissingMultiplierIDs...)
	state.MissingBalanceIDs = append([]int64(nil), preview.MissingBalanceIDs...)
	for _, guard := range preview.Guards {
		state.GuardCodes = append(state.GuardCodes, guard.Code)
	}
	state.MultiplierByAccount = make(map[int64]MultiplierState, len(snapshot.Accounts))
	for _, account := range snapshot.Accounts {
		if account.Balance != nil && *account.Balance <= cfg.LowBalanceThreshold {
			state.LowBalanceByAccount[account.ID] = *account.Balance
		}
		state.MultiplierByAccount[account.ID] = stateForAccount(account)
	}
	normalizeTargetState(&state)
	return state
}

func composeEvent(snapshot Snapshot, preview PriorityPreview, previous TargetState, result *ApplyResult, cfg Config) PoolEvent {
	event := PoolEvent{
		TargetID:       snapshot.TargetID,
		GeneratedAt:    time.Now(),
		PriorityResult: result,
	}
	previousMissingMultiplier := int64Set(previous.MissingMultiplierIDs)
	for _, accountID := range preview.MissingMultiplierIDs {
		if _, exists := previousMissingMultiplier[accountID]; !exists {
			event.MissingMultiplierIDs = append(event.MissingMultiplierIDs, accountID)
		}
	}
	previousMissingBalance := int64Set(previous.MissingBalanceIDs)
	for _, accountID := range preview.MissingBalanceIDs {
		if _, exists := previousMissingBalance[accountID]; !exists {
			event.MissingBalanceIDs = append(event.MissingBalanceIDs, accountID)
		}
	}
	previousGuards := stringSet(previous.GuardCodes)
	for _, guard := range preview.Guards {
		if _, exists := previousGuards[guard.Code]; !exists {
			event.Guards = append(event.Guards, guard)
		}
	}
	for _, account := range snapshot.Accounts {
		if account.Balance != nil && *account.Balance <= cfg.LowBalanceThreshold {
			if _, exists := previous.LowBalanceByAccount[account.ID]; !exists {
				event.LowBalances = append(event.LowBalances, LowBalance{
					AccountID:   account.ID,
					AccountName: account.Name,
					Balance:     *account.Balance,
				})
			}
		}
		current := stateForAccount(account)
		previousMultiplier, exists := previous.MultiplierByAccount[account.ID]
		if !exists || equalFloatPointers(previousMultiplier.UpstreamRate, current.UpstreamRate) &&
			equalFloatPointers(previousMultiplier.GroupRate, current.GroupRate) {
			continue
		}
		event.RateChanges = append(event.RateChanges, RateChange{
			AccountID:         account.ID,
			AccountName:       account.Name,
			PreviousRate:      cloneFloat(previousMultiplier.UpstreamRate),
			CurrentRate:       cloneFloat(current.UpstreamRate),
			PreviousGroupRate: cloneFloat(previousMultiplier.GroupRate),
			CurrentGroupRate:  cloneFloat(current.GroupRate),
		})
	}
	return event
}

func int64Set(items []int64) map[int64]struct{} {
	out := make(map[int64]struct{}, len(items))
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}

func stringSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}

func hasEventSignal(event PoolEvent) bool {
	if len(event.RateChanges) > 0 ||
		len(event.MissingMultiplierIDs) > 0 ||
		len(event.MissingBalanceIDs) > 0 ||
		len(event.LowBalances) > 0 ||
		len(event.Guards) > 0 {
		return true
	}
	return event.PriorityResult != nil &&
		(len(event.PriorityResult.Applied) > 0 || len(event.PriorityResult.Failed) > 0)
}

func runRecordFor(result *RunResult, status string) RunRecord {
	record := RunRecord{
		TargetID:           result.Preview.TargetID,
		Status:             status,
		PreviewSignature:   result.Preview.Signature,
		ChangeCount:        len(result.Preview.Changes),
		RateChangeCount:    result.RateChangeCount,
		GuardCount:         len(result.Preview.Guards),
		StatePersisted:     result.StatePersisted,
		NotificationStatus: result.NotificationStatus,
	}
	if result.Apply != nil {
		record.AppliedCount = len(result.Apply.Applied)
		record.FailedCount = len(result.Apply.Failed)
	}
	for _, guard := range result.Preview.Guards {
		record.GuardCodes = append(record.GuardCodes, guard.Code)
	}
	return record
}

func stateForAccount(account AccountSnapshot) MultiplierState {
	var groupRate *float64
	if len(account.LowestGroups) > 0 {
		value := account.LowestGroups[0].Ratio
		groupRate = &value
	}
	return MultiplierState{
		UpstreamRate: cloneFloat(account.UpstreamRate),
		GroupRate:    groupRate,
	}
}

func equalFloatPointers(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Service) targetLock(targetID uint) *sync.Mutex {
	value, _ := s.locks.LoadOrStore(targetID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Service) acquireTargetLease(targetID uint) (func(), error) {
	if s.leases == nil {
		return func() {}, nil
	}
	acquired, err := s.leases.AcquireLease(targetID, s.leaseOwner, time.Now().Add(10*time.Minute))
	if err != nil {
		return nil, ErrUnavailable
	}
	if !acquired {
		return nil, ErrBusy
	}
	return func() {
		_ = s.leases.ReleaseLease(targetID, s.leaseOwner)
	}, nil
}

func (s *Service) renewTargetLease(targetID uint) error {
	if s.leases == nil {
		return nil
	}
	renewed, err := s.leases.RenewLease(targetID, s.leaseOwner, time.Now().Add(10*time.Minute))
	if err != nil {
		return ErrUnavailable
	}
	if !renewed {
		return ErrBusy
	}
	return nil
}
