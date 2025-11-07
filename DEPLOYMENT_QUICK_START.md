# Omnara Fly.io Deployment - Quick Start

This quick guide gets you deploying to Fly.io in ~30 minutes.

## What Was Created

### Infrastructure Files
✅ `infrastructure/docker/relay.Dockerfile` - Docker image for relay server
✅ `fly.backend.toml` - Fly.io config for Backend API (port 8000)
✅ `fly.servers.toml` - Fly.io config for Unified Server (port 8080)
✅ `fly.relay.toml` - Fly.io config for Relay Server (port 8787)
✅ `.flyignore` - Build optimization

### Automation Scripts
✅ `infrastructure/scripts/deploy-to-fly.sh` - One-command deployment
✅ `infrastructure/scripts/setup-fly-secrets.sh` - Interactive secrets setup

### Documentation
✅ `docs/deployment/fly-io-setup.md` - Complete deployment guide (read this for details!)

## Prerequisites

1. **Fly.io CLI:**
   ```bash
   curl -L https://fly.io/install.sh | sh
   flyctl auth login
   ```

2. **Supabase Account:**
   - Create project at https://supabase.com/dashboard
   - Copy credentials (see full guide)

3. **JWT Keys:**
   ```bash
   python infrastructure/scripts/generate_jwt_keys.py
   ```

## 5-Step Deployment

### Step 1: Configure Secrets
```bash
./infrastructure/scripts/setup-fly-secrets.sh
```
This interactive script will prompt for all required credentials.

### Step 2: Run Migrations
```bash
export PRODUCTION_DB_URL="postgresql://..."  # From Supabase
export ENVIRONMENT=production
cd src/shared && alembic upgrade head && cd ../..
```

### Step 3: Deploy Services
```bash
./infrastructure/scripts/deploy-to-fly.sh
```

### Step 4: Verify Health
```bash
curl https://omnara-backend.fly.dev/health
curl https://omnara-servers.fly.dev/health
curl https://omnara-relay.fly.dev/health
```

### Step 5: Test Authentication
- Create test user in Supabase Dashboard
- Test login via web dashboard

## Your Service URLs

After deployment:
- **Backend API:** https://omnara-backend.fly.dev
- **Unified Server:** https://omnara-servers.fly.dev (MCP + REST)
- **Relay Server:** https://omnara-relay.fly.dev (WebSocket)

## Cost Estimate

- 3 services × 256MB = ~$10-15/month
- Supabase free tier = $0
- **Total: $10-15/month**

## Need Help?

Read the complete guide: `docs/deployment/fly-io-setup.md`

## Quick Commands

```bash
# View logs
flyctl logs -a omnara-backend

# Scale up
flyctl scale memory 512 -a omnara-backend

# Update code
git pull && ./infrastructure/scripts/deploy-to-fly.sh

# List secrets
flyctl secrets list -a omnara-backend
```

## Architecture Overview

```
Fly.io Apps (3 services)
├── omnara-backend (Web API, Supabase auth)
├── omnara-servers (MCP + REST, Agent communication)
└── omnara-relay (WebSocket terminal streaming)
     ↓
Supabase (Auth + PostgreSQL)
```

---

**Ready to deploy?** Start with Step 1 above!

For detailed troubleshooting, configuration options, and advanced features, see the complete guide at `docs/deployment/fly-io-setup.md`.
