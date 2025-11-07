#!/bin/bash

# Omnara Fly.io Secrets Configuration Script
# Interactively prompts for and sets all required secrets

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🔐 Omnara Fly.io Secrets Setup${NC}"
echo -e "${BLUE}==============================${NC}\n"

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

# Function to set secret for an app
set_secret() {
    local app=$1
    local key=$2
    local value=$3

    if [ -z "$value" ]; then
        echo -e "${YELLOW}Skipping ${key} (empty value)${NC}"
        return
    fi

    echo -e "${BLUE}Setting ${key} for ${app}...${NC}"
    echo "$value" | flyctl secrets set -a "$app" "${key}=-" > /dev/null 2>&1
    echo -e "${GREEN}✓ ${key} set${NC}"
}

# Function to prompt for input
prompt() {
    local prompt_text=$1
    local default=$2
    local is_secret=${3:-false}

    if [ "$is_secret" = true ]; then
        read -s -p "$prompt_text" value
        echo ""
    else
        read -p "$prompt_text" value
    fi

    if [ -z "$value" ] && [ -n "$default" ]; then
        value=$default
    fi

    echo "$value"
}

echo -e "${YELLOW}This script will help you configure secrets for all three Omnara services.${NC}"
echo -e "${YELLOW}Press Enter to skip optional values.${NC}\n"

# ====================================================================
# REQUIRED SECRETS
# ====================================================================

echo -e "${BLUE}=== Required Secrets ===${NC}\n"

# Supabase PostgreSQL URL
echo -e "${YELLOW}1. Supabase PostgreSQL Connection String${NC}"
echo -e "   Get from: Supabase Dashboard → Project Settings → Database → Connection String"
echo -e "   Format: postgresql://postgres.[PROJECT-REF]:[PASSWORD]@aws-0-[REGION].pooler.supabase.com:6543/postgres"
PRODUCTION_DB_URL=$(prompt "   Enter PRODUCTION_DB_URL: " "" false)

# Supabase Auth Credentials
echo -e "\n${YELLOW}2. Supabase Authentication Credentials${NC}"
echo -e "   Get from: Supabase Dashboard → Project Settings → API"
SUPABASE_URL=$(prompt "   Enter SUPABASE_URL: " "" false)
SUPABASE_ANON_KEY=$(prompt "   Enter SUPABASE_ANON_KEY: " "" true)
SUPABASE_SERVICE_ROLE_KEY=$(prompt "   Enter SUPABASE_SERVICE_ROLE_KEY: " "" true)

# JWT Keys
echo -e "\n${YELLOW}3. JWT Keys for Agent Authentication${NC}"
echo -e "   Generate with: python infrastructure/scripts/generate_jwt_keys.py"
echo -e "   ${RED}Important: Copy the ENTIRE key including -----BEGIN/END----- lines${NC}"
echo -e "   Press Enter, then paste the key, then press Enter twice:"

echo "   JWT_PRIVATE_KEY (paste and press Enter twice):"
JWT_PRIVATE_KEY=$(sed '/^$/q')

echo "   JWT_PUBLIC_KEY (paste and press Enter twice):"
JWT_PUBLIC_KEY=$(sed '/^$/q')

# Frontend URLs
echo -e "\n${YELLOW}4. Frontend URLs for CORS${NC}"
echo -e "   Enter as JSON array, e.g., [\"https://yourdomain.com\", \"http://localhost:3000\"]"
FRONTEND_URLS=$(prompt "   Enter FRONTEND_URLS: " '["https://omnara-backend.fly.dev"]' false)

# ====================================================================
# OPTIONAL SECRETS
# ====================================================================

echo -e "\n${BLUE}=== Optional Secrets (press Enter to skip) ===${NC}\n"

# Sentry
echo -e "${YELLOW}5. Sentry Error Tracking (optional)${NC}"
SENTRY_DSN=$(prompt "   Enter SENTRY_DSN: " "" false)

# Stripe Billing
echo -e "\n${YELLOW}6. Stripe Billing (optional)${NC}"
STRIPE_SECRET_KEY=$(prompt "   Enter STRIPE_SECRET_KEY: " "" true)
STRIPE_WEBHOOK_SECRET=$(prompt "   Enter STRIPE_WEBHOOK_SECRET: " "" true)

# Twilio Notifications
echo -e "\n${YELLOW}7. Twilio Notifications (optional)${NC}"
TWILIO_ACCOUNT_SID=$(prompt "   Enter TWILIO_ACCOUNT_SID: " "" false)
TWILIO_AUTH_TOKEN=$(prompt "   Enter TWILIO_AUTH_TOKEN: " "" true)
TWILIO_FROM_PHONE_NUMBER=$(prompt "   Enter TWILIO_FROM_PHONE_NUMBER: " "" false)
TWILIO_SENDGRID_API_KEY=$(prompt "   Enter TWILIO_SENDGRID_API_KEY: " "" true)
TWILIO_FROM_EMAIL=$(prompt "   Enter TWILIO_FROM_EMAIL: " "" false)

# Anthropic API
echo -e "\n${YELLOW}8. Anthropic API (optional)${NC}"
ANTHROPIC_API_KEY=$(prompt "   Enter ANTHROPIC_API_KEY: " "" true)

# ====================================================================
# SET SECRETS
# ====================================================================

echo -e "\n${BLUE}Setting secrets for Omnara services...${NC}\n"

# Backend API secrets
echo -e "${BLUE}=== Backend API (omnara-backend) ===${NC}"
set_secret "omnara-backend" "PRODUCTION_DB_URL" "$PRODUCTION_DB_URL"
set_secret "omnara-backend" "SUPABASE_URL" "$SUPABASE_URL"
set_secret "omnara-backend" "SUPABASE_ANON_KEY" "$SUPABASE_ANON_KEY"
set_secret "omnara-backend" "SUPABASE_SERVICE_ROLE_KEY" "$SUPABASE_SERVICE_ROLE_KEY"
set_secret "omnara-backend" "JWT_PRIVATE_KEY" "$JWT_PRIVATE_KEY"
set_secret "omnara-backend" "JWT_PUBLIC_KEY" "$JWT_PUBLIC_KEY"
set_secret "omnara-backend" "FRONTEND_URLS" "$FRONTEND_URLS"
set_secret "omnara-backend" "SENTRY_DSN" "$SENTRY_DSN"
set_secret "omnara-backend" "STRIPE_SECRET_KEY" "$STRIPE_SECRET_KEY"
set_secret "omnara-backend" "STRIPE_WEBHOOK_SECRET" "$STRIPE_WEBHOOK_SECRET"
set_secret "omnara-backend" "TWILIO_ACCOUNT_SID" "$TWILIO_ACCOUNT_SID"
set_secret "omnara-backend" "TWILIO_AUTH_TOKEN" "$TWILIO_AUTH_TOKEN"
set_secret "omnara-backend" "TWILIO_FROM_PHONE_NUMBER" "$TWILIO_FROM_PHONE_NUMBER"

# Unified Server secrets
echo -e "\n${BLUE}=== Unified Server (omnara-servers) ===${NC}"
set_secret "omnara-servers" "PRODUCTION_DB_URL" "$PRODUCTION_DB_URL"
set_secret "omnara-servers" "JWT_PUBLIC_KEY" "$JWT_PUBLIC_KEY"
set_secret "omnara-servers" "SENTRY_DSN" "$SENTRY_DSN"
set_secret "omnara-servers" "ANTHROPIC_API_KEY" "$ANTHROPIC_API_KEY"
set_secret "omnara-servers" "TWILIO_ACCOUNT_SID" "$TWILIO_ACCOUNT_SID"
set_secret "omnara-servers" "TWILIO_AUTH_TOKEN" "$TWILIO_AUTH_TOKEN"
set_secret "omnara-servers" "TWILIO_SENDGRID_API_KEY" "$TWILIO_SENDGRID_API_KEY"
set_secret "omnara-servers" "TWILIO_FROM_EMAIL" "$TWILIO_FROM_EMAIL"

# Relay Server secrets
echo -e "\n${BLUE}=== Relay Server (omnara-relay) ===${NC}"
RELAY_ORIGINS="https://omnara-backend.fly.dev,https://omnara-servers.fly.dev"
set_secret "omnara-relay" "OMNARA_RELAY_ALLOWED_ORIGINS" "$RELAY_ORIGINS"
set_secret "omnara-relay" "JWT_PUBLIC_KEY" "$JWT_PUBLIC_KEY"
set_secret "omnara-relay" "SUPABASE_URL" "$SUPABASE_URL"
set_secret "omnara-relay" "SUPABASE_ANON_KEY" "$SUPABASE_ANON_KEY"

echo -e "\n${GREEN}✓ All secrets configured!${NC}\n"
echo -e "${BLUE}Next steps:${NC}"
echo -e "  1. Run database migrations: ./infrastructure/scripts/deploy-to-fly.sh --run-migrations"
echo -e "  2. Deploy services: ./infrastructure/scripts/deploy-to-fly.sh"
echo -e "  3. Test health endpoints"
echo -e ""
echo -e "${YELLOW}View secrets with:${NC}"
echo -e "  flyctl secrets list -a omnara-backend"
echo -e "  flyctl secrets list -a omnara-servers"
echo -e "  flyctl secrets list -a omnara-relay"
