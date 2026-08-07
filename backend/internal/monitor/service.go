// Package monitor 周期性扫描渠道，采集余额 / 倍率并写入快照、变化日志和通知。
package monitor

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"time"

	"github.com/worryzyy/upstream-hub/internal/channel"
	"github.com/worryzyy/upstream-hub/internal/connector"
	"github.com/worryzyy/upstream-hub/internal/notify"
	"github.com/worryzyy/upstream-hub/internal/progress"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

// Service 监控扫描服务。
type Service struct {
	channels    *storage.Channels
	rates       *storage.Rates
	monitorLogs *storage.MonitorLogs
	channelSvc  *channel.Service
	dispatcher  *notify.Dispatcher
	alertStates *storage.AlertStates
	templates   map[storage.NotificationEvent]string
	log         *slog.Logger
}

func NewService(
	channels *storage.Channels,
	rates *storage.Rates,
	monitorLogs *storage.MonitorLogs,
	channelSvc *channel.Service,
	dispatcher *notify.Dispatcher,
	alertStates *storage.AlertStates,
	templates map[storage.NotificationEvent]string,
	log *slog.Logger,
) *Service {
	return &Service{
		channels:    channels,
		rates:       rates,
		monitorLogs: monitorLogs,
		channelSvc:  channelSvc,
		dispatcher:  dispatcher,
		alertStates: alertStates,
		templates:   templates,
		log:         log,
	}
}

// ScanAllBalances 扫描所有启用监控的渠道余额。单个失败不影响其他。
func (s *Service) ScanAllBalances(ctx context.Context) {
	list, err := s.channels.ListMonitorEnabled()
	if err != nil {
		s.log.Error("list channels", "err", err)
		return
	}
	for i := range list {
		c := list[i]
		if err := s.RefreshBalance(ctx, &c); err != nil {
			s.log.Warn("refresh balance failed", "channel", c.Name, "err", err)
		}
	}
}

// ScanAllRates 扫描所有启用监控的渠道倍率。
func (s *Service) ScanAllRates(ctx context.Context) {
	list, err := s.channels.ListMonitorEnabled()
	if err != nil {
		s.log.Error("list channels", "err", err)
		return
	}
	for i := range list {
		c := list[i]
		if err := s.RefreshRates(ctx, &c); err != nil {
			s.log.Warn("refresh rates failed", "channel", c.Name, "err", err)
		}
	}
}

// RefreshBalance 单个渠道余额刷新，可被 API 手动触发。
func (s *Service) RefreshBalance(ctx context.Context, c *storage.Channel) error {
	resolved, conn, session, err := s.prepare(ctx, c)
	if err != nil {
		s.notifyError(ctx, c, storage.EventLoginFailed, "登录失败", err)
		return err
	}

	progress.Start(ctx, progress.StageBalance, "拉取余额…")
	started := time.Now()
	res, err := conn.GetBalance(ctx, resolved, session)
	finished := time.Now()
	_ = s.monitorLogs.Append(&storage.MonitorLog{
		ChannelID:    c.ID,
		Job:          storage.MonitorJobBalance,
		Success:      err == nil,
		ErrorMessage: errString(err),
		StartedAt:    started,
		FinishedAt:   finished,
	})
	if err != nil {
		progress.Fail(ctx, progress.StageBalance, err.Error())
		s.notifyError(ctx, c, storage.EventMonitorFailed, "余额采集失败", err)
		return err
	}

	sampledAt := res.SampledAt
	if sampledAt.IsZero() {
		sampledAt = time.Now()
	}
	if err := s.channels.UpdateBalance(c.ID, res.Balance, &sampledAt, ""); err != nil {
		return err
	}
	_ = s.rates.AppendBalance(&storage.BalanceSnapshot{
		ChannelID: c.ID,
		Balance:   res.Balance,
		SampledAt: sampledAt,
	})
	progress.OK(ctx, progress.StageBalance, fmt.Sprintf("当前余额 %.4f", res.Balance),
		map[string]any{"balance": res.Balance})

	if c.BalanceThreshold > 0 && res.Balance < c.BalanceThreshold {
		data := notify.RenderData{
			ChannelName: c.Name,
			Balance:      res.Balance,
			Threshold:    c.BalanceThreshold,
		}
		subject, body := notify.Render(storage.EventBalanceLow, s.templates[storage.EventBalanceLow], data, s.log)
		body = notify.AppendRechargeURL(body, c.RechargeURL)
		s.dispatchAlert(ctx, c, storage.EventBalanceLow, subject, body)
	}
	return nil
}

// RefreshRates 单个渠道倍率刷新，可被 API 手动触发。
func (s *Service) RefreshRates(ctx context.Context, c *storage.Channel) error {
	resolved, conn, session, err := s.prepare(ctx, c)
	if err != nil {
		s.notifyError(ctx, c, storage.EventLoginFailed, "登录失败", err)
		return err
	}

	progress.Start(ctx, progress.StageRates, "拉取分组倍率…")
	started := time.Now()
	results, err := conn.GetRates(ctx, resolved, session)
	finished := time.Now()
	_ = s.monitorLogs.Append(&storage.MonitorLog{
		ChannelID:    c.ID,
		Job:          storage.MonitorJobRates,
		Success:      err == nil,
		ErrorMessage: errString(err),
		StartedAt:    started,
		FinishedAt:   finished,
	})
	if err != nil {
		progress.Fail(ctx, progress.StageRates, err.Error())
		s.notifyError(ctx, c, storage.EventMonitorFailed, "倍率采集失败", err)
		return err
	}

	now := time.Now()
	changes := make([]notify.RateChange, 0, len(results))
	for _, r := range results {
		prev, err := s.rates.Upsert(&storage.RateSnapshot{
			ChannelID:       c.ID,
			ModelName:       r.ModelName,
			Description:     r.Description,
			Ratio:           r.Ratio,
			CompletionRatio: r.CompletionRatio,
			LastSeenAt:      now,
		})
		if err != nil {
			s.log.Warn("rate upsert failed", "channel", c.Name, "model", r.ModelName, "err", err)
			continue
		}
		if prev == nil {
			continue
		}
		if prev.Ratio == r.Ratio && prev.CompletionRatio == r.CompletionRatio {
			continue
		}
		oldRatio := prev.Ratio
		oldComp := prev.CompletionRatio
		_ = s.rates.AppendChange(&storage.RateChangeLog{
			ChannelID:          c.ID,
			ModelName:          r.ModelName,
			OldRatio:           &oldRatio,
			NewRatio:           r.Ratio,
			OldCompletionRatio: &oldComp,
			NewCompletionRatio: r.CompletionRatio,
			ChangedAt:          now,
		})
		changes = append(changes, notify.RateChange{
			GroupName: r.ModelName,
			OldRatio:  oldRatio,
			NewRatio:  r.Ratio,
			OldComp:   oldComp,
			NewComp:   r.CompletionRatio,
			ChangedAt: now,
		})
	}
	// 一次扫描的所有变化打包推送：去抖策略（合并 / 涨跌幅过滤）由 Dispatcher.Policy 决定。
	if len(changes) > 0 {
		_ = s.dispatcher.DispatchRateBatch(ctx, c, changes)
	}
	progress.OK(ctx, progress.StageRates, fmt.Sprintf("拉到 %d 个分组", len(results)),
		map[string]any{"count": len(results)})
	return nil
}

func (s *Service) prepare(ctx context.Context, c *storage.Channel) (*connector.Channel, connector.Connector, *connector.AuthSession, error) {
	resolved, err := s.channelSvc.Resolve(ctx, c)
	if err != nil {
		return nil, nil, nil, err
	}
	conn, err := connector.For(resolved.Type)
	if err != nil {
		return nil, nil, nil, err
	}
	session, err := s.channelSvc.EnsureSession(ctx, c, resolved, conn)
	if err != nil {
		return nil, nil, nil, err
	}
	return resolved, conn, session, nil
}

func (s *Service) notifyError(ctx context.Context, c *storage.Channel, event storage.NotificationEvent, title string, err error) {
	data := notify.RenderData{
		ChannelName: c.Name,
		Title:        title,
		Error:        err.Error(),
	}
	subject, body := notify.Render(event, s.templates[event], data, s.log)
	body = notify.AppendRechargeURL(body, c.RechargeURL)
	s.dispatchAlert(ctx, c, event, subject, body)
}

// dispatchAlert 发一条带告警 ID 的通知，并落库 AlertState（pending）。
// alertID 写进 msg.Extra，飞书 app 卡片的按钮 value 回带它，回调端点据此定位记录。
// 已处理静默窗内的告警会被 Dispatcher 跳过（见 dispatcher.suppressByAlertState），
// 此时不再落库新记录，避免静默期间堆积无意义 pending。
func (s *Service) dispatchAlert(ctx context.Context, c *storage.Channel, event storage.NotificationEvent, subject, body string) {
	alertID := newAlertID()
	msg := notify.Message{
		Event:     event,
		ChannelID: c.ID,
		Subject:   subject,
		Body:      body,
		Extra: map[string]any{
			"alert_id":   alertID,
			"channel_id": c.ID,
		},
	}
	if err := s.dispatcher.Dispatch(ctx, msg); err != nil && s.log != nil {
		s.log.Warn("dispatch alert", "event", event, "channel", c.Name, "err", err)
	}
	// 落库 pending：飞书 app 卡片成功发送后，Dispatcher 会把飞书 message_id 回填到该记录。
	// 静默窗内 Dispatch 跳过的情况，这条 pending 没有卡片可点，无副作用（LatestHandled 只查
	// handled 状态，pending 不影响静默判断）；保留落库以保持链路简单。
	s.recordAlertState(alertID, c.ID, event)
}

// recordAlertState 落库一条 pending 告警状态。失败仅记日志，不阻断告警链路。
func (s *Service) recordAlertState(alertID string, channelID uint, event storage.NotificationEvent) {
	if s.alertStates == nil {
		return
	}
	if err := s.alertStates.Create(&storage.AlertState{
		AlertID:   alertID,
		ChannelID: channelID,
		Event:     event,
		Status:    storage.AlertStatusPending,
	}); err != nil && s.log != nil {
		s.log.Warn("create alert state", "alert_id", alertID, "err", err)
	}
}

// newAlertID 生成一个 UUIDv4 字符串（不引外部依赖，用 crypto/rand）。
func newAlertID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
