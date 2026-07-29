-- 为已有 channels 表补充可选充值链接。
-- 服务启动时 GORM AutoMigrate 会执行等价变更；此文件供生产人工核对或手动迁移。

ALTER TABLE channels
    ADD COLUMN IF NOT EXISTS recharge_url VARCHAR(2048) NOT NULL DEFAULT '';
