# n8n-nodes-omnara

This is an n8n community node that lets you interact with [Omnara](https://omnara.com) AI agents in your n8n workflows.

Omnara allows you to communicate with your AI agents from anywhere, enabling real-time interaction and human-in-the-loop workflows.

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

## Authentication

1. Get your API key from your [Omnara dashboard](https://app.omnara.io)
2. In n8n, create new Omnara credentials:
   - **API Key**: Your Omnara API key
   - **Server URL**: `https://api.omnara.io` (or your custom deployment URL)

## Operations

### Message Resource

#### Send Message
Send a message to an Omnara agent and optionally receive queued user messages.

**Parameters:**
- **Agent Instance ID**: The ID of the agent instance (creates new if doesn't exist)
- **Message**: The content to send
- **Additional Options**:
  - Agent Type: Type of agent (e.g., 'claude_code', 'cursor')
  - Git Diff: Git diff content to store
  - Send Email/SMS/Push: Notification preferences

#### Send and Wait
Send a message and wait for a user response. Perfect for approval workflows and human-in-the-loop scenarios.

**Parameters:**
- **Agent Instance ID**: The ID of the agent instance
- **Agent Type**: Type of agent (e.g., 'claude_code', 'cursor')
- **Message**: The question or message requiring user input
- **Wait Options**:
  - Timeout: How long to wait (default 24 hours)
  - **Sync Mode (For AI Agents)**: Enable synchronous polling mode for AI Agent compatibility
    - Sync Timeout: Max wait time in sync mode (default 5 min, max 2 hours)
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

## Support

For issues and questions:
- [GitHub Issues](https://github.com/omnara-ai/omnara/issues)
- Email: contact@omnara.com