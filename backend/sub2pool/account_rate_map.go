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
	channelID     uint
	normalizedURL string
	channelName   string
	modelName     string
	ratio         float64
	active        bool
}

var accountLabelRatePattern = regexp.MustCompile(
	`(?i)(pro|plus|cc|kiro|gemini|grok|g|图|生图)\s*([0-9]+(?:\s*\.\s*[0-9]+)?)`,
)
var accountDiscountRatePattern = regexp.MustCompile(`(?i)([0-9]+(?:\s*\.\s*[0-9]+)?)\s*折`)
var accountCentRatePattern = regexp.MustCompile(`(?i)([0-9]+(?:\s*\.\s*[0-9]+)?)\s*分`)
var accountLabelTokenPattern = regexp.MustCompile(`[\p{L}\p{Han}]+`)

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
	return resolveAccountRateMappingsWithGroups(
		ctx,
		targetID,
		accounts,
		nil,
		store,
		channels,
		rates,
	)
}

func resolveAccountRateMappingsWithGroups(
	ctx context.Context,
	targetID uint,
	accounts []sub2api.PoolAccount,
	groups []sub2api.AdminGroup,
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
		accountURL[account.Account.ID] = normalizeMappingURL(account.Identity().BaseURL)
	}
	monitorChannels, err := channels.List()
	if err != nil {
		return out
	}
	observedRates := make([]observedAccountRate, 0)
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
			observedRates = append(
				observedRates,
				observedAccountRate{
					channelID:     channel.ID,
					normalizedURL: normalized,
					channelName:   channel.Name,
					modelName:     modelName,
					ratio:         snapshot.Ratio,
					active:        channel.MonitorEnabled,
				},
			)
		}
	}
	for _, mapping := range mappings {
		if err := ctx.Err(); err != nil {
			return out
		}
		normalized := normalizeMappingURL(mapping.SiteURL)
		if normalized == "" || !mappingURLsEqual(normalized, accountURL[mapping.AccountID]) {
			continue
		}
		modelName := normalizeModelName(mapping.ModelName)
		if modelName == "" {
			continue
		}

		candidates := preferActiveObservedRates(
			observedRatesForURL(observedRates, normalized),
		)
		values := make([]float64, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.modelName == modelName {
				values = append(values, candidate.ratio)
			}
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
		normalized := normalizeMappingURL(account.Identity().BaseURL)
		nameCandidates := observedRatesForExactChannelName(account.Account.Name, observedRates)
		if urlCandidates := observedRatesForURL(observedRates, normalized); len(urlCandidates) > 0 {
			if narrowed := intersectObservedRates(nameCandidates, urlCandidates); len(narrowed) > 0 {
				nameCandidates = narrowed
			}
		}
		if rate, ok := resolveObservedAccountRate(
			account.Account.Name,
			nameCandidates,
			len(nameCandidates) > 0,
		); ok {
			out[account.Account.ID] = rate
			continue
		}

		identityCandidates := observedRatesForIdentity(account.Account.Name, observedRates)
		if urlCandidates := observedRatesForURL(observedRates, normalized); len(urlCandidates) > 0 {
			if narrowed := intersectObservedRates(identityCandidates, urlCandidates); len(narrowed) > 0 {
				identityCandidates = narrowed
			}
		}
		if rate, ok := resolveObservedAccountRate(account.Account.Name, identityCandidates, false); ok {
			out[account.Account.ID] = rate
			continue
		}

		if rate, ok := resolveObservedAccountRate(
			account.Account.Name,
			observedRatesForURL(observedRates, normalized),
			false,
		); ok {
			out[account.Account.ID] = rate
			continue
		}

		// Keep a numeric account/group label visible while the monitor channel
		// is unavailable. These values remain display-only and cannot drive
		// automatic priority changes.
		if hint, ok := accountLabelRateHint(account.Account.Name); ok {
			out[account.Account.ID] = hint
			continue
		}
		if hint, ok := accountGroupRateHint(account, groups); ok {
			out[account.Account.ID] = hint
		}
	}
	return out
}

func resolveObservedAccountRate(
	accountName string,
	candidates []observedAccountRate,
	exactLabel bool,
) (float64, bool) {
	if strings.TrimSpace(accountName) == "" || len(candidates) == 0 {
		return 0, false
	}
	candidates = preferActiveObservedRates(candidates)

	if hint, ok := accountLabelRateHint(accountName); ok {
		rateMatched := make([]observedAccountRate, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.ratio == hint {
				rateMatched = append(rateMatched, candidate)
			}
		}
		if rate, unique := uniqueObservedRate(rateMatched); unique {
			return rate, true
		}
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
		if exactLabel {
			return hint, true
		}
	}

	return uniqueObservedRate(candidates)
}

func observedRatesForExactChannelName(
	accountName string,
	candidates []observedAccountRate,
) []observedAccountRate {
	accountLabel := normalizeLabel(accountName)
	if accountLabel == "" {
		return nil
	}
	out := make([]observedAccountRate, 0)
	for _, candidate := range candidates {
		for _, part := range strings.Split(candidate.channelName, "/") {
			if accountLabel == normalizeLabel(part) {
				out = append(out, candidate)
				break
			}
		}
	}
	return out
}

func observedRatesForIdentity(
	accountName string,
	candidates []observedAccountRate,
) []observedAccountRate {
	identityTokens := labelIdentityTokens(accountName)
	if len(identityTokens) == 0 {
		return nil
	}
	channelIDs := make(map[uint]struct{})
	for _, candidate := range candidates {
		for _, part := range strings.Split(candidate.channelName, "/") {
			if sharesIdentityToken(identityTokens, labelIdentityTokens(part)) {
				channelIDs[candidate.channelID] = struct{}{}
				break
			}
		}
	}
	if len(channelIDs) != 1 {
		return nil
	}
	out := make([]observedAccountRate, 0)
	for _, candidate := range candidates {
		if _, ok := channelIDs[candidate.channelID]; ok {
			out = append(out, candidate)
		}
	}
	return out
}

func intersectObservedRates(
	left []observedAccountRate,
	right []observedAccountRate,
) []observedAccountRate {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	channelIDs := make(map[uint]struct{})
	for _, candidate := range right {
		channelIDs[candidate.channelID] = struct{}{}
	}
	out := make([]observedAccountRate, 0)
	for _, candidate := range left {
		if _, ok := channelIDs[candidate.channelID]; ok {
			out = append(out, candidate)
		}
	}
	return out
}

func preferActiveObservedRates(candidates []observedAccountRate) []observedAccountRate {
	active := make([]observedAccountRate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.active {
			active = append(active, candidate)
		}
	}
	if len(active) > 0 {
		return active
	}
	return candidates
}

func observedRatesForURL(
	candidates []observedAccountRate,
	normalizedURL string,
) []observedAccountRate {
	if normalizedURL == "" {
		return nil
	}
	out := make([]observedAccountRate, 0)
	for _, candidate := range candidates {
		if mappingURLsEqual(normalizedURL, candidate.normalizedURL) {
			out = append(out, candidate)
		}
	}
	return out
}

func labelIdentityTokens(value string) []string {
	ignored := map[string]struct{}{
		"api": {}, "cc": {}, "gemini": {}, "grok": {}, "gpt": {},
		"image": {}, "kiro": {}, "openai": {}, "plus": {}, "pro": {},
		"claude": {}, "生图": {}, "专用": {}, "渠道": {}, "号池": {},
		"高缓存": {}, "高缓": {}, "特惠": {}, "推荐": {}, "纯血": {},
		"官方": {}, "自用": {}, "限时": {}, "混池": {},
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, token := range accountLabelTokenPattern.FindAllString(
		strings.ToLower(value),
		-1,
	) {
		if _, skip := ignored[token]; skip || len([]rune(token)) < 2 {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func sharesIdentityToken(left, right []string) bool {
	for _, l := range left {
		for _, r := range right {
			if l == r {
				return true
			}
		}
	}
	return false
}

func normalizeLabel(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func normalizeMappingURL(value string) string {
	normalized := normalizeURL(value)
	if normalized == "" {
		return ""
	}
	parts := strings.SplitN(normalized, "://", 2)
	if len(parts) != 2 {
		return normalized
	}
	return parts[0] + "://" + strings.TrimPrefix(parts[1], "api.")
}

func mappingURLsEqual(left, right string) bool {
	left = normalizeMappingURL(left)
	right = normalizeMappingURL(right)
	return left != "" && left == right
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
	if len(match) == 3 {
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
	if match := accountDiscountRatePattern.FindStringSubmatch(value); len(match) == 2 {
		rate, err := strconv.ParseFloat(strings.ReplaceAll(match[1], " ", ""), 64)
		return rate / 10, err == nil && rate > 0 && rate <= 100
	}
	if match := accountCentRatePattern.FindStringSubmatch(value); len(match) == 2 {
		rate, err := strconv.ParseFloat(strings.ReplaceAll(match[1], " ", ""), 64)
		return rate / 100, err == nil && rate > 0 && rate <= 100
	}
	return 0, false
}

func accountGroupRateHint(
	account sub2api.PoolAccount,
	groups []sub2api.AdminGroup,
) (float64, bool) {
	if len(account.Account.GroupIDs) == 0 || len(groups) == 0 {
		return 0, false
	}
	groupIDs := make(map[int64]struct{}, len(account.Account.GroupIDs))
	for _, id := range account.Account.GroupIDs {
		groupIDs[id] = struct{}{}
	}
	hints := make([]float64, 0)
	for _, group := range groups {
		if _, ok := groupIDs[group.ID]; !ok {
			continue
		}
		if hint, ok := accountLabelRateHint(group.Name); ok {
			hints = append(hints, hint)
		}
	}
	return uniqueFloatRate(hints)
}

func uniqueFloatRate(values []float64) (float64, bool) {
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
