import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import {
  Activity,
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useConfirm } from "@/components/ui/confirm-dialog";
import { apiFetch } from "@/lib/api";
import { formatRatio, relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import type {
  GroupDiscoveryApplyResult,
  GroupDiscoveryCandidate,
  GroupDiscoveryCandidateStatus,
  GroupDiscoveryProbeBatchResult,
  GroupDiscoveryProbeResult,
  GroupDiscoveryScanResult,
  UpstreamSyncTarget,
  UpstreamSyncTargetGroup,
} from "@/lib/api-types";

// ── types & constants ──────────────────────────────────────────────

interface ApprovalForm {
  targetID: string;
  targetGroupIDs: number[];
  accountName: string;
  platform: string;
  concurrency: string;
  weight: string;
}

type StatusFilter = "all" | GroupDiscoveryCandidateStatus;
type ProbeFilter = "all" | "untested" | "ok" | "fail";
type ApprovalMode = "single" | "batch";

const emptyApprovalForm: ApprovalForm = {
  targetID: "",
  targetGroupIDs: [],
  accountName: "",
  platform: "openai",
  concurrency: "10",
  weight: "1",
};

/** 类型预设：只填 targetGroupIDs（及单目标时自动选中），不静默 apply */
const TYPE_PRESETS = [
  { key: "plus", label: "Plus 组", groupIDs: [3, 66, 83] },
  { key: "pro", label: "Pro 组", groupIDs: [77, 84, 88, 89] },
] as const;

const STATUS_FILTER_OPTIONS: { value: StatusFilter; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "pending", label: "待审核" },
  { value: "approved", label: "已批准" },
  { value: "applied", label: "已应用" },
  { value: "failed", label: "失败" },
  { value: "rejected", label: "忽略" },
  { value: "applying", label: "应用中" },
];

const PROBE_FILTER_OPTIONS: { value: ProbeFilter; label: string }[] = [
  { value: "all", label: "测活：全部" },
  { value: "untested", label: "未测活" },
  { value: "ok", label: "测活通过" },
  { value: "fail", label: "测活失败" },
];

// ── helpers ────────────────────────────────────────────────────────

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

function probeStatusLabel(status?: string | null) {
  if (status === "ok") return "通过";
  if (status === "fail") return "失败";
  return "未测";
}

function probeStatusClass(status?: string | null) {
  if (status === "ok") return "bg-emerald-50 text-emerald-700";
  if (status === "fail") return "bg-danger/10 text-danger";
  return "bg-muted/40 text-muted-foreground";
}

function mergeCandidate(
  list: GroupDiscoveryCandidate[],
  next: GroupDiscoveryCandidate,
) {
  return list.map((item) => (item.id === next.id ? next : item));
}

function positiveInteger(value: string, fallback: number) {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : fallback;
}

function defaultAccountName(candidate: GroupDiscoveryCandidate) {
  return candidate.account_name || candidate.source_group_name;
}

function isRejectable(status: GroupDiscoveryCandidateStatus) {
  return status === "pending" || status === "rejected";
}

function isApplyable(status: GroupDiscoveryCandidateStatus) {
  return (
    status === "approved" || status === "failed" || status === "applying"
  );
}

function targetSummary(
  candidate: GroupDiscoveryCandidate,
  targetByID: Map<number, UpstreamSyncTarget>,
) {
  if (!candidate.target_id) return "—";
  const target = targetByID.get(candidate.target_id);
  const groups =
    candidate.target_group_names.join("、") ||
    candidate.target_group_ids.join("、") ||
    "—";
  return `${target?.name ?? `ID ${candidate.target_id}`} · ${groups}`;
}

// ── component ──────────────────────────────────────────────────────

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

  // filters
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("pending");
  const [probeFilter, setProbeFilter] = useState<ProbeFilter>("all");
  const [searchQuery, setSearchQuery] = useState("");
  const [filterSeeded, setFilterSeeded] = useState(false);

  // selection
  const [selectedIDs, setSelectedIDs] = useState<number[]>([]);

  // approval dialog (single or batch)
  const [approvalMode, setApprovalMode] = useState<ApprovalMode>("single");
  const [approvalCandidates, setApprovalCandidates] = useState<
    GroupDiscoveryCandidate[]
  >([]);
  const [approvalForm, setApprovalForm] =
    useState<ApprovalForm>(emptyApprovalForm);
  const [openErrorIDs, setOpenErrorIDs] = useState<number[]>([]);

  const approvedOrRetryable = useMemo(
    () => candidates.filter((c) => isApplyable(c.status)),
    [candidates],
  );
  const targetByID = useMemo(
    () => new Map(targets.map((t) => [t.id, t])),
    [targets],
  );
  const enabledTargets = useMemo(
    () => targets.filter((t) => t.enabled),
    [targets],
  );
  const activeTargetGroups = useMemo(
    () =>
      targetGroups
        .filter((g) => g.status === "active" || !g.status)
        .sort((a, b) => a.ratio - b.ratio || a.sort - b.sort || a.id - b.id),
    [targetGroups],
  );

  const filteredCandidates = useMemo(() => {
    const q = searchQuery.trim().toLowerCase();
    return candidates.filter((c) => {
      if (statusFilter !== "all" && c.status !== statusFilter) return false;
      const probe = (c.probe_status || "").toLowerCase();
      if (probeFilter === "untested" && (probe === "ok" || probe === "fail")) {
        return false;
      }
      if (probeFilter === "ok" && probe !== "ok") return false;
      if (probeFilter === "fail" && probe !== "fail") return false;
      if (!q) return true;
      return (
        c.source_group_name.toLowerCase().includes(q) ||
        c.source_channel_name.toLowerCase().includes(q) ||
        (c.probe_error || "").toLowerCase().includes(q)
      );
    });
  }, [candidates, statusFilter, probeFilter, searchQuery]);

  const visibleIDs = useMemo(
    () => filteredCandidates.map((c) => c.id),
    [filteredCandidates],
  );
  const allVisibleSelected =
    visibleIDs.length > 0 && visibleIDs.every((id) => selectedIDs.includes(id));
  const selectedSet = useMemo(() => new Set(selectedIDs), [selectedIDs]);
  const selectedCandidates = useMemo(
    () => candidates.filter((c) => selectedSet.has(c.id)),
    [candidates, selectedSet],
  );
  const selectedRejectable = useMemo(
    () => selectedCandidates.filter((c) => isRejectable(c.status)),
    [selectedCandidates],
  );
  const selectedApplyable = useMemo(
    () => selectedCandidates.filter((c) => isApplyable(c.status)),
    [selectedCandidates],
  );
  const selectedApprovable = useMemo(
    () =>
      selectedCandidates.filter(
        (c) => c.status !== "applying" && c.status !== "applied",
      ),
    [selectedCandidates],
  );

  const approvalOpen = approvalCandidates.length > 0;
  const singleCandidate =
    approvalMode === "single" ? approvalCandidates[0] ?? null : null;

  // ── data loading ─────────────────────────────────────────────────

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

  // drop selection for candidates that disappeared
  useEffect(() => {
    const alive = new Set(candidates.map((c) => c.id));
    setSelectedIDs((prev) => prev.filter((id) => alive.has(id)));
  }, [candidates]);

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
      if (!filterSeeded) {
        const hasPending = items.some((c) => c.status === "pending");
        setStatusFilter(hasPending ? "pending" : "all");
        setFilterSeeded(true);
      }
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
      // 不自动裁剪 targetGroupIDs：切换目标时 Select 已清空；
      // 类型预设可能先于分组缓存写入 ID，需保留供用户确认。
    } catch (err) {
      setTargetGroups([]);
      toast.error(err instanceof Error ? err.message : "加载目标分组失败");
    }
  }

  // ── scan / sync groups ───────────────────────────────────────────

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
      toast.success("目标分组已刷新");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "刷新目标分组失败");
    } finally {
      setBusy(null);
    }
  }

  // ── selection ────────────────────────────────────────────────────

  function toggleSelect(id: number, checked: boolean) {
    setSelectedIDs((prev) =>
      checked
        ? prev.includes(id)
          ? prev
          : [...prev, id]
        : prev.filter((item) => item !== id),
    );
  }

  function selectAllVisible() {
    setSelectedIDs((prev) => {
      const next = new Set(prev);
      for (const id of visibleIDs) next.add(id);
      return Array.from(next);
    });
  }

  function deselectVisible() {
    const visible = new Set(visibleIDs);
    setSelectedIDs((prev) => prev.filter((id) => !visible.has(id)));
  }

  function clearSelection() {
    setSelectedIDs([]);
  }

  // ── approval dialog ──────────────────────────────────────────────

  function openSingleApproval(candidate: GroupDiscoveryCandidate) {
    setApprovalMode("single");
    setApprovalCandidates([candidate]);
    setApprovalForm({
      targetID: candidate.target_id ? String(candidate.target_id) : "",
      targetGroupIDs: candidate.target_group_ids ?? [],
      accountName: defaultAccountName(candidate),
      platform: candidate.platform || "openai",
      concurrency: String(candidate.concurrency || 10),
      weight: String(candidate.weight || 1),
    });
  }

  function openBatchApproval() {
    if (selectedApprovable.length === 0) {
      toast.error("没有可审核的选中项（应用中/已应用不可批量审核）");
      return;
    }
    const first = selectedApprovable[0];
    const singleTarget =
      enabledTargets.length === 1 ? String(enabledTargets[0].id) : "";
    setApprovalMode("batch");
    setApprovalCandidates(selectedApprovable);
    setApprovalForm({
      targetID: first.target_id
        ? String(first.target_id)
        : singleTarget,
      targetGroupIDs: first.target_group_ids ?? [],
      accountName: "", // batch: each row uses own default
      platform: first.platform || "openai",
      concurrency: String(first.concurrency || 10),
      weight: String(first.weight || 1),
    });
  }

  function closeApproval() {
    setApprovalCandidates([]);
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

  /** 预设只填 targetGroupIDs；若仅一个启用目标则自动选中 */
  function applyTypePreset(groupIDs: number[]) {
    setApprovalForm((previous) => {
      let targetID = previous.targetID;
      if (!targetID && enabledTargets.length === 1) {
        targetID = String(enabledTargets[0].id);
      }
      return {
        ...previous,
        targetID,
        targetGroupIDs: [...groupIDs],
      };
    });
  }

  async function approveSingle(candidate: GroupDiscoveryCandidate) {
    const targetID = Number(approvalForm.targetID);
    if (!targetID) {
      throw new Error("请选择目标 Sub2API 上游");
    }
    if (approvalForm.targetGroupIDs.length === 0) {
      throw new Error("至少选择一个目标分组");
    }
    await apiFetch<GroupDiscoveryCandidate>(
      `/upstream-sync/group-discovery/candidates/${candidate.id}/approve`,
      {
        method: "POST",
        body: JSON.stringify({
          target_id: targetID,
          target_group_ids: approvalForm.targetGroupIDs,
          account_name: (
            approvalMode === "batch"
              ? defaultAccountName(candidate)
              : approvalForm.accountName
          ).trim(),
          platform: approvalForm.platform.trim(),
          concurrency: positiveInteger(approvalForm.concurrency, 10),
          weight: positiveInteger(approvalForm.weight, 1),
        }),
      },
    );
  }

  async function approve() {
    if (approvalCandidates.length === 0) return;
    const targetID = Number(approvalForm.targetID);
    if (!targetID) {
      toast.error("请选择目标 Sub2API 上游");
      return;
    }
    if (approvalForm.targetGroupIDs.length === 0) {
      toast.error("至少选择一个目标分组");
      return;
    }

    if (approvalMode === "single") {
      const candidate = approvalCandidates[0];
      setBusy(`approve-${candidate.id}`);
      try {
        await approveSingle(candidate);
        await loadCandidates();
        closeApproval();
        toast.success("映射已批准，等待显式应用");
      } catch (err) {
        toast.error(err instanceof Error ? err.message : "批准映射失败");
      } finally {
        setBusy(null);
      }
      return;
    }

    // batch: serial await, count success/fail
    setBusy("approve-batch");
    let success = 0;
    let fail = 0;
    const errors: string[] = [];
    try {
      for (const candidate of approvalCandidates) {
        try {
          await approveSingle(candidate);
          success += 1;
        } catch (err) {
          fail += 1;
          errors.push(
            `${candidate.source_group_name}: ${err instanceof Error ? err.message : "失败"}`,
          );
        }
      }
      await loadCandidates();
      closeApproval();
      if (fail === 0) {
        toast.success(`已批准 ${success} 条映射`);
      } else {
        toast.warning(
          `批准完成：成功 ${success}，失败 ${fail}${errors.length ? `（${errors.slice(0, 3).join("；")}${errors.length > 3 ? "…" : ""}）` : ""}`,
        );
      }
    } finally {
      setBusy(null);
    }
  }

  // ── reject ───────────────────────────────────────────────────────

  async function rejectOne(candidate: GroupDiscoveryCandidate) {
    await apiFetch<GroupDiscoveryCandidate>(
      `/upstream-sync/group-discovery/candidates/${candidate.id}/reject`,
      { method: "POST" },
    );
  }

  async function reject(candidate: GroupDiscoveryCandidate) {
    const ok = await confirm({
      title: `忽略来源分组 ${candidate.source_group_name}？`,
      description:
        "这只会忽略本地候选，不会修改上游或目标站。已有远端对象的候选不能直接忽略。",
      confirmLabel: "忽略",
      destructive: true,
    });
    if (!ok) return;
    setBusy(`reject-${candidate.id}`);
    try {
      await rejectOne(candidate);
      await loadCandidates();
      toast.success("候选已忽略");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "忽略候选失败");
    } finally {
      setBusy(null);
    }
  }

  async function rejectBatch() {
    if (selectedRejectable.length === 0) {
      toast.error("选中项中没有可忽略的候选（仅待审核/已忽略）");
      return;
    }
    const ok = await confirm({
      title: `批量忽略 ${selectedRejectable.length} 条候选？`,
      description:
        "这只会忽略本地候选，不会修改上游或目标站。已有远端对象的候选会失败。",
      confirmLabel: "批量忽略",
      destructive: true,
    });
    if (!ok) return;
    setBusy("reject-batch");
    let success = 0;
    let fail = 0;
    const errors: string[] = [];
    try {
      for (const candidate of selectedRejectable) {
        try {
          await rejectOne(candidate);
          success += 1;
        } catch (err) {
          fail += 1;
          errors.push(
            `${candidate.source_group_name}: ${err instanceof Error ? err.message : "失败"}`,
          );
        }
      }
      await loadCandidates();
      clearSelection();
      if (fail === 0) {
        toast.success(`已忽略 ${success} 条候选`);
      } else {
        toast.warning(
          `忽略完成：成功 ${success}，失败 ${fail}${errors.length ? `（${errors.slice(0, 3).join("；")}${errors.length > 3 ? "…" : ""}）` : ""}`,
        );
      }
    } finally {
      setBusy(null);
    }
  }

  // ── apply ────────────────────────────────────────────────────────

  async function apply(
    candidateIDs: number[],
    label: string,
    busyKey: string,
  ): Promise<boolean> {
    // 前端禁止空数组（后端空数组=全部合格项）
    if (candidateIDs.length === 0) {
      toast.error("没有已批准或可重试的候选");
      return false;
    }
    const ok = await confirm({
      title: `应用 ${candidateIDs.length} 条${label}？`,
      description:
        "这会在来源渠道创建或更新专用 API Key，并在目标 Sub2API 创建或更新账号。应用会先校验当前分组和同名手工对象。",
      confirmLabel: "确认应用",
    });
    if (!ok) return false;
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
        toast.warning(
          `已应用 ${result.applied} 条，${result.failed} 条失败，可查看错误后重试`,
        );
      } else {
        toast.success(`已应用 ${result.applied} 条候选`);
      }
      return true;
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "应用候选失败");
      return false;
    } finally {
      setBusy(null);
    }
  }

  // ── probe / 测活 ─────────────────────────────────────────────────

  async function probeOne(candidate: GroupDiscoveryCandidate) {
    setBusy(`probe-${candidate.id}`);
    try {
      const result = await apiFetch<GroupDiscoveryProbeResult>(
        `/upstream-sync/group-discovery/candidates/${candidate.id}/probe`,
        { method: "POST" },
      );
      setCandidates((list) => mergeCandidate(list, result.candidate));
      if (result.ok) {
        toast.success(
          `${candidate.source_group_name} 测活通过${result.candidate.probe_model ? ` · ${result.candidate.probe_model}` : ""}`,
        );
      } else {
        toast.error(
          `${candidate.source_group_name} 测活失败：${result.error || result.candidate.probe_error || "未知错误"}`,
        );
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "测活失败");
    } finally {
      setBusy(null);
    }
  }

  async function probeBatch() {
    if (selectedIDs.length === 0) {
      toast.error("请先选择要测活的候选");
      return;
    }
    const ok = await confirm({
      title: `批量测活 ${selectedIDs.length} 条分组？`,
      description:
        "会对每条候选在来源站创建/复用 discovery 专用 API Key，拉取模型列表并做一次最小对话。已应用的账号还会跑 Sub2 测活。不会创建新的 Sub2 账号。",
      confirmLabel: "开始测活",
    });
    if (!ok) return;
    setBusy("probe-batch");
    try {
      const result = await apiFetch<GroupDiscoveryProbeBatchResult>(
        "/upstream-sync/group-discovery/probe",
        {
          method: "POST",
          body: JSON.stringify({ candidate_ids: selectedIDs }),
        },
      );
      setCandidates((list) => {
        let next = list;
        for (const item of result.items) {
          if (item.candidate?.id) {
            next = mergeCandidate(next, item.candidate);
          }
        }
        return next;
      });
      if (result.failed === 0) {
        toast.success(`测活完成：${result.ok} 条全部通过`);
      } else {
        toast.warning(`测活完成：通过 ${result.ok}，失败 ${result.failed}`);
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "批量测活失败");
    } finally {
      setBusy(null);
    }
  }

  async function applySelected() {
    if (selectedApplyable.length === 0) {
      toast.error("选中项中没有可应用的候选（已批准/失败/应用中）");
      return;
    }
    const ids = selectedApplyable.map((c) => c.id);
    const done = await apply(ids, "选中候选", "apply-batch");
    if (done) {
      setSelectedIDs((prev) => prev.filter((id) => !ids.includes(id)));
    }
  }

  // ── render helpers ───────────────────────────────────────────────

  function toggleError(id: number) {
    setOpenErrorIDs((current) =>
      current.includes(id)
        ? current.filter((item) => item !== id)
        : [...current, id],
    );
  }

  function rowActions(candidate: GroupDiscoveryCandidate) {
    const isApplying = busy === `apply-${candidate.id}`;
    const isProbing = busy === `probe-${candidate.id}`;
    return (
      <div className="flex flex-wrap items-center gap-1">
        {candidate.source_channel_url ? (
          <Button size="sm" variant="ghost" className="h-7 px-2" asChild>
            <a
              href={candidate.source_channel_url}
              target="_blank"
              rel="noopener noreferrer"
              title="打开渠道网站"
            >
              <ExternalLink className="size-3.5" />
            </a>
          </Button>
        ) : null}
        <Button
          size="sm"
          variant="outline"
          className="h-7"
          onClick={() => void probeOne(candidate)}
          disabled={isProbing || busy === "probe-batch"}
          title={
            candidate.probe_error ||
            candidate.probe_detail ||
            "测活：来源 Key + 模型列表 + 最小对话"
          }
        >
          {isProbing ? (
            <RefreshCw className="size-3.5 animate-spin" />
          ) : (
            <Activity className="size-3.5" />
          )}
          测活
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="h-7"
          onClick={() => openSingleApproval(candidate)}
          disabled={candidate.status === "applying"}
        >
          <ListChecks className="size-3.5" />
          {candidate.status === "approved" || candidate.status === "failed"
            ? "编辑"
            : "审核"}
        </Button>
        {isApplyable(candidate.status) || candidate.status === "applied" ? (
          <Button
            size="sm"
            variant={
              candidate.status === "failed" || candidate.status === "applying"
                ? "default"
                : "outline"
            }
            className="h-7"
            onClick={() =>
              void apply(
                [candidate.id],
                candidate.status === "failed"
                  ? "失败候选"
                  : candidate.status === "applying"
                    ? "遗留应用"
                    : "候选",
                `apply-${candidate.id}`,
              )
            }
            disabled={isApplying}
          >
            {isApplying ? (
              <RefreshCw className="size-3.5 animate-spin" />
            ) : (
              <Play className="size-3.5" />
            )}
            {isApplying
              ? "应用中"
              : candidate.status === "failed"
                ? "重试"
                : candidate.status === "applying"
                  ? "恢复"
                  : "应用"}
          </Button>
        ) : null}
        {isRejectable(candidate.status) ? (
          <Button
            size="sm"
            variant="ghost"
            className="h-7 text-muted-foreground"
            onClick={() => void reject(candidate)}
            disabled={busy === `reject-${candidate.id}`}
          >
            <X className="size-3.5" />
            忽略
          </Button>
        ) : null}
      </div>
    );
  }

  function errorBlock(candidate: GroupDiscoveryCandidate) {
    if (!candidate.apply_error) return null;
    const errorOpen = openErrorIDs.includes(candidate.id);
    return (
      <div className="mt-1.5 rounded-lg border border-danger/20 bg-danger/5 px-2.5 py-1.5">
        <button
          type="button"
          className="flex w-full items-center justify-between gap-2 text-left text-xs text-danger"
          onClick={() => toggleError(candidate.id)}
        >
          <span className="flex min-w-0 items-center gap-1.5">
            <CircleAlert className="size-3.5 shrink-0" />
            应用失败，{errorOpen ? "收起详情" : "查看详情"}
          </span>
          {errorOpen ? (
            <ChevronUp className="size-3.5" />
          ) : (
            <ChevronDown className="size-3.5" />
          )}
        </button>
        {errorOpen ? (
          <pre className="mt-1.5 whitespace-pre-wrap break-words border-t border-danger/15 pt-1.5 text-xs leading-5 text-danger">
            {candidate.apply_error}
          </pre>
        ) : null}
      </div>
    );
  }

  // ── loading ──────────────────────────────────────────────────────

  if (loading) {
    return (
      <p className="text-sm text-muted-foreground">分组发现队列加载中...</p>
    );
  }

  // ── main ─────────────────────────────────────────────────────────

  return (
    <div className="space-y-5">
      {/* 顶部工具栏 */}
      <section className="rounded-3xl border border-border/80 bg-muted/20 p-5">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-sm font-semibold text-foreground">
                上游 API Key 分组发现
              </p>
              <Badge variant="outline" className="border-border bg-background">
                {candidates.length} 条候选
              </Badge>
              {approvedOrRetryable.length > 0 ? (
                <Badge
                  variant="outline"
                  className="border-transparent bg-sky-50 text-sky-700"
                >
                  {approvedOrRetryable.length} 条待应用
                </Badge>
              ) : null}
              {selectedIDs.length > 0 ? (
                <Badge
                  variant="outline"
                  className="border-transparent bg-violet-50 text-violet-700"
                >
                  已选 {selectedIDs.length}
                </Badge>
              ) : null}
            </div>
            <p className="max-w-3xl text-sm leading-6 text-muted-foreground">
              扫描只读取已启用监控的来源渠道，按账号池业务类型分别保留最低 N
              条（临界倍率并列全收）。建议先「测活」筛掉不能用的分组，再审核映射；
              只有点击应用才会创建目标 Sub2 账号。测活会创建/复用 discovery 专用 Key，
              并做模型列表 + 最小对话。
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={() => void loadBase()}
              disabled={scanning}
            >
              <RefreshCw className="size-3.5" />
              刷新队列
            </Button>
            <div className="flex items-center gap-1.5 rounded-md border border-border bg-background px-2">
              <Label
                htmlFor="discovery-top-n"
                className="whitespace-nowrap text-xs text-muted-foreground"
              >
                每类最低
              </Label>
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
            <Button
              size="sm"
              variant="outline"
              onClick={() => void scan()}
              disabled={scanning}
            >
              <ScanSearch
                className={cn("size-3.5", scanning && "animate-pulse")}
              />
              {scanning ? "扫描中" : "扫描来源分组"}
            </Button>
            <Button
              size="sm"
              onClick={() =>
                void apply(
                  approvedOrRetryable.map((item) => item.id),
                  "已批准候选",
                  "apply-all",
                )
              }
              disabled={
                approvedOrRetryable.length === 0 || busy === "apply-all"
              }
            >
              {busy === "apply-all" ? (
                <RefreshCw className="size-3.5 animate-spin" />
              ) : (
                <Play className="size-3.5" />
              )}
              应用已批准项
            </Button>
          </div>
        </div>
      </section>

      {/* 过滤 + 批量操作栏 */}
      {candidates.length > 0 ? (
        <section className="flex flex-col gap-3 rounded-2xl border border-border bg-background/80 p-3 sm:flex-row sm:flex-wrap sm:items-center sm:justify-between">
          <div className="flex flex-wrap items-center gap-2">
            <Select
              value={statusFilter}
              onValueChange={(v) => setStatusFilter(v as StatusFilter)}
            >
              <SelectTrigger className="h-8 w-[120px]">
                <SelectValue placeholder="状态" />
              </SelectTrigger>
              <SelectContent>
                {STATUS_FILTER_OPTIONS.map((opt) => (
                  <SelectItem key={opt.value} value={opt.value}>
                    {opt.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={probeFilter}
              onValueChange={(v) => setProbeFilter(v as ProbeFilter)}
            >
              <SelectTrigger className="h-8 w-[130px]">
                <SelectValue placeholder="测活" />
              </SelectTrigger>
              <SelectContent>
                {PROBE_FILTER_OPTIONS.map((opt) => (
                  <SelectItem key={opt.value} value={opt.value}>
                    {opt.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder="搜索分组名 / 渠道名"
              className="h-8 w-full sm:w-56"
            />
            <span className="text-xs text-muted-foreground">
              显示 {filteredCandidates.length}/{candidates.length}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            <Button
              size="sm"
              variant="outline"
              className="h-8"
              onClick={selectAllVisible}
              disabled={visibleIDs.length === 0}
            >
              全选当前
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="h-8"
              onClick={clearSelection}
              disabled={selectedIDs.length === 0}
            >
              清除
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="h-8"
              onClick={() => void probeBatch()}
              disabled={selectedIDs.length === 0 || busy === "probe-batch"}
            >
              {busy === "probe-batch" ? (
                <RefreshCw className="size-3.5 animate-spin" />
              ) : (
                <Activity className="size-3.5" />
              )}
              批量测活 ({selectedIDs.length})
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="h-8"
              onClick={openBatchApproval}
              disabled={
                selectedApprovable.length === 0 || busy === "approve-batch"
              }
            >
              {busy === "approve-batch" ? (
                <RefreshCw className="size-3.5 animate-spin" />
              ) : (
                <ListChecks className="size-3.5" />
              )}
              批量审核 ({selectedApprovable.length})
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="h-8 text-muted-foreground"
              onClick={() => void rejectBatch()}
              disabled={
                selectedRejectable.length === 0 || busy === "reject-batch"
              }
            >
              {busy === "reject-batch" ? (
                <RefreshCw className="size-3.5 animate-spin" />
              ) : (
                <X className="size-3.5" />
              )}
              批量忽略 ({selectedRejectable.length})
            </Button>
            <Button
              size="sm"
              className="h-8"
              onClick={applySelected}
              disabled={
                selectedApplyable.length === 0 || busy === "apply-batch"
              }
            >
              {busy === "apply-batch" ? (
                <RefreshCw className="size-3.5 animate-spin" />
              ) : (
                <Play className="size-3.5" />
              )}
              批量应用 ({selectedApplyable.length})
            </Button>
          </div>
        </section>
      ) : null}

      {/* 列表 */}
      {candidates.length === 0 ? (
        <div className="rounded-2xl border border-dashed border-border bg-background/70 px-4 py-8 text-sm text-muted-foreground">
          尚未发现来源分组。点击“扫描来源分组”创建本地审核候选。
        </div>
      ) : filteredCandidates.length === 0 ? (
        <div className="rounded-2xl border border-dashed border-border bg-background/70 px-4 py-8 text-sm text-muted-foreground">
          当前过滤条件下没有候选。可切换状态或清空搜索。
        </div>
      ) : (
        <>
          {/* 桌面表格 */}
          <div className="hidden rounded-2xl border border-border bg-background/80 md:block">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-10">
                    <Checkbox
                      checked={allVisibleSelected}
                      onCheckedChange={(v) =>
                        v === true ? selectAllVisible() : deselectVisible()
                      }
                      aria-label="全选当前可见"
                    />
                  </TableHead>
                  <TableHead>来源分组</TableHead>
                  <TableHead>来源渠道</TableHead>
                  <TableHead className="w-20">倍率</TableHead>
                  <TableHead className="w-20">类型</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-28">测活</TableHead>
                  <TableHead>账号名</TableHead>
                  <TableHead>目标摘要</TableHead>
                  <TableHead className="w-[260px]">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filteredCandidates.map((candidate) => (
                  <TableRow
                    key={candidate.id}
                    data-state={
                      selectedSet.has(candidate.id) ? "selected" : undefined
                    }
                  >
                    <TableCell>
                      <Checkbox
                        checked={selectedSet.has(candidate.id)}
                        onCheckedChange={(v) =>
                          toggleSelect(candidate.id, v === true)
                        }
                        aria-label={`选择 ${candidate.source_group_name}`}
                      />
                    </TableCell>
                    <TableCell className="max-w-[180px]">
                      <div className="min-w-0">
                        <p className="truncate font-medium text-foreground">
                          {candidate.source_group_name}
                        </p>
                        {candidate.source_group_id != null ? (
                          <p className="truncate text-xs text-muted-foreground">
                            ID {candidate.source_group_id}
                          </p>
                        ) : null}
                        {errorBlock(candidate)}
                      </div>
                    </TableCell>
                    <TableCell className="max-w-[140px] truncate text-muted-foreground">
                      {candidate.source_channel_name}
                    </TableCell>
                    <TableCell className="tabular-nums">
                      {formatRatio(candidate.ratio)}
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant="outline"
                        className="border-border bg-muted/40 text-muted-foreground"
                      >
                        {candidate.channel_type || "Other"}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant="outline"
                        className={cn(
                          "border-transparent",
                          candidateStatusClass(candidate.status),
                        )}
                      >
                        {candidateStatusLabel(candidate.status)}
                      </Badge>
                    </TableCell>
                    <TableCell className="max-w-[140px]">
                      <div className="space-y-1">
                        <Badge
                          variant="outline"
                          className={cn(
                            "border-transparent",
                            probeStatusClass(candidate.probe_status),
                          )}
                          title={
                            candidate.probe_error ||
                            candidate.probe_detail ||
                            undefined
                          }
                        >
                          {probeStatusLabel(candidate.probe_status)}
                        </Badge>
                        {candidate.probe_model ? (
                          <p className="truncate text-[11px] text-muted-foreground">
                            {candidate.probe_model}
                          </p>
                        ) : null}
                        {candidate.probed_at ? (
                          <p className="truncate text-[11px] text-muted-foreground">
                            {relativeTime(candidate.probed_at)}
                            {candidate.probe_latency_ms
                              ? ` · ${candidate.probe_latency_ms}ms`
                              : ""}
                          </p>
                        ) : null}
                      </div>
                    </TableCell>
                    <TableCell className="max-w-[140px] truncate">
                      {candidate.account_name || "—"}
                    </TableCell>
                    <TableCell className="max-w-[200px]">
                      <p className="truncate text-xs text-muted-foreground">
                        {targetSummary(candidate, targetByID)}
                      </p>
                      {candidate.applied_at ? (
                        <p className="truncate text-xs text-muted-foreground">
                          应用 {relativeTime(candidate.applied_at)}
                        </p>
                      ) : null}
                    </TableCell>
                    <TableCell>{rowActions(candidate)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          {/* 移动紧凑行 */}
          <div className="space-y-2 md:hidden">
            {filteredCandidates.map((candidate) => (
              <div
                key={candidate.id}
                className={cn(
                  "rounded-xl border border-border bg-background/80 p-3",
                  selectedSet.has(candidate.id) && "bg-muted/40",
                )}
              >
                <div className="flex items-start gap-2">
                  <Checkbox
                    className="mt-1"
                    checked={selectedSet.has(candidate.id)}
                    onCheckedChange={(v) =>
                      toggleSelect(candidate.id, v === true)
                    }
                    aria-label={`选择 ${candidate.source_group_name}`}
                  />
                  <div className="min-w-0 flex-1 space-y-1.5">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <p className="truncate text-sm font-semibold">
                        {candidate.source_group_name}
                      </p>
                      <Badge
                        variant="outline"
                        className={cn(
                          "border-transparent",
                          candidateStatusClass(candidate.status),
                        )}
                      >
                        {candidateStatusLabel(candidate.status)}
                      </Badge>
                      <Badge
                        variant="outline"
                        className={cn(
                          "border-transparent",
                          probeStatusClass(candidate.probe_status),
                        )}
                        title={
                          candidate.probe_error ||
                          candidate.probe_detail ||
                          undefined
                        }
                      >
                        测活{probeStatusLabel(candidate.probe_status)}
                      </Badge>
                      <Badge
                        variant="outline"
                        className="border-border bg-muted/40 text-muted-foreground"
                      >
                        {formatRatio(candidate.ratio)}
                      </Badge>
                      <Badge
                        variant="outline"
                        className="border-border bg-background text-muted-foreground"
                      >
                        {candidate.channel_type || "Other"}
                      </Badge>
                    </div>
                    <p className="text-xs text-muted-foreground">
                      {candidate.source_channel_name}
                      {candidate.account_name
                        ? ` · 账号 ${candidate.account_name}`
                        : ""}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {targetSummary(candidate, targetByID)}
                    </p>
                    {errorBlock(candidate)}
                    {rowActions(candidate)}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </>
      )}

      {/* 审核对话框（单条 / 批量共用） */}
      <Dialog
        open={approvalOpen}
        onOpenChange={(open) => !open && closeApproval()}
      >
        <DialogContent className="max-h-[calc(100dvh-1rem)] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              {approvalMode === "batch"
                ? `批量审核映射（${approvalCandidates.length} 条）`
                : "审核来源分组映射"}
            </DialogTitle>
            <DialogDescription>
              {approvalMode === "batch" ? (
                <>
                  将为 {approvalCandidates.length}{" "}
                  条候选写入相同目标/分组/平台/并发/权重；每条账号名仍使用各自默认（
                  <code className="text-xs">account_name || source_group_name</code>
                  ）。保存审核不会写入远端。
                </>
              ) : singleCandidate ? (
                `来源 ${singleCandidate.source_channel_name} / ${singleCandidate.source_group_name}，倍率 ${formatRatio(singleCandidate.ratio)}。保存审核映射不会写入远端。`
              ) : (
                ""
              )}
            </DialogDescription>
          </DialogHeader>

          {approvalMode === "batch" ? (
            <div className="max-h-28 overflow-y-auto rounded-lg border border-border bg-muted/20 px-3 py-2 text-xs text-muted-foreground">
              {approvalCandidates.map((c) => (
                <div key={c.id} className="truncate py-0.5">
                  {c.source_group_name}
                  <span className="text-muted-foreground/70">
                    {" "}
                    · 账号将用「{defaultAccountName(c)}」
                  </span>
                </div>
              ))}
            </div>
          ) : null}

          <div className="space-y-4">
            {/* 类型预设 */}
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs text-muted-foreground">类型预设：</span>
              {TYPE_PRESETS.map((preset) => (
                <Button
                  key={preset.key}
                  type="button"
                  size="sm"
                  variant="outline"
                  className="h-7"
                  onClick={() => applyTypePreset([...preset.groupIDs])}
                >
                  {preset.label}
                  <span className="ml-1 text-xs text-muted-foreground">
                    ({preset.groupIDs.join(",")})
                  </span>
                </Button>
              ))}
              <span className="text-xs text-muted-foreground">
                仅填充分组 ID，可再改
              </span>
            </div>

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
                    {enabledTargets.map((target) => (
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
                  onChange={(event) =>
                    setApprovalForm((previous) => ({
                      ...previous,
                      platform: event.target.value,
                    }))
                  }
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
                  disabled={
                    !approvalForm.targetID || busy === "sync-target-groups"
                  }
                >
                  <RefreshCw
                    className={cn(
                      "size-3.5",
                      busy === "sync-target-groups" && "animate-spin",
                    )}
                  />
                  刷新远端分组
                </Button>
              </div>
              {!approvalForm.targetID ? (
                <p className="rounded-lg border border-dashed border-border px-3 py-3 text-sm text-muted-foreground">
                  先选择目标上游。
                </p>
              ) : activeTargetGroups.length === 0 ? (
                <p className="rounded-lg border border-dashed border-border px-3 py-3 text-sm text-muted-foreground">
                  目标分组缓存为空。点击“刷新远端分组”读取当前目标站分组。
                </p>
              ) : (
                <div className="grid gap-2 sm:grid-cols-2">
                  {activeTargetGroups.map((group) => {
                    const checked = approvalForm.targetGroupIDs.includes(
                      group.remote_group_id,
                    );
                    return (
                      <label
                        key={group.id}
                        className="flex cursor-pointer items-start gap-3 rounded-lg border border-border bg-background px-3 py-2.5"
                      >
                        <Checkbox
                          checked={checked}
                          onCheckedChange={(value) =>
                            toggleTargetGroup(
                              group.remote_group_id,
                              value === true,
                            )
                          }
                        />
                        <span className="min-w-0 text-sm">
                          <span className="block truncate font-medium text-foreground">
                            {group.name}
                          </span>
                          <span className="block text-xs text-muted-foreground">
                            {group.platform || "未分类"} · 倍率{" "}
                            {formatRatio(group.ratio)} · ID{" "}
                            {group.remote_group_id}
                          </span>
                        </span>
                      </label>
                    );
                  })}
                </div>
              )}
              {approvalForm.targetGroupIDs.length > 0 ? (
                <p className="text-xs text-muted-foreground">
                  已选分组 ID：{approvalForm.targetGroupIDs.join(", ")}
                  {activeTargetGroups.length > 0 &&
                  approvalForm.targetGroupIDs.some(
                    (id) =>
                      !activeTargetGroups.some(
                        (g) => g.remote_group_id === id,
                      ),
                  )
                    ? "（部分 ID 不在当前缓存中，保存前会由服务端校验）"
                    : ""}
                </p>
              ) : null}
            </div>

            <div
              className={cn(
                "grid gap-4",
                approvalMode === "single"
                  ? "sm:grid-cols-[minmax(0,1fr)_120px_120px]"
                  : "sm:grid-cols-2",
              )}
            >
              {approvalMode === "single" ? (
                <div className="space-y-2">
                  <Label>加入 Sub2 后的账号名称</Label>
                  <Input
                    value={approvalForm.accountName}
                    maxLength={100}
                    onChange={(event) =>
                      setApprovalForm((previous) => ({
                        ...previous,
                        accountName: event.target.value,
                      }))
                    }
                  />
                  <p className="text-xs text-muted-foreground">
                    可修改；应用后 Sub2 将使用此名称，便于和来源渠道一一对应。
                  </p>
                </div>
              ) : null}
              <div className="space-y-2">
                <Label>并发</Label>
                <Input
                  type="number"
                  min="1"
                  value={approvalForm.concurrency}
                  onChange={(event) =>
                    setApprovalForm((previous) => ({
                      ...previous,
                      concurrency: event.target.value,
                    }))
                  }
                />
              </div>
              <div className="space-y-2">
                <Label>权重</Label>
                <Input
                  type="number"
                  min="1"
                  value={approvalForm.weight}
                  onChange={(event) =>
                    setApprovalForm((previous) => ({
                      ...previous,
                      weight: event.target.value,
                    }))
                  }
                />
              </div>
            </div>

            <p className="rounded-lg border border-sky-200 bg-sky-50 px-3 py-2 text-xs leading-5 text-sky-800">
              批准时会再次读取目标分组并验证所选分组仍启用；实际 API Key
              和目标账号会在后续“应用”操作中创建或更新。预设只填表，不会自动应用。
            </p>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={closeApproval}>
              取消
            </Button>
            <Button
              onClick={() => void approve()}
              disabled={
                !approvalOpen ||
                busy === "approve-batch" ||
                (approvalMode === "single" &&
                  busy === `approve-${singleCandidate?.id}`)
              }
            >
              {(busy === "approve-batch" ||
                (approvalMode === "single" &&
                  busy === `approve-${singleCandidate?.id}`)) && (
                <RefreshCw className="size-3.5 animate-spin" />
              )}
              <Check className="size-3.5" />
              {approvalMode === "batch"
                ? `批量保存审核（${approvalCandidates.length}）`
                : "保存审核映射"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {dialog}
    </div>
  );
}
