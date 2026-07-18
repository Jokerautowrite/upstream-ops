package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/storage"
)

const (
	apiKeyPageSize = 100
	maxAPIKeyPages = 100
)

func (s *Service) ensureSourceAPIKey(
	ctx context.Context,
	item *storage.GroupDiscoveryCandidate,
	channel *storage.Channel,
	group *connector.APIKeyGroup,
) (*connector.APIKey, string, error) {
	if channel == nil || group == nil {
		return nil, "", errors.New("source channel or group is missing")
	}
	keyName := strings.TrimSpace(item.SourceAPIKeyName)
	if keyName == "" {
		keyName = discoveryAPIKeyName(item.ID)
	}
	keys, err := s.listAllAPIKeys(ctx, channel.ID)
	if err != nil {
		return nil, "", fmt.Errorf("list source API keys: %w", err)
	}

	var key *connector.APIKey
	if item.SourceAPIKeyID != nil {
		key = apiKeyByID(keys, *item.SourceAPIKeyID)
		if key != nil && strings.TrimSpace(key.Name) != keyName {
			return nil, "", errors.New("tracked source API key no longer has the discovery key name")
		}
		if key == nil {
			if len(apiKeysByName(keys, keyName)) > 0 {
				return nil, "", errors.New("tracked source API key disappeared and its name is occupied; refusing to take over an unknown key")
			}
			// An explicitly retried candidate may recreate a key the operator
			// deliberately removed. No key with the stable name remains.
			if err := s.candidates.SetSourceAPIKey(item.ID, nil, keyName, nil); err != nil {
				return nil, "", err
			}
			item.SourceAPIKeyID = nil
			item.SourceKeyCreateAttemptedAt = nil
		}
	}

	if key == nil {
		matches := apiKeysByName(keys, keyName)
		switch len(matches) {
		case 0:
			if item.SourceKeyCreateAttemptedAt != nil {
				return nil, "", errors.New("source API key creation outcome is unresolved; inspect the upstream key list before retrying again")
			}
			attemptedAt := s.now()
			if err := s.candidates.SetSourceAPIKey(item.ID, nil, keyName, &attemptedAt); err != nil {
				return nil, "", err
			}
			item.SourceAPIKeyName = keyName
			item.SourceKeyCreateAttemptedAt = &attemptedAt
			unlimitedQuota, neverExpire := sourceKeyDefaults(channel)
			created, err := s.channelSvc.CreateAPIKey(ctx, channel.ID, connector.APIKeyCreateRequest{
				Name:           keyName,
				Group:          strings.TrimSpace(group.Name),
				GroupID:        group.ID,
				UnlimitedQuota: unlimitedQuota,
				ExpiredTime:    neverExpire,
			})
			if err != nil {
				if definitelyNoRemoteWrite(err) {
					_ = s.candidates.SetSourceAPIKey(item.ID, nil, keyName, nil)
					item.SourceKeyCreateAttemptedAt = nil
				}
				return nil, "", fmt.Errorf("create source API key: %w", err)
			}
			if created == nil || created.ID == 0 {
				return nil, "", errors.New("create source API key returned no id")
			}
			key = created
			if err := s.candidates.SetSourceAPIKey(item.ID, &key.ID, keyName, &attemptedAt); err != nil {
				return nil, "", err
			}
			item.SourceAPIKeyID = &key.ID
		case 1:
			if item.SourceKeyCreateAttemptedAt == nil {
				return nil, "", errors.New("source API key name is already occupied; refusing to take over an unknown key")
			}
			key = matches[0]
			if !apiKeyMatchesSourceGroup(key, group) {
				return nil, "", errors.New("a recovered source API key is bound to a different upstream group")
			}
			if err := s.candidates.SetSourceAPIKey(item.ID, &key.ID, keyName, item.SourceKeyCreateAttemptedAt); err != nil {
				return nil, "", err
			}
			item.SourceAPIKeyID = &key.ID
		default:
			return nil, "", errors.New("multiple source API keys use the discovery key name; manual cleanup is required")
		}
	}
	if key == nil {
		return nil, "", errors.New("source API key could not be reconciled")
	}

	name := keyName
	groupName := strings.TrimSpace(group.Name)
	unlimitedQuota, neverExpire := sourceKeyDefaults(channel)
	updated, err := s.channelSvc.UpdateAPIKey(ctx, channel.ID, key.ID, connector.APIKeyUpdateRequest{
		Name:           &name,
		Group:          &groupName,
		GroupID:        group.ID,
		UnlimitedQuota: unlimitedQuota,
		ExpiredTime:    neverExpire,
	})
	if err != nil {
		return nil, "", fmt.Errorf("update source API key group: %w", err)
	}
	if updated != nil {
		key = updated
	}
	secret, err := s.channelSvc.RevealAPIKey(ctx, channel.ID, key.ID)
	if err != nil {
		return nil, "", fmt.Errorf("reveal source API key: %w", err)
	}
	if strings.TrimSpace(secret) == "" {
		return nil, "", errors.New("revealed source API key is empty")
	}
	return key, secret, nil
}

func (s *Service) listAllAPIKeys(ctx context.Context, channelID uint) ([]connector.APIKey, error) {
	items := make([]connector.APIKey, 0)
	seenIDs := make(map[int64]struct{})
	for pageNumber := 1; pageNumber <= maxAPIKeyPages; pageNumber++ {
		page, err := s.channelSvc.ListAPIKeys(ctx, channelID, connector.APIKeyQuery{
			Page: pageNumber, PageSize: apiKeyPageSize,
		})
		if err != nil {
			return nil, err
		}
		if page == nil {
			return nil, errors.New("source API key list returned no page")
		}
		added := 0
		for _, item := range page.Items {
			if item.ID != 0 {
				if _, exists := seenIDs[item.ID]; exists {
					continue
				}
				seenIDs[item.ID] = struct{}{}
			}
			items = append(items, item)
			added++
		}
		knownPages := page.Pages
		if knownPages <= 0 && page.Total > 0 {
			knownPages = int((page.Total + apiKeyPageSize - 1) / apiKeyPageSize)
		}
		if knownPages > maxAPIKeyPages {
			return nil, fmt.Errorf("source API key list exceeds %d pages", maxAPIKeyPages)
		}
		if len(page.Items) < apiKeyPageSize || added == 0 || (knownPages > 0 && pageNumber >= knownPages) {
			return items, nil
		}
	}
	return nil, fmt.Errorf("source API key list exceeds %d pages", maxAPIKeyPages)
}

func definitelyNoRemoteWrite(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, status := range []string{"status 400", "status 401", "status 403", "status 404", "status 409", "status 422"} {
		if strings.Contains(message, status) {
			return true
		}
	}
	return false
}
