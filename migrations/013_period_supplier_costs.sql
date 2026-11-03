-- 目标库 cx_ec；先备份 ai_model_pricing 的结构和数据，再执行一次。
-- MySQL 5.7 DDL 隐式提交，不要放入业务事务或在失败后盲目重试。
-- 执行前确认下列六列不存在；若存在请停止并检查实际结构。
-- 仅新增可空供应商成本，不改变任何客户单价、钱包余额或时段配置。
USE cx_ec;

ALTER TABLE ai_model_pricing
  ADD COLUMN peak_input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN peak_cache_input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN peak_output_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN limited_input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN limited_cache_input_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN limited_output_cost_micro_yuan_per_1m BIGINT UNSIGNED NULL DEFAULT NULL;

-- 验证：应返回 6；历史记录六列均为 NULL，表示该时段成本尚未配置。
SELECT COUNT(*) AS period_cost_columns
FROM information_schema.columns
WHERE table_schema='cx_ec' AND table_name='ai_model_pricing'
AND column_name IN ('peak_input_cost_micro_yuan_per_1m','peak_cache_input_cost_micro_yuan_per_1m',
 'peak_output_cost_micro_yuan_per_1m','limited_input_cost_micro_yuan_per_1m',
 'limited_cache_input_cost_micro_yuan_per_1m','limited_output_cost_micro_yuan_per_1m');

-- 若尚未启用新代码、且没有填入时段成本，回退时先恢复旧后台，再删除这六列。
-- 已填成本时应先导出成本配置，不得直接删除字段丢失数据。
