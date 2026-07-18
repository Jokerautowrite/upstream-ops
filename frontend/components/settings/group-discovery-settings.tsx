import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import {
  Check,
  ChevronDown,
  ChevronUp,
  CircleAlert,
  ExternalLink,
  ListChecks,
  Play,
  RefreshCw,
  ScanSearch,
  X,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useConfirm } from "@/components/ui/confirm-dialog";
import { apiFetch } from "@/lib/api";
import { formatRatio, relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import type {
  GroupDiscoveryApplyResult,
  GroupDiscoveryCandidate,
  GroupDiscoveryCandidateStatus,
  GroupDiscoveryScanResult,
  UpstreamSyncTarget,
  UpstreamSyncTargetGroup,
} from "@/lib/api-types";

interface ApprovalForm {
  targetID: string;
  targetGroupIDs: number[];
  accountName: string;
  platform: string;
  concurrency: string;
  weight: string;
}

const emptyApprovalForm: ApprovalForm = {
  targetID: "",
  targetGroupIDs: [],
  accountName: "",
  platform: "openai",
  concurrency: "10",
  weight: "1",
};

function candidateStatusLabel(status: GroupDiscoveryCandidateStatus) {
  const labels: Record<GroupDiscoveryCandidateStatus, string> = {
    pending: "待审核",
    approved: "已批准",
    rejected: "已忽略",
    applying: "应用中",
    applied: "已应用",
    failed: "待重试",
  };
  return labels[status];
}

function candidateStatusClass(status: GroupDiscoveryCandidateStatus) {
  switch (status) {
    case "failed":
      return "bg-danger/10 text-danger";
    case "rejected":
      return "bg-slate-100 text-slate-500";
    case "pending":
      return "bg-amber-50 text-amber-700";
    case "approved":
    case "applying":
      return "bg-sky-50 text-sky-700";
    case "applied":
      return "bg-emerald-50 text-emerald-700";
  }
}

function positiveInteger(value: string, fallback: number) {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : fallback;
}

export function GroupDiscoverySettings() {
  const { confirm, dialog } = useConfirm();
  const [candidates, setCandidates] = useState<GroupDiscoveryCandidate[]>([]);
  const [targets, setTargets] = useState<UpstreamSyncTarget[]>([]);
  const [targetGroups, setTargetGroups] = useState<UpstreamSyncTargetGroup[]>(
    [],
  );
  const [loading, setLoading] = useState(true);
  const [scanning, setScanning] = useState(false);
  const [topNPerChannel, setTopNPerChannel] = useState("5");
  const [busy, setBusy] = useState<string | null>(null);
  const [approvalCandidate, setApprovalCandidate] =
    useState<GroupDiscoveryCandidate | null>(null);
  const [approvalForm, setApprovalForm] =
    useState<ApprovalForm>(emptyApprovalForm);
  const [openErrorIDs, setOpenErrorIDs] = useState<number[]>([]);

  const approvedOrRetryable = useMemo(
    () =>
      candidates.filter(
        (candidate) =>
          candidate.status === "approved" ||
          candidate.status === "failed" ||
          candidate.status === "applying",
      ),
    [candidates],
  );
  const targetByID = useMemo(
    () => new Map(targets.map((target) => [target.id, target])),
    [targets],
  );
  const activeTargetGroups = useMemo(
    () =>
      targetGroups
        .filter((group) => group.status === "active" || !group.status)
        .sort((a, b) => a.ratio - b.ratio || a.sort - b.sort || a.id - b.id),
    [targetGroups],
  );

  useEffect(() => {
    void loadBase();
  }, []);

  useEffect(() => {
    const targetID = Number(approvalForm.targetID);
    if (!targetID) {
      setTargetGroups([]);
      return;
    }
    void loadTargetGroups(targetID);
  }, [approvalForm.targetID]);

  async function loadBase() {
    setLoading(true);
    try {
      const [items, targetList] = await Promise.all([
        apiFetch<GroupDiscoveryCandidate[]>(
          "/upstream-sync/group-discovery/candidates",
        ),
        apiFetch<UpstreamSyncTarget[]>("/upstream-sync/targets"),
      ]);
      setCandidates(items);
      setTargets(targetList);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "加载分组发现队列失败");
    } finally {
      setLoading(false);
    }
  }

  async function loadCandidates() {
    const items = await apiFetch<GroupDiscoveryCandidate[]>(
      "/upstream-sync/group-discovery/candidates",
    );
    setCandidates(items);
  }

  async function loadTargetGroups(targetID: number) {
    try {
      const groups = await apiFetch<UpstreamSyncTargetGroup[]>(
        `/upstream-sync/targets/${targetID}/groups?include_missing=1`,
      );
      setTargetGroups(groups);
      setApprovalForm((previous) => ({
        ...previous,
        targetGroupIDs: previous.targetGroupIDs.filter((id) =>
          groups.some(
            (group) =>
              group.remote_group_id === id &&
              (group.status === "active" || !group.status),
          ),
        ),
      }));
    } catch (err) {
      setTargetGroups([]);
      toast.error(err instanceof Error ? err.message : "加载目标分组失败");
    }
  }

  async function scan() {
	const topN = positiveInteger(topNPerChannel, 5);
    setScanning(true);
    try {
      const result = await apiFetch<GroupDiscoveryScanResult>(
        "/upstream-sync/group-discovery/scan",
		{
			method: "POST",
			body: JSON.stringify({ top_n_per_channel: topN }),
		},
      );
      await loadCandidates();
		const summary = `已扫描 ${result.scanned_channels}/${result.total_channels} 个监控渠道；每类最低 ${result.top_n_per_channel} 条，并列全收；选中 ${result.selected_candidates} 条，新增 ${result.new_candidates} 条，更新 ${result.updated_candidates} 条，移除 ${result.deleted_candidates} 条`;
      if (result.errors?.length) {
        toast.warning(`${summary}，${result.errors.length} 个渠道异常`);
      } else {
        toast.success(summary);
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "扫描分组失败");
    } finally {
      setScanning(false);
    }
  }

  async function syncApprovalTargetGroups() {
    const targetID = Number(approvalForm.targetID);
    if (!targetID) return;
    setBusy("sync-target-groups");
    try {
      const groups = await apiFetch<UpstreamSyncTargetGroup[]>(
        `/upstream-sync/targets/${targetID}/groups/sync`,
        { method: "POST" },
      );
      setTargetGroups(groups);
      setApprovalForm((previous) => ({
        ...previous,
        targetGroupIDs: previous.targetGroupIDs.filter((id) =>
          groups.some(
            (group) =>
              group.remote_group_id === id &&
              (group.status === "active" || !group.status),
          ),
        ),
      }));
      toast.success("目标分组已刷新");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "刷新目标分组失败");
    } finally {
      setBusy(null);
    }
  }

  function openApproval(candidate: GroupDiscoveryCandidate) {
    setApprovalCandidate(candidate);
    setApprovalForm({
      targetID: candidate.target_id ? String(candidate.target_id) : "",
      targetGroupIDs: candidate.target_group_ids ?? [],
      accountName: candidate.account_name || candidate.source_group_name,
      platform: candidate.platform || "openai",
      concurrency: String(candidate.concurrency || 10),
      weight: String(candidate.weight || 1),
    });
  }

  function closeApproval() {
    setApprovalCandidate(null);
    setApprovalForm(emptyApprovalForm);
    setTargetGroups([]);
  }

  function toggleTargetGroup(id: number, checked: boolean) {
    setApprovalForm((previous) => ({
      ...previous,
      targetGroupIDs: checked
        ? [...previous.targetGroupIDs, id]
        : previous.targetGroupIDs.filter((item) => item !== id),
    }));
  }

  async function approve() {
    if (!approvalCandidate) return;
    const targetID = Number(approvalForm.targetID);
    if (!targetID) {
      toast.error("请选择目标 Sub2API 上游");
      return;
    }
    if (approvalForm.targetGroupIDs.length === 0) {
      toast.error("至少选择一个目标分组");
      return;
    }
    setBusy(`approve-${approvalCandidate.id}`);
    try {
      await apiFetch<GroupDiscoveryCandidate>(
        `/upstream-sync/group-discovery/candidates/${approvalCandidate.id}/approve`,
        {
          method: "POST",
          body: JSON.stringify({
            target_id: targetID,
            target_group_ids: approvalForm.targetGroupIDs,
            account_name: approvalForm.accountName.trim(),
            platform: approvalForm.platform.trim(),
            concurrency: positiveInteger(approvalForm.concurrency, 10),
            weight: positiveInteger(approvalForm.weight, 1),
          }),
        },
      );
      await loadCandidates();
      closeApproval();
      toast.success("映射已批准，等待显式应用");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "批准映射失败");
    } finally {
      setBusy(null);
    }
  }

  async function reject(candidate: GroupDiscoveryCandidate) {
    const ok = await confirm({
      title: `忽略来源分组 ${candidate.source_group_name}？`,
      description: "这只会忽略本地候选，不会修改上游或目标站。已有远端对象的候选不能直接忽略。",
      confirmLabel: "忽略",
      destructive: true,
    });
    if (!ok) return;
    setBusy(`reject-${candidate.id}`);
    try {
      await apiFetch<GroupDiscoveryCandidate>(
        `/upstream-sync/group-discovery/candidates/${candidate.id}/reject`,
        { method: "POST" },
      );
      await loadCandidates();
      toast.success("候选已忽略");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "忽略候选失败");
    } finally {
      setBusy(null);
    }
  }

  async function apply(candidateIDs: number[], label: string) {
    if (candidateIDs.length === 0) {
      toast.error("没有已批准或可重试的候选");
      return;
    }
    const ok = await confirm({
      title: `应用 ${candidateIDs.length} 条${label}？`,
      description:
        "这会在来源渠道创建或更新专用 API Key，并在目标 Sub2API 创建或更新账号。应用会先校验当前分组和同名手工对象。",
      confirmLabel: "确认应用",
    });
    if (!ok) return;
    const busyKey = candidateIDs.length === 1 ? `apply-${candidateIDs[0]}` : "apply-all";
    setBusy(busyKey);
    try {
      const result = await apiFetch<GroupDiscoveryApplyResult>(
        "/upstream-sync/group-discovery/apply",
        {
          method: "POST",
          body: JSON.stringify({ candidate_ids: candidateIDs }),
        },
      );
      await loadCandidates();
      if (result.failed > 0) {
        toast.warning(`已应用 ${result.applied} 条，${result.failed} 条失败，可查看错误后重试`);
      } else {
        toast.success(`已应用 ${result.applied} 条候选`);
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "应用候选失败");
    } finally {
      setBusy(null);
    }
  }

  if (loading) {
    return <p className="text-sm text-muted-foreground">分组发现队列加载中...</p>;
  }

  return (
    <div className="space-y-5">
      <section className="rounded-3xl border border-border/80 bg-muted/20 p-5">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-sm font-semibold text-foreground">上游 API Key 分组发现</p>
              <Badge variant="outline" className="border-border bg-background">
                {candidates.length} 条候选
              </Badge>
              {approvedOrRetryable.length > 0 ? (
                <Badge variant="outline" className="border-transparent bg-sky-50 text-sky-700">
                  {approvedOrRetryable.length} 条待应用
                </Badge>
              ) : null}
            </div>
            <p className="max-w-3xl text-sm leading-6 text-muted-foreground">
              扫描只读取已启用监控的来源渠道，按账号池业务类型分别保留最低 N 条（临界倍率并列全收）。先逐条审核，只有点击应用才会创建或更新远端对象。
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" onClick={() => void loadBase()} disabled={scanning}>
              <RefreshCw className="size-3.5" />
              刷新队列
            </Button>
			<div className="flex items-center gap-1.5 rounded-md border border-border bg-background px-2">
				<Label htmlFor="discovery-top-n" className="whitespace-nowrap text-xs text-muted-foreground">每类最低</Label>
				<Input
					id="discovery-top-n"
					type="number"
					min="1"
					max="100"
					value={topNPerChannel}
					onChange={(event) => setTopNPerChannel(event.target.value)}
					className="h-7 w-14 border-0 px-1 text-center shadow-none focus-visible:ring-0"
				/>
			</div>
            <Button size="sm" variant="outline" onClick={() => void scan()} disabled={scanning}>
              <ScanSearch className={cn("size-3.5", scanning && "animate-pulse")} />
              {scanning ? "扫描中" : "扫描来源分组"}
            </Button>
            <Button
              size="sm"
              onClick={() => void apply(approvedOrRetryable.map((item) => item.id), "已批准候选")}
              disabled={approvedOrRetryable.length === 0 || busy === "apply-all"}
            >
              {busy === "apply-all" ? <RefreshCw className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
              应用已批准项
            </Button>
          </div>
        </div>
      </section>

      {candidates.length === 0 ? (
        <div className="rounded-2xl border border-dashed border-border bg-background/70 px-4 py-8 text-sm text-muted-foreground">
          尚未发现来源分组。点击“扫描来源分组”创建本地审核候选。
        </div>
      ) : (
        <div className="space-y-3">
          {candidates.map((candidate) => {
            const isApplying = busy === `apply-${candidate.id}`;
            const target = candidate.target_id
              ? targetByID.get(candidate.target_id)
              : undefined;
            const errorOpen = openErrorIDs.includes(candidate.id);
            return (
              <article key={candidate.id} className="rounded-2xl border border-border bg-background/80 p-4">
                <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_auto] xl:items-start">
                  <div className="min-w-0 space-y-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <p className="truncate text-sm font-semibold text-foreground">
                        {candidate.source_group_name}
                      </p>
                      <Badge variant="outline" className={cn("border-transparent", candidateStatusClass(candidate.status))}>
                        {candidateStatusLabel(candidate.status)}
                      </Badge>
                      <Badge variant="outline" className="border-border bg-muted/40 text-muted-foreground">
                        倍率 {formatRatio(candidate.ratio)}
                      </Badge>
					  <Badge variant="outline" className="border-border bg-background text-muted-foreground">
						{candidate.channel_type || "Other"}
					  </Badge>
                    </div>
                    <p className="text-xs text-muted-foreground">
                      来源：{candidate.source_channel_name}
                      {candidate.source_group_id != null ? ` · 分组 ID ${candidate.source_group_id}` : ""}
                      {candidate.source_group_description ? ` · ${candidate.source_group_description}` : ""}
                    </p>
                    {candidate.target_id ? (
                      <p className="text-xs text-muted-foreground">
                        目标：{target?.name ?? `目标 ID ${candidate.target_id}`} · 分组 {candidate.target_group_names.join("、") || candidate.target_group_ids.join("、")} · 账号 {candidate.account_name}
                      </p>
                    ) : (
                      <p className="text-xs text-muted-foreground">尚未选择目标映射</p>
                    )}
                    {candidate.applied_at ? (
                      <p className="text-xs text-muted-foreground">
                        最近应用 {relativeTime(candidate.applied_at)}
                        {candidate.target_account_id ? ` · 目标账号 ID ${candidate.target_account_id}` : ""}
                      </p>
                    ) : null}
                    {candidate.apply_error ? (
                      <div className="rounded-lg border border-danger/20 bg-danger/5 px-3 py-2">
                        <button
                          type="button"
                          className="flex w-full items-center justify-between gap-3 text-left text-xs text-danger"
                          onClick={() =>
                            setOpenErrorIDs((current) =>
                              current.includes(candidate.id)
                                ? current.filter((id) => id !== candidate.id)
                                : [...current, candidate.id],
                            )
                          }
                        >
                          <span className="flex min-w-0 items-center gap-1.5">
                            <CircleAlert className="size-3.5 shrink-0" />
                            应用失败，{errorOpen ? "收起详情" : "查看详情"}
                          </span>
                          {errorOpen ? <ChevronUp className="size-3.5" /> : <ChevronDown className="size-3.5" />}
                        </button>
                        {errorOpen ? (
                          <pre className="mt-2 whitespace-pre-wrap break-words border-t border-danger/15 pt-2 text-xs leading-5 text-danger">
                            {candidate.apply_error}
                          </pre>
                        ) : null}
                      </div>
                    ) : null}
                  </div>
                  <div className="flex flex-wrap items-center gap-2 xl:justify-end">
                    {candidate.source_channel_url ? (
                      <Button size="sm" variant="outline" asChild>
                        <a
                          href={candidate.source_channel_url}
                          target="_blank"
                          rel="noopener noreferrer"
                        >
                          <ExternalLink className="size-3.5" />
                          打开渠道网站
                        </a>
                      </Button>
                    ) : null}
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => openApproval(candidate)}
                      disabled={candidate.status === "applying"}
                    >
                      <ListChecks className="size-3.5" />
                      {candidate.status === "approved" || candidate.status === "failed" ? "编辑映射" : "审核映射"}
                    </Button>
                    {(candidate.status === "approved" || candidate.status === "failed" || candidate.status === "applying" || candidate.status === "applied") ? (
                      <Button
                        size="sm"
                        variant={candidate.status === "failed" || candidate.status === "applying" ? "default" : "outline"}
                        onClick={() => void apply([candidate.id], candidate.status === "failed" ? "失败候选" : candidate.status === "applying" ? "遗留应用" : "候选")}
                        disabled={isApplying}
                      >
                        {isApplying ? <RefreshCw className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                        {isApplying ? "应用中" : candidate.status === "failed" ? "重试" : candidate.status === "applying" ? "恢复应用" : "应用"}
                      </Button>
                    ) : null}
                    {(candidate.status === "pending" || candidate.status === "rejected") ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-muted-foreground"
                        onClick={() => void reject(candidate)}
                        disabled={busy === `reject-${candidate.id}`}
                      >
                        <X className="size-3.5" />
                        忽略
                      </Button>
                    ) : null}
                  </div>
                </div>
              </article>
            );
          })}
        </div>
      )}

      <Dialog open={approvalCandidate != null} onOpenChange={(open) => !open && closeApproval()}>
        <DialogContent className="max-h-[calc(100dvh-1rem)] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>审核来源分组映射</DialogTitle>
            <DialogDescription>
              {approvalCandidate
                ? `来源 ${approvalCandidate.source_channel_name} / ${approvalCandidate.source_group_name}，倍率 ${formatRatio(approvalCandidate.ratio)}。保存审核映射不会写入远端。`
                : ""}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label>目标 Sub2API 上游</Label>
                <Select
                  value={approvalForm.targetID}
                  onValueChange={(value) =>
                    setApprovalForm((previous) => ({
                      ...previous,
                      targetID: value,
                      targetGroupIDs: [],
                    }))
                  }
                >
                  <SelectTrigger>
                    <SelectValue placeholder="选择已启用目标" />
                  </SelectTrigger>
                  <SelectContent>
                    {targets.filter((target) => target.enabled).map((target) => (
                      <SelectItem key={target.id} value={String(target.id)}>
                        {target.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label>平台</Label>
                <Input
                  value={approvalForm.platform}
                  placeholder="openai"
                  onChange={(event) => setApprovalForm((previous) => ({ ...previous, platform: event.target.value }))}
                />
              </div>
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between gap-3">
                <Label>目标分组</Label>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => void syncApprovalTargetGroups()}
                  disabled={!approvalForm.targetID || busy === "sync-target-groups"}
                >
                  <RefreshCw className={cn("size-3.5", busy === "sync-target-groups" && "animate-spin")} />
                  刷新远端分组
                </Button>
              </div>
              {!approvalForm.targetID ? (
                <p className="rounded-lg border border-dashed border-border px-3 py-3 text-sm text-muted-foreground">先选择目标上游。</p>
              ) : activeTargetGroups.length === 0 ? (
                <p className="rounded-lg border border-dashed border-border px-3 py-3 text-sm text-muted-foreground">目标分组缓存为空。点击“刷新远端分组”读取当前目标站分组。</p>
              ) : (
                <div className="grid gap-2 sm:grid-cols-2">
                  {activeTargetGroups.map((group) => {
                    const checked = approvalForm.targetGroupIDs.includes(group.remote_group_id);
                    return (
                      <label key={group.id} className="flex cursor-pointer items-start gap-3 rounded-lg border border-border bg-background px-3 py-2.5">
                        <Checkbox checked={checked} onCheckedChange={(value) => toggleTargetGroup(group.remote_group_id, value === true)} />
                        <span className="min-w-0 text-sm">
                          <span className="block truncate font-medium text-foreground">{group.name}</span>
                          <span className="block text-xs text-muted-foreground">{group.platform || "未分类"} · 倍率 {formatRatio(group.ratio)} · ID {group.remote_group_id}</span>
                        </span>
                      </label>
                    );
                  })}
                </div>
              )}
            </div>
            <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_120px_120px]">
              <div className="space-y-2">
				<Label>加入 Sub2 后的账号名称</Label>
                <Input
                  value={approvalForm.accountName}
                  maxLength={100}
                  onChange={(event) => setApprovalForm((previous) => ({ ...previous, accountName: event.target.value }))}
                />
				<p className="text-xs text-muted-foreground">可修改；应用后 Sub2 将使用此名称，便于和来源渠道一一对应。</p>
              </div>
              <div className="space-y-2">
                <Label>并发</Label>
                <Input
                  type="number"
                  min="1"
                  value={approvalForm.concurrency}
                  onChange={(event) => setApprovalForm((previous) => ({ ...previous, concurrency: event.target.value }))}
                />
              </div>
              <div className="space-y-2">
                <Label>权重</Label>
                <Input
                  type="number"
                  min="1"
                  value={approvalForm.weight}
                  onChange={(event) => setApprovalForm((previous) => ({ ...previous, weight: event.target.value }))}
                />
              </div>
            </div>
            <p className="rounded-lg border border-sky-200 bg-sky-50 px-3 py-2 text-xs leading-5 text-sky-800">
              批准时会再次读取目标分组并验证所选分组仍启用；实际 API Key 和目标账号会在后续“应用”操作中创建或更新。
            </p>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={closeApproval}>取消</Button>
            <Button onClick={() => void approve()} disabled={!approvalCandidate || busy === `approve-${approvalCandidate?.id}`}>
              <Check className="size-3.5" />
              保存审核映射
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {dialog}
    </div>
  );
}
