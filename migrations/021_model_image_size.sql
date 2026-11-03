-- 执行前备份 cx_ec.ai_model。
-- 已只读确认 max_image_bytes 不存在，表为InnoDB，共134个模型。
-- DDL隐式提交，失败后检查实际结构，不盲目重跑。
USE cx_ec;
ALTER TABLE ai_model ADD COLUMN max_image_bytes INT UNSIGNED NOT NULL DEFAULT 4194304 AFTER allow_images;
-- 初始每张4MiB，后台按KiB配置，范围1KiB–32MiB；不改模型启用状态、价格或钱包。
