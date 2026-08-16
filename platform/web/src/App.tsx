import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AuthProvider } from 'react-oidc-context';

import { oidcConfig } from './auth/config';
import { RequireAuth } from './auth/RequireAuth';
import { ErrorBoundary } from './components/ErrorBoundary';
import { Layout } from './components/Layout';
import { ApiError } from './lib/api';

import Dashboard from './pages/Dashboard';
import Devices from './pages/Devices';
import DeviceDetail from './pages/DeviceDetail';
import Customers from './pages/Customers';
import DeviceGroups from './pages/DeviceGroups';
import SupportGroups from './pages/SupportGroups';
import Users from './pages/Users';
import EnrollmentTokens from './pages/EnrollmentTokens';
import AuditLog from './pages/AuditLog';
import Settings from './pages/Settings';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      // Retrying a 401 or a 403 cannot succeed and only delays the error the
      // user needs to see.
      retry: (failureCount, error) => {
        if (error instanceof ApiError && error.status >= 400 && error.status < 500) return false;
        return failureCount < 2;
      },
    },
  },
});

/** AppRoutes is exported separately so tests can mount it without OIDC. */
export function AppRoutes() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route path="/" element={<Dashboard />} />
        <Route path="/devices" element={<Devices />} />
        <Route path="/devices/:id" element={<DeviceDetail />} />
        <Route path="/customers" element={<Customers />} />
        <Route path="/device-groups" element={<DeviceGroups />} />
        <Route path="/support-groups" element={<SupportGroups />} />
        <Route path="/users" element={<Users />} />
        <Route path="/enrollment-tokens" element={<EnrollmentTokens />} />
        <Route path="/audit" element={<AuditLog />} />
        <Route path="/settings" element={<Settings />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}

export default function App() {
  return (
    // The outer boundary is the backstop for everything above the layout: a
    // failure in the OIDC provider, in RequireAuth, or in the layout's own
    // chrome. Layout has its own boundary around the routed page, which is
    // where an error is far more likely and where keeping the navigation
    // usable actually matters.
    <ErrorBoundary where="application root">
      <AuthProvider {...oidcConfig}>
        <QueryClientProvider client={queryClient}>
          <BrowserRouter>
            <RequireAuth>
              <AppRoutes />
            </RequireAuth>
          </BrowserRouter>
        </QueryClientProvider>
      </AuthProvider>
    </ErrorBoundary>
  );
}
