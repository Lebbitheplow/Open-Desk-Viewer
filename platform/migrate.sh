#!/bin/bash
# Migration script for OpenDeskViewer
# Usage: ./migrate.sh [up|down|version]
# Set ODV_MIGRATE=false to skip migration during API start

set -e

if [ "$ODV_MIGRATE" = "false" ]; then
    echo "Skipping migration (ODV_MIGRATE=false)"
    exit 0
fi

cd "$(dirname "$0")/.."

if [ $# -eq 0 ]; then
    echo "Usage: $0 [up|down|version]"
    exit 1
fi

ACTION=$1
shift

# Database connection
PGHOST=${POSTGRES_HOST:-localhost}
PGPORT=${POSTGRES_PORT:-5432}
PGDB=${POSTGRES_DB:-opendeskviewer}
PGUSER=${POSTGRES_USER:-opendeskviewer}
PGPASSWORD=${POSTGRES_PASSWORD:-opendeskviewer}

# Construct connection string
CONN_STR="postgresql://${PGUSER}:${PGPASSWORD}@${PGHOST}:${PGPORT}/${PGDB}"

# Run migration
echo "Running migrate $ACTION on $CONN_STR"
migrate -database "$CONN_STR" -path migrations $ACTION "$@"
