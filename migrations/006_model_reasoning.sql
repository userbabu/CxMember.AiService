-- 在 cx_ec 依次完成 005、006 后再发布；本脚本不会由服务自动执行。
-- 预检 ai_model 无 reasoning_efforts/reasoning_format 同名字段，备份模型配置并暂停聊天和模型编辑。
USE cx_ec;
ALTER TABLE ai_model
  ADD COLUMN reasoning_efforts VARCHAR(128) NOT NULL DEFAULT '[]' AFTER allow_thinking,
  ADD COLUMN reasoning_format ENUM('reasoning_effort','deepseek') NOT NULL DEFAULT 'reasoning_effort' AFTER reasoning_efforts;

-- 现有允许思考的模型明确初始化为 high；管理员随后按上游实际支持的档位勾选。
START TRANSACTION;
UPDATE ai_model SET reasoning_efforts='["high"]' WHERE allow_thinking=1;
UPDATE ai_model m JOIN ai_provider p ON p.provider_id=m.provider_id
 SET m.reasoning_format='deepseek'
 WHERE LOWER(TRIM(TRAILING '/' FROM p.base_url)) IN ('https://api.deepseek.com','https://api.deepseek.com/v1');
COMMIT;
-- DDL 隐式提交；任何步骤失败都应停止发布并核查已提交的列及初始化状态，禁止盲目重复执行。
-- 回退先恢复旧 Go/PHP，导出新思考配置，再 ALTER TABLE ai_model DROP COLUMN reasoning_efforts, DROP COLUMN reasoning_format;
