package storage

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	glebarez "github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 支持的数据库驱动。
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

type DBConfig struct {
	// Driver 数据库类型：sqlite（默认，零依赖单文件）| postgres。
	// postgresql / pg 作为别名归一化处理；空值视为 sqlite。
	Driver string
	// Path sqlite 数据库文件路径，仅 Driver=sqlite 时生效。
	Path string

	// 以下字段仅 Driver=postgres 时生效。
	Host         string
	Port         int
	User         string
	Password     string
	Name         string
	SSLMode      string
	Timezone     string
	MaxOpenConns int
	MaxIdleConns int
}

func (c DBConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=%s",
		c.Host, c.Port, c.User, c.Password, c.Name, c.SSLMode, c.Timezone,
	)
}

// newGormLogger 关掉 GORM 默认 logger 对 ErrRecordNotFound 的告警噪音。
//
// 业务代码（如 Rates.Upsert）显式处理了"找不到就插入"，这种情况下 GORM 默认仍会
// 把 record not found 当 Warn 打出来，造成日志看起来满是错误其实没问题。
// IgnoreRecordNotFoundError = true 可以静默这类预期内的"未找到"。
func newGormLogger() logger.Interface {
	return logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)
}

// Open 按配置的驱动打开数据库连接。
//
// driver 为空或 "sqlite" 时使用纯 Go SQLite 驱动（glebarez/sqlite，基于 modernc，
// 无 CGO 依赖，可与 CGO_ENABLED=0 的静态构建共存）；"postgres" / "postgresql" / "pg"
// 走原有 PostgreSQL 路径。其他值报错，避免静默落到错误的后端。
func Open(cfg DBConfig) (*gorm.DB, error) {
	switch driver := strings.ToLower(strings.TrimSpace(cfg.Driver)); {
	case driver == "" || driver == DriverSQLite:
		return openSQLite(cfg)
	case driver == DriverPostgres || driver == "postgresql" || driver == "pg":
		return openPostgres(cfg)
	default:
		return nil, fmt.Errorf("unknown database driver %q (want %q or %q)", cfg.Driver, DriverSQLite, DriverPostgres)
	}
}

func openPostgres(cfg DBConfig) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{
		Logger: newGormLogger(),
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	return db, nil
}

func openSQLite(cfg DBConfig) (*gorm.DB, error) {
	path := cfg.Path
	if path == "" {
		path = "upstream-hub.db"
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite data dir: %w", err)
		}
	}
	// WAL + busy_timeout：SQLite 多读单写的模型下，并发扫描任务 + API 请求
	// 偶尔撞锁时等待而不是直接报 SQLITE_BUSY；synchronous=NORMAL 配合 WAL
	// 是性能 / 掉电安全性的常用折中。
	dsn := fmt.Sprintf(
		"%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)",
		path,
	)
	db, err := gorm.Open(glebarez.Open(dsn), &gorm.Config{
		Logger: newGormLogger(),
	})
	if err != nil {
		// modernc 驱动把所有打开失败（含权限不足）都报成 "out of memory (14)"，
		// 极度误导。这里先探一次真实原因，权限问题给出直白的提示。
		if probe, perr := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644); perr != nil {
			if os.IsPermission(perr) {
				return nil, fmt.Errorf("open sqlite: no write permission on %s (running as uid=%d gid=%d): %w", path, os.Getuid(), os.Getgid(), perr)
			}
			return nil, fmt.Errorf("open sqlite %s: %w", path, perr)
		} else {
			_ = probe.Close()
		}
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	// SQLite 写串行；连接池开太大只会增加撞锁等待。默认给 1 条写连接 + 少量读连接。
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 || maxOpen > 4 {
		maxOpen = 4
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(1)
	return db, nil
}

// AutoMigrate 启动时自动同步表结构。SQLite 完全依赖这条路径；
// PostgreSQL 生产环境也提供 migrations/*.sql 作为对照（注意那批 SQL 是 PG 方言）。
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&Channel{},
		&AuthSession{},
		&CaptchaConfig{},
		&RateSnapshot{},
		&RateChangeLog{},
		&BalanceSnapshot{},
		&NotificationChannel{},
		&NotificationLog{},
		&NotificationCooldown{},
		&MonitorLog{},
		&AlertState{},
		&AppSetting{},
	)
}
