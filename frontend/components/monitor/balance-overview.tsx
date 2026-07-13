"use client"

import { Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis, CartesianGrid } from "recharts"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { useIsMobile } from "@/hooks/use-mobile"
import { useBalanceTrend, useCostTrend, useDashboardSummary } from "@/lib/queries"
import { money } from "@/lib/format"
import { cn } from "@/lib/utils"

function formatY(n: number) {
  if (n === 0) return "$0"
  if (n >= 1000) return `$${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}K`
  if (n >= 100) return `$${n.toFixed(0)}`
  return `$${n.toFixed(n >= 10 ? 1 : 2)}`
}

/**
 * niceCeil 把最大值向上取整到一个"好看的"刻度，避免曲线贴顶。
 * 例如 47 → 50；478 → 500；12,300 → 15,000。
 */
function niceCeil(n: number): number {
  if (!Number.isFinite(n) || n <= 0) return 10
  const padded = n * 1.15
  const mag = Math.pow(10, Math.floor(Math.log10(padded)))
  const norm = padded / mag
  const step = norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 5 ? 5 : 10
  return step * mag
}

function formatDay(iso: string) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return `${d.getMonth() + 1}月${d.getDate()}日`
}

interface ChartPoint {
  day: string
  balance: number | null
  cost: number | null
}

interface TooltipPayloadItem {
  dataKey?: string
  value: number
}

function ChartTooltip({ active, payload, label }: { active?: boolean; payload?: TooltipPayloadItem[]; label?: string }) {
  if (!active || !payload?.length) return null
  const balance = payload.find((p) => p.dataKey === "balance")?.value
  const cost = payload.find((p) => p.dataKey === "cost")?.value
  return (
    <div className="rounded-md border border-border bg-popover px-3 py-2 shadow-[var(--shadow-elevated)]">
      <p className="text-xs text-muted-foreground">{label}</p>
      {balance != null ? (
        <p className="text-sm font-semibold text-brand">
          {"余额："}{money(balance)}
        </p>
      ) : null}
      {cost != null ? (
        <p className="mt-1 text-sm font-semibold text-warning">
          {"消费："}{money(cost)}
        </p>
      ) : null}
    </div>
  )
}

export function BalanceOverview() {
  const isMobile = useIsMobile()
  const trend = useBalanceTrend(7)
  const costTrend = useCostTrend(7)
  const summary = useDashboardSummary()

  const channels = summary.data?.channels ?? []
  const trendMap = new Map<string, ChartPoint>()

  for (const point of trend.data ?? []) {
    const key = point.day
    const existing = trendMap.get(key)
    trendMap.set(key, {
      day: formatDay(point.day),
      balance: point.balance,
      cost: existing?.cost ?? null,
    })
  }
  for (const point of costTrend.data ?? []) {
    const key = point.day
    const existing = trendMap.get(key)
    trendMap.set(key, {
      day: existing?.day ?? formatDay(point.day),
      balance: existing?.balance ?? null,
      cost: point.cost,
    })
  }

  const data = Array.from(trendMap.entries())
    .sort(([a], [b]) => new Date(a).getTime() - new Date(b).getTime())
    .map(([, value]) => value)
  const balanceValues = data.map((d) => d.balance ?? 0)
  const costValues = data.map((d) => d.cost ?? 0)
  const yMax = data.length > 0 ? niceCeil(Math.max(...balanceValues)) : 10
  const costMax = data.length > 0 ? niceCeil(Math.max(...costValues)) : 10
  const isLoading = trend.loading || costTrend.loading
  const chartMargin = isMobile
    ? { top: 6, right: 4, left: -18, bottom: 0 }
    : { top: 8, right: 12, left: 0, bottom: 0 }
  const dot = isMobile ? false : { r: 4, fill: "var(--background)", strokeWidth: 2 }
  const activeDot = isMobile ? { r: 4, strokeWidth: 0 } : { r: 5, strokeWidth: 0 }

  return (
    <Card className="min-w-0 gap-0 overflow-hidden border border-border py-0 shadow-[var(--shadow-card)] lg:h-105">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0">
          <CardTitle className="text-sm font-semibold">{"余额与消费趋势"}</CardTitle>
          <p className="mt-0.5 text-[11px] text-muted-foreground">最近 7 天采样</p>
        </div>
        <div className="flex shrink-0 items-center gap-3 text-[11px]">
          <span className="inline-flex items-center gap-1.5">
            <span className="h-0.5 w-4 rounded-full bg-brand" />
            <span className="text-muted-foreground">余额</span>
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span className="h-0.5 w-4 rounded-full bg-warning" />
            <span className="text-muted-foreground">消费</span>
          </span>
        </div>
      </CardHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col px-0">
        <div className="h-64 w-full px-2 pt-3 sm:h-72 sm:px-4 lg:h-auto lg:min-h-40 lg:flex-1">
          {isLoading ? (
            <div className="flex h-full items-center justify-center text-xs text-muted-foreground">{"加载中…"}</div>
          ) : data.length === 0 ? (
            <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
              {"暂无趋势采样，等待下次扫描或手动刷新"}
            </div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={data} margin={chartMargin}>
                <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" vertical={false} />
                <XAxis
                  dataKey="day"
                  tickLine={false}
                  axisLine={false}
                  interval={isMobile ? 1 : 0}
                  minTickGap={isMobile ? 8 : 5}
                  tick={{ fill: "var(--muted-foreground)", fontSize: isMobile ? 10 : 11 }}
                  dy={isMobile ? 6 : 8}
                />
                <YAxis
                  tickLine={false}
                  axisLine={false}
                  width={isMobile ? 40 : 48}
                  tick={{ fill: "var(--muted-foreground)", fontSize: isMobile ? 10 : 11 }}
                  tickFormatter={formatY}
                  domain={[0, yMax]}
                />
                <YAxis
                  yAxisId="cost"
                  orientation="right"
                  tickLine={false}
                  axisLine={false}
                  width={isMobile ? 0 : 52}
                  tick={isMobile ? false : { fill: "var(--muted-foreground)", fontSize: 11 }}
                  tickFormatter={formatY}
                  domain={[0, costMax]}
                />
                <Tooltip content={<ChartTooltip />} cursor={{ stroke: "var(--border)", strokeDasharray: "4 4" }} />
                <Line
                  type="monotone"
                  dataKey="balance"
                  stroke="var(--brand)"
                  strokeWidth={2.25}
                  dot={dot}
                  activeDot={{ ...activeDot, fill: "var(--brand)" }}
                />
                <Line
                  yAxisId="cost"
                  type="monotone"
                  dataKey="cost"
                  stroke="var(--warning)"
                  strokeWidth={2.25}
                  connectNulls={false}
                  dot={dot}
                  activeDot={{ ...activeDot, fill: "var(--warning)" }}
                />
              </LineChart>
            </ResponsiveContainer>
          )}
        </div>

        {/* per-channel snapshot */}
        {channels.length > 0 ? (
          <div className="shrink-0 border-t border-border">
            <div className="flex items-center justify-between px-4 py-2">
              <span className="text-[11px] font-medium text-muted-foreground">渠道快照</span>
              <span className="text-[10px] text-muted-foreground">{channels.length} 个渠道</span>
            </div>
            <div className="grid max-h-23 grid-cols-1 overflow-y-auto border-t border-border sm:grid-cols-2">
              {channels.map((c) => {
                const isFailed = !!c.last_error
                const isUnknown = c.last_balance == null
                return (
                  <div
                    key={c.id}
                    className="flex min-w-0 items-center gap-2 border-b border-border px-4 py-2 last:border-b-0 sm:odd:border-r"
                  >
                    <span
                      className={cn(
                        "size-1.5 shrink-0 rounded-full",
                        isFailed ? "bg-danger" : isUnknown ? "bg-muted-foreground/40" : "bg-success",
                      )}
                    />
                    <span className="min-w-0 flex-1 truncate text-[11px] font-medium text-foreground">{c.name}</span>
                    <span className="shrink-0 text-right text-[10px] tabular-nums text-muted-foreground">
                      <span className="text-foreground">{money(c.last_balance)}</span>
                      {" / "}
                      {money(c.today_cost)}
                    </span>
                  </div>
                )
              })}
            </div>
          </div>
        ) : null}
      </CardContent>
    </Card>
  )
}
