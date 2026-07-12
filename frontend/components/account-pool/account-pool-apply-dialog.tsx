import { AlertTriangle, Loader2 } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import type { Sub2PoolPriorityPreviewResponse } from "@/lib/api-types"

interface AccountPoolApplyDialogProps {
  open: boolean
  targetName?: string | null
  preview: Sub2PoolPriorityPreviewResponse | null
  applying: boolean
  conflictMessage?: string | null
  onOpenChange: (open: boolean) => void
  onApply: () => void
}

export function AccountPoolApplyDialog({
  open,
  targetName,
  preview,
  applying,
  conflictMessage,
  onOpenChange,
  onApply,
}: AccountPoolApplyDialogProps) {
  const summary = preview?.summary
  const changedItems =
    preview?.items.filter(
      (item) =>
        item.target_priority != null &&
        item.before_priority != null &&
        item.before_priority !== item.target_priority,
    ) ?? []
  const skippedItems = preview?.items.filter((item) => Boolean(item.skip_reason)) ?? []

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="rounded-lg sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>应用优先级预览</DialogTitle>
          <DialogDescription>
            该操作会把后端预览结果写入 {targetName || "当前目标"}。前端不会重新排序或自行计算目标优先级。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3 text-sm">
          <div className="grid grid-cols-3 gap-2">
            <div className="rounded-lg border border-border bg-muted/20 px-3 py-2">
              <div className="text-[11px] text-muted-foreground">总项</div>
              <div className="mt-1 font-semibold">{summary?.total ?? preview?.items.length ?? 0}</div>
            </div>
            <div className="rounded-lg border border-border bg-muted/20 px-3 py-2">
              <div className="text-[11px] text-muted-foreground">修改</div>
              <div className="mt-1 font-semibold">{summary?.changed ?? 0}</div>
            </div>
            <div className="rounded-lg border border-border bg-muted/20 px-3 py-2">
              <div className="text-[11px] text-muted-foreground">跳过</div>
              <div className="mt-1 font-semibold">{summary?.skipped ?? 0}</div>
            </div>
          </div>

          <div className="rounded-lg border border-border bg-muted/20 px-3 py-2">
            <div className="text-[11px] text-muted-foreground">snapshot_signature</div>
            <div className="mt-1 break-all font-mono text-xs">{preview?.snapshot_signature ?? "—"}</div>
          </div>

          <div className="max-h-56 space-y-2 overflow-y-auto rounded-lg border border-border p-2">
            {changedItems.map((item) => (
              <div
                key={item.account_id}
                className="flex items-center justify-between gap-3 rounded-md bg-muted/30 px-2 py-1.5 text-xs"
              >
                <div className="min-w-0">
                  <div className="truncate font-medium">{item.account_name}</div>
                  <div className="text-[10px] text-muted-foreground">#{item.account_id}</div>
                </div>
                <div className="shrink-0 font-mono tabular-nums">
                  {item.before_priority ?? "—"} {"->"} {item.target_priority ?? "—"}
                </div>
              </div>
            ))}
            {changedItems.length === 0 ? (
              <div className="px-2 py-3 text-center text-xs text-muted-foreground">
                没有需要写入的优先级修改。
              </div>
            ) : null}
          </div>

          {skippedItems.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {skippedItems.slice(0, 8).map((item) => (
                <Badge key={item.account_id} variant="outline" className="rounded-md text-[10px]">
                  #{item.account_id} {item.skip_reason}
                </Badge>
              ))}
              {skippedItems.length > 8 ? (
                <Badge variant="outline" className="rounded-md text-[10px]">
                  另有 {skippedItems.length - 8} 个跳过项
                </Badge>
              ) : null}
            </div>
          ) : null}

          <div className="rounded-lg border border-warning/25 bg-warning/10 px-3 py-2 text-xs text-warning">
            <div className="flex gap-2">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              <div>
                签名用于证明 Apply 对应的是刚才那次预览快照。若账号池在预览后被刷新、
                其他人写入或后端重新生成排序，后端应返回快照冲突（409），并且不应用任何修改。
              </div>
            </div>
          </div>

          {conflictMessage ? (
            <div className="rounded-lg border border-danger/25 bg-danger/10 px-3 py-2 text-xs text-danger">
              {conflictMessage}
            </div>
          ) : null}
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={applying}>
            取消
          </Button>
          <Button
            type="button"
            variant="destructive"
            onClick={onApply}
            disabled={
              !preview ||
              applying ||
              Boolean(conflictMessage) ||
              (summary?.changed ?? 0) === 0 ||
              (preview?.guards?.length ?? 0) > 0
            }
            className="gap-1.5"
          >
            {applying ? <Loader2 className="size-3.5 animate-spin" /> : null}
            确认 Apply
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
