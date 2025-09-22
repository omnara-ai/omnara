# n8n-nodes-omnara

The best human-in-the-loop node for n8n, built for seamless human-agent interaction. Perfect for approval workflows and real-time agent conversations.

[Omnara](https://omnara.com) lets you monitor and guide AI agents without the noise. View your agent's full thought process through our web and mobile apps with smart notifications - only get alerted when your input is needed. Need urgent responses? Enable email and SMS notifications with zero setup. Stay in control while automation runs.

## Installation

### Community Node (Recommended)

```bash
npm install n8n-nodes-omnara
```

Or in n8n, go to **Settings** > **Community Nodes** and search for `n8n-nodes-omnara`.

### Manual Installation

1. Clone or download this repository
2. In the repository folder, run:
   ```bash
   npm install
   npm run build
   ```
3. Copy the `dist` folder to your n8n custom nodes folder
4. Restart n8n

### Development Installation

For development and testing:

```bash
# Install dependencies
npm install

# Build the node
npm run build

# For development with auto-rebuild
npm run dev

# Link to n8n for testing
npm link
# In your n8n installation:
npm link n8n-nodes-omnara
```

## Getting Started

### 1. Get Your API Key

1. Go to [Omnara Dashboard](https://omnara.com/dashboard)
2. Click on your email in the bottom left corner
3. Click "API Keys"
4. Create a new API key and copy it

### 2. Add Omnara Credentials in n8n

1. In n8n, go to Credentials → Add Credential → Omnara API
2. Paste your API key
3. Leave the API URL as default (unless you have a custom deployment)
4. Save the credentials

### 3. Important: Agent Instance ID and Agent Type

**Every Omnara node in your workflow needs:**
- **Agent Instance ID**: A unique UUID for each workflow run (same across all nodes in one run)
- **Agent Type**: The name of your agent on the Omnara dashboard (same across all nodes)

⚠️ **Critical**: Use the SAME agent instance ID and agent type across ALL Omnara nodes in a single workflow run!

## Workflow Setup

### Option A: Trigger with Webhook (Recommended)

This lets you start workflows from the Omnara dashboard.

**Step 1: Set up n8n Webhook**
1. Add a **Webhook** node as your trigger
2. Set it to **POST** method
3. Copy the webhook URL (looks like: `https://your-n8n.com/webhook/xxx`)

**Step 2: Configure Omnara Dashboard**
1. Go to [Omnara Dashboard](https://omnara.com/dashboard)
2. Click the **+ New Instance** button (top left)
3. Paste your webhook URL
4. Create your agent type
5. Send a message to trigger your workflow

**Step 3: Use Webhook Data in Omnara Nodes**
The webhook provides these values - use them in ALL your Omnara nodes:
- **Agent Instance ID**: `{{ $('Webhook').item.json.body.agent_instance_id }}`
- **Agent Type**: `{{ $('Webhook').item.json.body.agent_type }}`

The webhook also provides:
- **Prompt**: `{{ $('Webhook').item.json.body.prompt }}` - This is the message you entered in Omnara dashboard. Use it as input for your workflow if needed.

### Option B: Trigger Without Webhook

For workflows triggered by other events (cron, manual, other apps).

**Requirements:**
1. Generate a unique UUID for Agent Instance ID (use Expression: `{{ $uuid() }}`)
2. Choose an agent type name (e.g., "customer_support", "code_reviewer")
3. Store these in Set node variables
4. Use the SAME values across all Omnara nodes in the workflow

**Example Setup:**
```
Trigger → Set Node (store UUID & agent type) → Your Workflow → Omnara Nodes
```

In Set node:
- `agent_instance_id`: `{{ $uuid() }}`
- `agent_type`: "your_agent_name"

Then reference in all Omnara nodes:
- **Agent Instance ID**: `{{ $('Set').item.json.agent_instance_id }}`
- **Agent Type**: `{{ $('Set').item.json.agent_type }}`

## Operations

### Message Resource

#### Send Message (Non-blocking)
Send status updates and progress reports to users without waiting for a response. Perfect for AI agents to share their thought process, progress updates, or informational messages while continuing to work.

**Parameters:**
- **Agent Instance ID**: The ID of the agent instance (creates new if doesn't exist)
- **Agent Type**: Type of agent (e.g., 'claude_code', 'cursor')
- **Message**: Status update or informational message (workflow continues immediately)
- **Additional Options**:
  - Send Email/SMS/Push: Notification preferences

#### Send and Wait (Blocking)
Send a question or request and WAIT for the user's response. Essential for AI agents that need human input, approvals, or answers to continue their task. The workflow pauses until the user responds via the Omnara web/mobile app.

**Parameters:**
- **Agent Instance ID**: The ID of the agent instance
- **Agent Type**: Type of agent (e.g., 'claude_code', 'cursor')
- **Message**: Question or request that requires user response (workflow pauses until answered)
- **Wait Options**:
  - Timeout: How long to wait (default 24 hours)
  - **Sync Mode (For AI Agents)**: Enable synchronous polling mode for AI Agent compatibility
    - Sync Timeout: Max wait time in sync mode (default 2 hours, max 48 hours)
    - Poll Interval: How often to check for responses (default 5 seconds)
- **Additional Options**: Same as Send Message

**How it works:**

*Normal Mode (for regular workflows):*
1. Sends message to Omnara with `requires_user_input: true`
2. Generates a webhook URL and stores it with the message
3. Pauses workflow execution
4. When user responds in Omnara dashboard, workflow resumes with the response

*Sync Mode (for AI Agent tools):*
1. Sends message to Omnara
2. Polls for responses every 5 seconds
3. Returns immediately when response received
4. Works within AI Agent execution context (avoids async/await issues)

### Session Resource

#### End Session
End an agent session and mark it as completed.

**Parameters:**
- **Agent Instance ID**: The ID of the agent instance session to end

## Usage Examples

### Basic Message Send
```javascript
// Send a status update to your agent
{
  "resource": "message",
  "operation": "send",
  "agentInstanceId": "agent-123",
  "content": "Build completed successfully"
}
```

### Approval Workflow (Regular)
```javascript
// Ask for approval and wait for response
{
  "resource": "message",
  "operation": "sendAndWait",
  "agentInstanceId": "agent-456",
  "agentType": "claude_code",
  "message": "Deploy to production? Please approve or decline.",
  "options": {
    "sendEmail": true,
    "sendPush": true
  }
}
```

### AI Agent Tool Usage (Sync Mode)
```javascript
// Use sync mode when calling from AI Agent tools
{
  "resource": "message",
  "operation": "sendAndWait",
  "agentInstanceId": "agent-789",
  "agentType": "claude_code",
  "message": "What changes should I make to the database schema?",
  "options": {
    "syncMode": true,
    "syncTimeout": 600,  // Wait up to 10 minutes
    "pollInterval": 5,   // Check every 5 seconds
    "sendEmail": true,
    "sendPush": true
  }
}
```

### End Session
```javascript
// Mark agent session as completed
{
  "resource": "session",
  "operation": "end",
  "agentInstanceId": "agent-789"
}
```

## Agent Tool Support

This node is configured with `usableAsTool: true`, making it available as a tool for AI agents in n8n. 

**Important**: When using Send and Wait as an AI Agent tool, you MUST enable **Sync Mode** in the options. This solves the async/await limitations of AI Agents in n8n.

AI Agents can:
- Send status updates
- Ask for human input and wait for responses (using Sync Mode)
- Manage agent sessions

### Why Sync Mode?
Regular Send and Wait uses `putExecutionToWait()` which doesn't work properly in AI Agent context due to n8n's architecture. Sync Mode polls for responses synchronously, allowing AI Agents to wait for human responses without timing out or returning prematurely.

## Webhook Integration

The Send and Wait operation uses n8n's built-in webhook functionality:
- Webhook URLs are automatically generated by n8n
- Omnara stores the webhook URL and triggers it when users respond
- Workflows resume with the user's response data
- Supports timeout configuration (max 7 days)

## Error Handling

The node includes comprehensive error handling:
- Authentication failures
- Invalid agent instance IDs
- Timeout handling for wait operations
- Network errors and API issues

All operations support the "Continue On Fail" option in n8n.

## Development

### Building
```bash
npm run build
```

### Linting
```bash
npm run lint
npm run lint:fix
```

### Code Formatting
```bash
npm run format
```

## Resources

- [Omnara GitHub](https://github.com/omnara-ai/omnara)
- [n8n Documentation](https://docs.n8n.io)
- [n8n Community Nodes](https://docs.n8n.io/integrations/community-nodes/)

## License

MIT

## Support & Feedback

Our mission is to make Omnara the best platform for human-in-the-loop workflows and human-AI collaboration. We're actively improving and would love your input!

- **Issues & feature requests**: [GitHub Issues](https://github.com/omnara-ai/omnara/issues) or contact@omnara.com
- **Questions & support**: contact@omnara.com

Share your ideas on how we can make human-AI interaction better!