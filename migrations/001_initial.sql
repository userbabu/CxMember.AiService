USE cx_ec;

CREATE TABLE IF NOT EXISTS ai_account (
  uid BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  status ENUM('active','suspended') NOT NULL DEFAULT 'active',
  disabled_message VARCHAR(500) NOT NULL DEFAULT '',
  quota_limit_micro BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_wallet (
  uid BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  balance_micro BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  CONSTRAINT fk_ai_wallet_account FOREIGN KEY (uid) REFERENCES ai_account(uid) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_provider (
  provider_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(100) NOT NULL,
  base_url VARCHAR(500) NOT NULL,
  protocol VARCHAR(24) NOT NULL DEFAULT 'openai-chat',
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uq_ai_provider_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_provider_credential (
  credential_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  provider_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(100) NOT NULL,
  secret_value VARBINARY(4096) NOT NULL COMMENT 'AES-GCM encrypted; key configured outside database',
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  KEY idx_ai_credential_provider (provider_id),
  CONSTRAINT fk_ai_credential_provider FOREIGN KEY (provider_id) REFERENCES ai_provider(provider_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_model (
  model_id VARCHAR(120) NOT NULL PRIMARY KEY,
  display_name VARCHAR(160) NOT NULL,
  provider_id BIGINT UNSIGNED NOT NULL,
  credential_id BIGINT UNSIGNED NOT NULL,
  upstream_model VARCHAR(160) NOT NULL,
  product_scope VARCHAR(32) NOT NULL DEFAULT '',
  input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  output_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  multiplier_bps INT UNSIGNED NOT NULL DEFAULT 10000,
  tpm INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0 means unlimited',
  rpm INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0 means unlimited',
  max_output_tokens INT UNSIGNED NOT NULL DEFAULT 4096,
  allow_thinking TINYINT(1) NOT NULL DEFAULT 0,
  allow_tools TINYINT(1) NOT NULL DEFAULT 0,
  enabled TINYINT(1) NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  KEY idx_ai_model_provider (provider_id), KEY idx_ai_model_credential (credential_id),
  CONSTRAINT fk_ai_model_provider FOREIGN KEY (provider_id) REFERENCES ai_provider(provider_id),
  CONSTRAINT fk_ai_model_credential FOREIGN KEY (credential_id) REFERENCES ai_provider_credential(credential_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_user_token (
  jti CHAR(36) NOT NULL PRIMARY KEY,
  uid BIGINT UNSIGNED NOT NULL,
  product VARCHAR(32) NOT NULL DEFAULT '17',
  expires_at DATETIME NOT NULL,
  revoked_at DATETIME NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  KEY idx_ai_token_uid (uid, expires_at),
  CONSTRAINT fk_ai_token_account FOREIGN KEY (uid) REFERENCES ai_account(uid) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_rate_window (
  uid BIGINT UNSIGNED NOT NULL,
  model_id VARCHAR(120) NOT NULL,
  window_minute DATETIME NOT NULL,
  request_count INT UNSIGNED NOT NULL DEFAULT 0,
  token_count INT UNSIGNED NOT NULL DEFAULT 0,
  PRIMARY KEY (uid, model_id, window_minute),
  KEY idx_ai_rate_expiry (window_minute)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_usage (
  usage_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  request_id CHAR(36) NOT NULL,
  uid BIGINT UNSIGNED NOT NULL,
  model_id VARCHAR(120) NOT NULL,
  prompt_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
  completion_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
  charged_micro BIGINT NOT NULL DEFAULT 0,
  status VARCHAR(16) NOT NULL,
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uq_ai_usage_request (request_id), KEY idx_ai_usage_uid_date (uid, created_at), KEY idx_ai_usage_model_date (model_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_wallet_ledger (
  ledger_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  uid BIGINT UNSIGNED NOT NULL,
  request_id CHAR(36) NULL,
  delta_micro BIGINT NOT NULL,
  balance_after_micro BIGINT NOT NULL,
  reason VARCHAR(32) NOT NULL,
  operator_id VARCHAR(100) NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uq_ai_ledger_request_reason (request_id, reason), KEY idx_ai_ledger_uid_date (uid, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_api_key (
  api_key_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  uid BIGINT UNSIGNED NOT NULL,
  key_prefix VARCHAR(20) NOT NULL,
  key_hash BINARY(32) NOT NULL,
  name VARCHAR(100) NOT NULL,
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  expires_at DATETIME NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uq_ai_api_key_hash (key_hash), KEY idx_ai_api_key_uid (uid)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_admin_audit (
  audit_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  admin_id VARCHAR(100) NOT NULL,
  action VARCHAR(100) NOT NULL,
  target VARCHAR(180) NOT NULL,
  details JSON NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  KEY idx_ai_audit_date (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
