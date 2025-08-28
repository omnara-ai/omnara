# Omnara GitHub Integration

This integration allows you to trigger AI agents from GitHub issues, PRs, and the Omnara dashboard to automatically respond to comments and perform code tasks.

## Quick Setup Guide

### Prerequisites

1. **GitHub Personal Access Token (PAT)**
   - Go to GitHub Settings → Developer settings → Personal access tokens → Tokens (classic)
   - Click "Generate new token (classic)"
   - Select scopes: `repo` (full control of private repositories)
   - Save the token securely

2. **Omnara API Key**
   - Get your API key from the [Omnara dashboard](https://omnara.com)
   - You'll need this for webhook configuration

3. **Anthropic API Key**
   - Sign up at [Anthropic](https://console.anthropic.com)
   - Generate an API key from your account settings

### Step 1: Install the Claude Code GitHub App

Visit [github.com/apps/claude-code](https://github.com/apps/claude-code) and install the app on your repository or organization.

### Step 2: Add the Workflow to Your Repository

1. Create `.github/workflows/omnara.yml` in your repository
2. Copy the workflow from [claude-code-action/examples/omnara.yml](claude-code-action/examples/omnara.yml)
3. Commit and push the file

### Step 3: Configure Repository Secrets

Add these secrets to your repository (Settings → Secrets and variables → Actions):

- `ANTHROPIC_API_KEY`: Your Anthropic API key
- `OMNARA_API_KEY`: Your Omnara API key (optional if passing via webhook)

### Step 4: Set Up Webhook in Omnara Dashboard

1. Go to the Omnara dashboard
2. Create a new agent with type "GitHub Webhook"
3. Fill in the configuration:
   - **Repository**: `YOUR_ORG/YOUR_REPO`
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
   - GitHub Actions workflow runs with Omnara tracking

2. **From GitHub Comments**:
   - **`@omnara [request]`** - Runs with Omnara session tracking (visible in dashboard)
     - Requires `OMNARA_API_KEY` in secrets, falls back to Claude if missing
   - **`@claude [request]`** - Runs standard Claude Code (no tracking, faster)
   
The action automatically chooses based on the trigger phrase!

## Webhook Payload Structure

When triggering from Omnara, the webhook sends:

```json
{
  "event_type": "omnara-trigger",
  "client_payload": {
    "prompt": "Your task for the AI agent",
    "omnara_api_key": "YOUR_OMNARA_API_KEY",
    "agent_instance_id": "unique-instance-id",
    "agent_type": "Claude Code",
    "branch_name": "feature/new-feature"  // Optional: target branch
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
      "agent_type": "Claude Code",
      "branch_name": "docs/add-readme"
    }
  }'
```

### From Omnara Dashboard

1. Navigate to your configured agent
2. Click "Launch"
3. Enter your prompt
4. Monitor progress in GitHub Actions tab

## Configuration Options

The workflow uses Claude Code Action v1.0 with these key inputs:

- `prompt`: Instructions for Claude (auto-detects mode based on presence)
- `claude_args`: Configuration arguments like `--max-turns 30 --model claude-3-5-sonnet-latest`
- `trigger_phrase`: Override to use `@omnara` instead of `@claude`
- `branch_prefix`: Prefix for created branches (default: `claude/`)

The action automatically detects:
- **With prompt provided** → Runs in automation mode (for repository_dispatch)
- **Without prompt** → Waits for @omnara mentions in comments

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

- `claude-code-action/`: GitHub Action v1.0 with smart Omnara integration
  - `examples/omnara-repository-dispatch.yml`: Example workflow for your repository
  - `README-OMNARA.md`: Detailed documentation about the Omnara integration
  - Smart detection: Uses Omnara for `@omnara` mentions, standard Claude for `@claude`

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