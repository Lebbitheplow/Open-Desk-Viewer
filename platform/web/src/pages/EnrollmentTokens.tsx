import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient, type EnrollmentToken } from '../lib/api';
import {
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Spinner,
} from '../components/ui';
import { formatDate } from '../lib/format';

export default function EnrollmentTokens() {
  const [issued, setIssued] = useState<EnrollmentToken | null>(null);
  const queryClient = useQueryClient();

  const customers = useQuery({
    queryKey: ['customers', 'all'],
    queryFn: () => apiClient.getCustomers(1, 200),
  });

  const [customerId, setCustomerId] = useState('');

  const tokens = useQuery({
    queryKey: ['enrollment-tokens', customerId],
    queryFn: () => apiClient.getEnrollmentTokens(customerId || undefined),
    enabled: Boolean(customerId),
  });

  const create = useMutation({
    mutationFn: (body: { customer_id: string; max_uses: number; expires_at: string }) =>
      apiClient.createEnrollmentToken(body),
    onSuccess: (token) => {
      setIssued(token);
      void queryClient.invalidateQueries({ queryKey: ['enrollment-tokens'] });
    },
  });

  const revoke = useMutation({
    mutationFn: (id: string) => apiClient.revokeEnrollmentToken(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['enrollment-tokens'] }),
  });

  return (
    <>
      <PageHeader
        title="Enrollment tokens"
        description="A token lets a new device register itself against a customer without being claimed by hand."
      />

      {issued && (
        <Card className="mb-6 border-amber-300 bg-amber-50 p-5">
          <h2 className="text-sm font-medium text-amber-900">
            Copy this token now. It is not shown again.
          </h2>
          <code className="mt-2 block break-all rounded bg-white p-3 font-mono text-sm text-gray-900">
            {issued.token}
          </code>
          <button
            type="button"
            onClick={() => setIssued(null)}
            className="mt-3 text-sm text-amber-800 hover:underline"
          >
            I have copied it
          </button>
        </Card>
      )}

      <Card className="mb-6 p-5">
        <form
          onSubmit={(event) => {
            event.preventDefault();
            const form = new FormData(event.currentTarget);
            const customer = String(form.get('customer_id') ?? '');
            if (!customer) return;
            const days = Number(form.get('days') ?? 30);
            create.mutate({
              customer_id: customer,
              max_uses: Number(form.get('max_uses') ?? 1),
              expires_at: new Date(Date.now() + days * 86_400_000).toISOString(),
            });
          }}
          className="flex flex-wrap items-end gap-3"
        >
          <label className="text-sm">
            <span className="mb-1 block text-gray-600">Customer</span>
            <select
              name="customer_id"
              required
              value={customerId}
              onChange={(event) => setCustomerId(event.target.value)}
              className="w-56 rounded-md border border-gray-300 px-3 py-2 text-sm"
            >
              <option value="">Choose…</option>
              {customers.data?.data.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </label>
          <label className="text-sm">
            <span className="mb-1 block text-gray-600">Maximum uses</span>
            <input
              name="max_uses"
              type="number"
              min={1}
              defaultValue={1}
              className="w-32 rounded-md border border-gray-300 px-3 py-2 text-sm"
            />
          </label>
          <label className="text-sm">
            <span className="mb-1 block text-gray-600">Expires in (days)</span>
            <input
              name="days"
              type="number"
              min={1}
              defaultValue={30}
              className="w-32 rounded-md border border-gray-300 px-3 py-2 text-sm"
            />
          </label>
          <button
            type="submit"
            disabled={create.isPending}
            className="rounded-md bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-500 disabled:opacity-50"
          >
            Issue token
          </button>
        </form>
        {create.error && (
          <div className="mt-3">
            <ErrorNotice error={create.error} />
          </div>
        )}
      </Card>

      {!customerId && (
        <EmptyState message="Choose a customer to list its enrollment tokens." />
      )}
      {customerId && tokens.isLoading && <Spinner label="Loading tokens" />}
      {tokens.error && <ErrorNotice error={tokens.error} />}

      {customerId && tokens.data && (
        <Card>
          {tokens.data.data.length === 0 ? (
            <EmptyState message="No tokens issued for this customer." />
          ) : (
            <table className="min-w-full divide-y divide-gray-200 text-sm">
              <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500">
                <tr>
                  <th className="px-4 py-2 font-medium">Prefix</th>
                  <th className="px-4 py-2 font-medium">Uses</th>
                  <th className="px-4 py-2 font-medium">Expires</th>
                  <th className="px-4 py-2 font-medium">Status</th>
                  <th className="px-4 py-2 font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {tokens.data.data.map((token) => {
                  const revoked = Boolean(token.revoked_at);
                  return (
                    <tr key={token.id}>
                      <td className="px-4 py-2 font-mono text-xs text-gray-700">
                        {token.prefix}…
                      </td>
                      <td className="px-4 py-2 text-gray-700">
                        {token.uses} of {token.max_uses}
                      </td>
                      <td className="px-4 py-2 text-gray-500">{formatDate(token.expires_at)}</td>
                      <td className="px-4 py-2">
                        <span
                          className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                            revoked ? 'bg-red-100 text-red-800' : 'bg-green-100 text-green-800'
                          }`}
                        >
                          {revoked ? 'Revoked' : 'Valid'}
                        </span>
                      </td>
                      <td className="px-4 py-2">
                        {!revoked && (
                          <button
                            type="button"
                            onClick={() => revoke.mutate(token.id)}
                            className="rounded-md border border-red-300 px-2.5 py-1 text-xs font-medium text-red-700 hover:bg-red-50"
                          >
                            Revoke
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </Card>
      )}
    </>
  );
}
