# OpenDeskViewer — remediation plan after first implementation pass

## Context

The original plan (self-hosted RustDesk management platform: Go API + React portal +
Keycloak + PostgreSQL around unmodified `hbbs`/`hbbr`) has had a first implementation pass
done by hand. This document is the result of reviewing that work against the plan, plus a new
requirement that was not in the original scope:

> The Android, Linux, and Windows apps should all be pre-configured to this environment, and
> an admin/management account signing into the desktop apps should see every device in the
> environment.

The review verdict: **the skeleton and the schema are broadly right, but nothing runs.** The
Go module does not compile, the RustDesk Pro API contract is not matched, the authorization
model has correctness and security defects, and the deployment stack exposes several services
it should not. The Android CI workflow does not build a working APK. Roughly 20% of the
planned surface exists.

Reviewed at commit `1d09760ef` + untracked `platform/`, `.github/workflows/odv-android.yml`,
and seven root-level `.md`/`.sh` files.

---

## What is solid and should be kept

- **Directory layout** matches the plan (`platform/api/internal/{access,config,enrollment,
  fleet,httpx,identity,postgres,rustdeskapi,telemetry}`, `platform/web`, `platform/android`).
- **`platform/migrations/00001_initial_schema.sql`** covers nearly every planned entity, with
  the right `pg_trgm` GIN indexes on `devices.name`/`hostname` and sensible btree coverage.
  It needs fixes, not a rewrite.
- **`platform/api/openapi.yaml`** lists all 23 Pro-compat paths with correct names — good
  evidence the contract research landed.
- **`internal/config/config.go`** covers most of the needed environment surface.
- Compose/Caddy/Keycloak/Casbin scaffolding exists in the right shape.

---

## Findings

### P0 — Security

1. **CORS reflects any origin with credentials.**
   `internal/httpx/router.go:129-136` — `CORSMiddleware(allowedOrigins []string)` accepts the
   allowlist and **never reads it**, setting `Access-Control-Allow-Origin` to the caller's own
   `Origin` header alongside `Access-Control-Allow-Credentials: true`. Any website a logged-in
   technician visits can make credentialed calls to the API and read the responses.
   Fix: match `origin` against the configured list; emit nothing on a miss; add `Vary: Origin`.
   `API_CORS_ORIGINS` is not in `config.Config` at all and must be added.

2. **Caddy admin API exposed on all interfaces.**
   `platform/Caddyfile:2` sets `admin :2019` (Caddy defaults to `localhost:2019`) and
   `docker-compose.yml:209` publishes `2019:2019`. That is unauthenticated remote control of
   the reverse proxy — anyone who reaches it can re-route or terminate traffic.
   Fix: delete the `admin` directive, delete the port mapping.

3. **No TLS anywhere.** `Caddyfile:3` sets `auto_https off` and the only site block is `:80`.
   Keycloak runs `start-dev` with `KC_HOSTNAME_STRICT_HTTPS: "false"`
   (`docker-compose.yml:48,58`). OIDC tokens and session cookies cross the network in plaintext.
   Fix: real site blocks keyed on `PORTAL_HOST`/`KEYCLOAK_HOST` with ACME, `ACME_EMAIL` in env.

4. **Datastores published to the host.** `postgres 5432` (`docker-compose.yml:15-16`),
   `redis 6379` (`:31-32`), `keycloak 8080` (`:61-62`), `api 8000` (`:132-133`).
   Only Caddy needs host ports. Fix: drop these `ports:` blocks; keep them on the internal
   network. Use `expose:` where documentation value is wanted.

5. **Keycloak admin credentials hardcoded to `admin`/`admin`**, and under the wrong variable
   name. `docker-compose.yml:50-51` sets `KEYCLOAK_ADMIN_USER`, but Keycloak 23 reads
   `KEYCLOAK_ADMIN` — so the intended admin is never created and the password is a literal
   anyway. `KC_DB_PASSWORD: keycloak_password` (`:55`) is likewise hardcoded.
   Fix: `KEYCLOAK_ADMIN`/`KEYCLOAK_ADMIN_PASSWORD` from env, no defaults.

6. **Enrollment tokens stored in plaintext.**
   `migrations/00001_initial_schema.sql:163` (`token VARCHAR(255) UNIQUE NOT NULL`) and
   `internal/enrollment/service.go:55-60`. A database read discloses live enrollment secrets.
   Fix: store `token_hash` (SHA-256) plus a short non-secret `prefix` for display; look up by
   hash; return the plaintext exactly once at creation.

7. **`crypto/rand` error ignored.** `internal/enrollment/service.go:51` — `rand.Read(token)`
   discards its error. Fix: check it and fail closed.

8. **JSON built by string interpolation.**
   `internal/rustdeskapi/handlers.go:64-65` formats `user.DisplayName`, `user.Email` and the
   session token straight into a JSON literal via `fmt.Fprintf`. A display name containing a
   quote breaks or forges the response. Fix: `encoding/json` everywhere. This is the pattern
   across the whole file (`:33`, `:90`, `:118`).

9. **Session token echoed back in the response body** (`handlers.go:57,65`). Unnecessary
   disclosure; the client already holds it. Fix: remove.

10. **Authorization ignores device state and user status.**
    `internal/access/resolver.go:50-68` and `internal/identity/service.go:282-300` check only
    group membership. Neither filters `devices.state` nor `users.active`. A `DISCOVERED`
    device — one that self-asserted its ID over the unauthenticated `/api/sysinfo` endpoint and
    was never claimed — is therefore connectable, and a disabled user retains all access. The
    latter directly violates design §40 ("Disabled user → Everything = DENY").
    Fix: `AND d.state = 'ACTIVE' AND u.active`. Add the tests from §40.

11. **Enrollment token consumption is not atomic.**
    `internal/enrollment/service.go:99-116` reads, checks `uses >= max_uses`, then updates in a
    separate statement. Concurrent enrollments overrun the cap. Fix: single
    `UPDATE ... SET uses = uses + 1 WHERE id = $1 AND (max_uses IS NULL OR uses < max_uses)
    RETURNING ...` and treat zero rows as failure. Note `ConsumeToken` uses `MaxUses > 0`
    while `ValidateToken` (`:129`) uses `max_uses IS NULL` — pick one.

### P1 — Does not compile or does not work

12. **The Go module does not build.** `go build ./...` in `platform/api` fails.
    - `go.mod:16` contains a literal placeholder line `// ... (full dependencies in go.mod)`.
    - `github.com/casbin/casbin/v2 v2.110.1` does not exist (`unknown revision`).
    - `github.com/dxtr/otp` is not a real dependency of this project.
    - There is no `go.sum`.
    Fix: regenerate with `go mod init`/`go get` against versions that resolve, then `go mod tidy`.

13. **Two `main` packages.** `platform/api/main.go` and `platform/api/cmd/api/main.go` are
    near-duplicates; the former makes the module root a `main` package. Delete the root one.

14. **`telemetry.go` imports stdlib `log` then calls zerolog on it.** `:6` imports `"log"`,
    `:168,181,208,222` call `log.Error().Err(err).Msg(...)`. Compile error. Also `:214`
    declares `retention` and never uses it (compile error), and `NewHandlers` in
    `rustdeskapi/handlers.go:22` takes `*telemetry.Service` while the type is
    `TelemetryService`.

15. **Heartbeat and sysinfo parse the wrong wire format.**
    `handlers.go:73-75,97-103` use `r.FormValue`, but the client posts **JSON bodies**
    (`src/hbbs_http/sync.rs:235-243` for heartbeat, `:125-179` for sysinfo). Every heartbeat
    will 400. Fix: decode JSON into typed structs — heartbeat is
    `{id, uuid, ver, conns, modified_at}`; there is no `online` field, liveness is implied by
    the request itself.

16. **`/api/login-options` returns the wrong JSON type.**
    `handlers.go:33` returns an object `{"loginOptions":[...]}`. The client does
    `for (final item in jsonDecode(resp.body)) ops.add(item as String)`
    (`flutter/lib/models/user_model.dart:243-260`) — it requires a **bare JSON array of
    strings**. Login option discovery throws as written.

17. **`/api/currentUser` returns the wrong shape.** `handlers.go:52-65` emits
    `{id, username, email, role, token, groups}`. The client parses `UserPayload`:
    `{name, display_name, avatar, email, note, verifier, status, is_admin}`
    (`flutter/lib/common/hbbs/hbbs.dart:26-49`). Note `status` is an int (`1` normal, `0`
    disabled, `-1` unverified), not a string. `getGroupNames` (`handlers.go:130`) returns the
    literal string `"[2]"` — a count, not names.

18. **`ProcessSysinfo` writes columns that do not exist.**
    `telemetry.go:114-123` updates `username` and `platform`; `devices` has neither
    (`migrations/00001_initial_schema.sql:122-138`). Runtime SQL error on every sysinfo post.

19. **`recomputeConnectivity` passes a Go `time.Duration` where SQL expects an interval.**
    `telemetry.go:159-178` compares `now() - last_seen_at > $2` against a duration that pgx
    encodes as an int64. Fix: pass seconds and use `make_interval(secs => $2)`.

20. **Migrations will not run the way the stack invokes them.** Files are named
    `00001_initial_schema.sql`; `golang-migrate` (declared in `go.mod`) requires
    `.up.sql`/`.down.sql`. Separately, `docker-compose.yml:14` mounts the directory into
    `/docker-entrypoint-initdb.d`, which only executes on an empty volume and would also try to
    execute `apply.sh`. Pick one mechanism — embedded `golang-migrate` run at API startup — and
    drop the initdb mount.

21. **Schema defects.**
    - `increment_enrollment_uses()` (`:340-349`) references `NEW.enrollment_token_id`, a column
      no table has, and the trigger is never attached. Dead and broken.
    - `accessible_devices` view (`:279-288`) calls `current_setting('app.current_user_id')`
      without the `missing_ok` argument — it raises whenever the setting is unset.
    - `devices.uuid` has no unique constraint, so re-registration and ID collision cannot be
      detected as the plan intended.
    - `customers.code` / no `external_id`, `locations` has no `external_id` — deviates from
      design §25 and from the integration story.
    - `connection_sessions.status` defaults to `'active'`; the plan and design §20 specify
      `REQUESTED|STARTED|ENDED|FAILED`.

22. **The redundant self-join is a performance trap, in four places.**
    `resolver.go:55-58`, `resolver.go:75-78`, `identity/service.go:287-290`,
    `migrations:282-283` all join `device_group_members` to itself on the same
    `device_group_id`. It is semantically a no-op but multiplies intermediate rows by the group
    size — a 5,000-device group produces 25M rows per authorization check. Remove `dgm2`
    entirely.

23. **Android CI workflow cannot produce a working APK.**
    `.github/workflows/odv-android.yml` — every one of these is fatal:
    - Never builds `librustdesk.so`. No `cargo ndk`, no `flutter/ndk_arm64.sh`, no copy of
      `liblibrustdesk.so` → `librustdesk.so`, no `libc++_shared.so`. The APK would have no
      native library.
    - Never runs `flutter_rust_bridge_codegen`. `flutter/lib/generated_bridge.dart` is
      gitignored and absent, so `flutter build` fails outright.
    - `flutter build apk` runs from the repo root, not `flutter/`.
    - `./vcpkg install` (`:66`) does not exist; the real script is
      `flutter/build_android_deps.sh <abi>`. `continue-on-error: true` (`:67`) hides the failure.
    - Cache key hashes `flutter/vcpkg.json`, which does not exist (it is at the repo root).
    - **No `RENDEZVOUS_SERVER` / `RS_PUB_KEY`** — the entire point of the workflow, absent.
    - The "Sign APK" step (`:90-93`) unzips and re-zips the APK with a stray literal `APK`
      argument. It produces a corrupt, unsigned archive. No `apksigner`, no keystore.
    - Upload (`:81`) runs before signing (`:88`), so the artifact is the unsigned one.
    - NDK `r28b` vs the tree's pinned `r28c`; Flutter 3.22.3 (the *bridge* version) is used for
      the *build*, which needs 3.24.5 plus the SDK patch at
      `.github/patches/flutter_3.24.4_dropdown_menu_enableFilter.diff`.
    - Creates a GitHub Release on every push to master.

24. **`platform/android/rebrand.sh` does not rebrand.**
    - `:39` matches `android:label="FlutterDesk"`; the actual value is `"RustDesk"`
      (`flutter/android/app/src/main/AndroidManifest.xml:28`). No-op.
    - `:44` replaces `FlutterDesk` → `opendeskviewer`; that string does not occur. The deep-link
      scheme stays `rustdesk` (`AndroidManifest.xml:79`).
    - `:56-59` fixes the package declaration in `LaunchActivity.kt` only. There are **12**
      Kotlin files under `com/carriez/flutter_hbb`, all with `package com.carriez.flutter_hbb`.
      The other 11 break the build.
    - `PACKAGE_NAME` is a **path** (`com/opendeskviewer/android`) but is substituted into a
      Kotlin `package` statement at `:58`, producing `package com/opendeskviewer/android`.
    - Never touches `app/src/debug/AndroidManifest.xml`.
    - `:69-73` copies one mdpi icon into all five density buckets.
    - `:85` copies `app/key.properties.example` (wrong path) to `app/key.properties`; Gradle
      reads `flutter/android/key.properties` (`flutter/android/app/build.gradle:12`).
    - Not idempotent and irreversible — it edits the working tree in place.

### P2 — Planned but missing

25. **No `/api/v1` surface at all.** No `internal/apiv1` package; `openapi.yaml` documents only
    the 23 Pro-compat paths and zero portal endpoints. The React portal has nothing to call.
    (`openapi.yaml` also has an invalid path template
    `/api/ab/peer/{add,update}/{guid}` — that is two paths.)
26. **No audit service.** No `internal/audit` package, no `audit.Record(...)` from design §32,
    despite `audit_events` existing in the schema. No mutation is currently audited.
27. **Only 4 of 23 Pro endpoints implemented** — `login-options`, `currentUser`, `heartbeat`,
    `sysinfo`. Missing: `/api/login`, `/api/logout`, the whole `/api/ab/*` family, the OIDC
    broker (`/api/oidc/auth`, `/api/oidc/auth-query`), `/api/device-group/accessible`,
    `/api/users`, `/api/peers`, `/api/sysinfo_ver`, `/api/audit/conn`, `/api/audit/file`.
    Without `/api/ab/*` and `/api/peers` the technician address book — the core workflow — does
    not exist.
28. **No OIDC token validation.** `config.Config` has a `JWTSecret` (`config.go:42`), implying
    self-signed tokens rather than the planned Keycloak JWKS verification with audience checks.
    No JWKS client, no middleware.
29. **`CasbinResolver` contains no Casbin.** `internal/access/resolver.go` is hand-written SQL;
    `platform/casbin/model.conf` is mounted (`docker-compose.yml:135`) and unused. Either wire
    Casbin in for role capabilities or drop it and rename the type honestly.
30. **The React portal is a static mockup.** `platform/web/src/App.tsx` hardcodes `0` for every
    metric. No router, no auth, no data fetching, no `src/pages`. `platform/web/Dockerfile` is
    referenced by `docker-compose.yml:183` but **does not exist**, so `docker compose build`
    fails. No `tailwind.config.js` despite Tailwind classes and a `postcss.config.js`.
31. **Zero tests.** No `*_test.go`, no `*.test.tsx`, anywhere — against a plan that called for
    the authorization matrix and Pro-contract golden files to be the most heavily tested code.
32. **`/api` is unreachable at the URL clients use.** The client derives its API server as
    `http://<rendezvous-host>:21114` (`src/common.rs:1064-1084`). Nothing listens on 21114:
    the API is on 8000 and Caddy on 80/443. Either publish the API on 21114 or set the
    `api-server` option explicitly in the baked client config.
33. **hbbs/hbbr are misconfigured.** `docker-compose.yml:79` runs `hbbs -r ${PUBLIC_HOST}:21116`
    — `-r` names the *relay* server, which is hbbr on 21117, not the rendezvous port.
    `:95` runs `hbbr -r ${PUBLIC_HOST}`; `hbbr` has no `-r`. No `-k` for key enforcement.
    Ports 21114 and 21117 are not published.
34. **Root-directory pollution.** `COMPLETION_CHECKLIST.md`, `IMPLEMENTATION_SUMMARY.md`,
    `INSTALL.md`, `README_PLATFORM.md`, `install.sh`, `install_and_verify.sh`,
    `verify_structure.sh` all sit in the repo root, against `CLAUDE.md`'s "NEVER save working
    files, text/mds, or tests to the root folder". They also duplicate `platform/README.md`.
    Consolidate into `platform/` and delete the rest.
35. **Redis was added but is unused** by any Go code reviewed. Either use it (session cache,
    rate limiting) or remove the service and its config.

### P3 — Quality

36. `ContextMiddleware` (`router.go:152`) uses a bare string as a `context.WithValue` key —
    `go vet` flags this; use an unexported key type.
37. `httpx.ReadyzHandler(db *Pool)` (`router.go:167`) refers to a `Pool` in package `httpx`;
    the real pool is `postgres.Pool`. Layering inversion.
38. `Caddyfile:20-25` answers `/healthz` and `/readyz` at the proxy with a static `200`,
    masking the API's real readiness (including its DB check).
39. `Caddyfile:8` `reverse_proxy /api*` precedes `/api/v1*` (`:14`) and swallows it; both go to
    the same upstream so it is currently harmless, but it is a trap.
40. `docker-compose.yml:1` `version: '3.8'` is obsolete and warns on modern Compose.
41. `StateDiscoveried` (`telemetry.go:19`) — typo in an exported identifier.
42. `GetDeviceEnrollmentInfo` (`enrollment/service.go:174-177`) and `updateDevicesInGroup`
    (`telemetry.go:133-136`) are stubs that silently return nothing.
43. `RevokeToken` (`enrollment/service.go:136-142`) hard-deletes, losing the audit trail.
    Prefer a `revoked_at` column.

---

## New requirement: pre-configured desktop clients + admin-sees-all

Neither half exists today. Both are additive.

### A. Admin/manager sees every device

The client already renders whatever we return; the change is server-side only.

- `access.Resolver` gains a role short-circuit: `Administrator` and `Support Manager` resolve to
  **all** `ACTIVE` devices, bypassing the support-group chain, rather than returning empty as
  they do now. Keep the `state`/`active` filters from finding 10.
- `/api/currentUser` must set `is_admin: true` (`hbbs.dart:49` reads `json['is_admin']`) so the
  client unlocks its admin-facing UI.
- `/api/ab/shared/profiles` returns one profile per device group for a technician; for an admin,
  additionally an "All Devices" profile covering the whole fleet.
- `/api/users` and `/api/device-group/accessible` are admin-visible listings — currently missing
  entirely; the client's Groups tab depends on them.
- Add explicit tests: admin → every device ALLOW; manager → every device ALLOW; technician →
  only in-group ALLOW; read-only → view but never connect.

### B. Pre-configured Windows, Linux, and Android clients

Two mechanisms, and we should ship both:

1. **Baked at build time (all three platforms).** Set `RENDEZVOUS_SERVER` and `RS_PUB_KEY` as
   build environment so they land in `hbb_common`'s compile-time constants. The read sites are
   proven (`src/common.rs:1040`, `:1873`); `.github/workflows/playground.yml:26-28` shows the
   intended wiring. **Verify the `option_env!` in `libs/hbb_common/src/config.rs` first** — the
   submodule is still not checked out (`git submodule status` shows `-69cea8da…`), and this is
   the one unproven link in the chain. Nothing Rust-side builds until it is initialized.
2. **Windows filename provisioning (no rebuild).** `src/custom_server.rs:39-108` parses
   `…host=<h>,key=<k>,api=<a>,relay=<r>.exe` from the executable name, picked up automatically
   via `get_license_from_exe_name()` (`src/platform/windows.rs:2084`). Renaming a stock
   installer configures it. Free, and worth documenting as the fallback.
   The matching desktop CLI is `rustdesk --config "host=…,key=…"`
   (`src/core_main.rs:503-528`) — note it requires installed + root.

Build work, extending the existing `.github/workflows/flutter-build.yml` rather than
reinventing it (it already has correct Windows, Linux `.deb`/`.rpm`/AppImage, and Android jobs):

- `.github/workflows/odv-android.yml` — rewrite from `flutter-build.yml:945-1237`.
- `.github/workflows/odv-desktop.yml` — new, from the Windows and Linux jobs, with the same
  baked env and rebranding.
- `platform/android/rebrand.sh` → `platform/clients/rebrand.sh`, covering all platforms and all
  12 Kotlin files, idempotent, with a `--revert`.

Because these ship **modified RustDesk binaries**, design §43's AGPL review applies to all
three. The Go/React platform stays behind a network boundary and is unaffected.

---

## Work plan

Ordered so that each phase leaves the tree in a better verifiable state than it found it.

**Phase 1 — make it build and make it safe.** Findings 1-14, 36-37, 40-41.
Regenerate `go.mod`/`go.sum` against resolving versions; delete `platform/api/main.go`; fix the
zerolog/stdlib mix and the type mismatches until `go build ./...` and `go vet ./...` are clean.
Then the P0 security set: CORS allowlist, Caddy admin removal, TLS site blocks, unpublish
datastores, Keycloak admin from env, enrollment token hashing, atomic consumption,
`encoding/json` throughout, and the `state`/`active` filters in the resolver. Land the design
§40 authorization test matrix in the same phase — it is the regression net for everything after.

**Phase 2 — schema and migrations.** Findings 20-22.
Rename to `.up.sql`/`.down.sql`, drop the initdb mount, run `golang-migrate` embedded at API
startup. Fix the broken trigger function, the `current_setting` call, add `devices.uuid`
uniqueness, `external_id` columns, the `connection_sessions.status` enum, and remove the `dgm2`
self-join in all four places.

**Phase 3 — complete the Pro-compat surface.** Findings 15-17, 27-28, plus requirement A.
JWKS validation and the OIDC broker first, then `/api/login`, `/api/logout`, the `/api/ab/*`
family, `/api/device-group/accessible`, `/api/users`, `/api/peers`, `/api/sysinfo_ver`, and the
audit intake endpoints. Golden-file tests pinned to `flutter/lib/common/hbbs/hbbs.dart` for
every response. This is the phase that makes the product work.

**Phase 4 — audit, `/api/v1`, and the portal.** Findings 25-26, 30.
`internal/audit` with `audit.Record(...)` wired through every mutation; `internal/apiv1` with
the device/customer/group/technician CRUD, search, transactional reassignment, and
`POST /api/v1/devices/{id}/connect`; `openapi.yaml` extended to cover it. Then replace the
mockup portal with the real one and add the missing `web/Dockerfile` and Tailwind config.

**Phase 5 — deployment.** Findings 32-33, 35, 38-39.
Correct `hbbs`/`hbbr` arguments and ports, decide the 21114 question, remove or use Redis,
stop masking health checks at the proxy, and clean up the root-directory files (finding 34).

**Phase 6 — clients.** Findings 23-24, requirement B.
Submodule init and the `option_env!` verification gate everything here. Then the rewritten
Android workflow, the new desktop workflow, and the cross-platform rebrand script.

Phases 3 and 4 can run in parallel across two subagents once Phase 2 lands, since they share
only the domain layer. Phase 6 is independent of 3-5 and can start as soon as the submodule
question is answered.

---

## Verification

Gates, in order — each must pass before the next phase is called done:

1. `go build ./...` and `go vet ./...` clean in `platform/api`.
2. `go test ./...` green, with the design §40 authorization matrix covered: technician in-group
    ALLOW, out-of-group DENY, admin ALLOW-all, manager ALLOW-all, read-only view-but-not-connect,
    disabled user DENY, `DISCOVERED` device DENY, reassignment revoking the old customer's
    access, concurrent enrollment respecting `max_uses`.
3. Golden-file tests for all 23 Pro paths, asserted against the field names and types in
    `flutter/lib/common/hbbs/hbbs.dart` — including `status` as an int and `is_admin` as a bool.
4. `docker compose config` valid, then `docker compose up -d` reaching healthy with **no**
    host-published ports other than Caddy's 80/443 and the RustDesk ports.
5. A port scan of the host from another machine showing only 80, 443, and the RustDesk range.
6. Simulated device: `POST /api/sysinfo` then `/api/heartbeat` with a fake `id`/`uuid` lands as
    `DISCOVERED`, is invisible to technicians, becomes `ACTIVE`/`ONLINE` once claimed, then walks
    `STALE` → `OFFLINE` when the heartbeat stops.
7. A real desktop RustDesk client pointed at the stack: sign in as a technician and see exactly
    the authorized devices; sign in as an admin and see the whole fleet.
8. End-to-end connect to a real Android device, with a `connection_sessions` row and the
    client's own `/api/audit/conn` post both landing.
9. Signed, rebranded APK and Windows/Linux packages install and register with no manual
    configuration.

---

## Open questions

- **Port 21114**: publish the API there to match the client's derived URL, or always set
  `api-server` explicitly in baked client config? The second is cleaner but makes stock,
  unbranded clients unable to find the API. Recommend doing both.
- **Casbin**: wire it in for role capabilities, or drop it and keep honest SQL? Recommend
  dropping it — the current SQL resolver plus a role short-circuit covers the four roles, and
  the `access.Resolver` interface already preserves the OpenFGA escape hatch.
- **Redis**: keep and use, or remove? Nothing needs it yet.
- **AGPL sign-off** for distributing modified Windows, Linux, and Android binaries — now
  three platforms rather than one.

(End of file - total 406 lines)
