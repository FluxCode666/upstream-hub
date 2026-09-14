package storage

import (
	"gorm.io/gorm"
)

// Channels 渠道仓库。
type Channels struct{ db *gorm.DB }

func NewChannels(db *gorm.DB) *Channels { return &Channels{db: db} }

func (r *Channels) Create(c *Channel) error { return r.db.Create(c).Error }
func (r *Channels) Update(c *Channel) error { return r.db.Save(c).Error }
func (r *Channels) FindByID(id uint) (*Channel, error) {
	var c Channel
	if err := r.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// FindByName 按名称查找未删除的渠道，用于重名校验。找不到返回 (nil, nil)。
func (r *Channels) FindByName(name string) (*Channel, error) {
	var c Channel
	err := r.db.Where("name = ?", name).First(&c).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// Delete 硬删除渠道及其全部关联数据（会话、倍率快照、变化日志、余额历史、
// 监控日志、告警状态、通知冷却），单事务内完成。
//
// 注意：这里必须走 Unscoped 物理删除而不是 GORM 默认的软删除——
// channels.name 上有数据库级唯一索引，软删行会一直占住名字，
// 导致"删除渠道后新建同名渠道"撞唯一约束报错。
func (r *Channels) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Delete(&Channel{}, id).Error; err != nil {
			return err
		}
		// 关联表都没有 DeletedAt（本身就是 append-only 日志 / KV 表），
		// 直接按 channel_id 物理清理。
		for _, m := range []any{
			&AuthSession{},
			&RateSnapshot{},
			&RateChangeLog{},
			&BalanceSnapshot{},
			&MonitorLog{},
			&AlertState{},
			&NotificationCooldown{},
		} {
			if err := tx.Where("channel_id = ?", id).Delete(m).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// SoftDeleteForTest 测试专用：模拟旧版本的软删除行为，制造遗留脏数据场景。
func (r *Channels) SoftDeleteForTest(id uint) error {
	return r.db.Delete(&Channel{}, id).Error
}

// PurgeSoftDeleted 物理清除历史遗留的软删渠道行。
// 旧版本删除走软删，这些行至今仍占着唯一 name 索引；启动时清一次，
// 之后由于 Delete 已改为硬删除，不会再产生新的软删行。
func (r *Channels) PurgeSoftDeleted() (int64, error) {
	res := r.db.Unscoped().Where("deleted_at IS NOT NULL").Delete(&Channel{})
	return res.RowsAffected, res.Error
}

// HardDeleteSoftDeletedByName 兜底：同名且已被软删的遗留行直接物理删除，
// 避免极旧库里残留的软删行在 Create 时撞唯一索引（PurgeSoftDeleted 之外的保险）。
func (r *Channels) HardDeleteSoftDeletedByName(name string) error {
	return r.db.Unscoped().
		Where("name = ? AND deleted_at IS NOT NULL", name).
		Delete(&Channel{}).Error
}
func (r *Channels) List() ([]Channel, error) {
	var list []Channel
	if err := r.db.Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}
func (r *Channels) ListMonitorEnabled() ([]Channel, error) {
	var list []Channel
	if err := r.db.Where("monitor_enabled = ?", true).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}
func (r *Channels) UpdateBalance(id uint, balance float64, at any, lastErr string) error {
	return r.db.Model(&Channel{}).Where("id = ?", id).Updates(map[string]any{
		"last_balance":    balance,
		"last_balance_at": at,
		"last_error":      lastErr,
	}).Error
}
func (r *Channels) SetLastError(id uint, msg string) error {
	return r.db.Model(&Channel{}).Where("id = ?", id).Update("last_error", msg).Error
}
