-- 告警处理状态表。飞书交互卡片"已处理/不处理"按钮回写。
-- 服务启动时 GORM AutoMigrate 会执行等价变更；此文件供生产人工核对或手动迁移。

CREATE TABLE IF NOT EXISTS alert_states (
    id                SERIAL PRIMARY KEY,
    alert_id          VARCHAR(64) NOT NULL,
    channel_id        INTEGER     NOT NULL,
    event             VARCHAR(64) NOT NULL,
    notify_channel_id INTEGER,
    feishu_message_id VARCHAR(64),
    status            VARCHAR(16) NOT NULL DEFAULT 'pending',
    handled_by        VARCHAR(64),
    handled_at        TIMESTAMP,
    created_at        TIMESTAMP   NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_alert_states_alert_id ON alert_states (alert_id);
CREATE INDEX IF NOT EXISTS idx_alert_chan_event ON alert_states (channel_id, event);
CREATE INDEX IF NOT EXISTS idx_alert_states_created_at ON alert_states (created_at);
