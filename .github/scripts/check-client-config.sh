#!/bin/bash
# CI guard to ensure Rust clients are properly configured
# This script checks that the hbb_common submodule is at upstream (no local patches)
# and that required environment variables are set for runtime pre-configuration

set -e

echo "=== CI Guard: Client Configuration Check ==="

# Check hbb_common submodule status
cd /home/lebbi/OpenDeskViewer
SUBMODULE_STATUS=$(git submodule status libs/hbb_common | cut -c1)

if [ "$SUBMODULE_STATUS" = "-" ] || [ "$SUBMODULE_STATUS" = "+" ]; then
    echo "ERROR: hbb_common submodule has local modifications"
    echo "Please do not patch hbb_common. Use runtime pre-configuration instead."
    echo "See F.4 in plans/finish-line.md for runtime configuration options."
    exit 1
else
    echo "✓ hbb_common submodule is at upstream (no local patches)"
fi

# Check required environment variables
MISSING_VARS=0

if [ -z "$RS_PUB_KEY" ]; then
    echo "ERROR: RS_PUB_KEY is not set"
    MISSING_VARS=$((MISSING_VARS + 1))
else
    echo "✓ RS_PUB_KEY is set"
fi

if [ -z "$RENDEZVOUS_SERVERS" ]; then
    echo "ERROR: RENDEZVOUS_SERVERS is not set"
    MISSING_VARS=$((MISSING_VARS + 1))
else
    echo "✓ RENDEZVOUS_SERVERS is set"
    # Check that it doesn't contain rustdesk.com
    if echo "$RENDEZVOUS_SERVERS" | grep -q "rustdesk.com"; then
        echo "ERROR: RENDEZVOUS_SERVERS should not contain rustdesk.com"
        MISSING_VARS=$((MISSING_VARS + 1))
    else
        echo "✓ RENDEZVOUS_SERVERS does not reference rustdesk.com"
    fi
fi

if [ -z "$API_SERVER" ]; then
    echo "ERROR: API_SERVER is not set"
    MISSING_VARS=$((MISSING_VARS + 1))
else
    echo "✓ API_SERVER is set"
fi

if [ -z "$APP_NAME" ]; then
    echo "ERROR: APP_NAME is not set"
    MISSING_VARS=$((MISSING_VARS + 1))
else
    echo "✓ APP_NAME is set"
fi

if [ $MISSING_VARS -gt 0 ]; then
    echo ""
    echo "ERROR: $MISSING_VARS required environment variable(s) are missing or invalid."
    echo "Please set RS_PUB_KEY, RENDEZVOUS_SERVERS, API_SERVER, and APP_NAME"
    echo "in your CI build environment."
    exit 1
fi

echo ""
echo "=== All CI guard checks passed ==="
echo "Clients will be properly pre-configured at runtime."
