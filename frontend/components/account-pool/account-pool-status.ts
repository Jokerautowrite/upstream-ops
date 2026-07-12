import type { Sub2PoolAccount } from "@/lib/api-types"

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

export function formatNumeric(value: number | null | undefined, fallback = "—") {
  if (value == null || !Number.isFinite(value)) return fallback
  return value.toLocaleString("zh-CN")
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
