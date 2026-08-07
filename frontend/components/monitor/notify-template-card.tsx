"use client"

import { useEffect, useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { apiFetch } from "@/lib/api"
import { useNotifyTemplates } from "@/lib/queries"
import type { NotifyTemplates, NotificationEvent } from "@/lib/api-types"

const eventOrder: { key: NotificationEvent; label: string; vars: string }[] = [
  {
    key: "balance_low",
    label: "余额低于阈值",
    vars: "可用：{{.ChannelName}} {{.Balance}} {{.Threshold}} {{.Time}}",
  },
  {
    key: "rate_changed",
    label: "倍率变化",
    vars: "可用：{{.ChannelName}} {{.GroupName}} {{.OldRatio}} {{.NewRatio}} {{.Arrow}} {{.Count}} {{.Time}}",
  },
  {
    key: "login_failed",
    label: "登录失败",
    vars: "可用：{{.ChannelName}} {{.Title}} {{.Error}} {{.Time}}",
  },
  {
    key: "monitor_failed",
    label: "采集失败",
    vars: "可用：{{.ChannelName}} {{.Title}} {{.Error}} {{.Time}}",
  },
  {
    key: "captcha_failed",
    label: "验证码失败",
    vars: "可用：{{.ChannelName}} {{.Title}} {{.Error}} {{.Time}}",
  },
]

// subjectSep 与后端 notify/template.go 的 subjectSep 一致（\f）。
// Subject 与 Body 用 \f 分隔；不写分隔符时整段作为 Body。
const SEPARATOR_HINT = "用 \\f 把标题和正文分开；不写则整段作为正文。充值链接会自动追加，无需在模板里写。"

export function NotifyTemplateCard() {
  const { data, loading } = useNotifyTemplates()
  const [editing, setEditing] = useState<NotifyTemplates | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (data) setEditing({ ...data.current })
  }, [data])

  if (loading || !editing) {
    return (
      <Card className="border border-border shadow-none">
        <CardHeader className="pb-2">
          <CardTitle className="text-base font-semibold">通知模板</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-xs text-muted-foreground">加载中…</p>
        </CardContent>
      </Card>
    )
  }

  function resetToDefaults() {
    if (!data) return
    setEditing({ ...data.defaults })
  }

  async function handleSave() {
    if (!editing) return
    setSaving(true)
    try {
      await apiFetch("/settings/notify-templates", {
        method: "PUT",
        body: JSON.stringify(editing),
      })
      toast.success("模板已保存，需重启后端生效")
    } catch (e) {
      const err = e as Error
      toast.error(err.message || "保存失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card className="border border-border shadow-none">
      <CardHeader className="flex flex-row items-center justify-between pb-2">
        <CardTitle className="text-base font-semibold">通知模板</CardTitle>
        <div className="flex gap-1">
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="h-7 text-xs"
            onClick={resetToDefaults}
            disabled={saving}
          >
            恢复默认
          </Button>
          <Button
            type="button"
            size="sm"
            className="h-7 text-xs"
            onClick={handleSave}
            disabled={saving}
          >
            {saving ? "保存中…" : "保存"}
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-[11px] text-muted-foreground">{SEPARATOR_HINT}</p>
        {eventOrder.map(({ key, label, vars }) => (
          <div key={key} className="space-y-1.5">
            <Label htmlFor={`tmpl-${key}`} className="text-xs font-medium">
              {label}
            </Label>
            <Textarea
              id={`tmpl-${key}`}
              rows={3}
              value={editing[key]}
              onChange={(e) => setEditing({ ...editing, [key]: e.target.value })}
              disabled={saving}
              className="font-mono text-xs"
            />
            <p className="text-[11px] text-muted-foreground">{vars}</p>
          </div>
        ))}
      </CardContent>
    </Card>
  )
}
