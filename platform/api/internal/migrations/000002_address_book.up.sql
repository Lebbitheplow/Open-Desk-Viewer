-- OpenDeskViewer Address Book
-- Version: 2.0
-- Description: Storage for the personal address book the RustDesk client writes to.
--
-- Shared address books are NOT stored here. They are projections of the fleet:
-- one per support group the user belongs to, plus a fleet-wide book for
-- administrators, with the support group's own id used as the book's guid. That
-- keeps membership governed by device assignment rather than by ad-hoc edits in
-- the client, so there is nothing to keep in sync.

-- ========== Address book profiles ==========
CREATE TABLE ab_profiles (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    owner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    note TEXT,
    personal BOOLEAN DEFAULT false NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_ab_profiles_owner ON ab_profiles(owner_user_id);

-- One personal book per user. The partial index is what makes the
-- get-or-create in ResolvePersonalProfile a single atomic upsert.
CREATE UNIQUE INDEX idx_ab_profiles_one_personal_per_owner
    ON ab_profiles(owner_user_id) WHERE personal;

CREATE TRIGGER update_ab_profiles_updated_at
    BEFORE UPDATE ON ab_profiles
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ========== Address book peers ==========
CREATE TABLE ab_peers (
    id BIGSERIAL PRIMARY KEY,
    profile_id UUID NOT NULL REFERENCES ab_profiles(id) ON DELETE CASCADE,
    rustdesk_id VARCHAR(255) NOT NULL,
    username VARCHAR(255) DEFAULT '' NOT NULL,
    hostname VARCHAR(255) DEFAULT '' NOT NULL,
    platform VARCHAR(255) DEFAULT '' NOT NULL,
    alias VARCHAR(255) DEFAULT '' NOT NULL,
    note TEXT DEFAULT '' NOT NULL,
    -- The client sends a password hash for a personal book; it never leaves the
    -- owner's own book.
    hash TEXT DEFAULT '' NOT NULL,
    force_always_relay BOOLEAN DEFAULT false NOT NULL,
    rdp_port VARCHAR(16) DEFAULT '' NOT NULL,
    rdp_username VARCHAR(255) DEFAULT '' NOT NULL,
    tags TEXT[] DEFAULT '{}' NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    UNIQUE (profile_id, rustdesk_id)
);

CREATE INDEX idx_ab_peers_profile ON ab_peers(profile_id);

CREATE TRIGGER update_ab_peers_updated_at
    BEFORE UPDATE ON ab_peers
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ========== Address book tags ==========
CREATE TABLE ab_tags (
    id BIGSERIAL PRIMARY KEY,
    profile_id UUID NOT NULL REFERENCES ab_profiles(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    -- Flutter Color.value is a 32-bit ARGB value, which overflows int4.
    color BIGINT DEFAULT 0 NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    UNIQUE (profile_id, name)
);

CREATE INDEX idx_ab_tags_profile ON ab_tags(profile_id);
