import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  CheckCircle2,
  ExternalLink,
  PauseCircle,
  PlayCircle,
  ShieldAlert,
  ShieldCheck,
  TimerReset,
} from "lucide-react"
import type { AccountPoolSort } from "./account-pool-filters"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
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
  accountMatchLabel,
  accountMissingLabels,
  accountMultiplierSourceLabel,
  accountSchedulableLabel,
  formatNumeric,
  isSchedulable,
} from "./account-pool-status"

interface AccountPoolListProps {
  accounts: Sub2PoolAccount[]
  busyAccountID: number | null
  onToggleSchedulable: (account: Sub2PoolAccount, next: boolean) => void
}

interface AccountPoolDesktopTableProps extends AccountPoolListProps {
  sort: AccountPoolSort
  onSortChange: (sort: AccountPoolSort) => void
}

type SortField = "name" | "balance" | "multiplier" | "group-rate" | "priority"

const sortValues: Record<SortField, { asc: AccountPoolSort; desc: AccountPoolSort }> = {
  name: { asc: "name-asc", desc: "name-desc" },
  balance: { asc: "balance-asc", desc: "balance-desc" },
  multiplier: { asc: "multiplier-asc", desc: "multiplier-desc" },
  "group-rate": { asc: "group-rate-asc", desc: "group-rate-desc" },
  priority: { asc: "priority-asc", desc: "priority-desc" },
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

function priorityLabel(account: Sub2PoolAccount) {
  const current = formatNumeric(account.current_priority)
  if (
    account.suggested_priority == null ||
    account.current_priority === account.suggested_priority
  ) {
    return `${current}（不变）`
  }
  return `${current}（→ ${formatNumeric(account.suggested_priority)}）`
}

function AccountActions({
  account,
  busy,
  onToggleSchedulable,
}: {
  account: Sub2PoolAccount
  busy: boolean
  onToggleSchedulable: (account: Sub2PoolAccount, next: boolean) => void
}) {
  const schedulable = isSchedulable(account)

  return (
    <div className="flex items-center justify-end gap-1">
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="outline"
            size="icon-sm"
            asChild={Boolean(account.upstream_url)}
            disabled={!account.upstream_url}
            aria-label={`打开 ${account.name} 上游网页`}
          >
            {account.upstream_url ? (
              <a href={account.upstream_url} target="_blank" rel="noreferrer">
                <ExternalLink className="size-3.5" />
              </a>
            ) : (
              <ExternalLink className="size-3.5" />
            )}
          </Button>
        </TooltipTrigger>
        <TooltipContent>打开上游网页</TooltipContent>
      </Tooltip>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="outline"
            size="icon-sm"
            disabled={busy}
            onClick={() => onToggleSchedulable(account, !schedulable)}
            aria-label={`${schedulable ? "暂停" : "恢复"} ${account.name} 调度`}
          >
            {schedulable ? (
              <PauseCircle className="size-3.5" />
            ) : (
              <PlayCircle className="size-3.5" />
            )}
          </Button>
        </TooltipTrigger>
        <TooltipContent>{schedulable ? "暂停调度" : "恢复调度"}</TooltipContent>
      </Tooltip>
    </div>
  )
}

function SortableHead({
  field,
  label,
  sort,
  onSortChange,
  className,
}: {
  field: SortField
  label: React.ReactNode
  sort: AccountPoolSort
  onSortChange: (sort: AccountPoolSort) => void
  className?: string
}) {
  const values = sortValues[field]
  const direction = sort === values.asc ? "asc" : sort === values.desc ? "desc" : null
  const nextSort = direction === "asc" ? values.desc : values.asc

  return (
    <TableHead className={className} aria-sort={direction === "asc" ? "ascending" : direction === "desc" ? "descending" : "none"}>
      <button
        type="button"
        className="inline-flex min-h-8 items-center gap-1 text-left font-medium hover:text-foreground"
        onClick={() => onSortChange(nextSort)}
      >
        <span>{label}</span>
        {direction === "asc" ? (
          <ArrowUp className="size-3.5" />
        ) : direction === "desc" ? (
          <ArrowDown className="size-3.5" />
        ) : (
          <ArrowUpDown className="size-3.5 text-muted-foreground" />
        )}
      </button>
    </TableHead>
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
        <AccountActions
          account={account}
          busy={busy}
          onToggleSchedulable={onToggleSchedulable}
        />
      </div>

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <div className="rounded-md border border-border bg-muted/20 px-2 py-1.5">
          <div className="text-[10px] text-muted-foreground">优先级</div>
          <div className="mt-0.5 font-semibold tabular-nums">
            {priorityLabel(account)}
          </div>
        </div>
        <div className="rounded-md border border-border bg-muted/20 px-2 py-1.5">
          <div className="text-[10px] text-muted-foreground">上游倍率</div>
          <div className="mt-0.5 font-semibold tabular-nums">
            {account.upstream_multiplier == null ? "—" : formatRatio(account.upstream_multiplier)}
          </div>
          <div className="mt-0.5 text-[10px] text-muted-foreground">
            {accountMultiplierSourceLabel(account)}
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
          Sub2 分组倍率：{account.min_group_multiplier == null ? "—" : formatRatio(account.min_group_multiplier)}
        </Badge>
        <Badge variant="outline" className="min-w-0 max-w-full whitespace-normal rounded-md text-[11px]">
          今日请求：{formatNumeric(account.today_requests)}
        </Badge>
        <Badge variant="outline" className="min-w-0 max-w-full whitespace-normal rounded-md text-[11px]">
          并发：{account.current_concurrency == null ? "—" : `${account.current_concurrency}${account.max_concurrency != null ? ` / ${account.max_concurrency}` : ""}`}
        </Badge>
      </div>

      <div className="flex flex-wrap gap-1">
        {(account.groups ?? []).map((group) => (
          <Badge key={group.id} variant="secondary" className="rounded-md text-[10px]">
            {group.name} {formatRatio(group.multiplier)}
          </Badge>
        ))}
        {account.upstream_url ? (
          <a
            href={account.upstream_url}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-0.5 text-[10px] text-muted-foreground hover:text-foreground"
          >
            <ExternalLink className="size-3" />
            打开上游
          </a>
        ) : null}
      </div>

      <AccountHealthIcons account={account} />
      <AccountMissingChips account={account} />
      <div className="text-[11px] text-muted-foreground">倍率诊断：{accountMatchLabel(account)}</div>
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
  sort,
  onSortChange,
  onToggleSchedulable,
}: AccountPoolDesktopTableProps) {
  return (
    <div className="hidden overflow-x-auto rounded-lg border border-border bg-card shadow-sm lg:block">
      <Table className="min-w-[1480px]">
        <TableHeader>
          <TableRow>
            <SortableHead field="name" label="账号" sort={sort} onSortChange={onSortChange} className="w-52" />
            <SortableHead field="balance" label="余额" sort={sort} onSortChange={onSortChange} className="w-24" />
            <SortableHead field="multiplier" label="上游倍率" sort={sort} onSortChange={onSortChange} className="w-32" />
            <SortableHead
              field="group-rate"
              label={<><span className="block">Sub2</span><span className="block">最低组</span></>}
              sort={sort}
              onSortChange={onSortChange}
              className="w-24 whitespace-normal leading-tight"
            />
            <TableHead className="w-56">完整分组</TableHead>
            <TableHead className="w-44">上游地址</TableHead>
            <TableHead className="w-36">异常 / 限流</TableHead>
            <TableHead className="w-28">请求 / 并发</TableHead>
            <SortableHead field="priority" label="优先级" sort={sort} onSortChange={onSortChange} className="w-36" />
            <TableHead className="w-24 text-right">操作</TableHead>
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
                    <div className="text-[11px] text-muted-foreground">
                      #{account.id} · {account.platform || "—"} / {account.type || "—"}
                    </div>
                  </div>
                </TableCell>
                <TableCell className={cn("font-mono text-[12px] tabular-nums", balanceTone === "debt" && "text-danger", balanceTone === "low" && "text-warning")}>
                  {account.balance == null ? "—" : decimal(account.balance, 4)}
                </TableCell>
                <TableCell className="text-[11px]">
                  <div className="font-mono text-[12px] tabular-nums">
                    {account.upstream_multiplier == null ? "—" : formatRatio(account.upstream_multiplier)}
                  </div>
                  <div className="text-muted-foreground">{accountMultiplierSourceLabel(account)}</div>
                </TableCell>
                <TableCell className="w-24 max-w-24 whitespace-normal break-words text-[11px]">
                  <div className="whitespace-normal break-words">{account.min_group || "—"}</div>
                  <div className="font-mono text-muted-foreground">
                    {account.min_group_multiplier == null ? "—" : formatRatio(account.min_group_multiplier)}
                  </div>
                </TableCell>
                <TableCell>
                  <div className="flex max-w-56 flex-wrap gap-1">
                    {(account.groups ?? []).map((group) => (
                      <Badge key={group.id} variant="secondary" className="rounded-md text-[10px]">
                        {group.name} {formatRatio(group.multiplier)}
                      </Badge>
                    ))}
                  </div>
                </TableCell>
                <TableCell className="max-w-0 text-[11px]">
                  {account.upstream_url ? (
                    <a
                      href={account.upstream_url}
                      target="_blank"
                      rel="noreferrer"
                      className="flex items-center gap-1 text-muted-foreground hover:text-foreground"
                      title={account.upstream_url}
                    >
                      <ExternalLink className="size-3 shrink-0" />
                      <span className="truncate">{account.upstream_url.replace(/^https?:\/\//, "")}</span>
                    </a>
                  ) : "—"}
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
                    <span className="max-w-32 truncate text-[10px] text-muted-foreground" title={accountMatchLabel(account)}>
                      {accountMatchLabel(account)}
                    </span>
                  </div>
                </TableCell>
                <TableCell className="text-[11px] text-muted-foreground">
                  <div>{formatNumeric(account.today_requests)}</div>
                  <div>
                    {account.current_concurrency == null ? "—" : `${account.current_concurrency}${account.max_concurrency != null ? ` / ${account.max_concurrency}` : ""}`}
                  </div>
                </TableCell>
                <TableCell className="font-mono text-[11px] tabular-nums">
                  <div>{priorityLabel(account)}</div>
                  <div className="font-sans text-[10px] text-muted-foreground">{accountBusinessChannel(account)}</div>
                </TableCell>
                <TableCell className="text-right">
                  <AccountActions
                    account={account}
                    busy={busyAccountID === account.id}
                    onToggleSchedulable={onToggleSchedulable}
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
