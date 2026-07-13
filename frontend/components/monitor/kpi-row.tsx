"use client"

import { ArrowUpRight, DollarSign, MessageSquare, Wallet } from "lucide-react"
import { cn } from "@/lib/utils"
import { useDashboardSummary, useRateChanges } from "@/lib/queries"
import { money } from "@/lib/format"
import type { LucideIcon } from "lucide-react"
import type { ReactNode } from "react"

interface Kpi {
  label: string
  value: ReactNode
  icon: LucideIcon
  iconBg: string
  iconColor: string
  footer: ReactNode
}

function countTodayChanges(changes: { changed_at: string }[]) {
  const startOfDay = new Date()
  startOfDay.setHours(0, 0, 0, 0)
  return changes.filter((c) => new Date(c.changed_at) >= startOfDay).length
}

export function KpiRow() {
  const summary = useDashboardSummary()
  const recentChanges = useRateChanges(1, 100)

  const data = summary.data
  const total = data?.total_channels ?? 0
  const active = data?.active_channels ?? 0
  const failed = data?.failed_channels ?? 0
  const totalBalance = data?.total_balance ?? 0
  const todayTotalCost = data?.today_total_cost ?? 0
  const totalCost = data?.total_cost ?? 0
  const lowest = data?.lowest_balance ?? null

  const todayChangeCount = countTodayChanges(recentChanges.data?.items ?? [])

  const kpis: Kpi[] = [
    {
      label: "总余额",
      value: money(totalBalance),
      icon: DollarSign,
      iconBg: "bg-brand/10",
      iconColor: "text-brand",
      footer: lowest ? (
        <span className="text-muted-foreground">
          {"最低："}
          <span className="font-medium text-foreground">{lowest.name}</span>
          {" "}
          <span className="text-warning">{money(lowest.balance)}</span>
        </span>
      ) : (
        <span className="text-muted-foreground">{"—"}</span>
      ),
    },
    {
      label: "今日总消费",
      value: money(todayTotalCost),
      icon: Wallet,
      iconBg: "bg-warning/10",
      iconColor: "text-warning",
      footer: (
        <span className="text-muted-foreground">
          {todayTotalCost > 0 ? "按实际扣费统计" : "今日暂无消费"}
        </span>
      ),
    },
    {
      label: "累计消费",
      value: money(totalCost),
      icon: DollarSign,
      iconBg: "bg-brand/10",
      iconColor: "text-brand",
      footer: (
        <span className="text-muted-foreground">
          {totalCost > 0 ? "全渠道累计实际扣费" : "暂无累计消费"}
        </span>
      ),
    },
    {
      label: "渠道状态",
      value: (
        <span>
          {active}
          <span className="mx-1 text-lg font-normal text-muted-foreground">{"/"}</span>
          <span className="text-lg font-normal text-muted-foreground">{total}</span>
        </span>
      ),
      icon: MessageSquare,
      iconBg: "bg-success/10",
      iconColor: "text-success",
      footer: (
        <span className="text-muted-foreground">
          <span className="text-success font-medium">{active} 健康</span>
          {failed > 0 ? (
            <>
              {" · "}
              <span className="text-danger font-medium">{failed} 失败</span>
            </>
          ) : null}
        </span>
      ),
    },
    {
      label: "今日倍率变动",
      value: (
        <span className={cn(todayChangeCount > 0 ? "text-danger" : "text-foreground")}>
          {todayChangeCount}
        </span>
      ),
      icon: ArrowUpRight,
      iconBg: "bg-danger/10",
      iconColor: "text-danger",
      footer: (
        <span className="text-muted-foreground">
          {todayChangeCount > 0 ? `检测到 ${todayChangeCount} 次变动` : "今日无变动"}
        </span>
      ),
    },
  ]

  return (
    <section className="grid grid-cols-2 overflow-hidden rounded-lg border border-border bg-card shadow-[var(--shadow-card)] md:grid-cols-5">
      {kpis.map((k, index) => (
        <div
          key={k.label}
          className={cn(
            "flex min-w-0 items-start justify-between gap-2 border-b border-border p-3 sm:p-3.5 md:min-h-27 md:border-b-0 md:border-r",
            index % 2 === 0 && index !== kpis.length - 1 && "border-r",
            index === kpis.length - 1 && "col-span-2 border-b-0 md:col-span-1 md:border-r-0",
          )}
        >
          <div className="flex min-w-0 flex-col">
            <span className="text-[11px] font-medium text-muted-foreground">{k.label}</span>
            <p className="mt-1 text-xl font-semibold tabular-nums text-foreground">{k.value}</p>
            <p className="mt-1.5 min-w-0 text-[11px] leading-4">{k.footer}</p>
          </div>
          <span className={cn("flex size-7 shrink-0 items-center justify-center rounded-md", k.iconBg)}>
            <k.icon className={cn("size-3.5", k.iconColor)} />
          </span>
        </div>
      ))}
    </section>
  )
}
