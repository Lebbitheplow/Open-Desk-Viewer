-- Recreate user_credentials and login_attempts exactly as 000004 defined them.
--
-- The rows are not recoverable, and in this deployment there were none to lose:
-- no code path could write a credential, which is why the tables were dropped.

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

-- 000008 grants the runtime role its privileges through ALTER DEFAULT
-- PRIVILEGES, which covers tables created later but not tables recreated on a
-- rollback by a different session. Grant explicitly so a rolled-back deployment
-- comes back up.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'odv_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON user_credentials, login_attempts TO odv_app;
        GRANT USAGE, SELECT ON SEQUENCE login_attempts_id_seq TO odv_app;
    END IF;
END
$$;
