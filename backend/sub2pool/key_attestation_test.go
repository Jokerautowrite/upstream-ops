package sub2pool

import (
	"context"
	"testing"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
)

func TestGormKeyAttestationUpsertAndList(t *testing.T) {
	_, store := newRecoveryGormStore(t)
	first := keyHash("first-current-key")
	if err := store.UpsertKeyAttestations([]KeyAttestation{{
		TargetID:     1,
		AccountID:    11,
		APIKeySHA256: first,
		ChannelID:    7,
		Source:       "operator_attested",
	}}); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}

	second := keyHash("second-current-key")
	if err := store.UpsertKeyAttestations([]KeyAttestation{{
		TargetID:     1,
		AccountID:    11,
		APIKeySHA256: second,
		ChannelID:    8,
		Source:       "operator_attested",
	}}); err != nil {
		t.Fatalf("replacement upsert: %v", err)
	}

	rows, err := store.ListKeyAttestations(1)
	if err != nil {
		t.Fatalf("list attestations: %v", err)
	}
	if len(rows) != 1 || rows[0].AccountID != 11 || rows[0].ChannelID != 8 || rows[0].APIKeySHA256 != second {
		t.Fatalf("upserted rows = %#v", rows)
	}
}

func TestResolveKeyAttestationsRejectsStaleDisabledAndWrongOriginBindings(t *testing.T) {
	accounts := []sub2api.PoolAccount{
		poolAccount(11, "https://api.example.test/v1", "current-key", 10),
		poolAccount(12, "https://api.example.test/v1", "rotated-key", 20),
		poolAccount(13, "https://api.example.test/v1", "disabled-key", 30),
		poolAccount(14, "https://other.example.test/v1", "wrong-origin-key", 40),
	}
	store := staticKeyAttestations{items: []KeyAttestation{
		{TargetID: 1, AccountID: 11, APIKeySHA256: keyHash("current-key"), ChannelID: 1},
		{TargetID: 1, AccountID: 12, APIKeySHA256: keyHash("old-key"), ChannelID: 1},
		{TargetID: 1, AccountID: 13, APIKeySHA256: keyHash("disabled-key"), ChannelID: 2},
		{TargetID: 1, AccountID: 14, APIKeySHA256: keyHash("wrong-origin-key"), ChannelID: 3},
	}}
	channels := staticChannels{items: []storage.Channel{
		{ID: 1, SiteURL: "https://api.example.test", MonitorEnabled: true},
		{ID: 2, SiteURL: "https://api.example.test", MonitorEnabled: false},
		{ID: 3, SiteURL: "https://api.example.test", MonitorEnabled: true},
	}}

	resolved := resolveKeyAttestations(1, accounts, store, channels)
	if len(resolved) != 1 || resolved[11].channel.ID != 1 {
		t.Fatalf("resolved attestations = %#v", resolved)
	}
}

func TestAttestKeyMappingsUsesCurrentFingerprintAndTrustedPriorityPath(t *testing.T) {
	service, admin := newTestService(t, []sub2api.PoolAccount{
		poolAccount(11, "https://api.example.test/v1", "current-key", 10),
	}, Config{MinimumAccountCount: 1})
	store := &memoryKeyAttestations{}
	service.SetKeyAttestationStore(store)
	service.matcher.keys = &fakeKeys{
		items:    map[uint][]connector.APIKey{1: {{ID: 99, GroupRatio: 0.1}}},
		revealed: map[string]string{keyIDKey(1, 99): "different-key"},
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

	if _, err := service.AttestKeyMappings(context.Background(), 1, []KeyAttestationInput{{AccountID: 11, ChannelID: 1}}); err != nil {
		t.Fatalf("attest current key: %v", err)
	}
	snapshot, preview, err := service.SnapshotPreview(context.Background(), 1)
	if err != nil {
		t.Fatalf("snapshot after attestation: %v", err)
	}
	account := snapshot.Accounts[0]
	if account.MatchStatus != "key_attested" || account.MultiplierSource != "key_attested" ||
		!account.Availability.Matched || account.UpstreamRate == nil || *account.UpstreamRate != 0.3 {
		t.Fatalf("attested account = %#v", account)
	}
	if len(preview.Proposals) != 1 || preview.Proposals[0].AccountID != 11 {
		t.Fatalf("attested account did not enter priority preview: %#v", preview)
	}

	admin.accounts[11] = poolAccount(11, "https://api.example.test/v1", "rotated-key", 10)
	snapshot, err = service.Snapshot(context.Background(), 1)
	if err != nil {
		t.Fatalf("snapshot after key rotation: %v", err)
	}
	account = snapshot.Accounts[0]
	if account.MatchStatus == "key_attested" || account.Availability.Matched {
		t.Fatalf("rotated key retained stale attestation: %#v", account)
	}
}

func TestAttestKeyMappingsRejectsDisabledOrCrossOriginChannel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		channel storage.Channel
	}{
		{
			name:    "disabled",
			channel: storage.Channel{ID: 1, SiteURL: "https://api.example.test", MonitorEnabled: false},
		},
		{
			name:    "cross_origin",
			channel: storage.Channel{ID: 1, SiteURL: "https://other.example.test", MonitorEnabled: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _ := newTestService(t, []sub2api.PoolAccount{
				poolAccount(11, "https://api.example.test/v1", "current-key", 10),
			}, Config{MinimumAccountCount: 1})
			service.SetKeyAttestationStore(&memoryKeyAttestations{})
			service.matcher.channels = &fakeChannels{items: []storage.Channel{tc.channel}}

			if _, err := service.AttestKeyMappings(context.Background(), 1, []KeyAttestationInput{{AccountID: 11, ChannelID: 1}}); !isPublicError(err, ErrInvalidInput.Code) {
				t.Fatalf("attest error = %v, want invalid input", err)
			}
		})
	}
}

type staticKeyAttestations struct {
	items []KeyAttestation
}

func (s staticKeyAttestations) ListKeyAttestations(uint) ([]KeyAttestation, error) {
	return append([]KeyAttestation(nil), s.items...), nil
}

func (staticKeyAttestations) UpsertKeyAttestations([]KeyAttestation) error {
	return nil
}

type memoryKeyAttestations struct {
	items []KeyAttestation
}

func (s *memoryKeyAttestations) ListKeyAttestations(targetID uint) ([]KeyAttestation, error) {
	out := make([]KeyAttestation, 0, len(s.items))
	for _, item := range s.items {
		if item.TargetID == targetID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *memoryKeyAttestations) UpsertKeyAttestations(items []KeyAttestation) error {
	for _, item := range items {
		replaced := false
		for index, existing := range s.items {
			if existing.TargetID == item.TargetID && existing.AccountID == item.AccountID {
				s.items[index] = item
				replaced = true
				break
			}
		}
		if !replaced {
			s.items = append(s.items, item)
		}
	}
	return nil
}
