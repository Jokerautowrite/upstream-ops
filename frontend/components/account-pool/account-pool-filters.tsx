import { Search, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"

export type AccountPoolViewMode = "problems" | "all"
export type AccountPoolQuickFocus =
  | "none"
  | "debt"
  | "missing_multiplier"
  | "unhealthy"
  | "priority_mismatch"
  | "schedulable"
  | "disabled"
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
  | "name_asc"
  | "name_desc"
  | "current_priority_asc"
  | "current_priority_desc"
  | "suggested_priority_asc"
  | "suggested_priority_desc"
  | "upstream_multiplier_asc"
  | "upstream_multiplier_desc"
  | "balance_asc"
  | "balance_desc"

export interface AccountPoolFilterState {
  query: string
  businessChannel: string
  schedule: AccountPoolScheduleFilter
  health: AccountPoolHealthFilter
  missing: AccountPoolMissingFilter
  sort: AccountPoolSort
  viewMode: AccountPoolViewMode
  quickFocus: AccountPoolQuickFocus
}

export const defaultAccountPoolFilters: AccountPoolFilterState = {
  query: "",
  businessChannel: "all",
  schedule: "all",
  health: "all",
  missing: "all",
  sort: "upstream_multiplier_asc",
  viewMode: "problems",
  quickFocus: "none",
}

interface AccountPoolFiltersProps {
  filters: AccountPoolFilterState
  businessChannels: string[]
  resultCount: number
  totalCount: number
  problemCount: number
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
  { value: "upstream_multiplier_asc", label: "上游倍率：低到高" },
  { value: "upstream_multiplier_desc", label: "上游倍率：高到低" },
  { value: "balance_asc", label: "余额：低到高" },
  { value: "balance_desc", label: "余额：高到低" },
  { value: "suggested_priority_asc", label: "建议优先级：低到高" },
  { value: "suggested_priority_desc", label: "建议优先级：高到低" },
  { value: "current_priority_asc", label: "当前优先级：低到高" },
  { value: "current_priority_desc", label: "当前优先级：高到低" },
  { value: "name_asc", label: "账号名称：A-Z" },
  { value: "name_desc", label: "账号名称：Z-A" },
]

export function AccountPoolFilters({
  filters,
  businessChannels,
  resultCount,
  totalCount,
  problemCount,
  onChange,
}: AccountPoolFiltersProps) {
  const hasActiveFilters =
    filters.query ||
    filters.businessChannel !== "all" ||
    filters.schedule !== "all" ||
    filters.health !== "all" ||
    filters.missing !== "all" ||
    filters.sort !== "upstream_multiplier_asc" ||
    filters.viewMode !== "problems" ||
    filters.quickFocus !== "none"

  function patch(next: Partial<AccountPoolFilterState>) {
    onChange({ ...filters, ...next })
  }

  return (
    <section className="rounded-lg border border-border bg-card p-3 shadow-sm">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <div className="inline-flex rounded-lg border border-border bg-muted/40 p-0.5">
          <button
            type="button"
            onClick={() =>
              patch({
                viewMode: "problems",
                quickFocus: "none",
                schedule: "all",
                health: "all",
                missing: "all",
              })
            }
            className={cn(
              "rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
              filters.viewMode === "problems"
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            问题
          </button>
          <button
            type="button"
            onClick={() =>
              patch({
                viewMode: "all",
                quickFocus: "none",
                schedule: "all",
                health: "all",
                missing: "all",
              })
            }
            className={cn(
              "rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
              filters.viewMode === "all"
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            全部
          </button>
        </div>
        <span className="text-xs text-muted-foreground">
          问题 {problemCount.toLocaleString("zh-CN")} / 共 {totalCount.toLocaleString("zh-CN")}
          {" · 当前显示 "}
          {resultCount.toLocaleString("zh-CN")}
        </span>
      </div>

      <div className="grid grid-cols-1 gap-2 md:grid-cols-[minmax(0,1.4fr)_repeat(5,minmax(0,1fr))_auto]">
        <div className="relative min-w-0">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={filters.query}
            onChange={(event) => patch({ query: event.target.value })}
            placeholder="搜索 ID、名称、平台、分组"
            className="h-9 pl-8"
          />
        </div>

        <Select
          value={filters.businessChannel}
          onValueChange={(value) => patch({ businessChannel: value })}
        >
          <SelectTrigger className="w-full">
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
          onValueChange={(value) =>
            patch({
              schedule: value as AccountPoolScheduleFilter,
              quickFocus: "none",
            })
          }
        >
          <SelectTrigger className="w-full">
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
          value={filters.health}
          onValueChange={(value) =>
            patch({
              health: value as AccountPoolHealthFilter,
              quickFocus: "none",
            })
          }
        >
          <SelectTrigger className="w-full">
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

        <Select
          value={filters.missing}
          onValueChange={(value) =>
            patch({
              missing: value as AccountPoolMissingFilter,
              quickFocus: "none",
            })
          }
        >
          <SelectTrigger className="w-full">
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

        <Select
          value={filters.sort}
          onValueChange={(value) => patch({ sort: value as AccountPoolSort })}
        >
          <SelectTrigger className="w-full">
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

        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-9 gap-1.5"
          disabled={!hasActiveFilters}
          onClick={() => onChange({ ...defaultAccountPoolFilters })}
        >
          <X className="size-3.5" />
          清空
        </Button>
      </div>
    </section>
  )
}
