package sub2pool

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxStopReasonBytes = 512

var (
	stopReasonSensitiveValue = regexp.MustCompile(`(?i)(["']?(?:authorization|proxy[_-]?authorization|x[_-]?api[_-]?key|api[_-]?key|access[_-]?token|refresh[_-]?token|admin[_-]?api[_-]?key|token|client[_-]?secret|private[_-]?key|password|passwd|secret|cookie|set-cookie|session(?:id)?|csrf(?:[_-]?token)?|jwt)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	stopReasonBearer         = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	stopReasonJWT            = regexp.MustCompile(`\b[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	stopReasonSK             = regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9_-]{8,}\b`)
	stopReasonURL            = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s<>"']+`)
)

func sanitizeStopReason(value string) string {
	value = strings.ToValidUTF8(value, "")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = stopReasonBearer.ReplaceAllString(value, "Bearer [redacted]")
	value = stopReasonJWT.ReplaceAllString(value, "[redacted-jwt]")
	value = stopReasonSK.ReplaceAllString(value, "sk-[redacted]")
	value = stopReasonURL.ReplaceAllString(value, "[redacted-url]")
	value = stopReasonSensitiveValue.ReplaceAllString(value, `${1}[redacted]`)
	value = strings.Join(strings.Fields(value), " ")
	return truncateUTF8Bytes(value, maxStopReasonBytes)
}

func truncateUTF8Bytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return strings.TrimSpace(value[:end])
}

func sanitizeSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Accounts = append([]AccountSnapshot(nil), snapshot.Accounts...)
	now := time.Now()
	for index := range snapshot.Accounts {
		account := &snapshot.Accounts[index]
		account.StopReason = sanitizeStopReason(account.StopReason)
		account.StopSource = sanitizeStopSource(account.StopSource)
		account.StopTime = cloneTime(account.StopTime)
		if stopReasonIsStale(*account, now) || account.StopReason == "" {
			account.StopSource = ""
			account.StopReason = ""
			account.StopTime = nil
		}
	}
	return snapshot
}

func sanitizeStopSource(value string) string {
	switch strings.TrimSpace(value) {
	case "temp_unschedulable_reason",
		"error_message",
		"temporarily_unschedulable",
		"rate_limit",
		"overload",
		"schedulable":
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func stopReasonIsStale(account AccountSnapshot, now time.Time) bool {
	if account.StopSource == "temp_unschedulable_reason" ||
		account.StopSource == "temporarily_unschedulable" {
		if account.StopTime != nil && !account.StopTime.After(now) {
			return true
		}
	}
	return account.Schedulable &&
		!account.Health.RateLimited &&
		!account.Health.TemporarilyUnschedulable &&
		!account.Health.Overloaded
}
