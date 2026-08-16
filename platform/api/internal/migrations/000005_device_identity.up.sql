-- Device identity, and the heartbeat control channel that depends on it.
--
-- Until now a device was whatever it said it was: /api/heartbeat took a
-- rustdesk_id from an unauthenticated body and registered it. Anyone could
-- insert device rows, squat an id before the real device enrolled, or forge
-- liveness for a device that was switched off. The enrollment feature that was
-- supposed to prevent this existed but had no callers.
--
-- A device now redeems an enrollment token once and receives a secret. Every
-- later heartbeat presents that secret, and an id with no valid credential is
-- recorded as an observation rather than becoming a device.

-- One credential per device. The secret is 32 bytes from crypto/rand, so a
-- single SHA-256 is the right verifier: there is no low-entropy input to
-- brute-force, and the check happens on every heartbeat from every device.
-- (Contrast user_credentials, where Argon2id is required precisely because a
-- password is guessable.)
CREATE TABLE device_credentials (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    secret_hash VARCHAR(64) NOT NULL,
    enrolled_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    enrolled_by_token UUID REFERENCES enrollment_tokens(id) ON DELETE SET NULL,
    enrolled_from INET,
    last_used_at TIMESTAMPTZ,
    rotated_at TIMESTAMPTZ,
    -- Revocation is a timestamp rather than a delete, so the audit trail keeps
    -- the fact that this device was once enrolled.
    revoked_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_device_credentials_secret_hash ON device_credentials(secret_hash);

COMMENT ON COLUMN device_credentials.secret_hash IS
    'Lowercase hex SHA-256 of the device secret. The plaintext is returned once at enrollment and never stored.';

-- A heartbeat from an id we have no credential for. This is what replaces
-- auto-registration: the sighting is recorded so an operator can see a device
-- trying to reach the deployment, but nothing about it enters the fleet.
--
-- Keyed by rustdesk_id with an upsert, so a flood from one id costs one row
-- rather than one row per request.
CREATE TABLE device_observations (
    rustdesk_id VARCHAR(255) PRIMARY KEY,
    device_uuid VARCHAR(255),
    client_ip INET,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sightings BIGINT NOT NULL DEFAULT 1
);

CREATE INDEX idx_device_observations_last_seen ON device_observations(last_seen_at DESC);

-- Pending disconnects for the heartbeat control channel. The stock client
-- terminates the connection ids it receives in the response's "disconnect"
-- array (src/hbbs_http/sync.rs:251-255), so a row here is a live session the
-- platform has decided to end.
--
-- delivered_at rather than a delete: a disconnect that was requested and
-- delivered is evidence, and the audit log is the point of this product.
CREATE TABLE device_disconnect_requests (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    conn_id INTEGER NOT NULL,
    requested_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ
);

CREATE INDEX idx_device_disconnect_pending
    ON device_disconnect_requests(device_id)
    WHERE delivered_at IS NULL;

-- Per-device configuration the platform pushes through the heartbeat response.
--
-- modified_at is Unix seconds and is what the client echoes back in its next
-- heartbeat, so the strategy is only sent when it has actually changed
-- (sync.rs:242-243, 256-262).
--
-- Deliberate boundary with 1.3: keys baked into the client's OVERWRITE_SETTINGS
-- cannot be changed by a pushed strategy, which is the point. The application
-- keeps a separate allowlist of keys it is willing to push, and the two sets
-- must not overlap.
CREATE TABLE device_strategies (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    config_options JSONB NOT NULL DEFAULT '{}'::jsonb,
    modified_at BIGINT NOT NULL DEFAULT extract(epoch FROM now())::bigint,
    updated_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Enrollment token semantics, which the Go code and the SQL disagreed about.
--
-- ConsumeToken required expires_at > now(), so a token created without an
-- expiry (NULL) could never be redeemed. NULL now means "no expiry", matching
-- how max_uses NULL already meant "unlimited". The handler used to send Go's
-- zero time and 0 for these, which produced a token that was unusable in both
-- directions; that is fixed in the Go, and this comment records the contract.
COMMENT ON COLUMN enrollment_tokens.expires_at IS
    'NULL means the token does not expire. A non-NULL value is enforced by ConsumeToken.';
COMMENT ON COLUMN enrollment_tokens.max_uses IS
    'NULL means unlimited redemptions. A non-NULL value is enforced by ConsumeToken.';

-- uses is compared with < max_uses, so a NULL would make every comparison
-- unknown and silently reject the token.
UPDATE enrollment_tokens SET uses = 0 WHERE uses IS NULL;
ALTER TABLE enrollment_tokens ALTER COLUMN uses SET DEFAULT 0;
ALTER TABLE enrollment_tokens ALTER COLUMN uses SET NOT NULL;

-- The Go code wrote the token hash as a raw [32]byte into a VARCHAR(64) column
-- and the new code writes lowercase hex, so any existing row would never match
-- a presented token again. Nothing is lost by deleting them: no token could be
-- redeemed before this migration, because ConsumeToken required
-- expires_at > now() while the handler stored Go's zero time.
DELETE FROM enrollment_tokens;
