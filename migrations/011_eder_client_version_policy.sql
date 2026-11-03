USE cx_ec;

ALTER TABLE ai_eder_service_config
  ADD COLUMN blocked_eder_versions JSON NULL AFTER unavailable_message,
  ADD COLUMN blocked_software_versions JSON NULL AFTER blocked_eder_versions;

UPDATE ai_eder_service_config
SET blocked_eder_versions=JSON_ARRAY(),
    blocked_software_versions=JSON_ARRAY()
WHERE config_id=1;

ALTER TABLE ai_eder_service_config
  MODIFY COLUMN blocked_eder_versions JSON NOT NULL,
  MODIFY COLUMN blocked_software_versions JSON NOT NULL;
