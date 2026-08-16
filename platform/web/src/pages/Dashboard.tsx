import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { apiClient } from '../lib/api';
import {
  Badge,
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Spinner,
  StatTile,
} from '../components/ui';
import { formatDate } from '../lib/format';

export default function Dashboard() {
  const { data, isLoading, error } = useQuery({
    queryKey: ['dashboard'],
    queryFn: () => apiClient.getDashboard(),
  });

  if (isLoading) return <Spinner label="Loading the dashboard" />;
  if (error) return <ErrorNotice error={error} />;
  if (!data) return <EmptyState message="No dashboard data." />;

  const { stats, recent_connections: recent } = data;

  return (
    <>
      <PageHeader title="Dashboard" description="Fleet at a glance." />

      <dl className="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile label="Total devices" value={stats.total_devices} />
        <StatTile label="Online now" value={stats.online_now} />
        <StatTile label="Awaiting claim" value={stats.discovered_devices} />
        <StatTile label="Connections (24h)" value={stats.connections_24h} />
        <StatTile label="Active" value={stats.active_devices} />
        <StatTile label="Stale" value={stats.stale_devices} />
        <StatTile label="Customers" value={stats.total_customers} />
        <StatTile label="Technicians" value={stats.total_technicians} />
      </dl>

      <Card className="mt-8">
        <div className="border-b border-gray-200 px-4 py-4">
          <h2 className="text-base font-medium text-gray-900">Recent connections</h2>
        </div>
        {recent.length === 0 ? (
          <EmptyState message="No connections recorded yet." />
        ) : (
          <table className="min-w-full divide-y divide-gray-200 text-sm">
            <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500">
              <tr>
                <th className="px-4 py-2 font-medium">Device</th>
                <th className="px-4 py-2 font-medium">Technician</th>
                <th className="px-4 py-2 font-medium">Started</th>
                <th className="px-4 py-2 font-medium">Status</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {recent.map((session) => (
                <tr key={session.id}>
                  <td className="px-4 py-2">
                    {session.device_id ? (
                      <Link
                        to={`/devices/${session.device_id}`}
                        className="text-indigo-600 hover:underline"
                      >
                        {session.device_name ?? session.device_id}
                      </Link>
                    ) : (
                      '—'
                    )}
                  </td>
                  <td className="px-4 py-2 text-gray-700">{session.user_email ?? '—'}</td>
                  <td className="px-4 py-2 text-gray-500">{formatDate(session.start_time)}</td>
                  <td className="px-4 py-2">
                    <Badge value={session.status} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </>
  );
}
