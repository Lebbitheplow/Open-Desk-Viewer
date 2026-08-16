// The typed client for /api/v1.
//
// These types mirror internal/apiv1's Go structs. openapi.yaml carries the same
// surface and is the written contract; this file is what the pages compile
// against.

export interface Paginated<T> {
  total: number;
  current: number;
  pageSize: number;
  totalPages: number;
  data: T[];
}

export interface Device {
  id: string;
  rustdesk_id: string;
  name: string;
  hostname?: string;
  os?: string;
  state: string;
  connectivity: string;
  last_seen_at?: string;
  customer_id?: string;
  customer_name?: string;
  location_id?: string;
  location_name?: string;
  client_version?: string;
  serial_number?: string;
  created_at: string;
  updated_at: string;
}

export interface GroupSummary {
  id: string;
  name: string;
}

export interface Sysinfo {
  cpu?: string;
  memory?: string;
  disk?: string;
  network?: string[];
  software?: string[];
  collected_at: string;
}

export interface DeviceDetail extends Device {
  groups: GroupSummary[];
  sysinfo?: Sysinfo;
}

/**
 * The platform-managed connection password for one device.
 *
 * `applied` is the field that keeps the screen honest. A rotation changes the
 * platform's copy at once and reaches the device on its next heartbeat, so
 * until the device confirms the version the machine is still accepting the
 * previous password.
 */
export interface DevicePassword {
  password: string;
  version: number;
  applied_version: number | null;
  applied: boolean;
  applied_at: string | null;
  rotated_at: string;
  delivered_at_heartbeat: boolean;
}

export interface Customer {
  id: string;
  code: string;
  name: string;
  external_id?: string;
  description?: string;
  device_count: number;
  created_at: string;
  updated_at: string;
}

export interface Location {
  id: string;
  customer_id: string;
  name: string;
  external_id?: string;
  address?: string;
  created_at: string;
  updated_at: string;
}

export interface DeviceGroup {
  id: string;
  name: string;
  parent_id?: string;
  member_count: number;
  created_at: string;
  updated_at: string;
}

export interface Technician {
  id: number;
  email: string;
  display_name: string;
  active: boolean;
}

export interface SupportGroup {
  id: string;
  name: string;
  description?: string;
  technicians?: Technician[];
  device_groups?: GroupSummary[];
  member_count: number;
  created_at: string;
  updated_at: string;
}

export interface User {
  id: number;
  keycloak_subject: string;
  email: string;
  display_name: string;
  active: boolean;
  roles: string[];
  support_groups?: GroupSummary[];
  last_login?: string;
  created_at: string;
}

export interface Session {
  id: string;
  device_id?: string;
  device_name?: string;
  user_id?: number;
  user_email?: string;
  start_time: string;
  end_time?: string;
  duration_seconds?: number;
  status: string;
  connected_from?: string;
  note: string;
}

export interface AuditEvent {
  id: string;
  event_type: string;
  user_id?: number;
  user_email?: string;
  device_id?: string;
  device_name?: string;
  customer_id?: string;
  customer_name?: string;
  support_group_id?: string;
  description?: string;
  metadata?: Record<string, unknown>;
  created_at: string;
}

export interface Stats {
  total_devices: number;
  discovered_devices: number;
  active_devices: number;
  stale_devices: number;
  offline_devices: number;
  online_now: number;
  total_customers: number;
  total_technicians: number;
  connections_24h: number;
}

export interface Dashboard {
  stats: Stats;
  recent_connections: Session[];
}

export interface Settings {
  address_book_max_peers: number;
  audit_retention_days: number;
  device_stale_after_seconds: number;
  device_offline_after_seconds: number;
  editable: boolean;
  /**
   * Every role this deployment has, read from the roles table.
   *
   * The user screen used to hardcode ['Administrator', 'Manager', 'Technician'].
   * "Manager" is not a role that exists -- the seeded one is "Support Manager" --
   * so granting it always failed, and "Read Only" could not be granted at all
   * because it was never offered.
   */
  roles: string[];
  /** The signed-in user's own roles, so the portal can hide what would 403. */
  caller_roles: string[];
}

export interface EnrollmentToken {
  id: string;
  prefix: string;
  token?: string;
  customer_id: string;
  max_uses: number;
  uses: number;
  expires_at: string;
  created_at: string;
  revoked_at?: string;
}

export interface DeviceQuery {
  q?: string;
  state?: string;
  customer?: string;
  group?: string;
  current?: number;
  pageSize?: number;
}

/** The API returns {"error": "..."} on every failure path, so surface that. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

/**
 * getAccessToken is supplied by the auth layer at startup. The client does not
 * import the OIDC library itself: that would make every test that touches the
 * client need a configured identity provider.
 */
type TokenSource = () => string | undefined;

let tokenSource: TokenSource = () => undefined;

export function setTokenSource(source: TokenSource) {
  tokenSource = source;
}

const API_BASE_URL = import.meta.env.VITE_API_URL ?? '/api/v1';

function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') search.set(key, String(value));
  }
  const s = search.toString();
  return s ? `?${s}` : '';
}

export class ApiClient {
  constructor(private readonly baseUrl: string) {}

  private async request<T>(endpoint: string, options: RequestInit = {}): Promise<T> {
    const headers = new Headers(options.headers);
    headers.set('Content-Type', 'application/json');

    const token = tokenSource();
    if (token) headers.set('Authorization', `Bearer ${token}`);

    const response = await fetch(`${this.baseUrl}${endpoint}`, { ...options, headers });

    if (!response.ok) {
      // Every error path returns JSON, but a proxy in front of the API might
      // not, so falling back to the status text keeps the message useful.
      let message = response.statusText;
      try {
        const body: unknown = await response.json();
        if (
          typeof body === 'object' &&
          body !== null &&
          'error' in body &&
          typeof body.error === 'string'
        ) {
          message = body.error;
        }
      } catch {
        // keep statusText
      }
      throw new ApiError(response.status, message);
    }

    if (response.status === 204) return undefined as T;
    // The cast is the one unchecked assumption in this client: nothing at
    // runtime proves the body matches T. It is written out rather than hidden
    // behind an implicit any so that adding runtime validation later has an
    // obvious place to go.
    return (await response.json()) as T;
  }

  // Dashboard -------------------------------------------------------------
  getDashboard = () => this.request<Dashboard>('/stats/dashboard');

  // Devices ---------------------------------------------------------------
  getDevices = (params: DeviceQuery = {}) =>
    this.request<Paginated<Device>>(`/devices${query({ ...params })}`);

  getDevice = (id: string) => this.request<DeviceDetail>(`/devices/${id}`);

  getDeviceSessions = (id: string, current = 1, pageSize = 20) =>
    this.request<Paginated<Session>>(`/devices/${id}/sessions${query({ current, pageSize })}`);

  updateDevice = (id: string, body: { name?: string; customer_id?: string; location_id?: string }) =>
    this.request<{ success: boolean }>(`/devices/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    });

  claimDevice = (id: string, body: { customer_id: string; location_id?: string; name?: string }) =>
    this.request<{ success: boolean }>(`/devices/${id}/claim`, {
      method: 'POST',
      body: JSON.stringify(body),
    });

  reassignDevice = (
    id: string,
    body: { customer_id: string; location_id?: string; device_group_ids?: string[] },
  ) =>
    this.request<{ success: boolean }>(`/devices/${id}/reassign`, {
      method: 'POST',
      body: JSON.stringify(body),
    });

  connectDevice = (id: string) =>
    this.request<{ uri: string; rustdesk_id: string }>(`/devices/${id}/connect`, { method: 'POST' });

  deleteDevice = (id: string) =>
    this.request<{ success: boolean }>(`/devices/${id}`, { method: 'DELETE' });

  /**
   * Reading this is audited server-side, so it is deliberately not part of the
   * device detail query: the password is fetched when somebody asks for it, not
   * every time the page loads, or the audit trail would record a reveal for
   * every navigation.
   */
  getDevicePassword = (id: string) => this.request<DevicePassword>(`/devices/${id}/password`);

  rotateDevicePassword = (id: string) =>
    this.request<DevicePassword>(`/devices/${id}/password/rotate`, { method: 'POST' });

  // Customers -------------------------------------------------------------
  getCustomers = (current = 1, pageSize = 20) =>
    this.request<Paginated<Customer>>(`/customers${query({ current, pageSize })}`);

  getCustomer = (id: string) => this.request<Customer>(`/customers/${id}`);

  createCustomer = (body: { code: string; name: string; external_id?: string; description?: string }) =>
    this.request<Customer>('/customers', { method: 'POST', body: JSON.stringify(body) });

  updateCustomer = (id: string, body: Partial<{ code: string; name: string; description: string }>) =>
    this.request<Customer>(`/customers/${id}`, { method: 'PATCH', body: JSON.stringify(body) });

  deleteCustomer = (id: string) =>
    this.request<{ success: boolean }>(`/customers/${id}`, { method: 'DELETE' });

  getLocations = (customerId: string, current = 1, pageSize = 50) =>
    this.request<Paginated<Location>>(
      `/customers/${customerId}/locations${query({ current, pageSize })}`,
    );

  createLocation = (customerId: string, body: { name: string; address?: string }) =>
    this.request<Location>(`/customers/${customerId}/locations`, {
      method: 'POST',
      body: JSON.stringify(body),
    });

  deleteLocation = (customerId: string, locationId: string) =>
    this.request<{ success: boolean }>(`/customers/${customerId}/locations/${locationId}`, {
      method: 'DELETE',
    });

  // Device groups ---------------------------------------------------------
  getDeviceGroups = (current = 1, pageSize = 50) =>
    this.request<Paginated<DeviceGroup>>(`/device-groups${query({ current, pageSize })}`);

  createDeviceGroup = (body: { name: string; parent_id?: string }) =>
    this.request<DeviceGroup>('/device-groups', { method: 'POST', body: JSON.stringify(body) });

  updateDeviceGroup = (id: string, body: { name?: string; parent_id?: string }) =>
    this.request<DeviceGroup>(`/device-groups/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    });

  deleteDeviceGroup = (id: string) =>
    this.request<{ success: boolean }>(`/device-groups/${id}`, { method: 'DELETE' });

  getDeviceGroupMembers = (id: string, current = 1, pageSize = 50) =>
    this.request<Paginated<Device>>(`/device-groups/${id}/members${query({ current, pageSize })}`);

  addDeviceGroupMember = (id: string, deviceId: string) =>
    this.request<{ success: boolean }>(`/device-groups/${id}/members`, {
      method: 'POST',
      body: JSON.stringify({ device_id: deviceId }),
    });

  removeDeviceGroupMember = (id: string, deviceId: string) =>
    this.request<{ success: boolean }>(`/device-groups/${id}/members/${deviceId}`, {
      method: 'DELETE',
    });

  // Support groups --------------------------------------------------------
  getSupportGroups = (current = 1, pageSize = 50) =>
    this.request<Paginated<SupportGroup>>(`/support-groups${query({ current, pageSize })}`);

  getSupportGroup = (id: string) => this.request<SupportGroup>(`/support-groups/${id}`);

  createSupportGroup = (body: { name: string; description?: string }) =>
    this.request<SupportGroup>('/support-groups', { method: 'POST', body: JSON.stringify(body) });

  deleteSupportGroup = (id: string) =>
    this.request<{ success: boolean }>(`/support-groups/${id}`, { method: 'DELETE' });

  addTechnician = (id: string, userId: number) =>
    this.request<{ success: boolean }>(`/support-groups/${id}/technicians`, {
      method: 'POST',
      body: JSON.stringify({ user_id: userId }),
    });

  removeTechnician = (id: string, userId: number) =>
    this.request<{ success: boolean }>(`/support-groups/${id}/technicians/${userId}`, {
      method: 'DELETE',
    });

  grantDeviceGroup = (id: string, deviceGroupId: string) =>
    this.request<{ success: boolean }>(`/support-groups/${id}/device-groups`, {
      method: 'POST',
      body: JSON.stringify({ device_group_id: deviceGroupId }),
    });

  revokeDeviceGroup = (id: string, deviceGroupId: string) =>
    this.request<{ success: boolean }>(`/support-groups/${id}/device-groups/${deviceGroupId}`, {
      method: 'DELETE',
    });

  // Users -----------------------------------------------------------------
  getUsers = (params: { q?: string; active?: string; current?: number; pageSize?: number } = {}) =>
    this.request<Paginated<User>>(`/users${query({ ...params })}`);

  getUser = (id: number) => this.request<User>(`/users/${id}`);

  // The temporary password comes back exactly once, in this response. It is
  // stored nowhere and cannot be read again: the account carries Keycloak's
  // UPDATE_PASSWORD required action, so it is spent on first sign-in.
  createUser = (body: { email: string; display_name?: string; role?: string }) =>
    this.request<User & { temporary_password: string }>('/users', {
      method: 'POST',
      body: JSON.stringify(body),
    });

  deleteUser = (id: number) =>
    this.request<{
      success: boolean;
      identity_account_removed: boolean;
      passwords_rotated: number;
      rotation_in_force: string;
    }>(`/users/${id}`, { method: 'DELETE' });

  setUserActive = (id: number, active: boolean) =>
    this.request<User>(`/users/${id}`, { method: 'PATCH', body: JSON.stringify({ active }) });

  grantRole = (id: number, role: string) =>
    this.request<{ success: boolean }>(`/users/${id}/roles`, {
      method: 'POST',
      body: JSON.stringify({ role }),
    });

  revokeRole = (id: number, role: string) =>
    this.request<{ success: boolean }>(`/users/${id}/roles/${role}`, { method: 'DELETE' });

  // Enrollment tokens -----------------------------------------------------
  getEnrollmentTokens = (customerId?: string) =>
    this.request<Paginated<EnrollmentToken>>(`/enrollment-tokens${query({ customer: customerId })}`);

  createEnrollmentToken = (body: {
    customer_id: string;
    location_id?: string;
    device_group_id?: string;
    max_uses: number;
    expires_at: string;
  }) => this.request<EnrollmentToken>('/enrollment-tokens', {
    method: 'POST',
    body: JSON.stringify(body),
  });

  revokeEnrollmentToken = (id: string) =>
    this.request<{ success: boolean }>(`/enrollment-tokens/${id}`, { method: 'DELETE' });

  // Audit -----------------------------------------------------------------
  getAuditEvents = (
    params: {
      actor?: string;
      resource?: string;
      type?: string;
      device?: string;
      from?: string;
      to?: string;
      current?: number;
      pageSize?: number;
    } = {},
  ) => this.request<Paginated<AuditEvent>>(`/audit/events${query({ ...params })}`);

  getAuditSessions = (
    params: {
      device?: string;
      user?: string;
      from?: string;
      to?: string;
      current?: number;
      pageSize?: number;
    } = {},
  ) => this.request<Paginated<Session>>(`/audit/sessions${query({ ...params })}`);

  // Settings --------------------------------------------------------------
  getSettings = () => this.request<Settings>('/settings');
}

export const apiClient = new ApiClient(API_BASE_URL);
