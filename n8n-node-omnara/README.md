# n8n-nodes-omnara

This is an n8n community node that allows you to use Omnara as a human-in-the-loop (HITL) approval system in your n8n workflows.

[Omnara](https://github.com/omnara-ai/omnara) is a platform that enables real-time communication between AI agents and humans. This node brings that capability to n8n, allowing you to pause workflows for human approval, send notifications, and track agent sessions.

## Features

- 🛑 **Approval Gates**: Pause workflows and wait for human approval before proceeding
- 📱 **Mobile Notifications**: Get push notifications on your phone via the Omnara app
- 📊 **Session Tracking**: View all workflow interactions in the Omnara dashboard
- ⚡ **Real-time Updates**: Instant response handling when users approve/reject
- 🔄 **Session Management**: Track related workflow steps as a single session

## Installation

### Community Node (Recommended)

1. In n8n, go to **Settings** > **Community Nodes**
2. Search for `n8n-nodes-omnara`
3. Click **Install**

### Manual Installation

```bash
# Navigate to your n8n custom nodes folder
cd ~/.n8n/nodes

# Install the node
npm install n8n-nodes-omnara

# Restart n8n
```

## Setup

### 1. Get Your Omnara API Key

1. Sign up at [Omnara Dashboard](https://agent-dashboard-mcp.onrender.com)
2. Go to Settings > API Keys
3. Create a new API key
4. Copy the JWT token

### 2. Configure Credentials in n8n

1. In n8n, go to **Credentials** > **New**
2. Search for "Omnara API"
3. Enter your API key
4. (Optional) Change the base URL if using a self-hosted instance
5. Save the credentials

## Node Operations

### 1. Send and Wait for Approval

This is the primary operation for human-in-the-loop workflows. It sends a message to the Omnara dashboard and waits for a human response.

**Use Cases:**
- Approve/reject emails before sending
- Confirm data modifications
- Review AI-generated content
- Authorize sensitive operations

**Output:**
```json
{
  "approved": true,  // or false
  "response": "Yes, looks good!",
  "agentInstanceId": "uuid-here",
  "message_id": "uuid-here"
}
```

### 2. Send Message

Send a notification without waiting for a response. Useful for progress updates.

**Use Cases:**
- Log workflow progress
- Send status updates
- Notify about completed tasks

### 3. End Session

Mark an agent session as completed in the Omnara dashboard.

**Use Cases:**
- Clean up after workflow completion
- Signal end of a multi-step process

## Workflow Examples

### Example 1: Email Approval Workflow

```
[Gmail Trigger] 
    ↓
[Draft Reply with AI]
    ↓
[Omnara: Send and Wait for Approval]
    Message: "Should I send this email reply?
              
              To: {{$json["to"]}}
              Subject: {{$json["subject"]}}
              
              Draft:
              {{$json["draft"]}}"
    ↓
[IF Node: Check Approval]
    ├─ IF approved = true → [Send Email]
    └─ IF approved = false → [Send Slack: "Email rejected"]
```

### Example 2: Database Update Approval

```
[Webhook Trigger]
    ↓
[Prepare SQL Query]
    ↓
[Omnara: Send and Wait for Approval]
    Message: "Execute this database update?
              
              Query: {{$json["query"]}}
              Affected Rows: {{$json["affectedRows"]}}"
    Approval Keywords: "yes,execute,run,approve"
    Rejection Keywords: "no,stop,cancel,abort"
    ↓
[Switch Node]
    ├─ approved = true → [Execute Query] → [Omnara: Send Message "Update complete"]
    └─ approved = false → [Log: "Update cancelled"]
    ↓
[Omnara: End Session]
```

### Example 3: Multi-Step Approval Process

```
[Start]
    ↓
[Generate Report]
    ↓
[Omnara: Send and Wait for Approval]
    Message: "Report ready. Should I continue to invoice generation?"
    Agent Instance ID: {{$evaluateExpression("{{$workflow.id}}-{{$execution.id}}")}}
    ↓
[Generate Invoice]
    ↓
[Omnara: Send and Wait for Approval]
    Message: "Invoice generated. Send to customer?"
    Agent Instance ID: {{$node["Omnara"].json["agentInstanceId"]}}  // Same session
    ↓
[Send Invoice]
    ↓
[Omnara: End Session]
    Agent Instance ID: {{$node["Omnara"].json["agentInstanceId"]}}
```

### Example 4: AI Content Review Loop

```
[Trigger]
    ↓
[AI: Generate Content]
    ↓
┌─[Omnara: Send and Wait for Approval]
│   Message: "Is this content acceptable?
│             
│             {{$json["content"]}}"
│   ↓
│ [IF: Check Response]
│   ├─ approved = true → [Publish Content]
│   └─ approved = false AND response contains "revise" → [AI: Revise Content] ↩
```

## Configuration Options

### Additional Options for "Send and Wait for Approval"

- **Timeout (Minutes)**: How long to wait for a response (default: 60)
- **Poll Interval (Seconds)**: How often to check for responses (default: 10)
- **Approval Keywords**: Comma-separated words that indicate approval (default: "yes,approve,ok,continue,proceed,confirmed,accept")
- **Rejection Keywords**: Comma-separated words that indicate rejection (default: "no,reject,stop,cancel,deny,decline,abort")
- **Send Push Notification**: Send mobile push notification (default: true)
- **Send Email**: Send email notification (default: false)
- **Send SMS**: Send SMS notification (default: false)

## Tips and Best Practices

### 1. Use Consistent Agent Instance IDs

For related operations, use the same `agentInstanceId` to group them in the Omnara dashboard:

```javascript
// First node
Agent Instance ID: {{$workflow.id}}-{{$execution.id}}

// Subsequent nodes in same workflow
Agent Instance ID: {{$node["Omnara"].json["agentInstanceId"]}}
```

### 2. Leverage Response Keywords

Configure approval/rejection keywords based on your use case:

- **For yes/no questions**: "yes,no"
- **For technical approvals**: "deploy,rollback"
- **For content review**: "publish,revise,reject"

### 3. Handle Timeouts Gracefully

Always have a fallback plan for when approvals timeout:

```
[Omnara: Wait for Approval]
    ↓
[IF: Error Contains "timeout"]
    ├─ true → [Send Alert: "Approval timed out"]
    └─ false → [Continue normal flow]
```

### 4. Use Message Templates

Create clear, informative approval messages:

```
Title: {{$json["title"]}}
Priority: {{$json["priority"]}}
Requester: {{$json["requester"]}}

Action Required: {{$json["action"]}}

Details:
{{$json["details"]}}

Reply with 'approve' to proceed or 'reject' to cancel.
```

## Troubleshooting

### "Authentication failed"
- Verify your API key is correct
- Check that the base URL matches your Omnara instance

### "No response received within X minutes"
- Increase the timeout value
- Check the Omnara dashboard to see if the message was received
- Ensure someone is available to respond

### "Another process has read the response"
- This happens when multiple nodes try to read the same response
- Ensure you're using unique message IDs or agent instance IDs

## Development

### Building from Source

```bash
# Clone the repository
git clone https://github.com/omnara-ai/n8n-nodes-omnara.git
cd n8n-nodes-omnara

# Install dependencies
npm install

# Build the node
npm run build

# Link for local development
npm link
```

### Testing

1. Link the node to your n8n installation:
```bash
cd ~/.n8n/nodes
npm link n8n-nodes-omnara
```

2. Restart n8n
3. The Omnara node should appear in the nodes panel

## Support

- **Documentation**: [Omnara Docs](https://github.com/omnara-ai/omnara)
- **Issues**: [GitHub Issues](https://github.com/omnara-ai/n8n-nodes-omnara/issues)
- **Community**: [n8n Community Forum](https://community.n8n.io)

## License

MIT - See [LICENSE](LICENSE) file for details

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/AmazingFeature`)
3. Commit your changes (`git commit -m 'Add some AmazingFeature'`)
4. Push to the branch (`git push origin feature/AmazingFeature`)
5. Open a Pull Request

## Changelog

### 0.1.0
- Initial release
- Send and Wait for Approval operation
- Send Message operation
- End Session operation
- Support for push, email, and SMS notifications