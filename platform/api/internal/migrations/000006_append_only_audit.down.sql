DROP TRIGGER IF EXISTS audit_events_no_delete ON audit_events;
DROP TRIGGER IF EXISTS audit_events_no_update ON audit_events;
DROP FUNCTION IF EXISTS audit_events_append_only();

-- Restoring the foreign keys can fail: rows written while they were absent may
-- reference entities that have since been deleted. NOT VALID adds the
-- constraint for new rows without re-checking the existing ones, which is the
-- only way this rollback can be relied on.
ALTER TABLE audit_events
    ADD CONSTRAINT audit_events_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL NOT VALID;
ALTER TABLE audit_events
    ADD CONSTRAINT audit_events_device_id_fkey
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE SET NULL NOT VALID;
ALTER TABLE audit_events
    ADD CONSTRAINT audit_events_customer_id_fkey
    FOREIGN KEY (customer_id) REFERENCES customers(id) ON DELETE SET NULL NOT VALID;
ALTER TABLE audit_events
    ADD CONSTRAINT audit_events_support_group_id_fkey
    FOREIGN KEY (support_group_id) REFERENCES support_groups(id) ON DELETE SET NULL NOT VALID;
