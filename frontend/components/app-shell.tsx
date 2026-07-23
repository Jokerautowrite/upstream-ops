"use client"

import { Outlet } from "react-router-dom"
import { MonitorHeader } from "@/components/monitor/monitor-header"
import { DockBar } from "@/components/monitor/dock-bar"

/**
 * AppShell 是所有路由共享的外壳：顶部 header + 中间 Outlet（+ 可选底部 dock）。
 *
 * 当前 Dock 暂时隐藏 —— 单用户 / 少量数据下单页布局比拆页好。
 * 把 SHOW_DOCK 改成 true 即可恢复底部导航 + 路由跳转。
 */
const SHOW_DOCK = false

export function AppShell() {
  return (
    <div className="min-h-screen bg-background">
      <MonitorHeader />
      <main
        className={
          SHOW_DOCK
            ? "mx-auto max-w-[120rem] space-y-4 px-3 py-3 pb-24 sm:space-y-5 sm:px-6 sm:py-5 lg:px-8"
            : "mx-auto max-w-[120rem] space-y-4 px-3 py-3 sm:space-y-5 sm:px-6 sm:py-5 lg:px-8"
        }
      >
        <Outlet />
      </main>
      {SHOW_DOCK ? <DockBar /> : null}
    </div>
  )
}
