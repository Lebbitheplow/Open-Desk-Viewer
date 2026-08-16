import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient, type User } from '../lib/api';
import {
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Pagination,
  Spinner,
} from '../components/ui';
import { formatDate } from '../lib/format';

export default function Users() {
  const [search, setSearch] = useState('');
  const [current, setCurrent] = useState(1);
  const [creating, setCreating] = useState(false);
  // The temporary password is returned once and is readable nowhere else, so
  // it stays on screen until the administrator dismisses it.
  const [handover, setHandover] = useState<{ email: string; password: string } | null>(null);
  const queryClient = useQueryClient();

  // The roles come from the API. Hardcoding them here is what produced a
  // "Manager" button that always failed with "unknown role": the seeded role is
  // "Support Manager", and nothing compared the two lists.
  const settings = useQuery({
    queryKey: ['settings'],
    queryFn: () => apiClient.getSettings(),
    staleTime: 5 * 60 * 1000,
  });
  const roles = settings.data?.roles ?? [];

  const { data, isLoading, error } = useQuery({
    queryKey: ['users', search, current],
    queryFn: () => apiClient.getUsers({ q: search || undefined, current, pageSize: 20 }),
  });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['users'] });

  const setActive = useMutation({
    mutationFn: ({ id, active }: { id: number; active: boolean }) =>
      apiClient.setUserActive(id, active),
    onSuccess: invalidate,
  });

  const grant = useMutation({
    mutationFn: ({ id, role }: { id: number; role: string }) => apiClient.grantRole(id, role),
    onSuccess: invalidate,
  });

  const revoke = useMutation({
    mutationFn: ({ id, role }: { id: number; role: string }) => apiClient.revokeRole(id, role),
    onSuccess: invalidate,
  });

  const create = useMutation({
    mutationFn: (body: { email: string; display_name?: string; role?: string }) =>
      apiClient.createUser(body),
    onSuccess: (user) => {
      setCreating(false);
      setHandover({ email: user.email, password: user.temporary_password });
      void invalidate();
    },
  });

  const remove = useMutation({
    mutationFn: (id: number) => apiClient.deleteUser(id),
    onSuccess: invalidate,
  });

  return (
    <>
      <PageHeader
        title="Users"
        description="Deactivating a user takes effect on their next request, not at their next sign-in. Deactivating or removing one also rotates the connection password of every device they could reach, in force at each device's next heartbeat."
        action={
          <button
            type="button"
            onClick={() => setCreating((v) => !v)}
            className="rounded-md bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-500"
          >
            {creating ? 'Cancel' : 'New user'}
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
                email: String(form.get('email') ?? ''),
                display_name: String(form.get('display_name') ?? '') || undefined,
                role: String(form.get('role') ?? '') || undefined,
              });
            }}
            className="flex flex-wrap items-end gap-3"
          >
            <label className="text-sm">
              <span className="mb-1 block text-gray-600">Email</span>
              <input
                name="email"
                type="email"
                required
                className="rounded-md border border-gray-300 px-3 py-2 text-sm"
              />
            </label>
            <label className="text-sm">
              <span className="mb-1 block text-gray-600">Display name</span>
              <input
                name="display_name"
                className="rounded-md border border-gray-300 px-3 py-2 text-sm"
              />
            </label>
            <label className="text-sm">
              <span className="mb-1 block text-gray-600">Role</span>
              <select name="role" className="rounded-md border border-gray-300 px-3 py-2 text-sm">
                <option value="">No role yet</option>
                {roles.map((role) => (
                  <option key={role} value={role}>
                    {role}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="submit"
              disabled={create.isPending}
              className="rounded-md bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-500 disabled:opacity-50"
            >
              Create
            </button>
            <p className="w-full text-xs text-gray-500">
              Creates the sign-in account as well as the portal user. You will be shown a temporary
              password once, to hand over yourself; the person must change it when they first sign
              in.
            </p>
          </form>
          {create.error && (
            <div className="mt-3">
              <ErrorNotice error={create.error} />
            </div>
          )}
        </Card>
      )}

      {handover && (
        <Card className="mb-6 border-l-4 border-amber-400 p-5">
          <p className="text-sm text-gray-800">
            Temporary password for <span className="font-medium">{handover.email}</span>. This is
            shown once and cannot be retrieved again.
          </p>
          <code className="mt-2 block select-all rounded bg-gray-100 px-3 py-2 font-mono text-sm">
            {handover.password}
          </code>
          <button
            type="button"
            onClick={() => setHandover(null)}
            className="mt-3 rounded-md border border-gray-300 px-2.5 py-1 text-xs font-medium text-gray-700 hover:bg-gray-50"
          >
            I have handed it over
          </button>
        </Card>
      )}

      <input
        type="search"
        aria-label="Search users"
        placeholder="Search by name or email"
        value={search}
        onChange={(event) => {
          setSearch(event.target.value);
          setCurrent(1);
        }}
        className="mb-4 w-72 rounded-md border border-gray-300 px-3 py-2 text-sm"
      />

      {isLoading && <Spinner label="Loading users" />}
      {error && <ErrorNotice error={error} />}

      {data && (
        <Card>
          {data.data.length === 0 ? (
            <EmptyState message="No users match this search." />
          ) : (
            <table className="min-w-full divide-y divide-gray-200 text-sm">
              <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500">
                <tr>
                  <th className="px-4 py-2 font-medium">Name</th>
                  <th className="px-4 py-2 font-medium">Email</th>
                  <th className="px-4 py-2 font-medium">Roles</th>
                  <th className="px-4 py-2 font-medium">Last login</th>
                  <th className="px-4 py-2 font-medium">Status</th>
                  <th className="px-4 py-2 font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {data.data.map((user: User) => (
                  <tr key={user.id}>
                    <td className="px-4 py-2 font-medium text-gray-900">{user.display_name}</td>
                    <td className="px-4 py-2 text-gray-700">{user.email}</td>
                    <td className="px-4 py-2">
                      <div className="flex flex-wrap items-center gap-1">
                        {user.roles.map((role) => (
                          <button
                            key={role}
                            type="button"
                            title={`Revoke ${role}`}
                            onClick={() => revoke.mutate({ id: user.id, role })}
                            className="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-700 hover:bg-red-100 hover:text-red-700"
                          >
                            {role} ×
                          </button>
                        ))}
                        <select
                          aria-label={`Grant a role to ${user.display_name}`}
                          value=""
                          onChange={(event) => {
                            if (event.target.value) {
                              grant.mutate({ id: user.id, role: event.target.value });
                            }
                          }}
                          className="rounded-md border border-gray-300 px-1 py-0.5 text-xs"
                        >
                          <option value="">+</option>
                          {roles.filter((role) => !user.roles.includes(role)).map((role) => (
                            <option key={role} value={role}>
                              {role}
                            </option>
                          ))}
                        </select>
                      </div>
                    </td>
                    <td className="px-4 py-2 text-gray-500">{formatDate(user.last_login)}</td>
                    <td className="px-4 py-2">
                      <span
                        className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                          user.active ? 'bg-green-100 text-green-800' : 'bg-red-100 text-red-800'
                        }`}
                      >
                        {user.active ? 'Active' : 'Disabled'}
                      </span>
                    </td>
                    <td className="px-4 py-2">
                      <div className="flex items-center gap-2">
                        <button
                          type="button"
                          onClick={() => setActive.mutate({ id: user.id, active: !user.active })}
                          className="rounded-md border border-gray-300 px-2.5 py-1 text-xs font-medium text-gray-700 hover:bg-gray-50"
                        >
                          {user.active ? 'Deactivate' : 'Activate'}
                        </button>
                        <button
                          type="button"
                          onClick={() => {
                            if (
                              window.confirm(
                                `Remove ${user.email}? This deletes their sign-in account as well as their portal user, and rotates the connection password of every device they could reach. The record of what they did is kept.`,
                              )
                            ) {
                              remove.mutate(user.id);
                            }
                          }}
                          className="rounded-md border border-gray-300 px-2.5 py-1 text-xs font-medium text-red-700 hover:bg-red-50"
                        >
                          Remove
                        </button>
                      </div>
                    </td>
                  </tr>
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

      {(setActive.error || remove.error) && (
        <div className="mt-4">
          <ErrorNotice error={setActive.error ?? remove.error} />
        </div>
      )}
    </>
  );
}
