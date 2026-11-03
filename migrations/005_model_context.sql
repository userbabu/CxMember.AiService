-- 在 cx_ec 执行一次；先确认 ai_context_tier 与 ai_model.max_context_tokens 均不存在。
-- 暂停模型编辑及聊天入口；先迁移、核验，再发布 Go/PHP/客户端。
USE cx_ec;

-- MySQL 5.7 不执行 CHECK，使用固定档位字典与外键保证所有写入均受约束。
CREATE TABLE ai_context_tier (
  token_count INT UNSIGNED NOT NULL PRIMARY KEY
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
INSERT INTO ai_context_tier(token_count) VALUES
  (4096),(8192),(16384),(32768),(65536),(131072),(262144),(524288),(1048576);

-- 已有模型及后续导入模型明确初始化为 64K，不改价格、余额和历史用量。
ALTER TABLE ai_model
  ADD COLUMN max_context_tokens INT UNSIGNED NOT NULL DEFAULT 65536 AFTER max_output_tokens,
  ADD KEY idx_ai_model_context (max_context_tokens),
  ADD CONSTRAINT fk_ai_model_context FOREIGN KEY (max_context_tokens) REFERENCES ai_context_tier(token_count);

-- DDL 隐式提交；失败须停止发布，检查新表、列、外键的真实状态，不自动重试。
-- 回退须先恢复旧版 Go/PHP，导出各模型上下文，再依次执行：
-- ALTER TABLE ai_model DROP FOREIGN KEY fk_ai_model_context, DROP INDEX idx_ai_model_context, DROP COLUMN max_context_tokens;
-- DROP TABLE ai_context_tier;
