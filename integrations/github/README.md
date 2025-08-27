# Omnara GitHub Integration

This integration allows you to trigger AI agents from GitHub issues, PRs, and the Omnara dashboard to automatically respond to comments and perform code tasks.

> **Note:** This integration is based on Claude Code Action v0.x. The upstream repository has released v1.0 with breaking changes. We'll update to v1.0 in a future release.

## Quick Setup Guide

### Prerequisites

1. **GitHub Personal Access Token (PAT)**
   - Go to GitHub Settings → Developer settings → Personal access tokens → Tokens (classic)
   - Click "Generate new token (classic)"
   - Select scopes: `repo` (full control of private repositories)
   - Save the token securely

2. **Omnara API Key**
   - Get your API key from the [Omnara dashboard](https://omnara.ai)
   - You'll need this for webhook configuration

3. **Anthropic API Key**
   - Sign up at [Anthropic](https://console.anthropic.com)
   - Generate an API key from your account settings

### Step 1: Install the Claude Code GitHub App

Visit [github.com/apps/claude-code](https://github.com/apps/claude-code) and install the app on your repository or organization.

### Step 2: Add the Workflow to Your Repository

1. Create `.github/workflows/omnara.yml` in your repository
2. Copy the workflow from [test-workflow.yml](test-workflow.yml)
3. Commit and push the file

### Step 3: Configure Repository Secrets

Add these secrets to your repository (Settings → Secrets and variables → Actions):

- `ANTHROPIC_API_KEY`: Your Anthropic API key
- `OMNARA_API_KEY`: Your Omnara API key (optional if passing via webhook)

### Step 4: Set Up Webhook in Omnara Dashboard

1. Go to the Omnara dashboard
2. Create a new agent with type "GitHub Webhook"
3. Fill in the configuration:
   - **Repository URL**: `https://github.com/YOUR_ORG/YOUR_REPO`
   - **PAT Token**: Your GitHub Personal Access Token
   - **Event Type**: `omnara-trigger` (or custom)

### Step 5: Launch Your Agent

From the Omnara dashboard, you can now:
1. Click "Launch" on your configured agent
2. Enter a prompt for the AI to execute
3. The agent will create a branch, make changes, and optionally create a PR

## How It Works

1. **From Omnara Dashboard**: 
   - You launch an agent with a prompt
   - Omnara sends a `repository_dispatch` event to GitHub
   - GitHub Actions workflow runs with the AI agent

2. **From GitHub Comments** (optional):
   - Comment `@omnara [your request]` on an issue or PR
   - The workflow detects the mention and responds

## Webhook Payload Structure

When triggering from Omnara, the webhook sends:

```json
{
  "event_type": "omnara-trigger",
  "client_payload": {
    "prompt": "Your task for the AI agent",
    "omnara_api_key": "YOUR_OMNARA_API_KEY",
    "agent_instance_id": "unique-instance-id",
    "agent_type": "Claude Code"
  }
}
```

## Testing Your Setup

### Manual Trigger (using curl)

```bash
curl -X POST \
  -H "Authorization: token YOUR_PAT" \
  -H "Accept: application/vnd.github.v3+json" \
  https://api.github.com/repos/YOUR_ORG/YOUR_REPO/dispatches \
  -d '{
    "event_type": "omnara-trigger",
    "client_payload": {
      "prompt": "Add a README file with project description",
      "omnara_api_key": "YOUR_OMNARA_API_KEY",
      "agent_instance_id": "test-123",
      "agent_type": "Claude Code"
    }
  }'
```

### From Omnara Dashboard

1. Navigate to your configured agent
2. Click "Launch"
3. Enter your prompt
4. Monitor progress in GitHub Actions tab

## Configuration Options

The workflow supports these inputs:

- `mode`: Set to `agent` for repository_dispatch, `tag` for @omnara mentions
- `model`: AI model to use (default: `claude-3-5-sonnet-latest`)
- `max_turns`: Maximum conversation turns (default: 30)
- `timeout_minutes`: Execution timeout (default: 30)
- `branch_prefix`: Prefix for created branches (default: `omnara/`)
- `direct_prompt`: The prompt to execute (for agent mode)

**Note:** The workflow automatically uses:
- `mode: agent` for repository_dispatch events
- `mode: tag` for comment triggers (@omnara mentions)

## Architecture

```
Omnara Dashboard
    ↓
GitHub Repository Dispatch API
    ↓
GitHub Actions Workflow
    ↓
Omnara Headless (wraps Claude Code)
    ↓
AI performs tasks in repository
```

## Files in This Integration

- `test-workflow.yml`: Example workflow file for your repository
- `webhook-server.py`: Server component that handles webhook requests
- `claude-code-action/`: GitHub Action implementation (used by the workflow)

## Troubleshooting

### Workflow Not Triggering
- Ensure the workflow file is in the default branch
- Check GitHub Actions is enabled for your repository
- Verify your PAT has `repo` scope

### Authentication Errors
- Verify all secrets are properly set in repository settings
- Ensure PAT token hasn't expired
- Check Omnara API key is valid

### Agent Not Responding
- Check GitHub Actions logs for the workflow run
- Ensure the Claude Code GitHub App is installed
- Verify webhook payload contains all required fields

## Support

- [Omnara Documentation](https://docs.omnara.ai)
- [GitHub Issues](https://github.com/omnara-ai/omnara/issues)
- [Discord Community](https://discord.gg/omnara)