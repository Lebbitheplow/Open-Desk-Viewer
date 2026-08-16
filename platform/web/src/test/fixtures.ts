import type {
  AuditEvent,
  Customer,
  Dashboard,
  Device,
  DeviceDetail,
  DeviceGroup,
  DevicePassword,
  EnrollmentToken,
  Paginated,
  Session,
  Settings,
  SupportGroup,
  User,
} from '../lib/api';

export function page<T>(data: T[], overrides: Partial<Paginated<T>> = {}): Paginated<T> {
  return {
    total: data.length,
    current: 1,
    pageSize: 20,
    totalPages: 1,
    data,
    ...overrides,
  };
}

export const device: Device = {
  id: '11111111-1111-1111-1111-111111111111',
  rustdesk_id: '100000001',
  name: 'acme-hq-01',
  hostname: 'acme-hq-01.local',
  os: 'Windows 11',
  state: 'ACTIVE',
  connectivity: 'ONLINE',
  last_seen_at: '2026-08-12T09:00:00Z',
  customer_id: '22222222-2222-2222-2222-222222222222',
  customer_name: 'Acme',
  created_at: '2026-08-01T09:00:00Z',
  updated_at: '2026-08-12T09:00:00Z',
};

export const discoveredDevice: Device = {
  ...device,
  id: '11111111-1111-1111-1111-111111111112',
  rustdesk_id: '100000005',
  name: 'unclaimed-01',
  state: 'DISCOVERED',
  connectivity: 'UNKNOWN',
  customer_name: undefined,
};

export const deviceDetail: DeviceDetail = {
  ...device,
  serial_number: 'SN-0001',
  groups: [{ id: '33333333-3333-3333-3333-333333333333', name: 'Acme HQ' }],
  sysinfo: {
    cpu: 'AMD Ryzen 7',
    memory: '32 GB',
    disk: '1 TB',
    collected_at: '2026-08-12T08:00:00Z',
  },
};

export const dashboard: Dashboard = {
  stats: {
    total_devices: 42,
    discovered_devices: 3,
    active_devices: 39,
    stale_devices: 4,
    offline_devices: 6,
    online_now: 29,
    total_customers: 7,
    total_technicians: 5,
    connections_24h: 18,
  },
  recent_connections: [
    {
      id: '44444444-4444-4444-4444-444444444444',
      device_id: device.id,
      device_name: device.name,
      user_id: 2,
      user_email: 'tech1@example.com',
      start_time: '2026-08-12T08:30:00Z',
      status: 'ENDED',
      note: '',
    },
  ],
};

export const customer: Customer = {
  id: '22222222-2222-2222-2222-222222222222',
  code: 'ACME',
  name: 'Acme',
  device_count: 12,
  created_at: '2026-07-01T09:00:00Z',
  updated_at: '2026-07-01T09:00:00Z',
};

export const deviceGroup: DeviceGroup = {
  id: '33333333-3333-3333-3333-333333333333',
  name: 'Acme HQ',
  member_count: 12,
  created_at: '2026-07-01T09:00:00Z',
  updated_at: '2026-07-01T09:00:00Z',
};

export const supportGroup: SupportGroup = {
  id: '55555555-5555-5555-5555-555555555555',
  name: 'Team One',
  member_count: 2,
  created_at: '2026-07-01T09:00:00Z',
  updated_at: '2026-07-01T09:00:00Z',
};

export const supportGroupDetail: SupportGroup = {
  ...supportGroup,
  technicians: [
    { id: 2, email: 'tech1@example.com', display_name: 'Tech One', active: true },
  ],
  device_groups: [{ id: deviceGroup.id, name: deviceGroup.name }],
};

export const user: User = {
  id: 2,
  keycloak_subject: 'sub-tech1',
  email: 'tech1@example.com',
  display_name: 'Tech One',
  active: true,
  roles: ['Technician'],
  last_login: '2026-08-11T09:00:00Z',
  created_at: '2026-07-01T09:00:00Z',
};

// A password the device has confirmed, which is the ordinary steady state. The
// pending case is built from this in the test that needs it, so the difference
// between the two is visible at the point it matters.
export const devicePassword: DevicePassword = {
  password: 'abcdefghjkmnpqrs',
  version: 2,
  applied_version: 2,
  applied: true,
  applied_at: '2026-08-12T09:00:05Z',
  rotated_at: '2026-08-12T09:00:00Z',
  delivered_at_heartbeat: true,
};

export const session: Session = {
  id: '66666666-6666-6666-6666-666666666666',
  device_id: device.id,
  device_name: device.name,
  user_id: 2,
  user_email: 'tech1@example.com',
  start_time: '2026-08-12T08:30:00Z',
  end_time: '2026-08-12T08:45:00Z',
  duration_seconds: 900,
  status: 'ENDED',
  note: 'Replaced the printer driver',
};

export const auditEvent: AuditEvent = {
  id: '77777777-7777-7777-7777-777777777777',
  event_type: 'device.reassigned',
  user_id: 1,
  user_email: 'admin@example.com',
  device_id: device.id,
  device_name: device.name,
  description: 'Device reassigned to a different customer',
  created_at: '2026-08-12T07:00:00Z',
};

export const enrollmentToken: EnrollmentToken = {
  id: '88888888-8888-8888-8888-888888888888',
  prefix: 'abc12345',
  customer_id: customer.id,
  max_uses: 5,
  uses: 1,
  expires_at: '2026-09-12T09:00:00Z',
  created_at: '2026-08-12T09:00:00Z',
};

export const settings: Settings = {
  address_book_max_peers: 100,
  audit_retention_days: 90,
  device_stale_after_seconds: 300,
  device_offline_after_seconds: 600,
  editable: false,
  // The four roles migration 000001 seeds, spelled the way the database
  // spells them. "Support Manager", not "Manager".
  roles: ['Administrator', 'Support Manager', 'Technician', 'Read Only'],
  caller_roles: ['Administrator'],
};
