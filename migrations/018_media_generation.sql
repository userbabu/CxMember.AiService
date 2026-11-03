-- 执行前备份 cx_ec.ai_model、ai_model_pricing、ai_usage_pricing。
-- 已只读确认新增字段及 ai_generation 表不存在，相关表均为 InnoDB。
-- MySQL DDL 隐式提交；执行中断后检查实际结构，不得盲目重跑。
USE cx_ec;
ALTER TABLE ai_model
 ADD COLUMN generation_api VARCHAR(32) NOT NULL DEFAULT '',
 ADD COLUMN generation_size VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE ai_model_pricing
 ADD COLUMN generation_price_micro_per_unit BIGINT UNSIGNED NOT NULL DEFAULT 0,
 ADD COLUMN generation_cost_micro_yuan_per_unit BIGINT UNSIGNED NULL,
 ADD COLUMN peak_generation_price_micro_per_unit BIGINT UNSIGNED NOT NULL DEFAULT 0,
 ADD COLUMN peak_generation_cost_micro_yuan_per_unit BIGINT UNSIGNED NULL,
 ADD COLUMN limited_generation_price_micro_per_unit BIGINT UNSIGNED NOT NULL DEFAULT 0,
 ADD COLUMN limited_generation_cost_micro_yuan_per_unit BIGINT UNSIGNED NULL;
ALTER TABLE ai_usage_pricing
 MODIFY COLUMN billing_input_kind ENUM('text','vision','image_generation','video_generation') NOT NULL DEFAULT 'text',
 ADD COLUMN generation_units INT UNSIGNED NULL,
 ADD COLUMN generation_unit_price_micro BIGINT UNSIGNED NULL;
CREATE TABLE ai_generation (
 request_id VARCHAR(36) NOT NULL PRIMARY KEY,
 uid BIGINT UNSIGNED NOT NULL,
 model_id VARCHAR(100) NOT NULL,
 model_type ENUM('image','video') NOT NULL,
 request_hash CHAR(64) NOT NULL,
 status ENUM('submitting','queued','completed','failed') NOT NULL,
 generation_api VARCHAR(32) NOT NULL,
 base_url VARCHAR(500) NOT NULL,
 upstream_model VARCHAR(200) NOT NULL,
 secret_value VARBINARY(4096) NOT NULL,
 upstream_id VARCHAR(200) NOT NULL DEFAULT '',
 requested_units INT UNSIGNED NOT NULL,
 actual_units INT UNSIGNED NULL,
 unit_price_micro BIGINT UNSIGNED NOT NULL,
 reservation_micro BIGINT UNSIGNED NOT NULL,
 price_kind ENUM('standard','peak','limited') NOT NULL,
 time_zone VARCHAR(64) NOT NULL,
 price_locked_at DATETIME(6) NOT NULL,
 result_json LONGTEXT NULL,
 error_code VARCHAR(64) NOT NULL DEFAULT '',
 next_poll_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 lease_until DATETIME NULL,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
 KEY idx_generation_poll(status,next_poll_at,lease_until),
 KEY idx_generation_uid(uid,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- 不修改现有模型类型、启用状态、客户单价或余额。
-- 生成模型须在后台选择协议/尺寸并配置正数单价后启用。
-- 回退前停止生成提交并处理全部未完成任务，导出任务及价格快照；不得删除钱包账本。
