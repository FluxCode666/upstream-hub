// Package notify 通知模板渲染。
//
// 通知文案原本硬编码在 monitor/service.go 和 notify/policy.go 里。这里抽出一个
// 受控的模板层：5 种事件各一套默认模板（零配置等价于现状），用户可在设置页覆盖。
//
// 设计取舍：
//   - 用 Go text/template，占位符是 {{.Xxx}} 形式，白名单受控（见 RenderData 各字段）
//   - rate_changed 模板用 {{range .Changes}} 统一处理单条 / 合并多条，保证用户改一处
//     模板对所有 rate_changed 消息（逐条或合并）都生效，行为一致
//   - 渲染失败（用户模板写错语法）不吞掉告警，回落到默认模板并记 warn
//   - 渲染产出 Subject + Body 两个字段；充值链接仍由 AppendRechargeURL 在调用方追加，
//     模板里不写充值链接，避免重复
package notify

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"text/template"
	"time"

	"github.com/worryzyy/upstream-hub/internal/storage"
)

// subjectSep 分隔模板里的 Subject 与 Body。选 \f（form feed）是因为通知正文里
// 不会出现它，避免与用户文案冲突。
const subjectSep = "\f"

// RenderChangeItem rate_changed 合并消息里的一条分组变化。
type RenderChangeItem struct {
	GroupName string
	OldRatio  float64
	NewRatio  float64
	Arrow     string
}

// RenderData 传给模板的受控变量集合。所有字段都是非敏感的展示数据。
//
// 不同事件只填各自相关的字段，其余留零值。
//   - balance_low：ChannelName / Balance / Threshold
//   - rate_changed：ChannelName / Count + Changes（单条时 Count=1, Changes 长度 1
//     且顶层 GroupName/OldRatio/NewRatio/Arrow 也填一份，便于模板不写 range 的简写）
//   - login_failed / monitor_failed / captcha_failed：ChannelName / Error
type RenderData struct {
	ChannelName string
	ChannelID  uint
	Time       string

	// balance_low
	Balance   float64
	Threshold float64

	// rate_changed
	Count   int
	Changes []RenderChangeItem

	// 单条 rate_changed 时也填这组，供简写模板引用
	GroupName string
	OldRatio  float64
	NewRatio  float64
	Arrow     string

	// login_failed / monitor_failed / captcha_failed
	Title string
	Error string
}

// defaultTemplates 5 种事件的内置默认模板，与改造前硬编码文案等价，保证零配置向后兼容。
//
// rate_changed 模板用 Count>1 分支：单条走简写格式，多条走 range 列出每条变化，
// 输出与原 policy.go::BuildBatchMessage 的单条 / 合并两路一致。
func defaultTemplates() map[storage.NotificationEvent]string {
	return map[storage.NotificationEvent]string{
		storage.EventBalanceLow: "[upstream-hub] {{.ChannelName}} 余额低于阈值" + subjectSep +
			"当前余额: {{printf \"%.4f\" .Balance}}，阈值: {{printf \"%.4f\" .Threshold}}",

		storage.EventRateChanged: `【倍率变化提醒】{{.ChannelName}} · {{if gt .Count 1}}{{.Count}} 个分组变动{{else}}{{.GroupName}}{{end}}` + subjectSep +
			`渠道：{{.ChannelName}}
{{- if gt .Count 1}}
共 {{.Count}} 个分组倍率变化：
{{- range .Changes}}
  · {{.GroupName}}：{{printf "%g" .OldRatio}} {{.Arrow}}至 {{printf "%g" .NewRatio}}
{{- end}}
时间：{{.Time}}
{{- else}}
分组倍率：{{.GroupName}} 由 {{printf "%g" .OldRatio}} {{.Arrow}}至 {{printf "%g" .NewRatio}}
变化时间：{{.Time}}
{{- end}}`,

		storage.EventLoginFailed: "[upstream-hub] {{.ChannelName}} {{if .Title}}{{.Title}}{{else}}登录失败{{end}}" + subjectSep +
			"{{.Error}}",

		storage.EventMonitorFailed: "[upstream-hub] {{.ChannelName}} {{if .Title}}{{.Title}}{{else}}监控异常{{end}}" + subjectSep +
			"{{.Error}}",

		storage.EventCaptchaFailed: "[upstream-hub] {{.ChannelName}} {{if .Title}}{{.Title}}{{else}}验证码失败{{end}}" + subjectSep +
			"{{.Error}}",
	}
}

// DefaultTemplates 返回默认模板的副本，供设置页"恢复默认"和内部 fallback 使用。
func DefaultTemplates() map[storage.NotificationEvent]string {
	src := defaultTemplates()
	out := make(map[storage.NotificationEvent]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// Templates 合并用户覆盖的模板与默认模板：用户填了的覆盖默认，没填的用默认。
// userTmpl 为空 map / nil 时等价于全默认。
func Templates(userTmpl map[storage.NotificationEvent]string) map[storage.NotificationEvent]string {
	merged := DefaultTemplates()
	for k, v := range userTmpl {
		if strings.TrimSpace(v) != "" {
			merged[k] = v
		}
	}
	return merged
}

// Render 把模板渲染成 Subject + Body。tmpl 为空字符串时回落默认。
// 渲染失败时 fallback 到默认模板再试一次；默认也失败则返回朴素兜底（不应发生）。
//
// log 非空时，用户模板语法错误会记一条 warn。
func Render(event storage.NotificationEvent, tmpl string, data RenderData, log *slog.Logger) (subject, body string) {
	if data.Time == "" {
		data.Time = time.Now().Format("2006-01-02 15:04")
	}
	subject, body, err := renderOnce(event, tmpl, data)
	if err == nil {
		return subject, body
	}
	if log != nil {
		log.Warn("notify template render failed, falling back to default",
			"event", event, "err", err)
	}
	def := defaultTemplates()[event]
	subject, body, err = renderOnce(event, def, data)
	if err != nil {
		return fmt.Sprintf("[upstream-hub] %s", event), data.ChannelName
	}
	return subject, body
}

func renderOnce(event storage.NotificationEvent, tmpl string, data RenderData) (string, string, error) {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		return "", "", fmt.Errorf("empty template")
	}
	t, err := template.New("notify").Parse(tmpl)
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", "", err
	}
	out := buf.String()
	if i := strings.Index(out, subjectSep); i >= 0 {
		return strings.TrimRight(out[:i], "\n"), strings.TrimSpace(out[i+len(subjectSep):]), nil
	}
	// 模板里没有分隔符：整段当作 Body，Subject 用事件兜底。
	return fmt.Sprintf("[upstream-hub] %s", event), strings.TrimSpace(out), nil
}
