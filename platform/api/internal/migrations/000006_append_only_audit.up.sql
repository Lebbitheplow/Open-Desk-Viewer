-- Make audit_events append-only.
--
-- The audit log is what this product is sold on. Until now the application
-- could rewrite or delete any row in it, so a compromised API process, or a bug,
-- could quietly edit the evidence of what it had done. 2.1 stopped anyone
-- forging an entry through the HTTP surface; this stops entries being altered
-- once written.
--
-- The plan called for revoking UPDATE and DELETE from the application's database
-- role. That does not work as written in this deployment: the API connects as
-- POSTGRES_USER, which owns these tables, and a table owner is not restricted by
-- its own grants. Doing it properly means a second, non-owning role for runtime
-- and the owner only for migrations, which is a deployment change: two credential
-- sets in compose, and a failure mode where the wrong one is used. That is worth
-- doing and is recorded as the remaining half of 3.5.
--
-- A trigger gets the property now, for every connection, whatever role it uses.
--
-- What it does not stop, stated plainly: an owner can disable the trigger, and
-- TRUNCATE does not fire row-level triggers at all, so a role with TRUNCATE
-- rights can still empty the table in one statement. Both are single deliberate
-- DDL-level acts rather than a stray UPDATE, and both are visible in a schema
-- review; neither is prevented here. Closing them needs the non-owning runtime
-- role described above.
-- First, stop anything else needing to write to audit_events.
--
-- The four foreign keys out of this table were declared ON DELETE SET NULL, so
-- deleting a customer, user, device or support group issued an UPDATE against
-- every audit row that referenced it. With the trigger below in place that
-- UPDATE fails and the delete fails with it, which is how this was found:
-- TestCustomerCRUD started returning 500.
--
-- Loosening the trigger to permit "only the foreign keys changed" would have
-- left a hole exactly where it matters, because nulling user_id is how you
-- erase who did something without touching what was done.
--
-- So the constraints go instead. An audit record is a statement about the past,
-- and it has to outlive the rows it refers to: an event that says "admin@example
-- deleted customer Acme" must not lose its actor when that account is later
-- removed. The columns keep the ids; the joins in apiv1 are LEFT JOINs already
-- and return a null name for an entity that no longer exists, which is the
-- truthful answer. The cost is that nothing at the database level checks these
-- ids on insert, which is acceptable for a table only one package writes to.
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_user_id_fkey;
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_device_id_fkey;
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_customer_id_fkey;
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_support_group_id_fkey;

CREATE OR REPLACE FUNCTION audit_events_append_only() RETURNS trigger AS $$
BEGIN
    -- The retention worker is the one legitimate deleter. It announces itself
    -- with a transaction-local setting, so the exemption cannot leak into
    -- another statement on the same connection.
    IF TG_OP = 'DELETE' AND current_setting('odv.audit_retention', true) = 'on' THEN
        RETURN OLD;
    END IF;

    RAISE EXCEPTION 'audit_events is append-only (attempted %)', TG_OP
        USING HINT = 'Retention deletion must SET LOCAL odv.audit_retention = ''on'' inside its transaction.';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_events_no_update
    BEFORE UPDATE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();

CREATE TRIGGER audit_events_no_delete
    BEFORE DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();

COMMENT ON FUNCTION audit_events_append_only() IS
    'Refuses UPDATE and DELETE on audit_events. Retention deletes are allowed only inside a transaction that has SET LOCAL odv.audit_retention = on.';
