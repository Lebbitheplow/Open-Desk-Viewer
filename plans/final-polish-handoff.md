# OpenDeskViewer — final polish and verification handoff

**Audience:** a fresh session with no prior context. Everything needed to execute is below.
**Repo:** `/home/lebbi/OpenDeskViewer`. Platform lives in `platform/`.
**Toolchain:** Go at `/usr/local/go/bin/go` (1.26.4), Node 22, Docker 29. Run Go commands as
`go -C /home/lebbi/OpenDeskViewer/platform/api <cmd>` — the module root is `platform/api`, not
the repo root.

---

## What this project is

A self-hosted management platform around RustDesk Server OSS (`hbbs`/`hbbr`), replacing the
paid RustDesk Server Pro for internal remote support. Go API + React portal + Keycloak +
PostgreSQL. `hbbs`/`hbbr` are upstream and unmodified.

The load-bearing insight, verified in this tree: **the stock RustDesk client already contains a
complete Server Pro client.** It calls `/api/login-options`, `/api/oidc/auth`, `/api/ab/*`,
`/api/device-group/accessible`, `/api/peers`, `/api/users`, `/api/heartbeat`, `/api/sysinfo`,
`/api/audit/conn`. If our Go server answers those paths, an unmodified client becomes both the
technician console (address book scoped to authorised devices) and the managed-device agent.
Contract source of truth: `flutter/lib/common/hbbs/hbbs.dart`,
`flutter/lib/models/{ab,group,user}_model.dart`, `src/hbbs_http/sync.rs`.

---

## Current state — verified, not assumed

Confirmed green this pass:

- `go build ./...`, `go vet ./...` exit 0. `go test ./...` passes (`httpx`, `rustdeskapi`).
- Infrastructure hardening is genuinely done: `Caddyfile` has `admin off` and a host-based site
  block (ACME), and `docker-compose.yml` publishes **only** Caddy 80/443 and the RustDesk ports
  (21115-21119). Postgres, Redis, Keycloak and the API are no longer host-exposed. Keycloak
  admin now uses the correct `KEYCLOAK_ADMIN` variable, sourced from env.
- `internal/auth/jwt.go` does real cryptographic validation (`jwt.ParseWithClaims`, RSA key from
  JWKS by `kid`, algorithm/issuer/audience checks). The hand-rolled JWT code is gone.
- `enrollment.ConsumeToken` is a single atomic `UPDATE ... RETURNING` with expiry and revocation
  in the predicate.
- Service layer exists: `internal/peers/service.go`, `internal/addressbook/service.go`.
  Pagination returns a real `total` (`peers/service.go:52-80`).
- `access.Resolver` is now reachable — `peers/service.go:46,180,234` calls
  `IsAdminOrManager` and `CanAccessDevice`.
- `GetUserFromContext` returns `(*User, bool)`, so the nil dereference is a compile error now.
- All Go files are under 500 lines.

That is real progress and the hard security primitives are correct. What follows is what stands
between this and a working system.

---

## P0 — RESOLVED. The stack can boot and a login can complete

All of P0 below is fixed, along with three boot blockers this pass turned up that
were not in the original list. `build`, `vet` and `test` are clean.

What changed:

- **Two-mux split** (`cmd/api/main.go`, `internal/httpx/router.go`). `Mux.Group`
  returns a mux sharing the same `ServeMux` with extra middleware. Public routes
  register on the base mux, protected ones on the group, so a route left off the
  protected list is unreachable rather than unauthenticated.
- **Middleware order reversed** so the first entry is outermost. Two things
  depended on this: request IDs now reach the logger, and CORS preflight
  (`OPTIONS`, which carries no `Authorization`) is answered before the JWT
  middleware instead of 401ing.
- **Fail closed.** `Config.Validate` now only checks what every binary needs;
  `Config.ValidateAPI` adds the issuer settings and `cmd/api` calls it. The nil
  branch inside the middleware is gone — `JWTMiddleware` panics at construction
  on a nil validator or directory.
- **JIT provisioning.** `identity.AuthService.ResolveUser` reads by subject and
  provisions on `pgx.ErrNoRows`. Insert is `ON CONFLICT ... RETURNING id,
  (xmax = 0)`, so the default role is granted only on genuine creation and an
  administrator who removes a role does not get it re-granted at next sign-in.
  `API_BOOTSTRAP_ADMIN_EMAIL` grants Administrator, everyone else Technician.
- **Disabled users rejected** in the middleware, with tests on both credential
  paths.

Three additional blockers found and fixed:

- **`viper.SetEnvPrefix("ODV")` vs. bare env names.** Compose passes
  `POSTGRES_HOST`, `KEYCLOAK_REALM` and the rest unprefixed, so viper read *none*
  of them and the API died at `POSTGRES_HOST is required` before reaching any of
  the above. Prefix removed. `KEYCLOAK_CLIENT_API` was never set in compose at
  all; it is now.
- **The compose healthcheck ran `/usr/local/bin/healthcheck`**, which no
  Dockerfile ever creates, so the API could never report healthy and
  worker/web/caddy could never start. It now uses the same `wget /healthz` probe
  the Dockerfile declares.
- **The client's own session token was rejected on every protected route.**
  `/api/login` returns an opaque token from `client_sessions`, not a JWT, and the
  client replays it. Fixing only the route split would still have left every
  post-login call 401ing. `resolveCredential` now accepts either: three
  dot-separated segments take the JWT path, anything else is a session lookup.
  `GetSessionUser` gained `expires_at > now()` — sessions previously never
  expired. `HandleLogout` was revoking the header verbatim, `Bearer ` prefix
  included, so it never matched a row.

Original findings, for the record:

### 1. JWT middleware is applied to every route, including the ones that must be public

`cmd/api/main.go:106-118` appends `httpx.JWTMiddleware` to `middlewareList` and passes the whole
list to `httpx.NewRouter(...)`. `httpx.Mux.Handle` wraps **every** registered handler with
**every** middleware. `JWTMiddleware` (`internal/httpx/jwt_middleware.go:32-38`) rejects any
request with no `Authorization` header, unconditionally. There is no path allowlist.

Consequences, all of them fatal:

- `/api/login`, `/api/login-options`, `/api/oidc/auth`, `/api/oidc/auth-query` now require a
  token in order to obtain a token. **The login flow cannot complete. No user can ever
  authenticate.**
- `/api/heartbeat`, `/api/sysinfo`, `/api/sysinfo_ver` — devices do not send JWTs. All telemetry
  returns 401, so device registration, online status and the whole connectivity state machine
  are dead.
- `/healthz`, `/readyz` return 401. The API container never reports healthy, and because
  `docker-compose.yml` gates `worker`, `web` and `caddy` on `api: condition: service_healthy`,
  **the stack never finishes coming up.**

**Fix:** make the middleware path-aware, or better, split the router into a public mux and a
protected mux and apply `JWTMiddleware` only to the protected one. Public set:
`/api/login`, `/api/login-options`, `/api/logout`, `/api/oidc/*`, `/api/heartbeat`,
`/api/sysinfo`, `/api/sysinfo_ver`, `/healthz`, `/readyz`. Everything else protected.
Prefer the two-mux split — an allowlist inside the middleware is one typo away from silently
exposing a route, whereas a separate mux makes the default deny.

### 2. Authentication fails open

`cmd/api/main.go:114-116`:

```go
if jwtValidator != nil {
    middlewareList = append(middlewareList, httpx.JWTMiddleware(jwtValidator, authService))
}
```

`jwtValidator` is nil whenever `cfg.KeycloakURL == ""` (`:93-104`). A missing or misspelled
`KEYCLOAK_URL` therefore **silently removes all authentication from every endpoint**, with no
log line and no startup failure. `jwt_middleware.go:51` (`if validator != nil`) repeats the
same inversion one layer down.

**Fix:** validate configuration at startup and refuse to boot without a usable issuer. Delete
the nil branch inside the middleware — a nil validator should be impossible by construction.

### 3. No just-in-time user provisioning: a fresh install has zero users

`jwt_middleware.go:60` calls `authService.GetUserByKeycloakSubject(ctx, claims.Subject)` and
401s with `"user not found"` if there is no row. Nothing ever creates that row, and
`migrations/00001_initial_schema.up.sql` seeds roles but no users. So even after fixing findings
1 and 2, a valid Keycloak login is rejected forever.

**Fix:** on first successful token validation, create the user from the token claims
(`sub`, `email`, `preferred_username`) and assign a default role. Honour the existing
`API_BOOTSTRAP_ADMIN_EMAIL` config field by granting Administrator to a matching email. This is
the difference between a deployable product and one that cannot be used at all.

### 4. Disabled users still authenticate

The middleware loads the user and never checks `user.Active`. Round-3 added `u.active = true`
to the resolver, but that only filters device-scoped queries — `/api/currentUser` and
`/api/users` admit a disabled account. Design §40 requires "Disabled user → Everything = DENY".

**Fix:** reject `!user.Active` in the middleware, with a test.

---

## P1 — RESOLVED. The address book matches the client

### 5. Address book routes do not match the client — DONE

All twelve paths are implemented and registered, and a script comparison of
`ab_model.dart` against `cmd/api/main.go` shows twelve for twelve. The only
client path deliberately left out is bare `/api/ab`, which is the legacy-mode
fallback the client only uses when `/api/ab/personal` 404s.

**The model.** There are two kinds of book and they are stored differently:

- The **personal book** is real storage (`ab_profiles`, `ab_peers`, `ab_tags`,
  migration `00002`), one per user, created on first call to `/api/ab/personal`,
  fully writable, `rule = fullControl`.
- **Shared books are projections, not rows**: one per support group the user
  belongs to, plus a fleet-wide book for administrators, with the support group's
  own id used as the book's guid and a fixed sentinel
  (`00000000-0000-0000-0000-0000000000fe`) for the fleet. Their peers come
  straight from `devices`, so membership follows device assignment with nothing
  to keep in sync. They are `rule = read` and writes answer 403.

That is the deliberate trade: a technician's authorised devices appear
automatically and cannot be edited into an inconsistent state, and the personal
book is where their own aliases, notes and tags live.

**Contract details that are easy to get wrong**, all now covered by tests:

- A successful mutation must answer **200 with an empty body**. The client's
  `_jsonDecodeActionResp` treats any content on a 200 as an error message.
- `/api/ab/tags/{guid}` returns a **bare JSON array**, not a `{total, data}`
  envelope like the others.
- `forceAlwaysRelay` is serialised by the client as the **string** `"true"`, so
  the request decoder accepts either a bool or a quoted one.
- Tag colours are Flutter `Color.value` ARGB, which **overflows int4**; the
  column is `BIGINT`.
- `/api/ab/peer/update/{guid}` is a **partial** update. Absent stays absent all
  the way to the SQL, via pointers and `COALESCE`.
- The client sends `current` and `pageSize` **in the query string on a POST with
  an empty body**. `httpx.ParsePagination` only read the body for non-GET, so it
  would have paged at the default 10 while the client advanced its cursor by 100
  and never saw the tail of any list. Fixed to prefer the query.

Original text:



`cmd/api/main.go:136-138` registers `/api/ab/profiles`, `/api/ab/profile/{guid}`,
`/api/ab/shared/profiles`. The first two are invented paths the client never requests.

What `flutter/lib/models/ab_model.dart` actually calls (12 paths):

```
/api/ab/settings          /api/ab/personal         /api/ab/peers
/api/ab/shared/profiles   /api/ab/tags/{guid}      /api/ab/tag/{guid}
/api/ab/tag/add/{guid}    /api/ab/tag/rename/{guid}  /api/ab/tag/update/{guid}
/api/ab/peer/{guid}       /api/ab/peer/add/{guid}    /api/ab/peer/update/{guid}
```

One of twelve overlaps. The address book is the core of the Pro-compat approach and is
unreachable. Note the client's graceful degradation: a 404 on `/api/ab/personal` puts it in
"legacy mode" (`ab_model.dart:271`), and 404 on `/api/ab/settings` disables shared address books
(`:240`) — so this currently fails quietly rather than loudly.

**Fix:** implement the twelve real paths. Start with `settings`, `personal`, `peers`, `tags`
— those four make the address book populate. `POST` with `?current=&pageSize=` and a
`{total, data}` envelope.

---

## P1 — The remaining disorganisation, specifically

These are the spots where the structure still fights you rather than helping.

### 6. Untyped constructor wiring defeats the service-layer refactor — DONE

Constructors take `access.Resolver`. Every `interface{}` parameter and every type
assertion in `internal/rustdeskapi` is gone, including the anonymous-interface
assertion in `users.go`.

The same `interface{}` habit was hiding worse damage in the data layer, which was
fixed at the same time because it silently disabled the endpoints:

- `var rows interface{}` with `for rows.(interface{ Scan(...interface{}) error })
  != nil` as the loop condition, in `peers/service.go` and twice in
  `access/resolver.go`. That is not a scan loop: it breaks on the first
  iteration, so `ListAccessiblePeers`, `GetAccessibleDevices` and
  `GetAccessibleDevicesPaginated` returned an empty slice for every caller, with
  a correct-looking `total` beside it. Now `pgx.Rows` with real `Next`/`Scan`.
- `LIMIT $1 OFFSET $2` in queries whose `$1` was already the user id, called with
  `(limit, offset)`. The technician branch filtered devices by page size.
- `LEFT JOIN device_groups dg ON d.device_group_id = dg.id` and `d.note` —
  neither column exists on `devices`. `LEFT JOIN users u ON c.id = u.id` joins a
  uuid to a bigint. Every one of those queries would error at runtime. Device
  group now comes from `device_group_members` via a lateral join that keeps the
  result at one row per device, matching what the `COUNT` assumes; `user_name` is
  the customer name.
- Authorisation is now an `EXISTS` predicate shared by the list and count
  queries, so the two cannot disagree.

Original text:



```go
// internal/rustdeskapi/peers.go:21
service: peers.NewService(db, accessResolver.(access.Resolver)),
// internal/rustdeskapi/addressbook.go:22
service: addressbook.NewService(db, accessResolver.(access.Resolver)),
```

The handler constructors accept `interface{}` and type-assert. That throws away exactly the
compile-time safety the refactor was meant to buy, and panics at startup on a wrong type.

Worse, `internal/rustdeskapi/users.go:43`:

```go
isAdmin, err := h.access.(interface {
    IsAdminOrManager(...)
})...
```

An anonymous interface assertion. It bypasses `access.Resolver` entirely, cannot be mocked, is
unreadable, and panics if the method set drifts.

**Fix:** constructors take `access.Resolver` directly. Delete every `interface{}` and every type
assertion in `internal/rustdeskapi`. This is a mechanical change and it is the single highest
readability win left in the codebase.

### 7. Raw SQL still in the HTTP layer — DONE

`users.go` and `oidc.go` go through `identity.Service`. `grep -rn "SELECT "
internal/rustdeskapi/` returns nothing; the two remaining `INSERT`s are in
`audit.go`, which is the client-facing intake endpoint. Note the old `users.go`
scanned a `BIGSERIAL` id into a `string`, so `/api/users` could not have worked.

Original text:



`internal/rustdeskapi/users.go:55-86` and `internal/rustdeskapi/oidc.go:225-234` still query the
database directly. Move them behind `identity.Service`, matching what `peers` and `addressbook`
now do. Then `grep -rn "SELECT " internal/rustdeskapi/` should return nothing.

### 8. `internal/auth/jwt.go` polish — DONE

All four points. Validation is now `jwt.WithValidMethods`, `WithExpirationRequired`,
`WithIssuer` and `WithAudience`; the hand-rolled checks and the shadowed `Subject`
and `Audience` fields are gone; `ExpiresAt` is guarded; and the keyfunc is built
per call so it carries the request context and refetches the JWKS on an unknown
`kid`. Covered by `internal/auth/jwt_test.go`.

Original text:



- **Nil dereference:** `:246` guards `claims.ExpiresAt != nil`, then `:262` calls
  `claims.ExpiresAt.Unix()` unguarded. A token with no `exp` panics.
- **Shadowed claims:** the `Claims` struct (`:32-37`) redeclares `Subject` and `Audience`,
  shadowing the embedded `jwt.RegisteredClaims` fields, so the library validates empty values.
- **Hand-rolled checks:** `:224-248` reimplements issuer/audience/expiry validation that
  `golang-jwt/v5` provides as parser options. Replacing them with `jwt.WithIssuer`,
  `jwt.WithAudience`, `jwt.WithExpirationRequired` and
  `jwt.WithValidMethods([]string{"RS256"})` removes all three problems at once and shortens the
  file.
- **JWKS never refreshes on `kid` miss** (`:180-208`), so a Keycloak key rotation causes up to
  5 minutes of total auth outage. `context.Background()` at `:180` also discards request
  cancellation.

---

## P2 — Tests: `internal/auth` done, `internal/access` still open

`internal/auth/jwt_test.go` now covers the full list below against a throwaway
RSA keypair and an `httptest` JWKS: forged and tampered signatures, `alg: none`,
HMAC algorithm confusion, expired, missing `exp`, missing `sub`, wrong issuer,
wrong audience, and a key rotation forcing a JWKS refetch.

`internal/httpx` covers the P0 assertions directly: `/healthz`, `/api/login` and
`/api/heartbeat` return 200 with no `Authorization` header while `/api/peers`
returns 401; forged token 401; disabled user 401 on both credential paths; nil
validator panics; middleware nesting order.

Still open: `internal/access` reports `[no test files]`, and there are no
enrollment-concurrency or contract/golden tests. Original text follows.

`go test` is green, but `internal/auth` and `internal/access` both report `[no test files]` —
the two most security-critical packages in the repo, and `internal/auth` is where a total
authentication bypass shipped two rounds ago. The existing tests assert "missing header",
"bad format", "method not allowed" and context-helper behaviour. None of them would have caught
findings 1 through 4.

Required before this is callable done:

**`internal/auth`** — forged signature rejected; `alg: none` rejected; HMAC-signed token
rejected (algorithm confusion); expired rejected; wrong issuer rejected; wrong audience
rejected; missing `exp` rejected rather than panicking. Generate a throwaway RSA keypair in the
test and serve a JWKS from `httptest`.

**`internal/access`** — the design §40 matrix: technician in-group ALLOW, out-of-group DENY,
admin ALLOW-all, manager ALLOW-all, read-only view-but-not-connect, disabled user DENY,
`DISCOVERED` device DENY, reassignment revoking the old customer's access.

**`internal/enrollment`** — N concurrent `ConsumeToken` against `max_uses = 1` yields exactly
one success.

**Contract/golden tests** — one per Pro endpoint, asserting the `{total, data}` envelope and the
field names and types from `flutter/lib/common/hbbs/hbbs.dart`: `status` is an **int**
(1 normal, 0 disabled, -1 unverified), `is_admin` is a **bool**. These are the only defence
against upstream drift in a reverse-engineered contract.

Use `testcontainers-go` for the packages that need PostgreSQL.

---

## P3 — Scope still outstanding

9. **No `internal/apiv1`.** The portal has no API. `openapi.yaml` documents only the Pro paths
   and still contains the invalid template `/api/ab/peer/{add,update}/{guid}` (that is two
   paths, not one).
10. **No `internal/audit` service.** `rustdeskapi/audit.go` is the client-facing intake
    endpoint, not the reusable `audit.Record(actor, action, resource, metadata)` from design
    §32. Administrative mutations are unaudited.
11. **The portal is still a mockup.** `platform/web/src/` contains only `App.tsx`, `main.tsx`,
    `index.css` and a test stub, with hardcoded zeros. No router, no auth, no pages, no
    fetching. The `web/Dockerfile` builds the mockup.
12. **Missing Pro endpoints:** the twelve `/api/ab/*` (finding 5), plus `/api/devices/deploy`,
    `/api/devices/cli`, `/api/switch-grant`, `/api/record`, `/api/2fa`.
13. **Missing management features:** device search, transactional reassignment with
    `generateDeviceName`, and `POST /api/v1/devices/{id}/connect` returning
    `rustdesk://connect/<id>?password=<pw>`.
14. **Root clutter**, against `CLAUDE.md`'s "never save working files to the root folder":
    `COMPLETION_CHECKLIST.md`, `IMPLEMENTATION_SUMMARY.md`, `INSTALL.md`, `README_PLATFORM.md`,
    `install.sh`, `install_and_verify.sh`, `verify_structure.sh`. Fold anything worth keeping
    into `platform/README.md` and delete the rest.
15. **`platform/casbin/model.conf`** is unused and `CasbinResolver` contains no Casbin. Drop the
    directory and rename the type to `SQLResolver`.
16. **Client builds unverified.** `.github/workflows/odv-{android,linux,windows}.yml` exist but
    have never been run. Note the build-time config premise is false: there is **no
    `option_env!` anywhere in `libs/hbb_common/src/`**, `RS_PUB_KEY` is a hardcoded const
    (`config.rs:121`) and `PROD_RENDEZVOUS_SERVER` initialises to `""` with no writer
    (`config.rs:70`). Pre-configuration must come from a patched `hbb_common` fork or from
    runtime options (`custom-rendezvous-server`, `key`, `api-server`). Confirm which was chosen
    before trusting these workflows.

---

## Suggested order of work

Findings 1-8 are done, plus the three boot blockers, the session-token gap and
the pagination bug. The SQL is no longer taken on trust: `internal/integration`
runs 24 tests against a real PostgreSQL, and every query in `access`, `peers`,
`addressbook` and `identity` is exercised there. Remaining:

1. **Bring the whole stack up** with Keycloak in the loop, and point a real
   RustDesk client at it. The Go side is covered against a real database, but
   nothing has run against a real Keycloak, and no actual client has connected.
2. **Findings 9-11.** Audit service, `apiv1`, then the portal, which is still a
   mockup.
3. **Findings 12-16.** Remaining endpoints and cleanup.

## Running the database tests

They are skipped unless `ODV_TEST_DB=1`, so the default `go test ./...` still
runs without a database.

```bash
docker run -d --name odv-test -e POSTGRES_PASSWORD=odvtest -e POSTGRES_USER=odv \
    -e POSTGRES_DB=odv -p 55432:5432 postgres:16-alpine
docker exec -i odv-test psql -v ON_ERROR_STOP=1 -U odv -d odv \
    < platform/migrations/00001_initial_schema.up.sql
docker exec -i odv-test psql -v ON_ERROR_STOP=1 -U odv -d odv \
    < platform/migrations/00002_address_book.up.sql
ODV_TEST_DB=1 /usr/local/go/bin/go -C platform/api test ./internal/integration/ -v
```

Host, port, database, user and password are overridable with `ODV_TEST_PG_*`.

---

## Verification checklist

Run these in order; each must pass before moving on.

```bash
GO=/usr/local/go/bin/go
$GO -C /home/lebbi/OpenDeskViewer/platform/api build ./...
$GO -C /home/lebbi/OpenDeskViewer/platform/api vet ./...
$GO -C /home/lebbi/OpenDeskViewer/platform/api test ./...
```

- [x] Build, vet, test clean.
- [x] `internal/auth` no longer reports `[no test files]`. `internal/access` still does.
- [x] `GET /healthz` with **no** Authorization header returns **200**. Proves finding 1.
      (`TestPublicRoutesDoNotRequireAToken`, unit level.)
- [x] `POST /api/login` with no Authorization header is **not** 401. Proves finding 1.
- [x] `POST /api/heartbeat` with no Authorization header is accepted. Proves finding 1.
- [x] `GET /api/peers` with no Authorization header returns **401**. Proves auth still applies.
- [x] `GET /api/peers` with a forged-signature token returns **401**.
- [x] Starting the API without a usable issuer **fails to boot** rather than starting
      unauthenticated. Proves finding 2. Note the original diagnosis was slightly off:
      `KeycloakURL` is built with `fmt.Sprintf("http://%s:%d", ...)` and so is never
      empty, which made the nil branch dead code rather than a live fail-open. The
      real fail-open risk was the missing realm and client-id validation.
- [x] A Keycloak user with no database row is provisioned. Proves finding 3. Unit level
      only — the upsert has not run against Postgres.
- [x] A user with `active = false` gets 401. Proves finding 4.
- [x] `grep -rn "SELECT " platform/api/internal/rustdeskapi/` returns nothing.
- [x] No `interface{}` parameters or type assertions in `internal/rustdeskapi`. (The
      original wording, "`grep interface{}` returns nothing", cannot hold: the handlers
      build `map[string]interface{}` response bodies throughout.)
- [x] Every query in `access`, `peers`, `addressbook` and `identity` runs against a
      real PostgreSQL in `internal/integration` (24 tests), including the
      access-control matrix: technician in-group ALLOW, out-of-group DENY, admin
      ALLOW-all, `DISCOVERED` device invisible.
- [x] All twelve `/api/ab/*` paths answer over the real router with the exact
      requests `ab_model.dart` makes, including the empty-body mutation contract
      and the bare-array tags response.
- [x] `platform/api/openapi.yaml` parses and every `$ref` resolves. It previously
      did neither: an indentation slip at the `current` parameter and a
      `description:Peers list` missing its space meant no tool could ever read it.
- [x] `docker compose -f platform/docker-compose.yml config` is valid. Not yet run:
      `up -d` reaching
      **all services healthy** (this is currently blocked by finding 1).
- [ ] External port scan of the host shows only 80, 443 and 21115-21119.
- [ ] Fresh volume bring-up produces an `enrollment_tokens` table with `token_hash` and no
      `token` column.
- [ ] Simulated device: `POST /api/sysinfo` then `/api/heartbeat` lands as `DISCOVERED`,
      invisible to technicians, becomes `ACTIVE`/`ONLINE` when claimed, then walks
      `STALE` → `OFFLINE` when heartbeats stop.
- [ ] Real desktop RustDesk client pointed at the stack: technician signs in and the address
      book populates with only authorised devices; an admin sees the whole fleet.
- [ ] End-to-end connect writes a `connection_sessions` row **and** the client's own
      `/api/audit/conn` post lands.

---

## Reference

- Contract truth: `flutter/lib/common/hbbs/hbbs.dart`, `flutter/lib/models/ab_model.dart`,
  `flutter/lib/models/group_model.dart`, `flutter/lib/models/user_model.dart`,
  `src/hbbs_http/sync.rs`.
- Client API URL derivation: `src/common.rs:1064-1084` — defaults to
  `http://<rendezvous-host>:21114`, and heartbeat is disabled entirely if the URL contains
  `rustdesk.com` (`src/common.rs:1126`). **Open question: nothing currently listens on 21114.**
  Either publish the API there or always set the `api-server` client option explicitly;
  doing both is safest.
- Connection launch URI: `rustdesk://connect/<id>?password=<pw>`
  (`flutter/lib/common.dart:2482-2500`, `src/core_main.rs:816-885`).
- Windows zero-rebuild provisioning: rename the installer to
  `…host=<h>,key=<k>.exe` (`src/custom_server.rs:39-108`, `src/platform/windows.rs:2084`).
- AGPL: distributing modified Android/Windows/Linux binaries, and any `hbb_common` fork,
  needs sign-off before release.
