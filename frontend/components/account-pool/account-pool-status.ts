import type { Sub2PoolAccount } from "@/lib/api-types"
import { decimal, formatRatio } from "@/lib/format"

export type AccountPoolHealthTone = "healthy" | "limited" | "warning" | "failed" | "unknown"
export type AccountPoolBalanceTone = "ok" | "low" | "debt" | "unknown"

const MISSING_LABELS: Record<string, string> = {
  balance: "余额缺失",
  balance_status: "余额状态缺失",
  health: "健康状态缺失",
  health_status: "健康状态缺失",
  multiplier: "倍率缺失",
  upstream_multiplier: "倍率缺失",
  priority: "优先级缺失",
  current_priority: "优先级缺失",
  schedulable: "调度状态缺失",
  concurrency: "并发信息缺失",
  group: "分组/渠道缺失",
  business_channel: "业务渠道缺失",
  min_group: "最低分组缺失",
}

export function accountBusinessChannel(account: Sub2PoolAccount) {
  return account.business_channel?.trim() || account.min_group?.trim() || "—"
}

export function accountGroupLabel(account: Sub2PoolAccount) {
  const group = account.min_group?.trim() || "—"
  const channel = account.business_channel?.trim()
  if (!channel || channel === group) return group
  return `${group} / ${channel}`
}

/** 匹配精准度文案：远端 Key 精确与人工核验绑定必须明确区分。 */
export function accountMatchLabel(account: Sub2PoolAccount) {
  switch (account.match_status) {
    case "key_exact":
      return "API Key 精确匹配"
    case "key_attested":
      return "已核验 Key 绑定"
    case "account_mapping":
      return "账号映射（非 Key）"
    case "channel_name_exact":
      return "渠道名匹配（非 Key）"
    case "group_name_exact":
      return "分组名匹配（非 Key）"
    case "key_mismatch":
      return "API Key 不一致"
    case "key_ambiguous":
      return "API Key 匹配不唯一"
    case "fingerprint_missing":
      return "Sub2 未提供 Key 指纹"
    case "upstream_unavailable":
      return "上游 Key 采集不可用"
    case "monitor_source_missing":
      return "无同源监控渠道"
    case "url_missing":
      return "上游地址缺失"
    case "refresh_pending":
      return "等待下次扫描"
    default:
      return account.match_status?.trim() || "未匹配"
  }
}

export function accountMatchTone(account: Sub2PoolAccount): AccountPoolHealthTone {
  switch (account.match_status) {
    case "key_exact":
    case "key_attested":
      return "healthy"
    case "account_mapping":
    case "channel_name_exact":
    case "group_name_exact":
      return "warning"
    case "key_mismatch":
    case "key_ambiguous":
    case "upstream_unavailable":
    case "fingerprint_missing":
    case "url_missing":
      return "failed"
    case "monitor_source_missing":
      return "warning"
    default:
      return "unknown"
  }
}

/** 倍率旁的来源标签：可信 / 映射 / 仅展示 */
export function accountMultiplierSourceLabel(account: Sub2PoolAccount) {
  if (account.multiplier_source === "key_attested") return "已核验 Key"
  if (account.multiplier_confidence === "trusted" || account.multiplier_source === "key_exact") {
    return "Key 可信"
  }
  if (account.multiplier_source === "account_mapping") return "映射（非 Key）"
  if (account.upstream_multiplier == null) return "待映射"
  return "仅展示（非 Key）"
}

export function formatNumeric(value: number | null | undefined, fallback = "—") {
  if (value == null || !Number.isFinite(value)) return fallback
  return value.toLocaleString("zh-CN")
}

/** 上游倍率显示：nil 显示 —，保留低倍率精度并去掉浮点噪声。 */
export function accountMultiplierLabel(account: Sub2PoolAccount) {
  return formatRatio(account.upstream_multiplier)
}

/** 余额显示：nil 显示 —，保留 2-4 位小数。 */
export function accountBalanceValueLabel(account: Sub2PoolAccount) {
  if (account.balance == null) return "—"
  return decimal(account.balance, 4)
}

export function isSchedulable(account: Sub2PoolAccount) {
  return account.schedulable !== false
}

export function accountBalanceTone(account: Sub2PoolAccount): AccountPoolBalanceTone {
  const status = (account.balance_status ?? "").toLowerCase()
  if (status.includes("missing") || status.includes("unknown") || status.includes("缺失")) return "unknown"
  if (status.includes("debt") || status.includes("overdue") || status.includes("欠费")) return "debt"
  if (status.includes("low") || status.includes("warn") || status.includes("不足")) return "low"
  if (account.balance != null && account.balance < 0) return "debt"
  if (account.balance != null && account.balance < 10) return "low"
  if (account.balance_status) return "ok"
  return "unknown"
}

export function accountHealthTone(account: Sub2PoolAccount): AccountPoolHealthTone {
  const status = (account.health_status ?? "").toLowerCase()
  const rate = (account.rate_limit_status ?? "").toLowerCase()
  if (status.includes("disabled") || status.includes("inactive")) return "failed"
  if (status.includes("fail") || status.includes("error") || status.includes("bad") || status.includes("failed")) {
    return "failed"
  }
  if (status.includes("limit") || rate.includes("limit") || rate.includes("限流")) return "limited"
  if (
    status.includes("warn") ||
    status.includes("slow") ||
    status.includes("degraded") ||
    status.includes("temporarily_unschedulable") ||
    status.includes("overloaded") ||
    rate.includes("temporarily_unschedulable") ||
    rate.includes("overloaded")
  ) {
    return "warning"
  }
  if (status === "healthy") return "healthy"
  return "unknown"
}

export function accountMissingLabels(account: Sub2PoolAccount) {
  const raw = account.missing_data ?? []
  const labels = raw
    .map((item) => MISSING_LABELS[item] ?? item)
    .filter((item, index, list) => list.indexOf(item) === index)
  if (labels.length > 0) return labels

  const derived: string[] = []
  if (account.upstream_multiplier == null) derived.push("倍率缺失")
  if (account.current_priority == null) derived.push("优先级缺失")
  if (account.balance == null && !account.balance_status) derived.push("余额缺失")
  if (!account.health_status && !account.rate_limit_status) derived.push("健康状态缺失")
  return derived
}

export function hasMissingAccountField(account: Sub2PoolAccount, keyword: string) {
  const haystack = [...(account.missing_data ?? []), ...accountMissingLabels(account)]
  return haystack.some((item) => item.includes(keyword))
}

export function accountHealthLabel(account: Sub2PoolAccount) {
  const tone = accountHealthTone(account)
  switch (tone) {
    case "healthy":
      return "健康"
    case "limited":
      return "限流"
    case "warning":
      return "警告"
    case "failed":
      return "异常"
    default:
      return "未知"
  }
}

export function accountBalanceLabel(account: Sub2PoolAccount) {
  const tone = accountBalanceTone(account)
  switch (tone) {
    case "ok":
      return "正常"
    case "low":
      return "偏低"
    case "debt":
      return "欠费"
    default:
      return "未知"
  }
}

export function accountSchedulableLabel(account: Sub2PoolAccount) {
  return isSchedulable(account) ? "可调度" : "暂停"
}

/** 当前优先级与建议优先级都有限值且不相等。 */
export function hasPriorityMismatch(account: Sub2PoolAccount) {
  const current = account.current_priority
  const suggested = account.suggested_priority
  if (current == null || suggested == null) return false
  if (!Number.isFinite(current) || !Number.isFinite(suggested)) return false
  return current !== suggested
}

/** 是否缺倍率（字段为 null 或 missing 标注含倍率）。 */
export function isMissingMultiplier(account: Sub2PoolAccount) {
  return account.upstream_multiplier == null || hasMissingAccountField(account, "倍率")
}

/**
 * 需要处理的「问题账号」：
 * - 余额 debt/low
 * - 健康非 healthy
 * - 缺倍率
 * - 优先级错位
 * - 关键字段缺失
 */
export function isProblemAccount(account: Sub2PoolAccount) {
  const balanceTone = accountBalanceTone(account)
  if (balanceTone === "debt" || balanceTone === "low") return true
  if (accountHealthTone(account) !== "healthy") return true
  if (isMissingMultiplier(account)) return true
  if (hasPriorityMismatch(account)) return true
  if (accountMissingLabels(account).length > 0) return true
  return false
}

/** 问题原因标签，供列表小标签展示。 */
export function accountProblemReasons(account: Sub2PoolAccount): string[] {
  const reasons: string[] = []
  const balanceTone = accountBalanceTone(account)
  if (balanceTone === "debt") reasons.push("欠费")
  else if (balanceTone === "low") reasons.push("余额偏低")

  const healthTone = accountHealthTone(account)
  if (healthTone !== "healthy") {
    reasons.push(accountHealthLabel(account))
  }

  if (isMissingMultiplier(account)) reasons.push("缺倍率")
  if (hasPriorityMismatch(account)) reasons.push("优先级错位")

  for (const label of accountMissingLabels(account)) {
    if (label === "倍率缺失" && reasons.includes("缺倍率")) continue
    if (!reasons.includes(label)) reasons.push(label)
  }
  return reasons
}
