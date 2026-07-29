-- upstream-hub schema 0001_init
-- 此文件镜像 AutoMigrate 产生的结构，仅供生产人工核对；正常启动由 GORM AutoMigrate 自动建表。

CREATE TABLE IF NOT EXISTS channels (
    id                BIGSERIAL PRIMARY KEY,
    name              VARCHAR(128) NOT NULL,
    type              VARCHAR(32)  NOT NULL,
    site_url          VARCHAR(512) NOT NULL,
    recharge_url      VARCHAR(2048) NOT NULL DEFAULT '',
    username          VARCHAR(256) NOT NULL,
    password_cipher   VARCHAR(4096) NOT NULL,
    credential_mode   VARCHAR(16)  NOT NULL DEFAULT 'password',
    turnstile_enabled BOOLEAN DEFAULT false,
    captcha_config_id BIGINT,
    balance_threshold DOUBLE PRECISION DEFAULT 0,
    monitor_enabled   BOOLEAN DEFAULT true,
    last_balance      DOUBLE PRECISION,
    last_balance_at   TIMESTAMPTZ,
    last_error        TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_name ON channels (name);
CREATE INDEX IF NOT EXISTS idx_channels_type ON channels (type);
CREATE INDEX IF NOT EXISTS idx_channels_deleted_at ON channels (deleted_at);

CREATE TABLE IF NOT EXISTS auth_sessions (
    channel_id          BIGINT PRIMARY KEY,
    user_id             VARCHAR(64),
    access_token_cipher TEXT,
    cookie_cipher       TEXT,
    csrf_token_cipher   VARCHAR(1024),
    expires_at          TIMESTAMPTZ,
    last_login_at       TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS captcha_configs (
    id              BIGSERIAL PRIMARY KEY,
    name            VARCHAR(128) NOT NULL,
    type            VARCHAR(32)  NOT NULL,
    api_key_cipher  VARCHAR(1024),
    endpoint        VARCHAR(512),
    extra           TEXT,
    enabled         BOOLEAN DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_captcha_configs_name ON captcha_configs (name);
CREATE INDEX IF NOT EXISTS idx_captcha_configs_type ON captcha_configs (type);
CREATE INDEX IF NOT EXISTS idx_captcha_configs_deleted_at ON captcha_configs (deleted_at);

CREATE TABLE IF NOT EXISTS rate_snapshots (
    id                BIGSERIAL PRIMARY KEY,
    channel_id        BIGINT NOT NULL,
    model_name        VARCHAR(256) NOT NULL,
    description       VARCHAR(512),
    ratio             DOUBLE PRECISION NOT NULL,
    completion_ratio  DOUBLE PRECISION,
    first_seen_at     TIMESTAMPTZ NOT NULL,
    last_seen_at      TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_rate_chan_model ON rate_snapshots (channel_id, model_name);

CREATE TABLE IF NOT EXISTS rate_change_logs (
    id                    BIGSERIAL PRIMARY KEY,
    channel_id            BIGINT NOT NULL,
    model_name            VARCHAR(256) NOT NULL,
    old_ratio             DOUBLE PRECISION,
    new_ratio             DOUBLE PRECISION NOT NULL,
    old_completion_ratio  DOUBLE PRECISION,
    new_completion_ratio  DOUBLE PRECISION,
    changed_at            TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rate_change_channel ON rate_change_logs (channel_id);
CREATE INDEX IF NOT EXISTS idx_rate_change_model ON rate_change_logs (model_name);
CREATE INDEX IF NOT EXISTS idx_rate_change_at ON rate_change_logs (changed_at);

CREATE TABLE IF NOT EXISTS balance_snapshots (
    id         BIGSERIAL PRIMARY KEY,
    channel_id BIGINT NOT NULL,
    balance    DOUBLE PRECISION NOT NULL,
    sampled_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_balance_channel ON balance_snapshots (channel_id);
CREATE INDEX IF NOT EXISTS idx_balance_at ON balance_snapshots (sampled_at);

CREATE TABLE IF NOT EXISTS notification_channels (
    id            BIGSERIAL PRIMARY KEY,
    name          VARCHAR(128) NOT NULL,
    type          VARCHAR(32)  NOT NULL,
    config_cipher TEXT NOT NULL,
    subscriptions TEXT NOT NULL DEFAULT '[]',
    enabled       BOOLEAN DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_channels_name ON notification_channels (name);
CREATE INDEX IF NOT EXISTS idx_notification_channels_type ON notification_channels (type);
CREATE INDEX IF NOT EXISTS idx_notification_channels_deleted_at ON notification_channels (deleted_at);

CREATE TABLE IF NOT EXISTS notification_cooldowns (
    channel_id    BIGINT       NOT NULL,
    event         VARCHAR(64)  NOT NULL,
    last_sent_at  TIMESTAMPTZ  NOT NULL,
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, event)
);

CREATE TABLE IF NOT EXISTS notification_logs (
    id            BIGSERIAL PRIMARY KEY,
    channel_id    BIGINT NOT NULL,
    event         VARCHAR(64) NOT NULL,
    subject       VARCHAR(512) NOT NULL,
    body          TEXT,
    success       BOOLEAN NOT NULL,
    error_message TEXT,
    sent_at       TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_notification_logs_channel ON notification_logs (channel_id);
CREATE INDEX IF NOT EXISTS idx_notification_logs_event ON notification_logs (event);
CREATE INDEX IF NOT EXISTS idx_notification_logs_at ON notification_logs (sent_at);

CREATE TABLE IF NOT EXISTS monitor_logs (
    id            BIGSERIAL PRIMARY KEY,
    channel_id    BIGINT NOT NULL,
    job           VARCHAR(32) NOT NULL,
    success       BOOLEAN NOT NULL,
    error_message TEXT,
    duration_ms   BIGINT,
    started_at    TIMESTAMPTZ NOT NULL,
    finished_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_monitor_logs_channel ON monitor_logs (channel_id);
CREATE INDEX IF NOT EXISTS idx_monitor_logs_job ON monitor_logs (job);
CREATE INDEX IF NOT EXISTS idx_monitor_logs_started ON monitor_logs (started_at);
