import type { AuthProviderProps } from 'react-oidc-context';
import { WebStorageStateStore } from 'oidc-client-ts';

/**
 * Keycloak owns authentication; the API only validates tokens. This is the
 * authorisation code flow with PKCE and no client secret, because the secret
 * would be readable by anyone with the browser's developer tools.
 */
export const oidcConfig: AuthProviderProps = {
  authority: import.meta.env.VITE_OIDC_ISSUER ?? '',
  client_id: import.meta.env.VITE_OIDC_CLIENT_ID ?? 'odv-portal',
  redirect_uri: `${window.location.origin}/callback`,
  post_logout_redirect_uri: window.location.origin,
  response_type: 'code',
  scope: 'openid profile email',

  // Keep the session in sessionStorage rather than localStorage: closing the
  // tab ends the session, which is the safer default for a fleet console on a
  // shared workstation.
  userStore: new WebStorageStateStore({ store: window.sessionStorage }),

  automaticSilentRenew: true,

  // Strip the code and state from the URL after the callback so a copied link
  // does not carry a single-use authorisation code.
  onSigninCallback: () => {
    window.history.replaceState({}, document.title, window.location.pathname);
  },
};

/** isConfigured reports whether an issuer was supplied at build time. */
export const isConfigured = Boolean(import.meta.env.VITE_OIDC_ISSUER);
