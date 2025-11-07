# Omnara Fly.io Deployment Summary

## Successful Deployment ✅

All Omnara services are now deployed and working on Fly.io:

- **Backend API**: https://omnara-backend.fly.dev
- **Unified Servers**: https://omnara-servers.fly.dev
- **Relay Server**: wss://omnara-relay.fly.dev
- **Web Dashboard**: https://omnara-web.fly.dev

## Services Configuration

### Backend (omnara-backend)
- **Purpose**: Web dashboard API for read operations
- **Auth**: Supabase JWTs
- **Port**: 8000
- **Database**: Shared PostgreSQL on Supabase

### Unified Servers (omnara-servers)
- **Purpose**: Agent communication (MCP + REST API)
- **Auth**: Custom JWT with RSA keys
- **Port**: 8080
- **Endpoints**:
  - MCP: `/mcp/`
  - REST: `/api/v1/`

### Relay Server (omnara-relay)
- **Purpose**: WebSocket terminal streaming
- **Port**: 8787
- **Protocol**: Binary frames with metadata support

### Web Dashboard (omnara-web)
- **Purpose**: React SPA for user interface
- **Tech**: Next.js + Nginx
- **Auth**: Supabase authentication

## Using the Deployed System

### CLI Setup

Add to `~/.zshrc`:

```bash
export OMNARA_API_KEY="<your-api-key>"
alias omnara-fly='omnara --base-url https://omnara-servers.fly.dev --relay-url wss://omnara-relay.fly.dev/agent --api-key $OMNARA_API_KEY'
```

### Generate API Key

1. Sign in to https://omnara-web.fly.dev
2. Go to Settings → API Keys
3. Click "Create API Key"
4. Copy the key and add to your `~/.zshrc`

### Run Agent

```bash
source ~/.zshrc
omnara-fly
```

Your agent will appear in the dashboard at https://omnara-web.fly.dev

## Important Notes

### User ID Matching

**Critical**: Your API key must be generated for the same user you're logged in as on the web dashboard. The API key contains a user ID in the JWT payload, and agent instances are scoped to that user.

To check your user ID:
- Dashboard: Look at your Supabase user ID
- API Key: Decode the JWT (it's in the `sub` claim)

These must match!

### CORS Configuration

The backend uses `allow_credentials=False`, so CORS works with wildcard origins. No need to configure specific frontend URLs.

### Database

All services share a single PostgreSQL database on Supabase. Connection string is stored in Fly secrets as `DATABASE_URL`.

## Troubleshooting

### Agent Not Appearing in Dashboard

**Symptom**: Agent connects successfully but doesn't show in dashboard

**Cause**: User ID mismatch between API key and dashboard login

**Fix**: Generate a new API key through the dashboard while logged in

### CORS Errors

**Symptom**: Dashboard shows CORS errors

**Cause**: Backend service might be restarting

**Fix**: Wait a minute for the backend to fully start, then refresh

### Connection Refused

**Symptom**: Cannot connect to services

**Cause**: Fly machines might be stopped (auto-stop enabled)

**Fix**: Services auto-start on first request. Wait 10-15 seconds and retry.

## Deployment Commands

### Update Backend
```bash
flyctl deploy --config fly.backend.toml
```

### Update Servers
```bash
flyctl deploy --config fly.servers.toml
```

### Update Relay
```bash
flyctl deploy --config fly.relay.toml
```

### Update Web Dashboard
```bash
flyctl deploy --config fly.web.toml
```

### View Logs
```bash
flyctl logs -a omnara-backend
flyctl logs -a omnara-servers
flyctl logs -a omnara-relay
flyctl logs -a omnara-web
```

### Manage Secrets
```bash
flyctl secrets list -a omnara-backend
flyctl secrets set KEY=value -a omnara-backend
```

## Cost Optimization

All services configured with:
- `auto_stop_machines = "stop"` - Stop when idle
- `auto_start_machines = true` - Auto-start on request
- `min_machines_running = 0` - No always-on machines

This minimizes costs by only running machines when actively used.

## Next Steps

- [ ] Test complete workflow (agent → dashboard → interaction)
- [ ] Set up monitoring/alerting
- [ ] Configure custom domain (optional)
- [ ] Set up CI/CD for automated deployments
- [ ] Add production-grade logging and observability
