import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { useAuth } from 'react-oidc-context';

import { ErrorBoundary } from './ErrorBoundary';

const navigation = [
  { to: '/', label: 'Dashboard', end: true },
  { to: '/devices', label: 'Devices' },
  { to: '/customers', label: 'Customers' },
  { to: '/device-groups', label: 'Device groups' },
  { to: '/support-groups', label: 'Support groups' },
  { to: '/users', label: 'Users' },
  { to: '/enrollment-tokens', label: 'Enrollment' },
  { to: '/audit', label: 'Audit log' },
  { to: '/settings', label: 'Settings' },
];

export function Layout() {
  const auth = useAuth();
  const location = useLocation();
  const profile = auth.user?.profile;

  return (
    <div className="min-h-screen bg-gray-50">
      <header className="border-b border-gray-200 bg-white">
        <div className="mx-auto flex max-w-7xl items-center justify-between px-4 py-3 sm:px-6 lg:px-8">
          <span className="text-lg font-semibold text-indigo-600">OpenDeskViewer</span>
          <div className="flex items-center gap-4">
            <span className="text-sm text-gray-600">{profile?.email ?? profile?.name}</span>
            <button
              type="button"
              onClick={() => void auth.signoutRedirect()}
              className="rounded-md border border-gray-300 px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50"
            >
              Sign out
            </button>
          </div>
        </div>
        <nav className="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
          <ul className="flex gap-1 overflow-x-auto">
            {navigation.map((item) => (
              <li key={item.to}>
                <NavLink
                  to={item.to}
                  end={item.end}
                  className={({ isActive }) =>
                    `inline-block whitespace-nowrap border-b-2 px-3 py-2 text-sm font-medium ${
                      isActive
                        ? 'border-indigo-600 text-indigo-600'
                        : 'border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700'
                    }`
                  }
                >
                  {item.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>
      </header>

      {/*
        The boundary is inside the layout, so a page that throws leaves the
        header and the navigation standing and the operator can go somewhere
        else. A boundary only at the root would replace the whole application
        with an error panel and strand them there.

        Keyed on the path so it resets on navigation. Without the key it would
        stay in its error state after the user navigated away, since React
        remounts nothing and the boundary has no reason to clear itself.
      */}
      <main className="mx-auto max-w-7xl px-4 py-8 sm:px-6 lg:px-8">
        <ErrorBoundary key={location.pathname} where={location.pathname}>
          <Outlet />
        </ErrorBoundary>
      </main>
    </div>
  );
}
