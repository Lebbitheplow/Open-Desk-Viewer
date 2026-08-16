/**
 * Value formatters shared by the pages.
 *
 * These live outside components/ui.tsx on purpose. Vite's fast refresh only
 * preserves component state for a module that exports components alone, so a
 * single non-component export in the shared UI module costs a full reload of
 * every page that imports from it.
 */

/** formatDate renders an API timestamp, or an em rule for absent and unparseable values. */
export function formatDate(value?: string): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleString();
}
