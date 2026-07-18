package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

func sourceGroupKey(group connector.APIKeyGroup) string {
	if group.ID != nil {
		return "id:" + strconv.FormatInt(*group.ID, 10)
	}
	return "name:" + normalizeName(group.Name)
}

func normalizeName(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func uniquePositiveInt64s(ids []int64) ([]int64, error) {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, errors.New("target group ids must be positive")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func selectedTargetGroupNames(groups []sub2api.AdminGroup, selectedIDs []int64) ([]string, error) {
	byID := make(map[int64]sub2api.AdminGroup, len(groups))
	for _, group := range groups {
		byID[group.ID] = group
	}
	names := make([]string, 0, len(selectedIDs))
	for _, id := range selectedIDs {
		group, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("target group missing: %d", id)
		}
		status := strings.ToLower(strings.TrimSpace(group.Status))
		if status != "" && status != "active" {
			return nil, fmt.Errorf("target group is not active: %s", strings.TrimSpace(group.Name))
		}
		names = append(names, strings.TrimSpace(group.Name))
	}
	return names, nil
}

// readTargetGroups is intentionally a read-only remote operation. It updates
// only the local target-group cache so the approval form is validated against
// current Sub2 data rather than an old browser selection.
func (s *Service) readTargetGroups(ctx context.Context, targetID uint) ([]sub2api.AdminGroup, error) {
	target, err := s.targets.FindByID(targetID)
	if err != nil {
		return nil, err
	}
	if !target.Enabled {
		return nil, errors.New("target is disabled")
	}
	plain, err := s.cipher.Decrypt(target.AdminAPIKeyCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt target admin key: %w", err)
	}
	adminTarget := sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: plain}
	groups, err := sub2api.NewAdminClient().ListGroups(ctx, adminTarget, true)
	now := s.now()
	if err != nil {
		_ = s.targets.UpdateCheck(target.ID, "failed", &now, err.Error())
		return nil, fmt.Errorf("list target groups: %w", err)
	}
	_ = s.targets.UpdateCheck(target.ID, "ok", &now, "")
	if err := s.cacheTargetGroups(target.ID, groups); err != nil {
		return nil, err
	}
	return groups, nil
}

func (s *Service) loadTarget(targetID uint) (*storage.UpstreamSyncTarget, sub2api.AdminTarget, error) {
	target, err := s.targets.FindByID(targetID)
	if err != nil {
		return nil, sub2api.AdminTarget{}, err
	}
	if !target.Enabled {
		return nil, sub2api.AdminTarget{}, errors.New("target is disabled")
	}
	plain, err := s.cipher.Decrypt(target.AdminAPIKeyCipher)
	if err != nil {
		return nil, sub2api.AdminTarget{}, fmt.Errorf("decrypt target admin key: %w", err)
	}
	return target, sub2api.AdminTarget{BaseURL: target.BaseURL, APIKey: plain}, nil
}

func (s *Service) cacheTargetGroups(targetID uint, groups []sub2api.AdminGroup) error {
	if s.targetGroups == nil {
		return nil
	}
	now := s.now()
	seen := make([]int64, 0, len(groups))
	for _, group := range groups {
		seen = append(seen, group.ID)
		item, err := s.targetGroups.FindByTargetAndRemote(targetID, group.ID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			item = &storage.UpstreamSyncTargetGroup{TargetID: targetID, RemoteGroupID: group.ID}
		} else if err != nil {
			return err
		}
		item.Name = strings.TrimSpace(group.Name)
		item.Platform = strings.TrimSpace(group.Platform)
		item.Ratio = group.Ratio
		item.Status = strings.TrimSpace(group.Status)
		if item.Status == "" {
			item.Status = "active"
		}
		item.Sort = group.Sort
		item.Description = strings.TrimSpace(group.Description)
		item.LastSyncAt = &now
		if err := s.targetGroups.Upsert(item); err != nil {
			return err
		}
	}
	return s.targetGroups.DeleteMissing(targetID, seen)
}

func (s *Service) loadSourceGroup(ctx context.Context, item *storage.GroupDiscoveryCandidate) (*connector.APIKeyGroup, error) {
	groups, err := s.channelSvc.ListAPIKeyGroups(ctx, item.SourceChannelID)
	if err != nil {
		return nil, fmt.Errorf("list source groups: %w", err)
	}
	if item.SourceGroupID != nil {
		for i := range groups {
			if groups[i].ID != nil && *groups[i].ID == *item.SourceGroupID {
				return &groups[i], nil
			}
		}
		return nil, fmt.Errorf("source group missing: id %d", *item.SourceGroupID)
	}

	var matched *connector.APIKeyGroup
	for i := range groups {
		if normalizeName(groups[i].Name) != normalizeName(item.SourceGroupName) {
			continue
		}
		if matched != nil {
			return nil, fmt.Errorf("source group name is ambiguous: %s", item.SourceGroupName)
		}
		matched = &groups[i]
	}
	if matched == nil {
		return nil, fmt.Errorf("source group missing: %s", item.SourceGroupName)
	}
	return matched, nil
}

func discoveryAPIKeyName(candidateID uint) string {
	return fmt.Sprintf("uo-discovery-key-%d", candidateID)
}

func defaultAccountName(groupName string, candidateID uint) string {
	suffix := fmt.Sprintf(" [uo-%d]", candidateID)
	maxGroupRunes := maxTargetAccountNameRunes - len([]rune(suffix))
	groupRunes := []rune(strings.TrimSpace(groupName))
	if len(groupRunes) > maxGroupRunes {
		groupRunes = groupRunes[:maxGroupRunes]
	}
	return strings.TrimSpace(string(groupRunes)) + suffix
}

func validateTargetAccountName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("account_name is required")
	}
	if utf8.RuneCountInString(value) > maxTargetAccountNameRunes {
		return "", errors.New("account_name is too long")
	}
	return value, nil
}

// Legacy scans used the source group name directly. Only untouched pending
// candidates are safe to migrate to the stable, candidate-owned default.
func shouldSetDefaultAccountName(item *storage.GroupDiscoveryCandidate) bool {
	if item == nil || item.Status != statusPending || item.TargetID != nil ||
		item.SourceAPIKeyID != nil || item.TargetAccountID != nil ||
		item.SourceKeyCreateAttemptedAt != nil || item.TargetAccountCreateAttemptedAt != nil ||
		item.LastAttemptAt != nil || item.AppliedAt != nil {
		return false
	}
	accountName := strings.TrimSpace(item.AccountName)
	return accountName == "" ||
		accountName == strings.TrimSpace(item.SourceGroupName) ||
		accountName == defaultAccountName(item.SourceGroupName, item.ID)
}

func discoveryAccountMarker(candidateID uint) string {
	return fmt.Sprintf("Upstream Ops group discovery candidate:%d", candidateID)
}

func discoveryAccountNotes(candidateID uint) string {
	return discoveryAccountMarker(candidateID) + "\nManaged by Upstream Ops; retry from group discovery if this account is inactive."
}

func isDiscoveryAccount(item sub2api.AdminAccount, candidateID uint) bool {
	return strings.Contains(item.Notes, discoveryAccountMarker(candidateID))
}

func sourceKeyDefaults(channel *storage.Channel) (*bool, *int64) {
	if channel == nil || channel.Type != storage.ChannelTypeNewAPI {
		return nil, nil
	}
	unlimited := true
	neverExpire := int64(-1)
	return &unlimited, &neverExpire
}

func apiKeyByID(items []connector.APIKey, id int64) *connector.APIKey {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}

func apiKeysByName(items []connector.APIKey, name string) []*connector.APIKey {
	name = strings.TrimSpace(name)
	result := make([]*connector.APIKey, 0, 1)
	for i := range items {
		if strings.TrimSpace(items[i].Name) == name {
			result = append(result, &items[i])
		}
	}
	return result
}

func apiKeyMatchesSourceGroup(key *connector.APIKey, group *connector.APIKeyGroup) bool {
	if key == nil || group == nil {
		return false
	}
	if group.ID != nil && key.GroupID != nil && *group.ID == *key.GroupID {
		return true
	}
	groupName := normalizeName(group.Name)
	return groupName != "" && (normalizeName(key.Group) == groupName || normalizeName(key.GroupName) == groupName)
}

func modelMapping(models []string) map[string]string {
	mapping := make(map[string]string, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model != "" {
			mapping[model] = model
		}
	}
	return mapping
}

// sortedCandidates orders candidates by channel bucket, then ratio, with
// stable name/id tiebreakers. Callers receive the input slice back, sorted.
func sortedCandidates(items []storage.GroupDiscoveryCandidate) []storage.GroupDiscoveryCandidate {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].ChannelType != items[j].ChannelType {
			return items[i].ChannelType < items[j].ChannelType
		}
		if items[i].Ratio != items[j].Ratio {
			return items[i].Ratio < items[j].Ratio
		}
		if items[i].SourceChannelName != items[j].SourceChannelName {
			return items[i].SourceChannelName < items[j].SourceChannelName
		}
		if items[i].SourceGroupName != items[j].SourceGroupName {
			return items[i].SourceGroupName < items[j].SourceGroupName
		}
		return items[i].ID < items[j].ID
	})
	return items
}

// filterTopNPerChannel keeps, per channel bucket, the candidates with the
// lowest source ratio. The cutoff widens past topN so every candidate tied at
// the boundary ratio is kept. topN <= 0 keeps everything.
func filterTopNPerChannel(items []storage.GroupDiscoveryCandidate, topN int) []storage.GroupDiscoveryCandidate {
	if topN <= 0 || len(items) == 0 {
		return items
	}
	sortedCandidates(items)
	out := make([]storage.GroupDiscoveryCandidate, 0, len(items))
	keptInChannel := 0
	var cutoffRatio float64
	for index, item := range items {
		if index == 0 || item.ChannelType != items[index-1].ChannelType {
			keptInChannel = 0
			cutoffRatio = 0
		}
		if keptInChannel < topN {
			out = append(out, item)
			keptInChannel++
			if keptInChannel == topN {
				cutoffRatio = item.Ratio
			}
			continue
		}
		if item.Ratio == cutoffRatio {
			out = append(out, item)
		}
	}
	return out
}
