import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient } from '../lib/api';
import {
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Spinner,
} from '../components/ui';

/**
 * Support groups are where the authorisation model is actually administered:
 * a technician reaches a device because a support group they belong to has been
 * granted a device group the device is in. Both halves of that are edited here.
 */
export default function SupportGroups() {
  const [selected, setSelected] = useState<string | null>(null);
  const queryClient = useQueryClient();

  const groups = useQuery({
    queryKey: ['support-groups'],
    queryFn: () => apiClient.getSupportGroups(),
  });

  const create = useMutation({
    mutationFn: (name: string) => apiClient.createSupportGroup({ name }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['support-groups'] }),
  });

  return (
    <>
      <PageHeader
        title="Support groups"
        description="Technician teams, and the device groups each team can reach."
      />

      <Card className="mb-6 p-5">
        <form
          onSubmit={(event) => {
            event.preventDefault();
            const form = new FormData(event.currentTarget);
            const name = String(form.get('name') ?? '').trim();
            if (name) {
              create.mutate(name);
              event.currentTarget.reset();
            }
          }}
          className="flex flex-wrap items-end gap-3"
        >
          <label className="text-sm">
            <span className="mb-1 block text-gray-600">New support group</span>
            <input
              name="name"
              required
              className="w-64 rounded-md border border-gray-300 px-3 py-2 text-sm"
            />
          </label>
          <button
            type="submit"
            className="rounded-md bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-500"
          >
            Create
          </button>
        </form>
      </Card>

      {groups.isLoading && <Spinner label="Loading support groups" />}
      {groups.error && <ErrorNotice error={groups.error} />}

      {groups.data && (
        <div className="grid gap-6 lg:grid-cols-[20rem_1fr]">
          <Card>
            {groups.data.data.length === 0 ? (
              <EmptyState message="No support groups yet." />
            ) : (
              <ul className="divide-y divide-gray-100">
                {groups.data.data.map((group) => (
                  <li key={group.id}>
                    <button
                      type="button"
                      onClick={() => setSelected(group.id)}
                      className={`flex w-full items-center justify-between px-4 py-3 text-left text-sm hover:bg-gray-50 ${
                        selected === group.id ? 'bg-indigo-50 font-medium text-indigo-700' : ''
                      }`}
                    >
                      <span>{group.name}</span>
                      <span className="text-xs text-gray-500">{group.member_count}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          {selected ? (
            <SupportGroupDetail id={selected} />
          ) : (
            <Card className="p-5">
              <p className="text-sm text-gray-500">
                Choose a support group to edit its technicians and grants.
              </p>
            </Card>
          )}
        </div>
      )}
    </>
  );
}

function SupportGroupDetail({ id }: { id: string }) {
  const queryClient = useQueryClient();

  const group = useQuery({
    queryKey: ['support-group', id],
    queryFn: () => apiClient.getSupportGroup(id),
  });

  const users = useQuery({
    queryKey: ['users', 'all'],
    queryFn: () => apiClient.getUsers({ pageSize: 200 }),
  });

  const deviceGroups = useQuery({
    queryKey: ['device-groups', 'all'],
    queryFn: () => apiClient.getDeviceGroups(1, 200),
  });

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['support-group', id] });
    void queryClient.invalidateQueries({ queryKey: ['support-groups'] });
  };

  const addTechnician = useMutation({
    mutationFn: (userId: number) => apiClient.addTechnician(id, userId),
    onSuccess: invalidate,
  });
  const removeTechnician = useMutation({
    mutationFn: (userId: number) => apiClient.removeTechnician(id, userId),
    onSuccess: invalidate,
  });
  const grant = useMutation({
    mutationFn: (deviceGroupId: string) => apiClient.grantDeviceGroup(id, deviceGroupId),
    onSuccess: invalidate,
  });
  const revoke = useMutation({
    mutationFn: (deviceGroupId: string) => apiClient.revokeDeviceGroup(id, deviceGroupId),
    onSuccess: invalidate,
  });

  if (group.isLoading) return <Spinner label="Loading the support group" />;
  if (group.error) return <ErrorNotice error={group.error} />;
  if (!group.data) return <EmptyState message="Support group not found." />;

  const g = group.data;
  const memberIds = new Set((g.technicians ?? []).map((t) => t.id));
  const grantedIds = new Set((g.device_groups ?? []).map((d) => d.id));

  return (
    <Card className="p-5">
      <h2 className="text-lg font-medium text-gray-900">{g.name}</h2>
      {g.description && <p className="mt-1 text-sm text-gray-500">{g.description}</p>}

      <section className="mt-6">
        <h3 className="mb-2 text-sm font-medium text-gray-900">Technicians</h3>
        {(g.technicians ?? []).length === 0 ? (
          <p className="text-sm text-gray-500">No technicians in this group.</p>
        ) : (
          <ul className="mb-3 space-y-1 text-sm">
            {g.technicians?.map((tech) => (
              <li key={tech.id} className="flex items-center gap-3">
                <span className="text-gray-700">{tech.display_name}</span>
                <span className="text-xs text-gray-400">{tech.email}</span>
                <button
                  type="button"
                  onClick={() => removeTechnician.mutate(tech.id)}
                  className="text-xs text-red-600 hover:underline"
                >
                  Remove
                </button>
              </li>
            ))}
          </ul>
        )}
        <select
          aria-label="Add a technician"
          value=""
          onChange={(event) => {
            if (event.target.value) addTechnician.mutate(Number(event.target.value));
          }}
          className="rounded-md border border-gray-300 px-3 py-2 text-sm"
        >
          <option value="">Add a technician…</option>
          {users.data?.data
            .filter((u) => !memberIds.has(u.id))
            .map((u) => (
              <option key={u.id} value={u.id}>
                {u.display_name} ({u.email})
              </option>
            ))}
        </select>
      </section>

      <section className="mt-8">
        <h3 className="mb-2 text-sm font-medium text-gray-900">Device groups this team reaches</h3>
        {(g.device_groups ?? []).length === 0 ? (
          <p className="text-sm text-gray-500">
            No grants. Technicians in this group see no devices.
          </p>
        ) : (
          <ul className="mb-3 space-y-1 text-sm">
            {g.device_groups?.map((dg) => (
              <li key={dg.id} className="flex items-center gap-3">
                <span className="text-gray-700">{dg.name}</span>
                <button
                  type="button"
                  onClick={() => revoke.mutate(dg.id)}
                  className="text-xs text-red-600 hover:underline"
                >
                  Revoke
                </button>
              </li>
            ))}
          </ul>
        )}
        <select
          aria-label="Grant a device group"
          value=""
          onChange={(event) => {
            if (event.target.value) grant.mutate(event.target.value);
          }}
          className="rounded-md border border-gray-300 px-3 py-2 text-sm"
        >
          <option value="">Grant a device group…</option>
          {deviceGroups.data?.data
            .filter((dg) => !grantedIds.has(dg.id))
            .map((dg) => (
              <option key={dg.id} value={dg.id}>
                {dg.name}
              </option>
            ))}
        </select>
      </section>
    </Card>
  );
}
