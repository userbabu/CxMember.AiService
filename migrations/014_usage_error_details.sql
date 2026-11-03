-- 现有成功记录保持为空；失败详情与用量记录使用相同的30天保留期。
ALTER TABLE ai_usage
  ADD COLUMN error_detail MEDIUMTEXT NULL,
  ADD COLUMN elapsed_ms BIGINT UNSIGNED NULL;
