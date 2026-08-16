import type { ReactNode } from 'react';
import { ApiError } from '../lib/api';

export function PageHeader({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="mb-6 flex items-start justify-between gap-4">
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">{title}</h1>
        {description && <p className="mt-1 text-sm text-gray-500">{description}</p>}
      </div>
      {action}
    </div>
  );
}

export function Card({ children, className = '' }: { children: ReactNode; className?: string }) {
  return (
    <div className={`rounded-lg border border-gray-200 bg-white shadow-sm ${className}`}>
      {children}
    </div>
  );
}

export function StatTile({ label, value }: { label: string; value: number | string }) {
  return (
    <Card className="p-5">
      <dt className="truncate text-sm font-medium text-gray-500">{label}</dt>
      <dd className="mt-1 text-3xl font-semibold text-gray-900">{value}</dd>
    </Card>
  );
}

export function Spinner({ label = 'Loading' }: { label?: string }) {
  return (
    <div role="status" aria-live="polite" className="flex items-center gap-3 py-10 text-gray-500">
      <span className="h-5 w-5 animate-spin rounded-full border-2 border-gray-300 border-t-indigo-600" />
      <span className="text-sm">{label}</span>
    </div>
  );
}

/**
 * ErrorNotice renders a failed fetch. A 403 is a different problem from an
 * outage, so it says so rather than showing the same "something went wrong".
 */
export function ErrorNotice({ error }: { error: unknown }) {
  const forbidden = error instanceof ApiError && error.status === 403;
  const message =
    error instanceof Error ? error.message : 'The request failed for an unknown reason.';

  return (
    <div role="alert" className="rounded-md border border-red-200 bg-red-50 p-4">
      <h2 className="text-sm font-medium text-red-800">
        {forbidden ? 'You do not have access to this' : 'Could not load this page'}
      </h2>
      <p className="mt-1 text-sm text-red-700">{message}</p>
    </div>
  );
}

export function EmptyState({ message }: { message: string }) {
  return <p className="py-10 text-center text-sm text-gray-500">{message}</p>;
}

export function Pagination({
  current,
  totalPages,
  total,
  onChange,
}: {
  current: number;
  totalPages: number;
  total: number;
  onChange: (page: number) => void;
}) {
  if (total === 0) return null;

  return (
    <div className="flex items-center justify-between border-t border-gray-200 px-4 py-3">
      <p className="text-sm text-gray-500">
        Page {current} of {Math.max(totalPages, 1)} &middot; {total} total
      </p>
      <div className="flex gap-2">
        <button
          type="button"
          disabled={current <= 1}
          onClick={() => onChange(current - 1)}
          className="rounded-md border border-gray-300 px-3 py-1.5 text-sm font-medium text-gray-700 disabled:opacity-40"
        >
          Previous
        </button>
        <button
          type="button"
          disabled={current >= totalPages}
          onClick={() => onChange(current + 1)}
          className="rounded-md border border-gray-300 px-3 py-1.5 text-sm font-medium text-gray-700 disabled:opacity-40"
        >
          Next
        </button>
      </div>
    </div>
  );
}

const stateStyles: Record<string, string> = {
  ONLINE: 'bg-green-100 text-green-800',
  ACTIVE: 'bg-green-100 text-green-800',
  DISCOVERED: 'bg-amber-100 text-amber-800',
  STALE: 'bg-amber-100 text-amber-800',
  OFFLINE: 'bg-gray-100 text-gray-700',
  UNKNOWN: 'bg-gray-100 text-gray-700',
  DISABLED: 'bg-red-100 text-red-800',
};

export function Badge({ value }: { value: string }) {
  return (
    <span
      className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${
        stateStyles[value] ?? 'bg-gray-100 text-gray-700'
      }`}
    >
      {value}
    </span>
  );
}
