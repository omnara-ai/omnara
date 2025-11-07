# Omnara Fly.io Deployment Guide

Complete guide for deploying Omnara on Fly.io with Supabase authentication.

## Overview

This deployment uses:
- **Fly.io** for hosting Python services (Backend API, Unified Server, Relay Server)
- **Supabase** for user authentication and PostgreSQL database
- **Fly.io default domains** (.fly.dev subdomains)
- **256MB instances** for cost-effective personal/small-scale deployment

**Estimated Monthly Cost:** $10-15/month + Supabase free tier

## Architecture

```
┌─────────────────────────────────────────┐
│             Fly.io Cloud                │
├─────────────────────────────────────────┤
│                                         │
│  ┌──────────────────────────────────┐  │
│  │ Backend API                      │  │
│  │ omnara-backend.fly.dev:8000      │  │
│  │ • Web dashboard API              │  │
│  │ • Supabase auth                  │  │
│  │ • Read operations                │  │
│  └──────────────────────────────────┘  │
│                                         │
│  ┌──────────────────────────────────┐  │
│  │ Unified Server                   │  │
│  │ omnara-servers.fly.dev:8080      │  │
│  │ • MCP protocol (/mcp/)           │  │
│  │ • REST API (/api/v1/)            │  │
│  │ • Write operations               │  │
│  └──────────────────────────────────┘  │
│                                         │
│  ┌──────────────────────────────────┐  │
│  │ Relay Server                     │  │
│  │ omnara-relay.fly.dev:8787        │  │
│  │ • WebSocket terminal relay       │  │
│  │ • In-memory sessions             │  │
│  └──────────────────────────────────┘  │
└─────────────────────────────────────────┘
                  ↓
┌─────────────────────────────────────────┐
│            Supabase Cloud               │
├─────────────────────────────────────────┤
│  • User authentication (auth.users)     │
│  • PostgreSQL database                  │
│  • Agent data storage                   │
└─────────────────────────────────────────┘
```

## Prerequisites

1. **Fly.io Account**
   - Sign up at https://fly.io
   - Install flyctl CLI: `curl -L https://fly.io/install.sh | sh`
   - Authenticate: `flyctl auth login`

2. **Supabase Account**
   - Sign up at https://supabase.com
   - Create a new project
   - Choose same region as your Fly.io deployment

3. **Local Tools**
   - Python 3.10+ (for running migrations)
   - Git (for cloning repository)

## Step 1: Clone Repository

```bash
git clone https://github.com/omnara-ai/omnara.git
cd omnara
```

## Step 2: Supabase Setup

### 2.1 Create Supabase Project

1. Go to https://supabase.com/dashboard
2. Click "New Project"
3. Choose organization
4. Set project name (e.g., "omnara-production")
5. Generate a strong database password
6. Select region (choose closest to your users)
7. Wait for provisioning (~2 minutes)

### 2.2 Collect Supabase Credentials

Navigate to Project Settings → API and copy:

- **API URL** (`SUPABASE_URL`)
  - Example: `https://abcdefghij.supabase.co`

- **Anon/Public Key** (`SUPABASE_ANON_KEY`)
  - Long JWT starting with `eyJ...`

- **Service Role Key** (`SUPABASE_SERVICE_ROLE_KEY`)
  - Long JWT starting with `eyJ...` (different from anon key)

Navigate to Project Settings → Database and copy:

- **Connection String** (`PRODUCTION_DB_URL`)
  - Transaction pooler mode recommended
  - Example: `postgresql://postgres.abcdefghij:[YOUR-PASSWORD]@aws-0-us-west-1.pooler.supabase.com:6543/postgres`

**Important:** Save these credentials securely. You'll need them in Step 4.

## Step 3: Generate JWT Keys

Generate RSA keys for agent authentication:

```bash
python infrastructure/scripts/generate_jwt_keys.py
```

This will output:
- `JWT_PRIVATE_KEY` - Keep this secret!
- `JWT_PUBLIC_KEY` - Also keep secret (weaker RSA implementation)

**Copy both keys including the `-----BEGIN/END-----` lines.**

Example output:
```
JWT_PRIVATE_KEY:
-----BEGIN PRIVATE KEY-----
MIIEvQIBADANBgkqhkiG9w0BAQEFAASC...
-----END PRIVATE KEY-----

JWT_PUBLIC_KEY:
-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A...
-----END PUBLIC KEY-----
```

## Step 4: Configure Fly.io Secrets

Run the interactive secrets configuration script:

```bash
./infrastructure/scripts/setup-fly-secrets.sh
```

This will prompt you for:

**Required:**
- Supabase PostgreSQL URL
- Supabase authentication credentials
- JWT keys (paste entire key including headers)
- Frontend URLs for CORS

**Optional (press Enter to skip):**
- Sentry DSN (error tracking)
- Stripe keys (billing)
- Twilio credentials (notifications)
- Anthropic API key (AI features)

The script will automatically configure all three Fly.io apps with appropriate secrets.

### Manual Secret Configuration

If you prefer manual configuration:

```bash
# Backend API
flyctl secrets set -a omnara-backend \
  PRODUCTION_DB_URL="postgresql://..." \
  SUPABASE_URL="https://..." \
  SUPABASE_ANON_KEY="eyJ..." \
  SUPABASE_SERVICE_ROLE_KEY="eyJ..." \
  JWT_PRIVATE_KEY="-----BEGIN PRIVATE KEY-----
..." \
  JWT_PUBLIC_KEY="-----BEGIN PUBLIC KEY-----
..." \
  FRONTEND_URLS='["https://omnara-backend.fly.dev"]'

# Unified Server
flyctl secrets set -a omnara-servers \
  PRODUCTION_DB_URL="postgresql://..." \
  JWT_PUBLIC_KEY="-----BEGIN PUBLIC KEY-----
..."

# Relay Server
flyctl secrets set -a omnara-relay \
  OMNARA_RELAY_ALLOWED_ORIGINS="https://omnara-backend.fly.dev"
```

## Step 5: Run Database Migrations

Initialize the database schema:

```bash
# Export database URL
export PRODUCTION_DB_URL="postgresql://..."
export ENVIRONMENT=production

# Run migrations
cd src/shared
alembic upgrade head
cd ../..
```

You should see output like:
```
INFO  [alembic.runtime.migration] Running upgrade -> e80f941f1bf8, initial_migration
INFO  [alembic.runtime.migration] Running upgrade e80f941f1bf8 -> 40d4252deb5b, add_messages_table
...
```

## Step 6: Deploy Services

### Option A: Automated Deployment (Recommended)

```bash
./infrastructure/scripts/deploy-to-fly.sh
```

This will:
1. Create apps if they don't exist
2. Deploy all three services
3. Wait for stabilization
4. Run health checks
5. Display service URLs

### Option B: Manual Deployment

```bash
# Create apps
flyctl apps create omnara-backend
flyctl apps create omnara-servers
flyctl apps create omnara-relay

# Deploy each service
flyctl deploy -a omnara-backend --config fly.backend.toml
flyctl deploy -a omnara-servers --config fly.servers.toml
flyctl deploy -a omnara-relay --config fly.relay.toml
```

**Deployment time:** ~5-10 minutes per service (first deploy builds Docker images)

## Step 7: Verify Deployment

### 7.1 Check Health Endpoints

```bash
# Backend API
curl https://omnara-backend.fly.dev/health
# Expected: {"status":"healthy"}

# Unified Server
curl https://omnara-servers.fly.dev/health
# Expected: {"status":"healthy","server":"unified"}

# Relay Server
curl https://omnara-relay.fly.dev/health
# Expected: {"status":"healthy","server":"relay"}
```

### 7.2 Check Service Logs

```bash
# Backend API logs
flyctl logs -a omnara-backend

# Unified Server logs
flyctl logs -a omnara-servers

# Relay Server logs
flyctl logs -a omnara-relay
```

### 7.3 Test Supabase Authentication

1. Go to Supabase Dashboard → Authentication → Users
2. Click "Add User" → "Create new user"
3. Enter email and password
4. Test login via your web dashboard (if deployed)

## Service URLs

After deployment, your services will be available at:

- **Backend API:** https://omnara-backend.fly.dev
  - Health: `/health`
  - Docs: `/docs` (Swagger UI)
  - API: `/api/v1/*`

- **Unified Server:** https://omnara-servers.fly.dev
  - Health: `/health`
  - MCP: `/mcp/`
  - REST API: `/api/v1/*`
  - Docs: `/docs`

- **Relay Server:** https://omnara-relay.fly.dev
  - Health: `/health`
  - Agent WebSocket: `/agent`
  - Viewer WebSocket: `/terminal`

## Configuration

### Scaling

Default configuration runs 1 instance per service (256MB RAM).

To scale up:

```bash
# Increase RAM
flyctl scale memory 512 -a omnara-backend

# Add more instances
flyctl scale count 2 -a omnara-backend

# Autoscaling (paid feature)
flyctl autoscale set -a omnara-backend min=1 max=3
```

### Regions

To add additional regions for better latency:

```bash
# Add region
flyctl regions add lax -a omnara-backend  # Los Angeles

# List regions
flyctl regions list -a omnara-backend

# Remove region
flyctl regions remove lax -a omnara-backend
```

### Custom Domain (Optional)

To use custom domain instead of .fly.dev:

```bash
# Add certificate
flyctl certs add api.yourdomain.com -a omnara-backend

# Add DNS records (from output)
# CNAME api.yourdomain.com -> omnara-backend.fly.dev

# Verify
flyctl certs show api.yourdomain.com -a omnara-backend
```

## Monitoring

### Fly.io Dashboard

View metrics at: https://fly.io/dashboard/[app-name]

- Request rate
- Response times
- CPU/Memory usage
- Error rates

### Logs

```bash
# Follow logs in real-time
flyctl logs -a omnara-backend

# Filter by instance
flyctl logs -a omnara-backend --instance [instance-id]

# Show more history
flyctl logs -a omnara-backend --max 500
```

### Alerts (Optional)

Set up error tracking with Sentry:

1. Create Sentry project at https://sentry.io
2. Copy DSN
3. Add secret: `flyctl secrets set SENTRY_DSN="https://..." -a omnara-backend`
4. Restart: `flyctl apps restart omnara-backend`

## Troubleshooting

### Service Won't Start

```bash
# Check logs
flyctl logs -a omnara-backend

# Common issues:
# - Missing secrets: flyctl secrets list -a omnara-backend
# - Database connection: Test PRODUCTION_DB_URL manually
# - Port conflicts: Check fly.toml internal_port matches service
```

### Database Connection Errors

```bash
# Test connection from local machine
psql "postgresql://..."

# Common issues:
# - Wrong password in connection string
# - Firewall blocking (Supabase should allow all IPs)
# - Using wrong connection mode (use Transaction pooler)
```

### Health Check Failures

```bash
# SSH into container
flyctl ssh console -a omnara-backend

# Test health endpoint internally
curl http://localhost:8000/health

# Check if service is listening
netstat -tlnp | grep 8000
```

### Migration Errors

```bash
# Check current migration version
cd src/shared
alembic current

# View migration history
alembic history

# Rollback one migration
alembic downgrade -1

# Re-run migrations
alembic upgrade head
```

## Cost Optimization

### For Personal Use (~$10-15/month)

Current configuration:
- 3 services × 256MB = ~$9-12/month
- Outbound bandwidth: ~$0-3/month
- Supabase: Free tier (50k MAU, 500MB database)

**Total: $10-15/month**

### To Reduce Costs Further

1. **Auto-stop machines** (if okay with cold starts):
   ```toml
   [http_service]
     auto_stop_machines = true  # Stop when idle
     auto_start_machines = true  # Start on request
   ```

2. **Scale down during off-hours** (manual):
   ```bash
   # Stop services at night
   flyctl scale count 0 -a omnara-backend

   # Start in morning
   flyctl scale count 1 -a omnara-backend
   ```

3. **Use single shared database** for all environments

## Security Best Practices

### Secrets Management

✅ DO:
- Use `flyctl secrets set` for sensitive data
- Rotate JWT keys periodically
- Use different keys for production/staging
- Keep Supabase service role key secret

❌ DON'T:
- Commit secrets to git
- Share secrets in plain text
- Use same keys across environments
- Expose JWT_PRIVATE_KEY publicly

### Database Security

- Use Supabase transaction pooler (port 6543)
- Enable Row Level Security (RLS) in Supabase
- Regularly backup database
- Monitor for suspicious queries

### API Security

- Keep Supabase anon key public-facing only
- Never expose service role key to clients
- Implement rate limiting (Fly.io handles basic HTTPS)
- Monitor for unusual traffic patterns

## Updating Deployment

### Code Changes

```bash
# Pull latest changes
git pull origin main

# Deploy updated services
./infrastructure/scripts/deploy-to-fly.sh
```

### Database Migrations

```bash
# Create new migration
cd src/shared
alembic revision --autogenerate -m "description"

# Review migration file
cat alembic/versions/[new-file].py

# Apply migration
export PRODUCTION_DB_URL="postgresql://..."
alembic upgrade head
```

### Environment Variables

```bash
# Update secret
flyctl secrets set KEY="new-value" -a omnara-backend

# Verify
flyctl secrets list -a omnara-backend

# Service will auto-restart
```

## Backup and Recovery

### Database Backup

Supabase provides daily backups (free tier: 7 days retention)

Manual backup:
```bash
# Using pg_dump
pg_dump "postgresql://..." > backup-$(date +%Y%m%d).sql

# Restore
psql "postgresql://..." < backup-20250130.sql
```

### Configuration Backup

```bash
# Export all secrets (safe to commit)
flyctl secrets list -a omnara-backend > secrets-list.txt

# Export fly.toml files (already in git)
git add fly.*.toml
git commit -m "Update Fly.io configs"
```

## Support

- **Fly.io Docs:** https://fly.io/docs
- **Fly.io Community:** https://community.fly.io
- **Supabase Docs:** https://supabase.com/docs
- **Omnara Issues:** https://github.com/omnara-ai/omnara/issues

## Next Steps

1. **Deploy Web Dashboard** (Next.js app)
   - Build static site: `cd apps/web && npm run build`
   - Deploy to Fly.io, Vercel, or Netlify
   - Configure environment variables

2. **Configure Mobile App**
   - Update API URLs in mobile app
   - Build for iOS/Android
   - Distribute via App Store/Play Store

3. **Test Agent Integration**
   - Install Omnara CLI: `pip install omnara`
   - Generate API key via backend
   - Test: `omnara --help`

4. **Set Up Monitoring**
   - Configure Sentry for error tracking
   - Set up Fly.io alerts
   - Monitor database performance in Supabase

## Summary Checklist

- [ ] Supabase project created
- [ ] JWT keys generated
- [ ] Fly.io apps created (3 apps)
- [ ] Secrets configured (all apps)
- [ ] Database migrations run
- [ ] Services deployed successfully
- [ ] Health checks passing
- [ ] Test user created in Supabase
- [ ] Service URLs documented
- [ ] Monitoring configured (optional)
- [ ] Backup strategy in place

**Congratulations!** Your Omnara deployment is live on Fly.io! 🎉
