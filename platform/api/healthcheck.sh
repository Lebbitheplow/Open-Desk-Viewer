#!/bin/sh

# Health check script for OpenDeskViewer API

# Check database connectivity
echo "Checking database connectivity..."
if pg_isready -h postgres -p 5432 -U opendeskviewer > /dev/null 2>&1; then
    echo "Database: OK"
else
    echo "Database: FAILED"
    exit 1
fi

# Check API endpoint
echo "Checking API endpoint..."
if wget -q -O - http://localhost:8000/healthz > /dev/null 2>&1; then
    echo "API: OK"
else
    echo "API: FAILED"
    exit 1
fi

echo "All checks passed!"
exit 0
