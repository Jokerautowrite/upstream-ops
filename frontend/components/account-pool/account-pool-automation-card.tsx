import { Clock3, Loader2, ShieldAlert, Workflow } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Switch } from "@/components/ui/switch"
import { dateTime, relativeTime } from "@/lib/format"
import type { Sub2PoolAutomationStatus } from "@/lib/api-types"

interface AccountPoolAutomationCardProps {
  status: Sub2PoolAutomationStatus | null
  loading: boolean
  error?: string | null
  updating: boolean
  disabled?: boolean
  onToggle: (enabled: boolean) => void
}

function combinedResult(status: Sub2PoolAutomationStatus | null) {
  const result = status?.last_result
  if (!result) return "暂无运行结果"
  const priority = result.priority_changes ?? 0
  const multiplier = result.multiplier_changes ?? 0
  const skipped = result.skipped ?? 0
  const detail = `倍率变化 ${multiplier}，优先级修改 ${priority}，跳过 ${skipped}`
  if (result.summary) return `${result.summary} · ${detail}`
  return detail
}

export function AccountPoolAutomationCard({
  status,
  loading,
  error,
  updating,
  disabled,
  onToggle,
}: AccountPoolAutomationCardProps) {
  const reasons = status?.guard_block_reasons ?? []
  const blocked = Boolean(status?.guard_blocked || reasons.length > 0)
  const lastRun = status?.last_run_at ?? status?.last_result?.at ?? null

  return (
    <Card className="rounded-lg border-border bg-card py-0 shadow-sm">
      <CardHeader className="border-b border-border px-3 py-3 sm:px-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <CardTitle className="flex items-center gap-2 text-sm">
              <Workflow className="size-4 text-muted-foreground" />
              自动化状态
            </CardTitle>
            <p className="mt-1 text-xs text-muted-foreground">
              调度守卫、倍率变化与优先级修改使用同一条运行结果展示。
            </p>
          </div>
          <div className="flex items-center gap-2">
            {updating ? <Loader2 className="size-3.5 animate-spin text-muted-foreground" /> : null}
            <Switch
              checked={Boolean(status?.enabled)}
              disabled={disabled || updating || loading || !status}
              onCheckedChange={onToggle}
              aria-label={status?.enabled ? "关闭 Sub2 账号池自动化" : "开启 Sub2 账号池自动化"}
            />
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-3 p-3 sm:p-4">
        <div className="flex flex-wrap items-center gap-2">
          <Badge
            variant="outline"
            className={
              error || !status
                ? "rounded-md border-warning/20 bg-warning/10 text-warning"
                : status.enabled
                ? "rounded-md border-success/20 bg-success/10 text-success"
                : "rounded-md border-border bg-muted/40 text-muted-foreground"
            }
          >
            {error ? "状态获取失败" : !status ? (loading ? "加载中" : "状态未知") : status.enabled ? "已开启" : "已关闭"}
          </Badge>
          {status?.schedule ? (
            <Badge variant="outline" className="rounded-md">
              {status.schedule}
            </Badge>
          ) : null}
          {blocked ? (
            <Badge variant="outline" className="rounded-md border-warning/20 bg-warning/10 text-warning">
              守卫阻断
            </Badge>
          ) : null}
        </div>

        {error ? (
          <div className="rounded-lg border border-warning/25 bg-warning/10 px-3 py-2 text-xs text-warning">
            自动化状态未刷新：{error}
          </div>
        ) : null}

        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <div className="rounded-lg border border-border bg-muted/20 px-3 py-2">
            <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
              <Clock3 className="size-3.5" />
              最近运行
            </div>
            <div className="mt-1 text-sm font-medium">{dateTime(lastRun)}</div>
            <div className="mt-0.5 text-[11px] text-muted-foreground">{relativeTime(lastRun)}</div>
          </div>
          <div className="rounded-lg border border-border bg-muted/20 px-3 py-2">
            <div className="text-[11px] text-muted-foreground">结果摘要</div>
            <div className="mt-1 text-sm font-medium leading-5">{combinedResult(status)}</div>
          </div>
        </div>

        {blocked ? (
          <div className="rounded-lg border border-warning/25 bg-warning/10 px-3 py-2 text-xs text-warning">
            <div className="mb-1 flex items-center gap-1.5 font-medium">
              <ShieldAlert className="size-3.5" />
              守卫阻断原因
            </div>
            <ul className="space-y-1">
              {(reasons.length > 0 ? reasons : [status?.last_result?.guard_reason ?? "后端守卫阻断"]).map((reason) => (
                <li key={reason}>{reason}</li>
              ))}
            </ul>
          </div>
        ) : null}
      </CardContent>
    </Card>
  )
}
