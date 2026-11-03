ALTER TABLE ai_account
  ADD COLUMN disabled_message VARCHAR(500) NOT NULL DEFAULT '' AFTER status;
