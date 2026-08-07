package storage

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 通知模板在 app_settings 表里的 key。
const settingKeyNotifyTemplates = "notify_templates"

// Settings 通用 KV 设置仓库。当前承载通知模板。
type Settings struct{ db *gorm.DB }

func NewSettings(db *gorm.DB) *Settings { return &Settings{db: db} }

// Get 读取一个 key 的值；不存在返回 ("", nil)。
func (s *Settings) Get(key string) (string, error) {
	var row AppSetting
	err := s.db.First(&row, "key = ?", key).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", nil
		}
		return "", err
	}
	return row.Value, nil
}

// Set UPSERT 一个 key 的值。
func (s *Settings) Set(key, value string) error {
	row := AppSetting{Key: key, Value: value, UpdatedAt: time.Now()}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&row).Error
}

// GetNotifyTemplates 读取通知模板 JSON。缺失或解析失败均返回空 map（调用方回落默认）。
func (s *Settings) GetNotifyTemplates() (map[NotificationEvent]string, error) {
	raw, err := s.Get(settingKeyNotifyTemplates)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return map[NotificationEvent]string{}, nil
	}
	// JSON 反序列化用 map[string]string 再转，避免 NotificationEvent 键的编码差异。
	var tmp map[string]string
	if err := json.Unmarshal([]byte(raw), &tmp); err != nil {
		return map[NotificationEvent]string{}, nil
	}
	out := make(map[NotificationEvent]string, len(tmp))
	for k, v := range tmp {
		out[NotificationEvent(k)] = v
	}
	return out, nil
}

// SetNotifyTemplates 把模板 map JSON 化后 UPSERT。
func (s *Settings) SetNotifyTemplates(tmpls map[NotificationEvent]string) error {
	// 用 map[string]string 序列化，保证 JSON 键是纯字符串。
	tmp := make(map[string]string, len(tmpls))
	for k, v := range tmpls {
		tmp[string(k)] = v
	}
	raw, err := json.Marshal(tmp)
	if err != nil {
		return err
	}
	return s.Set(settingKeyNotifyTemplates, string(raw))
}
