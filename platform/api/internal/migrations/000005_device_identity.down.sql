ALTER TABLE enrollment_tokens ALTER COLUMN uses DROP NOT NULL;

DROP TABLE IF EXISTS device_strategies;
DROP TABLE IF EXISTS device_disconnect_requests;
DROP TABLE IF EXISTS device_observations;
DROP TABLE IF EXISTS device_credentials;
