USE cx_ec;

-- 缓存命中输入单独计价；默认 0 保持迁移前的实际费用不变。
ALTER TABLE ai_model
  ADD COLUMN cache_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER input_price_micro_per_1k,
  ADD COLUMN input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL AFTER output_price_micro_per_1k,
  ADD COLUMN cache_input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL AFTER input_cost_micro_yuan_per_1m,
  ADD COLUMN output_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL AFTER cache_input_cost_micro_yuan_per_1m,
  ADD COLUMN profit_percent_bps INT UNSIGNED NOT NULL DEFAULT 2000 AFTER output_cost_micro_yuan_per_1m,
  ADD COLUMN credit_pack_price_micro_yuan BIGINT UNSIGNED NOT NULL DEFAULT 5000000 AFTER profit_percent_bps;

ALTER TABLE ai_model_pricing
  ADD COLUMN peak_cache_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER peak_input_price_micro_per_1k,
  ADD COLUMN limited_cache_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER limited_input_price_micro_per_1k;

ALTER TABLE ai_usage
  ADD COLUMN cached_prompt_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER prompt_tokens;

ALTER TABLE ai_usage_pricing
  ADD COLUMN cache_input_price_micro_per_1k BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER input_price_micro_per_1k;
