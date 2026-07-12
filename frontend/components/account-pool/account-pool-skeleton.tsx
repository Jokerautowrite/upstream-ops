import { LoaderCircle } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"

export function AccountPoolSkeleton() {
  return (
    <div className="space-y-3">
      <div className="flex items-start gap-3 rounded-lg border border-border bg-card px-4 py-3">
        <LoaderCircle className="mt-0.5 size-4 shrink-0 animate-spin text-muted-foreground" />
        <div className="min-w-0">
          <p className="text-sm font-medium text-foreground">正在读取 Sub2 账号池</p>
          <p className="mt-1 text-xs leading-5 text-muted-foreground">
            正在核对账号、上游 Key、倍率和余额。首次加载可能需要十几秒，请保持当前页面。
          </p>
        </div>
      </div>

      <div className="rounded-lg border border-border bg-card p-3 shadow-sm sm:p-4">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-end lg:justify-between">
          <div className="space-y-2">
            <Skeleton className="h-5 w-40" />
            <Skeleton className="h-9 w-full sm:w-72" />
            <Skeleton className="h-4 w-52" />
          </div>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-5 lg:w-[660px]">
            {Array.from({ length: 5 }).map((_, index) => (
              <Skeleton key={index} className="h-17 rounded-lg" />
            ))}
          </div>
        </div>
      </div>

      <Skeleton className="h-24 rounded-lg" />

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <Skeleton className="h-72 rounded-lg" />
        <Skeleton className="h-72 rounded-lg" />
      </div>

      <div className="hidden rounded-lg border border-border bg-card p-3 lg:block">
        {Array.from({ length: 8 }).map((_, index) => (
          <Skeleton key={index} className="mb-2 h-10 rounded-md last:mb-0" />
        ))}
      </div>

      <div className="space-y-2 lg:hidden">
        {Array.from({ length: 4 }).map((_, index) => (
          <Skeleton key={index} className="h-52 rounded-lg" />
        ))}
      </div>
    </div>
  )
}
