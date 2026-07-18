package discovery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
)

const (
	adminAccountPageSize = 100
	maxAdminAccountPages = 100
)

func (s *Service) applyOne(ctx context.Context, item *storage.GroupDiscoveryCandidate) error {
	if item.TargetID == nil || *item.TargetID == 0 {
		return errors.New("target is not configured")
	}
	accountName, err := validateTargetAccountName(item.AccountName)
	if err != nil {
		return err
	}
	item.AccountName = accountName
	channel, err := s.channels.FindByID(item.SourceChannelID)
	if err != nil {
		return fmt.Errorf("load source channel: %w", err)
	}
	sourceGroup, err := s.loadSourceGroup(ctx, item)
	if err != nil {
		return err
	}
	// Apply against the current upstream group, not a stale scan snapshot.
	item.SourceGroupName = strings.TrimSpace(sourceGroup.Name)
	item.SourceGroupDescription = strings.TrimSpace(sourceGroup.Description)
	item.Ratio = sourceGroup.Ratio
	if err := s.candidates.Update(item); err != nil {
		return fmt.Errorf("refresh source group snapshot: %w", err)
	}

	selectedGroupIDs, err := s.candidates.ParseTargetGroupIDs(item)
	if err != nil {
		return fmt.Errorf("parse target group ids: %w", err)
	}
	if len(selectedGroupIDs) == 0 {
		return errors.New("target groups are not configured")
	}
	groups, err := s.readTargetGroups(ctx, *item.TargetID)
	if err != nil {
		return err
	}
	if _, err := selectedTargetGroupNames(groups, selectedGroupIDs); err != nil {
		return err
	}
	_, adminTarget, err := s.loadTarget(*item.TargetID)
	if err != nil {
		return err
	}
	client := sub2api.NewAdminClient()
	if err := s.checkTargetAccountOwnership(ctx, item, adminTarget, client); err != nil {
		return err
	}

	_, secret, err := s.ensureSourceAPIKey(ctx, item, channel, sourceGroup)
	if err != nil {
		return err
	}

	account, request, err := s.ensureTargetAccount(ctx, item, channel, adminTarget, client, selectedGroupIDs, secret)
	if err != nil {
		return err
	}
	if account == nil {
		return errors.New("target account was not returned")
	}

	// A newly created account must not receive traffic while its model mapping is
	// incomplete. The operation is idempotent for retries of a partial apply.
	if _, err := client.SetAccountSchedulable(ctx, adminTarget, account.ID, false); err != nil {
		return fmt.Errorf("disable target account scheduling before model sync: %w", err)
	}
	models, err := client.SyncAccountModelsFromUpstream(ctx, adminTarget, account.ID)
	if err != nil {
		s.disableTargetAccount(ctx, adminTarget, client, account.ID, request)
		return fmt.Errorf("sync target account models: %w", err)
	}
	mapping := modelMapping(models)
	if len(mapping) == 0 {
		s.disableTargetAccount(ctx, adminTarget, client, account.ID, request)
		return errors.New("synced upstream models are empty")
	}

	request.Status = "active"
	request.Credentials["model_mapping"] = mapping
	updated, err := client.UpdateAccount(ctx, adminTarget, account.ID, request)
	if err != nil {
		s.disableTargetAccount(ctx, adminTarget, client, account.ID, request)
		return fmt.Errorf("activate target account: %w", err)
	}
	if updated != nil {
		account = updated
	}
	if _, err := client.SetAccountSchedulable(ctx, adminTarget, account.ID, true); err != nil {
		s.disableTargetAccount(ctx, adminTarget, client, account.ID, request)
		return fmt.Errorf("enable target account scheduling: %w", err)
	}
	return nil
}

// checkTargetAccountOwnership prevents a target-side naming collision from
// creating a source key that the operator did not intend to use. The actual
// write path repeats this lookup so a concurrent remote change remains safe.
func (s *Service) checkTargetAccountOwnership(
	ctx context.Context,
	item *storage.GroupDiscoveryCandidate,
	target sub2api.AdminTarget,
	client *sub2api.AdminClient,
) error {
	accountName := strings.TrimSpace(item.AccountName)
	if accountName == "" {
		return errors.New("account name is empty")
	}
	accounts, err := s.listAllTargetAccounts(ctx, target, client)
	if err != nil {
		return fmt.Errorf("list target accounts: %w", err)
	}
	if item.TargetAccountID != nil {
		for _, account := range accounts {
			if account.ID != *item.TargetAccountID {
				continue
			}
			if !isDiscoveryAccount(account, item.ID) {
				return errors.New("tracked target account no longer has the discovery ownership marker")
			}
			return accountNameAvailable(accounts, accountName, account.ID)
		}
	}

	matches := accountsByName(accounts, accountName)
	switch len(matches) {
	case 0:
		if item.TargetAccountCreateAttemptedAt != nil {
			return errors.New("target account creation outcome is unresolved; inspect the target account list before retrying again")
		}
		return nil
	case 1:
		if !isDiscoveryAccount(*matches[0], item.ID) {
			return errors.New("target account name is already occupied by an unmanaged account")
		}
		if item.TargetAccountCreateAttemptedAt == nil {
			return errors.New("target account name is already occupied; refusing to take over an unknown account")
		}
		return nil
	default:
		return errors.New("multiple target accounts have the requested name; manual cleanup is required")
	}
}

func (s *Service) ensureTargetAccount(
	ctx context.Context,
	item *storage.GroupDiscoveryCandidate,
	channel *storage.Channel,
	target sub2api.AdminTarget,
	client *sub2api.AdminClient,
	groupIDs []int64,
	secret string,
) (*sub2api.AdminAccount, sub2api.AdminAccount, error) {
	accountName := strings.TrimSpace(item.AccountName)
	if accountName == "" {
		return nil, sub2api.AdminAccount{}, errors.New("account name is empty")
	}
	request := sub2api.AdminAccount{
		Name:           accountName,
		Platform:       strings.TrimSpace(item.Platform),
		Type:           "apikey",
		Status:         "inactive",
		Notes:          discoveryAccountNotes(item.ID),
		Concurrency:    positiveOrDefault(item.Concurrency, 10),
		Priority:       priorityForRatio(item.Ratio),
		RateMultiplier: item.Ratio,
		LoadFactor:     float64(positiveOrDefault(item.Weight, 1)),
		GroupIDs:       groupIDs,
		Credentials: map[string]any{
			"api_key":  strings.TrimSpace(secret),
			"base_url": strings.TrimSpace(channel.SiteURL),
		},
	}
	if request.Platform == "" {
		request.Platform = "openai"
	}

	accounts, err := s.listAllTargetAccounts(ctx, target, client)
	if err != nil {
		return nil, sub2api.AdminAccount{}, fmt.Errorf("list target accounts: %w", err)
	}
	byID := make(map[int64]sub2api.AdminAccount, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}

	var account *sub2api.AdminAccount
	if item.TargetAccountID != nil {
		if stored, found := byID[*item.TargetAccountID]; found {
			if !isDiscoveryAccount(stored, item.ID) {
				return nil, sub2api.AdminAccount{}, errors.New("tracked target account no longer has the discovery ownership marker")
			}
			if err := accountNameAvailable(accounts, accountName, stored.ID); err != nil {
				return nil, sub2api.AdminAccount{}, err
			}
			updated, err := client.UpdateAccount(ctx, target, stored.ID, request)
			if err != nil && !isHTTPNotFound(err) {
				return nil, sub2api.AdminAccount{}, fmt.Errorf("update target account: %w", err)
			}
			if err == nil {
				if updated == nil {
					updated = &stored
				}
				account = updated
			} else if isHTTPNotFound(err) {
				if err := s.candidates.SetTargetAccount(item.ID, nil, accountName, nil); err != nil {
					return nil, sub2api.AdminAccount{}, err
				}
				item.TargetAccountID = nil
				item.TargetAccountCreateAttemptedAt = nil
			}
		} else {
			if err := s.candidates.SetTargetAccount(item.ID, nil, accountName, nil); err != nil {
				return nil, sub2api.AdminAccount{}, err
			}
			item.TargetAccountID = nil
			item.TargetAccountCreateAttemptedAt = nil
		}
	}

	if account == nil {
		matches := accountsByName(accounts, accountName)
		switch len(matches) {
		case 0:
			if item.TargetAccountCreateAttemptedAt != nil {
				return nil, sub2api.AdminAccount{}, errors.New("target account creation outcome is unresolved; inspect the target account list before retrying again")
			}
			attemptedAt := s.now()
			if err := s.candidates.SetTargetAccount(item.ID, nil, accountName, &attemptedAt); err != nil {
				return nil, sub2api.AdminAccount{}, err
			}
			item.TargetAccountName = accountName
			item.TargetAccountCreateAttemptedAt = &attemptedAt
			created, err := client.CreateAccount(ctx, target, request)
			if err != nil {
				if definitelyNoRemoteWrite(err) {
					_ = s.candidates.SetTargetAccount(item.ID, nil, accountName, nil)
					item.TargetAccountCreateAttemptedAt = nil
				}
				return nil, sub2api.AdminAccount{}, fmt.Errorf("create target account: %w", err)
			}
			if created == nil || created.ID == 0 {
				return nil, sub2api.AdminAccount{}, errors.New("create target account returned no id")
			}
			account = created
			if err := s.candidates.SetTargetAccount(item.ID, &account.ID, accountName, &attemptedAt); err != nil {
				return nil, sub2api.AdminAccount{}, err
			}
			item.TargetAccountID = &account.ID
		case 1:
			if !isDiscoveryAccount(*matches[0], item.ID) {
				return nil, sub2api.AdminAccount{}, errors.New("target account name is already occupied by an unmanaged account")
			}
			if item.TargetAccountCreateAttemptedAt == nil {
				return nil, sub2api.AdminAccount{}, errors.New("target account name is already occupied; refusing to take over an unknown account")
			}
			account = matches[0]
			if err := s.candidates.SetTargetAccount(item.ID, &account.ID, accountName, item.TargetAccountCreateAttemptedAt); err != nil {
				return nil, sub2api.AdminAccount{}, err
			}
			item.TargetAccountID = &account.ID
		default:
			return nil, sub2api.AdminAccount{}, errors.New("multiple target accounts have the requested name; manual cleanup is required")
		}
	}

	if account == nil {
		return nil, sub2api.AdminAccount{}, errors.New("target account could not be reconciled")
	}
	// The create endpoint can normalize or even activate a record; PUT makes
	// the desired inactive configuration explicit before model synchronization.
	updated, err := client.UpdateAccount(ctx, target, account.ID, request)
	if err != nil {
		return nil, sub2api.AdminAccount{}, fmt.Errorf("configure target account: %w", err)
	}
	if updated != nil {
		account = updated
	}
	if err := s.candidates.SetTargetAccount(item.ID, &account.ID, accountName, item.TargetAccountCreateAttemptedAt); err != nil {
		return nil, sub2api.AdminAccount{}, err
	}
	item.TargetAccountID = &account.ID
	item.TargetAccountName = accountName
	return account, request, nil
}

func (s *Service) listAllTargetAccounts(ctx context.Context, target sub2api.AdminTarget, client *sub2api.AdminClient) ([]sub2api.AdminAccount, error) {
	items := make([]sub2api.AdminAccount, 0)
	seenIDs := make(map[int64]struct{})
	for page := 1; page <= maxAdminAccountPages; page++ {
		current, err := client.ListAccounts(ctx, target, page, adminAccountPageSize)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, account := range current {
			if account.ID != 0 {
				if _, exists := seenIDs[account.ID]; exists {
					continue
				}
				seenIDs[account.ID] = struct{}{}
			}
			items = append(items, account)
			added++
		}
		if len(current) < adminAccountPageSize || added == 0 {
			return items, nil
		}
	}
	return nil, fmt.Errorf("target account list exceeds %d pages", maxAdminAccountPages)
}

func accountsByName(accounts []sub2api.AdminAccount, name string) []*sub2api.AdminAccount {
	result := make([]*sub2api.AdminAccount, 0, 1)
	for i := range accounts {
		if strings.EqualFold(strings.TrimSpace(accounts[i].Name), strings.TrimSpace(name)) {
			result = append(result, &accounts[i])
		}
	}
	return result
}

func accountNameAvailable(accounts []sub2api.AdminAccount, name string, ownID int64) error {
	for _, account := range accounts {
		if account.ID == ownID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(account.Name), strings.TrimSpace(name)) {
			return errors.New("target account name is already occupied")
		}
	}
	return nil
}

func (s *Service) disableTargetAccount(
	ctx context.Context,
	target sub2api.AdminTarget,
	client *sub2api.AdminClient,
	accountID int64,
	request sub2api.AdminAccount,
) {
	request.Status = "inactive"
	_, _ = client.UpdateAccount(ctx, target, accountID, request)
	_, _ = client.SetAccountSchedulable(ctx, target, accountID, false)
}

func priorityForRatio(ratio float64) int {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
		return 1_000_000_000
	}
	priority := int(math.Round(ratio*1_000_000)) + 1
	if priority < 1 {
		return 1
	}
	if priority > 1_000_000_000 {
		return 1_000_000_000
	}
	return priority
}

func positiveOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func isHTTPNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "status 404")
}
