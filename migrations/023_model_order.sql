-- 执行前备份 cx_ec.ai_model；只读核验：表为 InnoDB，目标两个字段不存在。
-- MySQL DDL 隐式提交。失败后检查实际字段状态，不重复执行；回退前先恢复旧程序。
USE cx_ec;
ALTER TABLE ai_model
    ADD COLUMN sort_order INT NOT NULL DEFAULT 0 AFTER enabled,
    ADD COLUMN is_default TINYINT(1) NOT NULL DEFAULT 0 AFTER sort_order;
