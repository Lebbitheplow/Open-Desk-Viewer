import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { apiClient } from '../lib/api';
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

type Tab = 'sessions' | 'events';

export default function AuditLog() {
  const [tab, setTab] = useState<Tab>('sessions');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [current, setCurrent] = useState(1);

  function changeTab(next: Tab) {
    setTab(next);
    setCurrent(1);
  }

  return (
    <>
      <PageHeader
        title="Audit log"
        description="Connections made to devices, and administrative changes to the fleet."
      />

      <div className="mb-4 flex flex-wrap items-end gap-3">
        <div className="flex rounded-md border border-gray-300">
          <button
            type="button"
            onClick={() => changeTab('sessions')}
            className={`px-3 py-2 text-sm font-medium ${
              tab === 'sessions' ? 'bg-indigo-600 text-white' : 'text-gray-700'
            }`}
          >
            Connections
          </button>
          <button
            type="button"
            onClick={() => changeTab('events')}
            className={`px-3 py-2 text-sm font-medium ${
              tab === 'events' ? 'bg-indigo-600 text-white' : 'text-gray-700'
            }`}
          >
            Changes
          </button>
        </div>
        <label className="text-sm">
          <span className="mb-1 block text-gray-600">From</span>
          <input
            type="date"
            aria-label="From date"
            value={from}
            onChange={(event) => {
              setFrom(event.target.value);
              setCurrent(1);
            }}
            className="rounded-md border border-gray-300 px-3 py-2 text-sm"
          />
        </label>
        <label className="text-sm">
          <span className="mb-1 block text-gray-600">To</span>
          <input
            type="date"
            aria-label="To date"
            value={to}
            onChange={(event) => {
              setTo(event.target.value);
              setCurrent(1);
            }}
            className="rounded-md border border-gray-300 px-3 py-2 text-sm"
          />
        </label>
      </div>

      {tab === 'sessions' ? (
        <SessionsTable from={from} to={to} current={current} onPage={setCurrent} />
      ) : (
        <EventsTable from={from} to={to} current={current} onPage={setCurrent} />
      )}
    </>
  );
}

function SessionsTable({
  from,
  to,
  current,
  onPage,
}: {
  from: string;
  to: string;
  current: number;
  onPage: (page: number) => void;
}) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['audit-sessions', from, to, current],
    queryFn: () =>
      apiClient.getAuditSessions({
        from: from || undefined,
        to: to || undefined,
        current,
        pageSize: 25,
      }),
  });

  if (isLoading) return <Spinner label="Loading connections" />;
  if (error) return <ErrorNotice error={error} />;
  if (!data) return null;

  return (
    <Card>
      {data.data.length === 0 ? (
        <EmptyState message="No connections in this range." />
      ) : (
        <table className="min-w-full divide-y divide-gray-200 text-sm">
          <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500">
            <tr>
              <th className="px-4 py-2 font-medium">Device</th>
              <th className="px-4 py-2 font-medium">Technician</th>
              <th className="px-4 py-2 font-medium">Started</th>
              <th className="px-4 py-2 font-medium">Ended</th>
              <th className="px-4 py-2 font-medium">Status</th>
              <th className="px-4 py-2 font-medium">Note</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100">
            {data.data.map((session) => (
              <tr key={session.id}>
                <td className="px-4 py-2 text-gray-900">{session.device_name ?? '—'}</td>
                <td className="px-4 py-2 text-gray-700">{session.user_email ?? '—'}</td>
                <td className="px-4 py-2 text-gray-500">{formatDate(session.start_time)}</td>
                <td className="px-4 py-2 text-gray-500">{formatDate(session.end_time)}</td>
                <td className="px-4 py-2">
                  <Badge value={session.status} />
                </td>
                <td className="px-4 py-2 text-gray-700">{session.note || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <Pagination
        current={data.current}
        totalPages={data.totalPages}
        total={data.total}
        onChange={onPage}
      />
    </Card>
  );
}

function EventsTable({
  from,
  to,
  current,
  onPage,
}: {
  from: string;
  to: string;
  current: number;
  onPage: (page: number) => void;
}) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['audit-events', from, to, current],
    queryFn: () =>
      apiClient.getAuditEvents({
        from: from || undefined,
        to: to || undefined,
        current,
        pageSize: 25,
      }),
  });

  if (isLoading) return <Spinner label="Loading changes" />;
  if (error) return <ErrorNotice error={error} />;
  if (!data) return null;

  return (
    <Card>
      {data.data.length === 0 ? (
        <EmptyState message="No recorded changes in this range." />
      ) : (
        <table className="min-w-full divide-y divide-gray-200 text-sm">
          <thead className="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500">
            <tr>
              <th className="px-4 py-2 font-medium">When</th>
              <th className="px-4 py-2 font-medium">Event</th>
              <th className="px-4 py-2 font-medium">Actor</th>
              <th className="px-4 py-2 font-medium">Subject</th>
              <th className="px-4 py-2 font-medium">Description</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100">
            {data.data.map((event) => (
              <tr key={event.id}>
                <td className="px-4 py-2 text-gray-500">{formatDate(event.created_at)}</td>
                <td className="px-4 py-2 font-mono text-xs text-gray-700">{event.event_type}</td>
                <td className="px-4 py-2 text-gray-700">{event.user_email ?? 'system'}</td>
                <td className="px-4 py-2 text-gray-700">
                  {event.device_name ?? event.customer_name ?? '—'}
                </td>
                <td className="px-4 py-2 text-gray-700">{event.description ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <Pagination
        current={data.current}
        totalPages={data.totalPages}
        total={data.total}
        onChange={onPage}
      />
    </Card>
  );
}
