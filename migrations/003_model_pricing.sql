USE cx_ec;

CREATE TABLE ai_model_pricing (
  model_id VARCHAR(120) NOT NULL PRIMARY KEY,
  time_zone VARCHAR(64) NOT NULL DEFAULT 'Asia/Shanghai',
  peak_enabled TINYINT(1) NOT NULL DEFAULT 0,
  peak_start CHAR(5) NULL,
  peak_end CHAR(5) NULL,
  peak_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  peak_output_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  peak_multiplier_bps INT UNSIGNED NOT NULL DEFAULT 10000,
  limited_enabled TINYINT(1) NOT NULL DEFAULT 0,
  limited_start_unix BIGINT UNSIGNED NULL,
  limited_end_unix BIGINT UNSIGNED NULL,
  limited_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  limited_output_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  limited_multiplier_bps INT UNSIGNED NOT NULL DEFAULT 10000,
  CONSTRAINT fk_ai_pricing_model FOREIGN KEY (model_id) REFERENCES ai_model(model_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 所有现有模型显式初始化为常规计费，原价格不变。
INSERT INTO ai_model_pricing(model_id) SELECT model_id FROM ai_model;

-- 独立保存扣费证据，删除模型或用量记录时仍保留锁价信息。
CREATE TABLE ai_usage_pricing (
  request_id CHAR(36) NOT NULL PRIMARY KEY,
  uid BIGINT UNSIGNED NOT NULL,
  model_id VARCHAR(120) NOT NULL,
  price_kind ENUM('standard','peak','limited') NOT NULL,
  time_zone VARCHAR(64) NOT NULL,
  price_locked_at DATETIME(6) NOT NULL COMMENT 'UTC',
  input_price_micro_per_1k BIGINT UNSIGNED NOT NULL,
  output_price_micro_per_1k BIGINT UNSIGNED NOT NULL,
  multiplier_bps INT UNSIGNED NOT NULL,
  KEY idx_ai_usage_pricing_uid (uid,price_locked_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
