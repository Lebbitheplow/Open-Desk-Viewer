import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams, Link } from 'react-router-dom';
import { apiClient, type DevicePassword } from '../lib/api';
import {
  Badge,
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Spinner,
} from '../components/ui';
import { formatDate } from '../lib/format';

/**
 * The device's platform-managed connection password.
 *
 * Two decisions worth stating, because both are visible in the markup:
 *
 * - Nothing is fetched until somebody asks. Reading the password writes an
 *   audit event naming the reader, so loading it with the page would record a
 *   reveal every time anyone opened the device.
 * - The pending banner is not decoration. A rotation changes the platform's
 *   copy at once and reaches the machine on its next heartbeat, so between the
 *   two the device is still accepting the old password. Showing the new one
 *   without saying that would be the screen lying about what the fleet is doing.
 */
function ConnectionPassword({ deviceId }: { deviceId: string }) {
  const queryClient = useQueryClient();

  const reveal = useMutation({
    mutationFn: () => apiClient.getDevicePassword(deviceId),
    onSuccess: (data) => queryClient.setQueryData(['device-password', deviceId], data),
  });

  const rotate = useMutation({
    mutationFn: () => apiClient.rotateDevicePassword(deviceId),
    onSuccess: (data) => queryClient.setQueryData(['device-password', deviceId], data),
  });

  const password = queryClient.getQueryData<DevicePassword>(['device-password', deviceId]);
  const busy = reveal.isPending || rotate.isPending;
  const error = rotate.error ?? reveal.error;

  return (
    <Card className="mt-6 p-5">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h2 className="text-base font-medium text-gray-900">Connection password</h2>
          <p className="mt-1 text-sm text-gray-500">
            Set by the platform and delivered to the device over its heartbeat. Showing it is
            recorded in the audit log against your account.
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          <button
            type="button"
            disabled={busy}
            onClick={() => reveal.mutate()}
            className="rounded-md border border-gray-300 px-3 py-1.5 text-sm font-medium text-gray-700 disabled:opacity-40"
          >
            {password ? 'Refresh' : 'Show password'}
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => rotate.mutate()}
            className="rounded-md border border-gray-300 px-3 py-1.5 text-sm font-medium text-gray-700 disabled:opacity-40"
          >
            Rotate
          </button>
        </div>
      </div>

      {busy && <Spinner label="Talking to the device password store" />}
      {error && !busy && <div className="mt-4"><ErrorNotice error={error} /></div>}

      {password && !busy && (
        <div className="mt-4">
          <code className="inline-block rounded bg-gray-100 px-3 py-2 font-mono text-lg tracking-wider text-gray-900">
            {password.password}
          </code>
          <dl className="mt-3 grid grid-cols-2 gap-y-1 text-sm sm:max-w-md">
            <dt className="text-gray-500">Version</dt>
            <dd className="text-gray-900">{password.version}</dd>
            <dt className="text-gray-500">Last rotated</dt>
            <dd className="text-gray-900">{formatDate(password.rotated_at)}</dd>
            <dt className="text-gray-500">Confirmed by the device</dt>
            <dd className="text-gray-900">
              {password.applied ? formatDate(password.applied_at ?? undefined) : 'Not yet'}
            </dd>
          </dl>

          {!password.applied && (
            <p
              role="status"
              className="mt-3 rounded-md border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800"
            >
              This password is waiting for the device to check in. Until it does, the device is
              still accepting the previous one
              {password.applied_version !== null ? ` (version ${password.applied_version})` : ''}.
              Devices check in about every 15 seconds while they are online.
            </p>
          )}
        </div>
      )}
    </Card>
  );
}

export default function DeviceDetail() {
  const { id = '' } = useParams();

  const device = useQuery({
    queryKey: ['device', id],
    queryFn: () => apiClient.getDevice(id),
    enabled: Boolean(id),
  });

  const sessions = useQuery({
    queryKey: ['device-sessions', id],
    queryFn: () => apiClient.getDeviceSessions(id),
    enabled: Boolean(id),
  });

  if (device.isLoading) return <Spinner label="Loading the device" />;
  if (device.error) return <ErrorNotice error={device.error} />;
  if (!device.data) return <EmptyState message="Device not found." />;

  const d = device.data;

  return (
    <>
      <PageHeader
        title={d.name}
        description={`RustDesk ID ${d.rustdesk_id}`}
        action={
          <Link to="/devices" className="text-sm text-indigo-600 hover:underline">
            Back to devices
          </Link>
        }
      />

      <div className="grid gap-6 lg:grid-cols-2">
        <Card className="p-5">
          <h2 className="mb-3 text-base font-medium text-gray-900">Overview</h2>
          <dl className="grid grid-cols-2 gap-y-2 text-sm">
            <dt className="text-gray-500">State</dt>
            <dd>
              <Badge value={d.state} />
            </dd>
            <dt className="text-gray-500">Connectivity</dt>
            <dd>
              <Badge value={d.connectivity} />
            </dd>
            <dt className="text-gray-500">Customer</dt>
            <dd className="text-gray-900">{d.customer_name ?? '—'}</dd>
            <dt className="text-gray-500">Location</dt>
            <dd className="text-gray-900">{d.location_name ?? '—'}</dd>
            <dt className="text-gray-500">Hostname</dt>
            <dd className="text-gray-900">{d.hostname ?? '—'}</dd>
            <dt className="text-gray-500">Operating system</dt>
            <dd className="text-gray-900">{d.os ?? '—'}</dd>
            <dt className="text-gray-500">Client version</dt>
            <dd className="text-gray-900">{d.client_version ?? '—'}</dd>
            <dt className="text-gray-500">Serial number</dt>
            <dd className="text-gray-900">{d.serial_number ?? '—'}</dd>
            <dt className="text-gray-500">Last seen</dt>
            <dd className="text-gray-900">{formatDate(d.last_seen_at)}</dd>
          </dl>
        </Card>

        <Card className="p-5">
          <h2 className="mb-3 text-base font-medium text-gray-900">Hardware</h2>
          {d.sysinfo ? (
            <dl className="grid grid-cols-2 gap-y-2 text-sm">
              <dt className="text-gray-500">CPU</dt>
              <dd className="text-gray-900">{d.sysinfo.cpu ?? '—'}</dd>
              <dt className="text-gray-500">Memory</dt>
              <dd className="text-gray-900">{d.sysinfo.memory ?? '—'}</dd>
              <dt className="text-gray-500">Disk</dt>
              <dd className="text-gray-900">{d.sysinfo.disk ?? '—'}</dd>
              <dt className="text-gray-500">Collected</dt>
              <dd className="text-gray-900">{formatDate(d.sysinfo.collected_at)}</dd>
            </dl>
          ) : (
            <p className="text-sm text-gray-500">
              This device has not reported its hardware yet.
            </p>
          )}

          <h2 className="mb-2 mt-6 text-base font-medium text-gray-900">Groups</h2>
          {d.groups.length === 0 ? (
            <p className="text-sm text-gray-500">Not a member of any device group.</p>
          ) : (
            <ul className="flex flex-wrap gap-2">
              {d.groups.map((group) => (
                <li
                  key={group.id}
                  className="rounded-full bg-gray-100 px-2.5 py-0.5 text-xs text-gray-700"
                >
                  {group.name}
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      <ConnectionPassword deviceId={id} />

      <Card className="mt-6">
        <div className="border-b border-gray-200 px-4 py-4">
          <h2 className="text-base font-medium text-gray-900">Connections</h2>
        </div>
        {sessions.isLoading && <Spinner label="Loading connections" />}
        {sessions.error && <ErrorNotice error={sessions.error} />}
        {sessions.data &&
          (sessions.data.data.length === 0 ? (
            <EmptyState message="No connections to this device yet." />
          ) : (
            <table className="min-w-full divide-y divide-gray-200 text-sm">
              <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500">
                <tr>
                  <th className="px-4 py-2 font-medium">Technician</th>
                  <th className="px-4 py-2 font-medium">Started</th>
                  <th className="px-4 py-2 font-medium">Duration</th>
                  <th className="px-4 py-2 font-medium">Status</th>
                  <th className="px-4 py-2 font-medium">Note</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {sessions.data.data.map((session) => (
                  <tr key={session.id}>
                    <td className="px-4 py-2 text-gray-700">{session.user_email ?? '—'}</td>
                    <td className="px-4 py-2 text-gray-500">{formatDate(session.start_time)}</td>
                    <td className="px-4 py-2 text-gray-500">
                      {session.duration_seconds ? `${session.duration_seconds}s` : '—'}
                    </td>
                    <td className="px-4 py-2">
                      <Badge value={session.status} />
                    </td>
                    <td className="px-4 py-2 text-gray-700">{session.note || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ))}
      </Card>
    </>
  );
}
