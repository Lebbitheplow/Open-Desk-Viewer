-- Password authentication and non-guessable session tokens.
--
-- Two separate defects are closed here, and they have to move together because
-- both live on the /api/login path.
--
-- 1. AuthService.Authenticate selected password_hash from a user_credentials
--    table that no migration ever created. The query therefore always errored
--    and the path failed closed. It also never compared the hash to anything,
--    so creating this table without fixing the comparison would have opened a
--    full authentication bypass: any password would authenticate any account.
--
-- 2. client_sessions.rustdesk_token held the session credential in plaintext,
--    and the value was derived from time.Now().UnixNano() rather than a CSPRNG.

CREATE TABLE user_credentials (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,

    -- Argon2id in PHC string format:
    -- $argon2id$v=19$m=<KiB>,t=<iterations>,p=<lanes>$<salt>$<hash>
    -- The parameters live in the string so an existing hash stays verifiable
    -- after the cost parameters are raised.
    password_hash TEXT NOT NULL,

    -- Per-account lockout. Counts consecutive failures; cleared on success.
    failed_attempts INTEGER NOT NULL DEFAULT 0,
    locked_until TIMESTAMPTZ,

    password_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-IP throttling, which per-account lockout alone does not provide: an
-- attacker spreading guesses across many accounts never trips a single
-- account's counter.
CREATE TABLE login_attempts (
    id BIGSERIAL PRIMARY KEY,
    client_ip INET NOT NULL,
    email VARCHAR(255),
    succeeded BOOLEAN NOT NULL,
    attempted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_login_attempts_ip_time ON login_attempts(client_ip, attempted_at DESC);
CREATE INDEX idx_login_attempts_time ON login_attempts(attempted_at);

-- Existing sessions hold plaintext tokens that cannot be converted to hashes,
-- and every one of them was issued by the predictable generator. They are
-- 24-hour credentials, so dropping them costs a re-login and removes the
-- guessable ones from circulation.
DELETE FROM client_sessions;

ALTER TABLE client_sessions RENAME COLUMN rustdesk_token TO token_hash;
ALTER TABLE client_sessions ALTER COLUMN token_hash TYPE VARCHAR(64);

ALTER INDEX idx_client_sessions_rustdesk_token RENAME TO idx_client_sessions_token_hash;

COMMENT ON COLUMN client_sessions.token_hash IS
    'Lowercase hex SHA-256 of the session token. The plaintext is returned to the client once and never stored, so a database read does not yield live sessions.';
