-- Dropping this cancels any sign-in in flight. Nobody loses a session: a
-- completed sign-in has already become a client_sessions row, and an incomplete
-- one is retried by pressing the button again.
DROP TABLE IF EXISTS oidc_auth_requests;
