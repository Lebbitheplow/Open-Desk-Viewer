#!/bin/bash
# Gives the API a database role that does not own the schema.
#
# The privilege model lives in migration 000008, not here: it has to be the same
# whether the database is the bundled container or an external one, and a shell
# script that only runs on an empty data directory cannot promise that. This
# script does the one thing a migration cannot, which is set a credential that
# must not live in a file tracked by git.
#
# Ordering matters and is the reason this works: PostgreSQL runs this directory
# once, on first initialisation, before anything connects, so the role exists by
# the time the API runs its migrations and 000008 finds it already present.
#
# An existing deployment adopting this has to create the role by hand; the two
# statements are in platform/README.md.

set -euo pipefail

# The name is fixed rather than configurable. Migration 000008 grants the
# privileges and has to name the role in plain SQL, so a configurable name here
# would produce a role with a login and no grants, which fails at the first
# query rather than at deployment.
APP_USER=odv_app

if [ -z "${POSTGRES_APP_PASSWORD:-}" ]; then
    echo "POSTGRES_APP_PASSWORD is not set; refusing to create the application role" >&2
    exit 1
fi

# Passed as psql variables so the password never appears in a SQL string or in
# the statement log, the same way the Keycloak role is created.
psql -v ON_ERROR_STOP=1 \
     --username "$POSTGRES_USER" \
     --dbname "$POSTGRES_DB" \
     -v app_user="$APP_USER" \
     -v app_password="$POSTGRES_APP_PASSWORD" <<-'EOSQL'
	CREATE ROLE :"app_user" WITH LOGIN PASSWORD :'app_password';
	-- CONNECT and the table grants come from migration 000008. Nothing is
	-- granted here on purpose: two places that both hand out privileges are two
	-- places that can disagree about what the runtime role may do.
EOSQL

echo "Created the application role '$APP_USER'; migration 000008 grants its privileges"
