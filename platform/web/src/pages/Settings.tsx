import { useQuery } from '@tanstack/react-query';
import { apiClient } from '../lib/api';
import { Card, ErrorNotice, PageHeader, Spinner } from '../components/ui';

export default function Settings() {
  const { data, isLoading, error } = useQuery({
    queryKey: ['settings'],
    queryFn: () => apiClient.getSettings(),
  });

  if (isLoading) return <Spinner label="Loading settings" />;
  if (error) return <ErrorNotice error={error} />;
  if (!data) return null;

  const rows = [
    {
      label: 'Address book peer cap',
      value: data.address_book_max_peers === 0 ? 'Unlimited' : data.address_book_max_peers,
      env: 'AB_MAX_PEER_ONE_AB',
    },
    {
      label: 'Audit retention',
      value: `${data.audit_retention_days} days`,
      env: 'AUDIT_RETENTION_DAYS',
    },
    {
      label: 'Device marked stale after',
      value: `${data.device_stale_after_seconds} seconds`,
      env: 'DEVICE_STALE_AFTER_SECONDS',
    },
    {
      label: 'Device marked offline after',
      value: `${data.device_offline_after_seconds} seconds`,
      env: 'DEVICE_OFFLINE_AFTER_SECONDS',
    },
  ];

  return (
    <>
      <PageHeader
        title="Settings"
        description="These come from the deployment's environment. Change them in .env and restart the API."
      />

      <Card>
        <table className="min-w-full divide-y divide-gray-200 text-sm">
          <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500">
            <tr>
              <th className="px-4 py-2 font-medium">Setting</th>
              <th className="px-4 py-2 font-medium">Value</th>
              <th className="px-4 py-2 font-medium">Environment variable</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100">
            {rows.map((row) => (
              <tr key={row.env}>
                <td className="px-4 py-2 text-gray-900">{row.label}</td>
                <td className="px-4 py-2 text-gray-700">{row.value}</td>
                <td className="px-4 py-2 font-mono text-xs text-gray-500">{row.env}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>

      {!data.editable && (
        <p className="mt-4 text-sm text-gray-500">
          Settings are read-only in this release. Making them editable needs a configuration table
          and a reload path, which is deliberately out of scope for 1.0.
        </p>
      )}
    </>
  );
}
