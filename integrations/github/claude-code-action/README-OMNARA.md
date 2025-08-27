# Omnara Integration for Claude Code Action v1

This is a fork of [Claude Code Action v1.0](https://github.com/anthropics/claude-code-action) with added support for [Omnara](https://omnara.ai) - a platform that lets you monitor and interact with AI agents in real-time.

## What's Different?

This fork adds:
- **Repository dispatch support** - Trigger Claude from the Omnara dashboard via GitHub's `repository_dispatch` API
- **Session tracking** - All Claude interactions are tracked in your Omnara dashboard
- **Real-time monitoring** - See what Claude is doing as it happens
- **Two-way communication** - Claude can ask questions and receive feedback through Omnara

## Quick Start

### 1. Prerequisites

- **Anthropic API Key** - Get from [console.anthropic.com](https://console.anthropic.com)
- **Omnara API Key** - Get from [omnara.ai](https://omnara.ai)
- **GitHub PAT** - Create a Personal Access Token with `repo` scope

### 2. Add Workflow to Your Repository

Create `.github/workflows/omnara.yml`:

```yaml
name: Omnara AI Assistant

on:
  repository_dispatch:
    types: [omnara-trigger]
  issue_comment:
    types: [created]

permissions:
  contents: write
  issues: write
  pull-requests: write

jobs:
  omnara:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - uses: omnara-ai/omnara/integrations/github/claude-code-action@github-action
        with:
          prompt: ${{ github.event.client_payload.prompt }}
          claude_args: |
            --max-turns 30
            --model claude-3-5-sonnet-latest
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
        env:
          OMNARA_API_KEY: ${{ github.event.client_payload.omnara_api_key }}
          OMNARA_AGENT_INSTANCE_ID: ${{ github.event.client_payload.agent_instance_id }}
          OMNARA_AGENT_TYPE: ${{ github.event.client_payload.agent_type }}
```

### 3. Configure Repository Secrets

Add to your repository's Settings → Secrets:
- `ANTHROPIC_API_KEY` - Your Anthropic API key
- `OMNARA_API_KEY` - Your Omnara API key (optional if passing via webhook)

### 4. Set Up Omnara Dashboard

1. Go to [omnara.ai](https://omnara.ai)
2. Create a new agent with type "GitHub Webhook"
3. Configure:
   - Repository URL: `https://github.com/YOUR_ORG/YOUR_REPO`
   - PAT Token: Your GitHub Personal Access Token
   - Event Type: `omnara-trigger`
4. Launch the agent with your prompt

## How It Works

```mermaid
graph LR
    A[Omnara Dashboard] -->|repository_dispatch| B[GitHub Actions]
    B -->|omnara headless| C[Claude Code CLI]
    C -->|session updates| A
    C -->|makes changes| D[Your Repository]
```

1. You launch an agent from the Omnara dashboard with a prompt
2. Omnara sends a `repository_dispatch` event to GitHub
3. GitHub Actions runs this action, which uses `omnara headless` instead of plain Claude Code
4. Omnara tracks the session and shows real-time updates in your dashboard
5. Claude makes changes to your repository as requested

## Key Features

### Smart Detection: @claude vs @omnara

The action intelligently chooses between standard Claude and Omnara based on context:

- **`@omnara` mentions** → Uses Omnara headless (with session tracking)
  - ⚠️ If `OMNARA_API_KEY` is missing, falls back to standard Claude with a warning
- **`@claude` mentions** → Uses standard Claude Code (no tracking)
- **`repository_dispatch`** → Uses Omnara headless (when OMNARA_API_KEY present)

This allows you to use both in the same repository:
```yaml
trigger_phrase: "@omnara"  # For tracked sessions (requires OMNARA_API_KEY in secrets)
# or
trigger_phrase: "@claude"  # For untracked, quick interactions
```

**Note:** If you use `@omnara` without setting `OMNARA_API_KEY` in your repository secrets, the action will:
1. Print a warning in the logs
2. Fall back to standard Claude Code (without tracking)
3. Still execute your request normally

### Branch Management

When triggered via `repository_dispatch`, you can specify a target branch:
- If `branch_name` is in the payload:
  - **Branch exists** → Checks out that branch
  - **Branch doesn't exist** → Creates a new branch from main/master
- If no `branch_name` → Uses the default branch

### Session Tracking

When triggered with `@omnara` or via repository_dispatch, the action:
- Installs the Omnara Python package
- Uses `omnara headless` command instead of `claude-code`
- Tracks the session in your Omnara dashboard
- Enables real-time monitoring and two-way communication

### Environment Variables

Required for Omnara integration:
- `OMNARA_API_KEY` - Your Omnara API key
- `OMNARA_AGENT_INSTANCE_ID` - Unique session ID (auto-generated if not provided)
- `OMNARA_AGENT_TYPE` - Agent name shown in dashboard (default: "GitHub Action")

## Triggering from Omnara

The webhook payload from Omnara:

```json
{
  "event_type": "omnara-trigger",
  "client_payload": {
    "prompt": "Add tests for the authentication module",
    "omnara_api_key": "YOUR_API_KEY",
    "agent_instance_id": "unique-session-id",
    "agent_type": "Claude Code",
    "branch_name": "feature/add-auth-tests"  // Optional: specify target branch
  }
}
```

## Manual Testing

Test with curl:

```bash
curl -X POST \
  -H "Authorization: token YOUR_PAT" \
  -H "Accept: application/vnd.github.v3+json" \
  https://api.github.com/repos/YOUR_ORG/YOUR_REPO/dispatches \
  -d '{
    "event_type": "omnara-trigger",
    "client_payload": {
      "prompt": "Add a README file",
      "omnara_api_key": "YOUR_OMNARA_KEY",
      "agent_instance_id": "test-123",
      "agent_type": "Claude Code",
      "branch_name": "feature/add-readme"
    }
  }'
```

## Differences from Upstream

This fork adds:
1. **`src/github/context.ts`** - Added `RepositoryDispatchEvent` type and handling
2. **`base-action/src/run-omnara.ts`** - New file that runs `omnara headless` instead of `claude-code`
3. **`base-action/src/index.ts`** - Modified to use `runOmnara` when `OMNARA_API_KEY` is present
4. **`action.yml`** - Added Omnara installation step and environment variable passing

## Using Both @claude and @omnara

This fork supports both triggers in the same repository:

```yaml
- uses: omnara-ai/omnara/integrations/github/claude-code-action@github-action
  with:
    trigger_phrase: "@claude"  # Or "@omnara" for tracking
    anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
  env:
    OMNARA_API_KEY: ${{ secrets.OMNARA_API_KEY }}  # Optional
```

- **@claude** → Standard Claude Code (no tracking)
- **@omnara** → Omnara headless (with session tracking, requires OMNARA_API_KEY)

## Support

- [Omnara Documentation](https://docs.omnara.ai)
- [Original Claude Code Action](https://github.com/anthropics/claude-code-action)
- [Report Issues](https://github.com/omnara-ai/omnara/issues)