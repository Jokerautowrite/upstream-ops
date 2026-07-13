package sub2pool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bejix/upstream-ops/backend/notify"
	"github.com/bejix/upstream-ops/backend/storage"
)

// NotifyAdapter sends one combined account-pool message through the existing
// durable notification configuration. It never includes credentials or URLs.
type NotifyAdapter struct {
	dispatcher *notify.Dispatcher
}

func NewNotifyAdapter(dispatcher *notify.Dispatcher) *NotifyAdapter {
	return &NotifyAdapter{dispatcher: dispatcher}
}

func (n *NotifyAdapter) DispatchPoolEvent(ctx context.Context, event PoolEvent) error {
	if n == nil || n.dispatcher == nil {
		return ErrUnavailable
	}
	var dispatchErrors []error
	for _, message := range poolEventMessages(event) {
		if err := n.dispatcher.Dispatch(ctx, message); err != nil {
			dispatchErrors = append(dispatchErrors, err)
		}
	}
	return errors.Join(dispatchErrors...)
}

func poolEventMessages(event PoolEvent) []notify.Message {
	applied := realPriorityAppliedItems(event)
	failed := realPriorityFailedItems(event)
	messages := make([]notify.Message, 0, 5)
	if len(applied) > 0 {
		messages = append(messages, notify.Message{
			Event:             storage.EventSub2PoolPriorityApplied,
			ChannelID:         0,
			Subject:           "Sub2 优先级调整成功",
			Body:              priorityAppliedBody(applied),
			RequireSubscriber: true,
			Extra: map[string]any{
				"event_id":  event.EventID,
				"target_id": event.TargetID,
				"applied":   len(applied),
			},
		})
	}
	if len(failed) > 0 {
		messages = append(messages, notify.Message{
			Event:             storage.EventSub2PoolPriorityFailed,
			ChannelID:         0,
			Subject:           "Sub2 优先级调整失败",
			Body:              priorityFailedBody(failed),
			RequireSubscriber: true,
			Extra: map[string]any{
				"event_id":  event.EventID,
				"target_id": event.TargetID,
				"failed":    len(failed),
			},
		})
	}

	nonPriority := event
	nonPriority.PriorityResult = nil
	if hasEventSignal(nonPriority) {
		messages = append(messages, notify.Message{
			Event:     storage.EventSub2PoolChanged,
			ChannelID: 0,
			Subject:   poolEventSubject(nonPriority),
			Body:      poolEventBody(nonPriority),
			Extra: map[string]any{
				"event_id":           event.EventID,
				"target_id":          event.TargetID,
				"rate_changes":       len(event.RateChanges),
				"missing_multiplier": len(event.MissingMultiplierIDs),
				"missing_balance":    len(event.MissingBalanceIDs),
				"low_balances":       len(event.LowBalances),
				"guards":             len(event.Guards),
			},
		})
	}
	if len(applied) > 0 {
		compatibility := event
		compatibility.RateChanges = nil
		compatibility.MissingMultiplierIDs = nil
		compatibility.MissingBalanceIDs = nil
		compatibility.LowBalances = nil
		compatibility.Guards = nil
		compatibility.PriorityResult = &ApplyResult{Applied: applied}
		messages = append(messages, notify.Message{
			Event:                      storage.EventSub2PoolChanged,
			ChannelID:                  0,
			Subject:                    poolEventSubject(compatibility),
			Body:                       poolEventBody(compatibility),
			SkipIfExplicitlySubscribed: []storage.NotificationEvent{storage.EventSub2PoolPriorityApplied},
			Extra: map[string]any{
				"event_id":  event.EventID,
				"target_id": event.TargetID,
			},
		})
	}
	if len(failed) > 0 {
		compatibility := event
		compatibility.RateChanges = nil
		compatibility.MissingMultiplierIDs = nil
		compatibility.MissingBalanceIDs = nil
		compatibility.LowBalances = nil
		compatibility.Guards = nil
		compatibility.PriorityResult = &ApplyResult{Failed: failed}
		messages = append(messages, notify.Message{
			Event:                      storage.EventSub2PoolChanged,
			ChannelID:                  0,
			Subject:                    poolEventSubject(compatibility),
			Body:                       poolEventBody(compatibility),
			SkipIfExplicitlySubscribed: []storage.NotificationEvent{storage.EventSub2PoolPriorityFailed},
			Extra: map[string]any{
				"event_id":  event.EventID,
				"target_id": event.TargetID,
			},
		})
	}
	return messages
}

func poolEventSubject(event PoolEvent) string {
	if event.PriorityResult != nil {
		if len(event.PriorityResult.Failed) > 0 {
			return "Sub2 自动化部分完成"
		}
		if len(event.PriorityResult.Applied) > 0 {
			return "Sub2 倍率与优先级已同步"
		}
	}
	if len(event.Guards) > 0 {
		return "Sub2 自动化已保护性跳过"
	}
	if len(event.RateChanges) > 0 {
		return "Sub2 上游倍率变化"
	}
	return "Sub2 账号池提醒"
}

func poolEventBody(event PoolEvent) string {
	var sections []string
	if len(event.RateChanges) > 0 {
		lines := []string{fmt.Sprintf("倍率变化：%d 个", len(event.RateChanges))}
		for _, change := range event.RateChanges {
			lines = append(lines, fmt.Sprintf(
				"- %s：%s -> %s",
				poolAccountLabel(change.AccountName, change.AccountID),
				formatPoolRate(change.PreviousRate),
				formatPoolRate(change.CurrentRate),
			))
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	if event.PriorityResult != nil {
		result := event.PriorityResult
		lines := []string{fmt.Sprintf(
			"优先级调整：成功 %d，失败 %d，剩余 %d",
			len(result.Applied),
			len(result.Failed),
			result.Remaining,
		)}
		for _, item := range result.Applied {
			lines = append(lines, fmt.Sprintf(
				"- [%s] %s：%d -> %d",
				item.Channel,
				poolAccountLabel(item.AccountName, item.AccountID),
				item.BeforePriority,
				item.TargetPriority,
			))
		}
		for _, item := range result.Failed {
			after := "未知"
			if item.AfterPriority != nil {
				after = fmt.Sprintf("%d", *item.AfterPriority)
			}
			lines = append(lines, fmt.Sprintf(
				"- 失败 [%s] %s：%d -> %d，实际 %s（%s）",
				item.Channel,
				poolAccountLabel(item.AccountName, item.AccountID),
				item.BeforePriority,
				item.TargetPriority,
				after,
				item.Status,
			))
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	if len(event.MissingMultiplierIDs) > 0 {
		sections = append(sections, "缺上游倍率，已跳过："+joinPoolIDs(event.MissingMultiplierIDs))
	}
	if len(event.MissingBalanceIDs) > 0 {
		sections = append(sections, "缺余额，已跳过："+joinPoolIDs(event.MissingBalanceIDs))
	}
	if len(event.LowBalances) > 0 {
		lines := []string{fmt.Sprintf("低余额：%d 个", len(event.LowBalances))}
		for _, item := range event.LowBalances {
			lines = append(lines, fmt.Sprintf(
				"- %s：%.4f",
				poolAccountLabel(item.AccountName, item.AccountID),
				item.Balance,
			))
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	if len(event.Guards) > 0 {
		lines := []string{"保护性跳过，本轮未写入："}
		for _, guard := range event.Guards {
			lines = append(lines, fmt.Sprintf("- %s：%s", guard.Code, guard.Message))
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

func realPriorityAppliedItems(event PoolEvent) []ApplyItem {
	if event.PriorityResult == nil {
		return nil
	}
	out := make([]ApplyItem, 0, len(event.PriorityResult.Applied))
	for _, item := range event.PriorityResult.Applied {
		if item.Status == "applied" || item.Status == "recovered_applied" {
			out = append(out, item)
		}
	}
	return out
}

func realPriorityFailedItems(event PoolEvent) []ApplyItem {
	if event.PriorityResult == nil {
		return nil
	}
	out := make([]ApplyItem, 0, len(event.PriorityResult.Failed))
	for _, item := range event.PriorityResult.Failed {
		if item.Status == "failed" && item.Stage != "" && item.Code != "" {
			out = append(out, item)
		}
	}
	return out
}

func priorityAppliedBody(items []ApplyItem) string {
	lines := []string{fmt.Sprintf("优先级调整成功：%d 个", len(items))}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf(
			"- [%s] %s：%d -> %d",
			item.Channel,
			poolAccountLabel(item.AccountName, item.AccountID),
			item.BeforePriority,
			item.TargetPriority,
		))
	}
	return strings.Join(lines, "\n")
}

func priorityFailedBody(items []ApplyItem) string {
	lines := []string{fmt.Sprintf("优先级调整失败：%d 个", len(items))}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf(
			"- [%s] %s：%d -> %d（stage=%s code=%s）",
			item.Channel,
			poolAccountLabel(item.AccountName, item.AccountID),
			item.BeforePriority,
			item.TargetPriority,
			item.Stage,
			item.Code,
		))
	}
	return strings.Join(lines, "\n")
}

func poolAccountLabel(name string, id int64) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Sprintf("账号 #%d", id)
	}
	return fmt.Sprintf("%s (#%d)", name, id)
}

func formatPoolRate(value *float64) string {
	if value == nil {
		return "未接入"
	}
	return fmt.Sprintf("%.6g", *value)
}

func joinPoolIDs(ids []int64) string {
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	parts := make([]string, 0, len(sorted))
	for _, id := range sorted {
		parts = append(parts, fmt.Sprintf("#%d", id))
	}
	return strings.Join(parts, "、")
}
