import { RefreshCw, Target } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import type { Sub2PoolSnapshotSummary, Sub2PoolTarget } from "@/lib/api-types"
import { dateTime, relativeTime } from "@/lib/format"
import { cn } from "@/lib/utils"

interface AccountPoolSummaryStripProps {
  targets: Sub2PoolTarget[]
  selectedTargetID: string | null
  summary: Required<Pick<
    Sub2PoolSnapshotSummary,
    | "total_accounts"
    | "schedulable_accounts"
    | "healthy_accounts"
    | "debt_accounts"
    | "missing_multiplier_accounts"
  >>
  refreshedAt?: string | null
  loading?: boolean
  onTargetChange: (targetID: string) => void
  onRefresh: () => void
}

function SummaryTile({
  label,
  value,
  tone = "default",
}: {
  label: string
  value: number
  tone?: "default" | "success" | "warning" | "danger"
}) {
  return (
    <div className="min-w-0 rounded-lg border border-border bg-background px-3 py-2">
      <div className="text-[11px] leading-4 text-muted-foreground">{label}</div>
      <div
        className={cn(
          "mt-1 truncate text-xl font-semibold tabular-nums text-foreground",
          tone === "success" && "text-success",
          tone === "warning" && "text-warning",
          tone === "danger" && "text-danger",
        )}
      >
        {value.toLocaleString("zh-CN")}
      </div>
    </div>
  )
}

export function AccountPoolSummaryStrip({
  targets,
  selectedTargetID,
  summary,
  refreshedAt,
  loading,
  onTargetChange,
  onRefresh,
}: AccountPoolSummaryStripProps) {
  return (
    <section className="rounded-lg border border-border bg-card p-3 shadow-sm sm:p-4">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-end lg:justify-between">
        <div className="min-w-0 space-y-2">
          <div className="flex items-center gap-2 text-sm font-semibold">
            <Target className="size-4 text-muted-foreground" />
            <span>Sub2 账号池</span>
          </div>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Select
              value={selectedTargetID ?? undefined}
              onValueChange={onTargetChange}
              disabled={targets.length === 0}
            >
              <SelectTrigger className="w-full min-w-0 sm:w-72">
                <SelectValue placeholder="选择 target" />
              </SelectTrigger>
              <SelectContent>
                {targets.map((target) => (
                  <SelectItem key={target.id} value={target.id}>
                    {target.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onRefresh}
              disabled={loading || !selectedTargetID}
              className="w-full gap-1.5 sm:w-auto"
            >
              <RefreshCw className={cn("size-3.5", loading && "animate-spin")} />
              刷新
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            {refreshedAt
              ? `数据更新时间：${dateTime(refreshedAt)}（${relativeTime(refreshedAt)}）`
              : "尚未加载账号数据，点击刷新后读取最新快照"}
          </p>
        </div>

        <div className="grid grid-cols-2 gap-2 sm:grid-cols-5 lg:w-[660px]">
          <SummaryTile label="账号总数" value={summary.total_accounts} />
          <SummaryTile label="可调度" value={summary.schedulable_accounts} tone="success" />
          <SummaryTile label="健康" value={summary.healthy_accounts} tone="success" />
          <SummaryTile label="欠费" value={summary.debt_accounts} tone="danger" />
          <SummaryTile
            label="缺倍率"
            value={summary.missing_multiplier_accounts}
            tone={summary.missing_multiplier_accounts > 0 ? "warning" : "default"}
          />
        </div>
      </div>
    </section>
  )
}
