-- Remove the API-client credential store, which nothing ever read.
--
-- api_clients and api_tokens were created by the initial schema and no Go code
-- has ever referenced either. They were the intended answer to "how does an
-- external system authenticate", and the answer this deployment took instead is
-- a Keycloak service account: a client-credentials token, validated by the same
-- JWT middleware as every other caller, resolved to an ordinary users row by
-- identity.provisionUser. That keeps access control, the audit actor and the
-- IDOR sweep working on one kind of caller rather than two.
--
-- Dropped rather than left in place because an unused credential table is the
-- exact shape of defect the earlier audit kept finding: unreferenced code
-- accumulates faults silently, and a half-built second authentication path is a
-- worse one to leave lying around than most.

DROP INDEX IF EXISTS idx_api_tokens_expires_at;
DROP INDEX IF EXISTS idx_api_tokens_user_id;
DROP INDEX IF EXISTS idx_api_tokens_client_id;
DROP TABLE IF EXISTS api_tokens;

DROP INDEX IF EXISTS idx_api_clients_client_id;
DROP TABLE IF EXISTS api_clients;
