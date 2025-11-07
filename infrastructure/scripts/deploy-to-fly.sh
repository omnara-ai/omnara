#!/bin/bash

# Omnara Fly.io Deployment Script
# Automates deployment of all three services to Fly.io

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🚀 Omnara Fly.io Deployment Script${NC}"
echo -e "${BLUE}=====================================${NC}\n"

# Check if flyctl is installed
if ! command -v flyctl &> /dev/null; then
    echo -e "${RED}Error: flyctl is not installed${NC}"
    echo "Install it with: curl -L https://fly.io/install.sh | sh"
    exit 1
fi

# Check if user is authenticated
if ! flyctl auth whoami &> /dev/null; then
    echo -e "${RED}Error: Not authenticated with Fly.io${NC}"
    echo "Run: flyctl auth login"
    exit 1
fi

# Parse command line arguments
RUN_MIGRATIONS=false
SKIP_BUILD=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --run-migrations)
            RUN_MIGRATIONS=true
            shift
            ;;
        --skip-build)
            SKIP_BUILD=true
            shift
            ;;
        *)
            echo "Unknown option: $1"
            echo "Usage: $0 [--run-migrations] [--skip-build]"
            exit 1
            ;;
    esac
done

# Function to check if app exists
app_exists() {
    flyctl apps list 2>/dev/null | grep -q "^$1"
}

# Function to deploy a service
deploy_service() {
    local app_name=$1
    local config_file=$2
    local service_name=$3

    echo -e "\n${BLUE}Deploying ${service_name}...${NC}"

    if ! app_exists "$app_name"; then
        echo -e "${YELLOW}App ${app_name} doesn't exist. Creating...${NC}"
        flyctl apps create "$app_name"
    fi

    if [ "$SKIP_BUILD" = true ]; then
        echo -e "${YELLOW}Skipping build for ${app_name}${NC}"
    else
        flyctl deploy -a "$app_name" --config "$config_file" --ha=false
    fi

    echo -e "${GREEN}✓ ${service_name} deployed${NC}"
}

# Function to check service health
check_health() {
    local app_name=$1
    local service_name=$2

    echo -e "\n${BLUE}Checking ${service_name} health...${NC}"

    local url=$(flyctl status -a "$app_name" --json 2>/dev/null | python3 -c "import sys, json; print(json.load(sys.stdin).get('Hostname', ''))" 2>/dev/null || echo "")

    if [ -z "$url" ]; then
        echo -e "${YELLOW}Warning: Could not get URL for ${app_name}${NC}"
        return
    fi

    if curl -sf "https://${url}/health" > /dev/null; then
        echo -e "${GREEN}✓ ${service_name} is healthy${NC}"
    else
        echo -e "${RED}✗ ${service_name} health check failed${NC}"
    fi
}

# Step 1: Run migrations if requested
if [ "$RUN_MIGRATIONS" = true ]; then
    echo -e "\n${BLUE}Running database migrations...${NC}"

    if [ -z "$PRODUCTION_DB_URL" ]; then
        echo -e "${RED}Error: PRODUCTION_DB_URL environment variable not set${NC}"
        echo "Export it with: export PRODUCTION_DB_URL='postgresql://...'"
        exit 1
    fi

    echo -e "${YELLOW}Running Alembic migrations...${NC}"
    cd src/shared
    export ENVIRONMENT=production
    alembic upgrade head
    cd ../..
    echo -e "${GREEN}✓ Migrations completed${NC}"
fi

# Step 2: Deploy services in order
echo -e "\n${BLUE}Step 1: Deploying Backend API${NC}"
deploy_service "omnara-backend" "fly.backend.toml" "Backend API"

echo -e "\n${BLUE}Step 2: Deploying Unified Server${NC}"
deploy_service "omnara-servers" "fly.servers.toml" "Unified Server"

echo -e "\n${BLUE}Step 3: Deploying Relay Server${NC}"
deploy_service "omnara-relay" "fly.relay.toml" "Relay Server"

# Step 3: Wait for services to stabilize
echo -e "\n${YELLOW}Waiting 30 seconds for services to stabilize...${NC}"
sleep 30

# Step 4: Health checks
echo -e "\n${BLUE}Running health checks...${NC}"
check_health "omnara-backend" "Backend API"
check_health "omnara-servers" "Unified Server"
check_health "omnara-relay" "Relay Server"

# Step 5: Display URLs
echo -e "\n${GREEN}🎉 Deployment complete!${NC}\n"
echo -e "${BLUE}Service URLs:${NC}"
echo -e "  Backend API:    https://omnara-backend.fly.dev"
echo -e "  Unified Server: https://omnara-servers.fly.dev"
echo -e "  Relay Server:   https://omnara-relay.fly.dev"
echo -e ""
echo -e "${BLUE}Next steps:${NC}"
echo -e "  1. Test health endpoints (above URLs + /health)"
echo -e "  2. Configure web dashboard with these URLs"
echo -e "  3. Create test user in Supabase"
echo -e "  4. Test agent authentication"
echo -e ""
echo -e "${YELLOW}Monitor logs with:${NC}"
echo -e "  flyctl logs -a omnara-backend"
echo -e "  flyctl logs -a omnara-servers"
echo -e "  flyctl logs -a omnara-relay"
