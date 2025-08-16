# n8n-nodes-omnara

This is an n8n community node that lets you use Omnara in your n8n workflows.

[Omnara](https://github.com/omnara/omnara) is a platform that enables real-time communication between AI agents and humans, providing human-in-the-loop functionality for automated workflows.

[n8n](https://n8n.io/) is a [fair-code licensed](https://docs.n8n.io/reference/license/) workflow automation platform.

## Installation

### Community Nodes (Recommended)

1. Go to **Settings** > **Community Nodes**
2. Select **Install**
3. Enter `n8n-nodes-omnara` in the package name field
4. Click **Install**

### Manual Installation

To install this node manually in your n8n instance:

```bash
npm install n8n-nodes-omnara
```

## Prerequisites

Before using this node, you need:

1. An Omnara account with API access
2. An API key from your Omnara dashboard
3. The Omnara API URL (default: `https://agent-dashboard-mcp.onrender.com`)

## Operations

### Send Message
Send a simple notification or update to Omnara without waiting for a response.

**Parameters:**
- **Message Content**: The message to send
- **Agent Instance ID**: (Optional) Existing agent instance to use
- **Agent Type**: Required when creating a new instance
- **Additional Options**: Notifications settings, git diff, etc.

### Send Message (Wait Ready)
Send a message that's prepared for use with n8n's Wait node for human-in-the-loop workflows.

**Parameters:**
- Same as Send Message, plus:
- **Webhook URL**: The resume webhook URL from a Wait node

### End Session
Mark an agent session as completed.

**Parameters:**
- **Agent Instance ID**: The instance to end

## Usage Examples

### Example 1: Simple Notification

```
[HTTP Request Trigger] → [Omnara: Send Message] → [Email Send]
```

Send a notification to Omnara when an API is called, then send an email.

### Example 2: Human Approval Workflow

```
[Schedule Trigger] → [Get Data] → [Omnara: Send Message (Wait Ready)] → [Wait Node] → [Process Approval]
```

1. Configure the Omnara node to send a message requesting approval
2. Add a Wait node set to "Resume on Webhook"
3. Pass the Wait node's `$resumeWebhookUrl` to the Omnara node
4. When a user responds in Omnara, the workflow continues

**Omnara Node Configuration:**
- Operation: Send Message (Wait Ready)
- Message Content: "Please approve the deployment of version 2.0"
- Webhook URL: `{{$node["Wait"].json["$resumeWebhookUrl"]}}`

**Wait Node Configuration:**
- Resume: On Webhook Call
- Response: Using 'Respond to Webhook' Node

### Example 3: Multi-Step Approval Process

```
[Trigger] → [Data Processing] → [Omnara: Send Message] → [Wait] → [Switch on Response] → [Different Actions]
```

Process data, request human input, and take different actions based on the response.

## Credentials

You'll need to configure Omnara API credentials:

1. Go to **Credentials** > **New**
2. Select **Omnara API**
3. Enter your API Key
4. Enter the API URL (or use the default)
5. Click **Save**

## Backend Requirements

For the webhook-based human-in-the-loop functionality to work, your Omnara backend needs to support:

1. Accepting a `webhook_url` parameter when creating messages
2. Calling the webhook URL when a user responds with the response data

## Resources

- [Omnara Documentation](https://github.com/omnara/omnara)
- [n8n Community Nodes Documentation](https://docs.n8n.io/integrations/community-nodes/)

## Development

To build this node locally:

```bash
# Install dependencies
npm install

# Build the node
npm run build

# Link for local testing
npm link
```

Then in your n8n installation:

```bash
npm link n8n-nodes-omnara
```

## Testing

1. Install the node in your n8n instance
2. Create Omnara API credentials
3. Create a test workflow with the Omnara node
4. Test each operation:
   - Send a simple message
   - Test wait functionality with a Wait node
   - End a session

## License

[MIT](LICENSE.md)

## Support

For issues and feature requests, please use the [GitHub Issues](https://github.com/omnara/omnara/issues) page.

## AI Agent Integration

This package includes tools that can be used with n8n's AI Agent nodes.

### Using with AI Agents

The Omnara node is marked as `usableAsTool`, which means it can be connected to AI Agent nodes. Additionally, there's a dedicated `Omnara Tool` node specifically designed for AI Agent workflows.

#### Available AI Agent Tools:

1. **Send Message** - Let the AI agent send status updates to humans
2. **Ask for Human Input** - Allow the AI to request human intervention
3. **End Session** - Let the AI properly close agent sessions

#### Example AI Agent Workflow:

```
[Chat Trigger] → [AI Agent] → [Omnara Tool: Send Message]
                      ↓
              [OpenAI Model]
```

Configure the Omnara Tool with:
- Operation: Choose the appropriate action
- Description for AI: Explain when to use this tool
- Let the AI manage the agent instance ID

The AI agent will automatically determine when to use the Omnara tools based on the context and your descriptions.

## Version History

### 0.1.0
- Initial release
- Send Message operation
- Send Message (Wait Ready) operation
- End Session operation
- Support for webhook-based human-in-the-loop workflows
- AI Agent tool support with dedicated OmnaraTool node