-- 执行前备份 cx_ec.ai_eder_service_config，并确认 global_prompt 列不存在。
-- MySQL DDL 隐式提交；执行失败须检查实际结构，不可盲目重试。
USE cx_ec;

ALTER TABLE ai_eder_service_config
  ADD COLUMN global_prompt MEDIUMTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NULL AFTER unavailable_message;

-- 配置初始为空，不启用全局提示词；不修改钱包、模型或其他服务设置。
UPDATE ai_eder_service_config SET global_prompt='' WHERE config_id=1;
ALTER TABLE ai_eder_service_config MODIFY COLUMN global_prompt MEDIUMTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL;

SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE FROM information_schema.columns
WHERE TABLE_SCHEMA='cx_ec' AND TABLE_NAME='ai_eder_service_config' AND COLUMN_NAME='global_prompt';

-- 回退：先导出 global_prompt 并恢复旧 Go/PHP/前端，再删除字段。
-- ALTER TABLE cx_ec.ai_eder_service_config DROP COLUMN global_prompt;
