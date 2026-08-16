import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient, type DeviceGroup } from '../lib/api';
import {
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Pagination,
  Spinner,
} from '../components/ui';

export default function DeviceGroups() {
  const [current, setCurrent] = useState(1);
  const [expanded, setExpanded] = useState<string | null>(null);
  const queryClient = useQueryClient();

  const { data, isLoading, error } = useQuery({
    queryKey: ['device-groups', current],
    queryFn: () => apiClient.getDeviceGroups(current, 20),
  });

  const create = useMutation({
    mutationFn: (name: string) => apiClient.createDeviceGroup({ name }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['device-groups'] }),
  });

  const remove = useMutation({
    mutationFn: (id: string) => apiClient.deleteDeviceGroup(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['device-groups'] }),
  });

  return (
    <>
      <PageHeader
        title="Device groups"
        description="Collections of devices that support groups are granted access to."
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
            <span className="mb-1 block text-gray-600">New group name</span>
            <input
              name="name"
              required
              className="w-64 rounded-md border border-gray-300 px-3 py-2 text-sm"
            />
          </label>
          <button
            type="submit"
            disabled={create.isPending}
            className="rounded-md bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-500 disabled:opacity-50"
          >
            Create group
          </button>
        </form>
        {create.error && (
          <div className="mt-3">
            <ErrorNotice error={create.error} />
          </div>
        )}
      </Card>

      {isLoading && <Spinner label="Loading device groups" />}
      {error && <ErrorNotice error={error} />}

      {data && (
        <Card>
          {data.data.length === 0 ? (
            <EmptyState message="No device groups yet." />
          ) : (
            <table className="min-w-full divide-y divide-gray-200 text-sm">
              <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500">
                <tr>
                  <th className="px-4 py-2 font-medium">Name</th>
                  <th className="px-4 py-2 font-medium">Members</th>
                  <th className="px-4 py-2 font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {data.data.map((group) => (
                  <GroupRows
                    key={group.id}
                    group={group}
                    expanded={expanded === group.id}
                    onToggle={() => setExpanded(expanded === group.id ? null : group.id)}
                    onDelete={() => remove.mutate(group.id)}
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

function GroupRows({
  group,
  expanded,
  onToggle,
  onDelete,
}: {
  group: DeviceGroup;
  expanded: boolean;
  onToggle: () => void;
  onDelete: () => void;
}) {
  const queryClient = useQueryClient();

  const members = useQuery({
    queryKey: ['device-group-members', group.id],
    queryFn: () => apiClient.getDeviceGroupMembers(group.id),
    enabled: expanded,
  });

  const removeMember = useMutation({
    mutationFn: (deviceId: string) => apiClient.removeDeviceGroupMember(group.id, deviceId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['device-group-members', group.id] });
      void queryClient.invalidateQueries({ queryKey: ['device-groups'] });
    },
  });

  return (
    <>
      <tr>
        <td className="px-4 py-2 font-medium text-gray-900">{group.name}</td>
        <td className="px-4 py-2 text-gray-700">{group.member_count}</td>
        <td className="px-4 py-2">
          <div className="flex gap-2">
            <button
              type="button"
              onClick={onToggle}
              className="rounded-md border border-gray-300 px-2.5 py-1 text-xs font-medium text-gray-700 hover:bg-gray-50"
            >
              {expanded ? 'Hide members' : 'Members'}
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
          <td colSpan={3} className="bg-gray-50 px-4 py-3">
            {members.isLoading && <Spinner label="Loading members" />}
            {members.error && <ErrorNotice error={members.error} />}
            {members.data &&
              (members.data.data.length === 0 ? (
                <p className="text-sm text-gray-500">No devices in this group.</p>
              ) : (
                <ul className="space-y-1 text-sm">
                  {members.data.data.map((device) => (
                    <li key={device.id} className="flex items-center gap-3">
                      <span className="text-gray-700">{device.name}</span>
                      <button
                        type="button"
                        onClick={() => removeMember.mutate(device.id)}
                        className="text-xs text-red-600 hover:underline"
                      >
                        Remove
                      </button>
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
