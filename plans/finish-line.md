# OpenDeskViewer: the finish line

**Purpose.** Take the project from where it is now to a state where every remaining
task is bug fixing, not construction. After this plan is executed and its
acceptance run passes, there is no known unbuilt surface left.

**Audience.** A session with no prior context. Everything needed is here.

**Repo.** `/home/lebbi/OpenDeskViewer`. Platform in `platform/`. Go module root is
`platform/api`, so run Go as `go -C /home/lebbi/OpenDeskViewer/platform/api <cmd>`
with `GO=/usr/local/go/bin/go`.

---

## 0. Why the previous plans kept not finishing

This matters, because repeating the mistake produces a seventh plan.

Every earlier plan was written as **the output of a code review**: a numbered list
of defects someone had noticed. That makes the scope equal to "what has been
looked at so far." Each pass fixed those items, looked at new code in the
process, and found a new layer. The list never closed because it was never
derived from anything finite.

This plan is scoped from three **closed** sources instead:

1. **Every `/api/` path the client binaries actually call.** Obtained by sweeping
   `flutter/lib/` and `src/` for URL construction, not by memory. That set is
   finite and now fully enumerated in §3. It is the definition of "the client
   works."
2. **The product definition** in `plans/abundant-imagining-tower.md` §§"New
   requirement", "Work plan", plus the entity set in
   `platform/migrations/00001_initial_schema.up.sql`. Every table in that schema
   must be reachable by some feature, or be deleted. That is a closed checklist.
3. **The deployment surface**: every service in `platform/docker-compose.yml`,
   every route in `platform/Caddyfile`, every workflow in `.github/workflows/`.
   Also finite.

The sweep in item 1 immediately produced something no prior plan had listed, which
is the proof that the method matters: see finding **B1** below.

**Rule for this plan:** if a work item is not traceable to one of those three
sources, it is out of scope and belongs in §11 (explicitly deferred). No item gets
added later because someone noticed it; it gets added because a source demanded it.

---

## 1. Definition of done

The project is finished when all of the following are true. This is the contract.
Nothing else counts, and nothing here may be dropped.

**D1.** A fresh clone plus `docker compose up -d` reaches all services healthy with
no manual steps beyond filling `.env`.

**D2.** A stock, unmodified RustDesk desktop client pointed at the deployment can:
sign in as a technician, see exactly its authorised devices in the address book,
connect to one, and have that connection appear in the audit log.

**D3.** The same client signed in as an administrator sees the whole fleet.

**D4.** A pre-configured client built by CI needs zero configuration: it registers
with the deployment on first launch.

**D5.** The React portal can perform every management operation the product needs
(§7 enumerates them exhaustively), against a real API, with no mocked data.

**D6.** Every path in §3 either returns a correct response or a deliberate,
documented status. No path 404s by accident.

**D7.** `go build`, `go vet`, `go test ./...` clean; the database integration suite
passes; the portal's test suite passes; CI runs all three on every push.

**D8.** No host port is published except 80, 443, and the RustDesk range.

**D9.** Repo hygiene: no stray files in the root, no unused services, no
uncommitted patches load-bearing for a build.

---

## 2. Verified current state, as of this plan

Checked directly, not assumed. Do not re-verify these; they are the baseline.

**Working and tested:**

- Auth: two-mux public/protected split, fail-closed config, JIT user provisioning,
  disabled-user rejection, session-token and JWT credential paths.
  `internal/auth` has full negative coverage (forged, `alg:none`, HMAC confusion,
  expired, no `exp`, wrong issuer, wrong audience, kid rotation).
- Address book: all twelve `/api/ab/*` client paths, personal book stored,
  shared books projected from support groups, fleet book for admins.
- `internal/integration`: 24 tests against a real PostgreSQL covering `access`,
  `peers`, `addressbook`, `identity`, plus route-level tests for all twelve
  address book paths. Gated on `ODV_TEST_DB=1`.
- 83 tests pass, 0 failures.
- `platform/api/openapi.yaml` parses and every `$ref` resolves.
- Migrations: `00001` and `00002` apply cleanly; down migrations moved to
  `migrations/down/` so initdb no longer runs them.

**Confirmed still broken or missing** (each becomes a work item below):

| ID | Fact | Source |
|----|------|--------|
| A1 | `hbbs -r ${PUBLIC_HOST}:21116` is wrong: `-r` names the relay (hbbr, 21117) | `docker-compose.yml` |
| A2 | `hbbr -r ${PUBLIC_HOST}` is wrong: hbbr has no `-r` | `docker-compose.yml` |
| A3 | Neither hbbs nor hbbr is given `-k` for key enforcement | `docker-compose.yml` |
| A4 | Caddy answers `/healthz` and `/readyz` itself with a static 200, masking the API | `Caddyfile:20-22` |
| A5 | `reverse_proxy /api*` precedes `/api/v1*` and swallows it | `Caddyfile:7,13` |
| A6 | No migration runner: schema applies only via initdb, so an existing volume never gets `00002` | no `golang-migrate` in `cmd/` |
| A7 | Client with no `api-server` derives `http://<host>:21114` (plain HTTP, `RENDEZVOUS_PORT - 2`); nothing listens there. With nothing configured at all it falls back to `https://admin.rustdesk.com` | `src/common.rs:1064-1083`, verified |
| B1 | `PUT /api/audit` with `{guid, note}` is called by the client and is **not implemented** | `flutter/lib/common/widgets/dialog.dart:1773` |
| B2 | `/api/audit/conn` returns `{"success":true}`; the client stores a guid from that exchange | `audit.go`, `src/ui_session_interface.rs:594`, `src/flutter_ffi.rs:2054` |
| B3 | `/api/devices/deploy`, `/api/devices/cli`, `/api/record`, `/api/switch-grant`, `/api/plugin-sign` unimplemented | `src/ui_interface.rs:1102`, `src/core_main.rs:630`, `src/hbbs_http/record_upload.rs:100`, `src/hbbs_http/sync.rs:333`, `src/plugin/callback_msg.rs:283` |
| B4 | `/api/users` and `/api/device-group/accessible` return the whole set and report `total` as the page length, so the client re-fetches `ceil(N/100)` times | `users.go:68`, `peers.go:107` vs `group_model.dart:150,215` |
| C1 | No `internal/audit`; no mutation is audited; `audit_events` table unused | absent |
| D1 | No `internal/apiv1`; the portal has no API to call | absent |
| D2 | Portal is a mockup: `App.tsx` hardcodes zeros, no router, no auth, no fetching | `platform/web/src/` (116 lines total) |
| D3 | `tailwind.config.js` missing though Tailwind classes and `postcss.config.js` exist | `platform/web/` |
| E1 | `GetDeviceEnrollmentInfo` returns `nil, nil, nil` | `enrollment/service.go:169-171` |
| E2 | `updateDevicesInGroup` is an empty body | `telemetry/telemetry.go:126-127` |
| E3 | Enrollment tokens have no HTTP surface | absent |
| F1 | `libs/hbb_common/src/config.rs` is patched **uncommitted** to add `option_env!` for `RENDEZVOUS_SERVERS`/`RS_PUB_KEY`/`APP_NAME` | `git -C libs/hbb_common status` |
| F2 | CI checks out `submodules: recursive`, which restores upstream and **silently discards F1** | `.github/workflows/odv-*.yml:32-33` |
| F3 | No workflow sets `RENDEZVOUS_SERVERS`, `RS_PUB_KEY` or `APP_NAME` | same |
| F4 | Android workflow signs with `-keystore /dev/null -storepass changeit` | `odv-android.yml:111-113` |
| F5 | `flutter/lib/main.dart` `applyPreConfig()` is an uncommitted local change | `git diff` |
| G1 | Seven working files in the repo root | `git status` |
| G2 | `platform/casbin/model.conf` unused; `CasbinResolver` contains no Casbin | `internal/access/` |

**F1 combined with F2 is the most dangerous item in this table.** A CI build today
produces clients that point at `rs-ny.rustdesk.com` with RustDesk's public key,
because the checkout wipes the local patch and the `unwrap_or` defaults take over.
That is a silent, shipping-a-wrong-binary failure, not a build error. Fix it first
in stream F.

---

## 3. The complete client API surface

This is source (1) from §0, and it is the closed definition of Pro compatibility.
Obtained by sweeping every URL construction in `flutter/lib/` and `src/`. There is
no path outside this table.

| # | Path | Method | Caller | Status |
|---|------|--------|--------|--------|
| 1 | `/api/login-options` | GET | `user_model.dart` | done |
| 2 | `/api/login` | POST | `user_model.dart`, `src/hbbs_http/account.rs` | done |
| 3 | `/api/logout` | POST | `user_model.dart` | done |
| 4 | `/api/currentUser` | POST | `user_model.dart` | done |
| 5 | `/api/oidc/auth` | POST | `account.rs` | done |
| 6 | `/api/oidc/auth-query` | GET | `account.rs` | done |
| 7 | `/api/heartbeat` | POST | `sync.rs:284` | done |
| 8 | `/api/sysinfo` | POST | `sync.rs:210` | done |
| 9 | `/api/sysinfo_ver` | POST | `sync.rs:192` | done |
| 10 | `/api/peers` | GET | `group_model.dart:226` | done, paginates correctly |
| 11 | `/api/users` | GET | `group_model.dart:161` | **B4: ignores pagination** |
| 12 | `/api/device-group/accessible` | GET | `group_model.dart:105` | **B4: ignores pagination** |
| 13-24 | the twelve `/api/ab/*` | mixed | `ab_model.dart` | done |
| 25 | `/api/ab` | POST/GET | `ab_model.dart:1010,1064` | deliberate 404 (legacy mode) |
| 26 | `/api/audit/conn` | POST | `ui_session_interface.rs:594` | **B2: must return a guid** |
| 27 | `/api/audit/file` | POST | `common.rs:1123` | done |
| 28 | `PUT /api/audit` | PUT | `dialog.dart:1773` | **B1: missing** |
| 29 | `/api/devices/deploy` | POST | `ui_interface.rs:1102` | **B3** |
| 30 | `/api/devices/cli` | POST | `core_main.rs:630` | **B3** |
| 31 | `/api/record` | POST | `record_upload.rs:100` | **B3** |
| 32 | `/api/switch-grant` | POST | `sync.rs:333` | **B3** |
| 33 | `/lic/web/api/plugin-sign` | POST | `plugin/callback_msg.rs:283` | **B3** |

Note that #33 is under `/lic/web/api/`, not `/api/`. Any allowlist or proxy rule
that assumes a single `/api` prefix will miss it.

### 3.1 Re-deriving this table, and proving the list is still closed

This is the mechanism that stops a seventh plan. Anyone can regenerate the client
surface in one command and diff it against the table above. If the diff is empty,
there is no unknown API work, and that is a fact rather than an assurance.

```bash
cd /home/lebbi/OpenDeskViewer
{
  grep -rho '/api/[a-zA-Z0-9_/{}$.-]*' flutter/lib/ --include=*.dart \
    | sed 's/\${[a-zA-Z.]*}/{guid}/g'
  grep -rho '/api/[a-zA-Z0-9_/-]*' src/ --include=*.rs
} | sort -u
```

Two cautions learned from running it:

- The Rust sweep also catches MSDN documentation URLs in comments
  (`/api/winuser/...`, `/api/dxgiformat/...` and so on). Those are not endpoints.
  Filter them or eyeball them; do not implement them.
- Some paths are assembled rather than written literally:
  `src/hbbs_http/sync.rs:192,210` build sysinfo by `url.replace("heartbeat",
  "sysinfo")`, and `src/common.rs:1123` builds `/api/audit/{typ}`. A pure grep for
  the literal string finds neither. **Grep for the construction, not the result.**
  This is exactly how #26 and #28 were missed by earlier reviews.

Add this as a CI job (`grep sweep | diff - docs/client-api-surface.txt`) so a
RustDesk upstream merge that introduces a new endpoint fails the build instead of
being discovered months later. That single job is what converts "we think we are
done" into "we are told when we stop being done."

The same closure argument applies to the other two sources:

- **Schema:** `\dt` against a migrated database, diffed against the features in
  §7. Every table must be reachable by a page or endpoint, or be dropped.
- **Deployment:** every service in `docker-compose.yml`, every route block in
  `Caddyfile`, every workflow in `.github/workflows/`. All three are short enough
  to read in full, and §4 and §9 already enumerate them.

---

## 4. Stream A: deployment correctness

Goal: D1, D8. No code changes to Go. Do this stream first, because every later
stream is verified by running the stack.

### A.1 Fix the hbbs and hbbr commands

`platform/docker-compose.yml`.

- `hbbs`: the correct form is `hbbs -r <relay-host>:<relay-port> -k <key>`, where
  the relay is **hbbr**, not the rendezvous port. Change to
  `hbbs -r hbbr:21117 -k _` inside the compose network, or
  `hbbs -r ${PUBLIC_HOST}:${RELAY_PORT:-21117} -k _` if clients must reach the
  relay by public name (they must; use the public form).
- `hbbr`: drop `-r` entirely. Use `hbbr -k _`.
- `-k _` tells the server to accept only clients presenting the key it generates
  on first boot, and to publish that key. Read the generated public key out of the
  `id_ed25519.pub` file in the hbbs data volume; that value is what
  `RUSTDESK_PUBLIC_KEY` in `.env` and `RS_PUB_KEY` in the client build must be set
  to. Document that ordering explicitly: **the key exists only after hbbs has
  booted once**, so a first deployment is: bring up hbbs, read the key, put it in
  `.env`, then build clients.
- Publish `21115/tcp`, `21116/tcp`, `21116/udp`, `21117/tcp`, `21118/tcp`,
  `21119/tcp`. Verify each against the RustDesk server documentation for the
  version pinned in the compose file; do not guess.

**Acceptance:** `docker compose up -d`, then `docker compose logs hbbs` shows it
registering the relay and no argument errors; `docker compose logs hbbr` shows a
clean start.

### A.2 Stop masking health at the proxy

`platform/Caddyfile:20-22` intercepts `/healthz` and `/readyz` and answers 200
without consulting the API, so the proxy reports healthy while the API is down.
Delete the `@health` matcher and its `respond` block, and let both paths fall
through to `reverse_proxy http://api:8000`.

**Acceptance:** stop the API container; `curl -sS -o /dev/null -w '%{http_code}'
https://<host>/readyz` returns 502 or 503, not 200.

### A.3 Fix the route ordering

`Caddyfile:7` matches `/api*`, which also matches `/api/v1*` at `:13`. Caddy
sorts by specificity, so this is currently harmless, but it becomes a live bug the
moment the two need different directives (they will, in stream D: `/api/v1*`
needs the portal's CORS and a different rate limit). Make the ordering explicit
now: put the `/api/v1*` block first and give the `/api*` block a
`not path /api/v1*` guard.

Also add a route for `/lic/web/api/*` (path #33) pointing at the API, or a
deliberate 404, so it does not fall through to the web SPA and return `index.html`
with a 200, which the client would try to parse as JSON.

**Acceptance:** `curl -i https://<host>/lic/web/api/plugin-sign` returns JSON or a
JSON 404, never HTML.

### A.4 Embed a migration runner

Today the schema is applied only by Postgres's initdb, which runs **only on an
empty data volume**. An existing deployment will never receive `00002` or any
future migration. That is not shippable.

- Add `github.com/golang-migrate/migrate/v4` with the `pgx` and `iofs` drivers.
- `go:embed migrations/*.sql` into a new `platform/api/internal/migrations`
  package. This requires the SQL files to live inside the module, so **move**
  `platform/migrations/*.up.sql` to `platform/api/internal/migrations/` and update
  the compose mount (remove it; initdb no longer needs them).
- Rename to golang-migrate's convention: `000001_initial_schema.up.sql`,
  `000002_address_book.up.sql`, plus matching `.down.sql` in the same directory.
- Run `migrate.Up()` in `cmd/api/main.go` after the pool is created and before
  the router is built. Fail fast on error.
- Guard with an advisory lock so two API replicas cannot migrate concurrently:
  golang-migrate's postgres driver does this already; confirm it is enabled.
- Add `ODV_MIGRATE=false` to skip, for operators who apply migrations out of band.

**Acceptance:** start against a database that already has `00001` only; the API
applies `00002` and logs it. Start again; it applies nothing and logs "no change".

### A.5 Decide and close the 21114 question

`get_api_server_` (`src/common.rs:1064-1083`), read directly. The resolution order
is, in full:

1. On Windows only, `lic.api` from the filename license
   (`get_license_from_exe_name()`). This **outranks everything**, which is why
   filename provisioning in F.4 works without a rebuild.
2. The `api-server` option, if set.
3. Otherwise derived from the custom rendezvous server by
   `increase_port(&s0, -2)`, giving `http://<rendezvous-host>:21114`
   (`RENDEZVOUS_PORT - 2`). Plain HTTP, hardcoded in the `format!`.
4. Otherwise `https://admin.rustdesk.com`.

So a client told only the rendezvous server looks for the API on **21114 over
plain HTTP**, and nothing listens there. Worse, a client told nothing at all falls
through to RustDesk's hosted service.

Do **both**, as the original plan recommended:

1. Publish the API on 21114. Step 3 above builds an `http://` URL with no TLS
   negotiation, so this listener must be **plain HTTP**, not HTTPS. Add a
   dedicated Caddy site on `:21114` that proxies only `/api/*` to `api:8000` and
   returns 404 for everything else, so the plain-HTTP surface is as small as
   possible. Document that this port carries bearer tokens in clear text and
   should be restricted to the management network, or accepted as a deliberate
   risk for clients that cannot be pre-configured.
2. Set `api-server` explicitly in every pre-configured client (stream F), which
   takes precedence at step 2 and avoids the plain-HTTP path entirely. This is the
   preferred route; item 1 exists only for stock clients.

Also correcting a claim carried in `plans/final-polish-handoff.md`, which said
heartbeat is disabled for `rustdesk.com` URLs. It is not: `is_public()`
(`src/common.rs:1085-1089`) is consumed by `get_audit_server`
(`src/common.rs:1118-1124`), which returns an empty string for public URLs, so it
is **audit posting** that is silently suppressed, not heartbeat. The practical
consequence is the same in one respect and different in another: a misconfigured
client still sends heartbeats, so the fleet looks healthy while no connection is
ever audited. Add an acceptance check that the deployed API URL does not end with
`rustdesk.com` and that a connection produces an audit row.

**Acceptance:** a client configured with **only** `custom-rendezvous-server` and
`key` reaches the API and completes a heartbeat.

### A.6 Remove dead infrastructure

- `platform/casbin/` and its compose mount: `CasbinResolver` contains no Casbin.
  Delete the directory, delete the mount, delete `CASBIN_MODEL_PATH` from config,
  and rename `CasbinResolver` to `SQLResolver` across `internal/access` and its
  callers. The `access.Resolver` interface stays as the extension point.
- Confirm Redis is absent from the compose file (it appears to have been removed
  already). If any config key for it survives, delete that too.

**Acceptance:** `grep -ri casbin platform/` returns nothing;
`go build ./...` clean.

---

## 5. Stream B: close the client API surface

Goal: D2, D6. Every row of §3 resolved.

### B.1 `PUT /api/audit` (path #28)

Not implemented, and not previously noticed by any review.

The client calls it from the connection dialog to attach an operator note to a
connection record: body `{"guid": "<audit guid>", "note": "<text>"}`.

- Add `HandleAuditNote` to `internal/rustdeskapi/audit.go`, registered on the
  **protected** mux as `PUT /api/audit`.
- It updates `connection_sessions.note` where the row's guid matches **and** the
  caller is allowed to see that row: the connecting user, or an admin/manager.
  Anything else is 403, not 404, so a technician cannot probe for guids.
- `connection_sessions` has no `note` column today. Add it in a new migration
  `000003_audit_note.up.sql`: `ALTER TABLE connection_sessions ADD COLUMN note
  TEXT NOT NULL DEFAULT ''`.
- Response: 200 with an empty body on success. The client only checks
  `statusCode == 200` (`dialog.dart:1786`), but match the address book convention.

### B.2 `/api/audit/conn` must return a guid (path #26)

The client stores an audit guid per session (`src/flutter_ffi.rs:2054`,
`src/ui_session_interface.rs:74`) and later sends it to `PUT /api/audit`. Our
handler returns `{"success": true}`, so the guid is always empty and B.1 can never
match a row.

- Read `src/ui_session_interface.rs:594` and the code around the `post_request`
  call that follows it, to determine the **exact** field name the client parses out
  of the response. Do not guess: the field name is the whole contract. Follow it
  through to `session_set_audit_guid`.
- Change `HandleAuditConn` to `INSERT ... RETURNING id` and return that id under
  the field name the client actually reads.
- Add a round-trip integration test: POST `/api/audit/conn`, take the guid from
  the response, PUT `/api/audit` with it, assert the note landed on the row.

### B.3 The five remaining Pro endpoints (paths #29-33)

For each, decide **implement** or **deliberate refusal**, and make the decision
explicit in code and in `openapi.yaml`. A deliberate refusal is a real answer: a
documented status with a JSON body, never an accidental 404.

- **`/api/devices/deploy`** (`ui_interface.rs:1102`) and **`/api/devices/cli`**
  (`core_main.rs:630`): these serve deployment scripts. **Implement.** They are
  directly useful for this product: return a shell/PowerShell script that installs
  the client pre-configured with this deployment's rendezvous server, key and API
  URL. Gate on an enrollment token (stream E). This is the natural home for the
  onboarding story.
- **`/api/record`** (`record_upload.rs:100`): session recording upload.
  **Refuse deliberately** with 501 and a body explaining recording is not enabled,
  unless recording is a requirement. It implies object storage, retention policy
  and a privacy review, none of which are in the product definition. Document the
  decision in `platform/README.md`.
- **`/api/switch-grant`** (`sync.rs:333`): approval for switching control sides.
  **Implement** as a policy endpoint: allow if the requesting user has
  `CanAccessDevice` on the target, deny otherwise. Small, and it closes a real
  authorisation hole (silence here means the client decides locally).
- **`/lic/web/api/plugin-sign`** (`plugin/callback_msg.rs:283`): plugin signature
  verification. **Refuse deliberately** with 501. Enabling third-party plugins in
  a managed fleet needs a signing story that is out of scope. Ensure stream A.3
  routes it so it returns JSON.

### B.4 `/api/users` and `/api/device-group/accessible` ignore pagination

Found by checking the verbs in §3 rather than assuming them. Both are `GET` with
`current` and `pageSize` query parameters, and the client loops
`while (current * pageSize < total)` with `pageSize = 100`
(`group_model.dart:150,215`).

Our handlers (`users.go:68`, `peers.go:107`) call
`httpx.WritePaginatedResponse(w, int64(len(data)), data)`: they return the **whole**
set and report `total` as the length of what they returned.

This is not broken, and it is worth being precise about why: because the response
contains everything, `total` happens to equal the real count, so the client
assembles the correct list. The costs are that the client issues `ceil(N/100)`
identical full fetches to satisfy its loop, and that the first person to add a real
`LIMIT` to either query without also fixing `total` will silently truncate the
list with no error anywhere.

- Apply `httpx.ParsePagination` and a real `LIMIT/OFFSET` in
  `identity.ListAllUsers`, `identity.ListUsersSharingSupportGroups` and
  `peers.ListDeviceGroups`, returning a true total, exactly as
  `peers.ListAccessiblePeers` already does.
- Switch both handlers to `httpx.WritePaginatedResponsePage`.
- Add an integration test with more than one page that asserts page 2 differs from
  page 1 and that the union is the whole set. The existing
  `TestAddressBookPeersHonoursQueryPagination` is the template.

**Acceptance for stream B:** a script that walks every row of §3 against a running
stack and asserts none returns an unintended 404, and that #25 returns exactly 404
because the client depends on that. Plus: with 150 seeded users, `/api/users`
serves two distinct pages and the client-side loop terminates after two requests.

---

## 6. Stream C: the audit service

Goal: the `audit_events` table stops being dead weight, and administrative actions
become accountable.

Create `platform/api/internal/audit`:

```go
type Recorder interface {
    Record(ctx context.Context, e Event) error
}

type Event struct {
    Type     string      // "device.reassigned", "user.role_granted", ...
    ActorID  int64
    Resource string      // "device", "user", "customer"
    ResourceID string
    Description string
    Metadata map[string]any
}
```

- Backed by `audit_events`, which is already shaped for this: `event_type`,
  `user_id`, `device_id`, `support_group_id`, `customer_id`, `description`,
  `metadata JSONB`, `created_at`. Verified; no migration needed.
- Recording must never fail the request it describes: log the error, return nil.
  An audit write failing a device reassignment is worse than a missing audit row.
  Make that trade explicit in a comment, and add a counter for dropped events.
- Wire `Record` into **every** mutation, which is a closed list once stream D
  exists: every handler in `internal/apiv1` that is not a GET, plus
  `/api/ab/*` writes, plus `PUT /api/audit`, plus enrollment token issue/revoke.
  Enforce with a review checklist item, and a test that walks the apiv1 route
  table and asserts every non-GET route's handler calls the recorder (achievable
  with a fake recorder and table-driven route exercise).
- Retention: the worker already has `AUDIT_RETENTION_DAYS`. Confirm the cleanup
  job actually deletes from `audit_events` and `connection_sessions`, and add a
  test.

**Acceptance:** reassign a device via the portal; an `audit_events` row exists with
the actor, both customer ids, and a timestamp.

---

## 7. Stream D: `internal/apiv1` and the portal

Goal: D5. This is the largest stream. It is closed by enumerating the pages, then
deriving endpoints from them.

### D.1 The portal's pages, exhaustively

This list is the scope. Nothing else ships in 1.0.

1. **Login.** Redirect to Keycloak, handle the callback, store the token, refresh
   it, and sign out. No local password form.
2. **Dashboard.** Device counts by state (DISCOVERED, ACTIVE, STALE, OFFLINE),
   online now, customer count, technician count, connections in the last 24h,
   and a list of the ten most recent connections.
3. **Devices.** Paginated, searchable by name and hostname (the `pg_trgm` GIN
   indexes exist for exactly this), filterable by state, customer, and device
   group. Row actions: claim a DISCOVERED device, reassign to another
   customer/location, rename, add to a device group, and connect.
4. **Device detail.** Sysinfo, connectivity history, the connections involving it,
   and its group memberships.
5. **Customers.** CRUD, with their locations as a nested list.
6. **Device groups.** CRUD, plus membership editing.
7. **Support groups.** CRUD, plus technician membership and the device groups the
   support group can reach. This screen is where the authorisation model is
   actually administered, so it is not optional.
8. **Users.** List, assign and revoke roles, activate and deactivate. Deactivation
   must visibly take effect (§ the middleware rejects disabled users).
9. **Enrollment tokens.** Issue with a max-use count and an expiry, list with
   remaining uses, and revoke. Show the install command for a token once, at
   creation, and never again.
10. **Audit log.** Connection sessions and audit events, filterable by actor,
    device and date range, with the note from B.1 editable.
11. **Settings.** Address book peer cap, audit retention, device stale/offline
    thresholds. Read-only display is acceptable for 1.0 if the values come from
    config; if they are editable, they need a config table and a reload path, so
    prefer read-only and say so.

### D.2 The `/api/v1` surface derived from those pages

Under `platform/api/internal/apiv1`, all on the protected mux, all returning the
same `{total, data}` envelope as the Pro surface for lists.

```
GET    /api/v1/stats/dashboard
GET    /api/v1/devices                 ?q=&state=&customer=&group=&current=&pageSize=
GET    /api/v1/devices/{id}
PATCH  /api/v1/devices/{id}            name, customer_id, location_id
POST   /api/v1/devices/{id}/claim
POST   /api/v1/devices/{id}/reassign   transactional, see D.3
POST   /api/v1/devices/{id}/connect    returns rustdesk://connect/<id>?password=<pw>
DELETE /api/v1/devices/{id}
GET    /api/v1/devices/{id}/sessions
GET    /api/v1/customers               POST, GET/{id}, PATCH/{id}, DELETE/{id}
GET    /api/v1/customers/{id}/locations POST, PATCH, DELETE
GET    /api/v1/device-groups           POST, GET/{id}, PATCH/{id}, DELETE/{id}
POST   /api/v1/device-groups/{id}/members      DELETE /{deviceId}
GET    /api/v1/support-groups          POST, GET/{id}, PATCH/{id}, DELETE/{id}
POST   /api/v1/support-groups/{id}/technicians DELETE /{userId}
POST   /api/v1/support-groups/{id}/device-groups DELETE /{groupId}
GET    /api/v1/users                   GET/{id}, PATCH/{id} (active), 
POST   /api/v1/users/{id}/roles        DELETE /api/v1/users/{id}/roles/{role}
GET    /api/v1/enrollment-tokens       POST, DELETE/{id}
GET    /api/v1/audit/events            ?actor=&resource=&from=&to=
GET    /api/v1/audit/sessions          ?device=&user=&from=&to=
GET    /api/v1/settings
```

Every one of these is admin/manager-gated except the device list and connect,
which are scoped by `access.Resolver` exactly as `/api/peers` is. Reuse the
resolver; do not write a second authorisation path.

### D.3 Two operations that need care

**Reassignment** must be transactional and must revoke the old customer's access
atomically: update `devices.customer_id` and `location_id`, remove device group
memberships that belonged to the old customer, add the new ones, and write the
audit event, all in one transaction. There is an existing acceptance criterion for
this in the original plan ("reassignment revoking the old customer's access") and
it needs a test with two technicians in different support groups.

**Connect** returns `rustdesk://connect/<id>?password=<pw>`
(`flutter/lib/common.dart:2482-2500`, `src/core_main.rs:816-885`). Decide where the
password comes from: either the device's stored one-time password or a generated
temporary credential. If generated, it needs a lifetime and a store. Do not return
a permanent password in a URL that lands in browser history; prefer a short-lived
token and document the choice.

### D.4 The portal implementation

- Add the missing `tailwind.config.js` with the content globs for `src/**/*.tsx`.
  Tailwind classes are used today and none of them can be working.
- Router: `react-router-dom`. Auth: `oidc-client-ts` or `react-oidc-context`
  against Keycloak, PKCE, no client secret in the browser.
- Data: TanStack Query against `/api/v1`, with a typed client generated from
  `openapi.yaml` (`openapi-typescript`) so the portal and the API cannot drift.
  This is the reason to keep `openapi.yaml` accurate.
- Tests: Vitest plus Testing Library. At minimum, one test per page asserting it
  renders real fetched data, and one test that an unauthenticated visit redirects
  to login.
- Extend `openapi.yaml` with every `/api/v1` path above. The spec is currently
  Pro-only.

**Acceptance for stream D:** every page in D.1 loads real data from a running
stack; no component contains a hardcoded metric; the generated client compiles.

---

## 8. Stream E: fleet and enrollment completeness

Goal: the device lifecycle in D2 actually works end to end.

- **E1.** `GetDeviceEnrollmentInfo` (`enrollment/service.go:169-171`) returns
  `nil, nil, nil`. Implement it: given a customer, resolve the default location and
  device group a newly enrolled device should land in. If the product has no
  concept of a default, delete the function and its callers rather than leaving a
  stub that silently succeeds.
- **E2.** `updateDevicesInGroup` (`telemetry/telemetry.go:126-127`) has an empty
  body. Same rule: implement or delete. Find its callers first; an empty function
  called on every heartbeat is either a missing feature or dead code.
- **E3.** Enrollment tokens have no HTTP surface. Add the endpoints in D.2, and
  the device-side redemption path: a device presenting a token in its first
  `/api/sysinfo` gets created as ACTIVE and assigned, rather than DISCOVERED.
  `ConsumeToken` is already atomic; wire it.
- **E4.** ~~`RevokeToken` hard-deletes.~~ **Already done.** Verified:
  `enrollment_tokens.revoked_at` exists in `00001` and
  `enrollment/service.go:131-137` sets it with an `UPDATE`. No work. Retained here
  only so the next reader does not re-derive it from the stale finding in
  `plans/abundant-imagining-tower.md` §43.
- **E5.** Verify the state machine end to end with a test that drives it through
  time: `DISCOVERED` on first sysinfo, invisible to technicians, `ACTIVE` when
  claimed, `ONLINE` on heartbeat, `STALE` after `DEVICE_STALE_AFTER_SECONDS`,
  `OFFLINE` after `DEVICE_OFFLINE_AFTER_SECONDS`. Inject a clock rather than
  sleeping.

---

## 9. Stream F: the clients

Goal: D4. **Start with F.1; it is the highest-severity item in this plan.**

### F.1 Make the hbb_common fork real and committed

Today `libs/hbb_common/src/config.rs` carries an uncommitted three-line patch that
introduces `option_env!("RENDEZVOUS_SERVERS")`, `option_env!("RS_PUB_KEY")` and an
`APP_NAME` default of `OpenDeskViewer`. Every workflow does `submodules:
recursive`, which restores the upstream file. The `unwrap_or` defaults then apply,
so **CI produces clients that talk to `rs-ny.rustdesk.com` using RustDesk's public
key, with no build error.**

Fix properly:

1. Fork `hbb_common` to an organisation repository, commit the patch there with a
   clear message, and repoint `.gitmodules` at the fork with a pinned commit.
2. Alternatively, if a fork is unacceptable, delete the patch and pre-configure
   exclusively at runtime (F.4), so no build depends on a modified dependency.
   Choose one. Do not leave the current state, which is the worst of both.
3. Add a CI guard that **fails the build** if `RS_PUB_KEY` resolves empty or if
   `RENDEZVOUS_SERVERS` still contains `rustdesk.com`. A build that would produce
   a misconfigured client must not produce an artifact.

Because this ships modified RustDesk binaries, the AGPL obligation applies:
publish the corresponding source of the fork. Get that signed off before release.

### F.2 Wire the build-time configuration

In `.github/workflows/odv-{android,linux,windows}.yml`, set as build env:
`RENDEZVOUS_SERVERS`, `RS_PUB_KEY`, `API_SERVER`, `APP_NAME`, sourced from
repository secrets/variables, and assert non-empty before the build step.

### F.3 Real signing

`odv-android.yml:111-113` signs with `-keystore /dev/null -storepass changeit`.
That artifact cannot be distributed or upgraded in place. Add a real keystore from
secrets, and fail the build if the secret is absent on a release run. Do the same
for Windows code signing if a certificate exists; if not, document that the
installer will be unsigned and what that means for SmartScreen.

### F.4 Runtime pre-configuration, committed

`flutter/lib/main.dart` has an uncommitted `applyPreConfig()` reading
`--id-server=`, `--api-server=`, `--relay-server=` and `--key=` from boot args.
Commit it, and add:

- A test that the parser handles each flag and ignores unknown ones.
- Documentation of the three mechanisms and when each applies:
  1. build-time (F.2), for the CI-built clients;
  2. Windows filename provisioning, `…host=<h>,key=<k>.exe`
     (`src/custom_server.rs:39-108`, `src/platform/windows.rs:2084`), which needs
     no rebuild at all and is the best answer for ad-hoc installs;
  3. boot args (this item) and `rustdesk --config "host=…,key=…"`
     (`src/core_main.rs:503-528`), noting it requires an installed client and root.

### F.5 Rebranding

`platform/android/rebrand.sh` covers Android only. Move to
`platform/clients/rebrand.sh`, extend to Linux and Windows packaging metadata,
make it idempotent, and add `--revert`. Have CI run it and then run
`git diff --exit-code` after a `--revert` to prove idempotency.

### F.6 Prove the workflows

None of the three workflows has ever run. Trigger each via `workflow_dispatch`,
fix until green, and add the resulting artifacts to a release. A workflow that has
never executed is not a deliverable.

---

## 10. Stream G: hygiene, docs, and CI

- **G1.** Delete the seven root files (`COMPLETION_CHECKLIST.md`,
  `IMPLEMENTATION_SUMMARY.md`, `INSTALL.md`, `README_PLATFORM.md`, `install.sh`,
  `install_and_verify.sh`, `verify_structure.sh`), folding anything still true
  into `platform/README.md`. `CLAUDE.md` forbids working files in the root.
- **G2.** `platform/README.md` becomes the single operator document: prerequisites,
  `.env` reference with every variable the code actually reads (derive it from
  `internal/config/config.go`, do not write it from memory), the first-boot key
  ordering from A.1, how to build clients, and how to run the tests.
- **G3.** CI for the platform, which does not exist today: a workflow running
  `go build`, `go vet`, `go test ./...`, then the integration suite against a
  `services: postgres` container with `ODV_TEST_DB=1`, then the portal's
  `npm ci && npm run build && npm test`. This is what stops regressions between
  now and 1.0.
- **G4.** Add a `docker compose config` check and a Trivy or `govulncheck` scan to
  the same workflow.

---

## 11. Explicitly out of scope for 1.0

Recorded here so nobody discovers them later and calls the project unfinished.
Each is a deliberate decision, not an oversight.

- Session recording (`/api/record`) and plugin signing (`/lic/web/api/plugin-sign`):
  refused with 501 per B.3.
- Two-factor authentication beyond what Keycloak provides. Keycloak owns
  authentication; the API validates tokens. `/api/2fa` is a Pro path the client
  never calls in the flows we support.
- Multi-tenancy beyond the customer entity. One deployment serves one
  organisation's fleet.
- High availability, read replicas, and horizontal scaling of the API. The
  migration advisory lock in A.4 is the only concession to more than one replica.
- Mobile portal. The React app targets desktop browsers.
- The legacy `/api/ab` address book (path #25). The client's legacy mode is a
  fallback for old servers; we are not an old server.

---

## 12. Order of execution

Dependencies are real; this order respects them.

1. **F.1 alone, first.** It is a shipping-wrong-binaries bug and takes an hour.
   Everything else can wait behind it if necessary.
2. **Stream A** (deployment). Nothing downstream can be verified without a stack
   that comes up correctly. A.4 in particular unblocks every later migration.
3. **Stream G3** (CI) immediately after A, so the rest of the work is protected.
4. **Stream B** and **Stream E** in parallel. They share the telemetry and audit
   tables but not the code paths.
5. **Stream C** (audit service), which stream D depends on for its mutations.
6. **Stream D**, the largest. D.2 (apiv1) first, then D.4 (portal) against it.
   These can be two people, with `openapi.yaml` as the contract between them.
7. **Stream F** remainder (F.2 to F.6), which is independent of 2 to 6 and can run
   alongside once F.1 is settled.
8. **Stream G** remainder.

---

## 13. The acceptance run

This is the single gate. Run it top to bottom on a clean machine. Every line must
pass. When it does, the project is done and further work is bug fixing.

**Build and test**

1. `go build ./...`, `go vet ./...` clean.
2. `go test ./...` green.
3. `ODV_TEST_DB=1 go test ./internal/integration/` green.
4. `npm ci && npm run build && npm test` green in `platform/web`.
5. CI runs 1 to 4 on a push and reports green.
5a. The §3.1 client-surface sweep diffs clean against
    `docs/client-api-surface.txt`, and CI fails if it does not. This is the check
    that keeps the project finished after it is finished.

**Deployment**

6. Fresh clone, fill `.env`, `docker compose up -d`. All services healthy with no
   manual intervention.
7. Stop the API; `/readyz` through the proxy reports unhealthy (A.2).
8. External port scan from another machine shows only 80, 443, 21114 if chosen,
   and 21115-21119.
9. Restart against an existing volume; migrations apply incrementally (A.4).

**Pro surface**

10. A script walks all 33 rows of §3: none returns an unintended 404; #25 returns
    exactly 404.
11. Simulated device: `POST /api/sysinfo` then `/api/heartbeat` lands as
    DISCOVERED, is invisible to technicians, becomes ACTIVE and ONLINE when
    claimed, then walks STALE and OFFLINE when heartbeats stop.
12. `/api/audit/conn` returns a guid; `PUT /api/audit` with that guid attaches a
    note; the note is visible in the portal's audit log.

**Real clients**

13. A stock RustDesk desktop client configured with only rendezvous server and
    key finds the API and completes a heartbeat (A.5).
14. Technician signs in: the address book populates with exactly the authorised
    devices, and no others.
15. Administrator signs in: the whole fleet appears, plus the "All Devices" book.
16. A disabled user is rejected at sign-in and on every subsequent request.
17. End-to-end connect to a real Android device succeeds; a `connection_sessions`
    row exists and the client's own `/api/audit/conn` post landed.

**Pre-configured clients**

18. CI builds Android, Linux and Windows artifacts, all green, all signed.
19. The CI guard fails a build with an empty `RS_PUB_KEY` (F.1.3). Verify by
    running it deliberately.
20. Each artifact installs and registers with zero configuration (D4).
21. `rebrand.sh --revert` leaves the tree byte-identical (F.5).

**Portal**

22. Every page in D.1 loads real data; `grep -rn "0" src/App.tsx` finds no
    hardcoded metric.
23. An unauthenticated visit redirects to Keycloak; sign-out invalidates.
24. A device reassignment writes an `audit_events` row and removes the previous
    technician's access, verified by that technician's address book no longer
    listing the device.

**Hygiene**

25. Repo root contains no working files (G1).
26. `grep -ri casbin platform/` returns nothing (A.6).
27. `git -C libs/hbb_common status --short` is clean, and `.gitmodules` points at
    a pinned commit of a repository that contains the patch (F.1).
28. `platform/README.md` `.env` table matches `internal/config/config.go` exactly.

---

## 14. Honest sizing

- Stream A: 1 day. A.4 is most of it.
- Stream B: 2 days. B.2 needs careful reading of the Rust client; B.4 is an hour.
- Stream C: half a day, plus the wiring cost inside stream D.
- Stream D: 5 to 8 days. The portal is the bulk of the remaining project.
- Stream E: 1 to 2 days, depending on whether E1/E2 are implemented or deleted.
- Stream F: 2 to 3 days, most of it waiting on CI feedback loops.
- Stream G: 1 day.

Roughly two to three focused weeks. The acceptance run in §13 is another day, and
it will find things; that is what it is for.

## 15. Reference

- Contract truth: `flutter/lib/common/hbbs/hbbs.dart`,
  `flutter/lib/models/{ab,group,user}_model.dart`, `src/hbbs_http/sync.rs`,
  `src/hbbs_http/account.rs`.
- Client API URL derivation: `get_api_server_`, `src/common.rs:1064-1083`. Audit
  posting (not heartbeat) is suppressed for public URLs via `is_public` at
  `:1085-1089`, consumed by `get_audit_server` at `:1118-1124`.
- Connect URI: `flutter/lib/common.dart:2482-2500`, `src/core_main.rs:816-885`.
- Windows filename provisioning: `src/custom_server.rs:39-108`,
  `src/platform/windows.rs:2084`.
- Audit guid round trip: `src/ui_session_interface.rs:594`,
  `src/flutter_ffi.rs:2054-2062`, `flutter/lib/common/widgets/dialog.dart:1765`.
- Prior plans, retained for history: `plans/abundant-imagining-tower.md` (original
  review), `plans/final-polish-handoff.md` (auth and address book pass).
