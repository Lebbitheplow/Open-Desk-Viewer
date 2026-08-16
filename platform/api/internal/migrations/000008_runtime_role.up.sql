-- The non-owning runtime role, which is the half of 3.5 the trigger cannot do.
--
-- Migration 000006 made audit_events append-only with a trigger that refuses
-- UPDATE and DELETE. That is worth having, and it is not enough on its own: the
-- API connected as POSTGRES_USER, which owns the tables, and an owner can
-- disable its own trigger, and TRUNCATE does not fire row triggers at all. So
-- the append-only guarantee held against a stray UPDATE and not against the
-- process most likely to be compromised.
--
-- This creates odv_app, the role the API serves requests as. It is not the
-- owner, so it cannot ALTER TABLE ... DISABLE TRIGGER, and it is not granted
-- TRUNCATE. On audit_events it holds SELECT and INSERT and nothing else, so the
-- trigger is now the second line rather than the only one: the grant refuses
-- first, and an attacker who found a way to set odv.audit_retention would still
-- be refused by the privilege system.
--
-- Deliberately not covered: the retention pass, which genuinely has to delete
-- audit rows. cmd/worker connects as the owner for exactly that reason. It has
-- no HTTP surface of any kind, which is what makes that an acceptable trade
-- rather than a hole; platform/README.md says so out loud.
--
-- The role is created here rather than only in the compose init script so that
-- the privilege model lives in one place and is testable. The init script sets
-- LOGIN and a password; this migration is happy either way, and creates the role
-- NOLOGIN if nobody has. A database whose owner has no CREATEROLE (a managed
-- service, typically) raises a notice and leaves the grants unapplied, which is
-- visible in the migration output rather than silent.
DO $$
DECLARE
    have_role boolean;
BEGIN
    SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'odv_app') INTO have_role;

    IF NOT have_role THEN
        BEGIN
            CREATE ROLE odv_app NOLOGIN;
            have_role := true;
        EXCEPTION WHEN insufficient_privilege THEN
            RAISE NOTICE
                'Cannot create the odv_app runtime role: % lacks CREATEROLE. Create it by hand and re-run this migration to apply its grants.',
                current_user;
        END;
    END IF;

    IF NOT have_role THEN
        RETURN;
    END IF;

    EXECUTE format('GRANT CONNECT ON DATABASE %I TO odv_app', current_database());
    GRANT USAGE ON SCHEMA public TO odv_app;

    -- Everything the API does at request time, and nothing else. No TRUNCATE,
    -- no REFERENCES, no CREATE on the schema.
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO odv_app;
    GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO odv_app;

    -- The exception this whole migration exists for.
    REVOKE UPDATE, DELETE, TRUNCATE ON audit_events FROM odv_app;

    -- schema_migrations belongs to the migration runner. The API reads nothing
    -- from it and writing to it would let a compromised API convince the next
    -- deployment that a migration had already run.
    REVOKE ALL ON schema_migrations FROM odv_app;

    -- So that a table added by a later migration is covered without anyone
    -- having to remember. The default applies to objects created by the role
    -- running the migrations, which is the owner.
    EXECUTE format(
        'ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO odv_app',
        current_user);
    EXECUTE format(
        'ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO odv_app',
        current_user);
END $$;

COMMENT ON TABLE audit_events IS
    'Append-only. The odv_app runtime role holds SELECT and INSERT only; the trigger from 000006 is the second line. Only the owner, used by cmd/worker, may expire rows, and only inside a transaction that has set odv.audit_retention.';
