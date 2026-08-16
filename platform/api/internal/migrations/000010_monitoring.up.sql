-- Monitoring and notifications: items 6.5 and 6.6.
--
-- The platform already knew whether a device was up. devices.connectivity is
-- recomputed by the worker every minute, and the heartbeat sets it back to
-- ONLINE. What it did not have was *history*: a column holds one value, so
-- "this site's machines have been dropping out every afternoon" was not a
-- question anyone could ask, and "tell me when a machine goes down" had nothing
-- to fire on.
--
-- So this is not new data collection. It is a record of the transitions the
-- existing recomputation already performs, plus somewhere to send them.

-- One row per change of connectivity, written by the worker at the moment it
-- changes the device.
--
-- Transitions rather than samples. A sample per device per minute is a row per
-- device per minute forever, and answers the same questions worse: an operator
-- asks when a machine went down and for how long, not what it was doing at
-- 14:32. A fleet that is behaving costs no rows at all.
CREATE TABLE device_connectivity_events (
    id BIGSERIAL PRIMARY KEY,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,

    -- VARCHAR rather than an enum, matching devices.connectivity, which is a
    -- VARCHAR(50) despite reading like an enum. Introducing a type here that
    -- the column it mirrors does not use would mean two definitions of the same
    -- vocabulary and a cast on every comparison.
    previous_connectivity VARCHAR(50),
    connectivity VARCHAR(50) NOT NULL,

    -- How long the device was in the previous state. Computed at write time
    -- from devices.last_seen_at rather than by joining to the previous row,
    -- because that join is the expensive part of every report that would use
    -- it.
    previous_duration_seconds BIGINT,

    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_connectivity_events_device ON device_connectivity_events(device_id, occurred_at DESC);
CREATE INDEX idx_connectivity_events_time ON device_connectivity_events(occurred_at DESC);

COMMENT ON TABLE device_connectivity_events IS
    'One row per connectivity change, written by the worker. Transitions, not samples: a healthy fleet costs no rows.';

-- Where to send them.
--
-- Webhooks rather than email, and the reason is worth recording because the
-- plan left the choice open. Email needs an SMTP server, credentials, a sender
-- domain with SPF and DKIM, and a bounce story, none of which this deployment
-- has and all of which have to be right before the first message is useful.
-- A webhook needs a URL. An operator who wants email points the webhook at
-- something that sends it, which is a thing they already have.
CREATE TABLE notification_targets (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR(255) NOT NULL,
    url TEXT NOT NULL,

    -- Signs the body with HMAC-SHA256, so the receiver can tell a delivery from
    -- this platform from anything else that learned the URL. Optional: a
    -- receiver on a private network may not care, and requiring a secret nobody
    -- checks is theatre.
    secret TEXT,

    -- Which events to send. An empty array means all of them, which is what an
    -- operator setting up their first target wants.
    event_types TEXT[] NOT NULL DEFAULT '{}',

    enabled BOOLEAN NOT NULL DEFAULT true,

    -- Delivery health, so a target that has been failing for a week is visible
    -- rather than silently dropping everything.
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    last_failure_reason TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_notification_targets_name ON notification_targets(name);

-- The outbox. A notification is written here in the same transaction as the
-- thing it describes and delivered later by the worker.
--
-- This is the difference between a notification system and an HTTP call in a
-- handler. Posting inline makes the caller wait for somebody else's server,
-- fails the operation when that server is down, and loses the notification
-- entirely if the process restarts mid-flight. A row survives all three.
CREATE TABLE notification_deliveries (
    id BIGSERIAL PRIMARY KEY,
    target_id UUID NOT NULL REFERENCES notification_targets(id) ON DELETE CASCADE,

    event_type VARCHAR(100) NOT NULL,
    payload JSONB NOT NULL,

    attempts INTEGER NOT NULL DEFAULT 0,
    -- When to try next. Set forward on each failure, so retrying is a matter of
    -- selecting rows whose time has come rather than of keeping a timer.
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ,
    -- Set when the attempt budget runs out. A row with this set is not retried
    -- and is not deleted: an operator needs to see what was never delivered.
    abandoned_at TIMESTAMPTZ,
    last_error TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_notification_deliveries_pending
    ON notification_deliveries(next_attempt_at)
    WHERE delivered_at IS NULL AND abandoned_at IS NULL;

COMMENT ON TABLE notification_deliveries IS
    'Outbox. Written with the event, delivered by the worker, retried with backoff, and kept after abandonment so an operator can see what never arrived.';
