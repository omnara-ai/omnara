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

### Step 4: Set Up Webhook in Omnara Dashboard

1. Go to the Omnara dashboard
2. Create a new agent any name, and then assign it with type "GitHub Webhook"
3. Fill in the configuration:
   - **Repository**: `YOUR_ORG/YOUR_REPO`
   - **PAT Token**: Your GitHub Personal Access Token
   - **Event Type**: `omnara-trigger`

### Step 5: Launch Your Agent

From the Omnara dashboard, you can now:
1. Click "Launch" or "+" on your configured agent
2. Enter a prompt for the AI to execute
3. The agent will create a branch, make changes, and optionally create a PR if you ask it to