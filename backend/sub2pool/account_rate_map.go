package sub2pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GormPoolAccountRateMapping struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	TargetID   uint      `gorm:"not null;uniqueIndex:idx_sub2_pool_rate_mapping" json:"target_id"`
	AccountID  int64     `gorm:"not null;uniqueIndex:idx_sub2_pool_rate_mapping" json:"account_id"`
	SiteURL    string    `gorm:"size:512;not null" json:"site_url"`
	ModelName  string    `gorm:"size:256;not null" json:"model_name"`
	ManualRate *float64  `json:"manual_rate,omitempty"`
	Enabled    bool      `gorm:"not null;default:true" json:"enabled"`
	Source     string    `gorm:"size:64;not null;default:'manual'" json:"source"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (GormPoolAccountRateMapping) TableName() string {
	return "sub2_pool_account_rate_mappings"
}

type accountRateMapFile struct {
	Accounts map[string]accountRateMapEntry `json:"accounts"`
}

type accountRateMapEntry struct {
	SiteURL string   `json:"site_url"`
	Model   string   `json:"model"`
	Rate    *float64 `json:"rate"`
}

func (s *GormStateStore) ListAccountRateMappings(targetID uint) ([]AccountRateMapping, error) {
	var rows []GormPoolAccountRateMapping
	if err := s.db.
		Where("target_id = ? AND enabled = ?", targetID, true).
		Order("account_id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]AccountRateMapping, 0, len(rows))
	for _, row := range rows {
		out = append(out, AccountRateMapping{
			TargetID:   row.TargetID,
			AccountID:  row.AccountID,
			SiteURL:    row.SiteURL,
			ModelName:  row.ModelName,
			ManualRate: cloneFloat(row.ManualRate),
			Enabled:    row.Enabled,
		})
	}
	return out, nil
}

// ImportAccountRateMappingsIfEmpty imports a legacy account-ID map exactly
// once. Existing database rows are authoritative after the first import.
func (s *GormStateStore) ImportAccountRateMappingsIfEmpty(targetID uint, path string) (int, error) {
	if targetID == 0 {
		return 0, errors.New("account rate map target id is required")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, nil
	}

	var existing int64
	if err := s.db.Model(&GormPoolAccountRateMapping{}).
		Where("target_id = ?", targetID).
		Count(&existing).Error; err != nil {
		return 0, err
	}
	if existing > 0 {
		return 0, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read account rate map import: %w", err)
	}
	var data accountRateMapFile
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, fmt.Errorf("decode account rate map import: %w", err)
	}
	if len(data.Accounts) == 0 {
		return 0, errors.New("account rate map import contains no accounts")
	}

	rows := make([]GormPoolAccountRateMapping, 0, len(data.Accounts))
	for rawID, entry := range data.Accounts {
		accountID, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		if err != nil || accountID <= 0 {
			return 0, fmt.Errorf("invalid account id in account rate map import")
		}
		siteURL := normalizeURL(entry.SiteURL)
		model := strings.TrimSpace(entry.Model)
		if siteURL == "" || model == "" {
			return 0, fmt.Errorf("account rate map import entry %d requires site_url and model", accountID)
		}
		rows = append(rows, GormPoolAccountRateMapping{
			TargetID:   targetID,
			AccountID:  accountID,
			SiteURL:    siteURL,
			ModelName:  model,
			ManualRate: cloneFloat(entry.Rate),
			Enabled:    true,
			Source:     "legacy_import",
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].AccountID < rows[j].AccountID })
	var inserted int64
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "target_id"}, {Name: "account_id"}},
			DoNothing: true,
		}).Create(&rows)
		inserted = result.RowsAffected
		return result.Error
	}); err != nil {
		return 0, err
	}
	return int(inserted), nil
}

func resolveAccountRateMappings(
	ctx context.Context,
	targetID uint,
	accounts []sub2api.PoolAccount,
	store AccountRateMappingStore,
	channels ChannelStore,
	rates RateSnapshotStore,
) map[int64]float64 {
	out := make(map[int64]float64)
	if store == nil || channels == nil || rates == nil {
		return out
	}
	mappings, err := store.ListAccountRateMappings(targetID)
	if err != nil || len(mappings) == 0 {
		return out
	}
	accountURL := make(map[int64]string, len(accounts))
	for _, account := range accounts {
		accountURL[account.Account.ID] = normalizeURL(account.Identity().BaseURL)
	}
	monitorChannels, err := channels.List()
	if err != nil {
		return out
	}
	ratesByURL := make(map[string]map[string][]float64)
	for _, channel := range monitorChannels {
		if !channel.MonitorEnabled {
			continue
		}
		normalized := normalizeURL(channel.SiteURL)
		if normalized == "" {
			continue
		}
		snapshots, err := rates.ListByChannel(channel.ID)
		if err != nil {
			continue
		}
		if ratesByURL[normalized] == nil {
			ratesByURL[normalized] = make(map[string][]float64)
		}
		for _, snapshot := range snapshots {
			modelName := normalizeModelName(snapshot.ModelName)
			if modelName == "" {
				continue
			}
			ratesByURL[normalized][modelName] = append(
				ratesByURL[normalized][modelName],
				snapshot.Ratio,
			)
		}
	}
	for _, mapping := range mappings {
		if err := ctx.Err(); err != nil {
			return out
		}
		normalized := normalizeURL(mapping.SiteURL)
		if normalized == "" || normalized != accountURL[mapping.AccountID] {
			continue
		}
		modelName := normalizeModelName(mapping.ModelName)
		if modelName == "" {
			continue
		}

		if rate, unique := uniqueMappedRate(ratesByURL[normalized][modelName]); unique {
			out[mapping.AccountID] = rate
		} else if mapping.ManualRate != nil {
			out[mapping.AccountID] = *mapping.ManualRate
		}
	}
	return out
}

func normalizeModelName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func uniqueMappedRate(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	first := values[0]
	for _, value := range values[1:] {
		if value != first {
			return 0, false
		}
	}
	return first, true
}
