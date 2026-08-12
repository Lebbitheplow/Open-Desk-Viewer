-- Down migration: 00001_initial_schema
-- Drops all tables, views, functions, and extensions created by the up migration

-- Drop triggers
DROP TRIGGER IF EXISTS update_device_sysinfo_updated_at ON device_sysinfo;
DROP TRIGGER IF EXISTS update_devices_updated_at ON devices;
DROP TRIGGER IF EXISTS update_device_groups_updated_at ON device_groups;
DROP TRIGGER IF EXISTS update_locations_updated_at ON locations;
DROP TRIGGER IF EXISTS update_customers_updated_at ON customers;
DROP TRIGGER IF EXISTS update_support_groups_updated_at ON support_groups;
DROP TRIGGER IF EXISTS update_users_updated_at ON users;

-- Drop functions
DROP FUNCTION IF EXISTS update_updated_at_column();

-- Drop views
DROP VIEW IF EXISTS technician_device_access;
DROP VIEW IF EXISTS accessible_devices;

-- Drop tables (in reverse dependency order)
DROP TABLE IF EXISTS device_sysinfo;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS api_clients;
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS connection_sessions;
DROP TABLE IF EXISTS client_sessions;
DROP TABLE IF EXISTS enrollment_tokens;
DROP TABLE IF EXISTS device_group_members;
DROP TABLE IF EXISTS devices;
DROP TABLE IF EXISTS support_group_device_groups;
DROP TABLE IF EXISTS device_groups;
DROP TABLE IF EXISTS locations;
DROP TABLE IF EXISTS customers;
DROP TABLE IF EXISTS user_support_groups;
DROP TABLE IF EXISTS support_groups;
DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS users;

-- Drop enum type
DROP TYPE IF EXISTS connection_status;

-- Drop extensions
DROP EXTENSION IF EXISTS "pg_trgm";
DROP EXTENSION IF EXISTS "uuid-ossp";
