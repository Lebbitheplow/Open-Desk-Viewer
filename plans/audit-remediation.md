# OpenDeskViewer: Audit and Remediation Plan

Working plan for the audit remediation. Tracks per-item status so the work can be
picked up in a fresh session. Update the status column as items land.

Status key: `TODO` / `WIP` / `PARTIAL` / `DONE` / `BLOCKED` / `DEFERRED`.

## Context

OpenDeskViewer is a fork of RustDesk carrying a custom management platform in `platform/`
(Go API ~12.5k lines, React portal, Caddy/Docker/Keycloak). The goal is an enterprise
management layer around RustDesk's open-source server. The audit found that the stack cannot
currently start, that one authentication path is a landmine held shut only by a missing
database table, and that the single feature the product exists to sell (controlling who may
reach which machine) is not enforced anywhere.

### Decisions taken with the user

| Question | Decision |
|---|---|
| Tenancy | Single-tenant per deployment. `customers` stay a grouping concept. No org/tenant schema work. |
| Password login | Implement properly (Argon2id, real verification, lockout), plus the missing `user_credentials` migration. |
| Session authorization | Do **not** fork hbbs. Implement the heartbeat control channel the stock client already honours. Document the residual limits. |
| Scope | Get the app genuinely deployable, so that configuring env vars and domains is all that remains, and secure it to that same bar. |

### Deployment model this plan assumes

Managed devices are the project's own preconfigured Android clients. They do not sign in as
users. Human sign-in is technicians and administrators, via Keycloak for the portal and via
`/api/login` for the RustDesk client. Devices are trusted endpoints; the party being
authorized is the technician.

Important correction to the "closed system" assumption: the APK ships the deployment's
RustDesk key, so anyone who obtains one APK has it. The key is a network perimeter, not device
authentication. Device identity has to come from enrollment (Phase 3), not from possession of
the client.

### Two commit messages in this repository are not evidence

Carried forward from `plans/finish-line.md` before it was removed in 5.1, because it is the one
thing in that file not recoverable from git history — it lived in uncommitted edits.

Commits `8fb4beb6e` ("Stream D.1: Create internal/apiv1 endpoints") and `dc94b5349` (deployment
and migrations fixed) both describe work that was not done. Checked directly at the time: eight
of fourteen `apiv1` handlers returned an unconditional empty list, `HandleDevices` never
returned a row, `HandleCustomers` passed a hardcoded user id of 1 to `IsAdminOrManager`, the
migration runner shelled out to a `migrate` binary absent from the image, the API Dockerfile
copied a `platform/api/migrations` directory that had never existed, and `/api/v1/devices/{id}`
was registered twice, which panics `http.ServeMux` before the server listens.

Every one of those specific defects is fixed and covered by a test now. What survives is the
habit: **a commit message is not evidence.** This plan states the command that establishes each
claim for the same reason, and it is why Phase 0 found fourteen defects beyond the seven the
audit listed.

---

## Progress summary

| Phase | Scope | Status |
|---|---|---|
| 0 | Make it start | DONE |
| 1 | Critical security | DONE — 1.1 to 1.4; 1.5, 1.6 closed by 3.1 |
| 2 | High | DONE — 2.1 to 2.8 all landed; 2.3 was a 501 until session 8 implemented it as 6.3 |
| 3 | Enterprise capability gaps | DONE — 3.1 to 3.4; 3.5's second half landed as 6.2 |
| 4 | Medium: testing, correctness | DONE — 4.1 to 4.19 all landed |
| 5 | Low: repository cleanup | DONE — 5.1 to 5.6, plus 5.7 and 5.8 found while doing them |
| 6 | Close the leftovers, build what is missing | DONE — 6.1 to 6.7 and 6.9 built; 6.8 decided and deliberately 501 |

**Nothing is committed.** Every change from Phases 0 to 6 is in the working tree.

---

## Resume here

**The toolchain block is gone.** Session 7 installed Rust 1.82.0 and Flutter 3.24.0, the exact
versions `odv-android.yml` pins, plus the system build dependencies. Three of the four
client-build items are done and verified; see "Toolchains" below for how to get back to a
working build quickly.

| Item | Status |
|---|---|
| 1.3 part 2 | **DONE.** Build-time deployment lock. The spike found the stock mechanism is unusable by a fork, and that the fork had no build-time configuration at all |
| 2.8 | **DONE.** `flutter analyze` run; zero issues in the changed region |
| 3.1 client half | **DONE.** `cargo check` passes; nothing reported in `sync.rs` |
| 3.3 / 6.1 | **DONE in session 8.** Per-device connection passwords: server, device, portal, automatic rotation on access withdrawal |

Everything from Phases 0 to 5 is closed except **3.5's second half**, the non-owning database
role, which is item 6.2 below. The remaining work is Phase 6.

### Toolchains, as installed in session 7

Not in the repository; this is how to reproduce the environment.

```bash
# Rust, pinned to what odv-android.yml uses
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- \
  -y --no-modify-path --default-toolchain stable --profile minimal
rustup toolchain install 1.82.0 --profile minimal
rustup component add rustfmt          # flutter_rust_bridge_codegen shells out to it

# Flutter 3.24.0, likewise
curl -sSfLO https://storage.googleapis.com/flutter_infra_release/releases/stable/linux/flutter_linux_3.24.0-stable.tar.xz
tar xf flutter_linux_3.24.0-stable.tar.xz    # into ~/toolchains

# System build dependencies (Fedora names)
sudo dnf install -y libvpx-devel libyuv-devel opus-devel libaom-devel gtk3-devel \
  clang-devel llvm-devel libxcb-devel libXfixes-devel libxdo-devel alsa-lib-devel \
  pulseaudio-libs-devel cmake ninja-build nasm yasm gstreamer1-devel \
  gstreamer1-plugins-base-devel pam-devel libva-devel libdrm-devel gcc-c++ \
  openssl-devel perl-core
```

Then, and the flags are not optional:

```bash
export CXXFLAGS="-include cstdint"   # vendored libwebm predates GCC 13's stricter headers
# Do NOT also set CFLAGS: libsodium-sys runs `make check` and fails on the inherited flags.
cargo +1.82.0 check --features linux-pkg-config    # linux-pkg-config avoids needing vcpkg
```

For anything Dart, `flutter/lib/generated_bridge.dart` is gitignored and must be generated first
or `flutter analyze` is 900 lines of missing-import noise:

```bash
cargo +1.82.0 install flutter_rust_bridge_codegen --version 1.80.1 --locked   # matches Cargo.toml's =1.80
cd flutter && flutter pub get && cd ..
flutter_rust_bridge_codegen --rust-input ./src/flutter_ffi.rs \
  --dart-output ./flutter/lib/generated_bridge.dart    # needs flutter on PATH
```

Note that `flutter analyze` does not pass on this tree and never has — see 2.8 for the 897
pre-existing errors and their single structural cause. Measure a change by the **delta**, by
stashing it and re-running, not by the absolute count.

Note the related constraint from Phase 2: `/api/audit/conn` and `/api/audit/file` are on the
authenticated mux, and the stock client posts them with an empty auth header. The device
credential from 3.1 is now the natural fix — those two routes should accept a device credential
as well as a user token, which is a small follow-up rather than a design question.

### Running the tests

`go` is at `/usr/local/go/bin`, which is not on `PATH` by default in this environment.

```bash
export PATH=$PATH:/usr/local/go/bin
cd platform/api
go build ./... && go vet ./... && go test ./...
```

The integration suite skips silently without a database, and it is the only thing that
exercises the migrations. Start one first:

```bash
docker run -d --rm --name odvtestpg \
  -e POSTGRES_USER=odv -e POSTGRES_PASSWORD=odvtest -e POSTGRES_DB=odv \
  -p 55432:5432 postgres:16-alpine

export ODV_TEST_DB=1 ODV_TEST_PG_HOST=127.0.0.1 ODV_TEST_PG_PORT=55432 \
       ODV_TEST_PG_DB=odv ODV_TEST_PG_USER=odv ODV_TEST_PG_PASSWORD=odvtest
go test -count=1 ./...
```

Use `-count=1`: a cached `ok` line looks identical to a real run. And do not stop the database
container until the run has actually finished, or every integration test fails in 0.00 s and
looks like a regression.

The migration round-trip tests (4.8) additionally need `CREATE DATABASE`, because they run in a
scratch database of their own rather than the one the integration suite is using. The bundled
`postgres:16-alpine` container grants that to `POSTGRES_USER`; a locked-down CI database may
not, and those two tests will fail rather than skip if it does not.

### Bringing the stack up locally

Ports 80, 443 and 8080 are often taken on the dev machine, so override them. `PUBLIC_HOST` can
be `localhost`; Caddy issues its own certificate for it, hence `curl -k`.

```bash
cd platform
export PUBLIC_HOST=localhost POSTGRES_DB=odv POSTGRES_USER=odv POSTGRES_PASSWORD=odvlocaltest \
       KEYCLOAK_DB_PASSWORD=kclocaltest KEYCLOAK_ADMIN_PASSWORD=adminlocaltest \
       OIDC_CLIENT_SECRET=secretlocaltest RUSTDESK_PUBLIC_KEY=placeholderkey \
       API_BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
       JWT_SECRET=$(printf 'x%.0s' {1..70}) \
       CADDY_HTTP_PORT=18080 CADDY_HTTPS_PORT=18443
docker compose -p odvphase0 up -d --build
```

Keycloak takes about a minute and only reports healthy once the realm has imported, so its
healthcheck doubles as the import check. `docker compose restart keycloak` will **not** pick up
an edited realm file, because editing it replaces the inode behind the bind mount; use
`up -d --force-recreate keycloak`. Tear down with `docker compose -p odvphase0 down -v`.

The Keycloak admin console is deliberately not routed through Caddy. Reach it with
`docker compose -p odvphase0 port keycloak 8080`, or run admin API calls from a container on
the compose network.

---

## Phase 0: Make it start

Nothing below can be tested until these are fixed. Each is a defect that proves the stack has
never been brought up.

| # | Status | File | Defect |
|---|---|---|---|
| 0.1 | DONE | `platform/docker-compose.yml` | `- DEBUG=${DEBUG:-false}` was indented 7 spaces where its siblings use 6. Invalid YAML sequence entry, so `docker compose up` would not parse the file at all. Also dropped the obsolete `version:` key. |
| 0.2 | DONE | `.github/scripts/check-client-config.sh` | Hardcoded `cd /home/lebbi/OpenDeskViewer`. Now resolves the repo root from `git rev-parse --show-toplevel` relative to the script. |
| 0.3 | DONE | `platform/web/Dockerfile` | Now declares `ARG`/`ENV` for `VITE_API_URL`, `VITE_OIDC_ISSUER`, `VITE_OIDC_CLIENT_ID`, fails the build if the issuer is empty, and compose passes them via `build.args`. |
| 0.4 | DONE | `platform/docker-compose.yml` | Keycloak now runs `start --import-realm`. Dropped `--features=preview`. Deleted the superseded `platform/keycloak/init.sh`, which imported the realm over the admin API and did no placeholder substitution. |
| 0.5 | DONE | `platform/Caddyfile` | Added a named `@keycloak` matcher covering `/realms/*`, `/resources/*` and `/js/*`. `/admin` is deliberately not exposed. |
| 0.6 | DONE | `platform/postgres/init/10-keycloak-database.sh` | New init script creates the Keycloak role and database. `KC_DB_PASSWORD` is now templated from `KEYCLOAK_DB_PASSWORD` instead of hardcoded. Closes 5.6. |
| 0.7 | DONE | `platform/.env.example` | Rewritten. Fixed the `KEYCLOAK_CLIENT Portal=` space, renamed every variable to the name the code actually reads, removed the six that nothing reads, and added the missing ones. Was briefly blocked by a local tooling rule that denied reading any `.env.*` path; it was narrowed to real secret files so committed placeholder templates stay editable. |

Defects found while fixing the above, folded into Phase 0:

| # | Status | File | Defect |
|---|---|---|---|
| 0.8 | DONE | `platform/docker-compose.yml` | Keycloak `start` refuses to boot without TLS key material unless `KC_HTTP_ENABLED=true`. Behind Caddy it also needs `KC_PROXY=edge`. Both added, plus `KC_HEALTH_ENABLED`. |
| 0.9 | DONE | `platform/docker-compose.yml` | The Keycloak healthcheck shelled out to `curl`, which is not in the `quay.io/keycloak/keycloak` image, so the service could never report healthy and every `depends_on: service_healthy` blocked forever. Rewritten against bash's `/dev/tcp`, asking for the realm's discovery document so it also proves the import succeeded. |
| 0.10 | DONE | `platform/keycloak/realm-opendeskviewer.json` | Contained `${keycloak.host}`, `${keycloak.api.client.secret}` and `${portal.url}` placeholders Keycloak cannot resolve, plus `issuer`, `type`, `providers`, `sslRealmRelativeUrl`, `realmRoles` and `defaultRoles`, which are not part of `RealmRepresentation`. Rewritten with only valid fields. Also enabled PKCE (`S256`) on the portal client and turned off its direct access grants. The first attempt swapped the placeholders for `${env.…}` forms; see 0.17 for why that does not work either and what replaced it. |
| 0.11 | DONE | `platform/api/cmd/api/main.go`, `internal/config/config.go` | The API validated the issuer as `http://keycloak:8080/realms/...` while Keycloak, configured with `KC_HOSTNAME`, mints `https://<public host>/realms/...`. Every browser token would have been rejected. `WithIssuer` now uses `cfg.OIDCIssuer` (public) while the JWKS is still fetched over the internal network; `ValidateAPI` requires `OIDC_ISSUER` and checks it ends in `/realms/<realm>`. |
| 0.12 | DONE | `platform/keycloak/realm-opendeskviewer.json` | The portal client had no audience mapper for `odv-api` on the access token, and the API validates `aud == odv-api`. The portal could sign in and then be rejected by every API call. Added an `api-audience` mapper. |
| 0.13 | DONE | `platform/api/Dockerfile.worker` | Compose builds it with `target: worker` but the final stage was unnamed, so the build failed with "target stage worker could not be found". Named the stage. |
| 0.14 | DONE | `platform/api/Dockerfile`, `Dockerfile.worker` | Both pinned `golang:1.23-alpine` while `go.mod` declares `go 1.25.0`, which the 1.23 toolchain refuses. Bumped to `golang:1.25-alpine`. |
| 0.15 | DONE | `platform/docker-compose.yml` | hbbs published `21117:21117`, which is hbbr's relay port. `docker compose up` failed with "port is already allocated". hbbs keeps 21115, 21116 (TCP and UDP) and 21118; 21117 belongs to hbbr alone. |
| 0.16 | DONE | `platform/keycloak/realm-opendeskviewer.json` | `maxLoginFailureWaitSeconds` is not a `RealmRepresentation` field and Keycloak rejects unknown fields outright. The correct name is `maxFailureWaitSeconds`. |
| 0.17 | DONE | `platform/keycloak/realm-opendeskviewer.json` | Keycloak's `--import-realm` does **not** substitute `${env.VAR}` placeholders; they arrive as literal strings. This was verified: the import failed with "Root URL is not a valid URL" on `${env.KC_PORTAL_URL}`. The portal client now uses relative redirect URIs (`/*`) and `webOrigins: ["+"]`, which follow `PUBLIC_HOST` without the realm file knowing it, and the `odv-api` secret is left for Keycloak to generate. Any future realm-file value that must vary per deployment needs templating at deploy time, not `${env.…}`. |
| 0.18 | DONE | `platform/Caddyfile`, `platform/docker-compose.yml` | The site address was `${PUBLIC_HOST:-localhost}`, which is shell syntax. Caddy uses `{$VAR:default}` and does not substitute the shell form at all, so it tried to parse the literal text as a site address and died on "invalid port '-localhost}'". Caddy could never have started. The caddy service was also passed no environment at all, so even correct syntax would have resolved to an empty host; added a `PUBLIC_HOST` environment entry. |
| 0.19 | DONE | `platform/web/Dockerfile` | The healthcheck used `wget http://localhost:80/`. busybox resolves `localhost` to `::1` first and nginx's `listen 80` binds IPv4 only, so the check was always refused and the container never reported healthy. Changed to `127.0.0.1`. |
| 0.20 | DONE | `platform/keycloak/realm-opendeskviewer.json` | Keycloak rejects any unrecognised key, including `_comment`. JSON has no comment mechanism here; explanatory notes live in this plan instead. |
| 0.21 | DONE | `platform/docker-compose.yml` | The api and worker services listed their environment explicitly, and that list omitted 18 variables `internal/config` reads, including `CORS_ORIGINS` (which 1.4 depends on), `AUDIT_RETENTION_DAYS`, `AB_MAX_PEER_ONE_AB`, the `DEVICE_*` timeouts, the `JWT_*_EXPIRY_SECONDS` pair and every `WORKER_INTERVAL_*`. Setting any of them in `.env` did nothing. Both services now take `env_file: .env` with `required: false`, keeping their explicit `environment` entries, which win, for the internal-network addresses. Verified: a `.env` value for `POSTGRES_HOST` is correctly overridden to `postgres` while `CORS_ORIGINS` and `AUDIT_RETENTION_DAYS` come through. |

### What changed in `.env.example` (0.7), and why

Renamed to the name the code actually reads:

- `KEYCLOAK_CLIENT Portal=odv-portal` → `OIDC_CLIENT_PORTAL` (the space made it an invalid
  assignment, and compose reads `OIDC_CLIENT_PORTAL`).
- `KEYCLOAK_CLIENT_API` → `OIDC_CLIENT_API`, `KEYCLOAK_CLIENT_SECRET` → `OIDC_CLIENT_SECRET`.
- `JWT_ACCESS_TOKEN_EXPIRY_SECONDS` → `JWT_ACCESS_EXPIRY_SECONDS`, and the refresh equivalent.
- `DB_MIGRATION_POLICY=auto` → `ODV_MIGRATE=true`.

Removed because nothing reads them: `KEYCLOAK_JWKS_URI`, `RENDENZVOUS_HOST` (a typo), the four
`ROLE_*` vars (role names are compile-time constants at `identity/service.go:14-16`), the empty
"Casbin model file" comment, and `CADDY_ACME_EMAIL` (no Caddyfile directive consumes it;
wiring an ACME account email is folded into 2.7).

Corrected: `OIDC_ISSUER`, `OIDC_AUTH_URL` and `OIDC_TOKEN_URL` are now the **public** realm
URLs rather than `http://keycloak:8080/...` (see 0.11), and `OIDC_REDIRECT_URI` matches the
portal's `/callback`.

Added: `KEYCLOAK_DB_*` (0.6), `CORS_ORIGINS` documented as "empty means no CORS headers" (1.4),
and `AB_MAX_PEER_ONE_AB`.

Note that fixing the names alone was not enough: 0.21 covers the fact that most of these never
reached the API process at all.

**Exit criterion for Phase 0:** `docker compose up` brings the stack healthy, and an
administrator can sign in to the portal against Keycloak.

### Phase 0 verification, actually run

Brought the whole stack up locally (`PUBLIC_HOST=localhost`, Caddy on 18080/18443) and
measured rather than assumed:

| Check | Result |
|---|---|
| `docker compose config` | parses |
| All eight services | `postgres`, `keycloak`, `api`, `web` report healthy; `hbbs`, `hbbr`, `worker`, `caddy` running |
| Migrations | applied on API startup, schema at version 3 |
| `GET /realms/opendeskviewer/.well-known/openid-configuration` through Caddy | 200 |
| Portal root through Caddy | 200 |
| `GET /api/v1/devices` with no token | 401 |
| Discovery document issuer | `https://localhost/realms/opendeskviewer`, i.e. the public issuer the API now validates (0.11) |
| Portal JS bundle | contains the issuer, proving the Vite build arg was baked in (0.3) |
| Example access token for `odv-portal` | `aud: "odv-api"`, `azp: odv-portal`, confirming the audience mapper (0.12) |
| `go build`, `go vet`, `go test ./...` | pass |
| `npm test` | 24 tests, 2 files, pass |

**Not yet verified:** an interactive administrator sign-in through the browser. The realm
imports with `users: []`, so this needs a user created in Keycloak and a real browser round
trip. Everything that sign-in depends on has been checked individually, but the end-to-end
click-through has not been done.

---

## Phase 1: CRITICAL

| # | Status | Item |
|---|---|---|
| 1.1 | DONE | Password authentication does not verify the password |
| 1.2 | DONE | Session tokens are predictable |
| 1.3 | DONE | Plaintext `:21114` API can reprogram the fleet — redirect landed in session 2, build-time lock landed in session 7 |
| 1.4 | DONE | CORS defaults to allowing every origin with credentials |
| 1.5 | DEFERRED | Device telemetry is unauthenticated and self-registering — closed by 3.1 |
| 1.6 | DEFERRED | Enrollment exists but is never used — closed by 3.1 |

### What landed for 1.1, 1.2 and 1.4

New migration `000004_password_auth`:

- `user_credentials` (the table `Authenticate` queried but which no migration ever created),
  holding an Argon2id PHC string plus `failed_attempts` and `locked_until`.
- `login_attempts`, for per-IP throttling. Per-account lockout alone does not stop an attacker
  spreading guesses across many accounts.
- `client_sessions.rustdesk_token` renamed to `token_hash` and narrowed to 64 characters.
  Existing rows are deleted: they hold plaintext tokens that cannot be converted, and every one
  of them was minted by the predictable generator.

New `internal/identity/password.go`: Argon2id at the OWASP defaults (19 MiB, t=2, p=1), PHC
encoded so the cost can be raised later without invalidating existing hashes, verified with
`subtle.ConstantTimeCompare`.

`Authenticate` rewritten:

- verifies the password instead of ignoring it;
- returns one `ErrInvalidCredentials` for unknown account, wrong password and disabled account
  alike, and verifies against a dummy hash when no account matches so the response time does
  not answer "does this account exist" (measured: 15 ms unknown vs 20 ms known, both dominated
  by the same Argon2 work);
- locks an account for 15 minutes after 5 consecutive failures, and throttles a source address
  after 30 failures in 15 minutes;
- takes a `clientIP` argument, supplied by a new `httpx.ClientIP` that prefers Caddy's
  `X-Real-IP` and falls back to `RemoteAddr`.

`CreateSession` rewritten: 32 bytes from `crypto/rand`, base64url encoded, with only a SHA-256
stored. `GetSessionUser`, `GetClientSession`, `CreateClientSession`, `RevokeSession` and
`InvalidateClientSession` all hash on the way in. Note the last three were **not** in the audit:
they referenced the renamed column in raw SQL, which the Go compiler does not check, so they
would have failed at runtime.

`SetPassword` added, since nothing could previously write a credential.

CORS inverted (1.4): an empty origin list now emits no headers instead of reflecting any
origin with `Access-Control-Allow-Credentials: true`. A preflight from an unlisted origin gets
403. `ValidateAPI` rejects `*`, unqualified origins and trailing slashes in `CORS_ORIGINS`.

Also fixed, being the same lines: 4.10, where `CreateSession` returned an error if the
`last_login` update failed after the session row was already committed, handing the user a 500
alongside a perfectly good session. Now logged.

### 1.1 Password authentication does not verify the password
`platform/api/internal/identity/service.go:304-345`

`Authenticate()` looks the user up by email, selects `password_hash` into `storedHash`, and
never compares it to anything. Any password authenticates any account. The only thing
preventing exploitation today is that `user_credentials` does not exist in any migration, so
the query errors and the path fails closed.

**Fix:** add the `user_credentials` migration, hash with Argon2id, verify with a constant-time
comparison, and return a single generic error for both unknown user and bad password. Add
per-account and per-IP lockout. Until it is implemented, make the function return an explicit
"not enabled" error rather than falling through.

### 1.2 Session tokens are predictable
`platform/api/internal/identity/service.go:348-356`

Built from `time.Now().UnixNano()` and the user ID, no CSPRNG.

**Fix:** 32 bytes from `crypto/rand`, base64url encoded. Store only a SHA-256 hash in
`client_sessions.rustdesk_token` and look up by hash. `internal/enrollment/service.go:52-61`
already does exactly this and is the pattern to copy.

### 1.3 The API is served over plaintext HTTP, and that channel can reprogram the fleet
`platform/Caddyfile:32-38`

The stock client applies `strategy.config_options` from the heartbeat response directly into
its config (`src/hbbs_http/sync.rs:263-268`, `287-304`), which writes arbitrary keys including
`custom-rendezvous-server`, `api-server` and `key`, and persists them via `Config::set_options`
(`libs/hbb_common/src/config.rs:1235-1243`). An on-path attacker who answers a plaintext
heartbeat can permanently re-point every managed device.

**Fix, both parts.**

1. **DONE.** The `:21114` block is now a 308 redirect to `https://${PUBLIC_HOST}{uri}` rather
   than a proxy, so the strategy is never served in the clear. 308 rather than 301 so a POST
   keeps its method and body. Verified against a running Caddy: both GET and POST to
   `/api/heartbeat` return 308 with the path preserved.
2. **DONE, and the spike found something worse than expected.** See below.

#### The spike, and what it turned up

The question was "which build-time mechanism populates `OVERWRITE_SETTINGS`". The answer is
that the stock one **cannot be used by a fork at all**, and that the fork had no build-time
configuration of any kind.

Every route into `OVERWRITE_SETTINGS` goes through `common.rs:read_custom_client`, which
`decode64`s the blob and then calls `sign::verify` against a public key hardcoded in the source
to `5Qbwsde3unUcJBtrx9ZkvUmwFNoExHzpryHuPUdqlWM=` — RustDesk's own. Custom client configs are
their paid feature and only they can sign one. No amount of build wiring gets a fork past that
line.

Worse, and not in the audit: **the fork's Android builds have always pointed at RustDesk's
infrastructure.** `libs/hbb_common/src/config.rs:120-121` hardcodes
`RENDEZVOUS_SERVERS = ["rs-ny.rustdesk.com"]` and `RS_PUB_KEY = "OeVuKk5nlHiXp+..."`, and
nothing in `src/`, `libs/` or `flutter/lib/` reads `RS_PUB_KEY`, `RENDEZVOUS_SERVERS` or
`API_SERVER` from the environment. `.github/scripts/check-client-config.sh` requires all four
variables to be set and then nothing consumes them — and it runs in `odv-platform.yml`'s
`deployment` job while the APKs are built by `odv-android.yml`, a different workflow that never
sets them. A guard checking that four variables exist, in a job that does not build anything,
for values nothing reads. Combined with 5.8 (no `odv-*` workflow has ever run), it had three
independent reasons to be ineffective.

**What landed.** `common.rs:load_odv_locked_settings` inserts the deployment identity directly
into `OVERWRITE_SETTINGS` from four `option_env!` values baked at compile time:
`ODV_RENDEZVOUS_SERVER`, `ODV_RELAY_SERVER`, `ODV_API_SERVER`, `ODV_RS_PUB_KEY`. No signature,
because a fork cannot produce one; no `hbb_common` patch, because
`check-client-config.sh` fails the build on a dirty submodule and that guard is right — upstream
merges have to stay cheap.

This is both halves at once. `Config::get_option` reads `OVERWRITE_SETTINGS` ahead of the saved
options, so the value is what the client uses; and `Config::set_option` calls
`is_option_can_save`, which returns false for any key `OVERWRITE_SETTINGS` contains and then
*removes* it from the options map (`config.rs:1260`, `2760-2772`). The write is not merely
overridden, it is discarded. A pushed `strategy.config_options`, a `--api-server=` boot argument
and a hand-edited config file all fail identically.

Called from three places, because each covers a path the others miss: the top of
`load_custom_client` (core_main, service, and the Flutter no-config branch), the end of
`read_custom_client` (so a signed config's own `override-settings` cannot win by landing later),
and the Android JNI `startServer` (where the background service would otherwise run unlocked
while the UI process was locked). `build.rs` emits `cargo:rerun-if-env-changed` for all four,
without which a rebuild after changing `API_SERVER` would silently ship the previously baked-in
value.

`enrollment-token` and `device-token` are deliberately **not** locked: `sync.rs` has to write
one and clear the other.

**Verified, both directions.** `common::tests::odv_deployment_lock_is_read_preferred_and_unwritable`
asserts the locked value is returned *and* that a subsequent `set_option` to
`https://attacker.example` does not take. It passes with the variables set and, on a build
without them, asserts the opposite contract — that nothing is locked and the client behaves
exactly like upstream. The baked-in value was confirmed present in the compiled test binary with
`strings`, so the test is known to have taken the locked branch rather than the empty one. A
negative control (expecting the attacker value) fails with
`left: "https://odv.example.com" / right: "https://attacker.example"`, which is the lock working
and the assertion proved load-bearing.

**Still true after this.** The lock protects the four server-identity keys. Everything else in
`pushableOptions` (3.2) remains pushable by design, and a device provisioned by a build that set
none of the `ODV_*` variables is exactly as exposed as before — the lock is only as good as the
build that ships.

**Honest limit of part 1.** A redirect moves the *response* onto TLS but the client's first
request still leaves in the clear, so its body is exposed even though the reply is not. It also
does nothing against an attacker who answers on port 21114 instead of letting the redirect
through — the client asked for plaintext, and nothing our server does can prevent someone else
replying. The redirect's real value is that our own server never serves `strategy` over
plaintext and that misconfiguration becomes visible. The actual fix is to always provision
`api-server`, plus part 2.

**Windows filename route.** `NewAuditHandler` already sets `api-server` to `https://<host>`
(`audit.go:38`). The filename route cannot: a Windows filename cannot contain the `://` of a
URL, so `rustdesk-host=…,key=….exe` provisions host and key only and the client falls back to
deriving `http://<host>:21114`. The generated script now says so and tells the operator to
follow up with the explicit `--config` command rather than presenting the filename route as
equivalent.

### 1.4 CORS defaults to allowing every origin with credentials
`platform/api/internal/httpx/router.go:129-152`

Empty `allowedOrigins` sets `allowed = true` unconditionally, reflects `Origin`, and sets
`Access-Control-Allow-Credentials: true`.

**Fix:** invert the default. No configured origins means no CORS headers. Add `CORS_ORIGINS`
to `.env.example` and validate it in `ValidateAPI()`.

### 1.5 Device telemetry is unauthenticated and self-registering
`platform/api/cmd/api/main.go:143-145`, `internal/telemetry/telemetry.go:52-71`

`/api/heartbeat`, `/api/sysinfo` and `/api/sysinfo_ver` are public and `ProcessHeartbeat`
registers any unknown device ID. Anyone can insert unlimited device rows, squat a
`rustdesk_id` or `uuid` before a real device enrolls, or forge liveness.

**Fix:** fixed in Phase 3.1 by requiring a device credential. In the interim, rate limit hard
by IP and stop auto-registering unknown IDs; record them as unclaimed observations.

### 1.6 Enrollment exists but is never used
`platform/api/internal/enrollment/service.go`

`ConsumeToken`, `ValidateToken` and `GetDeviceEnrollmentInfo` have zero callers. Additional
defects in the same feature:

- `rustdeskapi/enrollment.go:103` calls `ListTokens(ctx, uuid.Nil)`, so the list is always empty.
- `enrollment.go:127-133` ignores limit and offset. Pagination is cosmetic.
- `enrollment.go:178-192`: omitting `expires_at` yields Go's zero time and omitting `max_uses`
  sets 0, and `ConsumeToken` rejects both. Both defaults produce an unusable token.
- Neither issuance nor revocation writes an audit event.

**Fix:** as part of Phase 3.1. Fix the defaults and the list query at the same time.

---

## Phase 2: HIGH

| # | Status | Item |
|---|---|---|
| 2.1 | DONE | The audit trail can be forged by any authenticated user |
| 2.2 | DONE | No rate limiting anywhere |
| 2.3 | DONE | The OIDC broker returns a fake token — now 501 |
| 2.4 | DONE | Error text leaks validation internals |
| 2.5 | DONE | Query strings are logged in full |
| 2.6 | DONE | Postgres connections are plaintext and the DSN is unescaped |
| 2.7 | DONE | No security headers |
| 2.8 | DONE | Client boot arguments can silently re-point an installed client — analyzed in session 7 |

### 2.1 The audit trail can be forged by any authenticated user
`platform/api/internal/rustdeskapi/audit.go`

`POST /api/audit/conn` and `POST /api/audit/file` took `device_id`, `user_id` and
`support_group_id` verbatim from the body, with no check that the caller was the claimed user
and no `CanAccessDevice` check. `device_id` and `user_id` were also inserted as strings into
UUID and bigint columns.

**Landed.** Both handlers now:

- take the actor from `httpx.GetUserFromContext`, and ignore any `user_id` in the body;
- resolve the device through `resolveDevice`, which accepts either the RustDesk id the client
  uses or our own uuid, and authorize it with `CanAccessDevice`;
- answer 403 for an unknown device as well as an unreachable one, so the endpoint is not an
  oracle for which device ids exist;
- accept `support_group_id` only when the caller belongs to that group, or is an
  administrator, and store null otherwise rather than rejecting the whole request;
- validate `status` against the `connection_status` enum, parse timestamps as RFC 3339, and
  pass typed values (`uuid.UUID`, `int64`, `*time.Time`) rather than strings.

`/api/audit/file` now writes through `audit.Recorder` instead of its own INSERT, which is what
handles the nullable actor and the metadata marshalling in one place.

**Correction to the audit.** `connection_sessions.client_id` is not `UNIQUE NOT NULL`: it is a
plain nullable `VARCHAR(255)` (`000001_initial_schema.up.sql:211`). The `UNIQUE NOT NULL`
client_id is on `api_clients` (`:247`), a different table. So the "second session with the same
client ID fails" defect does not exist and no migration was needed.

**Two things found while fixing it, both worth carrying into Phase 3.**

1. **The handlers match no client.** The stock client posts
   `{id, uuid, conn_id, session_id, nonce, action}` (`src/server/connection.rs:1507-1519`) and
   the note goes to the same URL as `{id, session_id, note}` (`ui_session_interface.rs:2076`).
   The Go struct expected `{device_id, user_id, protocol, start_time, …}`, which nothing sends.
   `HandleAuditConn` now accepts both shapes; `action: close` maps to `ENDED` and fills
   `end_time`. The note path still lives on `PUT /api/audit` by GUID and does not match the
   client's `POST` with `{id, session_id, note}`; that is unresolved.
2. **The route requires a token the client does not send.** These are registered on the
   authenticated mux, but the device posts them with an empty auth header
   (`post_request_with_status(url, body, "")`). Every real client audit post therefore 401s
   today. The endpoint is correct for the portal and for anything holding a session token; it
   becomes correct for devices when 3.1 gives them a credential. Making it public instead
   would re-open exactly the forgery this item closes.

Also open, and deliberately not fixed here: `action: new` and `action: close` insert two rows
rather than one row updated in place, because there is no unique key to upsert against. A row
per event is honest and does not lose data; joining them needs a
`(device_id, client_id)` unique index and belongs with 3.2.

### 2.2 No rate limiting anywhere
`platform/api/cmd/api/main.go`

**Landed.** `internal/httpx/ratelimit.go` adds a per-IP token bucket (no new dependency;
`golang.org/x/time` is not in `go.mod`) with a TTL sweep and a hard cap of 50,000 tracked keys
so forged `X-Real-IP` values cannot grow the map without bound. Three groups, because the
traffic genuinely differs:

| Group | Limit | Why |
|---|---|---|
| sign-in | 10/min, burst 5 | A human typing a password. Second line behind the per-account and per-IP lockouts already in `identity.Authenticate` |
| telemetry | 600/min, burst 120 | Devices on a heartbeat timer, a whole site possibly behind one NAT address |
| authenticated | 300/min, burst 60 | The portal, which fires a handful of calls per page |

`/healthz` and `/readyz` are deliberately unlimited: throttling them would take the container
out of rotation under exactly the load the limit exists for. `MaxBodyMiddleware(1<<20)` is on
the base router, so it covers routes added later too.

Honest limits: it is per process, so scaling the API out divides the effective limit by the
instance count; and it keys on `X-Real-IP`, which is only trustworthy because Caddy sets it
rather than forwarding a client-supplied one. Running the API without that proxy in front makes
the key attacker-controlled.

### 2.3 The OIDC broker returns a fake token
`platform/api/internal/rustdeskapi/oidc.go`

> **Superseded by 6.3, which implemented it.** The client-secret decision this section describes
> as open turned out not to need making: the exchange runs as the public `odv-portal` client with
> PKCE, so no secret is involved. The rest of this section is kept as the record of what the 501
> was and why it was the right interim answer.

**Landed as 501, not as an implementation.** `HandleAuthQuery` returned 200 with
`"access_token": "placeholder_token"`, which a caller cannot distinguish from a real token: it
stores it, sends it, and only discovers at the first API call that it authenticated nobody.

The exchange still needs the `odv-api` client secret, which the realm file deliberately no
longer pins (0.17), so implementing it means either reading the generated secret out of
Keycloak into `OIDC_CLIENT_SECRET` or templating the realm file at deploy time. That decision
is still open. Until it is made, 501 is the honest answer, and it removes the hole now. The
portal is unaffected: it exchanges its code with Keycloak directly.

`openapi.yaml` was corrected at the same time — it documented this route as `GET` with query
parameters, while the handler has only ever accepted `POST`.

### 2.4 Error text leaks validation internals
**Landed.** `oidc.go` logged nothing and returned `fmt.Sprintf("invalid token: %v", err)`,
which names the failing claim, the expected audience and the issuer — a description of how to
build an acceptable token, handed to whoever presented an unacceptable one. Now logged at warn
with the path, and the response is the generic form `jwt_middleware.go` already used.

### 2.5 Query strings are logged in full
**Landed.** `httpx.redactQuery` replaces the values of parameters whose names contain any of
token, secret, password, passwd, key, code, auth, signature or sig, case-insensitively, and
keeps everything else so the log still shows which filters were used. A query string that does
not parse is logged as `[unparseable]` rather than raw, since that is exactly where an
unexpected secret would hide. Covered by a table test.

### 2.6 Postgres connections are plaintext and the DSN is unescaped
**Landed.** `postgres.DSN` builds the connection string with `net/url`, so a password
containing `@`, `/` or `?` no longer changes which host is connected to. `sslmode` is a
parameter defaulting to `require`, validated in `Config.Validate` against the six libpq values
(a `POSTGRES_SSLMODE=disabled` typo used to surface as an unrelated libpq error at connect
time). The migration runner in `cmd/api/main.go` now calls the same builder, so it cannot drift
onto a different TLS setting from the pool that serves traffic. compose sets
`POSTGRES_SSLMODE=disable` explicitly for the bundled database, which has no certificate and
sits on the private network; an external database gets `require` unless overridden.

### 2.7 No security headers
**Landed.** `platform/Caddyfile` sets HSTS, `X-Content-Type-Options`, `X-Frame-Options`,
`Referrer-Policy`, `Cross-Origin-Opener-Policy` and `Permissions-Policy` for the whole site,
and strips the `Server` banner.

The CSP is applied through a `not path /realms/* /resources/* /js/*` matcher rather than
globally: Keycloak's login pages carry their own policy and their own inline script, so
imposing `script-src 'self'` on them would break the one page that has to work. `style-src`
keeps `'unsafe-inline'` because the portal's stylesheet is injected at runtime; scripts get no
such exemption.

The ACME account email left over from 0.7 landed here too, as `email "{$CADDY_ACME_EMAIL:}"` in
the global block. The quotes matter: an unset variable expands to no argument at all and Caddy
then fails to parse the directive rather than treating it as absent. Verified with
`caddy validate` both unset and set.

### 2.8 Client boot arguments can silently re-point an installed client
`flutter/lib/main.dart` (fork-authored)

**Landed.** `applyPreConfig` is now first-run only. It collects the four server-identity
arguments, validates each value (non-empty, no whitespace or control characters, and
`api-server` must parse as an absolute http or https URL), then refuses to change anything if
the client already knows its deployment — `custom-rendezvous-server` or `api-server` already
set, whether from an earlier run or baked into the build — unless `--overwrite-settings` is
also passed. Re-running provisioning with identical values is not treated as a change, so it
stays idempotent. A plain-HTTP `api-server` is accepted, because a lab deployment on a private
network is legitimate, but it logs why that is a channel which can reprogram the client.

**Analyzed in session 7, on Flutter 3.24.0 / Dart 3.5.0, the version `odv-android.yml` pins.**
`flutter analyze` reports **zero** errors, warnings or infos anywhere in the changed region.

Measuring it needed care, because the tree does not analyze clean. `flutter analyze` reports 897
errors, and it reports **exactly 897 with this change stashed as well** — identical before and
after, so the change contributes none of them. The one error in `main.dart` is at line 274,
`setResizable(!bind.isIncomingOnly())`, which is upstream code and present either way.

The 897 have a single cause and it is structural upstream: there are two `RustdeskImpl` classes,
one generated into `flutter/lib/generated_bridge.dart:1756` and one hand-written at
`flutter/lib/web/bridge.dart:53`, selected by the conditional import in
`models/platform_model.dart:1-2`. The analyzer resolves both, so identical type names from
different libraries do not unify, which is why the errors read as absurdities like "the argument
type 'bool' can't be assigned to the parameter type 'bool'". **No workflow in this repository,
upstream's or the fork's, runs `flutter analyze`** — the same shape as 4.19 and 5.8, and the
reason 897 errors have gone unnoticed.

`generated_bridge.dart` is gitignored and had to be produced first, with
`flutter_rust_bridge_codegen` 1.80.1 against `src/flutter_ffi.rs`, matching the `=1.80` pin in
`Cargo.toml`. That is what makes the analysis meaningful rather than a wall of missing-import
noise: it is what defines `mainGetOption`, and this change is the first caller of it in this
file. The generated signature is `Future<String> mainGetOption({required String key})`, which
matches `(await bind.mainGetOption(key: ...)).trim()` exactly.

### Phase 2 verification, actually run

Brought the full stack up again (`PUBLIC_HOST=localhost`, Caddy on 18080/18443, project
`odvphase2`) and measured:

| Check | Result |
|---|---|
| All eight services | `postgres`, `keycloak`, `api`, `web` healthy; `hbbs`, `hbbr`, `worker`, `caddy` running |
| Migrations through the new `postgres.DSN` with `POSTGRES_SSLMODE=disable` | applied, schema at version 4 (2.6) |
| Portal response headers | HSTS, CSP, `X-Frame-Options: DENY`, `X-Content-Type-Options`, `Referrer-Policy`, COOP, `Permissions-Policy` present; no `Server` banner (2.7) |
| Keycloak discovery document through Caddy | 200, carries HSTS but not our CSP, which is the matcher working as intended (2.7) |
| `POST /api/login` nine times in a row | 401, 401, 401, 401, 401, then 429 429 429 429, with `Retry-After: 6` (2.2) |
| `GET /api/v1/devices?page=2&token=supersecret` in the API log | `query=page=2&token=REDACTED` (2.5) |
| `POST /api/oidc/auth-query` | 501 when not rate limited (2.3) |
| `go build`, `go vet`, `go test ./...` including `ODV_TEST_DB=1` | pass |

### 2.9 Device telemetry could never have worked (found while verifying)

`internal/fleet/fleet.go`. Not in the original audit. Found by posting a heartbeat at the
running stack: it answered 500, and kept answering 500 for the same device id forever.

`devices.last_seen_at`, `client_version`, `os` and `hostname` are nullable, and `fleet.Device`
reads them into a `time.Time` and three plain `string`s. `RegisterDevice` writes none of them,
so the row it creates cannot be scanned back. The sequence was: first heartbeat registers the
device, then fails scanning it, and returns 500; every later heartbeat takes the same path and
fails identically. `/api/heartbeat` has therefore never succeeded once, against any client.

**Fixed** with a shared `deviceColumns` select list that COALESCEs the four columns, used by
both `GetDeviceByRustdeskID` and `SearchDevices` so the two cannot drift. `'epoch'` reads
correctly as "never seen" in the staleness comparison at `telemetry.go:77`. Covered by
`TestGetDeviceByRustdeskIDReadsAFreshlyRegisteredDevice`, and verified against the running
stack: heartbeat now returns 200 both for a new device id and for the row left broken by the
old code.

This does not change the plan for 3.1, which removes the auto-registration entirely. It does
mean the "device telemetry is unauthenticated and self-registering" defect (1.5) was, up to
now, unauthenticated and self-registering *and broken*.

---

## Phase 3: Enterprise capability gaps

### Feature status as built

| Capability | Status | Evidence |
|---|---|---|
| Users, roles, RBAC | Implemented | 4 seeded roles, `access.SQLResolver`, consistent `admin()` gate |
| Device groups, support groups | Implemented | `apiv1/groups.go`, resolver chain |
| Customers and locations | Implemented | `apiv1/customers.go` |
| Address book | Implemented | `internal/addressbook`, correctly owner-scoped |
| Audit log | Implemented, append-only | 2.1 fixed forgery; 3.5 made it append-only at the database |
| Device enrollment | **Implemented** | `POST /api/enroll`, closes 1.6 |
| Device identity | **Implemented** | Per-device secret, `internal/deviceauth`, closes 1.5 |
| Remote-session authorization | **Advisory only** | `/api/switch-grant` only, and only for side-switch |
| Revocation | **Implemented** | Disconnect and rotation both reach the device at its next heartbeat (3.2, 3.3) |
| Device connection credentials | **Implemented** | `internal/devicepw`, encrypted at rest, released on access check, rotated on withdrawal (3.3) |
| Session recording | Stub | 501 at `rustdeskapi/audit.go` |
| Monitoring, notifications, reporting | Missing | No code |
| Multi-tenant isolation | N/A | Single-tenant by decision |
| "Read Only" role | Dead | Seeded at `migrations/000001:40`, referenced by no code |

| # | Status | Item |
|---|---|---|
| 3.1 | DONE | Give devices a real identity (wire enrollment into heartbeat) — server and client both verified |
| 3.2 | DONE | Implement the heartbeat control channel (`disconnect`, `strategy`) |
| 3.3 | DONE | Platform-managed per-device connection credentials — server, client, portal and tests all landed in session 8 |
| 3.4 | DONE | Document the residual limitation honestly — rewritten in session 8 now that 3.3 changes what is true |
| 3.5 | PARTIAL | Audit log is append-only; the non-owning database role is still to do |

### 3.1 Give devices a real identity

**Landed.** A device is no longer whatever it says it is.

New migration `000005_device_identity`:

- `device_credentials`, one row per device: a lowercase-hex SHA-256 of a 32-byte secret, plus
  `enrolled_by_token`, `enrolled_from`, `last_used_at`, `rotated_at`, `revoked_at`. A fast hash
  is right here and wrong for a password: the input has 256 bits of entropy and the check runs
  on every heartbeat from every device.
- `device_observations`, an upsert keyed by `rustdesk_id`: this is what replaced
  auto-registration. A heartbeat with no valid credential records a sighting and enters nothing.
  A flood from one id costs one row, not one row per request.
- The enrollment token contract, fixed. `expires_at IS NULL` now means no expiry and `max_uses
  IS NULL` means unlimited, which is what the redemption query already half-assumed.

New `internal/deviceauth`: `GenerateSecret`, `HashSecret`, `Issue`, `Authenticate`, `Revoke`,
`IsEnrolled`. `Authenticate` looks a device up by secret alone and returns the device it
belongs to; the caller then compares the claimed id, so a credential used with somebody else's
id is refused rather than silently attributed.

`enrollment.Service.Enroll` does the whole redemption in one transaction: consume the token,
create or claim the device, add it to the token's device group, write the credential, clear the
observation. All of it, or none: burning a single-use token and then failing to create the
device would mean a truck roll for a preconfigured Android client already at a customer site.

`POST /api/enroll` is the one device route that cannot require a credential, because it is where
the credential comes from. It sits in the sign-in rate-limit group (10/min, burst 5) rather than
the telemetry group: a secret is being presented, so guessing is what must be expensive.

`/api/heartbeat` and `/api/sysinfo` now require the secret, in `X-Device-Token`, in
`Authorization: Device …`, or in a `device_token` body field. Three forms because the header is
the right channel, and the body field is what makes the whole path exercisable with curl.

**Also fixed here:** 1.6's list of enrollment defects. `ListTokens` took `uuid.Nil` and matched
nothing, so the list was always empty — it now takes an optional filter and pages in SQL rather
than fetching everything and reporting a page size. Issuance and revocation now write audit
events. `GetDeviceEnrollmentInfo` no longer picks the first device group in the deployment
regardless of customer (4.9).

**Two things found while building it.**

1. **The token hash was stored as a raw `[32]byte` into a `VARCHAR(64)`.** Combined with the
   `expires_at` bug, no token could ever have been redeemed. Migration 000005 deletes existing
   tokens rather than pretending they still work.
2. **`devices.uuid` is `NOT NULL` with a unique index.** Two devices enrolling without a machine
   uuid collided on the empty string. The RustDesk id is unique by definition and is the
   identity the rest of the system uses, so it is the fallback.

**Client side: written, and compiled in session 7.** `src/hbbs_http/sync.rs` now redeems an
`enrollment-token` option once, stores the returned secret as `device-token`, clears the
enrollment token, and sends `X-Device-Token` on heartbeat, sysinfo and sysinfo_ver.

`cargo check` passes on Rust 1.82.0, the version `odv-android.yml` pins: zero errors, and
nothing at all reported in `sync.rs`. The 45 remaining warnings are pre-existing upstream
dead-code warnings.

Getting a check to run at all took four environment fixes, none of them code problems, all worth
writing down for the next machine: `openssl-sys` fell back to building OpenSSL from source until
`openssl-devel` and `perl-core` were installed; `webm-sys`'s vendored libwebm does not compile
under GCC 13+ without `CXXFLAGS="-include cstdint"`; and setting `CFLAGS` alongside it breaks
`libsodium-sys`, whose build script runs `make check` and fails when the flags it inherits drop
optimisation. `--features linux-pkg-config` avoids needing vcpkg by taking libvpx, libyuv, opus
and aom from the system.

### 3.2 Implement the heartbeat control channel

**Landed.** `rustdeskapi/handlers.go` returned hardcoded empty `disconnect` and `strategy`, so
the platform could record a revocation in its own database and the device would never hear
about it.

- `device_disconnect_requests`: a queue of connection ids per device.
  `TakePendingDisconnects` returns them and marks them delivered in one `UPDATE … RETURNING`,
  so a disconnect is delivered exactly once and a reused conn id is not dropped later.
- `device_strategies`: per-device `config_options` with a `modified_at` version. The heartbeat
  returns the strategy only when the device's echoed `modified_at` differs, which is what stops
  a 15-second poll rewriting the client's configuration every 15 seconds.
- `POST /api/v1/devices/{id}/disconnect` (with no body, ends every session the platform believes
  is open), `GET`/`PUT /api/v1/devices/{id}/strategy`, and
  `GET /api/v1/device-observations` for the ids that reported in without a credential.

The `PUT` takes an allowlist, `pushableOptions`. `custom-rendezvous-server`, `api-server`,
`relay-server` and `key` are deliberately absent: those are what 1.3 locks at the client build,
and pushing them from here would reopen that door from the inside. Both responses say
`delivered_at_heartbeat: true` rather than implying the action already happened.

### 3.3 Platform-managed per-device connection credentials

**Landed, all four halves: server, device, portal and the automatic rotation.** This is the item
the product's stated value rests on. Before it, "this technician may no longer reach that
machine" was a statement about a row in our database: the device decided for itself, on a
password the platform never knew, and whoever had been given that password kept it.

New migration `000007_device_passwords`, one row per device: AES-256-GCM ciphertext, its nonce, a
`version` counter, and `applied_version`/`applied_at` for what the device has confirmed. No
history table — a superseded password is worth nothing and keeping it would mean keeping every
credential a technician was ever shown. The audit trail keeps the part that matters.

New `internal/devicepw`. `ParseKey`, `Generate`, `Rotate`, `Reveal`, `Status`, `Pending`,
`MarkApplied`, `RotateMany`. Four decisions in it worth naming:

- **A counter, not a timestamp, for the version.** Two rotations inside one second are an
  ordinary consequence of a script, and with a timestamp the second would look to the device
  exactly like the first. `device_strategies.modified_at` gets away with seconds because a human
  edits it; a rotation sweep does not.
- **`applied_version` is not cleared on rotation.** It keeps pointing at the password the device
  is actually using, which is what makes "rotated but not landed yet" distinguishable from "this
  device has never reported". Clearing it would erase the one fact the portal needs to be honest.
- **The heartbeat echo is the acknowledgement.** There is no ack endpoint: an ack sent as a second
  request is an ack a dropped connection loses, and the next heartbeat carries the same
  information anyway. So the password keeps being offered until the device echoes the version,
  and a lost response costs 15 seconds rather than the password.
- **`Pending` creates a row for a device that has none.** Devices enrolled before this existed
  would otherwise never receive a password, and the fleet would split into managed and unmanaged
  with nothing saying which.

`DEVICE_PASSWORD_KEY` is **required**, checked in `ValidateAPI` rather than at first use. A key
that does not decode would otherwise surface as a failed enrollment weeks later, at a customer
site, which is the worst possible moment. `ParseKey` accepts all four base64 spellings because
`openssl rand -base64 32` and a password manager produce different ones and rejecting either
looks like a corrupt key rather than a picky decoder.

**Two endpoints, deliberately at different privilege levels.** `GET …/password` is a technician's
and goes through the same `CanAccessDevice` every other device route uses, with a
`device.password_revealed` audit event naming them and `Cache-Control: no-store` on the response.
`POST …/password/rotate` is an administrator's: rotation is how access is taken away, and a
technician about to be removed from a support group should not be able to perform it on the way
out.

**Automatic rotation on withdrawal is what closes the loop.** Removing a technician from a support
group rotates every device that group reached; revoking a device group from a support group
rotates that group's devices only, because the technicians keep legitimate access to everything
else and rotating those would be churn with no security value. Both read the affected set
*before* the delete, so the answer is the set the departing party could reach. Neither fails the
request that triggered it: the access change has already committed and is what the operator asked
for, so a rotation that could not be written is reported in the response and the audit trail
rather than rolling back a revocation.

**Enrollment now seeds a password policy**, and without it the whole feature would be theatre.
`verification-method` defaults to accepting a temporary password the device generates for itself,
which nobody can revoke centrally, and `approve-mode` defaults to accepting a click at the
device, which for an unattended machine at a customer site means either nobody can connect or
anybody standing at it can let somebody in. Enrollment seeds `use-permanent-password` and
`approve-mode: password` with `ON CONFLICT DO NOTHING`, so an administrator who has changed either
through `PUT /strategy` keeps their change when the device re-enrolls.

**Client half**, `src/hbbs_http/sync.rs`. The heartbeat carries `password_version` from
`LocalConfig`; a `device_password` in the response is applied with
`Config::set_permanent_password` and the version recorded **only if that returns true**. That
ordering is the whole safety property: recording the version after a failed write would tell the
server the rotation had landed while the machine still accepted the old password. Note this
cannot go through 3.2's strategy channel — the permanent password is separate storage with its
own hashing and encryption, and putting a password into the options map would store something the
device never checks. `set_permanent_password` is already `pub`, so no `hbb_common` patch was
needed and upstream merges stay cheap.

**Portal half**, `DeviceDetail.tsx`. Nothing is fetched until somebody presses the button, because
reading is audited and loading it with the page would record a reveal for every navigation. An
unconfirmed rotation renders an amber banner naming the version the device is still on, rather
than showing the new password and letting an operator assume it is in force.

**Verified by measurement.** `go build`, `go vet`, `gofmt`, and the full suite with `ODV_TEST_DB=1`
against a real PostgreSQL (integration 21.6 s, migrations 9.3 s — both real runs, not skips).
Seven new integration tests in `internal/integration/password_test.go` covering delivery,
repeat-until-confirmed, the acknowledgement, rotation reaching the device, reveal being audited,
technician-may-read-but-not-rotate, both automatic rotations, and the seeded policy. Nine unit
tests in `internal/devicepw`, including that two seals of the same password differ and that `Open`
refuses a modified ciphertext. Two IDOR cases added, and `device_passwords` added to the
change-nothing snapshot so a refused request that still wrote a row would fail. `cargo check
--features linux-pkg-config` on Rust 1.82.0: zero errors, and the check was proved to be running
rather than cached by a negative control that failed exactly as predicted. The portal is at 33
tests with `npm run lint` clean and `npm run build` passing.

**Not verified, and it is the same gap 3.1 has:** no real RustDesk client has run this. The Rust
compiles and the contract matches the server's on both sides, but nothing here has watched an
Android device apply a pushed password.

### 3.4 Document the residual limitation honestly

**Landed** in `platform/README.md`, under "Where authorization is enforced, and where it is
not". It states plainly that the relay does not check authorization and never will while hbbs
and hbbr stay upstream; that revocation arrives at the next heartbeat, within about a minute,
and only if the device is online; that a technician holding a current device password keeps
access until 3.3 exists; and that the deployment key is a network perimeter rather than a device
identity.

Item 5.3 should link to that section from the root README.

### 3.5 Make the audit log tamper-evident

**Half landed.** Migration `000006_append_only_audit` adds a trigger that refuses `UPDATE` and
`DELETE` on `audit_events`, with one exemption: a transaction that has done
`SET LOCAL odv.audit_retention = 'on'`. The retention worker is the only caller that sets it,
and `SET LOCAL` means the exemption dies with the transaction rather than lingering on a pooled
connection.

The plan said to revoke `UPDATE`/`DELETE` from the application's role. That does not work as
written: the API connects as `POSTGRES_USER`, which owns the tables, and an owner is not
restricted by its own grants. Doing it properly needs a second, non-owning runtime role with the
owner reserved for migrations, which is a deployment change (two credential sets in compose, and
a failure mode where the wrong one is used). **That is the remaining half of 3.5.**

**Stated limits of the trigger.** An owner can disable it, and `TRUNCATE` does not fire
row-level triggers at all. Both are single deliberate DDL-level acts, visible in a schema
review, rather than a stray `UPDATE`. Neither is prevented until the non-owning role exists.

**A consequence worth knowing about.** `audit_events` had four foreign keys declared
`ON DELETE SET NULL`, so deleting a customer issued an `UPDATE` against every audit row that
referenced it — which the trigger then refused, and the delete failed with it. This surfaced as
`TestCustomerCRUD` returning 500. Loosening the trigger to allow "only the foreign keys changed"
would have left the hole exactly where it matters, since nulling `user_id` is how you erase who
did something. So the constraints were dropped instead: an audit record is a statement about the
past and has to outlive the rows it refers to. The columns keep the ids, the `apiv1` joins are
already `LEFT JOIN`s and return a null name for a deleted entity, and the cost is that nothing at
the database level checks those ids on insert.

Second consequence: `audit_events` is no longer reachable by `TRUNCATE … CASCADE` from `users`
or `devices`, so the integration fixture lists it explicitly. Without that, audit rows leak
between tests.

### Phase 3 verification, actually run

The full device lifecycle, against the running stack (project `odvphase3`, Caddy on
18080/18443), driven with curl exactly as a device would:

| Check | Result |
|---|---|
| Migrations | applied on startup, schema at version 6 |
| `POST /api/v1/enrollment-tokens` as an administrator | 201 with the plaintext token, `max_uses: 2`, `expires_at: null` |
| `POST /api/heartbeat` before enrolling | 401, no device row created |
| `POST /api/enroll` | 200 with `device_token` and `device_id`; device `ACTIVE`, named from the hostname, in the token's customer and device group |
| `POST /api/heartbeat` with `X-Device-Token` | 200, device `ONLINE` |
| An id that never enrolls | recorded in `device_observations` with `sightings: 2` and the client address, and listed by `GET /api/v1/device-observations` |
| `POST /api/v1/devices/{id}/disconnect` with `conn_ids: [11,12]` | `{"queued":2,"delivered_at_heartbeat":true}` |
| Next heartbeat | `{"disconnect":[11,12],"modified_at":…,"strategy":{"config_options":{"enable-file-transfer":"N"}}}` |
| Heartbeat after that, echoing the applied `modified_at` | `{"disconnect":[],"modified_at":…}` — delivered exactly once, strategy withheld |
| `PUT …/strategy` with `api-server` | 400, "server identity keys are locked at the client build" |
| `POST /api/sysinfo` with the credential | `SYSINFO_UPDATED` |
| The credential presented with another id | 401 |
| Audit trail | `enrollment_token.issued` and `device.enrolled` both recorded |
| `go build`, `go vet`, `go test ./...` with `ODV_TEST_DB=1` | pass |

**Not verified:** anything involving a real RustDesk client, because the client change cannot be
compiled here.

---

## Phase 4: MEDIUM

**Testing.** Coverage is broader than expected (~100 Go test functions plus 24 portal tests),
with integration tests behind `ODV_TEST_DB=1`. The gaps are precisely where the bugs are.

| # | Status | Test to add |
|---|---|---|
| 4.1 | DONE | `identity.Authenticate` rejects a wrong password; session tokens unpredictable and unique across rapid logins (1.1, 1.2) |
| 4.2 | DONE | CORS emits no headers when unconfigured (1.4) |
| 4.3 | DONE | `/api/heartbeat` refuses an unenrolled device and does not register unknown IDs (1.5). `TestEnrollThenHeartbeat`, `TestHeartbeatRefusesCredentialForAnotherID`, `TestSysinfoRequiresACredential` |
| 4.4 | DONE | Enrollment token redemption, expiry, max-uses, revocation, double-redemption (1.6). `TestEnrollmentTokenLimits`, `TestReenrollmentRotatesTheCredential`, `TestRevokedCredentialStopsHeartbeat` |
| 4.5 | DONE | `/api/audit/conn` refuses a forged `user_id` and an unauthorized `device_id` (2.1). Seven tests in `internal/integration/audit_conn_test.go`, plus `redactQuery` and rate-limit tests in `internal/httpx` |
| 4.6 | DONE | One IDOR test per `apiv1` resource: technician outside the support group gets 403. `internal/integration/idor_test.go` |
| 4.7 | DONE | Heartbeat control channel, both halves. The device side was already covered; `internal/integration/control_test.go` adds the portal side |
| 4.8 | DONE | Migration up/down round-trip. `internal/migrations/roundtrip_test.go` |

**Correctness and quality.**

| # | Status | Item |
|---|---|---|
| 4.9 | DONE | `enrollment.GetDeviceEnrollmentInfo` selected the first device group globally, ignoring `customer_id`. Fixed alongside 3.1: the group is now reached through the devices that belong to that customer |
| 4.10 | DONE | `identity.CreateSession` 500'd on a `last_login` update failure after the session row was committed. Fixed alongside 1.2, since it is the same function |
| 4.11 | DONE | `AuthService` duplicated the select-user-then-load-roles block four times. One `userSelect` and one `loadUser` now |
| 4.12 | DONE | The dropped-audit-event count is published on `/healthz`. It was already counted; nothing read it |
| 4.13 | DONE | One `fleet.Service`, injected into `telemetry` and `enrollment` rather than built three times |
| 4.14 | DONE | Corrected: the startup context never reached the migrations. Scoped it to the pool anyway and said why migrations have no deadline |
| 4.15 | DONE | `httpx.TimeoutMiddleware(10s)` on the base router |
| 4.16 | DONE | Unused portal dependencies removed. Seven, not six: `date-fns` was unused too |
| 4.17 | DONE | `src/components/ErrorBoundary.tsx`, mounted at the application root and around the routed page |
| 4.18 | DONE | `fleet.Device` scanned nullable columns into non-pointer fields, so no device could be read back after registration. See 2.9 |
| 4.19 | DONE | Found while verifying 4.16: `npm run lint` has never worked. There is no ESLint config file at all |

### What landed for 4.6, 4.7 and 4.8

**4.6, `internal/integration/idor_test.go`.** One case per `/api/v1` route, driven as a
technician against a resource belonging to another support group. Three tests, and the value is
in the second and third rather than the first:

- `TestTechnicianIsRefusedEveryForeignResource` is the sweep itself, 47 cases.
- `TestIDORSweepCoversEveryRoute` compares the case table against `apiv1.Handler.Routes()` in
  both directions. Adding a route without an IDOR case fails the build. Without this half, the
  sweep only ever covers the routes that existed the day it was written.
- `TestForeignRequestsChangeNothing` runs the whole sweep and then checks twelve tables have not
  moved, plus the two named escalations: the technician did not join the foreign support group,
  and did not grant themselves `Administrator`. A 403 that still writes is not a refusal.
- `TestScopedEndpointsAnswerDifferentlyPerCaller` covers the three routes that answer 200 to
  everyone and filter instead. Asking as `tech1` and as `tech2` must produce different bodies:
  an identical body means the caller is not being consulted, which is the exact shape of the bug
  this surface used to have when the admin check was handed a hardcoded user id of 1.

**4.7, `internal/integration/control_test.go`.** The device half was already covered. This adds
the portal half plus the join between them: `TestPortalDisconnectReachesTheDevice` enrolls a
device, queues a disconnect through `/api/v1`, and collects it from `/api/heartbeat` in one test,
because each half passing separately does not prove they agree. Also covered: an empty body ends
every open session and skips those already `ENDED`; all four server-identity keys are refused
and a map containing one is refused whole rather than half-applied; `GET` is a technician's and
`PUT` is an administrator's, while `disconnect` is deliberately the other way round.

**4.8, `internal/migrations/roundtrip_test.go`.** Four tests. Two need no database, so they run
in CI as it stands: file pairing and contiguity (golang-migrate silently ignores a file whose
name it cannot parse, so a typo is a migration that never runs), and a textual check that each
down file at least mentions every table, type, function and trigger its up file creates.

The two that need a database get their own scratch database, created and dropped per run. This
matters: `internal/migrations` and `internal/integration` are separate packages and so run
concurrently under `go test ./...`, and dropping every table mid-run would have surfaced as a
hundred unrelated failures.

- `TestMigrationsRoundTrip`: up, full down, up again. The second up is the point. A down file
  that leaves an enum type behind lets the first up pass and the second fail, and the only
  deployment that ever runs a second up is one that has just rolled back.
- `TestEachMigrationIsIndividuallyReversible`: climbs one version at a time recording a schema
  fingerprint at each, then descends comparing. Rolling back one release is the case that
  actually happens; rolling back to an empty database is not.

The fingerprint covers columns and types, constraints **including `convalidated`**, indexes,
enum labels, triggers and functions. `convalidated` is there on purpose: it is exactly what
000006's `NOT VALID` rollback changes, and a fingerprint without it would have reported a clean
round trip over the one difference this schema is known to have.

That difference is carried in a `knownRollbackDifferences` allowance list, one entry per
constraint, each with the reason in a comment. Verified to be load-bearing rather than
decorative: emptying the list fails the test with eight lines naming the four constraints
changing from `validated=t` to `validated=f`. A difference that is not on the list fails; a
difference that is on it had to be written down and argued for first.

### What landed for 4.11 to 4.15

**4.11.** `GetUserByEmail`, `GetUserByID`, `GetUserByKeycloakSubject` and `GetSessionUser` each
carried their own copy of the column list, the scan and the follow-up queries. They are now one
`userSelect` const and one `loadUser`, which takes a trailing clause so the session lookup's
`JOIN client_sessions` fits the same shape.

The copies had already drifted, and that is the finding rather than the line count:
`GetSessionUser` attached no support groups, so the same account was a different object
depending on which door it came through. Nothing reads `identity.User.SupportGroups` today, so
this was latent rather than live, which is exactly how it survived. Support groups now come back
as an array from the same round trip rather than a second query, which is what makes attaching
them unconditional instead of a judgement call at each call site.

Covered by `TestEveryUserLookupReturnsTheSameUser`, which asserts all four lookups return the
same roles and the same support groups, plus a user in no group reading as an empty list rather
than failing to scan.

Also fixed in passing: `getUserRoles` never checked `rows.Err()`. A connection that dies
mid-read ended the loop with no error, so a user would have been reported holding fewer roles
than they hold, which for a role check fails in the permissive direction on the way back out.

**4.12.** `audit.Service` already counted dropped events and already had a `Dropped()` method.
It had no callers, so the count never left the process, and the audit trail could develop holes
that only a log line nobody reads would mention. `/healthz` now returns
`{"status":"ok","audit_events_dropped":N}`.

It stays 200 with a non-zero count deliberately: a hole in the audit trail is a reason to page
somebody, not a reason to take the container out of rotation, and a restart loses the count
while fixing nothing. No metrics dependency was added for one counter; `go.mod` has no
Prometheus client and adding one for this would not be proportionate.

**4.13.** `fleet.Service` was constructed in `main.go`, in `enrollment.NewService` and in
`telemetry.NewService`. Both service constructors now take it as an argument. It is harmless
today because `fleet.Service` is a pool and a config with no state of its own; the point is that
the first field it caches would otherwise exist in three unsynchronised copies.

**4.14. The defect as described does not exist.** The audit says the 10-second startup context
also covers migrations. It does not: `migrations.Run` takes a DSN and opens its own connection,
so the context was never passed to it. What was true is that the context stayed in scope for the
whole of `main` behind a `defer cancel()`, which is a ten-second deadline hanging over nothing
in particular. It is now created immediately before `postgres.New` and cancelled immediately
after, and there is a comment on the migration block saying why it deliberately has no deadline:
golang-migrate takes a session advisory lock, so a second replica starting at the same time
waits there, and a timeout would turn "another replica is migrating" into a crash loop.

**4.15.** `httpx.TimeoutMiddleware(10 * time.Second)` on the base router, so it covers routes
added later for the same reason the body cap does.

Worth stating why this was not already covered: the server's `ReadTimeout` and `WriteTimeout`
are socket deadlines and neither cancels the request context. Handlers pass `r.Context()`
straight into pgx, so before this a query blocked on a lock held a pool connection until the
client went away. `MaxConns` is 20, so twenty such requests is the whole pool, and the symptom
is an API that passes its health check and answers nothing. The deadline is below the server's
15-second `WriteTimeout` on purpose, so the handler loses the race to its own deadline and gets
to write an error rather than having the socket closed underneath it.

Both are covered in `internal/httpx/router_test.go`, including a nil `DropCounter`, which must
not panic the one endpoint that has to answer.

### What landed for 4.16 and 4.17

**4.16.** Seven dependencies removed, not the six the audit listed: `axios`, `zustand`, `zod`,
`@tanstack/react-table`, `recharts`, `@types/react-router-dom` and `date-fns`. Each was checked
by grepping the whole of `src` for the package name rather than only for an import statement.
`@types/react-router-dom` was also wrong on its own terms: it types react-router v5, and the
portal is on v6, which ships its own types.

What is genuinely used is short: `@tanstack/react-query`, `react-router-dom`, `react-oidc-context`,
`oidc-client-ts`, `react` and `react-dom`. `package-lock.json` regenerated; `npm run build` and
`npm test` pass.

**4.17.** `src/components/ErrorBoundary.tsx`, mounted twice and deliberately so.

Without one, React 18 unmounts the whole tree when any component throws, and the operator gets a
blank white page: no message, no navigation, and nothing to distinguish a component bug from a
failed deployment or a dead network.

- Inside `Layout`, around the routed `<Outlet />`. A page that throws leaves the header and the
  navigation standing, so the operator can go somewhere else instead of being stranded.
  It is keyed on `location.pathname`, so navigating away clears it; without the key it would
  stay in its error state after the user had moved on, because nothing would remount it.
- At the application root in `App`, as the backstop for the OIDC provider, `RequireAuth` and the
  layout chrome itself.

It shows the error message and two buttons, Try again and Reload. The component stack goes to
the console, not the screen. Six tests in `ErrorBoundary.test.tsx`, including that Try again
recovers when the cause has gone and catches again when it has not.

Deliberately not caught, and said so in the file so nobody plans around it wrongly: event
handler errors, async callbacks, and React Query failures. The last of those is not an
oversight; those are data errors with a status code, and `ErrorNotice` already renders them
keeping the distinction between "you may not see this" and "the server is down".

### 4.19 The portal's lint script has never worked

Found while checking which dependencies 4.16 could remove. `npm run lint` fails immediately with
"ESLint couldn't find a configuration file": there is no `.eslintrc*` and no `eslint.config.js`
anywhere in `platform/web`.

So five devDependencies (`eslint`, both `@typescript-eslint` packages, `eslint-plugin-react-hooks`,
`eslint-plugin-react-refresh`) back a script that cannot run. CI does not call it, which is why
this has never surfaced: `.github/workflows/odv-platform.yml` runs `npm ci`, `npm run build` and
`npm test` only.

**Landed.** The config was added rather than the script dropped, and it was wired into CI in the
same change, because a lint that nothing calls is how this got here.

`platform/web/.eslintrc.cjs`, the `.eslintrc` form because the pinned `eslint` is 8.57. ESLint 9
reads only flat config and would ignore the file entirely, so moving that pin later is a rewrite
rather than an edit; that is stated at the top of the file.

Type-aware rules are on: `plugin:@typescript-eslint/recommended-requiring-type-checking`, which
costs a TypeScript program build per run and in exchange can see across files. That is the class
of rule worth the cost here — `no-floating-promises` and `no-misused-promises` are easy to write
in a React codebase full of awaited fetch wrappers, and no untyped lint can find them.

The first run found seven problems, all real, and each was fixed rather than silenced:

- **`src/lib/api.ts`, five errors, all one defect.** `response.json()` returns `Promise<any>`, and
  that `any` spread through the whole client: `body?.error` was an unchecked member access on an
  unknown shape, and `return response.json()` handed back an unvalidated `any` as `T` with nothing
  saying so. The error path now narrows `unknown` properly before reading `.error`, and the
  success path casts explicitly with a comment naming it as the one unchecked assumption in the
  file. Behaviour is unchanged; what changed is that the assumption is now visible and has an
  obvious place for runtime validation to go later.
- **`src/components/ui.tsx` and `src/App.tsx`, two `react-refresh/only-export-components`
  warnings.** `formatDate` moved to a new `src/lib/format.ts` and the seven pages that imported it
  now import from there; `queryClient` simply stopped being exported, since nothing outside
  `App.tsx` read it. Both are real: a single non-component export in the shared UI module costs a
  full page reload on every edit for every page that imports from it.

**The test-file exemption was removed after being measured.** The config first carried an override
turning the type-unsafe rules off for `*.test.tsx` and `src/test/**`, on the assumption that
Testing Library's loosely typed helpers would trip them. Deleting the override and re-running
produced zero errors, so the exemption was doing nothing. It is gone, and the reason is written in
the file: a relaxation that is not doing work is indistinguishable from one hiding something. This
is the same check that `knownRollbackDifferences` got in 4.8.

Wired into `.github/workflows/odv-platform.yml` as a `Lint` step between `npm ci` and
`npm run build`, so the config cannot rot the way the script did.

Verified: `npm run lint` exits 0 over 26 files (the test files included, confirmed from the JSON
reporter rather than assumed), `npm run build` passes, and the portal is still at 30 tests.

---

## Phase 5: LOW, repository cleanup

The fork's own footprint is small and clean: `platform/`, four `odv-*` CI workflows, two CI
scripts, one 18-line addition to `flutter/lib/main.dart`, and `plans/`. Everything else is
inherited upstream RustDesk and should be left alone.

| # | Status | Item |
|---|---|---|
| 5.1 | DONE | Removed the three superseded plan files; this file is now the only one in `plans/`. The audit named `check-client-config.sh` as a citer and it is not one: the two real references were `odv-platform.yml` and `client-api-sweep.sh`, and both now carry the rationale inline instead of pointing at a document |
| 5.2 | DONE | The sweep baseline moved to `.github/client-api-surface.txt` and is now tracked. It was never a build artifact: it is a hand-reviewed baseline, and it was simply never `git add`ed, so CI's `--check` step would have failed on every run for want of a file to compare against |
| 5.3 | DONE | A fork header now sits above the upstream README, pointing at `platform/` and linking the authorization-limits section as 3.4 asked. The upstream text below it is untouched and labelled as such, so merges stay cheap |
| 5.4 | DONE | Pinned to `govulncheck@v1.7.0`. Running it found eight live vulnerabilities, which is 5.7 |
| 5.5 | DONE | `FUNDING.yml` removed (decided with the user). `dependabot.yml` covered git submodules only, so neither the Go API nor the portal had any update path; both added, plus `github-actions` |
| 5.6 | DONE | `KC_DB_PASSWORD: keycloak_password` is hardcoded at `docker-compose.yml:34` rather than templated. Closed by 0.6 |

Found while doing Phase 5, and folded into it:

| # | Status | Item |
|---|---|---|
| 5.7 | DONE | `govulncheck` had never been run. Eight vulnerabilities, all reachable: a SQL injection in `pgx` and seven in the Go standard library |
| 5.8 | DONE | All four `odv-*` workflows trigger on `branches: [main]`, and this repository's default branch is `master`. None of them has ever run |

**Keep.** `AGENTS.md` and `GEMINI.md` at the repository root are upstream RustDesk files, not
fork artifacts. The `Copilot` commit authors are upstream contributors. Rewriting shared history
would complicate future upstream merges.

**Removed in session 8:** the root `CLAUDE.md`, which upstream added in `0cf3e8ed4` as a
one-line pointer to `AGENTS.md`. It carries no content that `AGENTS.md` does not, and a fork
presenting itself as OpenDeskViewer has no reason to ship a vendor-named file. The cost is a
one-line conflict on the next upstream merge that touches it, which is the same trade `FUNDING.yml`
took in 5.5.

**No committed secrets found.** No `.env`, key material, dump, log or build output is tracked.

### 5.1 and 5.2, and one correction to each

`plans/` now holds this file alone. The three removed plans are in git history, so nothing is
lost — with one exception that had to be rescued first: `finish-line.md` carried 119 lines of
**uncommitted** revisions, which deletion would have destroyed. The durable part of them is now
in the Context section above, under "Two commit messages in this repository are not evidence".
Everything else in those revisions was a status report on work that Phases 0 to 4 have since
redone and verified.

**The audit named the wrong second citer.** It said `.github/workflows/odv-platform.yml` and
`.github/scripts/check-client-config.sh` referenced `finish-line.md`. `check-client-config.sh`
does not mention it; the second citer is `client-api-sweep.sh`. Both real citers now carry the
rationale inline. That is the better outcome anyway: an explanation that lives next to the
mechanism cannot be orphaned by deleting a document.

**5.2 was not what it said either.** `docs/client-api-surface.txt` is not a build artifact — it
is a hand-reviewed baseline of the 33 API paths the client calls, and the whole point is that a
human decides when it changes. It was simply never `git add`ed. The consequence is worse than
untidiness: CI's `Client API surface has not drifted` step would have failed on every run,
because the file it diffs against was not in the checkout. It is now tracked, at
`.github/client-api-surface.txt` rather than `docs/`, because `docs/` is upstream's own tree of
translated READMEs and contributing guides and a fork artifact there is one more thing to
reconcile on each merge.

Verified: `--check` passes, `--write` is idempotent against the moved file, and the missing
baseline case still gives the actionable error rather than passing silently.

### 5.7 The vulnerability scan had never been run

Pinning `govulncheck` (5.4) meant running it, and it turns out CI's `Vulnerability scan` step
would have failed on every commit — except that 5.8 means it never got to.

Eight vulnerabilities, every one of them reported as reachable from this module's own code
rather than merely present in the dependency graph.

**The one that matters: `GO-2026-5004`, SQL injection in `github.com/jackc/pgx/v5`.** Placeholder
confusion with dollar-quoted string literals, present in v5.7.0, fixed in v5.9.2. govulncheck
traced it to `apiv1.Handler.HandleDeviceSessions` calling `pgxpool.Pool.Query`. Upgraded to
v5.9.2 (`puddle` came along to v2.2.2). This is worth sitting with: Phases 1 to 4 audited the
fork's own SQL carefully, and the injection was underneath all of it in the driver.

The remaining seven are Go standard library — `net/url`, `net/http` (three), `crypto/tls` (two),
`encoding/xml`, `encoding/asn1` — reached through `http.Server.ListenAndServe`, the JWKS client
and `x509.ParsePKIXPublicKey`. Those are a toolchain problem, not a code problem, so the fix is
a toolchain bump in the two places that build a shipped binary:

- `platform/api/go.mod` gains `toolchain go1.26.6`, the first release carrying all seven fixes.
  The `go 1.25.0` language line is deliberately left alone; this is a floor on the builder, not
  a language bump. CI picks it up through `setup-go`'s `go-version-file`.
- `platform/api/Dockerfile` and `Dockerfile.worker` move from `golang:1.25-alpine` to
  `golang:1.26-alpine`, so the containers that actually run in production are built on the
  patched standard library rather than only CI being clean.

Verified by measurement at each step: pgx alone took it from 8 to 7, the toolchain took it from
7 to **`No vulnerabilities found`**, and `go build`, `go vet` and the full suite including the
integration tests against a real PostgreSQL pass on go1.26.6 with pgx v5.9.2. The integration
package ran for 18.5 s rather than skipping, which is the check that it actually exercised the
database — a pgx minor-version bump verified only by a suite that silently skipped would be no
verification at all.

### 5.8 None of the fork's CI workflows has ever run

Not in the audit. Found while reading `dependabot.yml`'s `target-branch: "master"` and noticing
it disagreed with the workflows next to it.

All four `odv-*` workflows declare `branches: [main]` for both `push` and `pull_request`. This
repository's default branch is `master` (`origin/HEAD` → `origin/master`). GitHub does not warn
about a branch filter that matches nothing; the workflow simply never fires, and the Actions tab
looks quiet rather than broken.

This explains a great deal. Phase 0.1 called `docker compose config` "already a CI step that has
never run", and the reason was assumed to be that the file did not parse. The real reason is
larger: **no `odv-*` job has ever executed on any commit.** Every CI guard this project has
written — the compose parse, the client configuration check, the API surface sweep, the Go
suite, the vulnerability scan, and the lint step added in 4.19 — was inert. 5.7 is the direct
consequence: a scan that cannot fire cannot report.

All four now use `branches: [master, main]`. Both are listed rather than just `master` so that
renaming the default branch later does not silently switch CI back off, which is the same
failure in the other direction.

### 5.5, and why dependabot mattered more than FUNDING.yml

`FUNDING.yml` was removed. It pointed GitHub's Sponsor button at `rustdesk` and `ko_fi/rustdesk`,
which on a repository presenting itself as OpenDeskViewer solicits money for a project that does
not maintain this code. Decided with the user rather than assumed.

`dependabot.yml` was the substantive half. It tracked `gitsubmodule` and nothing else, so the
fork's entire dependency surface — the Go API and the React portal — had no update path at all.
That is not a hypothetical: it is why pgx sat at v5.7.0 with a reachable SQL injection until
5.7 found it by hand. Added `gomod` for `/platform/api`, `npm` for `/platform/web`, and
`github-actions` for the workflows, all on `target-branch: master` to match 5.8, grouped so a
routine patch sweep is one pull request while security updates still arrive on their own.

Note the shape of this one: the inherited config was not merely pointing at upstream, it was
pointing at a branch that exists (`master`) while the workflows beside it pointed at one that
does not. Reading the two files together is what surfaced 5.8.

---

## Phase 6: close the leftovers, then build what is missing

Scope agreed with the user in session 7: **the known leftovers and the missing enterprise
features.** Phases 0 to 5 were remediation of things that existed and were wrong. Most of Phase
6 is construction of things that do not exist, which is a different kind of work and should not
inherit the assumption that an audit item is a small fix.

Ordered so that each part is useful on its own if the phase is interrupted.

### 6A. The leftovers (finish the remediation first)

| # | Status | Item |
|---|---|---|
| 6.1 | DONE | **3.3**, per-device connection credentials. See 3.3's section for what landed and how it was verified |
| 6.2 | DONE | **3.5 second half**, the non-owning database role. See below |
| 6.3 | DONE | **2.3**, the OIDC broker. Implemented, and the decision it was waiting on turned out not to need making. See below |
| 6.4 | DONE | The client build is wired to the deployment lock, and the guard now checks the built artifact rather than the environment. See below |

### 6.2 The non-owning database role

**Landed.** Migration `000008_runtime_role` creates `odv_app` and grants it what the API needs at
request time: `SELECT`, `INSERT`, `UPDATE`, `DELETE` on the tables, `USAGE`/`SELECT` on the
sequences, and `ALTER DEFAULT PRIVILEGES` so a table added by a later migration is covered without
anyone remembering. Then it takes three things away.

- `UPDATE`, `DELETE` and `TRUNCATE` on `audit_events`. The trigger from 000006 is now the second
  line rather than the only one: the grant refuses first, and an attacker who found a way to set
  `odv.audit_retention` would still be stopped by the privilege system.
- `TRUNCATE` everywhere. It bypasses row triggers *and* foreign keys, and nothing the API does
  needs it.
- Everything on `schema_migrations`. An API that could write there could convince the next
  deployment that a migration it never ran had already been applied.

`odv_app` owns nothing, so it cannot `ALTER TABLE … DISABLE TRIGGER`, which is the hole 3.5 said
only a non-owning role could close.

**Three decisions worth stating.**

- **The role name is a constant, not a setting.** The migration has to name it in plain SQL, so a
  configurable name would produce a role that can log in and read nothing — a failure at the first
  query rather than at deployment.
- **The migration creates the role if the init script has not.** The privilege model then lives in
  one place and is testable in a scratch database, while
  `platform/postgres/init/20-application-role.sh` does the one thing a migration cannot, which is
  set a password. A database whose owner has no `CREATEROLE` raises a notice and leaves the grants
  unapplied, which is visible in the migration output rather than silent.
- **Order changed in `cmd/api/main.go`: migrations now run before the pool opens.** This is
  load-bearing rather than tidiness. The pool connects as `odv_app`, and it is 000008 that grants
  `odv_app` the right to connect at all; opening the pool first appears to work, because pgxpool
  connects lazily, and then fails on the first request of a first deployment.

**The residual, stated in `platform/README.md` rather than hidden.** The worker still connects as
the owner, because expiring audit rows past the retention period is a delete and `odv_app` may not
do one. The worker listens on no port and reads no user input, which is what makes that an
acceptable split; it is still a second process that could remove evidence if it were compromised.

Leaving `POSTGRES_APP_PASSWORD` empty falls back to connecting as the owner and warns at startup.
That is the pre-000008 behaviour, kept so an in-place upgrade does not fail to come back up.

**Verified.** Four tests in `internal/integration/runtime_role_test.go`, and they discriminate
rather than assert vacuously: the same `has_table_privilege` query returns true for `UPDATE` on
`devices` and false for `UPDATE` on `audit_events` in the same run, and the role is separately
checked not to be a superuser and not to own a table, either of which would make every refusal
meaningless. Proved load-bearing: granting `UPDATE ON audit_events` to `odv_app` by hand fails
`TestRuntimeRoleCanOnlyAppendToTheAuditLog` with the expected message.

### 6.3 The OIDC broker, and the decision that did not need making

**Landed as an implementation.** Item 2.3 left this at 501 pending a choice between reading
Keycloak's generated `odv-api` secret into `OIDC_CLIENT_SECRET` at deploy time and templating the
realm file. **Neither is necessary.** The exchange runs as `odv-portal`, which the realm already
declares a public client with `pkce.code.challenge.method: S256`; what proves the exchange belongs
to the party that started the flow is the code verifier, not a shared secret. Nothing is read out
of Keycloak, nothing is templated, and `OIDC_CLIENT_SECRET` is now documented as unread.

**Reading `src/hbbs_http/account.rs` is what made this implementable**, and it corrected two
things the plan had recorded wrongly:

1. **`/api/oidc/auth` is not a redirect.** The client `POST`s `{op, id, uuid, deviceInfo}` and
   parses the body for `{code, url}` (`account.rs:25-28, 174-177`). The old handler answered 302,
   which arrived as a parse failure, so the sign-in button did nothing an operator could interpret.
2. **`/api/oidc/auth-query` is a `GET` with query parameters.** Item 2.3 recorded `openapi.yaml` as
   wrong about that and "corrected" the spec to `POST` to match a handler that only accepted
   `POST`. `account.rs:181-195` builds a URL with `code`, `id` and `uuid` and issues a `GET`, so the
   handler was what was wrong and the spec was right. That is the fifth audit-era claim corrected by
   checking the source first.

**What it does.** `POST /api/oidc/auth` records a request and returns a polling handle plus a
Keycloak authorization URL carrying `state` and a PKCE S256 challenge. The browser completes at
Keycloak and lands on the new `GET /api/oidc/callback`, which matches the state against a request
this server started, exchanges the code with the verifier, validates the returned token through the
same `auth.JWTValidator` every other route uses, resolves or provisions the user, and renders a
page telling them to go back to the application. `GET /api/oidc/auth-query` then mints a session
and hands it over, once.

**Four things in migration `000009_oidc_auth_requests` worth naming.**

- The polling handle and the state are stored as SHA-256 hashes, like every other credential here,
  so a database reader cannot collect a sign-in in progress.
- The **session token is never stored**. The callback records only which user authenticated; the
  poll mints the session when it collects, so the plaintext exists in exactly one response.
- The claim is one `UPDATE … WHERE … AND user_id IS NOT NULL RETURNING`. Claiming
  unconditionally would need releasing again on the pending path, and a poll that died between the
  two would strand the sign-in.
- Pending, unknown, expired and already-collected all answer `200` with
  `{"error": "No authed oidc is found"}`. That string is matched verbatim by the client
  (`account.rs:325`), which keeps polling on it and treats anything else as a failure — so it is a
  contract, not a wording choice, and it is pinned by a test. It also means an unknown handle
  teaches a prober nothing.

**Verified.** Six integration tests in `internal/integration/oidc_login_test.go` drive the whole
flow against a stub token endpoint, asserting that the exchange sends a `code_verifier` and **no**
`client_secret`, that the callback page carries no token, that a forged state is refused *before*
any code is exchanged, that a token the validator rejects produces no session, that a completed
sign-in is collectable once, and that the token handed back is a session `GetSessionUser` accepts.
Four unit tests in `internal/rustdeskapi/oidc_test.go` pin the verbs, the pending string, and
PKCE S256 against the worked example in RFC 7636 appendix B rather than against itself.

**Not verified:** the browser leg. Nothing here has opened Keycloak's login page and clicked
through it, which is the same gap Phase 0 left open and is now the only unexercised part of the
sign-in path.

### 6.4 The client build, wired and checked

**Landed.** Session 7 built the mechanism and recorded that the guard around it was useless three
times over. Both halves are fixed.

`odv-android.yml`, `odv-linux.yml` and `odv-windows.yml` now set `ODV_RENDEZVOUS_SERVER`,
`ODV_RELAY_SERVER`, `ODV_API_SERVER` and `ODV_RS_PUB_KEY` at **workflow** level, because several
steps compile Rust and a step-level `env:` would cover one of them. These are the names
`src/common.rs` reads with `option_env!`; the four the old guard demanded — `RS_PUB_KEY`,
`RENDEZVOUS_SERVERS`, `API_SERVER`, `APP_NAME` — are read by nothing anywhere in the tree.

`check-client-config.sh` was rewritten around the distinction session 7 drew: **a guard that checks
its inputs exist, rather than that they took effect, is not a guard.** It now has two modes. The
first checks the four `ODV_*` names, refuses a rendezvous server pointing at `rustdesk.com`, and
refuses a plaintext `ODV_API_SERVER` — the api-server is the channel that can reprogram a client,
so shipping one over HTTP is worth failing a build over. The second, `--verify <dir>`, unpacks each
APK and greps the shared object inside it for the api-server that was supposed to be baked in.
`odv-android.yml` runs it after the build, before the upload.

It was also removed from `odv-platform.yml`, where it had been running in a job that builds no
client.

**Verified by measurement, including a bug the verification itself found.** All three input modes
were exercised: missing variables fail, an `http://` api-server fails, a complete set passes. The
`--verify` mode was run against a real 217 MB test binary compiled with
`ODV_API_SERVER=https://odv.example.com`, and against synthetic artifacts that do and do not carry
the value, and against an empty directory — which must fail rather than pass vacuously, since that
is precisely how the old guard managed to look green.

The first `--verify` run failed on a binary that **did** contain the string, twice. The cause is
worth carrying forward: `strings … | grep -q` under `set -o pipefail` makes `grep` exit at the
first match, `strings` die of `SIGPIPE`, and the pipeline report failure. It is also
non-deterministic — it depends on whether `strings` had finished writing — so a guard written this
way passes on small inputs and fails on large ones. `grep` now drains its input instead.

The first negative control was also wrong and had to be replaced: `https://attacker.example` is a
string literal in session 7's own lock test, so it is genuinely present in that binary and the
check correctly found it. **A negative control has to use a value that is actually absent**, which
is a smaller version of the same lesson as `knownRollbackDifferences` in 4.8.

### 6B. The missing features

From the Phase 3 capability table, the rows that read "Missing" or "Stub". Each needs a design
pass before an estimate; none is a small fix.

| # | Status | Item |
|---|---|---|
| 6.5 | DONE | **Monitoring.** Connectivity transitions are recorded and readable per device and fleet-wide |
| 6.6 | DONE | **Notifications.** Signed webhooks with an outbox, backoff and abandonment. The channel decision is made and recorded |
| 6.7 | DONE | **Reporting.** Three reports, CSV and JSON, over data that already existed |
| 6.8 | DECIDED, still 501 | **Session recording.** Feasibility settled: it is feasible, the client half already exists upstream, and what is missing is a storage decision that is the operator's, not ours |

### 6.5 Monitoring

**Landed.** Migration `000010` adds `device_connectivity_events`, and
`internal/monitoring` records one row per change at the moment the worker performs it. This is not
new data collection: `devices.connectivity` was already recomputed every minute, into a single
column that the next change overwrote. "How often does this site drop out" was not a question
anyone could ask, and "tell me when a machine goes down" had nothing to fire on.

Four decisions worth naming:

- **Transitions, not samples.** A sample per device per minute is a row per device per minute
  forever and answers the question worse. An operator asks when a machine went down and for how
  long, not what it was doing at 14:32. A fleet that is behaving costs no rows at all.
- **One statement per pass, `UPDATE … RETURNING` inside a CTE.** Reading the devices about to
  change and then updating them is a race: a heartbeat arriving between the two would be
  overwritten and the device reported offline while it was talking to us.
- **The offline pass runs before the stale pass.** The other order moves a long-dead device to
  STALE and then immediately to OFFLINE in the same tick, producing two events and two alerts for
  one thing happening.
- **Recovery is on the heartbeat, not the worker.** A device coming back is a device speaking, and
  routing it through the worker would make every recovery up to a minute late. It is a separate
  function because the trigger is genuinely different.

`previous_duration_seconds` is computed at write time from `last_seen_at`, so the report that
wants "down for how long" does not need a self-join to the previous row.

### 6.6 Notifications

**Landed, and the delivery-channel decision the plan left open is made: webhooks, not email.**
Email needs an SMTP server, credentials, a sender domain with SPF and DKIM and a bounce story,
none of which this deployment has and all of which have to be right before the first message is
useful. A webhook needs a URL, and an operator who wants email points one at something that
already sends email.

`notification_targets` and `notification_deliveries`, delivered by a new `NotificationWorker` on
its own loop. The parts that matter:

- **An outbox, not an HTTP call in a handler.** Posting inline makes the caller wait for somebody
  else's server, fails the operation when that server is down, and loses the notification entirely
  if the process restarts mid-flight. A row survives all three.
- **The claim and the select are one statement, with `FOR UPDATE SKIP LOCKED`.** Two worker
  replicas must not both send the same alert; without this, scaling the worker to two would double
  every notification.
- **The HTTP calls happen after the rows are read, not inside the loop over them.** Holding a pool
  connection across somebody else's network is how twenty slow receivers become an exhausted pool.
- **Abandoned deliveries are kept, not deleted.** "Which alerts never arrived" is a question
  somebody has after an incident, and a queue that discards its failures cannot answer it.
- **HMAC-SHA256 over the exact bytes sent**, as `X-ODV-Signature`, and https is required on the
  target URL: the payload names devices and customers and is one of the few things this platform
  sends off its own network.
- The secret is **never returned** by the API and never recorded in the audit event, because the
  audit log is read by the same people the secret is kept from.

### 6.7 Reporting

**Landed.** `GET /api/v1/reports/{report}` in JSON or CSV: `device-inventory`, `session-history`,
`access-review`. No new data — every one is a query over rows the platform already kept. What was
missing was a way to get any of it out in a form somebody can put in front of an auditor, which is
most of why it is kept.

One route rather than three, with the reports defined as data (query, columns, scan). Three
handlers would be three chances for the CSV and JSON paths to drift, and
`TestCSVAndJSONAgree` asserts they do not: same header, same row count, for all three.

CSV as well as JSON because the recipient of a report works in a spreadsheet, and an export that
means "here is some JSON" gets used once. Running a report writes a `report.generated` audit
event: an access review is evidence, and "who pulled a list of every machine and customer" should
be answerable.

**The access review found a real defect in its own first draft, which is the reason that test
exists.** `TestAccessReviewMatchesWhatIsEnforced` compares the report's device counts against
`access.Resolver.GetAccessibleDevices` and failed: the report said tech1 reached four devices and
the resolver granted three. The query counted `device_group_members` rows while the resolver
filters `state = 'ACTIVE'`, so a DISCOVERED device was being reported as reachable. **A review
that overstates access is the one thing a review must not do** — somebody signs off on a picture
that differs from what is enforced. Fixed by counting the joined device rather than the membership.

### 6.8 Session recording: feasible, and deliberately not built

The plan asked for feasibility to be established before anything was promised. It is established,
and the answer is not what the plan expected.

**It is feasible, and the client half already exists upstream.** The plan's concern was that the
relay is unmodified and never sees decrypted media, so recording would have to happen at an
endpoint. That is right, and it is already how RustDesk works: `src/hbbs_http/record_upload.rs`
records locally through `scrap::record` and streams the file to `POST /api/record` as
`?type=new|append|finish|remove&file=<name>` with the bytes as the body. Nothing needs writing on
the device.

**What is missing is not code. It is a storage decision, and it is the operator's:**

- a recorded session is video, so a busy fleet is gigabytes a day. It does not belong in Postgres,
  and this deployment has no object store;
- it needs a retention period, a quota and a disk-full behaviour, because getting those wrong ends
  with an API that stops serving because a volume filled;
- recordings would be the most sensitive thing this platform ever held. Playback needs its own
  authorisation and its own audit trail, both new surfaces rather than reuses.

So `/api/record` stays **501, deliberately**, and now says so in a message an operator can act on.
Accepting uploads into a directory nobody had sized would be the worse failure, because it would
leave somebody believing sessions were recorded.

**What works today, and is the right answer for most deployments:**
`enable-record-session` is already in `apiv1.pushableOptions`, so an administrator can turn
recording on fleet-wide through `PUT /api/v1/devices/{id}/strategy`. The client records locally on
the machine being controlled. That is the capability without the platform taking custody of the
media.

**The one open question for the user**, and the only one left in this plan: if server-side
recording storage is wanted, where should the files go — a Docker volume on the API host, or an
S3-compatible object store? Everything else follows from that answer.
| 6.9 | DONE | The "Read Only" role. Implemented rather than dropped. See below |

### 6.9 The Read Only role, and the role that never existed

**Implemented rather than dropped.** The role is defined in one place, at
`apiv1.RoleReadOnly`, and means exactly this: **fleet-wide read, no writes, no remote access, no
credentials.** A Read Only user sees every device, customer, group, user and audit event, and
cannot change any of them, cannot start a connection, cannot read or rotate a device password, and
cannot push configuration. It is the role for somebody doing an access review, which is why a
product with an audit log has one.

The mechanism is three pieces, and the shape of them is the point:

- `h.viewer()` beside `h.admin()`. Every administration **GET** calls the first and every
  administration **mutation** calls the second, so the role cannot drift into write access one
  handler at a time.
- `authoriseDeviceView()` beside `authoriseDevice()`. Two functions rather than a boolean
  argument, because the difference between them is the difference between reading a row and being
  handed a way onto somebody's machine, and that is not a distinction to make with a flag at a call
  site. `GET /devices/{id}/password` deliberately keeps the strict one.
- `seesWholeFleet()` replaces the `IsAdminOrManager` short-circuit in the device list and the
  dashboard, so a Read Only user's counts are the fleet's rather than an empty support group's.

**`access.Resolver` was deliberately not widened.** It answers the client-facing question — which
devices belong in a user's address book, what `/api/peers` returns — and an auditor has no business
appearing to their RustDesk client as somebody who may connect. The role exists on the portal
surface only, and `TestReadOnlyIsNotGrantedDeviceAccessByTheResolver` pins that.

**A second defect found while doing it, and it is the same shape as the first.** The portal's user
screen hardcoded `['Administrator', 'Manager', 'Technician']`. **"Manager" is not a role this
deployment has** — the seeded one is "Support Manager" — so that button always failed with "unknown
role", and "Read Only" could not be granted at all because it was never offered. Two of the four
seeded roles were therefore unreachable from the portal. `GET /api/v1/settings` now returns the
role list from the `roles` table, plus the caller's own roles, and the portal reads both; a list the
portal fetches cannot drift from the list the grant endpoint validates against.

**Verified.** Four integration tests in `internal/integration/readonly_test.go`. The middle one is
the one that matters: `TestReadOnlyCannotWriteAnything` is driven from
`apiv1.Handler.Routes()` rather than a hand-written list, so a mutating route added later is
covered the day it is added, and it checks twelve table counts afterwards because a 403 that still
writes would pass a status-code-only test. Proved load-bearing by pointing one mutating route at
`viewer()`: the test fails on both halves, the 201 and `device_groups changed from 2 to 3`.
The portal is at 34 tests, including one asserting the grant control offers "Read Only" and
"Support Manager" and does not offer "Manager".

### What is left, and it is only the things a machine here cannot do

Every item in this plan is now built or explicitly decided. Three things remain unverified, and
none of them is code:

1. **No `odv-*` workflow has still ever run.** 5.8 fixed the branch filter and this session fixed
   what the guards actually check, but the first real evidence is the first push. Expect it to
   surface things, the way bringing the stack up in Phase 2 did.
2. **Nobody has clicked through a browser sign-in.** Both halves of it are now exercised by tests
   against a stub identity provider, and every piece is checked individually, but no human has
   opened Keycloak's login page against this deployment. It is the same gap Phase 0 left.
3. **No real RustDesk client has run any of the client-side work.** 1.3's lock, 2.8's boot
   arguments, 3.1's enrollment and 3.3's password all compile on the pinned toolchain and match
   the server's contract on both sides. None has been watched on an Android device.

The one product question still open is 6.8's: where server-side session recordings should be
stored, if they are wanted at all.

---

## Verification

Phase 0, in order:

1. `cd platform && docker compose config` parses (catches 0.1, already a CI step that has never run).
2. `docker compose up -d` reaches healthy on every service.
3. Portal loads, redirects to Keycloak, and an administrator signs in (0.3, 0.4, 0.5).
4. `curl -i https://<host>/api/v1/devices` returns 401 without a token, 200 with one.

Per phase after that:

- `cd platform/api && go build ./... && go vet ./... && go test ./...`
- `ODV_TEST_DB=1 go test ./internal/integration/ -v`
- `cd platform/web && npm ci && npm run build && npm test`
- `govulncheck ./...`
- Manual: a technician account confirmed 403 on a device outside its support groups, on every
  `apiv1` resource, with the response checked rather than the UI.
- Phase 3: enroll a real Android client end to end, confirm it appears with an identity, then
  force-disconnect a live session from the portal and confirm the client drops it.

---

## Session log

Append one entry per working session so the next session knows where the work stopped.

### 2026-08-14, session 1: Phase 0

Phase 0 complete and verified by bringing the stack up for real (see the verification table
above). All of 0.1 to 0.21 are done.

Fourteen defects beyond the original audit's seven turned up, and every one of them was fatal
to startup or to correctness on its own. That is the strongest evidence yet that the stack had
never been run: fixing only the seven documented items would still have produced a stack that
does not boot. The pattern worth carrying forward is that each fix exposed the next failure,
because the first one masked everything behind it.

Grouped by what they tell you:

- **Nothing had ever reached Keycloak's realm import.** The healthcheck called a binary absent
  from the image (0.9), `start` will not boot without `KC_HTTP_ENABLED` (0.8), and once it did
  boot the realm file failed three separate schema validations (0.10, 0.16, 0.20).
- **The token path was wired to two different Keycloak identities.** The API validated an
  issuer Keycloak never mints (0.11) and portal tokens carried no `odv-api` audience (0.12), so
  even a successful sign-in would have 401'd on every API call.
- **The images could not build.** The worker referenced a nonexistent build stage (0.13) and
  both Go images pinned a toolchain older than `go.mod` accepts (0.14).
- **Caddy could not start and hbbs could not bind.** The Caddyfile used shell variable syntax
  Caddy does not implement, and the caddy service got no environment at all (0.18); hbbs
  published hbbr's relay port (0.15).
- **Configuration was documented but not delivered.** Beyond the naming mismatches in
  `.env.example` (0.7), the api and worker services never received 18 of the variables the code
  reads (0.21). Renaming a variable to match the code is not enough if compose does not pass
  it; both halves had to be fixed for any of it to take effect.

One finding worth remembering beyond this phase: **Keycloak's `--import-realm` does not
substitute `${env.VAR}`** (0.17). Anything in the realm file that must vary per deployment has
to be templated before the container starts.

### 2026-08-14, session 2: Phase 1

1.1, 1.2 and 1.4 are done; 1.3 is done on the server side with the client-side half
outstanding; 1.5 and 1.6 stay deferred to 3.1 as the audit specifies.

Two things worth carrying forward:

- **Renaming a SQL column is not compile-checked.** `client_sessions.rustdesk_token` became
  `token_hash`, and `go build` stayed green while three functions
  (`CreateClientSession`, `GetClientSession`, `InvalidateClientSession`) still referenced the
  old name in raw SQL and would have failed at runtime. Only grepping the string found them.
  Any future column rename needs the same sweep.
- **A test can pass without testing anything.** The per-IP throttle test passed in 0.16 s when
  the work it claims to do costs ~450 ms of Argon2. `login_attempts` has no foreign key to
  `users`, so the fixture's `TRUNCATE ... CASCADE` never reached it and rows leaked between
  runs, tripping the throttle before the loop did. Fixed by listing the table explicitly; the
  test now takes 0.65 s. The suspicious duration was the only signal that anything was wrong.

Verified: `go build`, `go vet`, `go test ./...` all pass, including the integration suite
against a real PostgreSQL (`ODV_TEST_DB=1`), which is what actually exercises migration 000004.
Timing equalisation measured at 15 ms unknown vs 20 ms known account. The `:21114` redirect
checked against a running Caddy for both GET and POST.

Next session: Phase 2. Suggested order is 2.1 (the forgeable audit trail, the most serious
remaining item and the one that most undermines the product's stated value), then 2.2 rate
limiting, then the smaller 2.4, 2.5, 2.6, 2.7 together. 2.3 depends on the `odv-api` client
secret, which the realm file no longer pins — see 0.17.

Not done and still open from Phase 1: the `OVERWRITE_SETTINGS` spike in the Android build
(1.3 part 2). That is the half that actually makes fleet redirection impossible, and it is
client-side work rather than platform work.

### 2026-08-14, session 3: Phase 2

All of 2.1 to 2.8 landed, plus 4.5 and one new defect (2.9 / 4.18) found while verifying.
2.3 is closed as an explicit 501 rather than an implementation, because the client secret
decision from 0.17 is still open and a placeholder token is worse than an error.

Four things worth carrying forward:

- **The audit endpoints matched no client at all.** Not just unauthorised: the Go structs
  expected `{device_id, user_id, protocol, start_time}` and the client sends
  `{id, uuid, conn_id, session_id, nonce, action}`. Reading the Rust that builds each request
  was the only way to see it, and it is worth doing for every remaining RustDesk-compatible
  endpoint before assuming it works. The same reading turned up that the device posts these
  with an empty auth header, so the route cannot serve real clients until 3.1.
- **The audit's claim about a UNIQUE constraint was wrong.** `connection_sessions.client_id`
  has no uniqueness; the `UNIQUE NOT NULL client_id` is on `api_clients`. Checking the schema
  rather than the audit text saved a migration that would have done nothing.
- **Bringing the stack up found what the tests could not.** The heartbeat 500 (2.9) is invisible
  to `go test`: every unit test constructs a device with fields populated, and the integration
  fixture inserts devices with `os` and `hostname` set. Only a real device row, created by the
  code under test, has the NULLs. One `curl` at a running server found a defect that means the
  telemetry path has never once worked.
- **Caddy's parser treats an unset variable as a missing argument, not an empty one.**
  `email {$CADDY_ACME_EMAIL:}` fails to parse; `email "{$CADDY_ACME_EMAIL:}"` is fine. Same
  class of problem as 0.18 and worth remembering for any future optional directive.

Verified by measurement, not assumption: see the Phase 2 verification table above. The full Go
suite passes including the integration tests against a real PostgreSQL.

Not verified: the Dart change (2.8) is unbuilt, because there is no Flutter toolchain here.

### 2026-08-15, session 4: Phase 3

3.1, 3.2 and 3.4 landed; 3.5 is half done; 3.3 is blocked and the reason is in its section.
1.5, 1.6, 4.3, 4.4 and 4.9 are closed as a consequence. Fourteen new integration tests, and the
whole device lifecycle was driven end to end against a running stack with curl.

The shape of the phase: devices now have an identity they can prove, and the platform now has a
channel to reach them. Those two together are what turn "the portal says this technician has no
access" into something that happens on the machine.

Five things worth carrying forward:

- **The enrollment feature could never have worked, in two independent ways.** The token hash
  was written as a raw `[32]byte` into a `VARCHAR(64)`, and `ConsumeToken` required
  `expires_at > now()` while the handler stored Go's zero time. Either alone would have made
  every token unredeemable. This is the same pattern as Phase 0: unused code accumulates defects
  silently, and each one hides the next.
- **Append-only broke deletes, and the honest fix was to drop foreign keys.** `ON DELETE SET
  NULL` means a delete issues an UPDATE, which an append-only trigger must refuse. Relaxing the
  trigger to allow "only the foreign keys changed" would have left a hole exactly where it
  matters, because nulling `user_id` is how you erase who did something. Audit rows outliving
  the rows they reference is the correct model for an audit log.
- **TRUNCATE does not fire row triggers.** That is why the integration fixture can still clear
  `audit_events`, and also why the append-only guarantee has a hole that only a non-owning
  database role can close. Both facts are written down where they matter.
- **A test caught a schema constraint that the design had not considered.** `devices.uuid` is
  `NOT NULL` with a unique index, so two devices enrolling without a machine uuid collided on
  the empty string. It surfaced only because a subtest enrolled a second device.
- **The client is now the critical path.** Everything the server needs is in place and verified.
  A stock RustDesk client cannot enroll, because it sends no credential; the `sync.rs` change
  that makes it possible is written and has never been compiled. Four items now sit behind a
  Rust and Flutter toolchain, and they should be done in one session on a machine that has them.

### 2026-08-15, session 5: Phase 4

Phase 4 is complete: 4.6, 4.7, 4.8 (testing) and 4.11 to 4.17 (correctness) all landed, in three
batches, each verified before moving on. One new item, 4.19, was found while doing 4.16 and is
recorded rather than fixed.

Counts: four new test files (`idor_test.go`, `control_test.go`, `roundtrip_test.go`,
`ErrorBoundary.test.tsx`), plus additions to `identity_test.go` and `router_test.go`. The Go
suite passes with `ODV_TEST_DB=1` against a real PostgreSQL; the portal is at 30 tests, and
`npm run build` passes.

Five things worth carrying forward:

- **A sweep is only as good as its completeness guard.** `TestIDORSweepCoversEveryRoute` compares
  the case table against `apiv1.Handler.Routes()` in both directions, so a route added later
  cannot skip the check. Without that half, the sweep would cover exactly the routes that existed
  the day it was written, and would look just as green.
- **A 403 is half a test.** The other half is that nothing changed.
  `TestForeignRequestsChangeNothing` runs the whole refused sweep and then checks twelve tables
  and two named escalations. A handler that writes and then refuses would pass a status-code-only
  test.
- **A 200 is where a scoping bug hides.** Three endpoints answer 200 to everyone and filter
  instead, and the only evidence they filter at all is that two technicians with different access
  get different bodies. That is asserted directly now.
- **The migration round-trip found what the plan predicted, and the allowance was verified to be
  load-bearing.** Emptying `knownRollbackDifferences` fails the test with eight lines naming the
  four `NOT VALID` constraints. An allowance list nobody has proved is doing work is
  indistinguishable from a test that does not check.
- **Two of the audit's items were not quite the defect described, and one was bigger.** 4.14's
  startup context never reached the migrations at all (fixed the real, smaller version and wrote
  down why migrations get no deadline). 4.12's counter already existed and was simply never read.
  4.16 was seven unused dependencies rather than six. Checking each claim against the code before
  fixing it is the same habit that corrected the `UNIQUE` constraint claim in Phase 2.

One latent bug came out of the 4.11 refactor rather than being looked for: `GetSessionUser`
attached no support groups while the other three lookups did, so the same account was a
different object depending on which door it came through. Nothing reads that field today, which
is exactly why four copies of a query could drift without anyone noticing.

Next session: Phase 5, the repository cleanup (5.1 to 5.5), plus 4.19. All of it is doable with
the tooling here. The four client-build items (1.3 part 2, 2.8, 3.1's `sync.rs`, 3.3) and the
non-owning database role for 3.5 are still the only things that are not.

### 2026-08-15, session 6: Phase 5 and 4.19

Phase 5 is complete and so is 4.19, in three batches. Two items were found while doing them and
are recorded as 5.7 and 5.8; both are more serious than anything Phase 5 originally listed.

The phase was scoped as "repository cleanup", the lowest-priority tier in the audit. It turned
out to contain a live SQL injection and the discovery that no CI has ever run. That is worth
noting for its own sake: the items were filed as low priority because they looked cosmetic from
the outside, and three of the six were mislabelled once someone actually opened them.

Five things worth carrying forward:

- **No `odv-*` workflow has ever executed.** All four filter on `branches: [main]`; the default
  branch is `master`. GitHub does not warn about a branch filter matching nothing. Every CI
  guard this project wrote was inert, which retroactively explains why Phase 0 found fourteen
  startup defects in a repository that had a compose-parse check, and why a vulnerability scan
  sat in the workflow while pgx carried a reachable SQL injection. **A green Actions tab and an
  empty one look the same from a distance.**
- **The dependency was the vulnerability, not the code.** Phases 1 to 4 went through this
  project's SQL by hand. `GO-2026-5004` was underneath all of it, in pgx's own placeholder
  sanitiser. Auditing your own queries does not cover the driver that runs them, and only
  running the scanner finds that.
- **Two Phase 5 items were factually wrong in the audit, in the same direction.** 5.1 named
  `check-client-config.sh` as citing `finish-line.md` when it does not, and 5.2 called the API
  surface baseline a "build artifact" when it is a hand-reviewed file whose absence broke CI.
  Both errors made the items sound more cosmetic than they were. That is now four audit claims
  corrected by checking the code first, after the `UNIQUE` constraint in Phase 2 and the startup
  context in 4.14.
- **Deleting a file loses whatever was never committed.** `plans/finish-line.md` had 119 lines of
  uncommitted revisions, invisible to "it is all in git history". `git rm` refused until forced,
  which was the only warning. The durable part was moved into this file first.
- **A relaxation nobody has proved is doing work should be deleted.** The ESLint config's
  test-file exemption was written on the assumption that Testing Library would trip the
  type-aware rules. Removing it and re-running produced zero errors, so it went. Same check as
  `knownRollbackDifferences` in 4.8, and it went the other way, which is the point of running it.

Verified by measurement: `npm run lint` exits 0 over 26 files, `npm run build` passes, 30 portal
tests pass, `go build`/`go vet` pass, the full Go suite passes on go1.26.6 with pgx v5.9.2
including the integration tests against a real PostgreSQL (18.5 s, not a silent skip), the API
surface sweep `--check` passes against its moved baseline, and `govulncheck` reports
`No vulnerabilities found` where it reported eight at the start of the session.

Not verified: the CI workflows themselves. The branch-filter fix is a one-line change whose
correctness is obvious from `origin/HEAD`, but the first real evidence will be the first push
that triggers a run — and given 5.8, that will be the first `odv-*` run in this repository's
history. Expect it to surface things, in the same way that bringing the stack up in Phase 2 did.

Still open, and unchanged: the four client-build items (1.3 part 2, 2.8, 3.1's `sync.rs`, 3.3)
and the non-owning database role for 3.5.

### 2026-08-15, session 7: the client-build items

Rust 1.82.0 and Flutter 3.24.0 installed, matching `odv-android.yml`. Three of the four
client-build items closed and verified: 1.3 part 2, 2.8 and 3.1's client half. 3.3 remains, but
it is now ordinary work rather than blocked.

Five things worth carrying forward:

- **The fork's Android clients have always pointed at RustDesk.** `RENDEZVOUS_SERVERS` and
  `RS_PUB_KEY` are compile-time constants in `hbb_common` naming `rs-ny.rustdesk.com` and
  RustDesk's own key, and nothing anywhere reads the `RS_PUB_KEY` / `RENDEZVOUS_SERVERS` /
  `API_SERVER` environment variables that `check-client-config.sh` insists on. The guard has
  three independent reasons to be useless: nothing consumes the values, it runs in a workflow
  that builds no APK, and per 5.8 no `odv-*` workflow has ever run. **A guard that checks its
  inputs exist, rather than that they took effect, is not a guard.**
- **The stock lock is closed to forks by signature.** `read_custom_client` verifies against a
  public key hardcoded to RustDesk's, so `OVERWRITE_SETTINGS` cannot be reached the intended
  way. Worth knowing before planning around any other RustDesk "custom client" capability: they
  are gated the same way.
- **`OVERWRITE_SETTINGS` is stronger than it looks, and that is what made the fix small.**
  `is_option_can_save` does not merely prefer the locked value, it discards the write and
  removes the key. One insertion therefore closes pushed strategies, boot arguments and manual
  config edits together.
- **A green test can be a test of nothing.** The deployment-lock test passes both with and
  without the `ODV_*` variables, by design, because a build without them must behave like
  upstream. That makes "ok" ambiguous, so which branch ran was confirmed independently with
  `strings` on the test binary, and the assertion was proved load-bearing with a negative
  control that failed exactly as predicted. Same discipline as `knownRollbackDifferences` in 4.8
  and the ESLint override in 4.19, and here it was needed twice over.
- **`flutter analyze` has never been run on this tree.** 897 errors, one structural cause: two
  `RustdeskImpl` classes selected by conditional import, so identical type names from different
  libraries do not unify. It is the third instance of the session-6 pattern, after 4.19's
  never-configured lint and 5.8's never-triggered CI. The lesson is now well evidenced: **in
  this repository, assume any quality gate has never executed until you have watched it run.**

Verified by measurement: `cargo check --features linux-pkg-config` on Rust 1.82.0, zero errors,
nothing in `sync.rs`. `flutter analyze` on Flutter 3.24.0, 897 errors with the 2.8 change and
897 with it stashed, so a delta of zero. The deployment-lock test passes in both configurations,
with the baked-in value confirmed in the binary and the assertion confirmed by negative control.

Next session: 3.3, then 3.5's non-owning database role, then Phase 6.

### 2026-08-15, session 8: 3.3, and the whole of Phase 6

Phase 6 is complete. 6.1 to 6.7 and 6.9 are built and verified; 6.8 is settled as a decision
rather than an implementation, for reasons in its section. With it, every item in this plan is
closed.

Batches, in order: 3.3/6.1 (per-device connection passwords), then 6.2/6.3/6.4 with the
repository cleanup, then 6.9/6.5/6.6/6.7/6.8.

Eight things worth carrying forward, and most of them are about verification rather than code:

- **Two decisions the plan was waiting on turned out not to need making.** 6.3 was blocked on how
  to get the `odv-api` client secret to the API; the answer is that no secret is needed, because
  `odv-portal` is a public client with PKCE. 6.8 was blocked on whether recording was feasible at
  all; it is, and the client half already ships upstream. **A question that has been open for a
  while is worth re-reading the source about rather than re-deciding.**
- **Reading `src/hbbs_http/account.rs` corrected the plan twice.** `/api/oidc/auth` is not a
  redirect, and `/api/oidc/auth-query` is a `GET` — which means item 2.3's "correction" of
  `openapi.yaml` to `POST` made the spec wrong to match a handler that was wrong. That is the
  fifth audit-era claim corrected by checking the code first.
- **A negative control has to use a value that is actually absent.** The first attempt at proving
  6.4's artifact check was load-bearing used `https://attacker.example`, which is a string literal
  in session 7's own lock test and therefore genuinely present in the binary. The check was right;
  the control was not.
- **`grep -q` under `set -o pipefail` is a bug, and a non-deterministic one.** `strings … | grep -q`
  makes grep exit on the first match, `strings` die of SIGPIPE, and the pipeline report failure. It
  passes on small inputs and fails on large ones, which is the worst way for a guard to be wrong.
  Found because 6.4's check failed on a 217 MB binary that contained the string twice.
- **One parameter used at two types is a statement Postgres refuses outright.** `$1` as both a
  `VARCHAR(100)` insert value and a `text[]` comparison gives "inconsistent types deduced for
  parameter $1", and the fix is explicit casts rather than trusting inference.
- **A test comparing a report against the thing it reports on found the report wrong.** The access
  review counted device-group members while the resolver counts ACTIVE devices, so it overstated
  who could reach what — the single failure mode an access review must not have. The test existed
  because that was the obvious thing to get wrong, and it was.
- **Two of four seeded roles were unreachable from the portal.** The user screen hardcoded
  `['Administrator', 'Manager', 'Technician']`; "Manager" does not exist and "Read Only" was not
  offered. Found while implementing 6.9. The list now comes from the API, so the portal's options
  and the grant endpoint's validation cannot disagree.
- **Every new refusal was proved load-bearing by breaking it on purpose.** 6.2's append-only grant
  (granting `UPDATE` by hand fails the test), 6.9's write sweep (pointing one mutating route at
  `viewer()` fails on both the status code and the row count), 6.4's artifact check in three modes
  including an empty directory. This is now the standing habit from 4.8 and it has paid twice more.

Verified by measurement: `go build`, `go vet`, `gofmt`, and the full Go suite with `ODV_TEST_DB=1`
against a real PostgreSQL — the integration package runs about 29 s and the migration package
about 6 s, so neither is silently skipping. Ten migrations round-trip, individually and in full.
The portal is at 34 tests with `npm run lint` clean and `npm run build` passing. `cargo check
--features linux-pkg-config` on Rust 1.82.0: zero errors, and the check was proved to be running
rather than cached by a negative control that failed exactly as predicted. `docker compose config`
parses, and fails when a newly required secret is missing, which is the `${VAR:?}` form working.

Repository state: `.gitignore` now covers the platform's own build output, coverage, local stack
state and `.env` variants, verified in both directions — the artifacts are ignored and no tracked
file became ignored. The root `CLAUDE.md` was removed (see 5.x). No secrets, build output or stray
files are tracked.

Not verified, and it is the same three things listed above the Verification section: no `odv-*`
workflow has run, nobody has clicked through a browser sign-in, and no real RustDesk client has
exercised the client-side work.
