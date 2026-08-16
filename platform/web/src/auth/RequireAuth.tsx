import { useEffect, type ReactNode } from 'react';
import { useAuth } from 'react-oidc-context';
import { setTokenSource } from '../lib/api';

/**
 * RequireAuth gates every page behind a Keycloak session and feeds the access
 * token to the API client.
 *
 * An unauthenticated visit redirects to Keycloak rather than rendering an empty
 * shell: a portal that renders its chrome and then fails every fetch looks
 * broken rather than logged out.
 */
export function RequireAuth({ children }: { children: ReactNode }) {
  const auth = useAuth();

  // The API client asks for a token per request, so it always sees the current
  // one rather than whatever was current when the app mounted.
  useEffect(() => {
    setTokenSource(() => auth.user?.access_token);
  }, [auth.user]);

  useEffect(() => {
    if (!auth.isLoading && !auth.isAuthenticated && !auth.activeNavigator && !auth.error) {
      void auth.signinRedirect();
    }
  }, [auth]);

  if (auth.isLoading || auth.activeNavigator) {
    return <FullPageMessage title="Signing in" detail="Talking to the identity provider." />;
  }

  if (auth.error) {
    return (
      <FullPageMessage
        title="Sign-in failed"
        detail={auth.error.message}
        action={
          <button
            type="button"
            onClick={() => void auth.signinRedirect()}
            className="mt-4 rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-500"
          >
            Try again
          </button>
        }
      />
    );
  }

  if (!auth.isAuthenticated) {
    return <FullPageMessage title="Redirecting to sign in" detail="" />;
  }

  return <>{children}</>;
}

function FullPageMessage({
  title,
  detail,
  action,
}: {
  title: string;
  detail: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50">
      <div className="text-center">
        <h1 className="text-xl font-semibold text-gray-900">{title}</h1>
        {detail && <p className="mt-2 text-sm text-gray-500">{detail}</p>}
        {action}
      </div>
    </div>
  );
}
