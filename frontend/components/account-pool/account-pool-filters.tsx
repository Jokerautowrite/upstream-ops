import { ChevronDown, Search, SlidersHorizontal, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

export type AccountPoolScheduleFilter = "all" | "schedulable" | "disabled"
export type AccountPoolHealthFilter = "all" | "healthy" | "limited" | "warning" | "failed" | "unknown"
export type AccountPoolMissingFilter =
  | "all"
  | "any"
  | "none"
  | "multiplier"
  | "priority"
  | "balance"
  | "health"
export type AccountPoolSort =
  | "default"
  | "priority-asc"
  | "priority-desc"
  | "multiplier-asc"
  | "multiplier-desc"
  | "balance-asc"
  | "balance-desc"
  | "name-asc"
  | "name-desc"
  | "group-rate-asc"
  | "group-rate-desc"

export interface AccountPoolFilterState {
  query: string
  businessChannel: string
  schedule: AccountPoolScheduleFilter
  health: AccountPoolHealthFilter
  missing: AccountPoolMissingFilter
  sort: AccountPoolSort
}

interface AccountPoolFiltersProps {
  filters: AccountPoolFilterState
  businessChannels: string[]
  resultCount: number
  totalCount: number
  onChange: (filters: AccountPoolFilterState) => void
}

const scheduleOptions: Array<{ value: AccountPoolScheduleFilter; label: string }> = [
  { value: "all", label: "全部调度状态" },
  { value: "schedulable", label: "可调度" },
  { value: "disabled", label: "暂停调度" },
]

const healthOptions: Array<{ value: AccountPoolHealthFilter; label: string }> = [
  { value: "all", label: "全部健康状态" },
  { value: "healthy", label: "健康" },
  { value: "limited", label: "限流" },
  { value: "warning", label: "警告" },
  { value: "failed", label: "异常" },
  { value: "unknown", label: "未知" },
]

const missingOptions: Array<{ value: AccountPoolMissingFilter; label: string }> = [
  { value: "all", label: "全部数据完整性" },
  { value: "any", label: "有缺失" },
  { value: "none", label: "无缺失" },
  { value: "multiplier", label: "缺倍率" },
  { value: "priority", label: "缺优先级" },
  { value: "balance", label: "缺余额" },
  { value: "health", label: "缺健康状态" },
]

const sortOptions: Array<{ value: AccountPoolSort; label: string }> = [
  { value: "default", label: "默认业务顺序" },
  { value: "priority-asc", label: "优先级从小到大" },
  { value: "priority-desc", label: "优先级从大到小" },
  { value: "multiplier-asc", label: "倍率从低到高" },
  { value: "multiplier-desc", label: "倍率从高到低" },
  { value: "balance-asc", label: "余额从低到高" },
  { value: "balance-desc", label: "余额从高到低" },
  { value: "name-asc", label: "名称 A-Z" },
  { value: "name-desc", label: "名称 Z-A" },
  { value: "group-rate-asc", label: "Sub2 最低组倍率从低到高" },
  { value: "group-rate-desc", label: "Sub2 最低组倍率从高到低" },
]

export function AccountPoolFilters({
  filters,
  businessChannels,
  resultCount,
  totalCount,
  onChange,
}: AccountPoolFiltersProps) {
  const hasActiveFilters =
    filters.query ||
    filters.businessChannel !== "all" ||
    filters.schedule !== "all" ||
    filters.health !== "all" ||
    filters.missing !== "all" ||
    filters.sort !== "default"
  const activeSecondaryFilters =
    Number(filters.health !== "all") + Number(filters.missing !== "all")

  function patch(next: Partial<AccountPoolFilterState>) {
    onChange({ ...filters, ...next })
  }

  return (
    <section className="rounded-lg border border-border bg-card p-2.5 shadow-[var(--shadow-card)]">
      <div className="flex min-w-0 flex-col gap-2 lg:flex-row lg:items-center">
        <div className="relative min-w-0 flex-1 lg:min-w-64">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={filters.query}
            onChange={(event) => patch({ query: event.target.value })}
            placeholder="搜索账号、ID、分组、倍率、余额、优先级"
            className="h-8 pl-8 text-xs"
          />
        </div>

        <Select
          value={filters.businessChannel}
          onValueChange={(value) => patch({ businessChannel: value })}
        >
          <SelectTrigger className="h-8 w-full text-xs lg:w-40">
            <SelectValue placeholder="业务渠道" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部业务渠道</SelectItem>
            {businessChannels.map((channel) => (
              <SelectItem key={channel} value={channel}>
                {channel}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={filters.schedule}
          onValueChange={(value) => patch({ schedule: value as AccountPoolScheduleFilter })}
        >
          <SelectTrigger className="h-8 w-full text-xs lg:w-36">
            <SelectValue placeholder="调度状态" />
          </SelectTrigger>
          <SelectContent>
            {scheduleOptions.map((item) => (
              <SelectItem key={item.value} value={item.value}>
                {item.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={filters.sort}
          onValueChange={(value) => patch({ sort: value as AccountPoolSort })}
        >
          <SelectTrigger className="h-8 w-full text-xs lg:w-44">
            <SelectValue placeholder="排序" />
          </SelectTrigger>
          <SelectContent>
            {sortOptions.map((item) => (
              <SelectItem key={item.value} value={item.value}>
                {item.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <details className="group relative">
          <summary className="flex h-8 w-full cursor-pointer list-none items-center justify-center gap-1.5 rounded-md border border-input bg-background px-2.5 text-xs font-medium text-foreground shadow-xs transition hover:bg-accent lg:w-auto">
            <SlidersHorizontal className="size-3.5 text-muted-foreground" />
            更多筛选
            {activeSecondaryFilters > 0 ? (
              <span className="flex size-4 items-center justify-center rounded-full bg-primary text-[9px] text-primary-foreground">
                {activeSecondaryFilters}
              </span>
            ) : null}
            <ChevronDown className="size-3.5 text-muted-foreground transition-transform group-open:rotate-180" />
          </summary>
          <div className="absolute right-0 top-10 z-20 w-[min(20rem,calc(100vw-2rem))] space-y-2 rounded-lg border border-border bg-popover p-3 shadow-[var(--shadow-elevated)]">
            <div>
              <label className="mb-1 block text-[11px] font-medium text-muted-foreground">健康状态</label>
              <Select
                value={filters.health}
                onValueChange={(value) => patch({ health: value as AccountPoolHealthFilter })}
              >
                <SelectTrigger className="h-8 w-full text-xs">
                  <SelectValue placeholder="健康状态" />
                </SelectTrigger>
                <SelectContent>
                  {healthOptions.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div>
              <label className="mb-1 block text-[11px] font-medium text-muted-foreground">数据完整性</label>
              <Select
                value={filters.missing}
                onValueChange={(value) => patch({ missing: value as AccountPoolMissingFilter })}
              >
                <SelectTrigger className="h-8 w-full text-xs">
                  <SelectValue placeholder="数据缺失" />
                </SelectTrigger>
                <SelectContent>
                  {missingOptions.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
        </details>

        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-8 gap-1.5 px-2 text-xs"
          disabled={!hasActiveFilters}
          onClick={() =>
            onChange({
              query: "",
              businessChannel: "all",
              schedule: "all",
              health: "all",
              missing: "all",
              sort: "default",
            })
          }
        >
          <X className="size-3.5" />
          清空
        </Button>

        <div className="shrink-0 border-t border-border pt-2 text-right text-[11px] text-muted-foreground lg:ml-auto lg:border-t-0 lg:pt-0">
          {resultCount.toLocaleString("zh-CN")} / {totalCount.toLocaleString("zh-CN")} 个账号
        </div>
      </div>
    </section>
  )
}
