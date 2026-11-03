-- 执行前备份 cx_ec.ai_model、ai_model_pricing、ai_usage_pricing。
-- 确认18个vision字段及billing_input_kind字段均不存在；DDL隐式提交，失败后检查实际结构，不盲目重跑。
USE cx_ec;
ALTER TABLE ai_model_pricing
  ADD COLUMN vision_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN vision_input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN vision_cache_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN vision_cache_input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN vision_output_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN vision_output_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN peak_vision_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN peak_vision_input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN peak_vision_cache_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN peak_vision_cache_input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN peak_vision_output_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN peak_vision_output_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN limited_vision_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN limited_vision_input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN limited_vision_cache_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN limited_vision_cache_input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN limited_vision_output_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN limited_vision_output_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL;

-- 初始多模态价格/成本与当前文本规则一致，后续由管理员独立配置。
START TRANSACTION;
UPDATE ai_model_pricing p JOIN ai_model m ON m.model_id=p.model_id SET
  p.vision_input_price_micro_per_1k=m.input_price_micro_per_1k,
  p.vision_input_cost_micro_yuan_per_1m=m.input_cost_micro_yuan_per_1m,
  p.vision_cache_input_price_micro_per_1k=m.cache_input_price_micro_per_1k,
  p.vision_cache_input_cost_micro_yuan_per_1m=m.cache_input_cost_micro_yuan_per_1m,
  p.vision_output_price_micro_per_1k=m.output_price_micro_per_1k,
  p.vision_output_cost_micro_yuan_per_1m=m.output_cost_micro_yuan_per_1m,
  p.peak_vision_input_price_micro_per_1k=p.peak_input_price_micro_per_1k,
  p.peak_vision_input_cost_micro_yuan_per_1m=p.peak_input_cost_micro_yuan_per_1m,
  p.peak_vision_cache_input_price_micro_per_1k=p.peak_cache_input_price_micro_per_1k,
  p.peak_vision_cache_input_cost_micro_yuan_per_1m=p.peak_cache_input_cost_micro_yuan_per_1m,
  p.peak_vision_output_price_micro_per_1k=p.peak_output_price_micro_per_1k,
  p.peak_vision_output_cost_micro_yuan_per_1m=p.peak_output_cost_micro_yuan_per_1m,
  p.limited_vision_input_price_micro_per_1k=p.limited_input_price_micro_per_1k,
  p.limited_vision_input_cost_micro_yuan_per_1m=p.limited_input_cost_micro_yuan_per_1m,
  p.limited_vision_cache_input_price_micro_per_1k=p.limited_cache_input_price_micro_per_1k,
  p.limited_vision_cache_input_cost_micro_yuan_per_1m=p.limited_cache_input_cost_micro_yuan_per_1m,
  p.limited_vision_output_price_micro_per_1k=p.limited_output_price_micro_per_1k,
  p.limited_vision_output_cost_micro_yuan_per_1m=p.limited_output_cost_micro_yuan_per_1m;
COMMIT;

-- 记录实际采用哪套价格；历史快照使用的原单价保持不变。
ALTER TABLE ai_usage_pricing ADD COLUMN billing_input_kind ENUM('text','vision') NOT NULL DEFAULT 'text' AFTER price_kind;
SELECT TABLE_NAME,COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COLUMN_DEFAULT FROM information_schema.columns
WHERE TABLE_SCHEMA='cx_ec' AND ((TABLE_NAME='ai_model_pricing' AND COLUMN_NAME LIKE '%vision%') OR
(TABLE_NAME='ai_usage_pricing' AND COLUMN_NAME='billing_input_kind')) ORDER BY TABLE_NAME,ORDINAL_POSITION;

-- 回退前导出多模态配置及使用该价格表的快照标记，先恢复旧Go/PHP/前端，再另行删除新增列。
-- 不得删除或重算任何钱包、账本、用量及原价格快照。
