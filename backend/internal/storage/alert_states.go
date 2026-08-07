package storage

import (
	"time"

	"gorm.io/gorm"
)

// AlertStates 告警处理状态仓库。
type AlertStates struct{ db *gorm.DB }

func NewAlertStates(db *gorm.DB) *AlertStates { return &AlertStates{db: db} }

func (a *AlertStates) Create(s *AlertState) error { return a.db.Create(s).Error }

// FindByAlertID 按 AlertID 取记录。
func (a *AlertStates) FindByAlertID(id string) (*AlertState, error) {
	var s AlertState
	if err := a.db.First(&s, "alert_id = ?", id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdateStatus 把一条告警标记为终态，并记处理人 / 时间。已终态时幂等返回当前状态。
func (a *AlertStates) UpdateStatus(alertID string, status AlertStatus, handledBy string) (*AlertState, error) {
	var s AlertState
	if err := a.db.First(&s, "alert_id = ?", alertID).Error; err != nil {
		return nil, err
	}
	if s.Status == AlertStatusHandled || s.Status == AlertStatusIgnored {
		return &s, nil
	}
	now := time.Now()
	s.Status = status
	s.HandledBy = handledBy
	s.HandledAt = &now
	if err := a.db.Save(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// SetFeishuMessageID 回填飞书消息 ID（发卡片成功后调用，回调就地更新卡片时需要）。
// alertID 不存在时静默忽略。
func (a *AlertStates) SetFeishuMessageID(alertID, messageID string) error {
	return a.db.Model(&AlertState{}).Where("alert_id = ?", alertID).
		Update("feishu_message_id", messageID).Error
}

// LatestHandled 在 (channelID, event) 维度找最近一条 handled 记录。
// 用于 Dispatcher 的"已处理静默窗"判断：若最近已处理时间足够新就跳过推送。
// 没有命中记录返回 (nil, nil)。
func (a *AlertStates) LatestHandled(channelID uint, event NotificationEvent) (*AlertState, error) {
	var s AlertState
	err := a.db.Where("channel_id = ? AND event = ? AND status = ?", channelID, event, AlertStatusHandled).
		Order("handled_at DESC").First(&s).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}
