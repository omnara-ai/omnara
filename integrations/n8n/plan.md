# n8n Integration for Omnara - Implementation Plan

## Overview
This integration enables n8n workflows to communicate with Omnara agents, providing human-in-the-loop functionality similar to Discord, Slack, and Telegram integrations.

## Architecture Decision: Webhook-based Approach

We're implementing the cleaner webhook-based approach that leverages n8n's built-in Wait node functionality. This follows the established pattern used by Discord, Slack, and other communication platform integrations.

### How It Works
1. **Omnara node** sends a message to the Omnara API
2. **n8n Wait node** (configured to "Resume on Webhook") generates a unique webhook URL
3. **Omnara backend** stores this webhook URL with the message
4. When user responds in Omnara dashboard, backend calls the webhook
5. **n8n workflow** resumes with the user's response data

## Node Operations

### 1. Send Message
**Purpose**: Send a simple notification or update to Omnara
- **Inputs**:
  - Content (string, required)
  - Agent Type (string, optional)
  - Agent Instance ID (string, optional)
  - Send Push/Email/SMS (boolean, optional)
  - Git Diff (string, optional)
- **Outputs**:
  - Success (boolean)
  - Message ID (string)
  - Agent Instance ID (string)
  - Queued User Messages (array)

### 2. Send Message (Wait Ready)
**Purpose**: Send a message that's prepared for use with Wait node
- **Inputs**: Same as Send Message, plus:
  - Webhook URL Expression (string, optional) - for passing `$resumeWebhookUrl`
- **Outputs**: Same as Send Message, plus:
  - Webhook URL (string) - to be stored in Omnara backend
- **Note**: User should connect this to a Wait node configured for webhook resume

### 3. End Session
**Purpose**: Mark an agent session as completed
- **Inputs**:
  - Agent Instance ID (string, required)
- **Outputs**:
  - Success (boolean)
  - Final Status (string)

## Implementation Structure

```
integrations/n8n/
├── package.json                    # NPM package configuration
├── tsconfig.json                   # TypeScript configuration
├── README.md                       # User documentation
├── plan.md                         # This file
├── credentials/
│   └── OmnaraApi.credentials.ts   # API key credential definition
├── nodes/
│   └── Omnara/
│       ├── Omnara.node.ts         # Main node implementation
│       ├── GenericFunctions.ts    # HTTP client and utilities
│       └── omnara.svg             # Node icon
└── dist/                           # Built files (gitignored)
```

## API Integration Details

### Required Endpoints
1. **POST /api/v1/messages/agent** - Send agent messages
2. **POST /api/v1/sessions/end** - End agent sessions
3. **GET /api/v1/messages/pending** - Get pending messages (for future polling support)

### Authentication
- JWT Bearer token in Authorization header
- API key stored securely in n8n credentials

### Backend Modifications Needed
To support the webhook approach, Omnara backend needs:
1. Add `webhook_url` field to message creation endpoint
2. When user responds, make HTTP POST to webhook URL with:
   ```json
   {
     "message_id": "...",
     "agent_instance_id": "...",
     "user_response": "...",
     "timestamp": "..."
   }
   ```

## Development Steps

1. **Create Base Structure** ✓
   - Set up directory structure
   - Create this plan document

2. **Package Configuration**
   - package.json with n8n node configuration
   - tsconfig.json for TypeScript compilation

3. **Credential Implementation**
   - OmnaraApi.credentials.ts with API key field
   - Connection test method

4. **Core Utilities**
   - GenericFunctions.ts with HTTP client
   - Error handling utilities
   - Response parsing helpers

5. **Node Implementation**
   - Omnara.node.ts with three operations
   - Proper parameter definitions
   - Error handling with n8n error types

6. **Icon and Documentation**
   - Create/obtain Omnara SVG icon
   - Write comprehensive README

7. **Testing and Building**
   - Local testing with n8n
   - Build process verification
   - NPM package preparation

## Usage Examples

### Example 1: Simple Notification
```
[Trigger] → [Omnara: Send Message] → [Continue Workflow]
```

### Example 2: Human Approval Flow
```
[Trigger] → [Omnara: Send Message (Wait Ready)] → [Wait (Webhook)] → [Process Response]
```
- Omnara node sends message and passes webhook URL
- Wait node pauses execution
- User responds in Omnara dashboard
- Omnara backend calls webhook
- Workflow continues with response data

### Example 3: Complex Decision Flow
```
[Data Processing] → [Omnara: Send Message (Wait Ready)] → [Wait] → [Switch on Response] → [Different Paths]
```

## Publishing Strategy

1. **Local Development**
   - Test with local n8n instance
   - Verify all operations work correctly

2. **NPM Package**
   - Publish as `n8n-nodes-omnara`
   - Follow n8n naming conventions

3. **Community Submission**
   - Submit to n8n community nodes
   - Provide documentation and examples
   - Include test workflows

## Future Enhancements

1. **Polling Support** - Add fallback polling for environments without webhook access
2. **File Attachments** - Support sending files with messages
3. **Bulk Operations** - Send messages to multiple instances
4. **Advanced Filtering** - Filter queued messages by type/timestamp
5. **MCP Tool Integration** - Create corresponding MCP tools for agents

## Success Criteria

- [ ] All three operations work reliably
- [ ] Webhook-based waiting functions correctly
- [ ] Clear documentation and examples
- [ ] Follows n8n best practices
- [ ] Can be installed via Community Nodes
- [ ] Integrates seamlessly with existing n8n workflows