import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient, type Customer } from '../lib/api';
import {
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Pagination,
  Spinner,
} from '../components/ui';
import { formatDate } from '../lib/format';

export default function Customers() {
  const [current, setCurrent] = useState(1);
  const [creating, setCreating] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);

  const queryClient = useQueryClient();

  const { data, isLoading, error } = useQuery({
    queryKey: ['customers', current],
    queryFn: () => apiClient.getCustomers(current, 20),
  });

  const create = useMutation({
    mutationFn: (body: { code: string; name: string }) => apiClient.createCustomer(body),
    onSuccess: () => {
      setCreating(false);
      void queryClient.invalidateQueries({ queryKey: ['customers'] });
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => apiClient.deleteCustomer(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['customers'] }),
  });

  return (
    <>
      <PageHeader
        title="Customers"
        description="Organisations whose devices this deployment manages."
        action={
          <button
            type="button"
            onClick={() => setCreating((v) => !v)}
            className="rounded-md bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-500"
          >
            {creating ? 'Cancel' : 'New customer'}
          </button>
        }
      />

      {creating && (
        <Card className="mb-6 p-5">
          <form
            onSubmit={(event) => {
              event.preventDefault();
              const form = new FormData(event.currentTarget);
              create.mutate({
                code: String(form.get('code') ?? ''),
                name: String(form.get('name') ?? ''),
              });
            }}
            className="flex flex-wrap items-end gap-3"
          >
            <label className="text-sm">
              <span className="mb-1 block text-gray-600">Code</span>
              <input
                name="code"
                required
                className="rounded-md border border-gray-300 px-3 py-2 text-sm"
              />
            </label>
            <label className="text-sm">
              <span className="mb-1 block text-gray-600">Name</span>
              <input
                name="name"
                required
                className="rounded-md border border-gray-300 px-3 py-2 text-sm"
              />
            </label>
            <button
              type="submit"
              disabled={create.isPending}
              className="rounded-md bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-500 disabled:opacity-50"
            >
              Create
            </button>
          </form>
          {create.error && (
            <div className="mt-3">
              <ErrorNotice error={create.error} />
            </div>
          )}
        </Card>
      )}

      {isLoading && <Spinner label="Loading customers" />}
      {error && <ErrorNotice error={error} />}

      {data && (
        <Card>
          {data.data.length === 0 ? (
            <EmptyState message="No customers yet." />
          ) : (
            <table className="min-w-full divide-y divide-gray-200 text-sm">
              <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500">
                <tr>
                  <th className="px-4 py-2 font-medium">Code</th>
                  <th className="px-4 py-2 font-medium">Name</th>
                  <th className="px-4 py-2 font-medium">Devices</th>
                  <th className="px-4 py-2 font-medium">Created</th>
                  <th className="px-4 py-2 font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {data.data.map((customer) => (
                  <CustomerRows
                    key={customer.id}
                    customer={customer}
                    expanded={expanded === customer.id}
                    onToggle={() => setExpanded(expanded === customer.id ? null : customer.id)}
                    onDelete={() => remove.mutate(customer.id)}
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

function CustomerRows({
  customer,
  expanded,
  onToggle,
  onDelete,
}: {
  customer: Customer;
  expanded: boolean;
  onToggle: () => void;
  onDelete: () => void;
}) {
  const locations = useQuery({
    queryKey: ['locations', customer.id],
    queryFn: () => apiClient.getLocations(customer.id),
    enabled: expanded,
  });

  return (
    <>
      <tr>
        <td className="px-4 py-2 font-mono text-xs text-gray-700">{customer.code}</td>
        <td className="px-4 py-2 font-medium text-gray-900">{customer.name}</td>
        <td className="px-4 py-2 text-gray-700">{customer.device_count}</td>
        <td className="px-4 py-2 text-gray-500">{formatDate(customer.created_at)}</td>
        <td className="px-4 py-2">
          <div className="flex gap-2">
            <button
              type="button"
              onClick={onToggle}
              className="rounded-md border border-gray-300 px-2.5 py-1 text-xs font-medium text-gray-700 hover:bg-gray-50"
            >
              {expanded ? 'Hide locations' : 'Locations'}
            </button>
            <button
              type="button"
              onClick={onDelete}
              className="rounded-md border border-red-300 px-2.5 py-1 text-xs font-medium text-red-700 hover:bg-red-50"
            >
              Delete
            </button>
          </div>
        </td>
      </tr>
      {expanded && (
        <tr>
          <td colSpan={5} className="bg-gray-50 px-4 py-3">
            {locations.isLoading && <Spinner label="Loading locations" />}
            {locations.error && <ErrorNotice error={locations.error} />}
            {locations.data &&
              (locations.data.data.length === 0 ? (
                <p className="text-sm text-gray-500">No locations for this customer.</p>
              ) : (
                <ul className="space-y-1 text-sm">
                  {locations.data.data.map((location) => (
                    <li key={location.id} className="text-gray-700">
                      <span className="font-medium">{location.name}</span>
                      {location.address && (
                        <span className="text-gray-500"> — {location.address}</span>
                      )}
                    </li>
                  ))}
                </ul>
              ))}
          </td>
        </tr>
      )}
    </>
  );
}
