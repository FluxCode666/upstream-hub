"use client"

import { Outlet } from "react-router-dom"
import { MonitorHeader } from "@/components/monitor/monitor-header"
import { DockBar } from "@/components/monitor/dock-bar"

/**
 * AppShell 是所有路由共享的外壳：顶部 header + 中间 Outlet（+ 可选底部 dock）。
 *
 * 当前 Dock 已开启：底部导航直达 监控面板 / 添加渠道 / 打码平台 / 通知渠道 / 系统设置。
 * 通知渠道页（飞书 app 模式）与系统设置页（通知模板）从这里进入。
 */
const SHOW_DOCK = true

export function AppShell() {
  return (
    <div className="min-h-screen bg-background">
      <MonitorHeader />
      <main
        className={
          SHOW_DOCK
            ? "mx-auto max-w-360 space-y-5 px-5 py-5 pb-24"
            : "mx-auto max-w-360 space-y-5 px-5 py-5"
        }
      >
        <Outlet />
      </main>
      {SHOW_DOCK ? <DockBar /> : null}
    </div>
  )
}
