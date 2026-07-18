// Package discovery implements the read -> review -> explicit apply workflow
// for upstream API-key groups. It deliberately stays separate from the bulk
// syncer because applying one reviewed item must not clean up other accounts.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

const (
	statusPending  = "pending"
	statusApproved = "approved"
	statusRejected = "rejected"
	statusApplying = "applying"
	statusApplied  = "applied"
	statusFailed   = "failed"

	maxTargetAccountNameRunes = 100
)

type channelService interface {
	ListAPIKeyGroups(ctx context.Context, channelID uint) ([]connector.APIKeyGroup, error)
	ListAPIKeys(ctx context.Context, channelID uint, query connector.APIKeyQuery) (*connector.APIKeyPage, error)
	CreateAPIKey(ctx context.Context, channelID uint, req connector.APIKeyCreateRequest) (*connector.APIKey, error)
	UpdateAPIKey(ctx context.Context, channelID uint, keyID int64, req connector.APIKeyUpdateRequest) (*connector.APIKey, error)
	RevealAPIKey(ctx context.Context, channelID uint, keyID int64) (string, error)
}

type cipher interface {
	Decrypt(ciphertext string) (string, error)
}

// Service owns discovery candidates and their explicitly-created remote
// objects. It serializes mutations in-process so a second request cannot race a
// first request between recording an attempt and receiving its remote response.
type Service struct {
	channels     *storage.Channels
	candidates   *storage.GroupDiscoveryCandidates
	targets      *storage.UpstreamSyncTargets
	targetGroups *storage.UpstreamSyncTargetGroups
	cipher       cipher
	channelSvc   channelService
	now          func() time.Time

	opMu sync.Mutex
}

func New(
	channels *storage.Channels,
	candidates *storage.GroupDiscoveryCandidates,
	targets *storage.UpstreamSyncTargets,
	targetGroups *storage.UpstreamSyncTargetGroups,
	cipher cipher,
	channelSvc channelService,
) *Service {
	return &Service{
		channels:     channels,
		candidates:   candidates,
		targets:      targets,
		targetGroups: targetGroups,
		cipher:       cipher,
		channelSvc:   channelSvc,
		now:          time.Now,
	}
}

type CandidateDTO struct {
	ID                     uint       `json:"id"`
	SourceChannelID        uint       `json:"source_channel_id"`
	SourceChannelName      string     `json:"source_channel_name"`
	SourceGroupID          *int64     `json:"source_group_id,omitempty"`
	SourceGroupName        string     `json:"source_group_name"`
	SourceGroupDescription string     `json:"source_group_description,omitempty"`
	Ratio                  float64    `json:"ratio"`
	Status                 string     `json:"status"`
	TargetID               *uint      `json:"target_id,omitempty"`
	TargetGroupIDs         []int64    `json:"target_group_ids"`
	TargetGroupNames       []string   `json:"target_group_names"`
	Platform               string     `json:"platform"`
	AccountName            string     `json:"account_name"`
	Concurrency            int        `json:"concurrency"`
	Weight                 int        `json:"weight"`
	SourceAPIKeyID         *int64     `json:"source_api_key_id,omitempty"`
	SourceAPIKeyName       string     `json:"source_api_key_name,omitempty"`
	TargetAccountID        *int64     `json:"target_account_id,omitempty"`
	TargetAccountName      string     `json:"target_account_name,omitempty"`
	ApplyError             string     `json:"apply_error,omitempty"`
	LastAttemptAt          *time.Time `json:"last_attempt_at,omitempty"`
	AppliedAt              *time.Time `json:"applied_at,omitempty"`
	DiscoveredAt           time.Time  `json:"discovered_at"`
	LastSeenAt             time.Time  `json:"last_seen_at"`
}

type ScanResult struct {
	TotalChannels     int         `json:"total_channels"`
	ScannedChannels   int         `json:"scanned_channels"`
	NewCandidates     int         `json:"new_candidates"`
	UpdatedCandidates int         `json:"updated_candidates"`
	Errors            []ScanError `json:"errors,omitempty"`
}

type ScanError struct {
	ChannelID   uint   `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	Error       string `json:"error"`
}

type ApprovalInput struct {
	TargetID       uint    `json:"target_id"`
	TargetGroupIDs []int64 `json:"target_group_ids"`
	AccountName    string  `json:"account_name"`
	Platform       string  `json:"platform"`
	Concurrency    int     `json:"concurrency"`
	Weight         int     `json:"weight"`
}

type ApplyResult struct {
	Requested int               `json:"requested"`
	Applied   int               `json:"applied"`
	Failed    int               `json:"failed"`
	Items     []ApplyItemResult `json:"items"`
}

type ApplyItemResult struct {
	ID     uint   `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Scan reads monitor-enabled source channels only. It never creates an API key,
// changes a target group, or touches a target Sub2 account.
func (s *Service) Scan(ctx context.Context) (*ScanResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	channels, err := s.channels.ListMonitorEnabled()
	if err != nil {
		return nil, fmt.Errorf("list monitor-enabled channels: %w", err)
	}
	result := &ScanResult{TotalChannels: len(channels)}
	now := s.now()
	for _, channel := range channels {
		groups, err := s.channelSvc.ListAPIKeyGroups(ctx, channel.ID)
		if err != nil {
			result.Errors = append(result.Errors, ScanError{
				ChannelID: channel.ID, ChannelName: channel.Name, Error: err.Error(),
			})
			continue
		}
		result.ScannedChannels++
		seen := make(map[string]struct{}, len(groups))
		for _, group := range groups {
			name := strings.TrimSpace(group.Name)
			if name == "" {
				result.Errors = append(result.Errors, ScanError{
					ChannelID: channel.ID, ChannelName: channel.Name,
					Error: "upstream returned a group with an empty name",
				})
				continue
			}
			key := sourceGroupKey(group)
			if _, duplicate := seen[key]; duplicate {
				result.Errors = append(result.Errors, ScanError{
					ChannelID: channel.ID, ChannelName: channel.Name,
					Error: fmt.Sprintf("upstream returned duplicate group identity %q", key),
				})
				continue
			}
			seen[key] = struct{}{}
			item := &storage.GroupDiscoveryCandidate{
				SourceChannelID:        channel.ID,
				SourceChannelName:      channel.Name,
				SourceGroupKey:         key,
				SourceGroupID:          group.ID,
				SourceGroupName:        name,
				SourceGroupDescription: strings.TrimSpace(group.Description),
				Ratio:                  group.Ratio,
				Status:                 statusPending,
				Platform:               "openai",
				Concurrency:            10,
				Weight:                 1,
				DiscoveredAt:           now,
				LastSeenAt:             now,
			}
			setDefaultAccountName := true
			previous, findErr := s.candidates.FindBySource(channel.ID, key)
			if findErr == nil {
				setDefaultAccountName = shouldSetDefaultAccountName(previous)
			} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
				result.Errors = append(result.Errors, ScanError{
					ChannelID: channel.ID, ChannelName: channel.Name, Error: findErr.Error(),
				})
				continue
			}
			stored, created, err := s.candidates.UpsertScanned(item)
			if err != nil {
				result.Errors = append(result.Errors, ScanError{
					ChannelID: channel.ID, ChannelName: channel.Name, Error: err.Error(),
				})
				continue
			}
			if setDefaultAccountName {
				stored.AccountName = defaultAccountName(stored.SourceGroupName, stored.ID)
				if err := s.candidates.Update(stored); err != nil {
					return nil, fmt.Errorf("set candidate account name: %w", err)
				}
			}
			if created {
				result.NewCandidates++
				continue
			}
			result.UpdatedCandidates++
		}
	}
	if err := s.migrateLegacyAccountNames(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) migrateLegacyAccountNames() error {
	items, err := s.candidates.List()
	if err != nil {
		return fmt.Errorf("list candidates for account name migration: %w", err)
	}
	for i := range items {
		if !shouldSetDefaultAccountName(&items[i]) {
			continue
		}
		items[i].AccountName = defaultAccountName(items[i].SourceGroupName, items[i].ID)
		if err := s.candidates.Update(&items[i]); err != nil {
			return fmt.Errorf("migrate candidate %d account name: %w", items[i].ID, err)
		}
	}
	return nil
}

func (s *Service) List() ([]CandidateDTO, error) {
	items, err := s.candidates.List()
	if err != nil {
		return nil, err
	}
	out := make([]CandidateDTO, 0, len(items))
	for i := range items {
		dto, err := s.toDTO(&items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, dto)
	}
	return out, nil
}

// Approve persists a human-selected target mapping. It validates the selected
// remote target groups before any API key or account can be created later.
func (s *Service) Approve(ctx context.Context, id uint, in ApprovalInput) (*CandidateDTO, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	item, err := s.candidates.FindByID(id)
	if err != nil {
		return nil, err
	}
	if in.TargetID == 0 {
		return nil, errors.New("target_id is required")
	}
	if (item.TargetAccountID != nil || item.TargetAccountCreateAttemptedAt != nil) && (item.TargetID == nil || *item.TargetID != in.TargetID) {
		return nil, errors.New("a target account may already exist; changing its target would leave an unmanaged remote account")
	}
	groupIDs, err := uniquePositiveInt64s(in.TargetGroupIDs)
	if err != nil {
		return nil, err
	}
	if len(groupIDs) == 0 {
		return nil, errors.New("select at least one target group")
	}
	accountName := strings.TrimSpace(in.AccountName)
	if accountName == "" {
		accountName = strings.TrimSpace(item.AccountName)
	}
	if accountName == "" {
		accountName = defaultAccountName(item.SourceGroupName, item.ID)
	}
	accountName, err = validateTargetAccountName(accountName)
	if err != nil {
		return nil, err
	}
	if item.TargetAccountID == nil && item.TargetAccountCreateAttemptedAt != nil && item.TargetAccountName != "" && item.TargetAccountName != accountName {
		return nil, errors.New("target account creation outcome is unresolved; keep the original account name until it is reconciled")
	}
	platform := strings.ToLower(strings.TrimSpace(in.Platform))
	if platform == "" {
		platform = "openai"
	}
	if len(platform) > 64 || strings.ContainsAny(platform, " \t\r\n") {
		return nil, errors.New("platform is invalid")
	}
	if in.Concurrency < 0 || in.Weight < 0 {
		return nil, errors.New("concurrency and weight cannot be negative")
	}
	concurrency := in.Concurrency
	if concurrency == 0 {
		concurrency = 10
	}
	weight := in.Weight
	if weight == 0 {
		weight = 1
	}

	groups, err := s.readTargetGroups(ctx, in.TargetID)
	if err != nil {
		return nil, err
	}
	groupNames, err := selectedTargetGroupNames(groups, groupIDs)
	if err != nil {
		return nil, err
	}
	item.TargetID = &in.TargetID
	item.TargetGroupIDsJSON = storage.MarshalInt64Array(groupIDs)
	item.TargetGroupNamesJSON = storage.MarshalStringArray(groupNames)
	item.AccountName = accountName
	item.Platform = platform
	item.Concurrency = concurrency
	item.Weight = weight
	item.Status = statusApproved
	item.ApplyError = ""
	item.AppliedAt = nil
	if err := s.candidates.Update(item); err != nil {
		return nil, err
	}
	dto, err := s.toDTO(item)
	if err != nil {
		return nil, err
	}
	return &dto, nil
}

// Reject is intentionally blocked once a remote object may exist: hiding a
// partially applied candidate would make its key or target account orphaned.
func (s *Service) Reject(id uint) (*CandidateDTO, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	item, err := s.candidates.FindByID(id)
	if err != nil {
		return nil, err
	}
	if item.SourceAPIKeyID != nil || item.TargetAccountID != nil || item.SourceKeyCreateAttemptedAt != nil || item.TargetAccountCreateAttemptedAt != nil {
		return nil, errors.New("remote objects may exist; retry or manage them explicitly before rejecting")
	}
	if item.Status == statusApplied {
		return nil, errors.New("an applied candidate cannot be rejected without deleting its remote account")
	}
	item.Status = statusRejected
	item.ApplyError = ""
	if err := s.candidates.Update(item); err != nil {
		return nil, err
	}
	dto, err := s.toDTO(item)
	if err != nil {
		return nil, err
	}
	return &dto, nil
}

// Apply processes explicitly supplied candidates. An empty id list is the
// explicit "apply all approved/retryable" action used by the settings page.
func (s *Service) Apply(ctx context.Context, ids []uint) (*ApplyResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	items, err := s.applyCandidates(ids)
	if err != nil {
		return nil, err
	}
	result := &ApplyResult{Requested: len(items), Items: make([]ApplyItemResult, 0, len(items))}
	for _, item := range items {
		attemptedAt := s.now()
		if err := s.candidates.UpdateApplyState(item.ID, statusApplying, "", &attemptedAt, nil); err != nil {
			return nil, err
		}
		item.Status = statusApplying
		item.ApplyError = ""
		item.LastAttemptAt = &attemptedAt
		if err := s.applyOne(ctx, item); err != nil {
			if stateErr := s.candidates.UpdateApplyState(item.ID, statusFailed, err.Error(), &attemptedAt, nil); stateErr != nil {
				return nil, fmt.Errorf("apply candidate %d: %v; persist failure: %w", item.ID, err, stateErr)
			}
			result.Failed++
			result.Items = append(result.Items, ApplyItemResult{ID: item.ID, Status: statusFailed, Error: err.Error()})
			continue
		}
		appliedAt := s.now()
		if err := s.candidates.UpdateApplyState(item.ID, statusApplied, "", &attemptedAt, &appliedAt); err != nil {
			return nil, err
		}
		result.Applied++
		result.Items = append(result.Items, ApplyItemResult{ID: item.ID, Status: statusApplied})
	}
	return result, nil
}

func (s *Service) applyCandidates(ids []uint) ([]*storage.GroupDiscoveryCandidate, error) {
	if len(ids) == 0 {
		all, err := s.candidates.List()
		if err != nil {
			return nil, err
		}
		out := make([]*storage.GroupDiscoveryCandidate, 0, len(all))
		for i := range all {
			if all[i].Status == statusApproved || all[i].Status == statusFailed || all[i].Status == statusApplying {
				item := all[i]
				out = append(out, &item)
			}
		}
		return out, nil
	}
	seen := make(map[uint]struct{}, len(ids))
	out := make([]*storage.GroupDiscoveryCandidate, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, errors.New("candidate id is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		item, err := s.candidates.FindByID(id)
		if err != nil {
			return nil, err
		}
		switch item.Status {
		case statusApproved, statusFailed, statusApplying, statusApplied:
		default:
			return nil, fmt.Errorf("candidate %d is not approved", id)
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) toDTO(item *storage.GroupDiscoveryCandidate) (CandidateDTO, error) {
	groupIDs, err := s.candidates.ParseTargetGroupIDs(item)
	if err != nil {
		return CandidateDTO{}, fmt.Errorf("parse target group ids for candidate %d: %w", item.ID, err)
	}
	groupNames, err := s.candidates.ParseTargetGroupNames(item)
	if err != nil {
		return CandidateDTO{}, fmt.Errorf("parse target group names for candidate %d: %w", item.ID, err)
	}
	return CandidateDTO{
		ID:                     item.ID,
		SourceChannelID:        item.SourceChannelID,
		SourceChannelName:      item.SourceChannelName,
		SourceGroupID:          item.SourceGroupID,
		SourceGroupName:        item.SourceGroupName,
		SourceGroupDescription: item.SourceGroupDescription,
		Ratio:                  item.Ratio,
		Status:                 item.Status,
		TargetID:               item.TargetID,
		TargetGroupIDs:         groupIDs,
		TargetGroupNames:       groupNames,
		Platform:               item.Platform,
		AccountName:            item.AccountName,
		Concurrency:            item.Concurrency,
		Weight:                 item.Weight,
		SourceAPIKeyID:         item.SourceAPIKeyID,
		SourceAPIKeyName:       item.SourceAPIKeyName,
		TargetAccountID:        item.TargetAccountID,
		TargetAccountName:      item.TargetAccountName,
		ApplyError:             item.ApplyError,
		LastAttemptAt:          item.LastAttemptAt,
		AppliedAt:              item.AppliedAt,
		DiscoveredAt:           item.DiscoveredAt,
		LastSeenAt:             item.LastSeenAt,
	}, nil
}
