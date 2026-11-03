USE cx_ec;

-- Install the official DeepSeek endpoint as a default provider. API credentials
-- are added through the authenticated admin UI and encrypted there.
INSERT INTO ai_provider(name, base_url, protocol, enabled)
VALUES ('DeepSeek官方', 'https://api.deepseek.com', 'openai-chat', 1)
ON DUPLICATE KEY UPDATE base_url=VALUES(base_url), protocol=VALUES(protocol), enabled=1;
