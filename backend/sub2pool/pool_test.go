package sub2pool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPriorityPreviewAssignsUniquePrioritiesForEqualRates(t *testing.T) {
	snapshot := Snapshot{
		TargetID: 1,
		Accounts: []AccountSnapshot{
			priorityAccount(2, ChannelPLUS, 90, floatPtr(0.05), floatPtr(20)),
			priorityAccount(1, ChannelPLUS, 80, floatPtr(0.05), floatPtr(20)),
			priorityAccount(3, ChannelPLUS, 70, floatPtr(0.10), floatPtr(20)),
		},
	}
	preview := buildPriorityPreview(snapshot)
	if got := proposalTargets(preview.Proposals); got[1] != 10 || got[2] != 20 || got[3] != 30 {
		t.Fatalf("targets = %#v", got)
	}
	if len(validateProposalTargets(snapshot.Accounts, preview.Proposals)) != 0 {
		t.Fatalf("proposals unexpectedly invalid: %#v", preview.Proposals)
	}
}

func TestPriorityTransitionAllowsPreexistingDuplicatesOutsideIntent(t *testing.T) {
	first := priorityAccount(1, ChannelPLUS, 100, nil, floatPtr(20))
	second := priorityAccount(2, ChannelPLUS, 100, nil, floatPtr(20))
	changing := priorityAccount(3, ChannelPLUS, 90, floatPtr(0.05), floatPtr(20))
	changing.Status = "active"
	changing.IdentityDigest = "identity-3"
	proposal := PriorityProposal{
		AccountID:              changing.ID,
		CurrentPriority:        changing.CurrentPriority,
		TargetPriority:         10,
		Channel:                changing.Channel,
		expectedGroupIDs:       append([]int64(nil), changing.GroupIDs...),
		expectedStatus:         changing.Status,
		expectedPoolManaged:    changing.PoolManaged,
		expectedIdentityDigest: changing.IdentityDigest,
	}

	intent, err := preparePriorityTransitionIntent(
		[]AccountSnapshot{first, second, changing},
		[]PriorityProposal{proposal},
	)
	if err != nil {
		t.Fatalf("preexisting duplicates blocked an isolated transition: %v", err)
	}
	if len(intent) != 1 || intent[0].stagingPriority <= 100 {
		t.Fatalf("intent = %#v", intent)
	}
	if err := validatePriorityTransitionIntent([]AccountSnapshot{first, second, changing}, intent); err != nil {
		t.Fatalf("persisted intent rejected preexisting duplicates: %v", err)
	}
}

func TestPriorityTransitionRejectsTargetCollisionWithUnchangedAccount(t *testing.T) {
	unchanged := priorityAccount(1, ChannelPLUS, 20, nil, floatPtr(20))
	changing := priorityAccount(2, ChannelPLUS, 90, floatPtr(0.05), floatPtr(20))
	changing.Status = "active"
	changing.IdentityDigest = "identity-2"
	proposal := PriorityProposal{
		AccountID:              changing.ID,
		CurrentPriority:        changing.CurrentPriority,
		TargetPriority:         unchanged.CurrentPriority,
		Channel:                changing.Channel,
		expectedGroupIDs:       append([]int64(nil), changing.GroupIDs...),
		expectedStatus:         changing.Status,
		expectedPoolManaged:    changing.PoolManaged,
		expectedIdentityDigest: changing.IdentityDigest,
	}

	if _, err := preparePriorityTransitionIntent(
		[]AccountSnapshot{unchanged, changing},
		[]PriorityProposal{proposal},
	); err == nil {
		t.Fatal("target collision with unchanged account was accepted")
	}
}

func TestPreviewSignatureIgnoresFundedBalanceDrift(t *testing.T) {
	before := Snapshot{
		TargetID: 1,
		Accounts: []AccountSnapshot{
			priorityAccount(1, ChannelPLUS, 10, floatPtr(0.05), floatPtr(20)),
		},
	}
	after := before
	after.Accounts = append([]AccountSnapshot(nil), before.Accounts...)
	after.Accounts[0].Balance = floatPtr(19.99)

	beforePreview := buildPriorityPreview(before)
	afterPreview := buildPriorityPreview(after)
	if beforePreview.Signature != afterPreview.Signature {
		t.Fatalf("funded balance drift changed signature: %s != %s", beforePreview.Signature, afterPreview.Signature)
	}
}

func TestPreviewSignatureChangesWhenBalanceCrossesDebtBoundary(t *testing.T) {
	before := Snapshot{
		TargetID: 1,
		Accounts: []AccountSnapshot{
			priorityAccount(1, ChannelPLUS, 10, floatPtr(0.05), floatPtr(20)),
		},
	}
	after := before
	after.Accounts = append([]AccountSnapshot(nil), before.Accounts...)
	after.Accounts[0].Balance = floatPtr(0)

	beforePreview := buildPriorityPreview(before)
	afterPreview := buildPriorityPreview(after)
	if beforePreview.Signature == afterPreview.Signature {
		t.Fatal("debt boundary change did not change signature")
	}
}

func TestPreviewSignatureIgnoresUnmatchedReasonDrift(t *testing.T) {
	before := Snapshot{
		TargetID: 1,
		Accounts: []AccountSnapshot{
			priorityAccount(1, ChannelPLUS, 10, nil, nil),
		},
	}
	before.Accounts[0].Availability.Matched = false
	before.Accounts[0].MatchStatus = "upstream_unavailable"
	after := before
	after.Accounts = append([]AccountSnapshot(nil), before.Accounts...)
	after.Accounts[0].MatchStatus = "key_mismatch"

	beforePreview := buildPriorityPreview(before)
	afterPreview := buildPriorityPreview(after)
	if beforePreview.Signature != afterPreview.Signature {
		t.Fatalf("unmatched reason drift changed signature: %s != %s", beforePreview.Signature, afterPreview.Signature)
	}
}

func TestPreviewSignatureChangesWhenMatchAvailabilityChanges(t *testing.T) {
	before := Snapshot{
		TargetID: 1,
		Accounts: []AccountSnapshot{
			priorityAccount(1, ChannelPLUS, 10, nil, nil),
		},
	}
	before.Accounts[0].Availability.Matched = false
	before.Accounts[0].MatchStatus = "key_mismatch"
	after := before
	after.Accounts = append([]AccountSnapshot(nil), before.Accounts...)
	after.Accounts[0].Availability.Matched = true
	after.Accounts[0].MatchStatus = "key_exact"

	beforePreview := buildPriorityPreview(before)
	afterPreview := buildPriorityPreview(after)
	if beforePreview.Signature == afterPreview.Signature {
		t.Fatal("match availability change did not change signature")
	}
}

func TestPriorityPreviewAvoidsCrossChannelCollisionsInSharedGroups(t *testing.T) {
	kiro := priorityAccount(1, ChannelKiro, 90, floatPtr(0.05), floatPtr(20))
	kiro.GroupIDs = []int64{1, 2}
	plus := priorityAccount(2, ChannelPLUS, 80, floatPtr(0.05), floatPtr(20))
	plus.GroupIDs = []int64{2}
	debt := priorityAccount(3, ChannelPLUS, 70, floatPtr(0.01), floatPtr(0))
	debt.GroupIDs = []int64{1}
	snapshot := Snapshot{
		TargetID: 1,
		Accounts: []AccountSnapshot{kiro, plus, debt},
	}
	preview := buildPriorityPreview(snapshot)
	targets := proposalTargets(preview.Proposals)
	if targets[1] != 10 || targets[2] != 20 || targets[3] != 30 {
		t.Fatalf("targets = %#v", targets)
	}
	if violations := validateProposalTargets(snapshot.Accounts, preview.Proposals); len(violations) != 0 {
		t.Fatalf("shared-group proposals invalid: %#v", violations)
	}
}

func TestPriorityPreviewPlacesDebtLastAndAllowsDebtWithoutMultiplier(t *testing.T) {
	snapshot := Snapshot{
		TargetID: 1,
		Accounts: []AccountSnapshot{
			priorityAccount(1, ChannelCC, 100, floatPtr(0.1), floatPtr(30)),
			priorityAccount(2, ChannelCC, 999, floatPtr(0.2), floatPtr(0)),
			priorityAccount(3, ChannelCC, 888, nil, floatPtr(-1)),
			priorityAccount(4, ChannelCC, 100, nil, floatPtr(10)),
		},
	}
	preview := buildPriorityPreview(snapshot)
	got := proposalTargets(preview.Proposals)
	if got[1] != 10 || got[2] != 110 || got[3] != 120 {
		t.Fatalf("targets = %#v", got)
	}
	if _, exists := got[4]; exists {
		t.Fatalf("positive-balance account without rate was proposed: %#v", got)
	}
	if len(preview.MissingMultiplierIDs) != 2 {
		t.Fatalf("missing multiplier ids = %#v", preview.MissingMultiplierIDs)
	}
}

func TestPriorityPreviewPreservesUnknownAndUntrustedAccounts(t *testing.T) {
	known := priorityAccount(1, ChannelPLUS, 90, floatPtr(0.10), floatPtr(20))
	discovery := priorityAccount(2, ChannelPLUS, 10, floatPtr(0.01), floatPtr(20))
	discovery.DiscoveryManaged = true
	untrusted := priorityAccount(3, ChannelPLUS, 20, floatPtr(0.01), floatPtr(20))
	untrusted.MultiplierSource = "display_only"
	unknown := priorityAccount(4, ChannelOther, 30, floatPtr(0.05), floatPtr(20))

	snapshot := Snapshot{
		TargetID: 1,
		Accounts: []AccountSnapshot{
			known,
			discovery,
			untrusted,
			unknown,
		},
	}
	preview := buildPriorityPreview(snapshot)
	targets := proposalTargets(preview.Proposals)

	if len(targets) != 1 || targets[known.ID] != 40 {
		t.Fatalf("targets = %#v", targets)
	}
	if len(preview.UnknownChannelIDs) != 0 {
		t.Fatalf("unknown channels blocked the preview: %#v", preview.UnknownChannelIDs)
	}
	if len(preview.MissingMultiplierIDs) != 1 || preview.MissingMultiplierIDs[0] != untrusted.ID {
		t.Fatalf("missing multiplier ids = %#v", preview.MissingMultiplierIDs)
	}
	if violations := validateGuards(snapshot, preview, emptyTargetState(), Config{MinimumAccountCount: 1}.withDefaults()); len(violations) != 0 {
		t.Fatalf("skipped accounts unexpectedly blocked automation: %#v", violations)
	}
}

func TestMatcherUsesFullKeyHashBeforeURLAndNeverFallsBackAfterMismatch(t *testing.T) {
	channels := &fakeChannels{items: []storage.Channel{{
		ID:             7,
		SiteURL:        "https://api.example.test/v1/",
		MonitorEnabled: true,
	}}}
	keys := &fakeKeys{
		items: map[uint][]connector.APIKey{
			7: {
				{ID: 11, GroupRatio: 0.05},
				{ID: 12, GroupRatio: 0.65},
			},
		},
		revealed: map[string]string{
			"7:11": "key-plus",
			"7:12": "key-cc",
		},
	}
	m := newMatcher(channels, keys)
	accounts := []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-plus", 10),
		poolAccount(2, "https://api.example.test/v1", "key-cc", 20),
	}
	matches, err := m.matchAccounts(context.Background(), accounts)
	if err != nil {
		t.Fatalf("match accounts: %v", err)
	}
	if matches[1].rate == nil || *matches[1].rate != 0.05 || matches[2].rate == nil || *matches[2].rate != 0.65 {
		t.Fatalf("same-url matches = %#v", matches)
	}
	if matches[1].status != "key_exact" || matches[2].status != "key_exact" {
		t.Fatalf("match statuses = %#v", matches)
	}

	mismatch := poolAccount(3, "https://api.example.test/v1", "", 30)
	mismatch.APIKeyFingerprint = keyHash("key-other")
	matches, err = m.matchAccounts(context.Background(), []sub2api.PoolAccount{mismatch})
	if err != nil {
		t.Fatalf("match mismatch account: %v", err)
	}
	if matches[3].status != "key_mismatch" || matches[3].matched {
		t.Fatalf("hash mismatch fell back unexpectedly: %#v", matches[3])
	}
}

func TestMatcherSeparatesMissingMonitorSourceFromKeyMismatch(t *testing.T) {
	channels := &fakeChannels{items: []storage.Channel{{
		ID:             7,
		SiteURL:        "https://other.example.test",
		MonitorEnabled: true,
	}}}
	keys := &fakeKeys{
		items:    map[uint][]connector.APIKey{},
		revealed: map[string]string{},
	}
	matches, err := newMatcher(channels, keys).matchAccounts(context.Background(), []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "valid-but-unmonitored-key", 10),
	})
	if err != nil {
		t.Fatalf("match accounts: %v", err)
	}
	if got := matches[1]; got.status != "monitor_source_missing" || got.matched {
		t.Fatalf("missing monitor source match = %#v", got)
	}
}

func TestMatcherUsesSameOriginKeyAcrossURLPath(t *testing.T) {
	channels := &fakeChannels{items: []storage.Channel{{
		ID:             7,
		SiteURL:        "https://api.example.test",
		MonitorEnabled: true,
	}}}
	keys := &fakeKeys{
		items: map[uint][]connector.APIKey{
			7: {{ID: 11, GroupRatio: 0.05}},
		},
		revealed: map[string]string{"7:11": "key-plus"},
	}
	matches, err := newMatcher(channels, keys).matchAccounts(context.Background(), []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-plus", 10),
	})
	if err != nil {
		t.Fatalf("match accounts: %v", err)
	}
	if matches[1].status != "key_exact" || !matches[1].matched || matches[1].rate == nil || *matches[1].rate != 0.05 {
		t.Fatalf("same-origin key did not allow URL path difference: %#v", matches[1])
	}
}

func TestMatcherMatchesUniqueKeyAcrossOrigin(t *testing.T) {
	// A full API-key hash is a stronger identity than base_url. When the key is
	// unique in the monitor inventory, accept it even if Sub2 stored a different host.
	channels := &fakeChannels{items: []storage.Channel{{
		ID:             7,
		SiteURL:        "https://first.example.test",
		MonitorEnabled: true,
	}}}
	keys := &fakeKeys{
		items:    map[uint][]connector.APIKey{7: {{ID: 11, GroupRatio: 0.05}}},
		revealed: map[string]string{"7:11": "shared-key"},
	}
	matches, err := newMatcher(channels, keys).matchAccounts(context.Background(), []sub2api.PoolAccount{
		poolAccount(1, "https://second.example.test/v1", "shared-key", 10),
	})
	if err != nil {
		t.Fatalf("match accounts: %v", err)
	}
	if matches[1].status != "key_exact" || !matches[1].matched || matches[1].rate == nil || *matches[1].rate != 0.05 {
		t.Fatalf("unique cross-origin key should match: %#v", matches[1])
	}
}

func TestMatcherMatchesUniqueChannelNameWhenKeyMissing(t *testing.T) {
	channels := &fakeChannels{items: []storage.Channel{{
		ID:             11,
		Name:           "生图专用老登",
		SiteURL:        "https://img.example.test",
		MonitorEnabled: true,
		LastBalance:    floatPtr(12),
	}}}
	keys := &fakeKeys{
		items:    map[uint][]connector.APIKey{11: {{ID: 1, GroupRatio: 1.0}}},
		revealed: map[string]string{"11:1": "other-key"},
	}
	m := newMatcher(channels, keys)
	m.setRates(staticRateSnapshots{items: map[uint][]storage.RateSnapshot{
		11: {{ChannelID: 11, ModelName: "gpt-image-2", Ratio: 1.0}},
	}})
	account := poolAccount(2782, "https://img.example.test", "pool-key", 10)
	account.Account.Name = "生图专用老登"
	matches, err := m.matchAccounts(context.Background(), []sub2api.PoolAccount{account})
	if err != nil {
		t.Fatalf("match accounts: %v", err)
	}
	got := matches[2782]
	if got.status != "channel_name_exact" || got.rate == nil || *got.rate != 1.0 {
		t.Fatalf("name match failed: %#v", got)
	}
	if got.balance == nil || *got.balance != 12 {
		t.Fatalf("balance not filled from channel: %#v", got)
	}
}

func TestMatcherDoesNotFallbackToURLWithoutKeyFingerprint(t *testing.T) {
	channels := &fakeChannels{items: []storage.Channel{{
		ID:             7,
		SiteURL:        "https://api.example.test/v1",
		MonitorEnabled: true,
	}}}
	keys := &fakeKeys{
		items: map[uint][]connector.APIKey{
			7: {
				{ID: 11, GroupRatio: 0.05},
				{ID: 12, GroupRatio: 0.05},
			},
		},
		revealed: map[string]string{
			"7:11": "first-key",
			"7:12": "second-key",
		},
	}
	m := newMatcher(channels, keys)
	account := sub2api.PoolAccount{Account: sub2api.AdminAccount{
		ID:          1,
		Credentials: map[string]any{"base_url": "https://api.example.test/v1"},
	}}
	matches, err := m.matchAccounts(context.Background(), []sub2api.PoolAccount{account})
	if err != nil {
		t.Fatalf("match accounts: %v", err)
	}
	if matches[1].status != "fingerprint_missing" || matches[1].matched {
		t.Fatalf("key-less account used URL fallback: %#v", matches[1])
	}

	keys.items[7][1].GroupRatio = 0.15
	matches, err = m.matchAccounts(context.Background(), []sub2api.PoolAccount{account})
	if err != nil {
		t.Fatalf("match unequal keys: %v", err)
	}
	if matches[1].status != "fingerprint_missing" || matches[1].matched {
		t.Fatalf("key-less account changed after URL candidates changed: %#v", matches[1])
	}
}

func TestMatcherIgnoresDisabledChannels(t *testing.T) {
	channels := &fakeChannels{items: []storage.Channel{{
		ID:             7,
		SiteURL:        "https://api.example.test/v1",
		MonitorEnabled: false,
		LastBalance:    floatPtr(50),
	}}}
	keys := &fakeKeys{
		items:    map[uint][]connector.APIKey{7: {{ID: 11, GroupRatio: 0.05}}},
		revealed: map[string]string{"7:11": "matching-key"},
	}
	matches, err := newMatcher(channels, keys).matchAccounts(context.Background(), []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "matching-key", 10),
	})
	if err != nil {
		t.Fatalf("match accounts: %v", err)
	}
	if matches[1].status != "monitor_source_missing" || matches[1].matched || matches[1].rate != nil || matches[1].balance != nil {
		t.Fatalf("disabled channel affected matcher: %#v", matches[1])
	}
}

func TestPriorityPreviewDebtFloorProtectsExcludedSchedulableAccounts(t *testing.T) {
	snapshot := Snapshot{
		TargetID: 1,
		Accounts: []AccountSnapshot{
			priorityAccount(1, ChannelCC, 10, floatPtr(0.1), floatPtr(50)),
			priorityAccount(2, ChannelCC, 90, floatPtr(0.2), nil),
			priorityAccount(3, ChannelCC, 80, nil, floatPtr(40)),
			priorityAccount(4, ChannelCC, 990, floatPtr(0.3), floatPtr(0)),
		},
	}
	preview := buildPriorityPreview(snapshot)
	targets := proposalTargets(preview.Proposals)
	if targets[1] != 10 || targets[4] != 100 {
		t.Fatalf("targets = %#v, want funded=10 debt=100", targets)
	}
	if _, exists := targets[2]; exists {
		t.Fatalf("missing balance account should remain unranked: %#v", targets)
	}
	if _, exists := targets[3]; exists {
		t.Fatalf("funded missing-rate account should remain unranked: %#v", targets)
	}
}

func TestPriorityPreviewReservesUnmanagedAccountPriority(t *testing.T) {
	unmanaged := priorityAccount(1, ChannelPLUS, 10, floatPtr(0.01), floatPtr(50))
	unmanaged.PoolManaged = false
	snapshot := Snapshot{
		TargetID: 1,
		Accounts: []AccountSnapshot{
			unmanaged,
			priorityAccount(2, ChannelPLUS, 80, floatPtr(0.02), floatPtr(50)),
		},
	}
	preview := buildPriorityPreview(snapshot)
	targets := proposalTargets(preview.Proposals)
	if _, exists := targets[1]; exists {
		t.Fatalf("unmanaged account received proposal: %#v", targets)
	}
	if targets[2] != 20 {
		t.Fatalf("managed target = %d, want 20 because priority 10 is reserved", targets[2])
	}
}

func TestMatcherTreatsConflictingExactKeyCandidatesAsAmbiguous(t *testing.T) {
	channels := &fakeChannels{items: []storage.Channel{
		{ID: 1, SiteURL: "https://api.example.test/v1", MonitorEnabled: true, LastBalance: floatPtr(50)},
		{ID: 2, SiteURL: "https://api.example.test/v1", MonitorEnabled: true, LastBalance: floatPtr(50)},
	}}
	keys := &fakeKeys{
		items: map[uint][]connector.APIKey{
			1: {{ID: 11, GroupRatio: 0.05}},
			2: {{ID: 22, GroupRatio: 0.15}},
		},
		revealed: map[string]string{
			"1:11": "same-key",
			"2:22": "same-key",
		},
	}
	matches, err := newMatcher(channels, keys).matchAccounts(context.Background(), []sub2api.PoolAccount{
		poolAccount(7, "https://api.example.test/v1", "same-key", 10),
	})
	if err != nil {
		t.Fatalf("match accounts: %v", err)
	}
	if matches[7].status != "key_ambiguous" || matches[7].matched {
		t.Fatalf("conflicting exact-key candidates = %#v", matches[7])
	}
}

func TestMatcherReportsUnavailableWhenKeyRevealFails(t *testing.T) {
	bal := 246.35
	channels := &fakeChannels{items: []storage.Channel{{
		ID:             1,
		SiteURL:        "https://api.example.test/v1",
		MonitorEnabled: true,
		LastBalance:    &bal,
	}}}
	keys := &fakeKeys{
		items:    map[uint][]connector.APIKey{1: {{ID: 11, GroupRatio: 0.05}}},
		revealed: map[string]string{},
	}
	matches, err := newMatcher(channels, keys).matchAccounts(context.Background(), []sub2api.PoolAccount{
		poolAccount(7, "https://api.example.test/v1", "same-key", 10),
	})
	if err != nil {
		t.Fatalf("match accounts: %v", err)
	}
	// Key 对不上（reveal 失败）仍标 upstream_unavailable，但同站余额必须能补上。
	if matches[7].status != "upstream_unavailable" || matches[7].matched {
		t.Fatalf("reveal failure was misclassified: %#v", matches[7])
	}
	if matches[7].balance == nil || *matches[7].balance != bal {
		t.Fatalf("expected URL balance after reveal failure, got %#v", matches[7])
	}
}

func TestMatcherLoadsChannelsConcurrentlyWithBoundedWorkers(t *testing.T) {
	const channelCount = 8
	channels := make([]storage.Channel, 0, channelCount)
	items := make(map[uint][]connector.APIKey, channelCount)
	revealed := make(map[string]string, channelCount)
	for id := 1; id <= channelCount; id++ {
		channelID := uint(id)
		channels = append(channels, storage.Channel{
			ID:             channelID,
			SiteURL:        fmt.Sprintf("https://channel-%d.example.test", id),
			MonitorEnabled: true,
		})
		items[channelID] = []connector.APIKey{{ID: int64(id), GroupRatio: 0.05}}
		revealed[keyIDKey(channelID, int64(id))] = fmt.Sprintf("key-%d", id)
	}
	keys := &delayedKeys{
		fakeKeys: fakeKeys{items: items, revealed: revealed},
		delay:    20 * time.Millisecond,
	}

	_, err := newMatcher(&fakeChannels{items: channels}, keys).matchAccounts(
		context.Background(),
		[]sub2api.PoolAccount{poolAccount(1, "https://channel-1.example.test", "key-1", 10)},
	)
	if err != nil {
		t.Fatalf("match accounts: %v", err)
	}
	if keys.maxActive < 2 {
		t.Fatalf("max concurrent channel reads = %d, want at least 2", keys.maxActive)
	}
	if keys.maxActive > matcherChannelWorkerLimit {
		t.Fatalf("max concurrent channel reads = %d, worker limit = %d", keys.maxActive, matcherChannelWorkerLimit)
	}
}

func TestApplyRejectsChangedPreviewWithoutWriting(t *testing.T) {
	service, admin := newTestService(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
	}, Config{MinimumAccountCount: 1})
	preview, err := service.Preview(context.Background(), 1)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	admin.setPriority(1, 60)
	_, err = service.Apply(context.Background(), 1, ApplyInput{
		Signature: preview.Signature,
		Proposals: preview.Proposals,
	})
	if !isPublicError(err, ErrPreviewConflict.Code) {
		t.Fatalf("apply error = %v, want preview conflict", err)
	}
	if admin.updateCount != 0 {
		t.Fatalf("updates = %d, want 0", admin.updateCount)
	}
}

func TestApplyKeepsPreparedTransitionWhenStagingWriteFails(t *testing.T) {
	store := NewMemoryStateStore()
	service, admin := newTestServiceWithState(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
		poolAccount(2, "https://api.example.test/v1", "key-2", 80),
	}, Config{MinimumAccountCount: 1}, store)
	admin.failUpdate[2] = true
	preview, err := service.Preview(context.Background(), 1)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	_, err = service.Apply(context.Background(), 1, ApplyInput{
		Signature: preview.Signature,
		Proposals: preview.Proposals,
	})
	if !isPublicError(err, ErrUnavailable.Code) {
		t.Fatalf("apply error=%v, want pool unavailable", err)
	}
	prepared, err := store.ListPreparedRuns(1, 1)
	if err != nil || len(prepared) != 1 {
		t.Fatalf("prepared runs=%#v err=%v", prepared, err)
	}
	if got := admin.priority(1); got <= 90 {
		t.Fatalf("account 1 priority = %d, want staging priority", got)
	}
	if got := admin.priority(2); got != 80 {
		t.Fatalf("account 2 priority = %d, want 80", got)
	}
	admin.failUpdate[2] = false
	recovered, err := service.Run(context.Background(), 1)
	if err != nil {
		t.Fatalf("recover prepared transition: %v", err)
	}
	if recovered.Apply == nil || len(recovered.Apply.Applied) != 2 ||
		admin.priority(1) != 20 || admin.priority(2) != 10 {
		t.Fatalf("recovered=%#v priorities=%d,%d", recovered, admin.priority(1), admin.priority(2))
	}
}

func TestApplyStagesPrioritySwapWithoutDuplicateGroupPriorities(t *testing.T) {
	service, admin := newTestService(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 20),
		poolAccount(2, "https://api.example.test/v1", "key-2", 10),
	}, Config{MinimumAccountCount: 1})
	keys := service.matcher.keys.(*fakeKeys)
	keys.items[1][0].GroupRatio = 0.1
	keys.items[1][1].GroupRatio = 0.2

	preview, err := service.Preview(context.Background(), 1)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Changes) != 2 {
		t.Fatalf("changes=%#v", preview.Changes)
	}
	if _, err := service.Apply(context.Background(), 1, ApplyInput{
		Signature: preview.Signature,
		Proposals: preview.Proposals,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if admin.priority(1) != 10 || admin.priority(2) != 20 {
		t.Fatalf("final priorities=%d,%d", admin.priority(1), admin.priority(2))
	}
	history := admin.priorityHistory()
	if len(history) != 4 {
		t.Fatalf("priority patch count=%d want=4", len(history))
	}
	for index, priorities := range history {
		if !uniquePriorityValues(priorities) {
			t.Fatalf("priority patch %d introduced a duplicate: %#v", index, priorities)
		}
	}
}

func TestPreparedRunRecoveryRecognizesOldStagingAndFinalStates(t *testing.T) {
	cases := []struct {
		name       string
		prepare    func(*fakeAdmin, []PriorityProposal)
		wantWrites int
	}{
		{
			name:       "old",
			prepare:    func(*fakeAdmin, []PriorityProposal) {},
			wantWrites: 4,
		},
		{
			name: "staging",
			prepare: func(admin *fakeAdmin, intent []PriorityProposal) {
				for _, proposal := range intent {
					admin.setPriority(proposal.AccountID, proposal.stagingPriority)
				}
			},
			wantWrites: 2,
		},
		{
			name: "final",
			prepare: func(admin *fakeAdmin, intent []PriorityProposal) {
				for _, proposal := range intent {
					admin.setPriority(proposal.AccountID, proposal.TargetPriority)
				}
			},
			wantWrites: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryStateStore()
			service, admin := newTestServiceWithState(t, []sub2api.PoolAccount{
				poolAccount(1, "https://api.example.test/v1", "key-1", 90),
				poolAccount(2, "https://api.example.test/v1", "key-2", 80),
			}, Config{MinimumAccountCount: 1}, store)
			snapshot, preview, err := service.SnapshotPreview(context.Background(), 1)
			if err != nil {
				t.Fatalf("snapshot preview: %v", err)
			}
			intent := preparedPriorityIntent(t, snapshot.Accounts, preview.Changes)
			if _, err := store.RecordRun(RunRecord{
				TargetID:           1,
				Status:             "prepared",
				PreviewSignature:   preview.Signature,
				ChangeCount:        len(intent),
				NotificationStatus: "pending",
				Intent:             intent,
			}); err != nil {
				t.Fatalf("record prepared run: %v", err)
			}
			tc.prepare(admin, intent)

			result, err := service.Run(context.Background(), 1)
			if err != nil {
				t.Fatalf("recover %s: %v", tc.name, err)
			}
			if result.Apply == nil || len(result.Apply.Applied) != len(intent) {
				t.Fatalf("recovery result=%#v", result)
			}
			if admin.updateCount != tc.wantWrites {
				t.Fatalf("writes=%d want=%d", admin.updateCount, tc.wantWrites)
			}
			if admin.priority(1) != 20 || admin.priority(2) != 10 {
				t.Fatalf("final priorities=%d,%d", admin.priority(1), admin.priority(2))
			}
			for index, priorities := range admin.priorityHistory() {
				if !uniquePriorityValues(priorities) {
					t.Fatalf("priority patch %d introduced a duplicate: %#v", index, priorities)
				}
			}
		})
	}
}

func TestPreparedRunRecoveryFailsClosedForUnknownPriorityState(t *testing.T) {
	store := NewMemoryStateStore()
	service, admin := newTestServiceWithState(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
		poolAccount(2, "https://api.example.test/v1", "key-2", 80),
	}, Config{MinimumAccountCount: 1}, store)
	snapshot, preview, err := service.SnapshotPreview(context.Background(), 1)
	if err != nil {
		t.Fatalf("snapshot preview: %v", err)
	}
	intent := preparedPriorityIntent(t, snapshot.Accounts, preview.Changes)
	if _, err := store.RecordRun(RunRecord{
		TargetID:           1,
		Status:             "prepared",
		PreviewSignature:   preview.Signature,
		ChangeCount:        len(intent),
		NotificationStatus: "pending",
		Intent:             intent,
	}); err != nil {
		t.Fatalf("record prepared run: %v", err)
	}
	admin.setPriority(intent[0].AccountID, 777)

	if _, err := service.Run(context.Background(), 1); !isPublicError(err, ErrPreviewConflict.Code) {
		t.Fatalf("recovery error=%v, want preview conflict", err)
	}
	if admin.updateCount != 0 {
		t.Fatalf("unknown state issued %d priority patches", admin.updateCount)
	}
	prepared, err := store.ListPreparedRuns(1, 1)
	if err != nil || len(prepared) != 1 {
		t.Fatalf("unknown state should remain prepared: runs=%#v err=%v", prepared, err)
	}
}

func TestManualApplyRecoversPreparedRunBeforeCreatingAnother(t *testing.T) {
	store := NewMemoryStateStore()
	service, admin := newTestServiceWithState(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
		poolAccount(2, "https://api.example.test/v1", "key-2", 80),
	}, Config{MinimumAccountCount: 1}, store)
	snapshot, preview, err := service.SnapshotPreview(context.Background(), 1)
	if err != nil {
		t.Fatalf("snapshot preview: %v", err)
	}
	if _, err := store.RecordRun(RunRecord{
		TargetID:           1,
		Status:             "prepared",
		PreviewSignature:   preview.Signature,
		ChangeCount:        len(preview.Changes),
		NotificationStatus: "pending",
		Intent:             preparedPriorityIntent(t, snapshot.Accounts, preview.Changes),
	}); err != nil {
		t.Fatalf("record prepared run: %v", err)
	}

	_, err = service.Apply(context.Background(), 1, ApplyInput{
		Signature: preview.Signature,
		Proposals: preview.Proposals,
	})
	if !isPublicError(err, ErrPreviewConflict.Code) {
		t.Fatalf("apply error=%v want preview conflict after recovery", err)
	}
	if admin.updateCount != len(preview.Changes)*2 {
		t.Fatalf("recovery writes=%d want=%d", admin.updateCount, len(preview.Changes)*2)
	}
	prepared, err := store.ListPreparedRuns(1, 10)
	if err != nil || len(prepared) != 0 {
		t.Fatalf("prepared runs=%#v err=%v", prepared, err)
	}
}

func TestSecondPreviewIsIdempotentAfterSuccessfulApply(t *testing.T) {
	service, _ := newTestService(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
		poolAccount(2, "https://api.example.test/v1", "key-2", 80),
	}, Config{MinimumAccountCount: 1})
	preview, err := service.Preview(context.Background(), 1)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if _, err := service.Apply(context.Background(), 1, ApplyInput{
		Signature: preview.Signature,
		Proposals: preview.Proposals,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	next, err := service.Preview(context.Background(), 1)
	if err != nil {
		t.Fatalf("second preview: %v", err)
	}
	if len(next.Changes) != 0 {
		t.Fatalf("second preview changes = %#v", next.Changes)
	}
}

func TestPreviewGuardsAccountCountAndInvalidProposalTargets(t *testing.T) {
	service, _ := newTestService(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
	}, Config{MinimumAccountCount: 20})
	preview, err := service.Preview(context.Background(), 1)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Guards) == 0 || preview.Guards[0].Code != "account_count_too_low" {
		t.Fatalf("guards = %#v", preview.Guards)
	}
	accounts := []AccountSnapshot{
		{ID: 1, Channel: ChannelPLUS, GroupIDs: []int64{1}},
		{ID: 2, Channel: ChannelPLUS, GroupIDs: []int64{1}},
	}
	violations := validateProposalTargets(accounts, []PriorityProposal{
		{AccountID: 1, Channel: ChannelPLUS, TargetPriority: 10},
		{AccountID: 2, Channel: ChannelPLUS, TargetPriority: 10},
	})
	if len(violations) != 1 || violations[0].Code != "duplicate_channel_target" {
		t.Fatalf("proposal guard = %#v", violations)
	}
}

func TestRunDoesNotRetryWritesWhenNotificationFails(t *testing.T) {
	service, admin := newTestService(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
		poolAccount(2, "https://api.example.test/v1", "key-2", 80),
	}, Config{MinimumAccountCount: 1})
	service.SetDispatcher(failingDispatcher{})
	first, err := service.Run(context.Background(), 1)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !first.NotificationFailed || admin.updateCount != 4 {
		t.Fatalf("first run = %#v, updates=%d", first, admin.updateCount)
	}
	second, err := service.Run(context.Background(), 1)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(second.Preview.Changes) != 0 || admin.updateCount != 4 {
		t.Fatalf("second run = %#v, updates=%d", second, admin.updateCount)
	}
	runs, err := service.ListRuns(1, 10)
	if err != nil || len(runs) != 2 || runs[0].Status != "no_change" {
		t.Fatalf("run records = %#v, err=%v", runs, err)
	}
}

func TestRunWithNoChangesDoesNotWriteOrNotify(t *testing.T) {
	service, admin := newTestService(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 10),
		poolAccount(2, "https://api.example.test/v1", "key-2", 20),
	}, Config{MinimumAccountCount: 1})
	dispatcher := &countingDispatcher{}
	service.SetDispatcher(dispatcher)

	result, err := service.Run(context.Background(), 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Preview.Changes) != 0 || admin.updateCount != 0 || dispatcher.calls != 0 {
		t.Fatalf("result=%#v updates=%d notifications=%d", result, admin.updateCount, dispatcher.calls)
	}
	if result.NotificationStatus != "skipped" {
		t.Fatalf("notification status = %q", result.NotificationStatus)
	}
}

func TestPreparedRunRecoveryDoesNotReplayCompletedWrites(t *testing.T) {
	store := NewMemoryStateStore()
	service, admin := newTestServiceWithState(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
		poolAccount(2, "https://api.example.test/v1", "key-2", 80),
	}, Config{MinimumAccountCount: 1}, store)
	dispatcher := &countingDispatcher{}
	service.SetDispatcher(dispatcher)

	snapshot, preview, err := service.SnapshotPreview(context.Background(), 1)
	if err != nil {
		t.Fatalf("snapshot preview: %v", err)
	}
	runID, err := store.RecordRun(RunRecord{
		TargetID:           1,
		Status:             "prepared",
		PreviewSignature:   preview.Signature,
		ChangeCount:        len(preview.Changes),
		NotificationStatus: "pending",
		Intent:             preparedPriorityIntent(t, snapshot.Accounts, preview.Changes),
	})
	if err != nil {
		t.Fatalf("record prepared run: %v", err)
	}
	for _, proposal := range preview.Changes {
		admin.setPriority(proposal.AccountID, proposal.TargetPriority)
	}

	result, err := service.Run(context.Background(), 1)
	if err != nil {
		t.Fatalf("recover run: %v", err)
	}
	if result.RunID != runID || result.Apply == nil || len(result.Apply.Applied) != len(preview.Changes) {
		t.Fatalf("recovered result = %#v", result)
	}
	if admin.updateCount != 0 {
		t.Fatalf("recovery replayed %d remote writes", admin.updateCount)
	}
	if dispatcher.calls != 1 || result.NotificationStatus != "sent" {
		t.Fatalf("notifications=%d result=%#v", dispatcher.calls, result)
	}
	runs, err := store.ListRuns(1, 1)
	if err != nil || len(runs) != 1 || runs[0].Status != "recovered" || !runs[0].StatePersisted {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
}

func TestPreparedRunRecoverySafelyCompletesUnwrittenIntent(t *testing.T) {
	store := NewMemoryStateStore()
	service, admin := newTestServiceWithState(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
		poolAccount(2, "https://api.example.test/v1", "key-2", 80),
	}, Config{MinimumAccountCount: 1}, store)

	snapshot, preview, err := service.SnapshotPreview(context.Background(), 1)
	if err != nil {
		t.Fatalf("snapshot preview: %v", err)
	}
	if _, err := store.RecordRun(RunRecord{
		TargetID:           1,
		Status:             "prepared",
		PreviewSignature:   preview.Signature,
		ChangeCount:        len(preview.Changes),
		NotificationStatus: "pending",
		Intent:             preparedPriorityIntent(t, snapshot.Accounts, preview.Changes),
	}); err != nil {
		t.Fatalf("record prepared run: %v", err)
	}

	recovered, err := service.Run(context.Background(), 1)
	if err != nil {
		t.Fatalf("recover run: %v", err)
	}
	if recovered.Apply == nil || len(recovered.Apply.Applied) != len(preview.Changes) {
		t.Fatalf("recovered result = %#v", recovered)
	}
	if admin.updateCount != len(preview.Changes)*2 {
		t.Fatalf("recovery writes=%d want=%d", admin.updateCount, len(preview.Changes)*2)
	}

	fresh, err := service.Run(context.Background(), 1)
	if err != nil {
		t.Fatalf("fresh run: %v", err)
	}
	if fresh.Apply != nil || len(fresh.Preview.Changes) != 0 {
		t.Fatalf("fresh result = %#v", fresh)
	}
	if admin.updateCount != len(preview.Changes)*2 {
		t.Fatalf("fresh cycle replayed writes=%d want=%d", admin.updateCount, len(preview.Changes)*2)
	}
}

func TestFinalizeFailureKeepsPreparedRunRecoverable(t *testing.T) {
	store := &failFinalizeStore{MemoryStateStore: NewMemoryStateStore(), failNext: true}
	service, admin := newTestServiceWithState(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
		poolAccount(2, "https://api.example.test/v1", "key-2", 80),
	}, Config{MinimumAccountCount: 1}, store)

	if _, err := service.Run(context.Background(), 1); !isPublicError(err, ErrUnavailable.Code) {
		t.Fatalf("first run error = %v", err)
	}
	initialWrites := admin.updateCount
	if initialWrites != 4 {
		t.Fatalf("initial writes=%d want=4", initialWrites)
	}
	runs, err := store.ListRuns(1, 1)
	if err != nil || len(runs) != 1 || runs[0].Status != "prepared" {
		t.Fatalf("prepared run was not retained: runs=%#v err=%v", runs, err)
	}

	recovered, err := service.Run(context.Background(), 1)
	if err != nil {
		t.Fatalf("recover after finalize failure: %v", err)
	}
	if recovered.Apply == nil || len(recovered.Apply.Applied) != 2 {
		t.Fatalf("recovered result=%#v", recovered)
	}
	if admin.updateCount != initialWrites {
		t.Fatalf("recovery replayed writes: before=%d after=%d", initialWrites, admin.updateCount)
	}
}

func TestPreparedBlockedRunPreservesGuardAndNeverWrites(t *testing.T) {
	store := NewMemoryStateStore()
	service, admin := newTestServiceWithState(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
	}, Config{MinimumAccountCount: 1}, store)
	snapshot, preview, err := service.SnapshotPreview(context.Background(), 1)
	if err != nil {
		t.Fatalf("snapshot preview: %v", err)
	}
	if _, err := store.RecordRun(RunRecord{
		TargetID:           1,
		Status:             "prepared",
		PreviewSignature:   preview.Signature,
		ChangeCount:        len(preview.Changes),
		GuardCount:         1,
		GuardCodes:         []string{"change_count_too_high"},
		NotificationStatus: "pending",
		Intent:             preparedPriorityIntent(t, snapshot.Accounts, preview.Changes),
	}); err != nil {
		t.Fatalf("record prepared run: %v", err)
	}

	result, err := service.Run(context.Background(), 1)
	if err != nil {
		t.Fatalf("recover blocked run: %v", err)
	}
	if result.Apply == nil || len(result.Apply.Failed) != 1 || admin.updateCount != 0 {
		t.Fatalf("blocked recovery wrote accounts: result=%#v writes=%d", result, admin.updateCount)
	}
	runs, err := store.ListRuns(1, 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
	if runs[0].Status != "recovered_blocked" ||
		len(runs[0].GuardCodes) != 1 ||
		runs[0].GuardCodes[0] != "change_count_too_high" {
		t.Fatalf("recovered run=%#v", runs[0])
	}
}

func TestComposeEventOnlyReportsLowBalanceOnThresholdEntry(t *testing.T) {
	balance := 5.0
	snapshot := Snapshot{
		TargetID: 1,
		Summary:  SnapshotSummary{HealthyCount: 1},
		Accounts: []AccountSnapshot{{
			ID:              7,
			Name:            "low-balance",
			Schedulable:     true,
			Channel:         ChannelPLUS,
			UpstreamRate:    floatPtr(0.1),
			Balance:         &balance,
			LowestGroups:    []GroupRef{{ID: 1, Name: "PLUS", Ratio: 0.1}},
			Availability:    Availability{Matched: true, BalanceAvailable: true, RateAvailable: true},
			CurrentPriority: 10,
		}},
	}
	preview := buildPriorityPreview(snapshot)
	first := composeEvent(snapshot, preview, emptyTargetState(), nil, Config{LowBalanceThreshold: 10}.withDefaults())
	if len(first.LowBalances) != 1 || first.LowBalances[0].AccountName != "low-balance" {
		t.Fatalf("first low-balance event = %#v", first.LowBalances)
	}
	previous := stateForCycle(snapshot, preview, Config{LowBalanceThreshold: 10}.withDefaults())
	second := composeEvent(snapshot, preview, previous, nil, Config{LowBalanceThreshold: 10}.withDefaults())
	if len(second.LowBalances) != 0 {
		t.Fatalf("repeated low-balance event = %#v", second.LowBalances)
	}
}

func TestDispatchPendingDoesNotReplayAccountWrites(t *testing.T) {
	service, admin := newTestService(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
		poolAccount(2, "https://api.example.test/v1", "key-2", 80),
	}, Config{MinimumAccountCount: 1})
	service.SetDispatcher(failingDispatcher{})
	if _, err := service.Run(context.Background(), 1); err != nil {
		t.Fatalf("run: %v", err)
	}
	if admin.updateCount != 4 {
		t.Fatalf("initial updates = %d, want 4", admin.updateCount)
	}

	dispatcher := &countingDispatcher{}
	service.SetDispatcher(dispatcher)
	result, err := service.DispatchPending(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("dispatch pending: %v", err)
	}
	if result.Delivered != 1 || dispatcher.calls != 1 || admin.updateCount != 4 {
		t.Fatalf("dispatch=%#v notifications=%d updates=%d", result, dispatcher.calls, admin.updateCount)
	}
}

func TestAutomationStateAndRecentRunPersistInGormStore(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	store := NewGormStateStore(db)
	if err := store.AutoMigrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := store.SaveAutomation(AutomationState{TargetID: 7, Enabled: true}); err != nil {
		t.Fatalf("save automation: %v", err)
	}
	state, err := store.LoadAutomation(7)
	if err != nil || !state.Enabled || state.UpdatedAt.IsZero() {
		t.Fatalf("automation state=%#v err=%v", state, err)
	}
	if _, err := store.RecordRun(RunRecord{
		TargetID:       7,
		Status:         "blocked",
		GuardCount:     1,
		GuardCodes:     []string{"healthy_count_drop"},
		StatePersisted: true,
	}); err != nil {
		t.Fatalf("record run: %v", err)
	}
	runs, err := store.ListRuns(7, 1)
	if err != nil || len(runs) != 1 || len(runs[0].GuardCodes) != 1 || runs[0].GuardCodes[0] != "healthy_count_drop" {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
}

func TestSnapshotCachePersistsSnapshotAndPreview(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	store := NewGormStateStore(db)
	if err := store.AutoMigrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	generatedAt := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	snapshot := &Snapshot{
		TargetID:    7,
		GeneratedAt: generatedAt,
		Summary:     SnapshotSummary{AccountCount: 1},
		Accounts:    []AccountSnapshot{{ID: 11, Name: "cached", CurrentPriority: 30}},
	}
	preview := &PriorityPreview{
		TargetID:    7,
		GeneratedAt: generatedAt,
		Signature:   "cached-signature",
		Proposals:   []PriorityProposal{{AccountID: 11, TargetPriority: 10}},
	}
	if err := store.SaveCachedSnapshot(snapshot, preview); err != nil {
		t.Fatalf("save cache: %v", err)
	}
	gotSnapshot, gotPreview, err := store.LoadCachedSnapshot(7)
	if err != nil {
		t.Fatalf("load cache: %v", err)
	}
	if gotSnapshot == nil || gotPreview == nil ||
		!gotSnapshot.GeneratedAt.Equal(generatedAt) ||
		len(gotSnapshot.Accounts) != 1 || gotSnapshot.Accounts[0].Name != "cached" ||
		gotPreview.Signature != "cached-signature" ||
		len(gotPreview.Proposals) != 1 || gotPreview.Proposals[0].TargetPriority != 10 {
		t.Fatalf("cached snapshot=%#v preview=%#v", gotSnapshot, gotPreview)
	}
	preview.Signature = "updated-signature"
	if err := store.SaveCachedSnapshot(snapshot, preview); err != nil {
		t.Fatalf("update cache: %v", err)
	}
	_, gotPreview, err = store.LoadCachedSnapshot(7)
	if err != nil || gotPreview == nil || gotPreview.Signature != "updated-signature" {
		t.Fatalf("updated preview=%#v err=%v", gotPreview, err)
	}
}

func TestSnapshotCacheMigrationAcceptsExistingProductionSchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec(`CREATE TABLE sub2_pool_snapshots (
		target_id INTEGER PRIMARY KEY,
		snapshot_json TEXT NOT NULL,
		preview_json TEXT NOT NULL,
		updated_at datetime
	)`).Error; err != nil {
		t.Fatalf("create production cache schema: %v", err)
	}
	if err := db.Exec(
		"INSERT INTO sub2_pool_snapshots (target_id, snapshot_json, preview_json, updated_at) VALUES (?, ?, ?, ?)",
		1,
		`{"target_id":1,"accounts":[],"groups":[]}`,
		`{"target_id":1,"signature":"existing"}`,
		time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC),
	).Error; err != nil {
		t.Fatalf("insert production cache row: %v", err)
	}
	store := NewGormStateStore(db)
	if err := store.AutoMigrate(); err != nil {
		t.Fatalf("migrate existing production cache: %v", err)
	}
	snapshot, preview, err := store.LoadCachedSnapshot(1)
	if err != nil || snapshot == nil || preview == nil || snapshot.GeneratedAt.IsZero() || preview.Signature != "existing" {
		t.Fatalf("existing cache snapshot=%#v preview=%#v err=%v", snapshot, preview, err)
	}
}

func TestCachedSnapshotPreviewDoesNotReadRemoteAdmin(t *testing.T) {
	service, admin := newTestService(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 30),
	}, Config{MinimumAccountCount: 1})
	if _, _, err := service.CachedSnapshotPreview(context.Background(), 1); !isPublicError(err, ErrSnapshotCacheMissing.Code) {
		t.Fatalf("missing cache error = %v", err)
	}
	if admin.groupListCount != 0 || admin.accountListCount != 0 {
		t.Fatalf("cache miss read remote admin: groups=%d accounts=%d", admin.groupListCount, admin.accountListCount)
	}
	liveSnapshot, livePreview, err := service.SnapshotPreview(context.Background(), 1)
	if err != nil {
		t.Fatalf("live snapshot: %v", err)
	}
	if admin.groupListCount != 1 || admin.accountListCount != 1 {
		t.Fatalf("live snapshot calls: groups=%d accounts=%d", admin.groupListCount, admin.accountListCount)
	}
	cachedSnapshot, cachedPreview, err := service.CachedSnapshotPreview(context.Background(), 1)
	if err != nil {
		t.Fatalf("cached snapshot: %v", err)
	}
	if admin.groupListCount != 1 || admin.accountListCount != 1 {
		t.Fatalf("cached read hit remote admin: groups=%d accounts=%d", admin.groupListCount, admin.accountListCount)
	}
	if !cachedSnapshot.GeneratedAt.Equal(liveSnapshot.GeneratedAt) || cachedPreview.Signature != livePreview.Signature {
		t.Fatalf("cached snapshot=%#v preview=%#v", cachedSnapshot, cachedPreview)
	}
}

func TestGormPreparedRunPersistsRecoveryIdentity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	store := NewGormStateStore(db)
	if err := store.AutoMigrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	proposal := PriorityProposal{
		AccountID:              7,
		AccountName:            "persisted",
		CurrentPriority:        90,
		stagingPriority:        110,
		TargetPriority:         10,
		Channel:                ChannelPLUS,
		Reason:                 "upstream_rate_ascending",
		expectedGroupIDs:       []int64{1, 3},
		expectedStatus:         "active",
		expectedPoolManaged:    true,
		expectedIdentityDigest: "identity-digest",
	}
	if _, err := store.RecordRun(RunRecord{
		TargetID: 1,
		Status:   "prepared",
		Intent:   []PriorityProposal{proposal},
	}); err != nil {
		t.Fatalf("record run: %v", err)
	}
	runs, err := store.ListPreparedRuns(1, 1)
	if err != nil || len(runs) != 1 || len(runs[0].Intent) != 1 {
		t.Fatalf("prepared runs=%#v err=%v", runs, err)
	}
	got := runs[0].Intent[0]
	if got.expectedIdentityDigest != proposal.expectedIdentityDigest ||
		got.expectedStatus != proposal.expectedStatus ||
		got.expectedPoolManaged != proposal.expectedPoolManaged ||
		got.stagingPriority != proposal.stagingPriority ||
		!equalInt64Sets(got.expectedGroupIDs, proposal.expectedGroupIDs) {
		t.Fatalf("persisted intent=%#v", got)
	}
}

func TestGormLeasePreventsConcurrentTargetOwnership(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:sub2-pool-lease?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	store := NewGormStateStore(db)
	if err := store.AutoMigrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	first, err := store.AcquireLease(1, "owner-1", time.Now().Add(time.Minute))
	if err != nil || !first {
		t.Fatalf("first lease = %v, err=%v", first, err)
	}
	second, err := store.AcquireLease(1, "owner-2", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if second {
		t.Fatal("second owner acquired an active lease")
	}
	if err := store.ReleaseLease(1, "owner-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	second, err = store.AcquireLease(1, "owner-2", time.Now().Add(time.Minute))
	if err != nil || !second {
		t.Fatalf("lease after release = %v, err=%v", second, err)
	}
}

func TestServicesShareLeaseAcrossInstances(t *testing.T) {
	store := NewMemoryStateStore()
	first, admin := newTestServiceWithState(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
	}, Config{MinimumAccountCount: 1}, store)
	second := New(
		first.targets,
		first.cipher,
		first.admin,
		first.matcher.channels,
		first.matcher.keys,
		store,
		first.cfg,
	)
	admin.listStarted = make(chan struct{}, 1)
	admin.listRelease = make(chan struct{})

	done := make(chan error, 1)
	go func() {
		_, err := first.Run(context.Background(), 1)
		done <- err
	}()
	select {
	case <-admin.listStarted:
	case <-time.After(time.Second):
		t.Fatal("first service did not enter leased snapshot")
	}

	if _, err := second.Run(context.Background(), 1); !isPublicError(err, ErrBusy.Code) {
		t.Fatalf("second service error=%v want target busy", err)
	}
	close(admin.listRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first service: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first service did not finish")
	}
}

func TestSetSchedulableReadsBeforeAndAfterWrite(t *testing.T) {
	service, _ := newTestService(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
	}, Config{MinimumAccountCount: 1})
	result, err := service.SetSchedulable(context.Background(), 1, 1, false)
	if err != nil {
		t.Fatalf("set schedulable: %v", err)
	}
	if result.Schedulable {
		t.Fatalf("result = %#v", result)
	}
}

func TestSetSchedulableRejectsNonPoolAPIKeyAndOAuthAccounts(t *testing.T) {
	nonPool := nonPoolAccount(1, 90)
	oauth := poolAccount(2, "https://api.example.test/v1", "key-2", 80)
	oauth.Account.Type = "oauth"
	service, admin := newTestService(t, []sub2api.PoolAccount{nonPool, oauth}, Config{MinimumAccountCount: 1})

	for _, accountID := range []int64{1, 2} {
		if _, err := service.SetSchedulable(context.Background(), 1, accountID, false); !isPublicError(err, ErrInvalidInput.Code) {
			t.Fatalf("account %d error=%v, want invalid input", accountID, err)
		}
		if !admin.accounts[accountID].Account.Schedulable {
			t.Fatalf("account %d was changed despite rejection", accountID)
		}
	}
}

func TestSnapshotMergesEqualLowestGroupsBeforeClassification(t *testing.T) {
	service, admin := newTestService(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 90),
	}, Config{MinimumAccountCount: 1})
	admin.groups = []sub2api.AdminGroup{
		{ID: 1, Name: "PLUS", Ratio: 0.1, Status: "active"},
		{ID: 2, Name: "Kiro", Ratio: 0.1, Status: "active"},
	}
	account := admin.accounts[1]
	account.Account.GroupIDs = []int64{1, 2}
	admin.accounts[1] = account

	snapshot, err := service.Snapshot(context.Background(), 1)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Accounts) != 1 || len(snapshot.Accounts[0].LowestGroups) != 2 {
		t.Fatalf("snapshot accounts = %#v", snapshot.Accounts)
	}
	if snapshot.Accounts[0].Channel != ChannelKiro {
		t.Fatalf("channel = %q, want %q", snapshot.Accounts[0].Channel, ChannelKiro)
	}
	if !snapshot.Accounts[0].Availability.Healthy || snapshot.Summary.HealthCoverage.Healthy != 1 {
		t.Fatalf("summary = %#v", snapshot.Summary)
	}
}

func TestSnapshotUsesSafeAccountTodayStatsAndHealthFlags(t *testing.T) {
	service, admin := newTestService(t, []sub2api.PoolAccount{
		poolAccount(1, "https://api.example.test/v1", "key-1", 10),
	}, Config{MinimumAccountCount: 1})
	requests := 17
	cost := 2.5
	account := admin.accounts[1]
	account.Stats = sub2api.PoolAccountStats{
		TodayRequests: &requests,
		TodayCost:     &cost,
	}
	account.Health.RateLimited = true
	admin.accounts[1] = account

	snapshot, err := service.Snapshot(context.Background(), 1)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	got := snapshot.Accounts[0]
	if got.TodayStats.Requests == nil || *got.TodayStats.Requests != 17 ||
		got.TodayStats.Cost == nil || *got.TodayStats.Cost != 2.5 ||
		!got.Availability.TodayStatsReady ||
		got.Availability.Healthy {
		t.Fatalf("account = %#v", got)
	}
	if snapshot.Summary.TodayStatsReadyCount != 1 || snapshot.Summary.HealthCoverage.Healthy != 0 {
		t.Fatalf("coverage = %#v", snapshot.Summary)
	}
}

func priorityAccount(id int64, channel string, current int, rate, balance *float64) AccountSnapshot {
	return AccountSnapshot{
		ID:               id,
		Schedulable:      true,
		PoolManaged:      true,
		CurrentPriority:  current,
		GroupIDs:         []int64{1},
		Channel:          channel,
		UpstreamRate:     rate,
		Balance:          balance,
		MultiplierSource: "key_exact",
		Availability: Availability{
			BalanceAvailable: balance != nil,
			RateAvailable:    rate != nil,
		},
	}
}

func proposalTargets(proposals []PriorityProposal) map[int64]int {
	out := map[int64]int{}
	for _, proposal := range proposals {
		out[proposal.AccountID] = proposal.TargetPriority
	}
	return out
}

func preparedPriorityIntent(t *testing.T, accounts []AccountSnapshot, changes []PriorityProposal) []PriorityProposal {
	t.Helper()
	intent, err := preparePriorityTransitionIntent(accounts, changes)
	if err != nil {
		t.Fatalf("prepare priority transition intent: %v", err)
	}
	return intent
}

func uniquePriorityValues(priorities map[int64]int) bool {
	seen := map[int]struct{}{}
	for _, priority := range priorities {
		if _, exists := seen[priority]; exists {
			return false
		}
		seen[priority] = struct{}{}
	}
	return true
}

func floatPtr(value float64) *float64 { return &value }

func poolAccount(id int64, baseURL, key string, priority int) sub2api.PoolAccount {
	return sub2api.PoolAccount{Account: sub2api.AdminAccount{
		ID:          id,
		Name:        "account",
		Platform:    "openai",
		Type:        "apikey",
		Status:      "active",
		Schedulable: true,
		Priority:    priority,
		GroupIDs:    []int64{1},
		Credentials: map[string]any{
			"base_url":  baseURL,
			"api_key":   key,
			"pool_mode": true,
		},
	}}
}

func nonPoolAccount(id int64, priority int) sub2api.PoolAccount {
	account := poolAccount(id, "https://not-pool.example", "not-pool-key", priority)
	account.Account.Credentials["pool_mode"] = false
	return account
}

func keyHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type fakeChannels struct {
	items []storage.Channel
	err   error
}

func (f *fakeChannels) List() ([]storage.Channel, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]storage.Channel(nil), f.items...), nil
}

type fakeKeys struct {
	items    map[uint][]connector.APIKey
	revealed map[string]string
}

func (f *fakeKeys) ListAPIKeys(_ context.Context, channelID uint, _ connector.APIKeyQuery) (*connector.APIKeyPage, error) {
	items := append([]connector.APIKey(nil), f.items[channelID]...)
	return &connector.APIKeyPage{Items: items, Page: 1, Pages: 1}, nil
}

func (f *fakeKeys) RevealAPIKey(_ context.Context, channelID uint, keyID int64) (string, error) {
	value, ok := f.revealed[keyIDKey(channelID, keyID)]
	if !ok {
		return "", errors.New("missing key")
	}
	return value, nil
}

type delayedKeys struct {
	fakeKeys
	delay     time.Duration
	mu        sync.Mutex
	active    int
	maxActive int
}

func (f *delayedKeys) ListAPIKeys(ctx context.Context, channelID uint, query connector.APIKeyQuery) (*connector.APIKeyPage, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return f.fakeKeys.ListAPIKeys(ctx, channelID, query)
}

func keyIDKey(channelID uint, keyID int64) string {
	return fmt.Sprintf("%d:%d", channelID, keyID)
}

type fakeTargets struct {
	target *storage.UpstreamSyncTarget
}

func (f fakeTargets) FindByID(id uint) (*storage.UpstreamSyncTarget, error) {
	if f.target == nil || id != f.target.ID {
		return nil, errors.New("target not found")
	}
	copy := *f.target
	return &copy, nil
}

type fakeAdmin struct {
	mu               sync.Mutex
	groups           []sub2api.AdminGroup
	accounts         map[int64]sub2api.PoolAccount
	failUpdate       map[int64]bool
	updateCount      int
	groupListCount   int
	accountListCount int
	priorityLog      []map[int64]int
	listStarted      chan struct{}
	listRelease      chan struct{}
}

func (f *fakeAdmin) ListGroups(_ context.Context, _ sub2api.AdminTarget, _ bool) ([]sub2api.AdminGroup, error) {
	if f.listStarted != nil {
		select {
		case f.listStarted <- struct{}{}:
		default:
		}
		<-f.listRelease
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupListCount++
	return append([]sub2api.AdminGroup(nil), f.groups...), nil
}

func (f *fakeAdmin) ListAllPoolAccounts(_ context.Context, _ sub2api.AdminTarget) ([]sub2api.PoolAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accountListCount++
	out := make([]sub2api.PoolAccount, 0, len(f.accounts))
	for _, account := range f.accounts {
		out = append(out, clonePoolAccount(account))
	}
	return out, nil
}

func (f *fakeAdmin) GetPoolAccount(_ context.Context, _ sub2api.AdminTarget, accountID int64) (*sub2api.PoolAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	account, ok := f.accounts[accountID]
	if !ok {
		return nil, errors.New("missing account")
	}
	copy := clonePoolAccount(account)
	return &copy, nil
}

func (f *fakeAdmin) UpdatePoolAccountPriority(_ context.Context, _ sub2api.AdminTarget, accountID int64, priority int) (*sub2api.PoolAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failUpdate[accountID] {
		return nil, errors.New("write rejected")
	}
	account, ok := f.accounts[accountID]
	if !ok {
		return nil, errors.New("missing account")
	}
	account.Account.Priority = priority
	f.accounts[accountID] = account
	f.updateCount++
	f.priorityLog = append(f.priorityLog, f.currentPrioritiesLocked())
	copy := clonePoolAccount(account)
	return &copy, nil
}

func (f *fakeAdmin) SetPoolAccountSchedulable(_ context.Context, _ sub2api.AdminTarget, accountID int64, schedulable bool) (*sub2api.PoolAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	account, ok := f.accounts[accountID]
	if !ok {
		return nil, errors.New("missing account")
	}
	account.Account.Schedulable = schedulable
	f.accounts[accountID] = account
	copy := clonePoolAccount(account)
	return &copy, nil
}

func (f *fakeAdmin) setPriority(accountID int64, priority int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	account := f.accounts[accountID]
	account.Account.Priority = priority
	f.accounts[accountID] = account
}

func (f *fakeAdmin) priority(accountID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accounts[accountID].Account.Priority
}

func (f *fakeAdmin) priorityHistory() []map[int64]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[int64]int, 0, len(f.priorityLog))
	for _, priorities := range f.priorityLog {
		copy := make(map[int64]int, len(priorities))
		for accountID, priority := range priorities {
			copy[accountID] = priority
		}
		out = append(out, copy)
	}
	return out
}

func (f *fakeAdmin) currentPrioritiesLocked() map[int64]int {
	out := make(map[int64]int, len(f.accounts))
	for accountID, account := range f.accounts {
		out[accountID] = account.Account.Priority
	}
	return out
}

func clonePoolAccount(value sub2api.PoolAccount) sub2api.PoolAccount {
	copy := value
	copy.Account.GroupIDs = append([]int64(nil), value.Account.GroupIDs...)
	copy.Account.Credentials = map[string]any{}
	for key, item := range value.Account.Credentials {
		copy.Account.Credentials[key] = item
	}
	return copy
}

type failingDispatcher struct{}

func (failingDispatcher) DispatchPoolEvent(context.Context, PoolEvent) error {
	return errors.New("notify down")
}

type countingDispatcher struct {
	calls int
}

func (d *countingDispatcher) DispatchPoolEvent(context.Context, PoolEvent) error {
	d.calls++
	return nil
}

type failFinalizeStore struct {
	*MemoryStateStore
	mu       sync.Mutex
	failNext bool
}

func (s *failFinalizeStore) FinalizeCycle(
	targetID, runID uint,
	record RunRecord,
	state TargetState,
	event *PoolEvent,
) (uint, bool, error) {
	s.mu.Lock()
	if s.failNext {
		s.failNext = false
		s.mu.Unlock()
		return 0, false, errors.New("injected finalize failure")
	}
	s.mu.Unlock()
	return s.MemoryStateStore.FinalizeCycle(targetID, runID, record, state, event)
}

func newTestService(t *testing.T, accounts []sub2api.PoolAccount, cfg Config) (*Service, *fakeAdmin) {
	return newTestServiceWithState(t, accounts, cfg, NewMemoryStateStore())
}

func newTestServiceWithState(
	t *testing.T,
	accounts []sub2api.PoolAccount,
	cfg Config,
	state StateStore,
) (*Service, *fakeAdmin) {
	t.Helper()
	cipher, err := crypto.NewCipher("test-secret")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	encrypted, err := cipher.Encrypt("admin-key")
	if err != nil {
		t.Fatalf("encrypt admin key: %v", err)
	}
	admin := &fakeAdmin{
		groups:     []sub2api.AdminGroup{{ID: 1, Name: "PLUS", Ratio: 0.1, Status: "active"}},
		accounts:   map[int64]sub2api.PoolAccount{},
		failUpdate: map[int64]bool{},
	}
	keys := &fakeKeys{items: map[uint][]connector.APIKey{}, revealed: map[string]string{}}
	for _, account := range accounts {
		admin.accounts[account.Account.ID] = clonePoolAccount(account)
		keys.items[1] = append(keys.items[1], connector.APIKey{
			ID:         account.Account.ID,
			GroupRatio: 0.1,
		})
		keys.revealed[keyIDKey(1, account.Account.ID)] = account.Account.Credentials["api_key"].(string)
	}
	service := New(
		fakeTargets{target: &storage.UpstreamSyncTarget{
			ID:                1,
			BaseURL:           "https://sub2.example.test",
			AdminAPIKeyCipher: encrypted,
		}},
		cipher,
		admin,
		&fakeChannels{items: []storage.Channel{{
			ID:             1,
			SiteURL:        "https://api.example.test/v1",
			MonitorEnabled: true,
			LastBalance:    floatPtr(50),
			TodayCost:      floatPtr(1.5),
		}}},
		keys,
		state,
		cfg,
	)
	return service, admin
}
