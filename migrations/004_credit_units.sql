USE cx_ec;

-- 金额转换由同目录 004_credit_units.php 在事务内执行，此表记录唯一计费单位。
CREATE TABLE IF NOT EXISTS ai_billing_unit (
  id TINYINT UNSIGNED NOT NULL PRIMARY KEY,
  unit VARCHAR(16) NOT NULL,
  micro_per_credit BIGINT UNSIGNED NOT NULL,
  migrated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
