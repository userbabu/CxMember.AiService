-- 在 cx_ec 执行一次；先确认 ai_model.model_type 不存在并备份模型配置。
-- 现有模型统一按 text 初始化，导入的新模型也由数据库默认值设为 text。
USE cx_ec;

ALTER TABLE ai_model
  ADD COLUMN model_type ENUM('text','image','video') NOT NULL DEFAULT 'text' AFTER upstream_model;

-- 验证：SHOW COLUMNS FROM ai_model LIKE 'model_type';
-- 验证：SELECT model_type,COUNT(*) FROM ai_model GROUP BY model_type;
-- MySQL DDL 隐式提交；失败后先检查列状态，禁止盲目重复执行。
-- 回退：ALTER TABLE ai_model DROP COLUMN model_type;
