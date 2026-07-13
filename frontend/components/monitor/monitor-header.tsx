import { useEffect, useMemo, useState } from "react"
import { useLocation, useNavigate } from "react-router-dom"
import { useTheme } from "next-themes"
import { Activity, Github, Home, LogOut, RefreshCw, Sun, Moon, Settings, UsersRound, MoreHorizontal } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { useAuth } from "@/lib/auth-context"
import { apiFetch } from "@/lib/api"
import { useTriggerRefresh } from "@/lib/refresh-context"
import { useAppVersion, useChannels } from "@/lib/queries"
import type { AppVersion } from "@/lib/api-types"
import { relativeTime } from "@/lib/format"
import { toast } from "sonner"

export function MonitorHeader() {
  const navigate = useNavigate()
  const location = useLocation()
  const { theme, setTheme } = useTheme()
  const { username, authDisabled, logout } = useAuth()
  const refresh = useTriggerRefresh()
  const channels = useChannels()
  const appVersion = useAppVersion()
  const [mounted, setMounted] = useState(false)
  const [syncing, setSyncing] = useState(false)
  const [checkingVersion, setCheckingVersion] = useState(false)

  const appTitle = appVersion.data?.title?.trim() || "UpstreamOps"
  const version = appVersion.data?.version?.trim()
  const latestVersion = appVersion.data?.latest_version?.trim()
  const updateAvailable = Boolean(appVersion.data?.update_available && latestVersion)
  const updateURL = appVersion.data?.release_url?.trim() || appVersion.data?.repo_url?.trim()

  useEffect(() => setMounted(true), [])

  useEffect(() => {
    document.title = appTitle
  }, [appTitle])

  /**
   * 找出所有渠道中最近一次采集时间——这是"上次采集"展示的依据，
   * 让用户知道页面上的余额到底是多新的快照（区别于"我刚点了刷新"）。
   */
  const lastCollectedAt = useMemo(() => {
    const list = channels.data ?? []
    let best: string | null = null
    let bestT = -Infinity
    for (const c of list) {
      if (!c.last_balance_at) continue
      const t = new Date(c.last_balance_at).getTime()
      if (Number.isFinite(t) && t > bestT) {
        bestT = t
        best = c.last_balance_at
      }
    }
    return best
  }, [channels.data])

  function handleRefresh() {
    setSyncing(true)
    refresh()
    setTimeout(() => setSyncing(false), 800)
  }

  async function handleCheckVersion() {
    setCheckingVersion(true)
    try {
      const result = await apiFetch<AppVersion>("/version?force=1")
      appVersion.setData(result)
      if (result.update_error) {
        toast.error(result.update_error)
      } else if (result.update_available && result.latest_version) {
        toast.warning(`发现新版本 ${result.latest_version}`)
      } else {
        toast.success("当前已是最新版本")
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "检测更新失败")
    } finally {
      setCheckingVersion(false)
    }
  }

  return (
    <header className="sticky top-0 z-30 border-b border-border bg-background/92 backdrop-blur-xl">
      <div className="mx-auto flex h-14 max-w-[1280px] items-center justify-between gap-2 px-3 sm:px-5 xl:px-6">
        {/* left: logo + title */}
        <div className="flex min-w-0 items-center gap-2.5">
          <div className="flex size-8 shrink-0 items-center justify-center rounded-lg border border-foreground/10 bg-foreground text-background shadow-[var(--shadow-card)]">
            <Activity className="size-4" strokeWidth={2.25} />
          </div>
          <div className="min-w-0">
            <h1 className="max-w-32 truncate text-sm font-semibold text-foreground sm:max-w-48 sm:text-[15px]">{appTitle}</h1>
            {version ? (
              <p className="truncate text-[11px] leading-3 text-muted-foreground">
                <button
                  type="button"
                  className="font-medium underline-offset-2 hover:text-foreground hover:underline"
                  onClick={handleCheckVersion}
                  disabled={checkingVersion}
                  title="点击检测更新"
                >
                  {checkingVersion ? "检测中..." : `v${version}`}
                </button>
                {updateAvailable ? (
                  <a
                    href={updateURL || "https://github.com/bejix/upstream-ops"}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="ml-2 font-medium text-emerald-600 underline-offset-2 hover:text-emerald-700 hover:underline"
                  >
                    有新版本 {latestVersion}
                  </a>
                ) : null}
              </p>
            ) : null}
          </div>
        </div>

        {/* right: actions */}
        <div className="flex shrink-0 items-center gap-1">
          {/* last collected + refresh */}
          <div className="mr-1 hidden items-center gap-2 lg:flex">
            <span className="text-xs text-muted-foreground">
              {"上次采集 "}
              <span className="font-medium text-foreground">{relativeTime(lastCollectedAt)}</span>
            </span>
            <Tooltip delayDuration={200}>
              <TooltipTrigger asChild>
                <Button
                  variant="outline"
                  size="icon-sm"
                  onClick={handleRefresh}
                  disabled={syncing}
                  className="border-border bg-card text-foreground shadow-none hover:bg-muted"
                  aria-label="刷新视图"
                >
                  <RefreshCw className={cn("size-3.5", syncing && "animate-spin")} />
                </Button>
              </TooltipTrigger>
              <TooltipContent side="bottom" className="max-w-xs text-xs">
                <p>{"重新拉取最新的快照数据。"}</p>
                <p className="mt-1 text-muted-foreground">
                  {"提示：实际采集由后台定时任务执行，如需立即采集请到具体渠道点 \"同步\"。"}
                </p>
              </TooltipContent>
            </Tooltip>
          </div>

          <Tooltip delayDuration={200}>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={handleRefresh}
                disabled={syncing}
                className="text-muted-foreground hover:text-foreground lg:hidden"
                aria-label="刷新视图"
              >
                <RefreshCw className={cn("size-3.5", syncing && "animate-spin")} />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="bottom" className="text-xs">
              {"刷新视图"}
            </TooltipContent>
          </Tooltip>

          <Tooltip delayDuration={200}>
            <TooltipTrigger asChild>
              <Button
                variant={location.pathname === "/" ? "secondary" : "ghost"}
                size="icon-sm"
                onClick={() => navigate("/")}
                className="text-muted-foreground shadow-none hover:text-foreground"
                aria-label="主页"
              >
                <Home className="size-3.5" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="bottom" className="text-xs">
              {"主页"}
            </TooltipContent>
          </Tooltip>

          <Tooltip delayDuration={200}>
            <TooltipTrigger asChild>
              <Button
                variant={location.pathname === "/account-pool" ? "secondary" : "outline"}
                size="icon-sm"
                onClick={() => navigate("/account-pool")}
                className="border-0 text-muted-foreground shadow-none hover:text-foreground"
                aria-label="Sub2 账号池"
              >
                <UsersRound className="size-3.5" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="bottom" className="text-xs">
              {"Sub2 账号池"}
            </TooltipContent>
          </Tooltip>

          {/* settings */}
          <Tooltip delayDuration={200}>
            <TooltipTrigger asChild>
              <Button
                variant={location.pathname === "/settings" ? "secondary" : "ghost"}
                size="icon-sm"
                onClick={() => navigate("/settings")}
                className="text-muted-foreground shadow-none hover:text-foreground"
                aria-label="系统设置"
              >
                <Settings className="size-3.5" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="bottom" className="text-xs">
              {"系统设置"}
            </TooltipContent>
          </Tooltip>

          <div className="hidden items-center gap-1 md:flex">
            <Tooltip delayDuration={200}>
              <TooltipTrigger asChild>
                <Button
                  asChild
                  variant="ghost"
                  size="icon-sm"
                  className="text-muted-foreground shadow-none hover:text-foreground"
                  aria-label="GitHub 仓库"
                >
                  <a
                    href="https://github.com/bejix/upstream-ops"
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    <Github className="size-3.5" />
                  </a>
                </Button>
              </TooltipTrigger>
              <TooltipContent side="bottom" className="text-xs">
                {"GitHub · bejix/upstream-ops"}
              </TooltipContent>
            </Tooltip>

            <Tooltip delayDuration={200}>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
                  className="text-muted-foreground shadow-none hover:text-foreground"
                  aria-label="切换主题"
                >
                  {mounted && theme === "dark" ? (
                    <Moon className="size-3.5" />
                  ) : (
                    <Sun className="size-3.5" />
                  )}
                </Button>
              </TooltipTrigger>
              <TooltipContent side="bottom" className="text-xs">
                {mounted && theme === "dark" ? "深色模式 · 点击切换浅色" : "浅色模式 · 点击切换深色"}
              </TooltipContent>
            </Tooltip>

            {authDisabled ? null : (
              <Tooltip delayDuration={200}>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={logout}
                    className="text-muted-foreground shadow-none hover:text-foreground"
                    aria-label="退出登录"
                  >
                    <LogOut className="size-3.5" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent side="bottom" className="text-xs">
                  {username ? `${username} · 退出登录` : "退出登录"}
                </TooltipContent>
              </Tooltip>
            )}
          </div>

          <DropdownMenu>
            <Tooltip delayDuration={200}>
              <TooltipTrigger asChild>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    className="text-muted-foreground shadow-none hover:text-foreground md:hidden"
                    aria-label="更多操作"
                  >
                    <MoreHorizontal className="size-4" />
                  </Button>
                </DropdownMenuTrigger>
              </TooltipTrigger>
              <TooltipContent side="bottom" className="text-xs">
                {"更多操作"}
              </TooltipContent>
            </Tooltip>
            <DropdownMenuContent align="end" className="w-48">
              <DropdownMenuItem asChild>
                <a
                  href="https://github.com/bejix/upstream-ops"
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  <Github className="size-3.5" />
                  GitHub 仓库
                </a>
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => setTheme(theme === "dark" ? "light" : "dark")}>
                {mounted && theme === "dark" ? <Moon className="size-3.5" /> : <Sun className="size-3.5" />}
                {mounted && theme === "dark" ? "切换浅色模式" : "切换深色模式"}
              </DropdownMenuItem>
              {authDisabled ? null : (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onSelect={logout}>
                    <LogOut className="size-3.5" />
                    {username ? `${username} · 退出` : "退出登录"}
                  </DropdownMenuItem>
                </>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
    </header>
  )
}
