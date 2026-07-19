"use client"

import { useEffect, useMemo, useState } from "react"
import { AlertTriangle, Database, RefreshCw } from "lucide-react"
import { toast } from "sonner"
import { AccountPoolApplyDialog } from "@/components/account-pool/account-pool-apply-dialog"
import { AccountPoolAutomationCard } from "@/components/account-pool/account-pool-automation-card"
import {
  AccountPoolFilters,
  defaultAccountPoolFilters,
  type AccountPoolFilterState,
  type AccountPoolSort,
} from "@/components/account-pool/account-pool-filters"
import {
  AccountPoolDesktopTable,
  AccountPoolMobileCards,
} from "@/components/account-pool/account-pool-list"
import { AccountPoolPreviewPanel } from "@/components/account-pool/account-pool-preview-panel"
import { AccountPoolSkeleton } from "@/components/account-pool/account-pool-skeleton"
import {
  AccountPoolSummaryStrip,
  type AccountPoolSummaryTileKey,
} from "@/components/account-pool/account-pool-summary-strip"
import {
  accountBalanceTone,
  accountBusinessChannel,
  accountHealthTone,
  accountMissingLabels,
  hasMissingAccountField,
  hasPriorityMismatch,
  isMissingMultiplier,
  isProblemAccount,
  isSchedulable,
} from "@/components/account-pool/account-pool-status"
import { Button } from "@/components/ui/button"
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
import { useConfirm } from "@/components/ui/confirm-dialog"
import { apiFetch } from "@/lib/api"
import {
  useSub2PoolAutomation,
  useSub2PoolSnapshot,
  useSub2PoolTargets,
} from "@/lib/queries"
import type {
  Sub2PoolAccount,
  Sub2PoolAutomationUpdateResult,
  Sub2PoolPriorityApplyResult,
  Sub2PoolPriorityPreviewResponse,
  Sub2PoolSchedulableResult,
  Sub2PoolSnapshotSummary,
} from "@/lib/api-types"

function compareAccounts(a: Sub2PoolAccount, b: Sub2PoolAccount, sort: AccountPoolSort) {
  const separator = sort.lastIndexOf("_")
  const key = sort.slice(0, separator) as
    | "name"
    | "current_priority"
    | "suggested_priority"
    | "upstream_multiplier"
    | "balance"
  const direction = sort.slice(separator + 1) === "desc" ? -1 : 1

  if (key === "name") {
    const compared = (a.name || "").localeCompare(b.name || "", "zh-CN")
    return compared === 0 ? a.id - b.id : compared * direction
  }
  const left = a[key]
  const right = b[key]
  const leftMissing = left == null || !Number.isFinite(left)
  const rightMissing = right == null || !Number.isFinite(right)
  if (leftMissing && rightMissing) return a.id - b.id
  if (leftMissing) return 1
  if (rightMissing) return -1
  if (left === right) return a.id - b.id
  return (left < right ? -1 : 1) * direction
}

function deriveSummary(accounts: Sub2PoolAccount[], fallback?: Sub2PoolSnapshotSummary | null) {
  const derived = {
    total_accounts: accounts.length,
    schedulable_accounts: accounts.filter(isSchedulable).length,
    healthy_accounts: accounts.filter((account) => accountHealthTone(account) === "healthy").length,
    debt_accounts: accounts.filter((account) => accountBalanceTone(account) === "debt").length,
    missing_multiplier_accounts: accounts.filter(
      (account) => account.upstream_multiplier == null || hasMissingAccountField(account, "倍率"),
    ).length,
  }

  if (fallback) {
    return {
      total_accounts: fallback.total_accounts ?? derived.total_accounts,
      schedulable_accounts: fallback.schedulable_accounts ?? derived.schedulable_accounts,
      healthy_accounts: fallback.healthy_accounts ?? derived.healthy_accounts,
      debt_accounts: fallback.debt_accounts ?? derived.debt_accounts,
      missing_multiplier_accounts:
        fallback.missing_multiplier_accounts ?? derived.missing_multiplier_accounts,
    }
  }

  return derived
}

function matchesMissingFilter(account: Sub2PoolAccount, filter: AccountPoolFilterState["missing"]) {
  if (filter === "all") return true
  const missing = accountMissingLabels(account)
  if (filter === "any") return missing.length > 0
  if (filter === "none") return missing.length === 0
  if (filter === "multiplier") return isMissingMultiplier(account)
  if (filter === "priority") return account.current_priority == null || hasMissingAccountField(account, "优先级")
  if (filter === "balance") return account.balance == null || hasMissingAccountField(account, "余额")
  if (filter === "health") return accountHealthTone(account) === "unknown" || hasMissingAccountField(account, "健康")
  return true
}

function matchesQuickFocus(account: Sub2PoolAccount, focus: AccountPoolFilterState["quickFocus"]) {
  if (focus === "none") return true
  if (focus === "debt") return accountBalanceTone(account) === "debt"
  if (focus === "missing_multiplier") return isMissingMultiplier(account)
  if (focus === "unhealthy") return accountHealthTone(account) !== "healthy"
  if (focus === "priority_mismatch") return hasPriorityMismatch(account)
  if (focus === "schedulable") return isSchedulable(account)
  if (focus === "disabled") return !isSchedulable(account)
  return true
}

/** 从当前筛选状态推导 KPI tile 激活态。 */
function deriveActiveSummaryTile(filters: AccountPoolFilterState): AccountPoolSummaryTileKey | null {
  const cleanSecondary =
    filters.query === "" &&
    filters.businessChannel === "all"

  if (!cleanSecondary) return null

  if (filters.quickFocus === "debt" && filters.viewMode === "problems") return "debt"
  if (filters.quickFocus === "missing_multiplier") return "missing_multiplier"
  if (
    filters.viewMode === "problems" &&
    filters.quickFocus === "none" &&
    filters.schedule === "all" &&
    filters.health === "all" &&
    filters.missing === "all"
  ) {
    return "problems"
  }
  if (
    filters.viewMode === "all" &&
    filters.quickFocus === "none" &&
    filters.schedule === "schedulable" &&
    filters.health === "all" &&
    filters.missing === "all"
  ) {
    return "schedulable"
  }
  if (
    filters.viewMode === "all" &&
    filters.quickFocus === "none" &&
    filters.schedule === "all" &&
    filters.health === "healthy" &&
    filters.missing === "all"
  ) {
    return "healthy"
  }
  if (
    filters.viewMode === "all" &&
    filters.quickFocus === "none" &&
    filters.schedule === "all" &&
    filters.health === "all" &&
    filters.missing === "all"
  ) {
    return "total"
  }
  return null
}

function filtersForSummaryTile(
  key: AccountPoolSummaryTileKey,
  current: AccountPoolFilterState,
): AccountPoolFilterState {
  const base: AccountPoolFilterState = {
    ...current,
    query: "",
    businessChannel: "all",
    schedule: "all",
    health: "all",
    missing: "all",
    quickFocus: "none",
  }
  switch (key) {
    case "total":
      return { ...base, viewMode: "all" }
    case "problems":
      return { ...base, viewMode: "problems" }
    case "schedulable":
      return { ...base, viewMode: "all", schedule: "schedulable" }
    case "healthy":
      return { ...base, viewMode: "all", health: "healthy" }
    case "debt":
      return { ...base, viewMode: "problems", quickFocus: "debt" }
    case "missing_multiplier":
      return {
        ...base,
        viewMode: "problems",
        quickFocus: "missing_multiplier",
        missing: "multiplier",
      }
    default:
      return base
  }
}

function updateAccountInSnapshot(
  accounts: Sub2PoolAccount[],
  accountID: number,
  nextAccount: Sub2PoolAccount | ((account: Sub2PoolAccount) => Sub2PoolAccount),
) {
  return accounts.map((account) => {
    if (account.id !== accountID) return account
    return typeof nextAccount === "function" ? nextAccount(account) : nextAccount
  })
}

function errorMessage(err: unknown, fallback: string) {
  return err instanceof Error ? err.message : fallback
}

export default function AccountPoolPage() {
  const targets = useSub2PoolTargets()
  const [targetID, setTargetID] = useState<string | null>(null)
  const snapshot = useSub2PoolSnapshot(targetID)
  const automation = useSub2PoolAutomation(targetID)
  const { confirm, dialog: confirmDialog } = useConfirm()

  const [filters, setFilters] = useState<AccountPoolFilterState>(defaultAccountPoolFilters)
  const [refreshing, setRefreshing] = useState(false)
  const [busyAccountID, setBusyAccountID] = useState<number | null>(null)
  const [preview, setPreview] = useState<Sub2PoolPriorityPreviewResponse | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  const [previewError, setPreviewError] = useState<string | null>(null)
  const [applyOpen, setApplyOpen] = useState(false)
  const [applying, setApplying] = useState(false)
  const [applyConflict, setApplyConflict] = useState<string | null>(null)
  const [automationUpdating, setAutomationUpdating] = useState(false)
  const [snapshotDirty, setSnapshotDirty] = useState(false)

  useEffect(() => {
    const list = targets.data ?? []
    if (list.length === 0) return
    if (!targetID || !list.some((target) => target.id === targetID)) {
      setTargetID(list[0].id)
    }
  }, [targetID, targets.data])

  useEffect(() => {
    setPreview(null)
    setPreviewError(null)
    setApplyConflict(null)
    setApplyOpen(false)
    setSnapshotDirty(false)
  }, [targetID])

  const snapshotMatchesTarget = snapshot.data?.target_id != null && String(snapshot.data.target_id) === targetID
  const automationMatchesTarget = automation.data?.target_id != null && String(automation.data.target_id) === targetID
  const currentSnapshot = snapshotMatchesTarget ? snapshot.data : null
  const currentAutomation = automationMatchesTarget ? automation.data : null
  const selectedTarget = (targets.data ?? []).find((target) => target.id === targetID) ?? null
  const accounts = currentSnapshot?.accounts ?? []
  const summary = useMemo(
    () => deriveSummary(accounts, currentSnapshot?.summary),
    [accounts, currentSnapshot?.summary],
  )

  const businessChannels = useMemo(() => {
    const seen = new Set<string>()
    for (const account of accounts) {
      const channel = accountBusinessChannel(account)
      if (channel !== "—") seen.add(channel)
    }
    return Array.from(seen).sort((a, b) => a.localeCompare(b, "zh-CN"))
  }, [accounts])

  const problemCount = useMemo(
    () => accounts.filter(isProblemAccount).length,
    [accounts],
  )

  const activeSummaryTile = useMemo(() => deriveActiveSummaryTile(filters), [filters])

  const filteredAccounts = useMemo(() => {
    const query = filters.query.trim().toLowerCase()
    const filtered = accounts.filter((account) => {
      // 1) 问题视图优先
      if (filters.viewMode === "problems" && !isProblemAccount(account)) return false
      // 2) KPI quickFocus
      if (!matchesQuickFocus(account, filters.quickFocus)) return false

      if (query) {
        const haystack = [
          String(account.id),
          account.name,
          account.platform,
          account.type,
          account.business_channel ?? "",
          account.min_group ?? "",
          account.upstream_multiplier ?? "",
          account.balance ?? "",
          account.current_priority ?? "",
          account.suggested_priority ?? "",
          account.health_status ?? "",
          account.schedulable_reason ?? "",
        ]
          .join(" ")
          .toLowerCase()
        if (!haystack.includes(query)) return false
      }

      if (filters.businessChannel !== "all" && accountBusinessChannel(account) !== filters.businessChannel) {
        return false
      }

      if (filters.schedule === "schedulable" && !isSchedulable(account)) return false
      if (filters.schedule === "disabled" && isSchedulable(account)) return false
      if (filters.health !== "all" && accountHealthTone(account) !== filters.health) return false
      return matchesMissingFilter(account, filters.missing)
    })
    return filtered.sort((a, b) => {
      // 问题视图下欠费优先，再走既有排序
      if (filters.viewMode === "problems") {
        const aDebt = accountBalanceTone(a) === "debt" ? 0 : 1
        const bDebt = accountBalanceTone(b) === "debt" ? 0 : 1
        if (aDebt !== bDebt) return aDebt - bDebt
      }
      return compareAccounts(a, b, filters.sort)
    })
  }, [accounts, filters])

  useEffect(() => {
    if (
      preview &&
      currentSnapshot?.snapshot_signature &&
      preview.snapshot_signature !== currentSnapshot.snapshot_signature
    ) {
      setPreview(null)
      setApplyOpen(false)
      setApplyConflict(null)
    }
  }, [currentSnapshot?.snapshot_signature, preview])

  async function handleRefresh() {
    if (!targetID) return
    setPreview(null)
    setApplyOpen(false)
    setApplyConflict(null)
    setRefreshing(true)
    try {
      const result = await apiFetch<NonNullable<typeof snapshot.data>>(
        `/sub2-pool/targets/${encodeURIComponent(targetID)}/snapshot`,
      )
      snapshot.setData(result)
      setSnapshotDirty(false)
      toast.success("账号池快照已刷新")
    } catch (err) {
      toast.error(errorMessage(err, "刷新账号池快照失败"))
    } finally {
      setRefreshing(false)
    }
  }

  async function handleToggleSchedulable(account: Sub2PoolAccount, next: boolean) {
    const ok = await confirm({
      title: next ? `恢复调度 ${account.name}？` : `暂停调度 ${account.name}？`,
      description: next
        ? "恢复后该账号会重新进入后端可调度集合。"
        : "暂停后该账号不会被调度，但不会删除任何账号或密钥。",
      confirmLabel: next ? "恢复调度" : "暂停调度",
      destructive: !next,
    })
    if (!ok || !targetID) return

    setBusyAccountID(account.id)
    try {
      const result = await apiFetch<Sub2PoolSchedulableResult>(
        `/sub2-pool/accounts/${account.id}/schedulable`,
        {
          method: "PATCH",
          body: JSON.stringify({ schedulable: next, target_id: targetID }),
        },
      )
      if (currentSnapshot) {
        snapshot.setData({
          ...currentSnapshot,
          accounts: updateAccountInSnapshot(
            currentSnapshot.accounts,
            account.id,
            (current) => ({
              ...current,
              ...(result.account ?? {}),
              schedulable: result.schedulable,
              updated_at: result.updated_at ?? result.account?.updated_at ?? current.updated_at,
            }),
          ),
        })
      }
      setPreview(null)
      setApplyOpen(false)
      setApplyConflict(null)
      setSnapshotDirty(true)
      toast.success(result.message ?? (result.schedulable ? "已恢复调度" : "已暂停调度"))
    } catch (err) {
      toast.error(errorMessage(err, "更新调度状态失败"))
    } finally {
      setBusyAccountID(null)
    }
  }

  async function handleGeneratePreview() {
    if (!targetID) return
    setPreviewLoading(true)
    setPreviewError(null)
    setApplyConflict(null)
    try {
      const result = await apiFetch<Sub2PoolPriorityPreviewResponse>(
        `/sub2-pool/targets/${encodeURIComponent(targetID)}/preview`,
        {
          method: "POST",
          body: JSON.stringify({
            snapshot_signature: currentSnapshot?.snapshot_signature ?? null,
          }),
        },
      )
      setPreview(result)
      toast.success("已生成优先级预览")
    } catch (err) {
      setPreviewError(errorMessage(err, "生成预览失败"))
      toast.error(errorMessage(err, "生成预览失败"))
    } finally {
      setPreviewLoading(false)
    }
  }

  async function handleApplyPreview() {
    if (!preview) return
    setApplying(true)
    setApplyConflict(null)
    try {
      const result = await apiFetch<Sub2PoolPriorityApplyResult>(
        `/sub2-pool/targets/${encodeURIComponent(preview.target_id || targetID || "")}/apply`,
        {
          method: "POST",
          body: JSON.stringify({ snapshot_signature: preview.snapshot_signature }),
        },
      )
      if (result.summary?.combined_result === "partial" || (result.failed?.length ?? 0) > 0) {
        const message = `部分写入成功，${result.failed?.length ?? 0} 个账号未完成。请刷新后重新生成预览。`
        setApplyConflict(message)
        setSnapshotDirty(true)
        toast.error(message)
        return
      }
      toast.success(result.message || "已应用优先级预览")
      setPreview(null)
      setApplyOpen(false)
      setSnapshotDirty(true)
    } catch (err) {
      const status = (err as { status?: number }).status
      const message =
        status === 409
          ? `${errorMessage(err, "快照冲突")}。请重新刷新并生成预览后再 Apply。`
          : errorMessage(err, "Apply 失败")
      setApplyConflict(message)
      toast.error(message)
    } finally {
      setApplying(false)
    }
  }

  async function handleToggleAutomation(enabled: boolean) {
    if (!targetID) return
    if (enabled) {
      const ok = await confirm({
        title: `开启 ${selectedTarget?.name ?? "当前目标"} 自动优先级？`,
        description: "开启后，倍率扫描完成时会按后端预览和安全守卫自动写入账号优先级；欠费账号排在渠道末尾。",
        confirmLabel: "开启自动化",
        destructive: true,
      })
      if (!ok) return
    }
    setAutomationUpdating(true)
    try {
      const result = await apiFetch<Sub2PoolAutomationUpdateResult>("/sub2-pool/automation", {
        method: "PATCH",
        body: JSON.stringify({ target_id: targetID, enabled }),
      })
      if (result.status) {
        automation.setData(result.status)
      } else if (currentAutomation) {
        automation.setData({ ...currentAutomation, enabled: result.enabled, updated_at: result.updated_at ?? currentAutomation.updated_at })
      } else {
        automation.setData({ target_id: targetID, enabled: result.enabled, updated_at: result.updated_at })
      }
      toast.success(result.message ?? (enabled ? "已开启自动化" : "已关闭自动化"))
    } catch (err) {
      toast.error(errorMessage(err, "更新自动化状态失败"))
    } finally {
      setAutomationUpdating(false)
    }
  }

  const targetChanging = Boolean(targetID) && !snapshotMatchesTarget && Boolean(snapshot.data)
  const initialLoading =
    (targets.loading && !targets.data) ||
    (Boolean(targetID) && (snapshot.loading || targetChanging) && !currentSnapshot)

  if (initialLoading) {
    return <AccountPoolSkeleton />
  }

  if (targets.error && !targets.data) {
    return (
      <Empty className="border border-border bg-card">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <AlertTriangle className="size-5" />
          </EmptyMedia>
          <EmptyTitle>账号池 target 加载失败</EmptyTitle>
          <EmptyDescription>{targets.error}</EmptyDescription>
        </EmptyHeader>
        <Button type="button" variant="outline" onClick={targets.refetch} className="gap-1.5">
          <RefreshCw className="size-3.5" />
          重试
        </Button>
      </Empty>
    )
  }

  if (!targets.loading && (targets.data?.length ?? 0) === 0) {
    return (
      <Empty className="border border-border bg-card">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <Database className="size-5" />
          </EmptyMedia>
          <EmptyTitle>暂无账号池 target</EmptyTitle>
          <EmptyDescription>后端需要返回 /api/sub2-pool/targets 后才能进入账号池页面。</EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  }

  if (snapshot.error && !snapshot.data) {
    return (
      <Empty className="border border-border bg-card">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <AlertTriangle className="size-5" />
          </EmptyMedia>
          <EmptyTitle>账号池快照加载失败</EmptyTitle>
          <EmptyDescription>{snapshot.error}</EmptyDescription>
        </EmptyHeader>
        <Button type="button" variant="outline" onClick={() => void handleRefresh()} className="gap-1.5">
          <RefreshCw className="size-3.5" />
          重试
        </Button>
      </Empty>
    )
  }

  return (
    <>
      <AccountPoolSummaryStrip
        targets={targets.data ?? []}
        selectedTargetID={targetID}
        summary={summary}
        problemCount={problemCount}
        activeTile={activeSummaryTile}
        refreshedAt={currentSnapshot?.refreshed_at}
        loading={refreshing || snapshot.loading || targetChanging}
        onTargetChange={setTargetID}
        onRefresh={() => void handleRefresh()}
        onTileClick={(key) => setFilters((current) => filtersForSummaryTile(key, current))}
      />

      {snapshot.error && currentSnapshot ? (
        <div className="rounded-lg border border-warning/25 bg-warning/10 px-3 py-2 text-xs text-warning">
          最新刷新失败，当前展示的是上一次成功快照：{snapshot.error}
        </div>
      ) : null}

      {snapshotDirty ? (
        <div className="rounded-lg border border-warning/25 bg-warning/10 px-3 py-2 text-xs text-warning">
          账号池状态已变更，当前仍展示上次缓存。点击“刷新”读取最新状态后再生成优先级预览。
        </div>
      ) : null}

      <AccountPoolFilters
        filters={filters}
        businessChannels={businessChannels}
        resultCount={filteredAccounts.length}
        totalCount={accounts.length}
        problemCount={problemCount}
        onChange={setFilters}
      />

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <AccountPoolAutomationCard
          status={currentAutomation}
          loading={automation.loading || (Boolean(targetID) && !automationMatchesTarget)}
          error={automation.error}
          updating={automationUpdating}
          disabled={!targetID}
          onToggle={handleToggleAutomation}
        />
        <AccountPoolPreviewPanel
          preview={preview}
          loading={previewLoading}
          error={previewError}
          disabled={!targetID || !currentSnapshot || targetChanging || snapshotDirty || accounts.length === 0}
          onGenerate={handleGeneratePreview}
          onOpenApply={() => setApplyOpen(true)}
        />
      </div>

      {!currentSnapshot ? (
        <Empty className="border border-border bg-card">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <Database className="size-5" />
            </EmptyMedia>
            <EmptyTitle>暂无缓存数据</EmptyTitle>
            <EmptyDescription>点击上方“刷新”读取当前账号池并保存为最新缓存。</EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : accounts.length === 0 ? (
        <Empty className="border border-border bg-card">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <Database className="size-5" />
            </EmptyMedia>
            <EmptyTitle>暂无账号</EmptyTitle>
            <EmptyDescription>当前 target 的快照里没有可见账号。</EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : filteredAccounts.length === 0 ? (
        <Empty className="border border-border bg-card">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <Database className="size-5" />
            </EmptyMedia>
            <EmptyTitle>没有匹配账号</EmptyTitle>
            <EmptyDescription>调整搜索词或筛选条件后重试。</EmptyDescription>
          </EmptyHeader>
          <Button type="button" variant="outline" onClick={() => setFilters(defaultAccountPoolFilters)}>
            清空筛选
          </Button>
        </Empty>
      ) : (
        <>
          <AccountPoolDesktopTable
            accounts={filteredAccounts}
            busyAccountID={busyAccountID}
            onToggleSchedulable={handleToggleSchedulable}
            sort={filters.sort}
            onSortChange={(sort) => setFilters((current) => ({ ...current, sort }))}
          />
          <AccountPoolMobileCards
            accounts={filteredAccounts}
            busyAccountID={busyAccountID}
            onToggleSchedulable={handleToggleSchedulable}
          />
        </>
      )}

      <AccountPoolApplyDialog
        open={applyOpen}
        targetName={selectedTarget?.name}
        preview={preview}
        applying={applying}
        conflictMessage={applyConflict}
        onOpenChange={setApplyOpen}
        onApply={handleApplyPreview}
      />
      {confirmDialog}
    </>
  )
}
