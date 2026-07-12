import { ArrowRight, GitCompareArrows, Loader2, Play, ShieldAlert } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { ScrollArea } from "@/components/ui/scroll-area"
import type { Sub2PoolPriorityPreviewResponse } from "@/lib/api-types"
import { formatRatio } from "@/lib/format"
import { formatNumeric } from "./account-pool-status"

interface AccountPoolPreviewPanelProps {
  preview: Sub2PoolPriorityPreviewResponse | null
  loading: boolean
  error: string | null
  disabled?: boolean
  onGenerate: () => void
  onOpenApply: () => void
}

function priorityText(value: number | null | undefined) {
  return formatNumeric(value)
}

export function AccountPoolPreviewPanel({
  preview,
  loading,
  error,
  disabled,
  onGenerate,
  onOpenApply,
}: AccountPoolPreviewPanelProps) {
  const summary = preview?.summary
  const guards = preview?.guards ?? []
  const blocked = guards.length > 0

  return (
    <Card className="rounded-lg border-border bg-card py-0 shadow-sm">
      <CardHeader className="border-b border-border px-3 py-3 sm:px-4">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            <CardTitle className="flex items-center gap-2 text-sm">
              <GitCompareArrows className="size-4 text-muted-foreground" />
              优先级预览
            </CardTitle>
            <p className="mt-1 text-xs text-muted-foreground">
              排序和目标优先级由后端计算，前端只展示结果。
            </p>
          </div>
          <div className="flex gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onGenerate}
              disabled={disabled || loading}
              className="gap-1.5"
            >
              {loading ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
              生成预览
            </Button>
            <Button
              type="button"
              size="sm"
              onClick={onOpenApply}
              disabled={!preview || loading || blocked || (summary?.changed ?? 0) === 0}
            >
              {blocked ? "守卫阻断" : "Apply"}
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="p-3 sm:p-4">
        {error ? (
          <div className="rounded-lg border border-danger/20 bg-danger/10 px-3 py-2 text-xs text-danger">
            {error}
          </div>
        ) : null}

        {!preview ? (
          <div className="rounded-lg border border-dashed border-border bg-muted/20 px-3 py-6 text-center text-sm text-muted-foreground">
            {"生成后会显示每个账号的 before -> target，以及后端给出的跳过原因。"}
          </div>
        ) : (
          <div className="space-y-3">
            <div className="flex flex-wrap gap-2 text-xs">
              <Badge variant="outline" className="rounded-md">
                总项 {summary?.total ?? preview.items.length}
              </Badge>
              <Badge variant="outline" className="rounded-md border-success/20 bg-success/10 text-success">
                修改 {summary?.changed ?? preview.items.filter((item) => !item.skip_reason).length}
              </Badge>
              <Badge variant="outline" className="rounded-md border-warning/20 bg-warning/10 text-warning">
                跳过 {summary?.skipped ?? preview.items.filter((item) => item.skip_reason).length}
              </Badge>
              <div className="max-w-full rounded-md border border-border bg-muted px-2 py-1 font-mono text-[10px] break-all">
                {preview.snapshot_signature}
              </div>
            </div>

            {blocked ? (
              <div className="rounded-lg border border-warning/25 bg-warning/10 px-3 py-2 text-xs text-warning">
                <div className="mb-1 flex items-center gap-1.5 font-medium">
                  <ShieldAlert className="size-3.5" />
                  保护性跳过，本次不可应用
                </div>
                <ul className="space-y-1">
                  {guards.map((guard) => (
                    <li key={`${guard.code}-${guard.count ?? 0}`}>
                      {guard.code}：{guard.message}
                      {guard.count != null ? `（${guard.count}）` : ""}
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}

            <ScrollArea className="h-80 rounded-lg border border-border">
              <div className="divide-y divide-border">
                {preview.items.map((item) => {
                  const skipped = Boolean(item.skip_reason)
                  return (
                    <div key={`${item.account_id}-${item.target_priority ?? "skip"}`} className="p-3">
                      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
                        <div className="min-w-0">
                          <div className="truncate text-sm font-medium">{item.account_name}</div>
                          <div className="text-[11px] text-muted-foreground">#{item.account_id}</div>
                        </div>
                        <Badge
                          variant="outline"
                          className={
                            skipped
                              ? "rounded-md border-warning/20 bg-warning/10 text-warning"
                              : "rounded-md border-success/20 bg-success/10 text-success"
                          }
                        >
                          {skipped ? "跳过" : "修改"}
                        </Badge>
                      </div>
                      <div className="mt-2 flex flex-wrap items-center gap-2 text-xs">
                        <span className="rounded-md bg-muted px-2 py-1 font-mono">
                          {priorityText(item.before_priority)}
                        </span>
                        <ArrowRight className="size-3.5 text-muted-foreground" />
                        <span className="rounded-md bg-muted px-2 py-1 font-mono">
                          {priorityText(item.target_priority)}
                        </span>
                        {item.multiplier_before != null || item.multiplier_target != null ? (
                          <span className="rounded-md border border-border px-2 py-1 font-mono text-muted-foreground">
                            倍率 {formatRatio(item.multiplier_before)} {"->"} {formatRatio(item.multiplier_target)}
                          </span>
                        ) : null}
                      </div>
                      {item.skip_reason ? (
                        <div className="mt-2 flex gap-1.5 rounded-md border border-dashed border-border bg-muted/20 px-2 py-1.5 text-[11px] text-muted-foreground">
                          <ShieldAlert className="mt-0.5 size-3 shrink-0" />
                          <span>{item.skip_reason}</span>
                        </div>
                      ) : null}
                    </div>
                  )
                })}
              </div>
            </ScrollArea>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
