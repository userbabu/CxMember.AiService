-- 执行前备份 cx_ec.ai_model；只读核验：表为 InnoDB，134 个模型，目标字段不存在。
-- MySQL DDL 隐式提交。失败后检查实际字段状态，不重复执行；回退前导出配置并先恢复旧程序。
USE cx_ec;
ALTER TABLE ai_model
    ADD COLUMN max_images_per_request TINYINT UNSIGNED NOT NULL DEFAULT 4 AFTER max_image_bytes;
