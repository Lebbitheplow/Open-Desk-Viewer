import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import type { AuthContextProps } from 'react-oidc-context';

import { RequireAuth } from './RequireAuth';

const signinRedirect = vi.fn().mockResolvedValue(undefined);
let authState: Partial<AuthContextProps>;

vi.mock('react-oidc-context', () => ({
  useAuth: () => authState,
}));

function auth(overrides: Partial<AuthContextProps>): Partial<AuthContextProps> {
  return {
    isLoading: false,
    isAuthenticated: false,
    activeNavigator: undefined,
    error: undefined,
    user: undefined,
    signinRedirect,
    ...overrides,
  };
}

beforeEach(() => {
  signinRedirect.mockClear();
});

describe('RequireAuth', () => {
  it('redirects an unauthenticated visit to Keycloak instead of rendering the page', async () => {
    authState = auth({});

    render(
      <RequireAuth>
        <p>fleet console</p>
      </RequireAuth>,
    );

    await waitFor(() => expect(signinRedirect).toHaveBeenCalled());
    expect(screen.queryByText('fleet console')).not.toBeInTheDocument();
  });

  it('renders the page once authenticated', () => {
    authState = auth({
      isAuthenticated: true,
      user: { access_token: 'token' } as AuthContextProps['user'],
    });

    render(
      <RequireAuth>
        <p>fleet console</p>
      </RequireAuth>,
    );

    expect(screen.getByText('fleet console')).toBeInTheDocument();
    expect(signinRedirect).not.toHaveBeenCalled();
  });

  it('does not loop on redirect while the provider is still loading', () => {
    authState = auth({ isLoading: true });

    render(
      <RequireAuth>
        <p>fleet console</p>
      </RequireAuth>,
    );

    expect(signinRedirect).not.toHaveBeenCalled();
    expect(screen.getByText('Signing in')).toBeInTheDocument();
  });

  it('surfaces a sign-in failure rather than retrying forever', () => {
    authState = auth({
      error: Object.assign(new Error('invalid client'), { source: 'unknown' as const }),
    });

    render(
      <RequireAuth>
        <p>fleet console</p>
      </RequireAuth>,
    );

    expect(screen.getByText('Sign-in failed')).toBeInTheDocument();
    expect(screen.getByText('invalid client')).toBeInTheDocument();
    expect(signinRedirect).not.toHaveBeenCalled();
  });
});
