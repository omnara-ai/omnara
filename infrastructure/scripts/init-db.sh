#!/bin/bash

# Database initialization script using Alembic migrations
# This script will upgrade the database to the latest migration version

set -e

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${YELLOW}🗄️  Initializing database with Alembic migrations...${NC}"

# Change to the shared directory where alembic.ini is located
cd "$(dirname "$0")/../../src/shared"

# Run alembic upgrade head to apply all pending migrations
if alembic upgrade head; then
    echo -e "${GREEN}✅ Database successfully migrated to latest version${NC}"
else
    echo -e "${RED}❌ Error running Alembic migrations${NC}"
    exit 1
fi