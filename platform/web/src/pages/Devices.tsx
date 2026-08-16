import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { apiClient, type Device } from '../lib/api';
import {
  Badge,
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Pagination,
  Spinner,
} from '../components/ui';
import { formatDate } from '../lib/format';

const PAGE_SIZE = 20;

export default function Devices() {
  const [search, setSearch] = useState('');
  const [state, setState] = useState('');
  const [customer, setCustomer] = useState('');
  const [current, setCurrent] = useState(1);

  const queryClient = useQueryClient();

  const { data, isLoading, error } = useQuery({
    queryKey: ['devices', search, state, customer, current],
    queryFn: () =>
      apiClient.getDevices({
        q: search || undefined,
        state: state || undefined,
        customer: customer || undefined,
        current,
        pageSize: PAGE_SIZE,
      }),
  });

  const customers = useQuery({
    queryKey: ['customers', 'all'],
    queryFn: () => apiClient.getCustomers(1, 200),
    // A technician cannot list customers; the filter simply does not appear.
    retry: false,
  });

  const claim = useMutation({
    mutationFn: ({ id, customerId }: { id: string; customerId: string }) =>
      apiClient.claimDevice(id, { customer_id: customerId }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['devices'] }),
  });

  const connect = useMutation({
    mutationFn: (id: string) => apiClient.connectDevice(id),
    onSuccess: (result) => {
      window.location.href = result.uri;
    },
  });

  function applyFilter(setter: (value: string) => void, value: string) {
    setter(value);
    setCurrent(1);
  }

  return (
    <>
      <PageHeader title="Devices" description="Every device this deployment manages." />

      <div className="mb-4 flex flex-wrap gap-3">
        <input
          type="search"
          aria-label="Search devices"
          placeholder="Search by name, hostname or serial"
          value={search}
          onChange={(event) => applyFilter(setSearch, event.target.value)}
          className="w-64 rounded-md border border-gray-300 px-3 py-2 text-sm"
        />
        <select
          aria-label="Filter by state"
          value={state}
          onChange={(event) => applyFilter(setState, event.target.value)}
          className="rounded-md border border-gray-300 px-3 py-2 text-sm"
        >
          <option value="">Any state</option>
          <option value="DISCOVERED">Discovered</option>
          <option value="ACTIVE">Active</option>
          <option value="DISABLED">Disabled</option>
          <option value="DECOMMISSIONED">Decommissioned</option>
        </select>
        {customers.data && (
          <select
            aria-label="Filter by customer"
            value={customer}
            onChange={(event) => applyFilter(setCustomer, event.target.value)}
            className="rounded-md border border-gray-300 px-3 py-2 text-sm"
          >
            <option value="">Any customer</option>
            {customers.data.data.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        )}
      </div>

      {isLoading && <Spinner label="Loading devices" />}
      {error && <ErrorNotice error={error} />}

      {data && (
        <Card>
          {data.data.length === 0 ? (
            <EmptyState message="No devices match these filters." />
          ) : (
            <table className="min-w-full divide-y divide-gray-200 text-sm">
              <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500">
                <tr>
                  <th className="px-4 py-2 font-medium">Name</th>
                  <th className="px-4 py-2 font-medium">Serial</th>
                  <th className="px-4 py-2 font-medium">Customer</th>
                  <th className="px-4 py-2 font-medium">State</th>
                  <th className="px-4 py-2 font-medium">Connectivity</th>
                  <th className="px-4 py-2 font-medium">Last seen</th>
                  <th className="px-4 py-2 font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {data.data.map((device) => (
                  <DeviceRow
                    key={device.id}
                    device={device}
                    canClaim={Boolean(customers.data)}
                    onClaim={(customerId) => claim.mutate({ id: device.id, customerId })}
                    onConnect={() => connect.mutate(device.id)}
                  />
                ))}
              </tbody>
            </table>
          )}
          <Pagination
            current={data.current}
            totalPages={data.totalPages}
            total={data.total}
            onChange={setCurrent}
          />
        </Card>
      )}
    </>
  );
}

function DeviceRow({
  device,
  canClaim,
  onClaim,
  onConnect,
}: {
  device: Device;
  canClaim: boolean;
  onClaim: (customerId: string) => void;
  onConnect: () => void;
}) {
  const [claiming, setClaiming] = useState(false);
  const customers = useQuery({
    queryKey: ['customers', 'all'],
    queryFn: () => apiClient.getCustomers(1, 200),
    enabled: claiming,
    retry: false,
  });

  return (
    <tr>
      <td className="px-4 py-2">
        <Link to={`/devices/${device.id}`} className="font-medium text-indigo-600 hover:underline">
          {device.name}
        </Link>
        <div className="text-xs text-gray-500">{device.hostname ?? device.rustdesk_id}</div>
      </td>
      <td className="px-4 py-2 font-mono text-xs text-gray-700">
        {device.serial_number ?? '—'}
      </td>
      <td className="px-4 py-2 text-gray-700">{device.customer_name ?? '—'}</td>
      <td className="px-4 py-2">
        <Badge value={device.state} />
      </td>
      <td className="px-4 py-2">
        <Badge value={device.connectivity} />
      </td>
      <td className="px-4 py-2 text-gray-500">{formatDate(device.last_seen_at)}</td>
      <td className="px-4 py-2">
        <div className="flex items-center gap-2">
          {device.state === 'DISCOVERED' && canClaim && !claiming && (
            <button
              type="button"
              onClick={() => setClaiming(true)}
              className="rounded-md bg-indigo-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-indigo-500"
            >
              Claim
            </button>
          )}
          {claiming && (
            <select
              aria-label={`Claim ${device.name} for customer`}
              defaultValue=""
              onChange={(event) => {
                if (event.target.value) {
                  onClaim(event.target.value);
                  setClaiming(false);
                }
              }}
              className="rounded-md border border-gray-300 px-2 py-1 text-xs"
            >
              <option value="">Choose customer…</option>
              {customers.data?.data.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          )}
          {device.state === 'ACTIVE' && (
            <button
              type="button"
              onClick={onConnect}
              className="rounded-md border border-gray-300 px-2.5 py-1 text-xs font-medium text-gray-700 hover:bg-gray-50"
            >
              Connect
            </button>
          )}
        </div>
      </td>
    </tr>
  );
}
