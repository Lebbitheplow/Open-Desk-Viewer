-- Withdraw the runtime role's privileges.
--
-- The role itself is deliberately not dropped. A rollback happens on a running
-- deployment, and DROP ROLE would fail while the API still holds connections as
-- it, or succeed and take the API down with it. Revoking is reversible by
-- re-running the up; dropping a role that other objects may reference is not.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'odv_app') THEN
        RETURN;
    END IF;

    EXECUTE format(
        'ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM odv_app',
        current_user);
    EXECUTE format(
        'ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public REVOKE USAGE, SELECT ON SEQUENCES FROM odv_app',
        current_user);

    REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM odv_app;
    REVOKE ALL ON ALL TABLES IN SCHEMA public FROM odv_app;
    REVOKE USAGE ON SCHEMA public FROM odv_app;
    EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM odv_app', current_database());
END $$;

COMMENT ON TABLE audit_events IS
    'Append-only at the database level: a trigger refuses UPDATE and DELETE unless the transaction has set odv.audit_retention.';
