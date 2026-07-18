package storage

import (
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
)

// GroupDiscoveryCandidates persists the local review queue for upstream groups.
// It deliberately has no remote side effects; all remote work belongs to the
// discovery service after an item has been explicitly approved.
type GroupDiscoveryCandidates struct{ db *gorm.DB }

type GroupDiscoverySourceRef struct {
	SourceChannelID uint
	SourceGroupKey  string
}

func NewGroupDiscoveryCandidates(db *gorm.DB) *GroupDiscoveryCandidates {
	return &GroupDiscoveryCandidates{db: db}
}

func (r *GroupDiscoveryCandidates) List() ([]GroupDiscoveryCandidate, error) {
	var list []GroupDiscoveryCandidate
	if err := r.db.
		Order("channel_type ASC").
		Order("ratio ASC").
		Order("source_channel_name ASC").
		Order("source_group_name ASC").
		Order("id ASC").
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *GroupDiscoveryCandidates) FindByID(id uint) (*GroupDiscoveryCandidate, error) {
	var item GroupDiscoveryCandidate
	if err := r.db.First(&item, id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *GroupDiscoveryCandidates) FindBySource(channelID uint, groupKey string) (*GroupDiscoveryCandidate, error) {
	var item GroupDiscoveryCandidate
	if err := r.db.First(&item, "source_channel_id = ? AND source_group_key = ?", channelID, groupKey).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// DeleteUnselectedSafe prunes only local, unapplied review rows. Anything
// approved or carrying a remote creation/apply trace is retained so a scan can
// never orphan a source key or Sub2 account.
func (r *GroupDiscoveryCandidates) DeleteUnselectedSafe(keep []GroupDiscoverySourceRef) (int64, error) {
	selected := make(map[GroupDiscoverySourceRef]struct{}, len(keep))
	for _, item := range keep {
		selected[item] = struct{}{}
	}
	var deleted int64
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var items []GroupDiscoveryCandidate
		if err := tx.Find(&items).Error; err != nil {
			return err
		}
		for i := range items {
			item := &items[i]
			ref := GroupDiscoverySourceRef{SourceChannelID: item.SourceChannelID, SourceGroupKey: item.SourceGroupKey}
			if _, exists := selected[ref]; exists || !safeToPruneDiscoveryCandidate(item) {
				continue
			}
			result := tx.Delete(item)
			if result.Error != nil {
				return result.Error
			}
			deleted += result.RowsAffected
		}
		return nil
	})
	return deleted, err
}

func safeToPruneDiscoveryCandidate(item *GroupDiscoveryCandidate) bool {
	if item == nil || (item.Status != "pending" && item.Status != "rejected") {
		return false
	}
	return item.SourceAPIKeyID == nil &&
		item.TargetAccountID == nil &&
		item.SourceKeyCreateAttemptedAt == nil &&
		item.TargetAccountCreateAttemptedAt == nil &&
		item.LastAttemptAt == nil &&
		item.AppliedAt == nil
}

func (r *GroupDiscoveryCandidates) Create(item *GroupDiscoveryCandidate) error {
	setCandidateDefaults(item)
	return r.db.Create(item).Error
}

func setCandidateDefaults(item *GroupDiscoveryCandidate) {
	if item.DiscoveredAt.IsZero() {
		item.DiscoveredAt = time.Now()
	}
	if item.LastSeenAt.IsZero() {
		item.LastSeenAt = item.DiscoveredAt
	}
	if item.TargetGroupIDsJSON == "" {
		item.TargetGroupIDsJSON = "[]"
	}
	if item.TargetGroupNamesJSON == "" {
		item.TargetGroupNamesJSON = "[]"
	}
}

func (r *GroupDiscoveryCandidates) Update(item *GroupDiscoveryCandidate) error {
	return r.db.Save(item).Error
}

// UpsertScanned writes only source-side fields. In particular it never changes
// the user's review decision or any remote-object mapping accumulated by apply.
func (r *GroupDiscoveryCandidates) UpsertScanned(item *GroupDiscoveryCandidate) (*GroupDiscoveryCandidate, bool, error) {
	var existing GroupDiscoveryCandidate
	err := r.db.First(&existing, "source_channel_id = ? AND source_group_key = ?", item.SourceChannelID, item.SourceGroupKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := r.Create(item); err != nil {
			return nil, false, err
		}
		return item, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := r.db.Model(&GroupDiscoveryCandidate{}).Where("id = ?", existing.ID).Updates(map[string]any{
		"source_channel_name":      item.SourceChannelName,
		"source_group_id":          item.SourceGroupID,
		"source_group_name":        item.SourceGroupName,
		"source_group_description": item.SourceGroupDescription,
		"ratio":                    item.Ratio,
		"channel_type":             item.ChannelType,
		"last_seen_at":             item.LastSeenAt,
	}).Error; err != nil {
		return nil, false, err
	}
	if err := r.db.First(&existing, existing.ID).Error; err != nil {
		return nil, false, err
	}
	return &existing, false, nil
}

func (r *GroupDiscoveryCandidates) UpdateApplyState(
	id uint,
	status, applyError string,
	lastAttemptAt, appliedAt *time.Time,
) error {
	return r.db.Model(&GroupDiscoveryCandidate{}).Where("id = ?", id).Updates(map[string]any{
		"status":          status,
		"apply_error":     applyError,
		"last_attempt_at": lastAttemptAt,
		"applied_at":      appliedAt,
	}).Error
}

func (r *GroupDiscoveryCandidates) SetSourceAPIKey(id uint, keyID *int64, name string, attemptedAt *time.Time) error {
	return r.db.Model(&GroupDiscoveryCandidate{}).Where("id = ?", id).Updates(map[string]any{
		"source_api_key_id":              keyID,
		"source_api_key_name":            name,
		"source_key_create_attempted_at": attemptedAt,
	}).Error
}

func (r *GroupDiscoveryCandidates) SetTargetAccount(id uint, accountID *int64, name string, attemptedAt *time.Time) error {
	return r.db.Model(&GroupDiscoveryCandidate{}).Where("id = ?", id).Updates(map[string]any{
		"target_account_id":                  accountID,
		"target_account_name":                name,
		"target_account_create_attempted_at": attemptedAt,
	}).Error
}

func (r *GroupDiscoveryCandidates) ParseTargetGroupIDs(item *GroupDiscoveryCandidate) ([]int64, error) {
	return parseInt64Array(item.TargetGroupIDsJSON)
}

func (r *GroupDiscoveryCandidates) ParseTargetGroupNames(item *GroupDiscoveryCandidate) ([]string, error) {
	return parseStringArray(item.TargetGroupNamesJSON)
}

func MarshalInt64Array(list []int64) string {
	if len(list) == 0 {
		return "[]"
	}
	body, _ := json.Marshal(list)
	return string(body)
}

func MarshalStringArray(list []string) string {
	if len(list) == 0 {
		return "[]"
	}
	body, _ := json.Marshal(list)
	return string(body)
}

func parseInt64Array(raw string) ([]int64, error) {
	var list []int64
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	return list, nil
}

func parseStringArray(raw string) ([]string, error) {
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	return list, nil
}
