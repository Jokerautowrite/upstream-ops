package sub2pool

import (
	"context"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GormPoolKeyAttestation struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TargetID     uint      `gorm:"not null;uniqueIndex:idx_sub2_pool_key_attestation" json:"target_id"`
	AccountID    int64     `gorm:"not null;uniqueIndex:idx_sub2_pool_key_attestation" json:"account_id"`
	APIKeySHA256 string    `gorm:"size:64;not null" json:"-"`
	ChannelID    uint      `gorm:"not null;index" json:"channel_id"`
	Source       string    `gorm:"size:64;not null;default:'operator_attested'" json:"source"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (GormPoolKeyAttestation) TableName() string {
	return "sub2_pool_key_attestations"
}

func (s *GormStateStore) ListKeyAttestations(targetID uint) ([]KeyAttestation, error) {
	var rows []GormPoolKeyAttestation
	if err := s.db.
		Where("target_id = ?", targetID).
		Order("account_id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]KeyAttestation, 0, len(rows))
	for _, row := range rows {
		out = append(out, KeyAttestation{
			TargetID:     row.TargetID,
			AccountID:    row.AccountID,
			APIKeySHA256: row.APIKeySHA256,
			ChannelID:    row.ChannelID,
			Source:       row.Source,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}
	return out, nil
}

func (s *GormStateStore) UpsertKeyAttestations(items []KeyAttestation) error {
	if len(items) == 0 {
		return nil
	}
	rows := make([]GormPoolKeyAttestation, 0, len(items))
	seen := make(map[[2]uint64]struct{}, len(items))
	for _, item := range items {
		fingerprint := normalizeAttestedSHA256(item.APIKeySHA256)
		if item.TargetID == 0 || item.AccountID <= 0 || item.ChannelID == 0 || fingerprint == "" {
			return errors.New("invalid key attestation")
		}
		key := [2]uint64{uint64(item.TargetID), uint64(item.AccountID)}
		if _, exists := seen[key]; exists {
			return errors.New("duplicate key attestation")
		}
		seen[key] = struct{}{}
		source := strings.TrimSpace(item.Source)
		if source == "" {
			source = "operator_attested"
		}
		rows = append(rows, GormPoolKeyAttestation{
			TargetID:     item.TargetID,
			AccountID:    item.AccountID,
			APIKeySHA256: fingerprint,
			ChannelID:    item.ChannelID,
			Source:       source,
		})
	}
	now := time.Now()
	for index := range rows {
		rows[index].CreatedAt = now
		rows[index].UpdatedAt = now
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "target_id"},
				{Name: "account_id"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"api_key_sha256",
				"channel_id",
				"source",
				"updated_at",
			}),
		}).Create(&rows).Error
	})
}

// ListKeyAttestations returns operator-owned bindings for a known target. The
// API layer must map these into a safe DTO because the stored fingerprint is
// deliberately never serializable.
func (s *Service) ListKeyAttestations(targetID uint) ([]KeyAttestation, error) {
	if s == nil || s.keyAttestations == nil {
		return nil, ErrUnavailable
	}
	if _, err := s.targetAccess(targetID); err != nil {
		return nil, err
	}
	rows, err := s.keyAttestations.ListKeyAttestations(targetID)
	if err != nil {
		return nil, ErrUnavailable
	}
	return rows, nil
}

type attestedMatch struct {
	channel storage.Channel
}

func resolveKeyAttestations(
	targetID uint,
	accounts []sub2api.PoolAccount,
	store KeyAttestationStore,
	channels ChannelStore,
) map[int64]attestedMatch {
	out := make(map[int64]attestedMatch)
	if targetID == 0 || store == nil || channels == nil {
		return out
	}
	rows, err := store.ListKeyAttestations(targetID)
	if err != nil || len(rows) == 0 {
		return out
	}
	monitorChannels, err := channels.List()
	if err != nil {
		return out
	}
	channelByID := make(map[uint]storage.Channel, len(monitorChannels))
	for _, channel := range monitorChannels {
		channelByID[channel.ID] = channel
	}
	accountByID := make(map[int64]sub2api.PoolAccount, len(accounts))
	for _, account := range accounts {
		accountByID[account.Account.ID] = account
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].AccountID < rows[j].AccountID
	})
	for _, row := range rows {
		account, found := accountByID[row.AccountID]
		if !found {
			continue
		}
		identity := account.Identity()
		if !identity.FingerprintSeen ||
			identity.APIKeySHA256 == "" ||
			identity.APIKeySHA256 != normalizeAttestedSHA256(row.APIKeySHA256) {
			continue
		}
		channel, found := channelByID[row.ChannelID]
		if !found || !channel.MonitorEnabled {
			continue
		}
		if normalizeURL(identity.BaseURL) == "" ||
			normalizeURL(identity.BaseURL) != normalizeURL(channel.SiteURL) {
			continue
		}
		out[row.AccountID] = attestedMatch{channel: channel}
	}
	return out
}

func normalizeAttestedSHA256(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

// AttestKeyMappings creates or updates explicit fingerprint bindings. It reads
// the safe fingerprint returned by Sub2's admin API; raw API keys are never
// persisted or returned.
func (s *Service) AttestKeyMappings(
	ctx context.Context,
	targetID uint,
	inputs []KeyAttestationInput,
) ([]KeyAttestation, error) {
	if len(inputs) == 0 || s == nil || s.admin == nil || s.keyAttestations == nil || s.matcher == nil || s.matcher.channels == nil {
		return nil, ErrInvalidInput
	}
	target, err := s.targetAccess(targetID)
	if err != nil {
		return nil, err
	}
	accounts, err := s.admin.ListAllPoolAccounts(ctx, target)
	if err != nil {
		return nil, ErrUnavailable
	}
	monitorChannels, err := s.matcher.channels.List()
	if err != nil {
		return nil, ErrUnavailable
	}
	accountByID := make(map[int64]sub2api.PoolAccount, len(accounts))
	for _, account := range accounts {
		accountByID[account.Account.ID] = account
	}
	channelByID := make(map[uint]storage.Channel, len(monitorChannels))
	for _, channel := range monitorChannels {
		channelByID[channel.ID] = channel
	}
	seen := make(map[int64]struct{}, len(inputs))
	rows := make([]KeyAttestation, 0, len(inputs))
	for _, input := range inputs {
		if input.AccountID <= 0 || input.ChannelID == 0 {
			return nil, ErrInvalidInput
		}
		if _, exists := seen[input.AccountID]; exists {
			return nil, ErrInvalidInput
		}
		seen[input.AccountID] = struct{}{}
		account, found := accountByID[input.AccountID]
		if !found || !strings.EqualFold(strings.TrimSpace(account.Account.Type), "apikey") {
			return nil, ErrInvalidInput
		}
		identity := account.Identity()
		channel, found := channelByID[input.ChannelID]
		if !found ||
			!channel.MonitorEnabled ||
			!identity.FingerprintSeen ||
			identity.APIKeySHA256 == "" ||
			normalizeURL(identity.BaseURL) == "" ||
			normalizeURL(identity.BaseURL) != normalizeURL(channel.SiteURL) {
			return nil, ErrInvalidInput
		}
		rows = append(rows, KeyAttestation{
			TargetID:     targetID,
			AccountID:    input.AccountID,
			APIKeySHA256: identity.APIKeySHA256,
			ChannelID:    input.ChannelID,
			Source:       "operator_attested",
		})
	}
	if err := s.keyAttestations.UpsertKeyAttestations(rows); err != nil {
		return nil, ErrUnavailable
	}
	return rows, nil
}
