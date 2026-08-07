package notify

import (
	"log/slog"
	"math"
	"time"

	"github.com/worryzyy/upstream-hub/internal/storage"
)

// Policy 通知去抖策略。所有字段都是面向"少烦用户"取向：
//   - BatchRateChanges：同次扫描中合并多条 rate_changed 成一条消息
//   - MinChangePct：涨跌幅小于阈值时跳过推送（仍写入 RateChangeLog 表）
//   - BalanceLowCooldown：同渠道 balance_low 在窗口内不重复发送
//   - SendMaxAttempts：单条消息最多发送尝试次数（含首发），<=1 表示不重试
type Policy struct {
	BatchRateChanges   bool
	MinChangePct       float64
	BalanceLowCooldown time.Duration
	SendMaxAttempts    int
}

// CooldownStore Dispatcher 用来判断某个 (channelID, event) 是否还在冷却窗口。
//
// 抽象成 interface 是为了让 dispatcher 不依赖具体存储；
// 生产实现是 *storage.Notifications.TryClaimCooldown（PostgreSQL UPSERT）；
// 测试时可以注入一个内存 stub。
type CooldownStore interface {
	TryClaimCooldown(channelID uint, event storage.NotificationEvent, cooldown time.Duration) (bool, error)
}

// RateChange 是一条待发送的倍率变化记录（去抖 / 合并的基本单元）。
type RateChange struct {
	GroupName string
	OldRatio  float64
	NewRatio  float64
	OldComp   float64
	NewComp   float64
	ChangedAt time.Time
}

// ChangePctAbove 涨跌幅是否达到阈值。
// minPct = 0 表示不过滤。OldRatio = 0 时按"新出现的分组"处理，永远算"达到阈值"。
func (rc RateChange) ChangePctAbove(minPct float64) bool {
	if minPct <= 0 {
		return true
	}
	if rc.OldRatio == 0 {
		return true
	}
	pct := math.Abs(rc.NewRatio-rc.OldRatio) / math.Abs(rc.OldRatio) * 100
	return pct >= minPct
}

// BuildBatchMessage 把多条 RateChange 合并成一条 notify.Message。
// 当只有 1 条时仍走这个路径，但 Subject / Body 自然退化成单条提醒。
//
// 包级函数用默认模板渲染；Dispatcher 实例方法 buildBatchMessage 用注入的用户模板。
// 保留这个包级入口是为了让 recharge_url_test.go 不依赖 Dispatcher 装配。
func BuildBatchMessage(channel *storage.Channel, changes []RateChange) Message {
	return buildBatchMessage(channel, changes, DefaultTemplates(), nil)
}

func buildBatchMessage(channel *storage.Channel, changes []RateChange, tmpls map[storage.NotificationEvent]string, log *slog.Logger) Message {
	if len(changes) == 0 {
		return Message{}
	}
	now := time.Now()
	data := RenderData{
		ChannelName: channel.Name,
		Time:        now.Format("2006-01-02 15:04"),
		Count:       len(changes),
		Changes:     make([]RenderChangeItem, 0, len(changes)),
	}
	for _, c := range changes {
		data.Changes = append(data.Changes, RenderChangeItem{
			GroupName: c.GroupName, OldRatio: c.OldRatio, NewRatio: c.NewRatio, Arrow: arrowFor(c.OldRatio, c.NewRatio),
		})
	}
	// 单条时也填顶层简写字段，供不写 range 的模板引用
	if len(changes) == 1 {
		data.GroupName = changes[0].GroupName
		data.OldRatio = changes[0].OldRatio
		data.NewRatio = changes[0].NewRatio
		data.Arrow = arrowFor(changes[0].OldRatio, changes[0].NewRatio)
	}
	subject, body := Render(storage.EventRateChanged, tmpls[storage.EventRateChanged], data, log)
	return Message{
		Event:     storage.EventRateChanged,
		ChannelID: channel.ID,
		ModelName: dataModelName(changes),
		Subject:   subject,
		Body:      AppendRechargeURL(body, channel.RechargeURL),
	}
}

// dataModelName 合并消息 ModelName 填空（订阅过滤在 Dispatcher 里按"先切片再合并"处理）。
func dataModelName(changes []RateChange) string {
	if len(changes) == 1 {
		return changes[0].GroupName
	}
	return ""
}

func arrowFor(oldV, newV float64) string {
	switch {
	case newV > oldV:
		return "上涨"
	case newV < oldV:
		return "下调"
	default:
		return "调整"
	}
}
