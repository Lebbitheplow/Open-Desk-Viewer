-- The state behind the RustDesk client's browser sign-in.
--
-- The client cannot host a redirect URI: it is a desktop or Android
-- application, so it opens a browser and then polls this API until the browser
-- half has finished. That is what /api/oidc/auth and /api/oidc/auth-query are
-- for, and it needs somewhere to keep the in-flight request between the two.
--
-- Nothing here is a credential that survives the flow. The polling handle and
-- the OAuth state are stored as SHA-256 hashes, exactly as client_sessions and
-- device_credentials store theirs, so a reader of this table cannot collect a
-- sign-in in progress. The session token itself is never stored here at all:
-- the callback records which user authenticated, and the poll mints the session
-- when it collects, so the plaintext token exists only in the one response that
-- carries it.
CREATE TABLE oidc_auth_requests (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),

    -- SHA-256 of the handle returned to the client and echoed on every poll.
    code_hash VARCHAR(64) NOT NULL UNIQUE,
    -- SHA-256 of the OAuth state parameter. The callback is an unauthenticated
    -- endpoint anyone can reach; matching the state is what ties an incoming
    -- authorization code to a request this server actually started.
    state_hash VARCHAR(64) NOT NULL UNIQUE,
    -- The PKCE verifier. Not hashed, because it has to be sent verbatim to
    -- Keycloak at the token exchange. It is useless without the authorization
    -- code, which never touches this table.
    code_verifier TEXT NOT NULL,

    -- What the client said it was. The poll has to present the same, so a
    -- handle lifted from one device cannot be redeemed by another.
    rustdesk_id VARCHAR(255) NOT NULL,
    device_uuid VARCHAR(255) NOT NULL,

    -- Filled in by the callback. A row with a user and no collected_at is a
    -- sign-in the browser has completed and the client has not picked up yet.
    user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
    completed_at TIMESTAMPTZ,
    collected_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Short. The client gives up after its own query timeout, and a pending
    -- sign-in that nobody completed is not worth keeping.
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_oidc_auth_requests_expiry ON oidc_auth_requests(expires_at);

COMMENT ON COLUMN oidc_auth_requests.code_verifier IS
    'PKCE verifier. odv-portal is a public client with no secret, so PKCE is what proves the token exchange comes from the party that started the flow.';
