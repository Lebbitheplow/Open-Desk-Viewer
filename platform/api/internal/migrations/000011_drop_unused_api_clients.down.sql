-- Recreate api_clients and api_tokens exactly as 000001 defined them.
--
-- The rows are not recoverable and there were none to lose: nothing ever wrote
-- to either table.

CREATE TABLE api_clients (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR(255) NOT NULL,
    client_id VARCHAR(255) UNIQUE NOT NULL,
    client_secret_hash VARCHAR(255) NOT NULL,
    scopes TEXT[],
    active BOOLEAN DEFAULT true NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_api_clients_client_id ON api_clients(client_id);

CREATE TABLE api_tokens (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    client_id UUID REFERENCES api_clients(id) ON DELETE CASCADE,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    token_hash VARCHAR(255) UNIQUE NOT NULL,
    scopes TEXT[],
    expires_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_api_tokens_client_id ON api_tokens(client_id);
CREATE INDEX idx_api_tokens_user_id ON api_tokens(user_id);
CREATE INDEX idx_api_tokens_expires_at ON api_tokens(expires_at);

-- 000008 grants the runtime role its privileges through ALTER DEFAULT
-- PRIVILEGES, which covers tables created later but not tables recreated on a
-- rollback by a different session. Grant explicitly so a rolled-back deployment
-- comes back up.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'odv_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON api_clients, api_tokens TO odv_app;
    END IF;
END
$$;
