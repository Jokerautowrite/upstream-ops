package sub2pool

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bejix/upstream-ops/backend/connector/sub2api"
)

func TestSanitizeStopReasonRedactsSecretsURLsAndControlsWithUTF8Limit(t *testing.T) {
	raw := "failed\x00 https://secret.example/path Authorization: Bearer abc.def.ghi " +
		`api_key="sk-super-secret-value" token=plain-secret cookie=session=secret-value jwt=aaaaaaaa.bbbbbbbb.cccccccc ` +
		"standalone sk-standalone-secret zzzzzzzz.yyyyyyyy.xxxxxxxx " +
		strings.Repeat("界", 300)
	got := sanitizeStopReason(raw)
	if len(got) > maxStopReasonBytes || !utf8.ValidString(got) {
		t.Fatalf("sanitized reason is not a valid bounded UTF-8 string: bytes=%d value=%q", len(got), got)
	}
	for _, forbidden := range []string{
		"\x00",
		"secret.example",
		"abc.def.ghi",
		"super-secret-value",
		"plain-secret",
		"session=secret-value",
		"aaaaaaaa.bbbbbbbb.cccccccc",
		"zzzzzzzz.yyyyyyyy.xxxxxxxx",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("sanitized reason leaked %q: %q", forbidden, got)
		}
	}
	for _, marker := range []string{"[redacted-url]", "[redacted]", "sk-[redacted]", "[redacted-jwt]"} {
		if !strings.Contains(got, marker) {
			t.Fatalf("sanitized reason missing marker %q: %q", marker, got)
		}
	}
}

func TestAccountStopDetailsIgnoresStaleAndExpiredReasons(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	source, reason, stopTime := accountStopDetails(
		sub2api.AdminAccount{Schedulable: true},
		sub2api.PoolAccountHealth{ErrorMessage: "stale secret"},
		now,
	)
	if source != "" || reason != "" || stopTime != nil {
		t.Fatalf("schedulable account retained stale reason: %q %q %v", source, reason, stopTime)
	}

	source, reason, stopTime = accountStopDetails(
		sub2api.AdminAccount{Schedulable: false},
		sub2api.PoolAccountHealth{
			TemporarilyUnschedulable: true,
			TempUnschedulableReason:  "expired maintenance",
			TempUnschedulableUntil:   &past,
		},
		now,
	)
	if source != "schedulable" || reason != "not_schedulable" || stopTime != nil {
		t.Fatalf("expired temporary reason remained active: %q %q %v", source, reason, stopTime)
	}

	source, reason, stopTime = accountStopDetails(
		sub2api.AdminAccount{Schedulable: false},
		sub2api.PoolAccountHealth{
			TemporarilyUnschedulable: true,
			TempUnschedulableReason:  "maintenance at https://secret.example",
			TempUnschedulableUntil:   &future,
		},
		now,
	)
	if source != "temp_unschedulable_reason" ||
		reason != "maintenance at [redacted-url]" ||
		stopTime == nil ||
		!stopTime.Equal(future) {
		t.Fatalf("active temporary reason = %q %q %v", source, reason, stopTime)
	}
}

func TestSnapshotPersistenceSanitizesStopReason(t *testing.T) {
	db, store := newRecoveryGormStore(t)
	generatedAt := time.Now()
	snapshot := Snapshot{
		TargetID:    1,
		GeneratedAt: generatedAt,
		Accounts: []AccountSnapshot{{
			ID:           7,
			Schedulable:  false,
			StopSource:   "error_message",
			StopReason:   "api_key=sk-persisted-secret https://secret.example/path",
			SkipReason:   "not_schedulable",
			MatchStatus:  "key_exact",
			Health:       AccountHealth{},
			Availability: Availability{},
		}},
	}
	if err := store.SaveSnapshot(snapshot, PriorityPreview{TargetID: 1}); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	var row GormPoolSnapshot
	if err := db.First(&row, "target_id = ?", 1).Error; err != nil {
		t.Fatalf("load raw snapshot row: %v", err)
	}
	for _, forbidden := range []string{"persisted-secret", "secret.example"} {
		if strings.Contains(row.SnapshotJSON, forbidden) {
			t.Fatalf("persisted snapshot leaked %q: %s", forbidden, row.SnapshotJSON)
		}
	}
	var persisted Snapshot
	if err := json.Unmarshal([]byte(row.SnapshotJSON), &persisted); err != nil {
		t.Fatalf("decode persisted snapshot: %v", err)
	}
	if len(persisted.Accounts) != 1 ||
		persisted.Accounts[0].StopSource != "error_message" ||
		!strings.Contains(persisted.Accounts[0].StopReason, "[redacted]") ||
		!strings.Contains(persisted.Accounts[0].StopReason, "[redacted-url]") {
		t.Fatalf("persisted safe stop reason = %#v", persisted.Accounts)
	}
}
