-- 通用 KV 设置表。当前承载通知模板（key = 'notify_templates'）。
-- 服务启动时 GORM AutoMigrate 会执行等价变更；此文件供生产人工核对或手动迁移。

CREATE TABLE IF NOT EXISTS app_settings (
    key        VARCHAR(64)  NOT NULL,
    value      TEXT,
    updated_at TIMESTAMP,
    CONSTRAINT app_settings_pkey PRIMARY KEY (key)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_app_settings_key ON app_settings (key);
