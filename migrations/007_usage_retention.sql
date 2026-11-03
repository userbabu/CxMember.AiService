USE cx_ec;

CREATE INDEX idx_ai_usage_created_at ON ai_usage(created_at, usage_id);
