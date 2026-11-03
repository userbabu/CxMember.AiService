-- 018 已执行后，生成任务模型编号必须与 ai_model.model_id 的120字符上限一致。
-- 只读确认 ai_generation 当前为空，修改不影响钱包或模型配置。
USE cx_ec;
ALTER TABLE ai_generation MODIFY COLUMN model_id VARCHAR(120) NOT NULL;
