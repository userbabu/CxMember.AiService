CREATE TABLE ai_eder_service_config (
    config_id TINYINT UNSIGNED NOT NULL PRIMARY KEY,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    unavailable_message VARCHAR(500) NOT NULL DEFAULT '',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO ai_eder_service_config(config_id, enabled, unavailable_message) VALUES(1, 1, '');
