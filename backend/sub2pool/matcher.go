package sub2pool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
)

type upstreamMatch struct {
	status         string
	rate           *float64
	balance        *float64
	todayCost      *float64
	matched        bool
	fingerprint    string
	identityDigest string
	rateAt         *time.Time
	balanceAt      *time.Time
	rateSource     string
	rateTrusted    bool
}

type upstreamCandidate struct {
	channelID      uint
	normalizedURL  string
	monitorEnabled bool
	rate           *float64
	balance        *float64
	todayCost      *float64
	rateAt         *time.Time
	balanceAt      *time.Time
}

type matcher struct {
	channels ChannelStore
	keys     ChannelKeyReader
}

const matcherChannelWorkerLimit = 4

type channelCandidateResult struct {
	normalized   string
	candidates   map[string][]upstreamCandidate
	urlCandidate upstreamCandidate
	err          error
}

func newMatcher(channels ChannelStore, keys ChannelKeyReader) *matcher {
	return &matcher{channels: channels, keys: keys}
}

func (m *matcher) matchAccounts(ctx context.Context, accounts []sub2api.PoolAccount) (map[int64]upstreamMatch, error) {
	result := make(map[int64]upstreamMatch, len(accounts))
	if m.channels == nil || m.keys == nil {
		for _, account := range accounts {
			identity := account.Identity()
			state := "missing"
			if identity.FingerprintSeen {
				state = "present"
			}
			result[account.Account.ID] = upstreamMatch{status: "upstream_unavailable", fingerprint: state}
		}
		return result, nil
	}

	channels, err := m.channels.List()
	if err != nil {
		return nil, err
	}
	allChannels := append([]storage.Channel(nil), channels...)
	channels = filterMonitorEnabledChannels(channels)
	sort.Slice(channels, func(i, j int) bool { return channels[i].ID < channels[j].ID })

	byURLKey := map[string]map[string][]upstreamCandidate{}
	byURL := map[string][]upstreamCandidate{}
	unavailableURLs := map[string]struct{}{}
	for _, channel := range allChannels {
		normalized := normalizeMappingURL(channel.SiteURL)
		if normalized == "" {
			continue
		}
		byURL[normalized] = append(
			byURL[normalized],
			urlCandidate(channel, normalized),
		)
	}
	jobs := make(chan storage.Channel)
	results := make(chan channelCandidateResult, len(channels))
	workerCount := min(matcherChannelWorkerLimit, len(channels))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for channel := range jobs {
				normalized := normalizeURL(channel.SiteURL)
				if normalized == "" {
					continue
				}
				candidates, candidateErr := m.channelCandidates(ctx, channel)
				results <- channelCandidateResult{
					normalized:   normalized,
					candidates:   candidates,
					urlCandidate: urlCandidate(channel, normalized),
					err:          candidateErr,
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, channel := range channels {
			select {
			case jobs <- channel:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	for result := range results {
		if result.err != nil {
			unavailableURLs[result.normalized] = struct{}{}
			continue
		}
		for keyHash, keyCandidates := range result.candidates {
			if byURLKey[result.normalized] == nil {
				byURLKey[result.normalized] = map[string][]upstreamCandidate{}
			}
			byURLKey[result.normalized][keyHash] = append(
				byURLKey[result.normalized][keyHash],
				keyCandidates...,
			)
		}
	}

	for _, account := range accounts {
		identity := account.Identity()
		normalized := normalizeURL(identity.BaseURL)
		state := "missing"
		if identity.FingerprintSeen {
			state = "present"
		}
		match := upstreamMatch{status: "url_missing", fingerprint: state}
		if normalized == "" {
			result[account.Account.ID] = match
			continue
		}
		if identity.FingerprintSeen {
			match.identityDigest = identityDigest(normalized, identity.APIKeySHA256)
			candidates := byURLKey[normalized][identity.APIKeySHA256]
			if candidate, unique := uniqueKeyCandidate(candidates); unique {
				match = makeExactMatch(candidate, state)
				match.identityDigest = identityDigest(normalized, identity.APIKeySHA256)
			} else if len(candidates) > 0 {
				match = upstreamMatch{
					status:         "key_ambiguous",
					fingerprint:    state,
					identityDigest: identityDigest(normalized, identity.APIKeySHA256),
				}
			} else {
				// A complete fingerprint that does not match must never fall
				// back to URL or name matching for its multiplier. A unique
				// monitored URL may still provide a site-level balance.
				if _, unavailable := unavailableURLs[normalized]; unavailable {
					match = upstreamMatch{
						status:         "upstream_unavailable",
						fingerprint:    state,
						identityDigest: identityDigest(normalized, identity.APIKeySHA256),
					}
				} else {
					match = upstreamMatch{
						status:         "key_mismatch",
						fingerprint:    state,
						identityDigest: identityDigest(normalized, identity.APIKeySHA256),
					}
				}
			}
			match = withURLBalance(match, byURL[normalizeMappingURL(identity.BaseURL)])
			result[account.Account.ID] = match
			continue
		}
		match = withURLBalance(
			upstreamMatch{status: "fingerprint_missing", fingerprint: state},
			byURL[normalizeMappingURL(identity.BaseURL)],
		)
		result[account.Account.ID] = match
	}
	return result, nil
}

func filterMonitorEnabledChannels(channels []storage.Channel) []storage.Channel {
	out := make([]storage.Channel, 0, len(channels))
	for _, channel := range channels {
		if channel.MonitorEnabled {
			out = append(out, channel)
		}
	}
	return out
}

func urlCandidate(channel storage.Channel, normalized string) upstreamCandidate {
	return upstreamCandidate{
		channelID:      channel.ID,
		normalizedURL:  normalized,
		monitorEnabled: channel.MonitorEnabled,
		balance:        cloneFloat(channel.LastBalance),
		todayCost:      cloneFloat(channel.TodayCost),
		balanceAt:      cloneTime(channel.LastBalanceAt),
	}
}

func uniqueKeyCandidate(candidates []upstreamCandidate) (upstreamCandidate, bool) {
	if len(candidates) == 0 {
		return upstreamCandidate{}, false
	}
	first := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.channelID != first.channelID ||
			!equalFloatPointers(first.rate, candidate.rate) ||
			!equalFloatPointers(first.balance, candidate.balance) ||
			!equalFloatPointers(first.todayCost, candidate.todayCost) {
			return upstreamCandidate{}, false
		}
	}
	return first, true
}

func uniqueURLCandidate(candidates []upstreamCandidate) (upstreamCandidate, bool) {
	candidates = preferBalanceCandidates(candidates)
	if len(candidates) == 0 {
		return upstreamCandidate{}, false
	}
	first := candidates[0]
	for _, candidate := range candidates[1:] {
		if !equalFloatPointers(first.balance, candidate.balance) ||
			!equalFloatPointers(first.todayCost, candidate.todayCost) {
			return upstreamCandidate{}, false
		}
	}
	return first, true
}

func preferBalanceCandidates(candidates []upstreamCandidate) []upstreamCandidate {
	active := make([]upstreamCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.monitorEnabled {
			active = append(active, candidate)
		}
	}
	if len(active) > 0 {
		return active
	}
	return candidates
}

func withURLBalance(match upstreamMatch, candidates []upstreamCandidate) upstreamMatch {
	if match.balance != nil {
		return match
	}
	candidate, unique := uniqueURLCandidate(candidates)
	if !unique {
		return match
	}
	match.balance = cloneFloat(candidate.balance)
	match.todayCost = cloneFloat(candidate.todayCost)
	match.balanceAt = cloneTime(candidate.balanceAt)
	return match
}

func makeExactMatch(candidate upstreamCandidate, fingerprint string) upstreamMatch {
	return upstreamMatch{
		status:      "key_exact",
		rate:        candidate.rate,
		balance:     candidate.balance,
		todayCost:   candidate.todayCost,
		matched:     true,
		fingerprint: fingerprint,
		rateAt:      cloneTime(candidate.rateAt),
		balanceAt:   cloneTime(candidate.balanceAt),
		rateSource:  "upstream_api_key",
		rateTrusted: true,
	}
}

func (m *matcher) channelCandidates(ctx context.Context, channel storage.Channel) (map[string][]upstreamCandidate, error) {
	const pageSize = 100
	const maxPages = 1000

	out := map[string][]upstreamCandidate{}
	for page := 1; page <= maxPages; page++ {
		keyPage, err := m.keys.ListAPIKeys(ctx, channel.ID, connector.APIKeyQuery{Page: page, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		for _, item := range keyPage.Items {
			rawKey, err := m.keys.RevealAPIKey(ctx, channel.ID, item.ID)
			if err != nil {
				return nil, err
			}
			hash := hashKey(rawKey)
			rawKey = ""
			if hash == "" {
				continue
			}
			var rate *float64
			if item.GroupRatio > 0 {
				value := item.GroupRatio
				rate = &value
			}
			out[hash] = append(out[hash], upstreamCandidate{
				channelID:     channel.ID,
				normalizedURL: normalizeURL(channel.SiteURL),
				rate:          rate,
				balance:       cloneFloat(channel.LastBalance),
				todayCost:     cloneFloat(channel.TodayCost),
				rateAt:        timePtr(time.Now()),
				balanceAt:     cloneTime(channel.LastBalanceAt),
			})
		}
		if keyPage.Pages <= page || len(keyPage.Items) < pageSize {
			return out, nil
		}
	}
	return nil, ErrUnavailable
}

func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return ""
	}
	port := parsed.Port()
	if port != "" && !((parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80")) {
		host = net.JoinHostPort(host, port)
	}
	return strings.ToLower(parsed.Scheme) + "://" + host
}

func identityDigest(normalizedURL, keySHA256 string) string {
	normalizedURL = strings.TrimSpace(normalizedURL)
	keySHA256 = strings.TrimSpace(keySHA256)
	if normalizedURL == "" || keySHA256 == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalizedURL + "\x00" + keySHA256))
	return hex.EncodeToString(sum[:])
}

func hashKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func timePtr(value time.Time) *time.Time {
	return &value
}
