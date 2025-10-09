# n8n Integration Architecture

## Overview

The Omnara n8n integration (`n8n-nodes-omnara`) is a community node package that enables workflows to communicate with users in real-time through the Omnara platform. It allows n8n workflows to send status updates, ask questions, and wait for user responses via web, mobile, email, and SMS.

**Key Capabilities:**
- **Non-blocking messages**: Send status updates while workflows continue
- **Blocking questions**: Pause workflows until users respond
- **AI Agent compatibility**: Usable as a tool in n8n AI Agents
- **Multi-channel notifications**: Email, SMS, and push notifications
- **Session management**: Track and end agent sessions

## Architecture

```
┌─────────────┐         ┌──────────────────┐         ┌─────────────┐
│   n8n       │         │     Omnara       │         │    User     │
│  Workflow   │────────▶│  Agent Server    │────────▶│  Dashboard  │
│             │  POST   │  (agent.omnara)  │  Real   │  Web/Mobile │
│             │ /messages│                  │  time   │             │
│             │         │                  │         │             │
│             │◀────────│  Stores in DB    │         │             │
│             │ Response│  + Sends Notifs  │         │             │
│             │         │                  │         │             │
│             │         │                  │◀────────│  Responds   │
│             │◀────────│  Webhook Trigger │  User   │             │
│  Resumes    │ POST    │  (async mode)    │ Message │             │
│             │         │                  │         │             │
└─────────────┘         └──────────────────┘         └─────────────┘
```

## Package Structure

```
src/integrations/n8n/
├── src/
│   ├── credentials/
│   │   └── OmnaraApi.credentials.ts      # API key authentication
│   ├── nodes/
│   │   └── Omnara/
│   │       ├── Omnara.node.ts            # Main node implementation
│   │       ├── Omnara.node.json          # Node metadata
│   │       ├── omnara.png                # Node icon
│   │       └── actions/
│   │           ├── message/
│   │           │   ├── index.ts          # Operation descriptions
│   │           │   ├── send.operation.ts # Non-blocking send
│   │           │   └── sendAndWait.operation.ts # Blocking send
│   │           └── session/
│   │               ├── index.ts
│   │               └── end.operation.ts  # End session
│   └── utils/
│       ├── GenericFunctions.ts           # API request helpers
│       ├── sendAndWaitWebhook.ts         # Webhook handler
│       └── sendAndWait/
│           ├── descriptions.ts           # Webhook config
│           └── configureWaitTillDate.ts  # Timeout config
├── package.json
├── tsconfig.json
└── README.md
```

## Authentication

### Credential Setup (`OmnaraApi.credentials.ts`)

The n8n node uses **Bearer token authentication** with Omnara's agent API:

```typescript
authenticate: IAuthenticateGeneric = {
  type: 'generic',
  properties: {
    headers: {
      Authorization: '=Bearer {{$credentials.apiKey}}'
    }
  }
}
```

**Configuration:**
- **API Key**: User's Omnara API key (obtained from dashboard)
- **Server URL**: Defaults to `https://agent.omnara.com` (customizable for self-hosted)
- **Credential Test**: Validates by calling `/api/v1/auth/verify`

**How It Works:**
1. User creates API key in Omnara dashboard
2. API key is stored in n8n's credential system
3. Every API request includes `Authorization: Bearer <api_key>` header
4. Omnara server validates JWT and extracts user_id
5. All operations are scoped to authenticated user

## Core Operations

### 1. Send Message (Non-blocking)

**Purpose**: Send informational messages without waiting for response

**Implementation**: `src/nodes/Omnara/actions/message/send.operation.ts:100`

**API Call:**
```
POST /api/v1/messages/agent
{
  "agent_instance_id": "uuid",
  "agent_type": "agent_name",
  "content": "Status update text",
  "requires_user_input": false,
  "send_email": false,
  "send_sms": false,
  "send_push": false
}
```

**Flow:**
1. n8n node receives message to send
2. Makes POST request to Omnara agent server
3. Server creates/updates agent instance
4. Stores message in database with `sender_type=AGENT`
5. Sends notifications based on user preferences
6. Returns immediately with message_id
7. Workflow continues without waiting

**Response includes:**
- `message_id`: ID of created message
- `agent_instance_id`: Instance ID (created if new)
- `queued_user_messages`: Any pending user responses (see Queued Messages below)

### 2. Send and Wait (Blocking)

**Purpose**: Ask questions and pause until user responds

**Implementation**: `src/nodes/Omnara/Omnara.node.ts:81`

**Two Modes:**

#### Mode A: Webhook Mode (Async) - Default for workflows

**How It Works:**
1. n8n generates unique webhook URL: `{resumeUrl}/{nodeId}`
2. Sends message with webhook URL in metadata:
```json
{
  "agent_instance_id": "uuid",
  "content": "Question text",
  "requires_user_input": true,
  "message_metadata": {
    "webhook_url": "https://n8n.example.com/webhook-waiting/exec-123/node-456",
    "webhook_type": "n8n_send_and_wait",
    "execution_id": "exec-123",
    "node_id": "node-456"
  }
}
```
3. Calls `putExecutionToWait()` - workflow pauses
4. User responds in Omnara dashboard
5. Omnara triggers webhook callback (`src/servers/shared/db/queries.py:490`)
6. n8n receives webhook POST and resumes workflow
7. Response data flows to next node

**Webhook Callback** (`src/utils/sendAndWaitWebhook.ts:12`):
```typescript
export async function omnaraSendAndWaitWebhook(this: IWebhookFunctions) {
  const body = this.getBodyData();

  const responseData = {
    userResponse: body.user_message,
    userId: body.user_id,
    messageId: body.message_id,
    agentInstanceId: body.agent_instance_id,
    timestamp: body.timestamp
  };

  return {
    webhookResponse: { status: 200 },
    workflowData: [[{ json: responseData }]]
  };
}
```

**Omnara Server Webhook Trigger** (`src/servers/shared/db/queries.py:490`):
```python
def trigger_webhook_for_user_response(
    db: Session,
    agent_instance_id: UUID,
    user_message_content: str,
    user_message_id: str,
    user_id: str
):
    # Get last agent message with requires_user_input=True
    last_agent_message = get_last_agent_message_waiting_for_input(...)

    # Extract webhook URL from message metadata
    webhook_url = last_agent_message.message_metadata.get("webhook_url")

    # Trigger webhook with user's response
    response = httpx.post(webhook_url, json={
        "user_message": user_message_content,
        "user_id": user_id,
        "message_id": user_message_id,
        "agent_instance_id": str(agent_instance_id),
        "timestamp": datetime.now().isoformat()
    })

    # Mark as triggered to prevent duplicates
    last_agent_message.message_metadata["webhook_triggered"] = True
```

#### Mode B: Sync Mode (Polling) - For AI Agents

**Why Needed**: AI Agents in n8n don't support async `putExecutionToWait()` properly

**How It Works** (`src/nodes/Omnara/Omnara.node.ts:91`):
1. Sends message with `sync_mode: true` in metadata
2. NO webhook URL sent
3. Polls `/api/v1/messages/pending` every 5 seconds
4. Checks for user responses using `last_read_message_id`
5. Returns when response found or timeout reached
6. Synchronous execution - works in AI Agent context

**Polling Loop:**
```typescript
const syncTimeout = options.syncTimeout || 7200; // 2 hours
const pollInterval = options.pollInterval || 5; // 5 seconds
const startTime = Date.now();

while (Date.now() - startTime < syncTimeout * 1000) {
  // Busy-wait for poll interval
  const pollStart = Date.now();
  while (Date.now() - pollStart < pollInterval * 1000) {
    await new Promise(resolve => resolve(undefined));
  }

  // Check for pending messages
  const pending = await GET('/messages/pending', {
    agent_instance_id: agentInstanceId,
    last_read_message_id: lastReadMessageId
  });

  if (pending.messages.length > 0) {
    return pending.messages[pending.messages.length - 1];
  }
}

// Timeout
return { success: false, error: 'Timeout' };
```

**Key Differences:**
| Feature | Webhook Mode | Sync Mode |
|---------|--------------|-----------|
| **Async/Await** | Yes | No (synchronous) |
| **Resource Usage** | Low (event-driven) | Higher (polling) |
| **AI Agent Compatible** | No | Yes |
| **Max Wait Time** | 7 days | 48 hours |
| **Use Case** | Regular workflows | AI Agent tools |

### 3. End Session

**Purpose**: Mark agent instance as completed

**Implementation**: `src/nodes/Omnara/actions/session/end.operation.ts:28`

**API Call:**
```
POST /api/v1/sessions/end
{
  "agent_instance_id": "uuid"
}
```

**What Happens:**
1. Sets `status = COMPLETED` on agent instance
2. Timestamps `ended_at`
3. Stops tracking as active session
4. Instance remains in history

## Agent Instance Management

### Creating Instances

**Two Patterns:**

#### Pattern 1: Webhook-triggered (Recommended)
```
1. User triggers workflow from Omnara dashboard
2. Webhook sends:
   - agent_instance_id: pre-generated UUID
   - agent_type: from dashboard
   - prompt: user's message
3. All n8n nodes use same instance_id from webhook
```

#### Pattern 2: Self-generated
```
1. Workflow generates UUID: {{ $uuid() }}
2. Stores in Set node variables
3. All n8n nodes reference same variables
```

### Instance Lifecycle

```
CREATE (first message) → ACTIVE → COMPLETED (end session)
                            ↓
                      Messages exchanged
```

**Database Operations** (`src/servers/shared/db/queries.py:70`):
```python
def get_or_create_agent_instance(
    db: Session,
    agent_instance_id: str,
    user_id: str,
    agent_type: str | None
) -> AgentInstance:
    # Try to get existing
    instance = db.query(AgentInstance).filter(
        AgentInstance.id == agent_instance_id
    ).first()

    if instance:
        # Validate user owns this instance
        if str(instance.user_id) != user_id:
            raise ValueError("Access denied")
        return instance
    else:
        # Create new with provided ID
        user_agent = create_or_get_user_agent(db, agent_type, user_id)
        instance = AgentInstance(
            id=agent_instance_id,  # Use provided ID
            user_agent_id=user_agent.id,
            user_id=user_id,
            status=AgentStatus.ACTIVE
        )
        db.add(instance)
        return instance
```

## Message System Integration

### Unified Messaging

All messages stored in single `messages` table with:
- `sender_type`: AGENT or USER
- `requires_user_input`: True for questions, False for updates
- `message_metadata`: JSON field for webhook URLs, node IDs, etc.

### Queued Messages Feature

**Problem**: User might respond BEFORE agent's next message
**Solution**: Return queued user messages with every agent message

**Flow** (`src/servers/shared/db/queries.py:170`):
```python
def create_agent_message(...):
    # Create message
    message = Message(
        agent_instance_id=instance.id,
        content=content,
        sender_type=SenderType.AGENT,
        requires_user_input=requires_user_input,
        message_metadata=message_metadata
    )
    db.add(message)

    # Get any user messages since last read
    queued_messages = get_unread_user_messages(
        db=db,
        agent_instance_id=instance.id,
        last_read_message_id=instance.last_read_message_id
    )

    return {
        "message_id": message.id,
        "queued_user_messages": queued_messages  # User responses since last check
    }
```

**In n8n** (`src/nodes/Omnara/actions/message/send.operation.ts:138`):
```typescript
const response = await omnaraApiRequest.call(this, 'POST', '/messages/agent', body);

return [{
  json: {
    success: response.success,
    messageId: response.message_id,
    queuedUserMessages: response.queued_user_messages.map(formatMessageResponse)
  }
}];
```

**Why This Matters:**
- Agents can check for responses on EVERY message send
- No messages missed between send and wait operations
- User responses always delivered, even if timing is off

## API Communication

### Helper Functions (`src/utils/GenericFunctions.ts`)

```typescript
export async function omnaraApiRequest(
  this: IExecuteFunctions | ILoadOptionsFunctions,
  method: IHttpRequestMethods,
  endpoint: string,
  body: IDataObject = {},
  qs: IDataObject = {}
): Promise<any> {
  const credentials = await this.getCredentials('omnaraApi');

  const options: IHttpRequestOptions = {
    method,
    body,
    qs,
    url: `${credentials.serverUrl}/api/v1${endpoint}`,
    json: true
  };

  return await this.helpers.httpRequestWithAuthentication.call(
    this,
    'omnaraApi',  // Credential name
    options
  );
}
```

**All API calls:**
- Use authenticated helper (auto-adds Bearer token)
- Target `/api/v1/*` endpoints
- Return JSON responses
- Throw `NodeApiError` on failure

### API Endpoints Used

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/v1/messages/agent` | POST | Send agent message (both send & sendAndWait) |
| `/api/v1/messages/pending` | GET | Poll for user responses (sync mode) |
| `/api/v1/sessions/end` | POST | End agent session |
| `/api/v1/auth/verify` | GET | Validate credentials |

## AI Agent Tool Integration

### Configuration

```typescript
export class Omnara implements INodeType {
  description: INodeTypeDescription = {
    // ... other config
    usableAsTool: true,  // Enable AI Agent usage
  };
}
```

**What This Enables:**
- AI Agents can call Omnara node as a function
- Agent describes when to use: "ask user for input" or "update user"
- Parameters passed as function arguments

### Usage Pattern

```javascript
// AI Agent decides to ask user
await tools.omnara({
  resource: "message",
  operation: "sendAndWait",
  agentInstanceId: "current-instance-id",
  agentType: "claude_code",
  message: "Should I proceed with this change?",
  options: {
    syncMode: true,  // REQUIRED for AI Agents
    syncTimeout: 600,
    pollInterval: 5,
    sendEmail: true
  }
});
// Returns user's response synchronously
```

**Critical**: MUST use `syncMode: true` because AI Agents run synchronously and can't handle async `putExecutionToWait()`.

## Notification System

### Notification Preferences

**Priority (high to low):**
1. **Message-level override**: `send_email`, `send_sms`, `send_push` in request
2. **User preferences**: Stored in database per user
3. **Default behavior**: No notifications unless specified

### When Notifications Sent

**Triggers:**
- New agent message (status update or question)
- `requires_user_input=true` → More urgent notification

**Channels:**
- **Email**: Always available, zero setup
- **SMS**: Available with phone verification
- **Push**: Available via mobile app or web push

**Implementation** (handled by Omnara backend, not n8n):
```python
# In create_agent_message()
send_notifications(
    user_id=instance.user_id,
    agent_name=user_agent.name,
    message_content=content,
    requires_input=requires_user_input,
    email_override=send_email,
    sms_override=send_sms,
    push_override=send_push
)
```

## Error Handling

### Credential Errors

```typescript
if (!credentials) {
  throw new NodeApiError(this.getNode(), {
    message: 'No credentials found'
  });
}
```

### Operation Errors

```typescript
try {
  const response = await omnaraApiRequest(...);
  return [{ json: response }];
} catch (error) {
  throw new NodeOperationError(
    this.getNode(),
    `Failed to send message: ${error.message}`,
    { itemIndex: index }
  );
}
```

### Continue On Fail

```typescript
} catch (error) {
  if (this.continueOnFail()) {
    returnData.push({
      json: {
        error: error.message,
        resource,
        operation,
        itemIndex: i
      },
      pairedItem: i
    });
    continue;
  }
  throw error;
}
```

## Development Workflow

### Building the Package

```bash
npm install          # Install dependencies
npm run build        # Compile TypeScript → dist/
npm run copy-assets  # Copy .png and .json files
```

**Build Output:**
```
dist/
├── credentials/
│   └── OmnaraApi.credentials.js
├── nodes/
│   └── Omnara/
│       ├── Omnara.node.js
│       ├── Omnara.node.json
│       └── omnara.png
└── utils/
    └── GenericFunctions.js
```

### Publishing to npm

```bash
npm run prepublishOnly  # Runs build automatically
npm publish            # Publish to npm registry
```

**Package Name**: `n8n-nodes-omnara`
**Registry**: https://www.npmjs.com/package/n8n-nodes-omnara

### Testing Locally

```bash
# In n8n-nodes-omnara directory
npm link

# In n8n installation directory
npm link n8n-nodes-omnara
```

## Integration Points with Omnara Platform

### Backend API (`src/servers/api/routers.py`)

```python
@router.post("/messages/agent")
async def create_message(
    request: CreateMessageRequest,
    user_id: str = Depends(verify_api_key_dependency)
):
    # Validate and create agent instance
    instance = get_or_create_agent_instance(
        db, request.agent_instance_id, user_id, request.agent_type
    )

    # Create message in database
    result = create_agent_message(
        db=db,
        agent_instance_id=instance.id,
        content=request.content,
        requires_user_input=request.requires_user_input,
        message_metadata=request.message_metadata,
        send_email=request.send_email,
        send_sms=request.send_sms,
        send_push=request.send_push,
        git_diff=request.git_diff
    )

    # Send notifications
    # Return queued user messages
    return result
```

### Database Models (`src/shared/models/`)

```python
class Message(Base):
    __tablename__ = "messages"

    id = Column(UUID, primary_key=True)
    agent_instance_id = Column(UUID, ForeignKey("agent_instances.id"))
    content = Column(Text, nullable=False)
    sender_type = Column(Enum(SenderType))  # AGENT or USER
    requires_user_input = Column(Boolean, default=False)
    message_metadata = Column(JSON)  # Stores webhook URLs
    created_at = Column(DateTime(timezone=True))

class AgentInstance(Base):
    __tablename__ = "agent_instances"

    id = Column(UUID, primary_key=True)
    user_agent_id = Column(UUID, ForeignKey("user_agents.id"))
    user_id = Column(UUID, ForeignKey("users.id"))
    status = Column(Enum(AgentStatus))  # ACTIVE, COMPLETED, STALE
    last_read_message_id = Column(UUID)  # For queued messages
    git_diff = Column(Text)  # Optional git context
    ended_at = Column(DateTime(timezone=True))
```

### Web Dashboard (`apps/web/`)

**User Interface:**
- Real-time message display (WebSocket or polling)
- Text input for responding to agent questions
- Notification preferences management
- Session history and filtering

**Response Flow:**
```
User types response → POST /messages/user →
Trigger webhook (if n8n waiting) →
Update UI with confirmation
```

## Configuration Examples

### Example 1: Simple Status Updates

```javascript
// n8n workflow
{
  "nodes": [
    {
      "name": "Build Project",
      "type": "n8n-nodes-base.code",
      "parameters": {
        "code": "// build logic"
      }
    },
    {
      "name": "Notify Progress",
      "type": "n8n-nodes-omnara.omnara",
      "parameters": {
        "resource": "message",
        "operation": "send",
        "agentInstanceId": "{{ $('Set').json.instanceId }}",
        "agentType": "CI/CD Agent",
        "message": "Build completed successfully! ✅",
        "additionalOptions": {
          "sendPush": true
        }
      }
    }
  ]
}
```

### Example 2: Approval Workflow

```javascript
{
  "nodes": [
    {
      "name": "Prepare Deployment",
      "type": "n8n-nodes-base.code",
      "parameters": {
        "code": "// deployment prep"
      }
    },
    {
      "name": "Request Approval",
      "type": "n8n-nodes-omnara.omnara",
      "parameters": {
        "resource": "message",
        "operation": "sendAndWait",
        "agentInstanceId": "{{ $('Set').json.instanceId }}",
        "agentType": "Deployment Agent",
        "message": "Ready to deploy to production. Approve?",
        "options": {
          "sendEmail": true,
          "sendPush": true
        },
        "limitWaitTime": true,
        "limitType": "afterTimeInterval",
        "resumeAmount": 1,
        "resumeUnit": "hours"
      }
    },
    {
      "name": "Deploy",
      "type": "n8n-nodes-base.code",
      "parameters": {
        "code": "// deploy only if approved"
      }
    }
  ]
}
```

### Example 3: AI Agent with Sync Mode

```javascript
{
  "nodes": [
    {
      "name": "AI Agent",
      "type": "@n8n/n8n-nodes-langchain.agent",
      "parameters": {
        "tools": ["omnara"],
        "prompt": "You are a helpful assistant. Use Omnara to ask the user for clarification when needed."
      }
    },
    {
      "name": "Omnara Tool Config",
      "type": "n8n-nodes-omnara.omnara",
      "parameters": {
        "resource": "message",
        "operation": "sendAndWait",
        "agentInstanceId": "{{ $json.agentInstanceId }}",
        "agentType": "AI Assistant",
        "message": "{{ $json.question }}",
        "options": {
          "syncMode": true,      // REQUIRED for AI Agents
          "syncTimeout": 600,    // 10 minute timeout
          "pollInterval": 5,     // Check every 5 seconds
          "sendEmail": true,
          "sendPush": true
        }
      }
    }
  ]
}
```

## Key Takeaways

1. **Two Message Types**:
   - **Send**: Non-blocking status updates (workflow continues immediately)
   - **Send and Wait**: Blocking questions (workflow pauses until response)

2. **Two Wait Modes**:
   - **Webhook Mode**: Efficient, event-driven (regular workflows)
   - **Sync Mode**: Polling-based (AI Agent tools)

3. **Webhook Magic**:
   - n8n generates unique webhook URL per execution
   - Omnara stores URL in message metadata
   - User response triggers webhook → workflow resumes
   - One-time use, automatic cleanup

4. **Agent Instance = Conversation Thread**:
   - Same `agent_instance_id` across all nodes in workflow
   - Groups all messages together
   - Tracked in dashboard as single session

5. **Queued Messages**:
   - Every agent message returns pending user responses
   - Prevents missed messages
   - Works even if timing is off

6. **AI Agent Compatibility**:
   - MUST use `syncMode: true`
   - Synchronous polling instead of async webhooks
   - Set `usableAsTool: true` in node config

7. **Authentication**:
   - Bearer token (JWT) in Authorization header
   - User-scoped operations (can't access other users' instances)
   - API key from dashboard

## Related Files

- **n8n Node**: `src/integrations/n8n/src/nodes/Omnara/Omnara.node.ts`
- **Webhook Handler**: `src/integrations/n8n/src/utils/sendAndWaitWebhook.ts`
- **API Models**: `src/servers/api/models.py`
- **DB Queries**: `src/servers/shared/db/queries.py`
- **Authentication**: `src/servers/api/auth.py`
- **User README**: `src/integrations/n8n/README.md`