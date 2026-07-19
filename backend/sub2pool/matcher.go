package sub2pool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
)

var embeddedRatePattern = regexp.MustCompile(`(?i)(?:^|[^0-9])(0?\.\d{1,6}|\d+\.\d{1,6})(?:[^0-9]|$)`)

type upstreamMatch struct {
	status         string
	rate           *float64
	balance        *float64
	todayCost      *float64
	matched        bool
	fingerprint    string
	identityDigest string
	channelID      uint
	groupName      string
}

type upstreamCandidate struct {
	channelID     uint
	channelName   string
	normalizedURL string
	groupName     string
	keyHash       string
	rate          *float64
	balance       *float64
	todayCost     *float64
}

type matcher struct {
	channels ChannelStore
	keys     ChannelKeyReader
	rates    RateSnapshotStore
}

const matcherChannelWorkerLimit = 4

type channelCandidateResult struct {
	normalized   string
	channel      storage.Channel
	candidates   map[string][]upstreamCandidate
	urlCandidate upstreamCandidate
	err          error
}

func newMatcher(channels ChannelStore, keys ChannelKeyReader) *matcher {
	return &matcher{channels: channels, keys: keys}
}

func (m *matcher) setRates(rates RateSnapshotStore) {
	if m == nil {
		return
	}
	m.rates = rates
}

// matchAccounts binds each Sub2 pool account to monitor-station data.
//
// Precision order (never invent rates):
//  1. full API-key SHA-256 exact match (global; URL is only a tie-break)
//  2. unique normalized account-name ↔ channel-name match
//  3. unique account-name / min-group ↔ rate-snapshot model_name within same-origin channels
//
// A complete key fingerprint that collides across multiple different rates stays
// ambiguous. URL-only matching never supplies a multiplier (balance only).
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
	channels = filterMonitorEnabledChannels(channels)
	sort.Slice(channels, func(i, j int) bool { return channels[i].ID < channels[j].ID })

	byKeyHash := map[string][]upstreamCandidate{}
	byURL := map[string][]upstreamCandidate{}
	byURLChannel := map[string][]storage.Channel{}
	channelByID := map[uint]storage.Channel{}
	unavailableURLs := map[string]struct{}{}
	nameIndex := map[string][]storage.Channel{}

	jobs := make(chan storage.Channel)
	results := make(chan channelCandidateResult, len(channels))
	workerCount := min(matcherChannelWorkerLimit, len(channels))
	if workerCount == 0 {
		workerCount = 1
	}
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
					channel:      channel,
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

	for item := range results {
		// 即使 List/Reveal API Key 失败（常见 429），监控站 last_balance 仍应可用于
		// 同 URL 账号补余额。旧逻辑 continue 会丢掉 byURL，导致「监控有余额、池里空」。
		if item.normalized != "" {
			channelByID[item.channel.ID] = item.channel
			byURL[item.normalized] = append(byURL[item.normalized], item.urlCandidate)
			byURLChannel[item.normalized] = append(byURLChannel[item.normalized], item.channel)
			nameKey := normalizeMatchName(item.channel.Name)
			if nameKey != "" {
				nameIndex[nameKey] = append(nameIndex[nameKey], item.channel)
			}
		}
		if item.err != nil {
			if item.normalized != "" {
				unavailableURLs[item.normalized] = struct{}{}
			}
			continue
		}
		for keyHash, keyCandidates := range item.candidates {
			byKeyHash[keyHash] = append(byKeyHash[keyHash], keyCandidates...)
		}
	}

	// Preload rate snapshots once for all monitor channels.
	ratesByChannel := m.loadChannelRates(ctx, channels)

	for _, account := range accounts {
		identity := account.Identity()
		normalized := normalizeURL(identity.BaseURL)
		state := "missing"
		if identity.FingerprintSeen {
			state = "present"
		}
		match := upstreamMatch{status: "url_missing", fingerprint: state}
		if identity.FingerprintSeen {
			match.identityDigest = identityDigest(normalized, identity.APIKeySHA256)
		}

		// 1) Full key fingerprint — global unique key is strongest identity.
		if identity.FingerprintSeen && identity.APIKeySHA256 != "" {
			candidates := byKeyHash[identity.APIKeySHA256]
			if len(candidates) == 0 {
				if _, unavailable := unavailableURLs[normalized]; unavailable && normalized != "" {
					match.status = "upstream_unavailable"
				} else {
					match.status = "key_mismatch"
				}
			} else if candidate, ok := uniqueKeyCandidatePreferOrigin(candidates, normalized); ok {
				match = makeExactMatch(candidate, state)
				match.identityDigest = identityDigest(normalized, identity.APIKeySHA256)
				match = fillRateFromSnapshots(match, candidate, ratesByChannel[candidate.channelID])
			} else {
				match.status = "key_ambiguous"
				match.identityDigest = identityDigest(normalized, identity.APIKeySHA256)
			}
		} else {
			match.status = "fingerprint_missing"
		}

		// 2) Unique channel-name match against monitor inventory.
		if match.rate == nil {
			if channel, ok := uniqueChannelByName(nameIndex, account.Account.Name); ok {
				nameMatch := matchFromChannel(channel, "channel_name_exact", state, ratesByChannel[channel.ID], account)
				if match.status == "key_mismatch" || match.status == "fingerprint_missing" || match.status == "url_missing" || match.status == "upstream_unavailable" {
					// Keep stronger status if key already exact; otherwise promote name match.
					if !match.matched {
						match = nameMatch
						if identity.FingerprintSeen {
							match.identityDigest = identityDigest(normalized, identity.APIKeySHA256)
						}
					} else if match.rate == nil {
						match.rate = nameMatch.rate
						match.channelID = nameMatch.channelID
						match.groupName = nameMatch.groupName
					}
				} else if match.rate == nil {
					match.rate = nameMatch.rate
					if match.channelID == 0 {
						match.channelID = nameMatch.channelID
					}
				}
			}
		}

		// 3) Unique group/model name match within same-origin monitor channels.
		if match.rate == nil && normalized != "" {
			if channel, group, rate, ok := uniqueGroupRateOnURL(byURLChannel[normalized], ratesByChannel, account); ok {
				groupMatch := upstreamMatch{
					status:      "group_name_exact",
					rate:        cloneFloat(&rate),
					balance:     cloneFloat(channel.LastBalance),
					todayCost:   cloneFloat(channel.TodayCost),
					matched:     true,
					fingerprint: state,
					channelID:   channel.ID,
					groupName:   group,
				}
				if identity.FingerprintSeen {
					groupMatch.identityDigest = identityDigest(normalized, identity.APIKeySHA256)
				}
				if !match.matched {
					match = groupMatch
				} else if match.rate == nil {
					match.rate = groupMatch.rate
					match.groupName = group
					if match.channelID == 0 {
						match.channelID = channel.ID
					}
				}
			}
		}

		// URL-unique balance only (never a multiplier).
		match = withURLBalance(match, byURL[normalized])
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
		channelID:     channel.ID,
		channelName:   channel.Name,
		normalizedURL: normalized,
		balance:       cloneFloat(channel.LastBalance),
		todayCost:     cloneFloat(channel.TodayCost),
	}
}

func uniqueKeyCandidatePreferOrigin(candidates []upstreamCandidate, preferredURL string) (upstreamCandidate, bool) {
	if len(candidates) == 0 {
		return upstreamCandidate{}, false
	}
	preferredURL = strings.TrimSpace(preferredURL)
	if preferredURL != "" {
		sameOrigin := make([]upstreamCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.normalizedURL == preferredURL {
				sameOrigin = append(sameOrigin, candidate)
			}
		}
		if len(sameOrigin) > 0 {
			return uniqueKeyCandidate(sameOrigin)
		}
	}
	return uniqueKeyCandidate(candidates)
}

func uniqueKeyCandidate(candidates []upstreamCandidate) (upstreamCandidate, bool) {
	if len(candidates) == 0 {
		return upstreamCandidate{}, false
	}
	first := candidates[0]
	for _, candidate := range candidates[1:] {
		// Same revealed key must land on one channel identity. Multiple rows on
		// the same channel (different group labels) are accepted when rates agree.
		if candidate.channelID != first.channelID {
			return upstreamCandidate{}, false
		}
		if !equalFloatPointers(first.rate, candidate.rate) ||
			!equalFloatPointers(first.balance, candidate.balance) ||
			!equalFloatPointers(first.todayCost, candidate.todayCost) {
			return upstreamCandidate{}, false
		}
	}
	return first, true
}

func uniqueURLCandidate(candidates []upstreamCandidate) (upstreamCandidate, bool) {
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
	return match
}

func makeExactMatch(candidate upstreamCandidate, fingerprint string) upstreamMatch {
	return upstreamMatch{
		status:      "key_exact",
		rate:        cloneFloat(candidate.rate),
		balance:     cloneFloat(candidate.balance),
		todayCost:   cloneFloat(candidate.todayCost),
		matched:     true,
		fingerprint: fingerprint,
		channelID:   candidate.channelID,
		groupName:   candidate.groupName,
	}
}

func matchFromChannel(
	channel storage.Channel,
	status, fingerprint string,
	snapshots []storage.RateSnapshot,
	account sub2api.PoolAccount,
) upstreamMatch {
	rate := pickChannelRate(snapshots, account, "")
	return upstreamMatch{
		status:      status,
		rate:        rate,
		balance:     cloneFloat(channel.LastBalance),
		todayCost:   cloneFloat(channel.TodayCost),
		matched:     rate != nil,
		fingerprint: fingerprint,
		channelID:   channel.ID,
	}
}

func fillRateFromSnapshots(match upstreamMatch, candidate upstreamCandidate, snapshots []storage.RateSnapshot) upstreamMatch {
	if match.rate != nil {
		return match
	}
	if rate := pickChannelRate(snapshots, sub2api.PoolAccount{}, candidate.groupName); rate != nil {
		match.rate = rate
		return match
	}
	return match
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
		if keyPage == nil {
			return out, nil
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
				channelName:   channel.Name,
				normalizedURL: normalizeURL(channel.SiteURL),
				groupName:     strings.TrimSpace(item.GroupName),
				keyHash:       hash,
				rate:          rate,
				balance:       cloneFloat(channel.LastBalance),
				todayCost:     cloneFloat(channel.TodayCost),
			})
		}
		if keyPage.Pages <= page || len(keyPage.Items) < pageSize {
			return out, nil
		}
	}
	return nil, ErrUnavailable
}

func (m *matcher) loadChannelRates(ctx context.Context, channels []storage.Channel) map[uint][]storage.RateSnapshot {
	out := make(map[uint][]storage.RateSnapshot, len(channels))
	if m == nil || m.rates == nil {
		return out
	}
	for _, channel := range channels {
		if err := ctx.Err(); err != nil {
			return out
		}
		rows, err := m.rates.ListByChannel(channel.ID)
		if err != nil || len(rows) == 0 {
			continue
		}
		out[channel.ID] = rows
	}
	return out
}

func uniqueChannelByName(index map[string][]storage.Channel, accountName string) (storage.Channel, bool) {
	key := normalizeMatchName(accountName)
	if key == "" {
		return storage.Channel{}, false
	}
	// exact normalized name
	if hits := index[key]; len(hits) == 1 {
		return hits[0], true
	}
	// unique containment: account name fully contains channel name or reverse,
	// only when exactly one channel qualifies.
	var found []storage.Channel
	seen := map[uint]struct{}{}
	for nameKey, channels := range index {
		if nameKey == "" {
			continue
		}
		if strings.Contains(key, nameKey) || strings.Contains(nameKey, key) {
			for _, channel := range channels {
				if _, ok := seen[channel.ID]; ok {
					continue
				}
				// Require meaningful length to avoid "pro"/"cc" false positives.
				if minNameLen(nameKey, key) < 6 && nameKey != key {
					continue
				}
				seen[channel.ID] = struct{}{}
				found = append(found, channel)
			}
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return storage.Channel{}, false
}

func minNameLen(a, b string) int {
	if len(a) < len(b) {
		return len(a)
	}
	return len(b)
}

func uniqueGroupRateOnURL(
	channels []storage.Channel,
	ratesByChannel map[uint][]storage.RateSnapshot,
	account sub2api.PoolAccount,
) (storage.Channel, string, float64, bool) {
	if len(channels) == 0 {
		return storage.Channel{}, "", 0, false
	}
	// Collect candidate labels from account name and notes.
	labels := accountMatchLabels(account)
	if len(labels) == 0 {
		return storage.Channel{}, "", 0, false
	}
	type hit struct {
		channel storage.Channel
		group   string
		rate    float64
	}
	var hits []hit
	seen := map[string]struct{}{}
	for _, channel := range channels {
		for _, snapshot := range ratesByChannel[channel.ID] {
			modelKey := normalizeMatchName(snapshot.ModelName)
			descKey := normalizeMatchName(snapshot.Description)
			for _, label := range labels {
				if label == "" {
					continue
				}
				if modelKey == label || descKey == label ||
					(len(label) >= 6 && (strings.Contains(modelKey, label) || strings.Contains(label, modelKey))) {
					key := strings.TrimSpace(snapshot.ModelName) + "\x00" + strings.TrimSpace(channel.Name)
					if _, ok := seen[key]; ok {
						continue
					}
					seen[key] = struct{}{}
					if snapshot.Ratio <= 0 {
						continue
					}
					hits = append(hits, hit{channel: channel, group: snapshot.ModelName, rate: snapshot.Ratio})
				}
			}
		}
	}
	if len(hits) == 0 {
		return storage.Channel{}, "", 0, false
	}
	// All hits must agree on the same rate to stay precise.
	first := hits[0]
	for _, item := range hits[1:] {
		if item.rate != first.rate || item.channel.ID != first.channel.ID {
			return storage.Channel{}, "", 0, false
		}
	}
	return first.channel, first.group, first.rate, true
}

func accountMatchLabels(account sub2api.PoolAccount) []string {
	labels := []string{normalizeMatchName(account.Account.Name)}
	// notes often carry discovery group names
	if notes := strings.TrimSpace(account.Account.Notes); notes != "" {
		labels = append(labels, normalizeMatchName(notes))
	}
	out := make([]string, 0, len(labels))
	seen := map[string]struct{}{}
	for _, label := range labels {
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	return out
}

func pickChannelRate(snapshots []storage.RateSnapshot, account sub2api.PoolAccount, preferredGroup string) *float64 {
	if len(snapshots) == 0 {
		return nil
	}
	preferredGroup = normalizeMatchName(preferredGroup)
	labels := accountMatchLabels(account)
	if preferredGroup != "" {
		labels = append([]string{preferredGroup}, labels...)
	}

	// 1) preferred / name-aligned group
	for _, label := range labels {
		if label == "" {
			continue
		}
		var matched []float64
		for _, snapshot := range snapshots {
			modelKey := normalizeMatchName(snapshot.ModelName)
			descKey := normalizeMatchName(snapshot.Description)
			if modelKey == label || descKey == label ||
				(len(label) >= 6 && (strings.Contains(modelKey, label) || strings.Contains(descKey, label))) {
				if snapshot.Ratio > 0 {
					matched = append(matched, snapshot.Ratio)
				}
			}
		}
		if rate, ok := uniquePositiveRate(matched); ok {
			return &rate
		}
	}

	// 2) account name embeds a decimal (e.g. "PLUS 0.04 天望") → nearest monitor ratio
	if hinted := extractEmbeddedRates(account.Account.Name); len(hinted) > 0 {
		allPositive := positiveRates(snapshots)
		if rate, ok := closestRate(allPositive, hinted); ok {
			return &rate
		}
	}

	// 3) if channel has one unique positive ratio across all groups, use it
	all := positiveRates(snapshots)
	if rate, ok := uniquePositiveRate(all); ok {
		return &rate
	}
	// 4) lowest positive ratio as conservative display (still from monitor data)
	if rate, ok := lowestPositiveRate(all); ok {
		return &rate
	}
	return nil
}

func positiveRates(snapshots []storage.RateSnapshot) []float64 {
	all := make([]float64, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.Ratio > 0 {
			all = append(all, snapshot.Ratio)
		}
	}
	return all
}

func extractEmbeddedRates(name string) []float64 {
	matches := embeddedRatePattern.FindAllStringSubmatch(name, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]float64, 0, len(matches))
	seen := map[float64]struct{}{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil || value <= 0 || value > 10 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func closestRate(candidates, hints []float64) (float64, bool) {
	if len(candidates) == 0 || len(hints) == 0 {
		return 0, false
	}
	bestRate := 0.0
	bestDist := math.MaxFloat64
	found := false
	for _, hint := range hints {
		for _, candidate := range candidates {
			dist := math.Abs(candidate - hint)
			// Require tight agreement so 0.04 does not latch onto 0.4.
			if dist > 0.02 && dist/math.Max(hint, 1e-9) > 0.25 {
				continue
			}
			if !found || dist < bestDist || (dist == bestDist && candidate < bestRate) {
				bestRate = candidate
				bestDist = dist
				found = true
			}
		}
	}
	return bestRate, found
}

func uniquePositiveRate(values []float64) (float64, bool) {
	var first float64
	found := false
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if !found {
			first = value
			found = true
			continue
		}
		if value != first {
			return 0, false
		}
	}
	return first, found
}

func lowestPositiveRate(values []float64) (float64, bool) {
	var best float64
	found := false
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if !found || value < best {
			best = value
			found = true
		}
	}
	return best, found
}

func normalizeMatchName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(value))
	prevSpace := false
	for _, r := range value {
		if unicode.IsSpace(r) || r == '_' || r == '-' || r == '/' || r == '|' || r == '·' {
			if !prevSpace {
				// drop separators entirely for tighter equality
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return b.String()
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
	// Strip common www. prefix so monitor URL and pool base_url agree.
	host = strings.TrimPrefix(host, "www.")
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
