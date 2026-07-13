package sub2pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
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

type observedAccountRate struct {
	channelName string
	modelName   string
	ratio       float64
	active      bool
}

var accountLabelRatePattern = regexp.MustCompile(
	`(?i)(pro|plus|cc|kiro|gemini|grok|g|图|生图)\s*([0-9]+(?:\s*\.\s*[0-9]+)?)`,
)

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
	if err != nil {
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
	activeRatesByURL := make(map[string]map[string][]float64)
	allRatesByURL := make(map[string]map[string][]float64)
	observedRatesByURL := make(map[string][]observedAccountRate)
	for _, channel := range monitorChannels {
		normalized := normalizeURL(channel.SiteURL)
		if normalized == "" {
			continue
		}
		snapshots, err := rates.ListByChannel(channel.ID)
		if err != nil {
			continue
		}
		for _, snapshot := range snapshots {
			modelName := normalizeModelName(snapshot.ModelName)
			if modelName == "" {
				continue
			}
			if allRatesByURL[normalized] == nil {
				allRatesByURL[normalized] = make(map[string][]float64)
			}
			observedRatesByURL[normalized] = append(
				observedRatesByURL[normalized],
				observedAccountRate{
					channelName: channel.Name,
					modelName:   modelName,
					ratio:       snapshot.Ratio,
					active:      channel.MonitorEnabled,
				},
			)
			allRatesByURL[normalized][modelName] = append(
				allRatesByURL[normalized][modelName],
				snapshot.Ratio,
			)
			if channel.MonitorEnabled {
				if activeRatesByURL[normalized] == nil {
					activeRatesByURL[normalized] = make(map[string][]float64)
				}
				activeRatesByURL[normalized][modelName] = append(
					activeRatesByURL[normalized][modelName],
					snapshot.Ratio,
				)
			}
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

		values := activeRatesByURL[normalized][modelName]
		if len(values) == 0 {
			// A disabled duplicate may still hold the last useful group
			// snapshot. Prefer live channels, but keep this as a fallback.
			values = allRatesByURL[normalized][modelName]
		}
		if rate, unique := uniqueMappedRate(values); unique {
			out[mapping.AccountID] = rate
		} else if mapping.ManualRate != nil {
			out[mapping.AccountID] = *mapping.ManualRate
		}
	}
	for _, account := range accounts {
		if _, exists := out[account.Account.ID]; exists {
			continue
		}
		normalized := accountURL[account.Account.ID]
		if rate, ok := resolveAccountLabelRate(account.Account.Name, observedRatesByURL[normalized]); ok {
			out[account.Account.ID] = rate
		}
	}
	return out
}

func resolveAccountLabelRate(accountName string, candidates []observedAccountRate) (float64, bool) {
	if strings.TrimSpace(accountName) == "" || len(candidates) == 0 {
		return 0, false
	}
	active := make([]observedAccountRate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.active {
			active = append(active, candidate)
		}
	}
	if len(active) > 0 {
		candidates = active
	}

	accountLabel := normalizeLabel(accountName)
	channelMatched := make([]observedAccountRate, 0, len(candidates))
	for _, candidate := range candidates {
		for _, part := range strings.Split(candidate.channelName, "/") {
			if accountLabel != "" && accountLabel == normalizeLabel(part) {
				channelMatched = append(channelMatched, candidate)
				break
			}
		}
	}
	if len(channelMatched) > 0 {
		candidates = channelMatched
	}

	if category := accountLabelCategory(accountName); category != "" {
		categoryMatched := make([]observedAccountRate, 0, len(candidates))
		for _, candidate := range candidates {
			if modelMatchesAccountCategory(candidate.modelName, category) {
				categoryMatched = append(categoryMatched, candidate)
			}
		}
		if len(categoryMatched) > 0 {
			candidates = categoryMatched
		}
	}

	if hint, ok := accountLabelRateHint(accountName); ok {
		rateMatched := make([]observedAccountRate, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.ratio == hint {
				rateMatched = append(rateMatched, candidate)
			}
		}
		if len(rateMatched) == 0 {
			return 0, false
		}
		candidates = rateMatched
	}

	return uniqueObservedRate(candidates)
}

func normalizeLabel(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func accountLabelCategory(value string) string {
	label := normalizeLabel(value)
	switch {
	case strings.Contains(label, "图") || strings.Contains(label, "生图") || strings.Contains(label, "image"):
		return "image"
	case strings.Contains(label, "gemini"):
		return "gemini"
	case strings.Contains(label, "grok"):
		return "grok"
	case strings.Contains(label, "kiro"):
		return "kiro"
	case strings.Contains(label, "plus"):
		return "plus"
	case strings.Contains(label, "pro"):
		return "pro"
	case strings.Contains(label, "cc"):
		return "cc"
	case strings.Contains(label, "glm") || strings.Contains(label, "国模"):
		return "cn"
	case strings.HasPrefix(label, "g "):
		return "gemini"
	default:
		return ""
	}
}

func modelMatchesAccountCategory(model, category string) bool {
	label := normalizeLabel(model)
	switch category {
	case "image":
		return strings.Contains(label, "image") ||
			strings.Contains(label, "生图") ||
			strings.Contains(label, "绘画")
	case "gemini":
		return strings.Contains(label, "gemini")
	case "grok":
		return strings.Contains(label, "grok")
	case "kiro":
		return strings.Contains(label, "kiro")
	case "plus":
		return strings.Contains(label, "plus") || strings.Contains(label, "chatgpt")
	case "pro":
		return strings.Contains(label, "pro")
	case "cc":
		return strings.Contains(label, "cc") || strings.Contains(label, "claude")
	case "cn":
		return strings.Contains(label, "glm") ||
			strings.Contains(label, "国产") ||
			strings.Contains(label, "国模")
	default:
		return false
	}
}

func accountLabelRateHint(value string) (float64, bool) {
	match := accountLabelRatePattern.FindStringSubmatch(value)
	if len(match) != 3 {
		return 0, false
	}
	raw := strings.ReplaceAll(strings.TrimSpace(match[2]), " ", "")
	if strings.Contains(raw, ".") {
		rate, err := strconv.ParseFloat(raw, 64)
		return rate, err == nil && rate >= 0 && rate <= 10
	}
	if strings.HasPrefix(raw, "0") && len(raw) > 1 {
		value, err := strconv.Atoi(raw)
		return float64(value) / 100, err == nil && value >= 0 && value <= 100
	}
	rate, err := strconv.ParseFloat(raw, 64)
	return rate, err == nil && rate >= 0 && rate <= 10
}

func uniqueObservedRate(values []observedAccountRate) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	first := values[0].ratio
	for _, value := range values[1:] {
		if value.ratio != first {
			return 0, false
		}
	}
	return first, true
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
