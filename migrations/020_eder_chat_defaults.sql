-- 执行前备份 cx_ec.ai_eder_service_config。
-- 已只读确认两个字段不存在、表为InnoDB、config_id=1唯一记录存在。
-- DDL隐式提交；中断后检查实际字段状态，不盲目重跑。
USE cx_ec;
ALTER TABLE ai_eder_service_config
 ADD COLUMN default_reasoning_effort ENUM('none','minimal','low','medium','high','xhigh','max') NOT NULL DEFAULT 'none',
 ADD COLUMN default_context_tokens INT UNSIGNED NOT NULL DEFAULT 65536;
-- 仅新增明确默认值，不修改服务开关、全局提示词、模型配置、用户选择或钱包。
