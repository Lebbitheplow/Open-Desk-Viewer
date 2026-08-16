-- Dropping these loses the connectivity history and any notification that had
-- not yet been delivered. Nothing else depends on them: devices.connectivity
-- still holds the current state, so the fleet view is unaffected.
DROP TABLE IF EXISTS notification_deliveries;
DROP TABLE IF EXISTS notification_targets;
DROP TABLE IF EXISTS device_connectivity_events;
