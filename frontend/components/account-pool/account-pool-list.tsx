import { AlertTriangle, CheckCircle2, PauseCircle, ShieldAlert, ShieldCheck, TimerReset } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { Sub2PoolAccount } from "@/lib/api-types"
import { decimal, formatRatio } from "@/lib/format"
import { cn } from "@/lib/utils"
import {
  accountBalanceLabel,
  accountBalanceTone,
  accountBusinessChannel,
  accountGroupLabel,
  accountHealthLabel,
  accountHealthTone,
  accountMissingLabels,
  accountSchedulableLabel,
  formatNumeric,
  isSchedulable,
} from "./account-pool-status"

interface AccountPoolListProps {
  accounts: Sub2PoolAccount[]
  busyAccountID: number | null
  onToggleSchedulable: (account: Sub2PoolAccount, next: boolean) => void
}

function toneClass(tone: string) {
  switch (tone) {
    case "healthy":
      return "border-success/20 bg-success/10 text-success"
    case "limited":
    case "warning":
      return "border-warning/20 bg-warning/10 text-warning"
    case "failed":
    case "debt":
      return "border-danger/20 bg-danger/10 text-danger"
    default:
      return "border-border bg-muted/40 text-muted-foreground"
  }
}

function smallBadgeTone(tone: string) {
  return cn("rounded-md border px-2 py-0.5 text-[11px] font-medium", toneClass(tone))
}

function AccountMissingChips({ account }: { account: Sub2PoolAccount }) {
  const missing = accountMissingLabels(account)
  if (missing.length === 0) return null
  return (
    <div className="flex flex-wrap gap-1">
      {missing.map((item) => (
        <Badge key={item} variant="outline" className="rounded-md text-[10px]">
          {item}
        </Badge>
      ))}
    </div>
  )
}

function AccountHealthIcons({ account }: { account: Sub2PoolAccount }) {
  const healthTone = accountHealthTone(account)
  const balanceTone = accountBalanceTone(account)
  const schedulable = isSchedulable(account)

  return (
    <div className="flex flex-wrap items-center gap-1">
      <Badge variant="outline" className={smallBadgeTone(healthTone)}>
        {healthTone === "healthy" ? <ShieldCheck className="size-3" /> : <ShieldAlert className="size-3" />}
        {accountHealthLabel(account)}
      </Badge>
      <Badge variant="outline" className={smallBadgeTone(balanceTone)}>
        {balanceTone === "debt" ? <AlertTriangle className="size-3" /> : <CheckCircle2 className="size-3" />}
        {accountBalanceLabel(account)}
      </Badge>
      <Badge variant="outline" className={smallBadgeTone(schedulable ? "healthy" : "warning")}>
        {schedulable ? <TimerReset className="size-3" /> : <PauseCircle className="size-3" />}
        {accountSchedulableLabel(account)}
      </Badge>
    </div>
  )
}

function AccountCore({
  account,
  busy,
  onToggleSchedulable,
}: {
  account: Sub2PoolAccount
  busy: boolean
  onToggleSchedulable: (account: Sub2PoolAccount, next: boolean) => void
}) {
  return (
    <div className="space-y-3">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <div className="truncate text-sm font-semibold text-foreground">{account.name}</div>
            <Badge variant="outline" className="rounded-md text-[10px]">
              #{account.id}
            </Badge>
          </div>
          <div className="mt-1 flex flex-wrap gap-1.5 text-[11px] text-muted-foreground">
            <span className="min-w-0 max-w-full break-all rounded-md border border-border bg-muted/20 px-1.5 py-0.5">
              {account.platform || "—"}
            </span>
            <span className="min-w-0 max-w-full break-all rounded-md border border-border bg-muted/20 px-1.5 py-0.5">
              {account.type || "—"}
            </span>
            <span className="min-w-0 max-w-full break-all rounded-md border border-border bg-muted/20 px-1.5 py-0.5">
              {accountGroupLabel(account)}
            </span>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-[11px] text-muted-foreground">调度</span>
          <Switch
            checked={isSchedulable(account)}
            onCheckedChange={(next) => onToggleSchedulable(account, next)}
            disabled={busy}
            aria-label={`${isSchedulable(account) ? "暂停" : "恢复"} ${account.name} 调度`}
          />
        </div>
      </div>

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <div className="rounded-md border border-border bg-muted/20 px-2 py-1.5">
          <div className="text-[10px] text-muted-foreground">当前优先级</div>
          <div className="mt-0.5 font-semibold tabular-nums">
            {formatNumeric(account.current_priority)}
          </div>
        </div>
        <div className="rounded-md border border-border bg-muted/20 px-2 py-1.5">
          <div className="text-[10px] text-muted-foreground">建议优先级</div>
          <div className="mt-0.5 font-semibold tabular-nums">
            {formatNumeric(account.suggested_priority)}
          </div>
        </div>
        <div className="rounded-md border border-border bg-muted/20 px-2 py-1.5">
          <div className="text-[10px] text-muted-foreground">上游倍率</div>
          <div className="mt-0.5 font-semibold tabular-nums">
            {account.upstream_multiplier == null ? "—" : formatRatio(account.upstream_multiplier)}
          </div>
        </div>
        <div className="rounded-md border border-border bg-muted/20 px-2 py-1.5">
          <div className="text-[10px] text-muted-foreground">余额</div>
          <div className="mt-0.5 font-semibold tabular-nums">
            {account.balance == null ? "—" : decimal(account.balance, 4)}
          </div>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="outline" className="min-w-0 max-w-full whitespace-normal break-all rounded-md text-[11px]">
          最低分组/渠道：{accountGroupLabel(account)}
        </Badge>
        <Badge variant="outline" className="min-w-0 max-w-full whitespace-normal break-all rounded-md text-[11px]">
          业务渠道：{accountBusinessChannel(account)}
        </Badge>
        <Badge variant="outline" className="min-w-0 max-w-full whitespace-normal rounded-md text-[11px]">
          今日请求：{formatNumeric(account.today_requests)}
        </Badge>
        <Badge variant="outline" className="min-w-0 max-w-full whitespace-normal rounded-md text-[11px]">
          并发：{account.current_concurrency == null ? "—" : `${account.current_concurrency}${account.max_concurrency != null ? ` / ${account.max_concurrency}` : ""}`}
        </Badge>
      </div>

      <AccountHealthIcons account={account} />
      <AccountMissingChips account={account} />
      {account.schedulable_reason ? (
        <div className="rounded-md border border-dashed border-border bg-muted/20 px-2 py-1.5 text-[11px] text-muted-foreground">
          调度说明：{account.schedulable_reason}
        </div>
      ) : null}
    </div>
  )
}

export function AccountPoolDesktopTable({
  accounts,
  busyAccountID,
  onToggleSchedulable,
}: AccountPoolListProps) {
  return (
    <div className="hidden overflow-x-auto rounded-lg border border-border bg-card shadow-sm lg:block">
      <Table className="min-w-[1100px]">
        <TableHeader>
          <TableRow>
            <TableHead className="w-48">账号</TableHead>
            <TableHead className="w-28">平台 / 类型</TableHead>
            <TableHead className="w-40">最低分组 / 渠道</TableHead>
            <TableHead className="w-24">当前优先级</TableHead>
            <TableHead className="w-24">建议优先级</TableHead>
            <TableHead className="w-24">上游倍率</TableHead>
            <TableHead className="w-24">余额</TableHead>
            <TableHead className="w-28">健康 / 限流</TableHead>
            <TableHead className="w-28">今日请求 / 并发</TableHead>
            <TableHead className="w-20 text-right">调度</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {accounts.map((account) => {
            const balanceTone = accountBalanceTone(account)
            const healthTone = accountHealthTone(account)
            return (
              <TableRow key={account.id}>
                <TableCell className="max-w-0">
                  <div className="min-w-0">
                    <div className="truncate font-medium">{account.name}</div>
                    <div className="text-[11px] text-muted-foreground">#{account.id}</div>
                  </div>
                </TableCell>
                <TableCell className="text-[11px] text-muted-foreground">
                  <div className="flex flex-col">
                    <span>{account.platform || "—"}</span>
                    <span>{account.type || "—"}</span>
                  </div>
                </TableCell>
                <TableCell className="max-w-0 text-[11px] text-muted-foreground">
                  <div className="truncate">{accountGroupLabel(account)}</div>
                  <div className="truncate text-muted-foreground/80">{accountBusinessChannel(account)}</div>
                </TableCell>
                <TableCell className="font-mono text-[12px] tabular-nums">
                  {formatNumeric(account.current_priority)}
                </TableCell>
                <TableCell
                  className={cn(
                    "font-mono text-[12px] tabular-nums",
                    account.suggested_priority != null &&
                      account.current_priority !== account.suggested_priority &&
                      "font-semibold text-warning",
                  )}
                >
                  {formatNumeric(account.suggested_priority)}
                </TableCell>
                <TableCell className="font-mono text-[12px] tabular-nums">
                  {account.upstream_multiplier == null ? "—" : formatRatio(account.upstream_multiplier)}
                </TableCell>
                <TableCell className={cn("font-mono text-[12px] tabular-nums", balanceTone === "debt" && "text-danger", balanceTone === "low" && "text-warning")}>
                  {account.balance == null ? "—" : decimal(account.balance, 4)}
                </TableCell>
                <TableCell className="text-[11px]">
                  <div className="flex flex-col gap-1">
                    <span className={cn("inline-flex w-fit items-center rounded-md border px-2 py-0.5", toneClass(healthTone))}>
                      {accountHealthLabel(account)}
                    </span>
                    <span className={cn("inline-flex w-fit items-center rounded-md border px-2 py-0.5", toneClass(balanceTone))}>
                      {accountBalanceLabel(account)}
                    </span>
                    {account.schedulable_reason ? (
                      <span
                        className="max-w-28 truncate text-[10px] text-muted-foreground"
                        title={account.schedulable_reason}
                      >
                        {account.schedulable_reason}
                      </span>
                    ) : null}
                  </div>
                </TableCell>
                <TableCell className="text-[11px] text-muted-foreground">
                  <div>{formatNumeric(account.today_requests)}</div>
                  <div>
                    {account.current_concurrency == null ? "—" : `${account.current_concurrency}${account.max_concurrency != null ? ` / ${account.max_concurrency}` : ""}`}
                  </div>
                </TableCell>
                <TableCell className="text-right">
                  <Switch
                    checked={isSchedulable(account)}
                    onCheckedChange={(next) => onToggleSchedulable(account, next)}
                    disabled={busyAccountID === account.id}
                    aria-label={`${isSchedulable(account) ? "暂停" : "恢复"} ${account.name} 调度`}
                  />
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}

export function AccountPoolMobileCards({
  accounts,
  busyAccountID,
  onToggleSchedulable,
}: AccountPoolListProps) {
  return (
    <div className="space-y-2 lg:hidden">
      {accounts.map((account) => (
        <Card key={account.id} className="rounded-lg border-border bg-card shadow-sm">
          <CardContent className="p-3">
            <AccountCore
              account={account}
              busy={busyAccountID === account.id}
              onToggleSchedulable={onToggleSchedulable}
            />
          </CardContent>
        </Card>
      ))}
    </div>
  )
}
