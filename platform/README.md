# OpenDeskViewer

Self-hosted RustDesk management platform for internal remote support.

## Overview

OpenDeskViewer is a community-driven, self-hosted alternative to TeamViewer for internal remote support. It's built on top of RustDesk OSS (`hbbs`/`hbbr`) and provides:

- Technician identity management via Keycloak
- Device inventory tied to customers
- Authorization rules for who may connect to what
- Comprehensive audit trails
- Web portal for device management

**Important:** OpenDeskViewer does NOT fork RustDesk core components. The RustDesk client (`hbbs`, `hbbr`) remains unmodified. We add a Go API server that speaks the RustDesk Pro API protocol, enabling the unmodified RustDesk client to work as both the technician console and managed-device agent.

## Architecture

```
Keycloak ──OIDC──> Go Management API ──> PostgreSQL
                        │  │
                        │  └── Casbin (RBAC) + relationship resolver
                        │
     ┌──────────────────┼──────────────────┐
     │                  │                  │
React portal      RustDesk client     Android devices
(/api/v1/*)       (/api/* Pro-compat) (/api/heartbeat, /api/sysinfo)
                        │
                  hbbs / hbbr (upstream OSS, unmodified)
```

**Two HTTP surfaces:**

- `/api/*` — **RustDesk Pro compatibility**. Shape is dictated by the client: snake_case, `{"error": "..."}` on failure
- `/api/v1/*` — **our own REST**, for the React portal and integrations. RFC 7807 problem+json, OpenAPI-first

## Quick Start

### Prerequisites

- Docker and Docker Compose
- A public hostname or IP address

### Installation

```bash
# 1. Clone the repository
git clone https://github.com/Lebbitheplow/Open-Desk-Viewer.git
cd Open-Desk-Viewer

# 2. Initialize Rust submodule (required for building)
git submodule update --init --recursive

# 3. Set up environment
cp platform/.env.example platform/.env

# Edit platform/.env with your values:
# - PUBLIC_HOST: Your public hostname/IP
# - JWT_SECRET: 64+ character random string
# - Keycloak credentials
# - PostgreSQL credentials

# 4. Start the stack
cd platform
docker compose up -d

# 5. Generate RustDesk public key
# Wait for hbbs to start, then:
docker compose exec hbbs cat /data/id_ed25519.pub

# Copy the public key and update RUSTDESK_PUBLIC_KEY in .env
# Restart the API:
docker compose up -d --force-recreate api

# 6. Create the bootstrap administrator in Keycloak.
# The admin console is deliberately not routed through Caddy, so reach it over a
# loopback-only port: see "Setup" in the root README for the compose override.
# Create a user whose email is exactly API_BOOTSTRAP_ADMIN_EMAIL; it gets the
# Administrator role on first sign-in. Every account after that is created from
# the portal's Users screen.
```

The root [README](../README.md#setup) carries the same path in full, including issuing the first
enrollment token and building a client. This section is the short form.

### Default Credentials (Change Immediately!)

**Keycloak:**
- Admin: `admin` / `admin`
- Realm: `opendeskviewer`

**PostgreSQL:**
- User: `opendeskviewer`
- Password: (set in .env)

## Directory Structure

```
platform/
├── README.md           # This file
├── .env.example        # Environment variable template
├── docker-compose.yml  # Service definitions
├── Caddyfile           # Reverse proxy configuration
├── keycloak/
│   └── realm-opendeskviewer.json  # Keycloak realm configuration
├── migrations/
│   └── 00001_initial_schema.sql   # Database migrations
│   └── 00002_address_book.sql     # Address book schema
├── api/
│   ├── go.mod          # Go module definition
│   ├── Dockerfile      # API server Dockerfile
│   ├── cmd/
│   │   ├── api/main.go # Main API server
│   │   └── worker/main.go # Background worker
│   └── internal/
│       ├── config/     # Configuration management
│       ├── postgres/   # Database connection pool
│       ├── httpx/      # HTTP router and middleware
│       ├── identity/   # User authentication
│       ├── fleet/      # Device fleet management
│       ├── access/     # Authorization resolver
│       ├── enrollment/ # Device enrollment tokens
│       ├── telemetry/  # Heartbeat and sysinfo
│       ├── audit/      # Audit trail service
│       ├── rustdeskapi/ # RustDesk Pro API handlers
│       └── apiv1/      # Our REST API handlers
└── web/                # React frontend (separate repository)
    └── ...
```

## Configuration

### Environment Variables

See `platform/.env.example` for all available options.

**Required.** The API validates these at startup and refuses to run without them, which is
deliberate: every one of them fails in a way that is much harder to diagnose later than at boot.

- `PUBLIC_HOST`: the public hostname or IP. Caddy provisions a certificate for it and Keycloak
  stamps it into every token's issuer claim
- `JWT_SECRET`: 64 characters or more
- `DEVICE_PASSWORD_KEY`: 32 random bytes, base64. `openssl rand -base64 32`. This encrypts every
  managed device's connection password, so **back it up with the database credentials**: losing it
  means the fleet's passwords cannot be read and the devices have to be re-enrolled
- `POSTGRES_PASSWORD`: the schema owner, used for migrations and by the worker
- `POSTGRES_APP_PASSWORD`: `odv_app`, the non-owning role the API serves requests as. See
  "The two database roles" below
- `KEYCLOAK_ADMIN_PASSWORD` and `KEYCLOAK_DB_PASSWORD`
- `OIDC_ISSUER`: the **public** realm URL. It must end in `/realms/<realm>`, and the API checks
  that at startup, because pointing it at the internal address rejects every token a browser
  presents

**RustDesk:**
- `RENDEZVOUS_PORT`: RustDesk rendezvous port (default: 21116)
- `RELAY_PORT`: RustDesk relay port (default: 21117)
- `RUSTDESK_PUBLIC_KEY`: Generated from hbbs (see above)

### Keycloak Setup

After initial startup:

1. Visit `http://your-host:8080`
2. Log in with admin/admin
3. Create the bootstrap administrator: a user whose email matches `API_BOOTSTRAP_ADMIN_EMAIL`,
   which is the one account that gets the Administrator role on its first sign-in

Everybody after that is created from the portal's Users screen, which is the point of the
following section. The Keycloak console is not routed through Caddy, so needing it for routine
account management would mean container-level access for an ordinary task.

### Accounts: who may create them, and what removal means

`POST /api/v1/users` creates the Keycloak account and the portal row together and returns a
**temporary password once**, in that response. It is stored nowhere and cannot be read again: the
account carries Keycloak's `UPDATE_PASSWORD` required action, so the value is spent the first time
the person signs in. The administrator hands it over themselves. There is deliberately no
invitation email, because this deployment has no mail server and an invitation that silently goes
nowhere is worse than a password read aloud.

This needs the API's Keycloak service account to be able to manage users. The bundled realm export
grants it:

```json
"users": [{
  "username": "service-account-odv-api",
  "serviceAccountClientId": "odv-api",
  "clientRoles": { "realm-management": ["manage-users", "view-users"] }
}]
```

A realm created before this was added needs the grant applying by hand: Clients → odv-api →
Service account roles → Assign role → filter by clients → `realm-management manage-users` and
`view-users`. Without it, and without `KEYCLOAK_CLIENT_SECRET`, the two routes answer 503 naming
what is missing rather than half-creating a user. Nothing else stops working.

**Removal removes both halves, and refuses if it cannot.** `DELETE /api/v1/users/{id}` deletes the
Keycloak account as well as the portal row. A local-only delete is not a removal: the person signs
in again, the middleware provisions a fresh row, and they come back as a Technician.

Two consequences worth stating:

- **Removal and deactivation both rotate device passwords.** A manager who was shown a device's
  connection password still holds a working credential after their account is gone, because the
  device goes on accepting it. Both operations rotate every device the user could reach, in force
  at each device's next heartbeat. This is the same mechanism that runs when a technician is
  removed from a support group.
- **The audit trail outlives the account.** `audit_events` has no foreign key to `users`, so what
  somebody did survives their removal. That is what the log is for.

**The last administrator cannot be removed, deactivated or demoted.** The guard covers all three
routes, because a deployment with no administrator has no way back through the portal. Grant
Administrator to somebody else first.

A machine identity -- a Keycloak service account, which is how an external system authenticates
against `/api/v1` -- has its Keycloak side owned by its client rather than by this route, so
removing one deletes the portal row and its roles only.

### Signing in

**The portal** uses the browser authorization-code flow against Keycloak.

**The RustDesk client** signs in through `POST /api/oidc/auth`, which `GET /api/login-options`
advertises as a single "Continue with SSO" option. Password sign-in is not offered and the
`user_credentials` table was dropped in migration 000012: nothing could ever write a credential to
it, so the username and password fields in the client's dialog refused everybody. Identity lives
in Keycloak, which owns password policy, lockout and disablement; a second credential store here
would have been governed by none of them.

## API Documentation

### RustDesk Pro API (`/api/*`)

Full specification in `api/openapi.yaml`. Key endpoints:

- `GET /api/login-options`: the sign-in options the client renders. One: single sign-on
- `POST /api/oidc/auth`: Start OIDC authorization
- `GET /api/oidc/auth-query`: Check authorization status
- `POST /api/login`: exchange a validated token for a session. A username and password are refused
- `POST /api/logout`: User logout
- `GET /api/currentUser`: Get current user info
- `GET /api/ab/settings`: Get address book settings
- `POST /api/heartbeat`: Device heartbeat
- `POST /api/sysinfo`: Device system info
- `GET /api/device-group/accessible`: Get accessible device groups

### API v1 (`/api/v1/*`)

The portal's own surface. `api/openapi.yaml` is the contract and is checked against the route
table by a test, so a route that is served and undocumented fails the build. Every route is listed
in `internal/apiv1/apiv1.go`; the groups are:

**Fleet.** Devices, customers, locations, device groups, support groups and users, with the usual
verbs. Every one is scoped through `access.Resolver`, the same resolver `/api/peers` uses.

**Reaching a device.**

- `POST /api/v1/devices/{id}/disconnect`: end live sessions, delivered at the next heartbeat
- `GET`/`PUT /api/v1/devices/{id}/strategy`: read and push client configuration
- `GET /api/v1/devices/{id}/password`: the platform-managed connection password. A technician with
  access; audited on every read
- `POST /api/v1/devices/{id}/password/rotate`: administrator. Also happens automatically when
  access is withdrawn

**Monitoring and notifications.**

- `GET /api/v1/devices/{id}/connectivity`: this device's up/down history
- `GET /api/v1/monitoring/events`: the same fleet-wide, filterable by state and time
- `GET`/`POST`/`DELETE /api/v1/notification-targets`: webhooks the platform posts events to.
  https only, optionally HMAC-SHA256 signed as `X-ODV-Signature`
- `GET /api/v1/notification-deliveries`: the outbox, including deliveries that were abandoned

**Reporting.** `GET /api/v1/reports/{report}` with `?format=csv` or JSON:

- `device-inventory`: what is managed, for whom, whether it is alive and whether it ever enrolled
- `session-history`: who connected to what, when and for how long
- `access-review`: who *can* reach what, derived from the same chain the API enforces

**Audit.** `GET /api/v1/audit/events` and `/api/v1/audit/sessions`.

## Database Schema

The migrations are embedded in the API binary and applied at startup unless `ODV_MIGRATE=false`.
The files are in `api/internal/migrations/`; `000001_initial_schema.up.sql` is the base and each
later one carries a comment explaining what it changes and why.

**Key Tables:**
- `users`: User accounts (Keycloak-backed)
- `roles`: System roles (Administrator, Support Manager, Technician, Read Only)
- `customers`: Customer entities
- `locations`: Device locations
- `devices`: Managed devices
- `device_groups`: Device groupings
- `support_groups`: Technician support groups
- `device_group_members`: Many-to-many device-group relationships
- `user_support_groups`: Many-to-many user-support group relationships
- `enrollment_tokens`: Device enrollment tokens
- `client_sessions`: Active client sessions
- `connection_sessions`: Remote connection logs
- `audit_events`: Audit trail

## Authorization Model

OpenDeskViewer uses a hierarchical RBAC model:

```
user → support_group → device_group → device
```

**Permission Chain:**
1. User is assigned to one or more support groups
2. Support groups have access to specific device groups
3. Device groups contain devices
4. Users can only access devices through their support groups' device group hierarchy

### Where authorization is enforced, and where it is not

This section is deliberately blunt, because the difference between what the
portal shows and what the network enforces is the thing an evaluator most needs
to understand.

**Enforced by this platform:**

- Every API request. A technician outside a device's support groups gets 403
  from the portal API, the address book, the peer list and the audit endpoints.
- Device identity. A device joins the fleet only by redeeming an enrollment
  token, and every heartbeat carries the per-device secret it received. An id
  with no valid credential is recorded as an observation and refused, not
  registered.
- The audit log. Entries are attributed to the authenticated caller, and
  `audit_events` is append-only at the database level twice over: a trigger
  refuses `UPDATE` and `DELETE`, and the role the API serves requests as,
  `odv_app`, is not granted them in the first place. `odv_app` owns nothing, so
  it cannot disable that trigger, and it holds no `TRUNCATE`, which is the
  operation that would otherwise slip past a row trigger entirely.
- The device's connection password. The platform generates it, keeps it
  encrypted with `DEVICE_PASSWORD_KEY`, delivers it to the device over the
  heartbeat, and releases it to a technician only after the same access check
  every other route uses, with an audit event naming who was shown it. Removing
  a technician from a support group, or revoking a device group from one,
  rotates the passwords of the devices that changed hands.

**Not enforced by this platform:**

- **The relay does not check authorization.** `hbbs` and `hbbr` are upstream
  RustDesk OSS and are deliberately unmodified. They connect whoever presents a
  valid RustDesk id and the deployment's key. A technician who knows a device's
  id and its connection password can reach it without this platform's consent,
  and the platform will not know until the client files its audit record.
  Authorization here is enforced at the device and credential layer, not at the
  relay.
- **Revocation is not instant.** Removing a technician's access, or ending a
  live session from the portal, is delivered on the device's next heartbeat.
  The client polls every 15 seconds, and it acknowledges nothing, so a
  disconnect that races a network drop is delivered to nobody. Treat revocation
  as "within a minute, if the device is online", not "now".
- **A rotated password is not in force until the device has it.** Rotation makes
  the platform's copy new immediately and reaches the machine on its next
  heartbeat. Until then the previous password still works there, and for a
  device that is switched off it works for as long as the device stays off. The
  portal reports this rather than hiding it: `applied` is false, and
  `applied_version` names the version the device last confirmed.
- **A password taken out of band stays out of band.** Rotation withdraws the
  credential the platform issued. It cannot withdraw a password a technician set
  at the device itself, or one a user of the machine chose, and a device whose
  strategy an administrator has changed away from `use-permanent-password` may
  accept a temporary password the platform never sees.
- **The deployment key is a perimeter, not an identity.** The client build ships
  the deployment's RustDesk key, so anyone who obtains one client has it. It
  keeps strangers off the rendezvous server; it does not authenticate a device.
- **The background worker connects as the schema owner.** Expiring audit rows
  past the retention period is a delete, and `odv_app` deliberately cannot do
  one, so the worker holds the owner credential. It listens on no port and reads
  no user input, which is what makes that an acceptable split; it is still a
  second process that could remove evidence if it were compromised.

### The two database roles

`POSTGRES_USER` owns the schema and is used for one thing: running migrations at
API startup, and by the worker. `odv_app` is what serves requests.

The bundled Postgres creates `odv_app` from
`platform/postgres/init/20-application-role.sh` the first time its data
directory is initialised, using `POSTGRES_APP_PASSWORD`. Migration `000008`
grants its privileges, so the privilege model is the same whether the database
is the bundled container or an external one.

An existing deployment, or an external database, needs the role created by hand
before the migration can grant to it:

```sql
CREATE ROLE odv_app WITH LOGIN PASSWORD 'the value of POSTGRES_APP_PASSWORD';
```

Then run the migrations as the owner; `000008` does the rest. If the owner has
no `CREATEROLE`, the migration says so in its output and leaves the grants
unapplied rather than failing the deployment. Leaving `POSTGRES_APP_PASSWORD`
empty makes the API fall back to connecting as the owner and warn at startup.

## Deploying the client

This is the contract between the platform and whoever builds the firmware image or configures the
MDM. It is a deliverable rather than a nicety: the fleet is preinstalled or pushed, with no
end-user setup, so everything a device needs has to arrive from one of the channels below. A
device that reaches none of them heartbeats, gets 401 forever, and lands in `device_observations`
instead of the fleet.

### 1. Provisioning: how a device joins

**Android, the primary platform: managed configuration.** The app declares its keys in
`res/xml/app_restrictions.xml`, which is the list an MDM console offers. They are the client's own
config keys, so what an administrator types is what the client stores:

| Key | Required | What it is |
|---|---|---|
| `enrollment-token` | **yes** | Issued by the portal, `POST /api/v1/enrollment-tokens`. Redeemed at first contact and then cleared, so a cloned image cannot enrol twice |
| `serial-number` | see §4 | The identifier a technician searches by |
| `api-server`, `custom-rendezvous-server`, `relay-server`, `key` | only for an unlocked build | Ignored by a build whose deployment is compiled in, which is what CI produces |

**A token's `max_uses` is a decision about the image.** One token baked into a firmware image is
one token every device off that line presents, so `max_uses` has to cover the production run, and
that token is a fleet-joining credential for as long as it has uses left. Prefer per-batch tokens
with a real expiry.

**Desktop and lab:** `--enrollment-token=<token>` at first run, or `rustdesk --option
enrollment-token <token>`, which is what the portal's generated deploy script emits.

### 2. Permissions the image must grant

None of these can be obtained from a customer who is not involved, and all of them are special
access rather than runtime dialogs. Grant them through the MDM policy or bake them into the system
image:

| Grant | Needed for | Without it |
|---|---|---|
| `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS` | starting on boot, staying alive | `BootReceiver` refuses to start and logs a warning naming both grants |
| `SYSTEM_ALERT_WINDOW` | same | as above |
| Accessibility service (`InputService`) enabled | remote **input control** | The technician sees the screen and cannot touch it |
| MediaProjection pre-granted, which needs the app installed as a **privileged/system app** or as a **device owner** | remote **screen capture** | The device enrols, reports in and is manageable; capture is unavailable, and the service logs that at warn on every boot |

**The MediaProjection point is the one to settle before the image is cut.** On stock Android there
is no way to obtain a capture token without a consent dialog, and this client deliberately does not
raise one on a boot start: nobody is there to answer it, and putting a system dialog in front of
the customer on every reboot is the opposite of what the deployment promises. So screen capture is
a property of the image. The two workable answers are a firmware preinstall as a privileged app,
or device-owner provisioning through the MDM. Pick one and record it here.

### 3. Start on boot

The client turns start-on-boot on **once**, for a build that knows its deployment, and records
that it has done so in `odv-start-on-boot-provisioned`. An operator who deliberately turns it back
off keeps that decision; this is provisioning, not policy. Nothing for the image to set, provided
the two grants in §2 are in place.

A service killed by the OS does not come back until the next boot (`START_NOT_STICKY`, kept
deliberately: a restarted service is handed no capture token, so it would look alive while being
useless). What notices is the platform: a device that stops heartbeating raises a connectivity
event, and `POST /api/v1/notification-targets` is where those go.

### 4. Which value carries the serial

`common.kt:deviceSerial` takes the first that is available:

1. **`serial-number` from managed configuration.** An asset tag the administrator sets. The most
   likely answer for the MDM path, and the only one under the operator's control.
2. **`Build.getSerial()`**, the hardware serial, readable only by a privileged or device-owner app
   since Android 10. The firmware-preinstall case.
3. **`ANDROID_ID`**, a stable per-device value that is *not* a manufacturer serial.

**Record which one this fleet uses, because it is what a technician types into the search box.**
The platform names devices `{customer}-{location}-{serial}` from it (`DEVICE_NAME_TEMPLATE`) and
searches it exactly through `GET /api/v1/devices?serial=`. A device that arrives with no serial is
named from its hostname instead, and is findable only by that.

### 5. Identity, signing and updates

- **The client is `com.opendeskviewer.client`**, labelled OpenDeskViewer. The `rustdesk://` deep
  link and the Kotlin package are deliberately unchanged; see `platform/android/README.md` for
  why.
- **One signing identity, decided before the first image.** An MDM cannot push an update whose
  signature changed, and a preinstalled app cannot be replaced by a differently signed one without
  uninstalling it, which takes the device's enrollment with it. CI signs from `ANDROID_SIGNING_KEY`
  and warns loudly when it is absent.
- **Updates** are pushed by the same channel that installed the app. The platform has no update
  mechanism of its own and does not want one: an app that can update itself outside the MDM is an
  app the MDM's signature check no longer protects.

### 6. What a deployed device may do to the fleet

Worth stating alongside the grants, because the image is trusted with it: a device's credential
authenticates that device only. It can heartbeat, post sysinfo, and receive the configuration and
connection password the portal issues it. It cannot read another device's password, enumerate the
fleet, or act as a user. Whoever holds the enrollment token can add devices to the customer and
device group named on the token, and nothing else.

## Development

### Building from Source

```bash
cd platform/api

# Install dependencies
go mod download

# Build
go build -o opendeskviewer ./cmd/api/main.go

# Run
./opendeskviewer
```

### Running Tests

```bash
go test ./...
```

### Code Style

- Follow Go best practices
- Use `gofmt` for formatting
- Write tests for all new functionality

## Deployment

### Production Checklist

1. Update `.env` with secure credentials
2. Generate strong `JWT_SECRET` (64+ characters)
3. Configure Keycloak with secure admin password
4. Set up TLS/HTTPS (Caddy can auto-provision Let's Encrypt)
5. Configure database backups
6. Review and enable audit logging
7. Set up monitoring and alerting

### Docker Compose Production

```bash
# Enable TLS in Caddyfile
# Update .env with production values
docker compose up -d --no-recreate
```

## Troubleshooting

### Common Issues

**API won't start:**
- Check database connectivity: `docker compose exec api psql`
- Verify `RUSTDESK_PUBLIC_KEY` is set correctly

**Devices not showing:**
- Verify heartbeat is being sent: `docker compose logs api`
- Check device state in database: `SELECT * FROM devices;`

**Keycloak not accessible:**
- Ensure port 8080 is not blocked
- Check Keycloak logs: `docker compose logs keycloak`

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Submit a pull request

## License

This project is licensed under the AGPL-3.0 License. See `LICENSE` for details.

**Note:** RustDesk is AGPL-licensed. Our platform (Go API, React portal) is separate from the RustDesk client binaries. The rebranded Android APK is a derived work of RustDesk and must comply with AGPL.

## Credits

- Built on [RustDesk](https://github.com/rustdesk/rustdesk) (MIT License)
- Uses [Keycloak](https://www.keycloak.org/) (Apache 2.0)
- Built with [Go](https://go.dev/) and [React](https://react.dev/)

## Security

- All passwords should be strong and unique
- Never commit `.env` files
- Rotate secrets regularly
- Keep dependencies updated

## Roadmap

- ✅ Phase 0: Contract and skeleton (this release)
- ✅ Phase 1: Identity and authorization
- ✅ Phase 2: Fleet domain
- ✅ Phase 3: RustDesk Pro compatibility
- 🔄 Phase 4: Worker and connection lifecycle
- 🔄 Phase 5: React portal
- 🔄 Phase 6: Rebranded Android APK

## Contact

For issues and feature requests, please use GitHub Issues.
