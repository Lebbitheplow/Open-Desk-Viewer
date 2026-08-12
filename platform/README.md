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
git clone https://github.com/your-org/OpenDeskViewer.git
cd OpenDeskViewer

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

# 6. Create admin user
# Visit http://your-host:8080 in Keycloak, create a user with email matching API_BOOTSTRAP_ADMIN_EMAIL
```

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

**Required:**
- `PUBLIC_HOST`: Your public hostname or IP
- `JWT_SECRET`: 64+ character secret for JWT tokens
- `KEYCLOAK_ADMIN_PASSWORD`: Keycloak admin password
- `POSTGRES_PASSWORD`: PostgreSQL password

**RustDesk:**
- `RENDEZVOUS_PORT`: RustDesk rendezvous port (default: 21116)
- `RELAY_PORT`: RustDesk relay port (default: 21117)
- `RUSTDESK_PUBLIC_KEY`: Generated from hbbs (see above)

### Keycloak Setup

After initial startup:

1. Visit `http://your-host:8080`
2. Log in with admin/admin
3. Create users with emails matching your organization
4. Assign roles: Administrator, Support Manager, Technician, or Read Only

## API Documentation

### RustDesk Pro API (`/api/*`)

Full specification in `api/openapi.yaml`. Key endpoints:

- `GET /api/login-options`: Get available login methods
- `POST /api/oidc/auth`: Start OIDC authorization
- `GET /api/oidc/auth-query`: Check authorization status
- `POST /api/login`: User login
- `POST /api/logout`: User logout
- `GET /api/currentUser`: Get current user info
- `GET /api/ab/settings`: Get address book settings
- `POST /api/heartbeat`: Device heartbeat
- `POST /api/sysinfo`: Device system info
- `GET /api/device-group/accessible`: Get accessible device groups

### API v1 (`/api/v1/*`)

Our REST API (not yet implemented, see `api/openapi.yaml` for draft):

- `GET /api/v1/devices`: List all devices
- `GET /api/v1/devices/{id}`: Get device details
- `PATCH /api/v1/devices/{id}`: Update device
- `GET /api/v1/customers`: List customers
- `POST /api/v1/customers`: Create customer
- `POST /api/v1/devices/{id}/connect`: Initiate remote connection

## Database Schema

See `migrations/00001_initial_schema.sql` for the complete schema.

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
