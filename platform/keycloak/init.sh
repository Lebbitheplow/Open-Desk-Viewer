#!/bin/bash

# Keycloak initialization script
# Waits for Keycloak to be ready and imports the realm

KEYCLOAK_HOST="${KEYCLOAK_HOST:-keycloak}"
KEYCLOAK_PORT="${KEYCLOAK_PORT:-8080}"
KEYCLOAK_ADMIN_USER="${KEYCLOAK_ADMIN_USER:-admin}"
KEYCLOAK_ADMIN_PASSWORD="${KEYCLOAK_ADMIN_PASSWORD:-change_me_in_production}"

echo "Waiting for Keycloak to be ready..."

# Wait for Keycloak to be available
for i in {1..60}; do
    if curl -s "http://${KEYCLOAK_HOST}:${KEYCLOAK_PORT}/realms/master" > /dev/null 2>&1; then
        echo "Keycloak is ready!"
        break
    fi
    echo "Waiting for Keycloak... ($i/60)"
    sleep 2
done

# Import realm
echo "Importing OpenDeskViewer realm..."
TOKEN=$(curl -s -X POST \
    "http://${KEYCLOAK_HOST}:${KEYCLOAK_PORT}/realms/master/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "username=${KEYCLOAK_ADMIN_USER}" \
    -d "password=${KEYCLOAK_ADMIN_PASSWORD}" \
    -d "grant_type=password" \
    -d "client_id=admin-cli" | jq -r '.access_token')

if [ "$TOKEN" != "null" ] && [ -n "$TOKEN" ]; then
    curl -s -X POST \
        -H "Authorization: Bearer $TOKEN" \
        -H "Content-Type: application/json" \
        -d @/opt/keycloak/data/import/realm-opendeskviewer.json \
        "http://${KEYCLOAK_HOST}:${KEYCLOAK_PORT}/admin/realms"
    echo "Keycloak realm import completed"
else
    echo "Failed to obtain Keycloak admin token"
    exit 1
fi
