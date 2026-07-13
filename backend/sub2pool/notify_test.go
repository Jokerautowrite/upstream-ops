package sub2pool

import (
	"strings"
	"testing"

	"github.com/bejix/upstream-ops/backend/notify"
	"github.com/bejix/upstream-ops/backend/storage"
)

func TestPoolPriorityMessagesAreExplicitAndDoNotMixGenericSignals(t *testing.T) {
	event := PoolEvent{
		EventID:              "event-1",
		TargetID:             1,
		MissingMultiplierIDs: []int64{9},
		MissingBalanceIDs:    []int64{10},
		LowBalances:          []LowBalance{{AccountID: 11, Balance: 1}},
		RateChanges:          []RateChange{{AccountID: 12, CurrentRate: floatPtr(0.2)}},
		PriorityResult: &ApplyResult{
			Applied: []ApplyItem{{
				AccountID: 7, AccountName: "applied", Channel: ChannelPLUS,
				BeforePriority: 90, TargetPriority: 10, Status: "applied",
			}},
		},
	}
	messages := poolEventMessages(event)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	applied := messageByEvent(messages, storage.EventSub2PoolPriorityApplied)
	if applied == nil {
		t.Fatalf("priority applied message missing: %#v", messages)
	}
	for _, forbidden := range []string{"倍率", "缺上游", "缺余额", "低余额"} {
		if strings.Contains(applied.Body, forbidden) {
			t.Fatalf("explicit applied message mixed %q: %s", forbidden, applied.Body)
		}
	}
	generic := messageByEvent(messages, storage.EventSub2PoolChanged)
	if generic == nil ||
		len(generic.SkipIfExplicitlySubscribed) != 1 ||
		generic.SkipIfExplicitlySubscribed[0] != storage.EventSub2PoolPriorityApplied {
		t.Fatalf("generic compatibility message = %#v", generic)
	}
}

func TestPoolPriorityFailureRequiresRealOperationStageAndCode(t *testing.T) {
	guardOnly := PoolEvent{
		TargetID: 1,
		Guards:   []GuardViolation{{Code: "guard", Message: "blocked"}},
		PriorityResult: &ApplyResult{Failed: []ApplyItem{{
			AccountID: 7, Status: "recovery_guard_blocked",
		}}},
	}
	messages := poolEventMessages(guardOnly)
	if len(messages) != 1 || messages[0].Event != storage.EventSub2PoolChanged {
		t.Fatalf("guard-only outcome triggered priority event: %#v", messages)
	}

	realFailure := PoolEvent{
		TargetID: 1,
		PriorityResult: &ApplyResult{Failed: []ApplyItem{{
			AccountID: 7, AccountName: "failed", Channel: ChannelCC,
			BeforePriority: 90, TargetPriority: 10, Status: "failed",
			Stage: "write", Code: "upstream_write_failed",
		}}},
	}
	messages = poolEventMessages(realFailure)
	failed := messageByEvent(messages, storage.EventSub2PoolPriorityFailed)
	if failed == nil ||
		!strings.Contains(failed.Body, "stage=write") ||
		!strings.Contains(failed.Body, "code=upstream_write_failed") {
		t.Fatalf("explicit failure message = %#v", failed)
	}
}

func TestPoolPriorityAppliedRequiresVerifiedAppliedStatus(t *testing.T) {
	event := PoolEvent{
		TargetID: 1,
		PriorityResult: &ApplyResult{Applied: []ApplyItem{{
			AccountID: 7,
			Status:    "guard_only",
		}}},
	}
	messages := poolEventMessages(event)
	if messageByEvent(messages, storage.EventSub2PoolPriorityApplied) != nil {
		t.Fatalf("non-applied item triggered success event: %#v", messages)
	}
}

func messageByEvent(messages []notify.Message, event storage.NotificationEvent) *notify.Message {
	for index := range messages {
		if messages[index].Event == event {
			return &messages[index]
		}
	}
	return nil
}
