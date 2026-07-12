package sub2pool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

func buildPriorityPreview(snapshot Snapshot) PriorityPreview {
	preview := PriorityPreview{
		TargetID:    snapshot.TargetID,
		GeneratedAt: time.Now(),
	}
	buckets := make(map[string][]AccountSnapshot, len(channelOrder))
	debtBuckets := make(map[string][]AccountSnapshot, len(channelOrder))
	channelFloor := make(map[string]int, len(channelOrder))
	groupFloor := make(map[int64]int)
	reservedChannels := make(map[string]map[int]struct{}, len(channelOrder))
	reservedGroups := make(map[int64]map[int]struct{})

	for _, account := range snapshot.Accounts {
		if !account.Schedulable {
			continue
		}
		if !account.PoolManaged {
			reserveAccountPriority(reservedChannels, reservedGroups, account, account.CurrentPriority)
			raiseAccountFloors(channelFloor, groupFloor, account, account.CurrentPriority)
			continue
		}
		if account.Channel == ChannelOther {
			preview.UnknownChannelIDs = append(preview.UnknownChannelIDs, account.ID)
		}
		if !account.Availability.BalanceAvailable {
			preview.MissingBalanceIDs = append(preview.MissingBalanceIDs, account.ID)
			reserveAccountPriority(reservedChannels, reservedGroups, account, account.CurrentPriority)
			raiseAccountFloors(channelFloor, groupFloor, account, account.CurrentPriority)
			continue
		}
		if !account.Availability.RateAvailable {
			preview.MissingMultiplierIDs = append(preview.MissingMultiplierIDs, account.ID)
			if account.Balance == nil || *account.Balance > 0 {
				reserveAccountPriority(reservedChannels, reservedGroups, account, account.CurrentPriority)
				raiseAccountFloors(channelFloor, groupFloor, account, account.CurrentPriority)
				continue
			}
		}
		if account.Balance != nil && *account.Balance <= 0 {
			debtBuckets[account.Channel] = append(debtBuckets[account.Channel], account)
			continue
		}
		buckets[account.Channel] = append(buckets[account.Channel], account)
	}

	// Funded accounts are assigned first across every business channel. This
	// preserves the channel-only ordering rule while reserving each target in
	// every real Sub2 group the account belongs to.
	for _, channel := range channelOrder {
		items := buckets[channel]
		if len(items) == 0 {
			continue
		}
		sort.Slice(items, func(i, j int) bool {
			return ratePriorityIDLess(items[i], items[j])
		})
		nextPriority := 10
		for _, item := range items {
			nextPriority = nextAvailableAccountPriority(
				nextPriority,
				item,
				reservedChannels,
				reservedGroups,
			)
			preview.addProposal(item, channel, nextPriority, "upstream_rate_ascending")
			reserveAccountPriority(reservedChannels, reservedGroups, item, nextPriority)
			raiseAccountFloors(channelFloor, groupFloor, item, nextPriority)
			nextPriority += 10
		}
	}

	// Debt accounts are assigned only after all funded targets are known. Their
	// target must be after every reserved account in both the business channel
	// and each actual Sub2 group, including accounts this module skipped.
	for _, channel := range channelOrder {
		debt := debtBuckets[channel]
		if len(debt) == 0 {
			continue
		}
		sort.Slice(debt, func(i, j int) bool {
			left, right := debt[i].UpstreamRate, debt[j].UpstreamRate
			switch {
			case left == nil && right == nil:
				return priorityIDLess(debt[i], debt[j])
			case left == nil:
				return false
			case right == nil:
				return true
			default:
				return ratePriorityIDLess(debt[i], debt[j])
			}
		})
		nextDebtPriority := roundUpTen(channelFloor[channel]) + 10
		for _, item := range debt {
			for _, groupID := range item.GroupIDs {
				nextDebtPriority = max(nextDebtPriority, roundUpTen(groupFloor[groupID])+10)
			}
			nextDebtPriority = nextAvailableAccountPriority(
				nextDebtPriority,
				item,
				reservedChannels,
				reservedGroups,
			)
			reason := "debt_last"
			if item.UpstreamRate == nil {
				reason = "debt_missing_multiplier_last"
			}
			preview.addProposal(item, channel, nextDebtPriority, reason)
			reserveAccountPriority(reservedChannels, reservedGroups, item, nextDebtPriority)
			raiseAccountFloors(channelFloor, groupFloor, item, nextDebtPriority)
			nextDebtPriority += 10
		}
	}

	preview.Signature = previewSignature(snapshot, preview.Proposals)
	return preview
}

func reserveChannelPriority(reserved map[string]map[int]struct{}, channel string, priority int) {
	if priority <= 0 {
		return
	}
	if reserved[channel] == nil {
		reserved[channel] = map[int]struct{}{}
	}
	reserved[channel][priority] = struct{}{}
}

func reserveGroupPriority(reserved map[int64]map[int]struct{}, groupID int64, priority int) {
	if groupID <= 0 || priority <= 0 {
		return
	}
	if reserved[groupID] == nil {
		reserved[groupID] = map[int]struct{}{}
	}
	reserved[groupID][priority] = struct{}{}
}

func reserveAccountPriority(
	reservedChannels map[string]map[int]struct{},
	reservedGroups map[int64]map[int]struct{},
	account AccountSnapshot,
	priority int,
) {
	reserveChannelPriority(reservedChannels, account.Channel, priority)
	for _, groupID := range account.GroupIDs {
		reserveGroupPriority(reservedGroups, groupID, priority)
	}
}

func raiseAccountFloors(
	channelFloor map[string]int,
	groupFloor map[int64]int,
	account AccountSnapshot,
	priority int,
) {
	channelFloor[account.Channel] = max(channelFloor[account.Channel], priority)
	for _, groupID := range account.GroupIDs {
		groupFloor[groupID] = max(groupFloor[groupID], priority)
	}
}

func nextAvailableAccountPriority(
	candidate int,
	account AccountSnapshot,
	reservedChannels map[string]map[int]struct{},
	reservedGroups map[int64]map[int]struct{},
) int {
	candidate = max(10, roundUpTen(candidate))
	for {
		if priorityAvailableForAccount(candidate, account, reservedChannels, reservedGroups) {
			return candidate
		}
		candidate += 10
	}
}

func priorityAvailableForAccount(
	priority int,
	account AccountSnapshot,
	reservedChannels map[string]map[int]struct{},
	reservedGroups map[int64]map[int]struct{},
) bool {
	if _, exists := reservedChannels[account.Channel][priority]; exists {
		return false
	}
	for _, groupID := range account.GroupIDs {
		if _, exists := reservedGroups[groupID][priority]; exists {
			return false
		}
	}
	return true
}

func (p *PriorityPreview) addProposal(item AccountSnapshot, channel string, target int, reason string) {
	proposal := PriorityProposal{
		AccountID:              item.ID,
		AccountName:            item.Name,
		CurrentPriority:        item.CurrentPriority,
		TargetPriority:         target,
		Channel:                channel,
		Reason:                 reason,
		expectedGroupIDs:       append([]int64(nil), item.GroupIDs...),
		expectedStatus:         item.Status,
		expectedPoolManaged:    item.PoolManaged,
		expectedIdentityDigest: item.IdentityDigest,
	}
	p.Proposals = append(p.Proposals, proposal)
	if proposal.CurrentPriority != proposal.TargetPriority {
		p.Changes = append(p.Changes, proposal)
	}
}

func ratePriorityIDLess(left, right AccountSnapshot) bool {
	if *left.UpstreamRate != *right.UpstreamRate {
		return *left.UpstreamRate < *right.UpstreamRate
	}
	return priorityIDLess(left, right)
}

func priorityIDLess(left, right AccountSnapshot) bool {
	if left.CurrentPriority != right.CurrentPriority {
		return left.CurrentPriority < right.CurrentPriority
	}
	return left.ID < right.ID
}

func roundUpTen(value int) int {
	if value <= 0 {
		return 0
	}
	return ((value + 9) / 10) * 10
}

func previewSignature(snapshot Snapshot, proposals []PriorityProposal) string {
	canonical := append([]PriorityProposal(nil), proposals...)
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Channel != canonical[j].Channel {
			return channelRank(canonical[i].Channel) < channelRank(canonical[j].Channel)
		}
		if canonical[i].TargetPriority != canonical[j].TargetPriority {
			return canonical[i].TargetPriority < canonical[j].TargetPriority
		}
		return canonical[i].AccountID < canonical[j].AccountID
	})
	type signatureAccount struct {
		ID              int64    `json:"id"`
		Type            string   `json:"type"`
		Status          string   `json:"status"`
		Schedulable     bool     `json:"schedulable"`
		PoolManaged     bool     `json:"pool_managed"`
		CurrentPriority int      `json:"current_priority"`
		GroupIDs        []int64  `json:"group_ids"`
		Channel         string   `json:"channel"`
		UpstreamRate    *float64 `json:"upstream_rate,omitempty"`
		BalanceState    string   `json:"balance_state"`
		MatchStatus     string   `json:"match_status"`
		IdentityDigest  string   `json:"identity_digest"`
	}
	accounts := make([]signatureAccount, 0, len(snapshot.Accounts))
	for _, account := range snapshot.Accounts {
		groupIDs := append([]int64(nil), account.GroupIDs...)
		sort.Slice(groupIDs, func(i, j int) bool { return groupIDs[i] < groupIDs[j] })
		accounts = append(accounts, signatureAccount{
			ID:              account.ID,
			Type:            account.Type,
			Status:          account.Status,
			Schedulable:     account.Schedulable,
			PoolManaged:     account.PoolManaged,
			CurrentPriority: account.CurrentPriority,
			GroupIDs:        groupIDs,
			Channel:         account.Channel,
			UpstreamRate:    cloneFloat(account.UpstreamRate),
			BalanceState:    balanceSignatureState(account.Balance),
			MatchStatus:     account.MatchStatus,
			IdentityDigest:  account.IdentityDigest,
		})
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	payload := struct {
		TargetID  uint               `json:"target_id"`
		Accounts  []signatureAccount `json:"accounts"`
		Proposals []PriorityProposal `json:"proposals"`
	}{
		TargetID:  snapshot.TargetID,
		Accounts:  accounts,
		Proposals: canonical,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func balanceSignatureState(balance *float64) string {
	if balance == nil {
		return "missing"
	}
	if *balance <= 0 {
		return "debt"
	}
	return "funded"
}

func channelRank(channel string) int {
	for index, candidate := range channelOrder {
		if channel == candidate {
			return index
		}
	}
	return len(channelOrder)
}

func classifyChannel(lowestGroupName string) string {
	name := strings.ToLower(strings.TrimSpace(lowestGroupName))
	switch {
	case strings.Contains(name, "kiro"):
		return ChannelKiro
	case strings.Contains(name, "claude code"),
		strings.Contains(name, "claude"),
		strings.Contains(name, "fable"),
		strings.Contains(name, "cc"):
		return ChannelCC
	case strings.Contains(name, "pro"):
		return ChannelPro
	case strings.Contains(name, "plus"), strings.Contains(name, "gpt"):
		return ChannelPLUS
	case strings.Contains(name, "grok"):
		return ChannelGrok
	case strings.Contains(name, "gemini"), strings.Contains(" "+name+" ", " g "):
		return ChannelGemini
	case strings.Contains(name, "image"), strings.Contains(name, "图片"), strings.Contains(name, "生图"):
		return ChannelImage
	case strings.Contains(name, "cn"),
		strings.Contains(name, "国模"),
		strings.Contains(name, "glm"),
		strings.Contains(name, "deepseek"),
		strings.Contains(name, "kimi"),
		strings.Contains(name, "qwen"),
		strings.Contains(name, "通义"):
		return ChannelCN
	default:
		return ChannelOther
	}
}

func classifyLowestGroups(groups []GroupRef) string {
	if len(groups) == 0 {
		return ChannelOther
	}
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		names = append(names, group.Name)
	}
	return classifyChannel(strings.Join(names, "\n"))
}

func validateGuards(snapshot Snapshot, preview PriorityPreview, previous TargetState, cfg Config) []GuardViolation {
	var violations []GuardViolation
	if len(snapshot.Accounts) < cfg.MinimumAccountCount {
		violations = append(violations, GuardViolation{
			Code:    "account_count_too_low",
			Message: "account count is below the configured safety minimum",
			Count:   len(snapshot.Accounts),
		})
	}
	if previous.LastHealthyCount > 0 && snapshot.Summary.HealthyCount*4 < previous.LastHealthyCount*3 {
		violations = append(violations, GuardViolation{
			Code:    "healthy_count_drop",
			Message: "healthy account count dropped below 75 percent of the last accepted snapshot",
			Count:   snapshot.Summary.HealthyCount,
		})
	}
	seenAccounts := map[int64]struct{}{}
	for _, account := range snapshot.Accounts {
		if _, exists := seenAccounts[account.ID]; exists {
			violations = append(violations, GuardViolation{
				Code:    "duplicate_account_id",
				Message: "duplicate account id in remote snapshot",
				Count:   int(account.ID),
			})
			break
		}
		seenAccounts[account.ID] = struct{}{}
	}
	if len(preview.UnknownChannelIDs) > 0 {
		violations = append(violations, GuardViolation{
			Code:    "unknown_channel",
			Message: "one or more schedulable accounts cannot be classified into a known channel",
			Count:   len(preview.UnknownChannelIDs),
		})
	}
	if len(preview.Changes) > cfg.MaximumChanges {
		violations = append(violations, GuardViolation{
			Code:    "change_count_too_high",
			Message: "priority change count exceeds the configured safety limit",
			Count:   len(preview.Changes),
		})
	}
	violations = append(violations, validateProposalTargets(snapshot.Accounts, preview.Proposals)...)
	return violations
}

func validateProposalTargets(accounts []AccountSnapshot, proposals []PriorityProposal) []GuardViolation {
	seenAccounts := map[int64]struct{}{}
	seenChannelTargets := map[string]struct{}{}
	seenGroupTargets := map[string]struct{}{}
	accountByID := make(map[int64]AccountSnapshot, len(accounts))
	targetByID := make(map[int64]int, len(proposals))
	for _, account := range accounts {
		accountByID[account.ID] = account
	}
	for _, proposal := range proposals {
		if _, exists := seenAccounts[proposal.AccountID]; exists {
			return []GuardViolation{{
				Code:    "duplicate_account_id",
				Message: "preview contains a duplicate account id",
				Count:   int(proposal.AccountID),
			}}
		}
		seenAccounts[proposal.AccountID] = struct{}{}
		if proposal.TargetPriority <= 0 || proposal.TargetPriority%10 != 0 {
			return []GuardViolation{{
				Code:    "invalid_target_priority",
				Message: "target priority must be positive and use a step of 10",
				Count:   proposal.TargetPriority,
			}}
		}
		channelKey := proposal.Channel + ":" + strconv.Itoa(proposal.TargetPriority)
		if _, exists := seenChannelTargets[channelKey]; exists {
			return []GuardViolation{{
				Code:    "duplicate_channel_target",
				Message: "target priority is duplicated within a channel",
				Count:   proposal.TargetPriority,
			}}
		}
		seenChannelTargets[channelKey] = struct{}{}
		account, exists := accountByID[proposal.AccountID]
		if !exists {
			return []GuardViolation{{
				Code:    "proposal_account_missing",
				Message: "preview proposal references an account outside the snapshot",
				Count:   int(proposal.AccountID),
			}}
		}
		for _, groupID := range account.GroupIDs {
			groupKey := strconv.FormatInt(groupID, 10) + ":" + strconv.Itoa(proposal.TargetPriority)
			if _, exists := seenGroupTargets[groupKey]; exists {
				return []GuardViolation{{
					Code:    "duplicate_group_target",
					Message: "target priority is duplicated within an actual Sub2 group",
					Count:   proposal.TargetPriority,
				}}
			}
			seenGroupTargets[groupKey] = struct{}{}
		}
		targetByID[proposal.AccountID] = proposal.TargetPriority
	}
	for _, debt := range accounts {
		if !debt.Schedulable || debt.Balance == nil || *debt.Balance > 0 {
			continue
		}
		debtPriority, proposed := targetByID[debt.ID]
		if !proposed {
			continue
		}
		debtGroups := int64Set(debt.GroupIDs)
		for _, other := range accounts {
			if other.ID == debt.ID || !other.Schedulable || other.Balance != nil && *other.Balance <= 0 {
				continue
			}
			if !sharesGroup(debtGroups, other.GroupIDs) {
				continue
			}
			otherPriority := other.CurrentPriority
			if target, exists := targetByID[other.ID]; exists {
				otherPriority = target
			}
			if debtPriority <= otherPriority {
				return []GuardViolation{{
					Code:    "debt_not_last",
					Message: "a debt account is not after every funded account in an actual Sub2 group",
					Count:   int(debt.ID),
				}}
			}
		}
	}
	return nil
}

func sharesGroup(left map[int64]struct{}, right []int64) bool {
	for _, groupID := range right {
		if _, exists := left[groupID]; exists {
			return true
		}
	}
	return false
}
