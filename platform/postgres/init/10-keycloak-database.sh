#!/bin/bash
# Creates the role and database Keycloak connects to.
#
# The compose file points Keycloak at postgres://<KEYCLOAK_DB_USER>@postgres/<KEYCLOAK_DB_NAME>,
# but nothing created either, so Keycloak could never start. Postgres runs the
# scripts in /docker-entrypoint-initdb.d exactly once, on an empty data
# directory. An existing deployment has to create the role and database by hand.
#
# Keycloak owns its own schema and must not share the application's role: the
# application's role is narrowed further in Phase 3.5.

set -euo pipefail

KEYCLOAK_DB_NAME="${KEYCLOAK_DB_NAME:-keycloak}"
KEYCLOAK_DB_USER="${KEYCLOAK_DB_USER:-keycloak}"

if [ -z "${KEYCLOAK_DB_PASSWORD:-}" ]; then
    echo "KEYCLOAK_DB_PASSWORD is not set; refusing to create the Keycloak role" >&2
    exit 1
fi

# Passed out of band so the password never appears in a SQL string or in the
# statement log.
psql -v ON_ERROR_STOP=1 \
     --username "$POSTGRES_USER" \
     --dbname "$POSTGRES_DB" \
     -v kc_user="$KEYCLOAK_DB_USER" \
     -v kc_password="$KEYCLOAK_DB_PASSWORD" \
     -v kc_db="$KEYCLOAK_DB_NAME" <<-'EOSQL'
	CREATE ROLE :"kc_user" WITH LOGIN PASSWORD :'kc_password';
	CREATE DATABASE :"kc_db" OWNER :"kc_user";
	REVOKE ALL ON DATABASE :"kc_db" FROM PUBLIC;
	GRANT ALL PRIVILEGES ON DATABASE :"kc_db" TO :"kc_user";
EOSQL

echo "Created Keycloak database '$KEYCLOAK_DB_NAME' owned by role '$KEYCLOAK_DB_USER'"
