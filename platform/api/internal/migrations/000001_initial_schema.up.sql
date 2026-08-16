-- OpenDeskViewer Database Schema
-- Version: 1.1
-- Description: Core tables for identity, fleet management, and authorization

-- Create extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pg_trgm";

-- ========== Enums ==========
CREATE TYPE connection_status AS ENUM ('REQUESTED', 'STARTED', 'ENDED', 'FAILED');

-- ========== Users ==========
CREATE TABLE users (
    id BIGSERIAL PRIMARY KEY,
    keycloak_subject VARCHAR(255) UNIQUE NOT NULL,
    email VARCHAR(255) UNIQUE NOT NULL,
    display_name VARCHAR(255) NOT NULL,
    active BOOLEAN DEFAULT true NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    last_login TIMESTAMPTZ
);

CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_keycloak_subject ON users(keycloak_subject);

-- ========== Roles ==========
CREATE TABLE roles (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) UNIQUE NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

-- Insert default roles
INSERT INTO roles (name, description) VALUES
    ('Administrator', 'Full system access'),
    ('Support Manager', 'Manage technicians and customers'),
    ('Technician', 'Remote support access'),
    ('Read Only', 'View-only access');

CREATE INDEX idx_roles_name ON roles(name);

-- ========== User Roles (many-to-many) ==========
CREATE TABLE user_roles (
    user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
    role_id BIGINT REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX idx_user_roles_user_id ON user_roles(user_id);
CREATE INDEX idx_user_roles_role_id ON user_roles(role_id);

-- ========== Support Groups ==========
CREATE TABLE support_groups (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR(255) NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_support_groups_name ON support_groups(name);

-- ========== User Support Groups (many-to-many) ==========
CREATE TABLE user_support_groups (
    user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
    support_group_id UUID REFERENCES support_groups(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, support_group_id)
);

CREATE INDEX idx_user_support_groups_user_id ON user_support_groups(user_id);
CREATE INDEX idx_user_support_groups_group_id ON user_support_groups(support_group_id);

-- ========== Customers ==========
CREATE TABLE customers (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR(255) NOT NULL,
    code VARCHAR(100) UNIQUE NOT NULL,
    external_id VARCHAR(255) UNIQUE,
    description TEXT,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_customers_name ON customers(name);
CREATE INDEX idx_customers_code ON customers(code);
CREATE INDEX idx_customers_external_id ON customers(external_id);

-- ========== Locations ==========
CREATE TABLE locations (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    external_id VARCHAR(255) UNIQUE,
    address TEXT,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_locations_customer_id ON locations(customer_id);
CREATE INDEX idx_locations_name ON locations(name);
CREATE INDEX idx_locations_address ON locations USING gin (address gin_trgm_ops);
CREATE INDEX idx_locations_external_id ON locations(external_id);

-- ========== Device Groups ==========
CREATE TABLE device_groups (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR(255) NOT NULL,
    parent_id UUID REFERENCES device_groups(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_device_groups_name ON device_groups(name);
CREATE INDEX idx_device_groups_parent_id ON device_groups(parent_id);

-- ========== Support Group Device Groups (many-to-many) ==========
CREATE TABLE support_group_device_groups (
    support_group_id UUID REFERENCES support_groups(id) ON DELETE CASCADE,
    device_group_id UUID REFERENCES device_groups(id) ON DELETE CASCADE,
    PRIMARY KEY (support_group_id, device_group_id)
);

CREATE INDEX idx_support_group_device_groups_sg_id ON support_group_device_groups(support_group_id);
CREATE INDEX idx_support_group_device_groups_dg_id ON support_group_device_groups(device_group_id);

-- ========== Devices ==========
CREATE TABLE devices (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    rustdesk_id VARCHAR(255) UNIQUE NOT NULL,
    name VARCHAR(255) NOT NULL,
    uuid VARCHAR(255) NOT NULL,
    customer_id UUID REFERENCES customers(id) ON DELETE SET NULL,
    location_id UUID REFERENCES locations(id) ON DELETE SET NULL,
    serial_number VARCHAR(255),
    state VARCHAR(50) DEFAULT 'DISCOVERED' NOT NULL,
    connectivity VARCHAR(50) DEFAULT 'UNKNOWN' NOT NULL,
    last_seen_at TIMESTAMPTZ,
    client_version VARCHAR(100),
    os VARCHAR(255),
    hostname VARCHAR(255),
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_devices_rustdesk_id ON devices(rustdesk_id);
CREATE INDEX idx_devices_uuid ON devices(uuid);
CREATE INDEX idx_devices_customer_id ON devices(customer_id);
CREATE INDEX idx_devices_location_id ON devices(location_id);
CREATE INDEX idx_devices_state ON devices(state);
CREATE INDEX idx_devices_connectivity ON devices(connectivity);
CREATE INDEX idx_devices_name ON devices USING gin (name gin_trgm_ops);
CREATE INDEX idx_devices_hostname ON devices USING gin (hostname gin_trgm_ops);
CREATE UNIQUE INDEX idx_devices_serial_customer ON devices(serial_number, customer_id) WHERE serial_number IS NOT NULL;
CREATE UNIQUE INDEX idx_devices_uuid_unique ON devices(uuid);

-- Device group members (many-to-many)
CREATE TABLE device_group_members (
    device_group_id UUID REFERENCES device_groups(id) ON DELETE CASCADE,
    device_id UUID REFERENCES devices(id) ON DELETE CASCADE,
    PRIMARY KEY (device_group_id, device_id)
);

CREATE INDEX idx_device_group_members_group_id ON device_group_members(device_group_id);
CREATE INDEX idx_device_group_members_device_id ON device_group_members(device_id);

-- ========== Enrollment Tokens ==========
CREATE TABLE enrollment_tokens (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    token_hash VARCHAR(64) NOT NULL,
    prefix VARCHAR(8) NOT NULL,
    customer_id UUID REFERENCES customers(id) ON DELETE CASCADE,
    location_id UUID REFERENCES locations(id) ON DELETE SET NULL,
    device_group_id UUID REFERENCES device_groups(id) ON DELETE SET NULL,
    device_group_name VARCHAR(255),
    address_book_name VARCHAR(255),
    max_uses INT,
    uses INT DEFAULT 0,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    revoked_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_enrollment_tokens_hash ON enrollment_tokens(token_hash);
CREATE INDEX idx_enrollment_tokens_prefix ON enrollment_tokens(prefix);
CREATE INDEX idx_enrollment_tokens_customer_id ON enrollment_tokens(customer_id);
CREATE INDEX idx_enrollment_tokens_expires_at ON enrollment_tokens(expires_at);

-- ========== Client Sessions ==========
CREATE TABLE client_sessions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
    rustdesk_token VARCHAR(255) UNIQUE NOT NULL,
    rustdesk_id VARCHAR(255),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_client_sessions_user_id ON client_sessions(user_id);
CREATE INDEX idx_client_sessions_rustdesk_token ON client_sessions(rustdesk_token);
CREATE INDEX idx_client_sessions_expires_at ON client_sessions(expires_at);

-- ========== Connection Sessions ==========
CREATE TABLE connection_sessions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    device_id UUID REFERENCES devices(id) ON DELETE CASCADE,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    support_group_id UUID REFERENCES support_groups(id) ON DELETE SET NULL,
    client_id VARCHAR(255),
    protocol VARCHAR(50),
    start_time TIMESTAMPTZ DEFAULT now() NOT NULL,
    end_time TIMESTAMPTZ,
    duration_seconds INTEGER,
    connected_from VARCHAR(100),
    status connection_status DEFAULT 'REQUESTED' NOT NULL
);

CREATE INDEX idx_connection_sessions_device_id ON connection_sessions(device_id);
CREATE INDEX idx_connection_sessions_user_id ON connection_sessions(user_id);
CREATE INDEX idx_connection_sessions_start_time ON connection_sessions(start_time);
CREATE INDEX idx_connection_sessions_status ON connection_sessions(status);

-- ========== Audit Events ==========
CREATE TABLE audit_events (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    event_type VARCHAR(100) NOT NULL,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    device_id UUID REFERENCES devices(id) ON DELETE SET NULL,
    support_group_id UUID REFERENCES support_groups(id) ON DELETE SET NULL,
    customer_id UUID REFERENCES customers(id) ON DELETE SET NULL,
    description TEXT,
    metadata JSONB,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_audit_events_event_type ON audit_events(event_type);
CREATE INDEX idx_audit_events_user_id ON audit_events(user_id);
CREATE INDEX idx_audit_events_device_id ON audit_events(device_id);
CREATE INDEX idx_audit_events_created_at ON audit_events(created_at);

-- ========== API Clients ==========
CREATE TABLE api_clients (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR(255) NOT NULL,
    client_id VARCHAR(255) UNIQUE NOT NULL,
    client_secret_hash VARCHAR(255) NOT NULL,
    scopes TEXT[],
    active BOOLEAN DEFAULT true NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_api_clients_client_id ON api_clients(client_id);

-- ========== API Tokens ==========
CREATE TABLE api_tokens (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    client_id UUID REFERENCES api_clients(id) ON DELETE CASCADE,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    token_hash VARCHAR(255) UNIQUE NOT NULL,
    scopes TEXT[],
    expires_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_api_tokens_client_id ON api_tokens(client_id);
CREATE INDEX idx_api_tokens_user_id ON api_tokens(user_id);
CREATE INDEX idx_api_tokens_expires_at ON api_tokens(expires_at);

-- ========== Device sysinfo cache ==========
CREATE TABLE device_sysinfo (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    device_id UUID UNIQUE NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    cpu VARCHAR(255),
    memory VARCHAR(255),
    disk VARCHAR(255),
    network TEXT[],
    software TEXT[],
    collected_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

CREATE INDEX idx_device_sysinfo_device_id ON device_sysinfo(device_id);

-- ========== Views ==========
-- View: devices accessible by a technician (dgm2 self-join removed)
CREATE VIEW accessible_devices AS
SELECT DISTINCT d.*
FROM devices d
JOIN device_group_members dgm ON d.id = dgm.device_id
JOIN device_groups dg ON dgm.device_group_id = dg.id
JOIN support_group_device_groups sgdg ON dg.id = sgdg.device_group_id
JOIN user_support_groups usg ON sgdg.support_group_id = usg.support_group_id
WHERE usg.user_id = COALESCE(NULLIF(current_setting('app.current_user_id', true), ''), '0')::BIGINT
  AND d.state = 'ACTIVE';

-- View: technician device access summary
CREATE VIEW technician_device_access AS
SELECT 
    u.id AS user_id,
    u.email,
    u.display_name,
    COUNT(DISTINCT d.id) AS accessible_device_count,
    COUNT(DISTINCT d.customer_id) AS customer_count,
    MAX(d.last_seen_at) AS last_activity
FROM users u
JOIN user_support_groups usg ON u.id = usg.user_id
JOIN support_group_device_groups sgdg ON usg.support_group_id = sgdg.support_group_id
JOIN device_group_members dgm ON sgdg.device_group_id = dgm.device_group_id
JOIN devices d ON dgm.device_id = d.id
WHERE u.active = true
GROUP BY u.id, u.email, u.display_name;

-- ========== Functions ==========
-- Update updated_at timestamp
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ language 'plpgsql';

-- Triggers for updated_at
CREATE TRIGGER update_users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_support_groups_updated_at BEFORE UPDATE ON support_groups
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_customers_updated_at BEFORE UPDATE ON customers
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_locations_updated_at BEFORE UPDATE ON locations
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_device_groups_updated_at BEFORE UPDATE ON device_groups
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_devices_updated_at BEFORE UPDATE ON devices
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_device_sysinfo_updated_at BEFORE UPDATE ON device_sysinfo
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
