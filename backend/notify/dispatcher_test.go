package notify

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/storage"
)

type memCooldown struct {
	last map[string]time.Time
}

func (m *memCooldown) TryClaimCooldown(channelID uint, event storage.NotificationEvent, cooldown time.Duration) (bool, error) {
	if m.last == nil {
		m.last = map[string]time.Time{}
	}
	key := string(event) + "/" + itoa(channelID)
	if t, ok := m.last[key]; ok && time.Since(t) < cooldown {
		return false, nil
	}
	m.last[key] = time.Now()
	return true, nil
}

func itoa(n uint) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestSuppressLoginFailedCooldown(t *testing.T) {
	cd := &memCooldown{}
	d := NewDispatcherWithCooldown(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Policy{
		LoginFailedCooldown:  time.Hour,
		BalanceLowCooldown:  time.Hour,
	}, cd)

	msg := Message{Event: storage.EventLoginFailed, ChannelID: 56}
	if d.suppress(msg) {
		t.Fatal("first login_failed should send")
	}
	if !d.suppress(msg) {
		t.Fatal("second login_failed within cooldown should suppress")
	}

	// other events without policy stay unsuppressed
	if d.suppress(Message{Event: storage.EventMonitorFailed, ChannelID: 56}) {
		t.Fatal("monitor_failed has no cooldown and must not suppress")
	}
}
