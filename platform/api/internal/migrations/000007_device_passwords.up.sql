-- Platform-managed per-device connection passwords.
--
-- A RustDesk device authenticates the person connecting to it with its own
-- password, and nothing about that password passed through this platform. So
-- "this technician may no longer reach that machine" was a statement about a
-- row in our database: whoever already knew the device's password kept it, and
-- the device would have gone on accepting it.
--
-- The platform now owns the password. It is generated here, encrypted at rest,
-- delivered to the device over the heartbeat channel, and released to a
-- technician only after an access check and only with an audit record. Ending
-- someone's access is therefore a rotation, which is an action on the machine
-- rather than a label on a row.
--
-- One row per device. There is no history table: a superseded password is worth
-- nothing to anyone and keeping it would mean keeping every credential a
-- technician was ever shown. The audit trail records that a rotation happened
-- and who caused it, which is the part that matters.
CREATE TABLE device_passwords (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,

    -- AES-256-GCM. The nonce is stored beside the ciphertext because GCM needs
    -- it to open and it is not secret; what must never repeat for one key is the
    -- nonce, and a fresh 12 bytes is drawn on every write.
    ciphertext BYTEA NOT NULL,
    nonce BYTEA NOT NULL,
    key_version INTEGER NOT NULL DEFAULT 1,

    -- A counter rather than a timestamp. Two rotations inside one second are an
    -- ordinary consequence of a script, and with a timestamp the second one
    -- would look to the device exactly like the first.
    version BIGINT NOT NULL DEFAULT 1,

    -- What the device has confirmed it is using. The confirmation is the device
    -- echoing the version back on its next heartbeat, so applied_version lagging
    -- version means a rotation that has not reached the machine yet: the old
    -- password still works there, and the portal has to say so rather than
    -- implying the rotation already took effect.
    applied_version BIGINT,
    applied_at TIMESTAMPTZ,

    rotated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE device_passwords IS
    'Platform-managed RustDesk connection password, one row per device, encrypted with DEVICE_PASSWORD_KEY.';
COMMENT ON COLUMN device_passwords.version IS
    'Incremented on every rotation. The device echoes the version it has applied on each heartbeat.';
COMMENT ON COLUMN device_passwords.applied_version IS
    'The version the device last confirmed. Lagging version means the rotation has not reached the device yet.';
