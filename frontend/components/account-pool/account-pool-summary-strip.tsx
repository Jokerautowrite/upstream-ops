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

export type AccountPoolSummaryTileKey =
  | "total"
  | "schedulable"
  | "healthy"
  | "debt"
  | "missing_multiplier"
  | "problems"

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
  problemCount: number
  activeTile: AccountPoolSummaryTileKey | null
  refreshedAt?: string | null
  loading?: boolean
  onTargetChange: (targetID: string) => void
  onRefresh: () => void
  onTileClick: (key: AccountPoolSummaryTileKey) => void
}

function SummaryTile({
  label,
  value,
  tone = "default",
  active,
  onClick,
}: {
  label: string
  value: number
  tone?: "default" | "success" | "warning" | "danger"
  active?: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "min-w-0 rounded-lg border bg-background px-3 py-2 text-left transition-colors",
        "hover:border-foreground/25 hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        active
          ? "border-primary ring-2 ring-primary/30 bg-primary/5"
          : "border-border",
      )}
    >
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
    </button>
  )
}

export function AccountPoolSummaryStrip({
  targets,
  selectedTargetID,
  summary,
  problemCount,
  activeTile,
  refreshedAt,
  loading,
  onTargetChange,
  onRefresh,
  onTileClick,
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
            最近刷新：{dateTime(refreshedAt)}（{relativeTime(refreshedAt)}）
          </p>
        </div>

        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:w-[720px] lg:grid-cols-6">
          <SummaryTile
            label="账号总数"
            value={summary.total_accounts}
            active={activeTile === "total"}
            onClick={() => onTileClick("total")}
          />
          <SummaryTile
            label="问题"
            value={problemCount}
            tone={problemCount > 0 ? "warning" : "default"}
            active={activeTile === "problems"}
            onClick={() => onTileClick("problems")}
          />
          <SummaryTile
            label="可调度"
            value={summary.schedulable_accounts}
            tone="success"
            active={activeTile === "schedulable"}
            onClick={() => onTileClick("schedulable")}
          />
          <SummaryTile
            label="健康"
            value={summary.healthy_accounts}
            tone="success"
            active={activeTile === "healthy"}
            onClick={() => onTileClick("healthy")}
          />
          <SummaryTile
            label="欠费"
            value={summary.debt_accounts}
            tone="danger"
            active={activeTile === "debt"}
            onClick={() => onTileClick("debt")}
          />
          <SummaryTile
            label="缺倍率"
            value={summary.missing_multiplier_accounts}
            tone={summary.missing_multiplier_accounts > 0 ? "warning" : "default"}
            active={activeTile === "missing_multiplier"}
            onClick={() => onTileClick("missing_multiplier")}
          />
        </div>
      </div>
    </section>
  )
}
