-- Remove the password sign-in credential store, which nothing could write to.
--
-- identity.SetPassword had no HTTP caller and never had one, so the only way a
-- row could appear in user_credentials was by hand. /api/login's password
-- branch therefore refused every caller it was offered to, which is a door that
-- cannot open: the client's sign-in dialog showed a username and a password
-- field that could not authenticate anybody.
--
-- The fix could have been to build the missing change-password surface. It is
-- not, because identity in this deployment lives in Keycloak, which owns
-- password policy, lockout, MFA and disablement. A second credential store here
-- would have been governed by none of those, and would have gone on working
-- after an account was disabled in the identity provider. The client signs in
-- through /api/oidc/auth instead, which is implemented and which
-- /api/login-options now actually advertises -- it used to return "common", a
-- value the client discards, so no provider button was rendered at all.
--
-- Same call as 000011 made about api_clients, for the same reason: a half-built
-- second authentication path is a worse thing to leave lying around than most.
--
-- client_sessions is untouched. It holds the session tokens /api/login issues
-- after an OIDC sign-in, which is a live path.

DROP TABLE IF EXISTS user_credentials;

DROP INDEX IF EXISTS idx_login_attempts_time;
DROP INDEX IF EXISTS idx_login_attempts_ip_time;
DROP TABLE IF EXISTS login_attempts;
