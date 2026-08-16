-- Reverses 000004.
--
-- Sessions are dropped again on the way down: the stored values are hashes and
-- cannot be turned back into the tokens clients hold.

DELETE FROM client_sessions;

ALTER INDEX idx_client_sessions_token_hash RENAME TO idx_client_sessions_rustdesk_token;

ALTER TABLE client_sessions ALTER COLUMN token_hash TYPE VARCHAR(255);
ALTER TABLE client_sessions RENAME COLUMN token_hash TO rustdesk_token;

DROP INDEX IF EXISTS idx_login_attempts_time;
DROP INDEX IF EXISTS idx_login_attempts_ip_time;
DROP TABLE IF EXISTS login_attempts;

DROP TABLE IF EXISTS user_credentials;
