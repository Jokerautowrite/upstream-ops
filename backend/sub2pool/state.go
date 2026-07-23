package sub2pool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TargetState struct {
	LastHealthyCount     int
	MultiplierByAccount  map[int64]MultiplierState
	LowBalanceByAccount  map[int64]float64
	MissingMultiplierIDs []int64
	MissingBalanceIDs    []int64
	GuardCodes           []string
}

type MultiplierState struct {
	UpstreamRate *float64 `json:"upstream_rate,omitempty"`
	GroupRate    *float64 `json:"group_rate,omitempty"`
}

type StateStore interface {
	Load(targetID uint) (TargetState, error)
	Save(targetID uint, state TargetState) error
}

type GormTargetState struct {
	TargetID            uint      `gorm:"primaryKey" json:"target_id"`
	LastHealthyCount    int       `gorm:"not null;default:0" json:"last_healthy_count"`
	MultiplierStateJSON string    `gorm:"type:text;not null;default:'{}'" json:"-"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (GormTargetState) TableName() string { return "sub2_pool_target_states" }

type GormPoolOutbox struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	TargetID           uint      `gorm:"not null;index;uniqueIndex:idx_sub2_pool_outbox_run" json:"target_id"`
	RunID              uint      `gorm:"not null;uniqueIndex:idx_sub2_pool_outbox_run" json:"run_id"`
	EventKey           string    `gorm:"size:64;not null;index" json:"event_key"`
	EventJSON          string    `gorm:"type:text;not null" json:"-"`
	Status             string    `gorm:"size:32;not null;index" json:"status"`
	Attempts           int       `gorm:"not null;default:0" json:"attempts"`
	LastDeliveryStatus string    `gorm:"size:32;not null;default:''" json:"last_delivery_status"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (GormPoolOutbox) TableName() string { return "sub2_pool_outbox" }

type GormPoolRun struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	TargetID           uint      `gorm:"not null;index" json:"target_id"`
	Status             string    `gorm:"size:32;not null;index" json:"status"`
	PreviewSignature   string    `gorm:"size:128;not null" json:"preview_signature"`
	ChangeCount        int       `gorm:"not null;default:0" json:"change_count"`
	RateChangeCount    int       `gorm:"not null;default:0" json:"rate_change_count"`
	AppliedCount       int       `gorm:"not null;default:0" json:"applied_count"`
	FailedCount        int       `gorm:"not null;default:0" json:"failed_count"`
	GuardCount         int       `gorm:"not null;default:0" json:"guard_count"`
	GuardCodesJSON     string    `gorm:"type:text;not null;default:'[]'" json:"-"`
	IntentJSON         string    `gorm:"type:text;not null;default:'[]'" json:"-"`
	StatePersisted     bool      `gorm:"not null;default:false" json:"state_persisted"`
	NotificationStatus string    `gorm:"size:32;not null;default:''" json:"notification_status"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (GormPoolRun) TableName() string { return "sub2_pool_runs" }

type GormPoolAutomation struct {
	TargetID  uint      `gorm:"primaryKey" json:"target_id"`
	Enabled   bool      `gorm:"not null;default:false" json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (GormPoolAutomation) TableName() string { return "sub2_pool_automation" }

type GormPoolLease struct {
	TargetID  uint      `gorm:"primaryKey" json:"target_id"`
	Owner     string    `gorm:"size:64;not null" json:"-"`
	ExpiresAt time.Time `gorm:"not null;index" json:"expires_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (GormPoolLease) TableName() string { return "sub2_pool_leases" }

// GormPoolSnapshotCache persists the latest successful snapshot per target.
// It backs the read-only "cached snapshot" page view so opening the account
// pool page does not hit the upstream admin API.
type GormPoolSnapshotCache struct {
	TargetID     uint      `gorm:"primaryKey" json:"target_id"`
	SnapshotJSON string    `gorm:"type:text;not null" json:"-"`
	PreviewJSON  string    `gorm:"type:text;not null" json:"-"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (GormPoolSnapshotCache) TableName() string { return "sub2_pool_snapshots" }

type GormStateStore struct {
	db *gorm.DB
}

func NewGormStateStore(db *gorm.DB) *GormStateStore {
	return &GormStateStore{db: db}
}

// AutoMigrate is intentionally separate from storage.AutoMigrate. The main
// integration point can opt these isolated tables in without touching
// upstream-sync tables.
func (s *GormStateStore) AutoMigrate() error {
	return s.db.AutoMigrate(
		&GormTargetState{},
		&GormPoolOutbox{},
		&GormPoolRun{},
		&GormPoolAutomation{},
		&GormPoolLease{},
		&GormPoolAccountRateMapping{},
		&GormPoolKeyAttestation{},
		&GormPoolSnapshotCache{},
	)
}

func (s *GormStateStore) AcquireLease(targetID uint, owner string, expiresAt time.Time) (bool, error) {
	if targetID == 0 || owner == "" {
		return false, errors.New("lease target and owner are required")
	}
	now := time.Now()
	result := s.db.Model(&GormPoolLease{}).
		Where("target_id = ? AND expires_at <= ?", targetID, now).
		Updates(map[string]any{
			"owner":      owner,
			"expires_at": expiresAt,
			"updated_at": now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	row := GormPoolLease{
		TargetID:  targetID,
		Owner:     owner,
		ExpiresAt: expiresAt,
		UpdatedAt: now,
	}
	result = s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (s *GormStateStore) ReleaseLease(targetID uint, owner string) error {
	return s.db.Where("target_id = ? AND owner = ?", targetID, owner).Delete(&GormPoolLease{}).Error
}

func (s *GormStateStore) RenewLease(targetID uint, owner string, expiresAt time.Time) (bool, error) {
	if targetID == 0 || owner == "" {
		return false, errors.New("lease target and owner are required")
	}
	result := s.db.Model(&GormPoolLease{}).
		Where("target_id = ? AND owner = ? AND expires_at > ?", targetID, owner, time.Now()).
		Updates(map[string]any{
			"expires_at": expiresAt,
			"updated_at": time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (s *GormStateStore) Load(targetID uint) (TargetState, error) {
	var row GormTargetState
	if err := s.db.First(&row, "target_id = ?", targetID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return emptyTargetState(), nil
		}
		return TargetState{}, err
	}
	state := emptyTargetState()
	state.LastHealthyCount = row.LastHealthyCount
	if row.MultiplierStateJSON != "" {
		var payload targetStatePayload
		if err := json.Unmarshal([]byte(row.MultiplierStateJSON), &payload); err == nil && payload.Accounts != nil {
			state.MultiplierByAccount = payload.Accounts
			state.LowBalanceByAccount = payload.LowBalances
			state.MissingMultiplierIDs = payload.MissingMultiplierIDs
			state.MissingBalanceIDs = payload.MissingBalanceIDs
			state.GuardCodes = payload.GuardCodes
		} else {
			// Compatibility with the first development build, which stored the
			// account multiplier map directly in this column.
			if err := json.Unmarshal([]byte(row.MultiplierStateJSON), &state.MultiplierByAccount); err != nil {
				return TargetState{}, err
			}
		}
	}
	normalizeTargetState(&state)
	return state, nil
}

func (s *GormStateStore) Save(targetID uint, state TargetState) error {
	row, err := targetStateRow(targetID, state)
	if err != nil {
		return err
	}
	return s.db.Save(&row).Error
}

func targetStateRow(targetID uint, state TargetState) (GormTargetState, error) {
	normalizeTargetState(&state)
	raw, err := json.Marshal(targetStatePayload{
		Accounts:             state.MultiplierByAccount,
		LowBalances:          state.LowBalanceByAccount,
		MissingMultiplierIDs: state.MissingMultiplierIDs,
		MissingBalanceIDs:    state.MissingBalanceIDs,
		GuardCodes:           state.GuardCodes,
	})
	if err != nil {
		return GormTargetState{}, err
	}
	return GormTargetState{
		TargetID:            targetID,
		LastHealthyCount:    state.LastHealthyCount,
		MultiplierStateJSON: string(raw),
		UpdatedAt:           time.Now(),
	}, nil
}

func (s *GormStateStore) LoadAutomation(targetID uint) (AutomationState, error) {
	var row GormPoolAutomation
	if err := s.db.First(&row, "target_id = ?", targetID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AutomationState{TargetID: targetID}, nil
		}
		return AutomationState{}, err
	}
	return AutomationState{
		TargetID:  row.TargetID,
		Enabled:   row.Enabled,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

func (s *GormStateStore) SaveAutomation(state AutomationState) error {
	if state.TargetID == 0 {
		return errors.New("automation target id is required")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now()
	}
	return s.db.Save(&GormPoolAutomation{
		TargetID:  state.TargetID,
		Enabled:   state.Enabled,
		UpdatedAt: state.UpdatedAt,
	}).Error
}

func (s *GormStateStore) EnqueueOutbox(targetID uint, event PoolEvent) (uint, bool, error) {
	return enqueueOutboxDB(s.db, targetID, 0, event)
}

func enqueueOutboxDB(db *gorm.DB, targetID, runID uint, event PoolEvent) (uint, bool, error) {
	raw, err := json.Marshal(event)
	if err != nil {
		return 0, false, err
	}
	row := GormPoolOutbox{
		TargetID:  targetID,
		RunID:     runID,
		EventKey:  poolEventKey(event),
		EventJSON: string(raw),
		Status:    "pending",
	}
	result := db.Where("target_id = ? AND run_id = ?", targetID, runID).Attrs(row).FirstOrCreate(&row)
	if result.Error != nil {
		return 0, false, result.Error
	}
	return row.ID, result.RowsAffected == 1, nil
}

func (s *GormStateStore) ListPendingOutbox(targetID uint, limit int) ([]OutboxItem, error) {
	if limit <= 0 {
		limit = 20
	}
	var rows []GormPoolOutbox
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&GormPoolOutbox{}).
			Where("status = ? AND updated_at < ?", "processing", time.Now().Add(-10*time.Minute)).
			Update("status", "pending").Error; err != nil {
			return err
		}
		query := tx.Where("status = ?", "pending")
		if targetID != 0 {
			query = query.Where("target_id = ?", targetID)
		}
		var candidates []GormPoolOutbox
		if err := query.Order("id ASC").Limit(limit).Find(&candidates).Error; err != nil {
			return err
		}
		for _, candidate := range candidates {
			result := tx.Model(&GormPoolOutbox{}).
				Where("id = ? AND status = ?", candidate.ID, "pending").
				Update("status", "processing")
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				candidate.Status = "processing"
				rows = append(rows, candidate)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]OutboxItem, 0, len(rows))
	for _, row := range rows {
		var event PoolEvent
		if err := json.Unmarshal([]byte(row.EventJSON), &event); err != nil {
			return nil, err
		}
		out = append(out, OutboxItem{
			ID:        row.ID,
			RunID:     row.RunID,
			TargetID:  row.TargetID,
			Event:     event,
			Status:    row.Status,
			Attempts:  row.Attempts,
			CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

func (s *GormStateStore) MarkOutboxDelivery(outboxID uint, delivered bool) error {
	status := "pending"
	deliveryStatus := "failed"
	if delivered {
		status = "sent"
		deliveryStatus = "sent"
	}
	return s.db.Model(&GormPoolOutbox{}).Where("id = ?", outboxID).Updates(map[string]any{
		"status":               status,
		"last_delivery_status": deliveryStatus,
		"attempts":             gorm.Expr("attempts + ?", 1),
	}).Error
}

func (s *GormStateStore) RecordRun(record RunRecord) (uint, error) {
	guardCodes, err := json.Marshal(record.GuardCodes)
	if err != nil {
		return 0, err
	}
	intent, err := marshalIntent(record.Intent)
	if err != nil {
		return 0, err
	}
	row := GormPoolRun{
		TargetID:           record.TargetID,
		Status:             record.Status,
		PreviewSignature:   record.PreviewSignature,
		ChangeCount:        record.ChangeCount,
		RateChangeCount:    record.RateChangeCount,
		AppliedCount:       record.AppliedCount,
		FailedCount:        record.FailedCount,
		GuardCount:         record.GuardCount,
		GuardCodesJSON:     string(guardCodes),
		IntentJSON:         string(intent),
		StatePersisted:     record.StatePersisted,
		NotificationStatus: record.NotificationStatus,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var active int64
		if err := tx.Model(&GormPoolRun{}).
			Where("target_id = ? AND status = ?", record.TargetID, "prepared").
			Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return errors.New("prepared run already exists")
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}

func (s *GormStateStore) FinalizeCycle(
	targetID, runID uint,
	record RunRecord,
	state TargetState,
	event *PoolEvent,
) (uint, bool, error) {
	stateRow, err := targetStateRow(targetID, state)
	if err != nil {
		return 0, false, err
	}
	guardCodes, err := json.Marshal(record.GuardCodes)
	if err != nil {
		return 0, false, err
	}
	intent, err := marshalIntent(record.Intent)
	if err != nil {
		return 0, false, err
	}
	var outboxID uint
	var created bool
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&stateRow).Error; err != nil {
			return err
		}
		if event != nil && hasEventSignal(*event) {
			var enqueueErr error
			outboxID, created, enqueueErr = enqueueOutboxDB(tx, targetID, runID, *event)
			if enqueueErr != nil {
				return enqueueErr
			}
		}
		result := tx.Model(&GormPoolRun{}).
			Where("id = ? AND target_id = ? AND status = ?", runID, targetID, "prepared").
			Updates(map[string]any{
				"status":              record.Status,
				"preview_signature":   record.PreviewSignature,
				"change_count":        record.ChangeCount,
				"rate_change_count":   record.RateChangeCount,
				"applied_count":       record.AppliedCount,
				"failed_count":        record.FailedCount,
				"guard_count":         record.GuardCount,
				"guard_codes_json":    string(guardCodes),
				"intent_json":         string(intent),
				"state_persisted":     true,
				"notification_status": record.NotificationStatus,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("prepared run not found")
		}
		return nil
	})
	return outboxID, created, err
}

func (s *GormStateStore) UpdateRunNotification(runID uint, status string) error {
	return s.db.Model(&GormPoolRun{}).
		Where("id = ?", runID).
		Update("notification_status", status).Error
}

func (s *GormStateStore) ListRuns(targetID uint, limit int) ([]RunRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	query := s.db
	if targetID != 0 {
		query = query.Where("target_id = ?", targetID)
	}
	var rows []GormPoolRun
	if err := query.Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRunRows(rows)
}

func decodeRunRows(rows []GormPoolRun) ([]RunRecord, error) {
	out := make([]RunRecord, 0, len(rows))
	for _, row := range rows {
		var guardCodes []string
		if row.GuardCodesJSON != "" {
			if err := json.Unmarshal([]byte(row.GuardCodesJSON), &guardCodes); err != nil {
				return nil, err
			}
		}
		var intent []PriorityProposal
		if row.IntentJSON != "" {
			if err := unmarshalIntent([]byte(row.IntentJSON), &intent); err != nil {
				return nil, err
			}
		}
		out = append(out, RunRecord{
			ID:                 row.ID,
			TargetID:           row.TargetID,
			Status:             row.Status,
			PreviewSignature:   row.PreviewSignature,
			ChangeCount:        row.ChangeCount,
			RateChangeCount:    row.RateChangeCount,
			AppliedCount:       row.AppliedCount,
			FailedCount:        row.FailedCount,
			GuardCount:         row.GuardCount,
			GuardCodes:         guardCodes,
			StatePersisted:     row.StatePersisted,
			NotificationStatus: row.NotificationStatus,
			Intent:             intent,
			CreatedAt:          row.CreatedAt,
		})
	}
	return out, nil
}

func (s *GormStateStore) ListPreparedRuns(targetID uint, limit int) ([]RunRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	query := s.db.Where("status = ?", "prepared")
	if targetID != 0 {
		query = query.Where("target_id = ?", targetID)
	}
	var rows []GormPoolRun
	if err := query.Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRunRows(rows)
}

func (s *GormStateStore) LoadCachedSnapshot(targetID uint) (*Snapshot, *PriorityPreview, error) {
	if targetID == 0 {
		return nil, nil, errors.New("snapshot cache target id is required")
	}
	var row GormPoolSnapshotCache
	if err := s.db.First(&row, "target_id = ?", targetID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(row.SnapshotJSON), &snapshot); err != nil {
		return nil, nil, err
	}
	var preview PriorityPreview
	if err := json.Unmarshal([]byte(row.PreviewJSON), &preview); err != nil {
		return nil, nil, err
	}
	if snapshot.GeneratedAt.IsZero() {
		snapshot.GeneratedAt = row.UpdatedAt
	}
	if preview.GeneratedAt.IsZero() {
		preview.GeneratedAt = snapshot.GeneratedAt
	}
	return &snapshot, &preview, nil
}

func (s *GormStateStore) SaveCachedSnapshot(snapshot *Snapshot, preview *PriorityPreview) error {
	if snapshot == nil || preview == nil || snapshot.TargetID == 0 || preview.TargetID != snapshot.TargetID {
		return errors.New("snapshot cache target id is required")
	}
	snapshotRaw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	previewRaw, err := json.Marshal(preview)
	if err != nil {
		return err
	}
	now := time.Now()
	row := GormPoolSnapshotCache{
		TargetID:     snapshot.TargetID,
		SnapshotJSON: string(snapshotRaw),
		PreviewJSON:  string(previewRaw),
		UpdatedAt:    now,
	}
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "target_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"snapshot_json", "preview_json", "updated_at",
		}),
	}).Create(&row).Error
}

type persistedPriorityProposal struct {
	AccountID              int64   `json:"account_id"`
	AccountName            string  `json:"account_name"`
	CurrentPriority        int     `json:"current_priority"`
	StagingPriority        int     `json:"staging_priority"`
	TargetPriority         int     `json:"target_priority"`
	Channel                string  `json:"channel"`
	Reason                 string  `json:"reason"`
	ExpectedGroupIDs       []int64 `json:"expected_group_ids"`
	ExpectedStatus         string  `json:"expected_status"`
	ExpectedPoolManaged    bool    `json:"expected_pool_managed"`
	ExpectedIdentityDigest string  `json:"expected_identity_digest"`
}

func marshalIntent(intent []PriorityProposal) ([]byte, error) {
	persisted := make([]persistedPriorityProposal, 0, len(intent))
	for _, proposal := range intent {
		persisted = append(persisted, persistedPriorityProposal{
			AccountID:              proposal.AccountID,
			AccountName:            proposal.AccountName,
			CurrentPriority:        proposal.CurrentPriority,
			StagingPriority:        proposal.stagingPriority,
			TargetPriority:         proposal.TargetPriority,
			Channel:                proposal.Channel,
			Reason:                 proposal.Reason,
			ExpectedGroupIDs:       append([]int64(nil), proposal.expectedGroupIDs...),
			ExpectedStatus:         proposal.expectedStatus,
			ExpectedPoolManaged:    proposal.expectedPoolManaged,
			ExpectedIdentityDigest: proposal.expectedIdentityDigest,
		})
	}
	return json.Marshal(persisted)
}

func unmarshalIntent(raw []byte, intent *[]PriorityProposal) error {
	var persisted []persistedPriorityProposal
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return err
	}
	out := make([]PriorityProposal, 0, len(persisted))
	for _, proposal := range persisted {
		out = append(out, PriorityProposal{
			AccountID:              proposal.AccountID,
			AccountName:            proposal.AccountName,
			CurrentPriority:        proposal.CurrentPriority,
			stagingPriority:        proposal.StagingPriority,
			TargetPriority:         proposal.TargetPriority,
			Channel:                proposal.Channel,
			Reason:                 proposal.Reason,
			expectedGroupIDs:       append([]int64(nil), proposal.ExpectedGroupIDs...),
			expectedStatus:         proposal.ExpectedStatus,
			expectedPoolManaged:    proposal.ExpectedPoolManaged,
			expectedIdentityDigest: proposal.ExpectedIdentityDigest,
		})
	}
	*intent = out
	return nil
}

type MemoryStateStore struct {
	mu         sync.Mutex
	items      map[uint]TargetState
	automation map[uint]AutomationState
	outbox     map[uint]OutboxItem
	runs       map[uint]RunRecord
	nextOutbox uint
	nextRun    uint
	eventIDs   map[string]uint
	leases     map[uint]GormPoolLease
	snapshots  map[uint]cachedSnapshot
}

type cachedSnapshot struct {
	snapshot Snapshot
	preview  PriorityPreview
}

func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{
		items:      map[uint]TargetState{},
		automation: map[uint]AutomationState{},
		outbox:     map[uint]OutboxItem{},
		runs:       map[uint]RunRecord{},
		eventIDs:   map[string]uint{},
		leases:     map[uint]GormPoolLease{},
		snapshots:  map[uint]cachedSnapshot{},
	}
}

func (s *MemoryStateStore) AcquireLease(targetID uint, owner string, expiresAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.leases[targetID]
	if exists && current.ExpiresAt.After(time.Now()) {
		return false, nil
	}
	s.leases[targetID] = GormPoolLease{TargetID: targetID, Owner: owner, ExpiresAt: expiresAt}
	return true, nil
}

func (s *MemoryStateStore) ReleaseLease(targetID uint, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, exists := s.leases[targetID]; exists && current.Owner == owner {
		delete(s.leases, targetID)
	}
	return nil
}

func (s *MemoryStateStore) RenewLease(targetID uint, owner string, expiresAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.leases[targetID]
	if !exists || current.Owner != owner || !current.ExpiresAt.After(time.Now()) {
		return false, nil
	}
	current.ExpiresAt = expiresAt
	s.leases[targetID] = current
	return true, nil
}

func (s *MemoryStateStore) Load(targetID uint) (TargetState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneState(s.items[targetID]), nil
}

func (s *MemoryStateStore) Save(targetID uint, state TargetState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[targetID] = cloneState(state)
	return nil
}

func (s *MemoryStateStore) LoadCachedSnapshot(targetID uint) (*Snapshot, *PriorityPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.snapshots[targetID]
	if !exists {
		return nil, nil, nil
	}
	return cloneSnapshot(entry.snapshot), clonePriorityPreview(entry.preview), nil
}

func (s *MemoryStateStore) SaveCachedSnapshot(snapshot *Snapshot, preview *PriorityPreview) error {
	if snapshot == nil || preview == nil || snapshot.TargetID == 0 || preview.TargetID != snapshot.TargetID {
		return errors.New("snapshot cache target id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[snapshot.TargetID] = cachedSnapshot{
		snapshot: *cloneSnapshot(*snapshot),
		preview:  *clonePriorityPreview(*preview),
	}
	return nil
}

func cloneSnapshot(snapshot Snapshot) *Snapshot {
	raw, _ := json.Marshal(snapshot)
	var out Snapshot
	_ = json.Unmarshal(raw, &out)
	return &out
}

func clonePriorityPreview(preview PriorityPreview) *PriorityPreview {
	raw, _ := json.Marshal(preview)
	var out PriorityPreview
	_ = json.Unmarshal(raw, &out)
	return &out
}

func (s *MemoryStateStore) LoadAutomation(targetID uint) (AutomationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.automation[targetID]
	if !ok {
		return AutomationState{TargetID: targetID}, nil
	}
	return state, nil
}

func (s *MemoryStateStore) SaveAutomation(state AutomationState) error {
	if state.TargetID == 0 {
		return errors.New("automation target id is required")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.automation[state.TargetID] = state
	return nil
}

func (s *MemoryStateStore) EnqueueOutbox(targetID uint, event PoolEvent) (uint, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := poolEventKey(event)
	if existing := s.eventIDs[key]; existing != 0 {
		return existing, false, nil
	}
	s.nextOutbox++
	s.outbox[s.nextOutbox] = OutboxItem{
		ID:        s.nextOutbox,
		RunID:     0,
		TargetID:  targetID,
		Event:     cloneEvent(event),
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	s.eventIDs[key] = s.nextOutbox
	return s.nextOutbox, true, nil
}

func poolEventKey(event PoolEvent) string {
	canonical := cloneEvent(event)
	canonical.GeneratedAt = time.Time{}
	if canonical.PriorityResult != nil {
		canonical.PriorityResult.Preview.GeneratedAt = time.Time{}
	}
	raw, _ := json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *MemoryStateStore) ListPendingOutbox(targetID uint, limit int) ([]OutboxItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	ids := make([]uint, 0, len(s.outbox))
	for id, item := range s.outbox {
		if item.Status == "pending" && (targetID == 0 || item.TargetID == targetID) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]OutboxItem, 0, min(limit, len(ids)))
	for _, id := range ids {
		if len(out) >= limit {
			break
		}
		item := s.outbox[id]
		item.Status = "processing"
		s.outbox[id] = item
		item.Event = cloneEvent(item.Event)
		out = append(out, item)
	}
	return out, nil
}

func (s *MemoryStateStore) MarkOutboxDelivery(outboxID uint, delivered bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.outbox[outboxID]
	if !ok {
		return nil
	}
	item.Attempts++
	if delivered {
		item.Status = "sent"
	} else {
		item.Status = "pending"
	}
	s.outbox[outboxID] = item
	return nil
}

func (s *MemoryStateStore) FinalizeCycle(
	targetID, runID uint,
	record RunRecord,
	state TargetState,
	event *PoolEvent,
) (uint, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.runs[runID]; !exists {
		return 0, false, errors.New("prepared run not found")
	}
	if s.runs[runID].Status != "prepared" {
		return 0, false, errors.New("prepared run not found")
	}
	s.items[targetID] = cloneState(state)
	var outboxID uint
	var created bool
	if event != nil && hasEventSignal(*event) {
		key := "run:" + strconv.FormatUint(uint64(targetID), 10) + ":" + strconv.FormatUint(uint64(runID), 10)
		if existing := s.eventIDs[key]; existing != 0 {
			outboxID = existing
		} else {
			s.nextOutbox++
			outboxID = s.nextOutbox
			s.outbox[outboxID] = OutboxItem{
				ID:        outboxID,
				RunID:     runID,
				TargetID:  targetID,
				Event:     cloneEvent(*event),
				Status:    "pending",
				CreatedAt: time.Now(),
			}
			s.eventIDs[key] = outboxID
			created = true
		}
	}
	record.ID = runID
	record.TargetID = targetID
	record.StatePersisted = true
	s.runs[runID] = cloneRunRecord(record)
	return outboxID, created, nil
}

func (s *MemoryStateStore) UpdateRunNotification(runID uint, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.runs[runID]
	if !exists {
		return nil
	}
	record.NotificationStatus = status
	s.runs[runID] = record
	return nil
}

func (s *MemoryStateStore) RecordRun(record RunRecord) (uint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.runs {
		if existing.TargetID == record.TargetID && existing.Status == "prepared" {
			return 0, errors.New("prepared run already exists")
		}
	}
	s.nextRun++
	record.ID = s.nextRun
	record.CreatedAt = time.Now()
	s.runs[record.ID] = cloneRunRecord(record)
	return record.ID, nil
}

func (s *MemoryStateStore) ListRuns(targetID uint, limit int) ([]RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	ids := make([]uint, 0, len(s.runs))
	for id, item := range s.runs {
		if targetID == 0 || item.TargetID == targetID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	out := make([]RunRecord, 0, min(limit, len(ids)))
	for _, id := range ids {
		if len(out) >= limit {
			break
		}
		out = append(out, cloneRunRecord(s.runs[id]))
	}
	return out, nil
}

func (s *MemoryStateStore) ListPreparedRuns(targetID uint, limit int) ([]RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	ids := make([]uint, 0, len(s.runs))
	for id, item := range s.runs {
		if item.Status == "prepared" && (targetID == 0 || item.TargetID == targetID) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]RunRecord, 0, min(limit, len(ids)))
	for _, id := range ids {
		if len(out) >= limit {
			break
		}
		out = append(out, cloneRunRecord(s.runs[id]))
	}
	return out, nil
}

func cloneState(value TargetState) TargetState {
	out := TargetState{
		LastHealthyCount:     value.LastHealthyCount,
		MultiplierByAccount:  make(map[int64]MultiplierState, len(value.MultiplierByAccount)),
		LowBalanceByAccount:  make(map[int64]float64, len(value.LowBalanceByAccount)),
		MissingMultiplierIDs: append([]int64(nil), value.MissingMultiplierIDs...),
		MissingBalanceIDs:    append([]int64(nil), value.MissingBalanceIDs...),
		GuardCodes:           append([]string(nil), value.GuardCodes...),
	}
	for accountID, multiplier := range value.MultiplierByAccount {
		out.MultiplierByAccount[accountID] = MultiplierState{
			UpstreamRate: cloneFloat(multiplier.UpstreamRate),
			GroupRate:    cloneFloat(multiplier.GroupRate),
		}
	}
	for accountID, balance := range value.LowBalanceByAccount {
		out.LowBalanceByAccount[accountID] = balance
	}
	return out
}

func cloneRunRecord(value RunRecord) RunRecord {
	out := value
	out.GuardCodes = append([]string(nil), value.GuardCodes...)
	out.Intent = make([]PriorityProposal, len(value.Intent))
	for index, proposal := range value.Intent {
		out.Intent[index] = proposal
		out.Intent[index].expectedGroupIDs = append([]int64(nil), proposal.expectedGroupIDs...)
	}
	return out
}

type targetStatePayload struct {
	Accounts             map[int64]MultiplierState `json:"accounts"`
	LowBalances          map[int64]float64         `json:"low_balances,omitempty"`
	MissingMultiplierIDs []int64                   `json:"missing_multiplier_ids,omitempty"`
	MissingBalanceIDs    []int64                   `json:"missing_balance_ids,omitempty"`
	GuardCodes           []string                  `json:"guard_codes,omitempty"`
}

func emptyTargetState() TargetState {
	return TargetState{
		MultiplierByAccount: map[int64]MultiplierState{},
		LowBalanceByAccount: map[int64]float64{},
	}
}

func normalizeTargetState(state *TargetState) {
	if state.MultiplierByAccount == nil {
		state.MultiplierByAccount = map[int64]MultiplierState{}
	}
	if state.LowBalanceByAccount == nil {
		state.LowBalanceByAccount = map[int64]float64{}
	}
	sort.Slice(state.MissingMultiplierIDs, func(i, j int) bool {
		return state.MissingMultiplierIDs[i] < state.MissingMultiplierIDs[j]
	})
	sort.Slice(state.MissingBalanceIDs, func(i, j int) bool {
		return state.MissingBalanceIDs[i] < state.MissingBalanceIDs[j]
	})
	sort.Strings(state.GuardCodes)
}

func cloneEvent(event PoolEvent) PoolEvent {
	raw, _ := json.Marshal(event)
	var out PoolEvent
	_ = json.Unmarshal(raw, &out)
	return out
}
