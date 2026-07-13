package sub2pool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
)

func TestAccountRateMapImportIsAtomicAndIdempotent(t *testing.T) {
	_, store := newRecoveryGormStore(t)
	path := filepath.Join(t.TempDir(), "legacy-map.json")
	if err := os.WriteFile(path, []byte(`{
		"accounts": {
			"11": {"site_url": "https://one.example.test/v1", "model": "Model One"},
			"12": {"site_url": "https://two.example.test", "model": "Model Two", "rate": 0.25}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write import: %v", err)
	}

	imported, err := store.ImportAccountRateMappingsIfEmpty(1, path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported != 2 {
		t.Fatalf("imported = %d, want 2", imported)
	}
	rows, err := store.ListAccountRateMappings(1)
	if err != nil {
		t.Fatalf("list imported mappings: %v", err)
	}
	if len(rows) != 2 || rows[0].AccountID != 11 || rows[1].AccountID != 12 {
		t.Fatalf("rows = %#v", rows)
	}

	if err := os.WriteFile(path, []byte(`{
		"accounts": {
			"99": {"site_url": "https://different.example.test", "model": "Different"}
		}
	}`), 0o600); err != nil {
		t.Fatalf("rewrite import: %v", err)
	}
	imported, err = store.ImportAccountRateMappingsIfEmpty(1, path)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if imported != 0 {
		t.Fatalf("second import = %d, want 0", imported)
	}
	rows, err = store.ListAccountRateMappings(1)
	if err != nil {
		t.Fatalf("list mappings after second import: %v", err)
	}
	if len(rows) != 2 || rows[0].AccountID != 11 || rows[1].AccountID != 12 {
		t.Fatalf("idempotent rows = %#v", rows)
	}
}

func TestResolveAccountRateMappingsUsesExactAccountURLAndModel(t *testing.T) {
	rate := 0.25
	resolved := resolveAccountRateMappings(
		context.Background(),
		1,
		[]sub2api.PoolAccount{
			poolAccount(11, "https://api.example.test/v1", "unmatched-key", 10),
			poolAccount(12, "https://changed.example.test/v1", "unmatched-key", 20),
		},
		staticAccountRateMappings{items: []AccountRateMapping{
			{TargetID: 1, AccountID: 11, SiteURL: "https://api.example.test", ModelName: "MODEL ONE"},
			{TargetID: 1, AccountID: 12, SiteURL: "https://api.example.test", ModelName: "Model One"},
		}},
		staticChannels{items: []storage.Channel{{
			ID:             7,
			SiteURL:        "https://api.example.test",
			MonitorEnabled: true,
		}}},
		staticRateSnapshots{items: map[uint][]storage.RateSnapshot{
			7: {{ChannelID: 7, ModelName: "model one", Ratio: rate}},
		}},
	)
	if resolved[11] != rate {
		t.Fatalf("mapped rate = %#v, want account 11 at %v", resolved, rate)
	}
	if _, exists := resolved[12]; exists {
		t.Fatalf("URL-changed account received stale mapping: %#v", resolved)
	}
}

func TestResolveAccountRateMappingsUsesDisabledSnapshotAsFallback(t *testing.T) {
	rate := 0.25
	resolved := resolveAccountRateMappings(
		context.Background(),
		1,
		[]sub2api.PoolAccount{
			poolAccount(11, "https://api.example.test/v1", "unmatched-key", 10),
		},
		staticAccountRateMappings{items: []AccountRateMapping{
			{TargetID: 1, AccountID: 11, SiteURL: "https://api.example.test", ModelName: "Model One"},
		}},
		staticChannels{items: []storage.Channel{{
			ID:             7,
			SiteURL:        "https://api.example.test",
			MonitorEnabled: false,
		}}},
		staticRateSnapshots{items: map[uint][]storage.RateSnapshot{
			7: {{ChannelID: 7, ModelName: "model one", Ratio: rate}},
		}},
	)
	if resolved[11] != rate {
		t.Fatalf("disabled snapshot fallback = %#v, want account 11 at %v", resolved, rate)
	}
}

func TestResolveAccountRateMappingsUsesAccountLabelAndObservedRate(t *testing.T) {
	account := poolAccount(5906, "https://api.example.test", "unmatched-key", 10)
	account.Account.Name = "Pro 008 阿拉丁"
	resolved := resolveAccountRateMappings(
		context.Background(),
		1,
		[]sub2api.PoolAccount{
			account,
		},
		staticAccountRateMappings{},
		staticChannels{items: []storage.Channel{{
			ID:             54,
			Name:           "Pro 008 阿拉丁 / Plus 006 阿拉丁",
			SiteURL:        "https://api.example.test",
			MonitorEnabled: true,
		}}},
		staticRateSnapshots{items: map[uint][]storage.RateSnapshot{
			54: {
				{ChannelID: 54, ModelName: "gpt pro", Ratio: 0.08},
				{ChannelID: 54, ModelName: "生图", Ratio: 0.03},
			},
		}},
	)
	if resolved[5906] != 0.08 {
		t.Fatalf("account label mapping = %#v, want account 5906 at 0.08", resolved)
	}
}

func TestResolveAccountRateMappingsUsesURLAliasForExplicitMapping(t *testing.T) {
	resolved := resolveAccountRateMappings(
		context.Background(),
		1,
		[]sub2api.PoolAccount{
			poolAccountNamedForRateMap(5834, "https://mhapi.cn", "kiro 0.08 梦幻"),
		},
		staticAccountRateMappings{items: []AccountRateMapping{{
			AccountID: 5834,
			SiteURL:   "https://mhapi.cn",
			ModelName: "Claude Kiro 有缓",
		}}},
		staticChannels{items: []storage.Channel{{
			ID:             22,
			Name:           "CC 0.7 梦幻 / Pro 0.11 梦幻 / Plus 0.04梦幻",
			SiteURL:        "https://api.mhapi.cn",
			MonitorEnabled: true,
		}}},
		staticRateSnapshots{items: map[uint][]storage.RateSnapshot{
			22: {{ChannelID: 22, ModelName: "Claude Kiro 有缓", Ratio: 0.08}},
		}},
	)
	if resolved[5834] != 0.08 {
		t.Fatalf("URL alias mapping = %#v, want account 5834 at 0.08", resolved)
	}
}

func TestResolveAccountRateMappingsUsesSharedChannelIdentity(t *testing.T) {
	resolved := resolveAccountRateMappings(
		context.Background(),
		1,
		[]sub2api.PoolAccount{
			poolAccountNamedForRateMap(5905, "https://r.l.cd", "Plus 0.035 吱吱鼠"),
		},
		staticAccountRateMappings{},
		staticChannels{items: []storage.Channel{{
			ID:             12,
			Name:           "Pro 0.12 吱吱鼠",
			SiteURL:        "https://gptplus.pp.ua",
			MonitorEnabled: true,
		}}},
		staticRateSnapshots{items: map[uint][]storage.RateSnapshot{
			12: {
				{ChannelID: 12, ModelName: "Plus", Ratio: 0.045},
				{ChannelID: 12, ModelName: "codex福利分组", Ratio: 0.035},
			},
		}},
	)
	if resolved[5905] != 0.035 {
		t.Fatalf("shared identity mapping = %#v, want account 5905 at 0.035", resolved)
	}
}

func TestResolveAccountRateMappingsUsesGroupLabelWhenMonitorUnavailable(t *testing.T) {
	account := poolAccountNamedForRateMap(5878, "https://ark.cn-beijing.volces.com", "deepseek")
	account.Account.GroupIDs = []int64{94}
	resolved := resolveAccountRateMappingsWithGroups(
		context.Background(),
		1,
		[]sub2api.PoolAccount{account},
		[]sub2api.AdminGroup{{
			ID:   94,
			Name: "国模大合集 自用 官方5折",
		}},
		staticAccountRateMappings{},
		staticChannels{},
		staticRateSnapshots{},
	)
	if resolved[5878] != 0.5 {
		t.Fatalf("group-label fallback = %#v, want account 5878 at 0.5", resolved)
	}
}

func TestResolveAccountRateMappingsUsesManualRateOnlyForSnapshotConflict(t *testing.T) {
	resolved := resolveAccountRateMappings(
		context.Background(),
		1,
		[]sub2api.PoolAccount{poolAccount(11, "https://api.example.test/v1", "key", 10)},
		staticAccountRateMappings{items: []AccountRateMapping{
			{TargetID: 1, AccountID: 11, SiteURL: "https://api.example.test", ModelName: "Model One", ManualRate: floatPtr(0.3)},
		}},
		staticChannels{items: []storage.Channel{
			{ID: 7, SiteURL: "https://api.example.test", MonitorEnabled: true},
			{ID: 8, SiteURL: "https://api.example.test", MonitorEnabled: true},
		}},
		staticRateSnapshots{items: map[uint][]storage.RateSnapshot{
			7: {{ChannelID: 7, ModelName: "Model One", Ratio: 0.1}},
			8: {{ChannelID: 8, ModelName: "Model One", Ratio: 0.2}},
		}},
	)
	if resolved[11] != 0.3 {
		t.Fatalf("manual fallback = %#v, want 0.3", resolved)
	}
}

func TestSnapshotShowsKeyMismatchAccountMappingWithoutTrustingIt(t *testing.T) {
	service, _ := newTestService(t, []sub2api.PoolAccount{
		poolAccount(11, "https://api.example.test/v1", "actual-key", 10),
	}, Config{MinimumAccountCount: 1})
	service.matcher.keys = &fakeKeys{
		items: map[uint][]connector.APIKey{
			1: {{ID: 99, GroupRatio: 0.1}},
		},
		revealed: map[string]string{
			keyIDKey(1, 99): "different-key",
		},
	}
	service.SetAccountRateMappingStore(
		staticAccountRateMappings{items: []AccountRateMapping{{
			TargetID:   1,
			AccountID:  11,
			SiteURL:    "https://api.example.test",
			ModelName:  "Model One",
			ManualRate: floatPtr(0.3),
		}}},
		staticRateSnapshots{},
	)

	snapshot, err := service.Snapshot(context.Background(), 1)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	got := snapshot.Accounts[0]
	if got.MatchStatus != "key_mismatch" ||
		got.Availability.Matched ||
		got.UpstreamRate == nil ||
		*got.UpstreamRate != 0.3 ||
		got.UpstreamRateSource != "account_mapping" ||
		got.Availability.RateTrusted {
		t.Fatalf("key mismatch mapping trust boundary = %#v", got)
	}
}

func TestSnapshotKeepsExactKeyRateAheadOfAccountMapping(t *testing.T) {
	service, _ := newTestService(t, []sub2api.PoolAccount{
		poolAccount(11, "https://api.example.test/v1", "exact-key", 10),
	}, Config{MinimumAccountCount: 1})
	service.SetAccountRateMappingStore(
		staticAccountRateMappings{items: []AccountRateMapping{{
			TargetID:   1,
			AccountID:  11,
			SiteURL:    "https://api.example.test",
			ModelName:  "Model One",
			ManualRate: floatPtr(0.3),
		}}},
		staticRateSnapshots{},
	)

	snapshot, err := service.Snapshot(context.Background(), 1)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	got := snapshot.Accounts[0]
	if got.MatchStatus != "key_exact" || got.UpstreamRate == nil || *got.UpstreamRate != 0.1 {
		t.Fatalf("exact key rate was replaced by mapping: %#v", got)
	}
}

func TestMergeUnavailableMatchesKeepsLastDisplayValuesUntrusted(t *testing.T) {
	raw := poolAccount(11, "https://api.example.test/v1", "exact-key", 10)
	rate := 0.1
	balance := 25.0
	cached := &Snapshot{Accounts: []AccountSnapshot{{
		ID:                 11,
		UpstreamURL:        "https://api.example.test",
		UpstreamRate:       &rate,
		UpstreamRateSource: "upstream_api_key",
		Balance:            &balance,
		MatchStatus:        "key_exact",
		FingerprintState:   "present",
		IdentityDigest: identityDigest(
			normalizeURL(raw.Identity().BaseURL),
			raw.Identity().APIKeySHA256,
		),
		Availability: Availability{
			Matched: true, RateAvailable: true, RateTrusted: true, BalanceAvailable: true,
		},
	}}}
	current := map[int64]upstreamMatch{
		11: {status: "upstream_unavailable", fingerprint: "present"},
	}

	mergeUnavailableMatches([]sub2api.PoolAccount{raw}, current, cached)

	got := current[11]
	if got.rate == nil || *got.rate != rate ||
		got.balance == nil || *got.balance != balance ||
		got.rateSource != "upstream_api_key" ||
		got.rateTrusted {
		t.Fatalf("merged unavailable match = %#v", got)
	}
}

func TestMergeUnavailableMatchesKeepsLastRateOnKeyMismatch(t *testing.T) {
	raw := poolAccount(11, "https://api.example.test/v1", "exact-key", 10)
	rate := 0.1
	cached := &Snapshot{Accounts: []AccountSnapshot{{
		ID:               11,
		UpstreamURL:      "https://api.example.test",
		UpstreamRate:     &rate,
		MatchStatus:      "key_exact",
		FingerprintState: "present",
		IdentityDigest: identityDigest(
			normalizeURL(raw.Identity().BaseURL),
			raw.Identity().APIKeySHA256,
		),
		Availability: Availability{
			RateAvailable: true,
			RateTrusted:   true,
		},
	}}}
	current := map[int64]upstreamMatch{
		11: {
			status:         "key_mismatch",
			fingerprint:    "present",
			identityDigest: identityDigest(normalizeURL(raw.Identity().BaseURL), raw.Identity().APIKeySHA256),
		},
	}

	mergeUnavailableMatches([]sub2api.PoolAccount{raw}, current, cached)

	got := current[11]
	if got.rate == nil || *got.rate != rate || got.rateTrusted {
		t.Fatalf("key mismatch cached rate = %#v, want untrusted %v", got, rate)
	}
}

type staticAccountRateMappings struct {
	items []AccountRateMapping
}

func poolAccountNamedForRateMap(id int64, baseURL, name string) sub2api.PoolAccount {
	account := poolAccount(id, baseURL, "mapping-test-key", 10)
	account.Account.Name = name
	return account
}

func (s staticAccountRateMappings) ListAccountRateMappings(uint) ([]AccountRateMapping, error) {
	return append([]AccountRateMapping(nil), s.items...), nil
}

type staticChannels struct {
	items []storage.Channel
}

func (s staticChannels) List() ([]storage.Channel, error) {
	return append([]storage.Channel(nil), s.items...), nil
}

type staticRateSnapshots struct {
	items map[uint][]storage.RateSnapshot
}

func (s staticRateSnapshots) ListByChannel(channelID uint) ([]storage.RateSnapshot, error) {
	return append([]storage.RateSnapshot(nil), s.items[channelID]...), nil
}
