package storage

import (
	"sort"
	"time"

	"gorm.io/gorm"
)

type Rates struct{ db *gorm.DB }

func NewRates(db *gorm.DB) *Rates { return &Rates{db: db} }

// ListByChannel 返回渠道当前所有倍率快照。
func (r *Rates) ListByChannel(channelID uint) ([]RateSnapshot, error) {
	var list []RateSnapshot
	if err := r.db.Where("channel_id = ?", channelID).Order("model_name ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// Upsert 更新或插入倍率快照，返回此前的记录（若有），调用方据此判断是否变化。
func (r *Rates) Upsert(snapshot *RateSnapshot) (*RateSnapshot, error) {
	var prev RateSnapshot
	err := r.db.
		Where("channel_id = ? AND model_name = ?", snapshot.ChannelID, snapshot.ModelName).
		First(&prev).Error
	switch {
	case err == nil:
		old := prev
		prev.Ratio = snapshot.Ratio
		prev.CompletionRatio = snapshot.CompletionRatio
		prev.Description = snapshot.Description
		prev.LastSeenAt = snapshot.LastSeenAt
		if err := r.db.Save(&prev).Error; err != nil {
			return nil, err
		}
		return &old, nil
	case err == gorm.ErrRecordNotFound:
		snapshot.FirstSeenAt = snapshot.LastSeenAt
		if err := r.db.Create(snapshot).Error; err != nil {
			return nil, err
		}
		return nil, nil
	default:
		return nil, err
	}
}

func (r *Rates) AppendChange(log *RateChangeLog) error {
	if log.ChangedAt.IsZero() {
		log.ChangedAt = time.Now()
	}
	return r.db.Create(log).Error
}

// ListChanges 倒序拉取倍率变化日志。channelID 为 0 表示不过滤。
func (r *Rates) ListChanges(channelID uint, limit int) ([]RateChangeLog, error) {
	if limit <= 0 {
		limit = 50
	}
	q := r.db.Model(&RateChangeLog{}).Order("changed_at DESC").Limit(limit)
	if channelID != 0 {
		q = q.Where("channel_id = ?", channelID)
	}
	var list []RateChangeLog
	if err := q.Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *Rates) AppendBalance(s *BalanceSnapshot) error {
	if s.SampledAt.IsZero() {
		s.SampledAt = time.Now()
	}
	return r.db.Create(s).Error
}

// DeleteBalanceSnapshotsBefore 删除 sampled_at < cutoff 的余额快照，返回删除行数。
func (r *Rates) DeleteBalanceSnapshotsBefore(cutoff time.Time) (int64, error) {
	res := r.db.Where("sampled_at < ?", cutoff).Delete(&BalanceSnapshot{})
	return res.RowsAffected, res.Error
}

// BalanceHistory 倒序拉取余额历史。
func (r *Rates) BalanceHistory(channelID uint, limit int) ([]BalanceSnapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	var list []BalanceSnapshot
	if err := r.db.
		Where("channel_id = ?", channelID).
		Order("sampled_at DESC").
		Limit(limit).
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// DailyAggregate 一天的聚合余额（所有渠道之和）。
type DailyAggregate struct {
	Day     time.Time `json:"day"`
	Balance float64   `json:"balance"`
}

// truncateLocalDay 取 t 所在本地时区的当天 00:00。
// 不能用 t.Truncate(24h)：那是对 UTC 时间零点取整，在东八区会把日界切到早上 8 点。
func truncateLocalDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// balanceRow 聚合趋势时从库里捞出的最小字段集。
type balanceRow struct {
	ChannelID uint
	SampledAt time.Time
	Balance   float64
}

// aggregateLastPerBucket 对每个 (channel, bucket) 取桶内最后一次采样，再按桶求和。
//
// bucket 决定"天"还是"小时"粒度；trunc 把时间戳截断到桶起点（用本地时区）。
// 之前这步用 PostgreSQL 的 date_trunc + CTE 在 SQL 里做；为了让 SQLite 也能跑
// （SQLite 的日期函数会先把带时区的时间转成 UTC，日界会偏），改为把行拉回 Go 内存聚合。
// 数据量是"渠道数 × 每 15 分钟一条 × N 天"，对单机自部署场景完全无压力。
func aggregateLastPerBucket(rows []balanceRow, trunc func(time.Time) time.Time) []DailyAggregate {
	type key struct {
		channel uint
		bucket  time.Time
	}
	last := make(map[key]balanceRow, len(rows))
	for _, row := range rows {
		k := key{row.ChannelID, trunc(row.SampledAt)}
		if prev, ok := last[k]; !ok || row.SampledAt.After(prev.SampledAt) {
			last[k] = row
		}
	}
	sums := make(map[time.Time]float64, len(last))
	for _, row := range last {
		sums[trunc(row.SampledAt)] += row.Balance
	}
	out := make([]DailyAggregate, 0, len(sums))
	for day, balance := range sums {
		out = append(out, DailyAggregate{Day: day, Balance: balance})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day.Before(out[j].Day) })
	return out
}

// AggregateBalanceTrend 取最近 N 天的"日内最后一次余额"按渠道之和，作为总余额趋势。
//
// 实现：对每个 (channel_id, day) 取该天最后一次 BalanceSnapshot 的余额，再按 day 求和。
// 没有采样的日子返回 0；调用方应自己外推 / 留空。
func (r *Rates) AggregateBalanceTrend(days int) ([]DailyAggregate, error) {
	if days <= 0 {
		days = 7
	}
	since := truncateLocalDay(time.Now().AddDate(0, 0, -(days - 1)))
	var rows []balanceRow
	if err := r.db.Model(&BalanceSnapshot{}).
		Select("channel_id, sampled_at, balance").
		Where("sampled_at >= ?", since).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return aggregateLastPerBucket(rows, truncateLocalDay), nil
}

// AggregateBalanceTrendHourly 取最近 N 小时的"小时内最后一次余额"按渠道之和。
//
// 实现与日趋势一致：对每个 (channel_id, hour) 取该小时最后一次 BalanceSnapshot，
// 再按 hour 求和，用于展示一天内余额波动。
func (r *Rates) AggregateBalanceTrendHourly(hours int) ([]DailyAggregate, error) {
	if hours <= 0 {
		hours = 24
	}
	since := time.Now().Add(-time.Duration(hours-1) * time.Hour).Truncate(time.Hour)
	var rows []balanceRow
	if err := r.db.Model(&BalanceSnapshot{}).
		Select("channel_id, sampled_at, balance").
		Where("sampled_at >= ?", since).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	// 整小时偏移的时区（如 Asia/Shanghai）下 Truncate(hour) 就是本地小时界。
	return aggregateLastPerBucket(rows, func(t time.Time) time.Time { return t.Truncate(time.Hour) }), nil
}
