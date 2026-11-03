-- 执行前备份 cx_ec.ai_model，确认 allow_images 列不存在。
-- MySQL DDL 隐式提交；失败后检查实际结构，不可盲目重试。
USE cx_ec;
ALTER TABLE ai_model ADD COLUMN allow_images TINYINT(1) NOT NULL DEFAULT 0 AFTER allow_tools;
-- 所有现有及新导入模型默认关闭，须按供应商真实能力逐一启用。
SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COLUMN_DEFAULT FROM information_schema.columns
WHERE TABLE_SCHEMA='cx_ec' AND TABLE_NAME='ai_model' AND COLUMN_NAME='allow_images';
-- 回退：先导出能力配置并恢复旧 Go/PHP/EC，再删除该列。
-- ALTER TABLE cx_ec.ai_model DROP COLUMN allow_images;
