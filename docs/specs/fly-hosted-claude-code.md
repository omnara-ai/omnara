# Fly.io-Hosted Claude Code Instances

**Status**: Draft
**Created**: 2025-10-31
**Author**: La Boeuf & Claude
**Epic**: Cloud-Native AI Agent Platform

## Problem Statement

Currently, users must keep their local machine running with Claude Code active to use Omnara's dashboard and mobile features. This creates several pain points:

1. **Always-On Requirement**: Local machine must stay powered and connected
2. **Limited Mobility**: Cannot truly work remotely without laptop
3. **Resource Consumption**: Local machine resources tied up by agent processes
4. **No Scalability**: Can't easily run multiple agents simultaneously
5. **Session Persistence**: Lose connection if local machine sleeps/restarts

## Proposed Solution

Enable users to launch Claude Code instances directly on Fly.io through the Omnara dashboard. These cloud-hosted instances connect to Omnara's infrastructure, allowing users to interact with their agents from any device without a local machine running.

## Architecture

### High-Level Flow

```
┌─────────────────────────────────────────────────────┐
│       User (Web Dashboard or Mobile App)            │
│              Anywhere, Anytime                      │
└──────────────────┬──────────────────────────────────┘
                   │
                   │ 1. Click "Launch Claude Code"
                   │ 2. Select project/workspace
                   │
                   ▼
┌─────────────────────────────────────────────────────┐
│          Omnara Backend API                         │
│   POST /api/v1/claude-instances/launch              │
│   - Authenticates user (Supabase JWT)              │
│   - Validates subscription/limits                   │
│   - Calls Fly Machines API                         │
└──────────────────┬──────────────────────────────────┘
                   │
                   │ 3. Create Fly Machine
                   │    flyctl machines create
                   │
                   ▼
┌─────────────────────────────────────────────────────┐
│         Claude Code Instance (Fly Machine)          │
│   ┌───────────────────────────────────────────┐   │
│   │ Container Environment:                     │   │
│   │ - Claude Code binary                       │   │
│   │ - CLAUDE_CODE_OAUTH_TOKEN (user's token)  │   │
│   │ - OMNARA_SERVER_URL                        │   │
│   │ - OMNARA_USER_ID                          │   │
│   │ - PROJECT_WORKSPACE (volume mount)        │   │
│   └───────────────────────────────────────────┘   │
│                                                     │
│   Startup Process:                                 │
│   1. Initialize Claude Code                        │
│   2. Register with Omnara (MCP connection)        │
│   3. Start serving requests                        │
└──────────────────┬──────────────────────────────────┘
                   │
                   │ 4. Connect via MCP
                   │    wss://omnara-servers.fly.dev/mcp/
                   │
                   ▼
┌─────────────────────────────────────────────────────┐
│         Omnara Unified Server                       │
│   - Receives agent registration                     │
│   - Stores messages in PostgreSQL                  │
│   - Streams updates to dashboard                   │
└──────────────────┬──────────────────────────────────┘
                   │
                   │ 5. Real-time updates
                   │
                   ▼
┌─────────────────────────────────────────────────────┐
│       User Dashboard (Auto-refreshes)               │
│   - See agent activity in real-time                │
│   - Send messages/questions                        │
│   - Monitor resource usage                         │
└─────────────────────────────────────────────────────┘
```

## Components

### 1. Machine Launcher Service

**Location**: `src/backend/api/claude_instances.py`

**Endpoints**:

```python
POST /api/v1/claude-instances/launch
Body: {
    "project_name": "my-webapp",
    "workspace_url": "https://github.com/user/repo.git",  # Optional
    "machine_size": "shared-cpu-1x",  # or "shared-cpu-2x"
    "auto_stop_timeout": "30m"
}
Response: {
    "instance_id": "machine-123abc",
    "status": "starting",
    "dashboard_url": "https://omnara.com/instances/machine-123abc",
    "estimated_cost_per_hour": 0.01
}

GET /api/v1/claude-instances
Response: {
    "instances": [
        {
            "id": "machine-123abc",
            "project_name": "my-webapp",
            "status": "running",
            "created_at": "2025-10-31T12:00:00Z",
            "last_active": "2025-10-31T15:30:00Z",
            "uptime_hours": 3.5,
            "cost_to_date": 0.035
        }
    ]
}

GET /api/v1/claude-instances/{instance_id}
Response: {
    "id": "machine-123abc",
    "status": "running",
    "metrics": {
        "cpu_percent": 15,
        "memory_mb": 180,
        "requests_count": 42
    },
    "agent": {
        "id": "agent-456def",
        "connected": true,
        "last_message": "Completed database migration"
    }
}

POST /api/v1/claude-instances/{instance_id}/stop
Response: {
    "status": "stopped",
    "message": "Instance will restart on next dashboard access"
}

DELETE /api/v1/claude-instances/{instance_id}
Response: {
    "status": "destroyed",
    "final_cost": 0.42,
    "message": "Instance and associated data destroyed"
}
```

**Implementation**:

```python
from typing import Optional
from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel
import httpx

router = APIRouter(prefix="/claude-instances", tags=["claude-instances"])

class LaunchInstanceRequest(BaseModel):
    project_name: str
    workspace_url: Optional[str] = None
    machine_size: str = "shared-cpu-1x"
    auto_stop_timeout: str = "30m"

class InstanceResponse(BaseModel):
    instance_id: str
    status: str
    dashboard_url: str
    estimated_cost_per_hour: float

@router.post("/launch", response_model=InstanceResponse)
async def launch_instance(
    request: LaunchInstanceRequest,
    user_id: str = Depends(get_current_user_id)
):
    """Launch a new Claude Code instance on Fly.io"""

    # 1. Check user subscription/limits
    if not await can_launch_instance(user_id):
        raise HTTPException(403, "Instance limit reached")

    # 2. Get user's OAuth token
    oauth_token = await get_user_oauth_token(user_id)
    if not oauth_token:
        raise HTTPException(400, "OAuth token not configured")

    # 3. Create Fly machine
    machine_id = await fly_client.create_machine(
        app="omnara-claude-instances",
        config={
            "image": "registry.fly.io/omnara-claude-code:latest",
            "env": {
                "OMNARA_USER_ID": user_id,
                "OMNARA_SERVER_URL": "https://omnara-servers.fly.dev",
                "CLAUDE_CODE_OAUTH_TOKEN": oauth_token,
                "PROJECT_NAME": request.project_name,
                "WORKSPACE_URL": request.workspace_url or ""
            },
            "guest": {
                "cpu_kind": "shared",
                "cpus": 1 if "1x" in request.machine_size else 2,
                "memory_mb": 256 if "1x" in request.machine_size else 512
            },
            "auto_destroy": False,
            "restart": {
                "policy": "always"
            },
            "services": [{
                "protocol": "tcp",
                "internal_port": 3000,
                "auto_stop_machines": {
                    "enabled": True,
                    "idle_timeout": request.auto_stop_timeout
                }
            }]
        }
    )

    # 4. Register instance in database
    await db.insert_claude_instance(
        user_id=user_id,
        instance_id=machine_id,
        project_name=request.project_name,
        machine_size=request.machine_size
    )

    return InstanceResponse(
        instance_id=machine_id,
        status="starting",
        dashboard_url=f"https://omnara.com/instances/{machine_id}",
        estimated_cost_per_hour=0.01 if "1x" in request.machine_size else 0.02
    )
```

### 2. Claude Code Docker Image

**Location**: `infrastructure/docker/claude-code.Dockerfile`

```dockerfile
FROM debian:bookworm-slim

# Install dependencies
RUN apt-get update && apt-get install -y \
    curl \
    git \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI
RUN curl -fsSL https://claude.ai/install.sh | sh

# Create workspace directory
RUN mkdir -p /workspace
WORKDIR /workspace

# Copy registration script
COPY infrastructure/scripts/register-claude-instance.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/register-claude-instance.sh

# Set environment variables
ENV CLAUDE_CODE_CONFIG_DIR=/workspace/.claude
ENV PATH="/root/.local/bin:${PATH}"

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s \
    CMD curl -f http://localhost:3000/health || exit 1

# Start Claude Code with Omnara registration
CMD ["/usr/local/bin/register-claude-instance.sh"]
```

**Registration Script**: `infrastructure/scripts/register-claude-instance.sh`

```bash
#!/bin/bash
set -e

echo "🚀 Starting Claude Code instance..."
echo "User ID: ${OMNARA_USER_ID}"
echo "Project: ${PROJECT_NAME}"

# Clone workspace if URL provided
if [ -n "$WORKSPACE_URL" ]; then
    echo "📦 Cloning workspace from ${WORKSPACE_URL}..."
    git clone "$WORKSPACE_URL" /workspace/project
    cd /workspace/project
fi

# Configure Claude Code with OAuth token
echo "🔑 Configuring Claude Code authentication..."
mkdir -p ~/.claude
cat > ~/.claude/config.json <<EOF
{
  "oauth_token": "${CLAUDE_CODE_OAUTH_TOKEN}",
  "api_endpoint": "https://api.anthropic.com"
}
EOF

# Register with Omnara
echo "📡 Registering with Omnara..."
curl -X POST "https://omnara-servers.fly.dev/api/v1/agents/register" \
  -H "Content-Type: application/json" \
  -d "{
    \"user_id\": \"${OMNARA_USER_ID}\",
    \"instance_id\": \"${FLY_MACHINE_ID}\",
    \"project_name\": \"${PROJECT_NAME}\",
    \"agent_type\": \"fly-hosted\"
  }"

# Start Claude Code in server mode
echo "✅ Claude Code ready! Connecting to Omnara..."
exec claude-code --server \
  --omnara-url "https://omnara-servers.fly.dev" \
  --user-id "${OMNARA_USER_ID}"
```

### 3. Fly Machines API Client

**Location**: `src/shared/fly_client.py`

```python
import httpx
from typing import Dict, Any, Optional
from shared.config import settings

class FlyMachinesClient:
    """Client for Fly.io Machines API"""

    def __init__(self):
        self.base_url = "https://api.machines.dev/v1"
        self.token = settings.fly_api_token

    async def create_machine(
        self,
        app: str,
        config: Dict[str, Any]
    ) -> str:
        """Create a new Fly machine"""
        async with httpx.AsyncClient() as client:
            response = await client.post(
                f"{self.base_url}/apps/{app}/machines",
                headers={"Authorization": f"Bearer {self.token}"},
                json={"config": config}
            )
            response.raise_for_status()
            return response.json()["id"]

    async def get_machine(self, app: str, machine_id: str) -> Dict[str, Any]:
        """Get machine details"""
        async with httpx.AsyncClient() as client:
            response = await client.get(
                f"{self.base_url}/apps/{app}/machines/{machine_id}",
                headers={"Authorization": f"Bearer {self.token}"}
            )
            response.raise_for_status()
            return response.json()

    async def stop_machine(self, app: str, machine_id: str):
        """Stop a machine"""
        async with httpx.AsyncClient() as client:
            await client.post(
                f"{self.base_url}/apps/{app}/machines/{machine_id}/stop",
                headers={"Authorization": f"Bearer {self.token}"}
            )

    async def destroy_machine(self, app: str, machine_id: str):
        """Destroy a machine permanently"""
        async with httpx.AsyncClient() as client:
            await client.delete(
                f"{self.base_url}/apps/{app}/machines/{machine_id}",
                headers={"Authorization": f"Bearer {self.token}"}
            )

fly_client = FlyMachinesClient()
```

### 4. Database Schema

**New Table**: `claude_instances`

```sql
CREATE TABLE claude_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    machine_id VARCHAR(255) NOT NULL UNIQUE,
    project_name VARCHAR(255) NOT NULL,
    machine_size VARCHAR(50) NOT NULL,
    status VARCHAR(50) NOT NULL,  -- starting, running, stopped, destroyed
    workspace_url TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    last_active_at TIMESTAMP WITH TIME ZONE,
    destroyed_at TIMESTAMP WITH TIME ZONE,

    -- Metrics
    total_uptime_hours DECIMAL(10, 2) DEFAULT 0,
    total_cost DECIMAL(10, 4) DEFAULT 0,

    INDEX idx_user_active (user_id, status) WHERE status != 'destroyed'
);
```

### 5. Dashboard UI Components

**Web**: `apps/web/components/ClaudeInstanceManager.tsx`

```typescript
export function ClaudeInstanceManager() {
  const [instances, setInstances] = useState<Instance[]>([]);
  const [showLaunchModal, setShowLaunchModal] = useState(false);

  return (
    <div className="instance-manager">
      <div className="header">
        <h2>Claude Code Instances</h2>
        <button onClick={() => setShowLaunchModal(true)}>
          + Launch New Instance
        </button>
      </div>

      <div className="instances-grid">
        {instances.map(instance => (
          <InstanceCard
            key={instance.id}
            instance={instance}
            onStop={() => stopInstance(instance.id)}
            onDestroy={() => destroyInstance(instance.id)}
          />
        ))}
      </div>

      {showLaunchModal && (
        <LaunchInstanceModal
          onClose={() => setShowLaunchModal(false)}
          onLaunch={launchInstance}
        />
      )}
    </div>
  );
}
```

## Cost Analysis

### Per-Instance Costs (Fly.io Pricing)

| Machine Size | vCPU | RAM | Cost/Hour | Cost/Day (24h) | Cost/Month |
|--------------|------|-----|-----------|----------------|------------|
| shared-cpu-1x | 1 | 256MB | $0.0075 | $0.18 | $5.40 |
| shared-cpu-1x | 1 | 512MB | $0.01 | $0.24 | $7.20 |
| shared-cpu-2x | 2 | 512MB | $0.02 | $0.48 | $14.40 |

**With Auto-Stop (30min idle timeout)**:
- Average 2 hours/day active: ~$0.015-0.02/day
- Monthly: ~$0.45-0.60 per instance

### Storage Costs

| Type | Size | Cost/Month |
|------|------|------------|
| Volume (persistent workspace) | 1GB | $0.15 |
| Volume (persistent workspace) | 10GB | $1.50 |

### Example User Scenarios

**Scenario 1: Single Developer**
- 1 instance, 2 hours/day active
- Cost: ~$0.60/month

**Scenario 2: Multi-Project Developer**
- 3 instances, average 1.5 hours/day each
- Cost: ~$1.35/month

**Scenario 3: Team (5 users)**
- 10 instances total, various activity
- Cost: ~$6-8/month

## Implementation Phases

### Phase 1: Core Infrastructure (Week 1)
- [ ] Create Fly Machines API client
- [ ] Build Claude Code Docker image
- [ ] Add `claude_instances` table migration
- [ ] Implement basic launch/stop/destroy endpoints
- [ ] Test single instance lifecycle

### Phase 2: Dashboard Integration (Week 2)
- [ ] Build Instance Manager UI component
- [ ] Add launch modal with configuration
- [ ] Display instance status and metrics
- [ ] Implement real-time status updates
- [ ] Add cost tracking display

### Phase 3: Agent Integration (Week 3)
- [ ] Modify Claude Code to support Omnara registration
- [ ] Test MCP connection from Fly instance
- [ ] Verify message persistence
- [ ] Test session resumption after restart
- [ ] Add health monitoring

### Phase 4: Polish & Optimization (Week 4)
- [ ] Add usage limits per subscription tier
- [ ] Implement cost alerts
- [ ] Add workspace volume management
- [ ] Optimize auto-stop heuristics
- [ ] Add batch operations (stop all, destroy all)

### Phase 5: Mobile Support (Week 5)
- [ ] Port Instance Manager to React Native
- [ ] Add push notifications for instance events
- [ ] Optimize for mobile bandwidth
- [ ] Test on iOS and Android

## Security Considerations

1. **OAuth Token Storage**
   - Encrypted at rest in database
   - Never logged or exposed in responses
   - Separate token per user, not shared

2. **Machine Isolation**
   - Each instance in separate Fly machine
   - No inter-machine communication by default
   - Resource limits enforced

3. **Access Control**
   - Users can only manage their own instances
   - API endpoints require authentication
   - Admin endpoints for monitoring

4. **Rate Limiting**
   - Max 10 concurrent instances per user (free tier)
   - Launch rate: 5 per hour per user
   - Prevents abuse

## Monitoring & Observability

**Metrics to Track**:
- Total instances launched
- Average instance uptime
- Total cost per user
- Instance failure rate
- Auto-stop effectiveness

**Alerts**:
- Instance failed to start
- Instance exceeded cost threshold
- Unusual resource usage
- OAuth token expiration

## Future Enhancements

1. **Pre-configured Workspaces**
   - Template workspaces (Next.js, Python, etc.)
   - One-click project initialization

2. **Collaborative Instances**
   - Share instance with team members
   - Multi-user access to same agent

3. **Scheduled Instances**
   - Start instance at specific times
   - Cron-like scheduling

4. **Advanced Auto-Scaling**
   - Scale based on queue depth
   - Burst capacity for heavy workloads

5. **Git Integration**
   - Auto-commit changes
   - PR creation from agent work

## Success Metrics

- **Adoption**: 40% of active users launch at least one instance
- **Engagement**: Average 3 instances per active user
- **Retention**: 70% of users keep instances running
- **Cost**: Average user cost under $3/month
- **Reliability**: 99.5% instance uptime

## Open Questions

1. Should instances auto-destroy after X days of inactivity?
2. What's the right balance for auto-stop timeout?
3. Do we need instance snapshots for faster restarts?
4. Should we support custom Docker images?
5. How do we handle long-running tasks that exceed auto-stop timeout?

## References

- [Fly.io Machines API Documentation](https://fly.io/docs/machines/api/)
- [Claude Code CLI Documentation](https://docs.claude.com/claude-code)
- [Omnara MCP Protocol Specification](./mcp-protocol.md)
