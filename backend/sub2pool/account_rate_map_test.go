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

type staticAccountRateMappings struct {
	items []AccountRateMapping
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
