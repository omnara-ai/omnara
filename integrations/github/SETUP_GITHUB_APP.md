# Testing Omnara GitHub Integration

This guide explains how to test the Omnara GitHub Action in a repository.

## Architecture Overview

There are two ways to trigger the Omnara GitHub Action:

1. **Direct Trigger**: GitHub events (issues, PRs, comments with `@omnara`) → GitHub Action → Omnara Headless → Claude Code
2. **Via GitHub App**: GitHub Webhooks → Your Server → `repository_dispatch` → GitHub Action → Omnara Headless → Claude Code

## Step 1: Create a GitHub App

### Option A: Manual Creation

1. Go to your GitHub account settings (or organization settings)
2. Navigate to **Developer settings** → **GitHub Apps** → **New GitHub App**
3. Fill in the following:

   - **GitHub App name**: `Omnara Bot` (or your preferred name)
   - **Homepage URL**: `https://omnara.ai` (or your URL)
   - **Webhook URL**: Your webhook server URL (e.g., `https://your-server.com/github/webhooks`)
   - **Webhook secret**: Generate a secure secret and save it

4. Set **Permissions**:
   - **Repository permissions**:
     - Actions: Write
     - Contents: Write
     - Issues: Write
     - Metadata: Read
     - Pull requests: Write
     - Checks: Write (optional)
     - Statuses: Write (optional)

5. Subscribe to **Events**:
   - Issues
   - Issue comments
   - Pull request
   - Pull request review
   - Pull request review comment

6. For **Where can this GitHub App be installed?**:
   - Choose "Only on this account" for testing
   - Choose "Any account" for production

7. Click **Create GitHub App**

### Option B: Using the Manifest (Recommended)

1. Go to: https://github.com/settings/apps/new
2. Click "Create GitHub App from a manifest"
3. Paste the contents of `github-app-manifest.json`
4. Review and create the app

## Step 2: Configure the App

After creating the app:

1. **Generate a Private Key**:
   - In your app settings, scroll to "Private keys"
   - Click "Generate a private key"
   - Save the downloaded `.pem` file securely

2. **Note your App ID**:
   - Found at the top of your app settings page
   - You'll need this for configuration

3. **Install the App**:
   - Go to "Install App" in the left sidebar
   - Choose the repositories where you want to install it
   - Click "Install"

## Step 3: Set Up the Webhook Server

### Option A: Using the Provided Server

1. Install dependencies:
   ```bash
   pip install fastapi uvicorn requests
   ```

2. Set environment variables:
   ```bash
   export GITHUB_WEBHOOK_SECRET="your-webhook-secret"
   export GITHUB_PAT="your-github-personal-access-token"
   export OMNARA_API_KEY="your-omnara-api-key"
   ```

3. Run the server:
   ```bash
   python webhook-server.py
   ```

### Option B: Deploy to Cloud (Recommended for Production)

Deploy the webhook server to your preferred platform:

- **Render**: Use the included `render.yaml`
- **Heroku**: Add a `Procfile` with `web: uvicorn webhook-server:app --host 0.0.0.0 --port $PORT`
- **AWS Lambda**: Use AWS API Gateway + Lambda
- **Google Cloud Run**: Deploy as a container

## Step 4: Create the Workflow File

In your target repository, create `.github/workflows/omnara.yml`:

```yaml
name: Omnara AI Assistant

on:
  # Triggered by webhook server
  repository_dispatch:
    types: [omnara-trigger]
  
  # Also support direct triggers
  issue_comment:
    types: [created]
  issues:
    types: [opened, edited]
  pull_request:
    types: [opened, edited]
  pull_request_review_comment:
    types: [created]

permissions:
  contents: write
  issues: write
  pull-requests: write
  actions: write

jobs:
  omnara:
    runs-on: ubuntu-latest
    # Only run if triggered by repository_dispatch or contains @omnara mention
    if: |
      github.event_name == 'repository_dispatch' ||
      contains(github.event.comment.body || github.event.issue.body || github.event.pull_request.body, '@omnara')
    
    steps:
      - uses: actions/checkout@v4
      
      - name: Run Omnara
        uses: ./integrations/github/claude-code-action
        with:
          # Mode configuration
          mode: "tag"
          trigger_phrase: "@omnara"
          
          # Authentication - Using secrets
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
          
          # Model configuration
          model: "claude-3-5-sonnet-latest"
          max_turns: 30
          
        env:
          # Pass Omnara configuration from repository_dispatch payload
          OMNARA_API_KEY: ${{ github.event.client_payload.omnara_api_key || secrets.OMNARA_API_KEY }}
          OMNARA_AGENT_INSTANCE_ID: ${{ github.event.client_payload.agent_instance_id }}
          OMNARA_AGENT_TYPE: ${{ github.event.client_payload.agent_type || 'Claude Code' }}
```

## Step 5: Configure Repository Secrets

Add these secrets to your repository:

1. Go to **Settings** → **Secrets and variables** → **Actions**
2. Add the following secrets:
   - `ANTHROPIC_API_KEY`: Your Anthropic API key (for Claude)
   - `OMNARA_API_KEY`: Your Omnara API key (for the dashboard)
   - `GITHUB_PAT`: Personal Access Token with repo scope (optional, for advanced features)

## Step 6: Test the Integration

### Test via Direct Trigger (No Webhook Server Needed)

1. Create an issue or PR in your repository
2. Add a comment with: `@omnara please analyze this code and suggest improvements`
3. The workflow should trigger automatically

### Test via Webhook Server

1. Ensure your webhook server is running and accessible
2. Use the manual trigger endpoint:

```bash
curl -X POST https://your-webhook-server.com/trigger \
  -H "Content-Type: application/json" \
  -d '{
    "owner": "your-username",
    "repo": "your-repo",
    "prompt": "Help me improve the README file",
    "agent_type": "Claude Code"
  }'
```

### Test via GitHub CLI

```bash
# Trigger repository_dispatch directly
gh api repos/YOUR_USERNAME/YOUR_REPO/dispatches \
  --method POST \
  -H "Accept: application/vnd.github.v3+json" \
  -f event_type='omnara-trigger' \
  -F 'client_payload[prompt]=Help me improve the code' \
  -F 'client_payload[agent_instance_id]=test-123' \
  -F 'client_payload[omnara_api_key]=YOUR_OMNARA_KEY'
```

## Step 7: Monitor Execution

1. **Check GitHub Actions**:
   - Go to the "Actions" tab in your repository
   - Look for the "Omnara AI Assistant" workflow
   - Click on the run to see details

2. **Check Omnara Dashboard**:
   - Log in to your Omnara dashboard
   - Find the agent instance by ID
   - Monitor real-time execution

## Troubleshooting

### Common Issues

1. **Workflow not triggering**:
   - Check that `@omnara` is mentioned in the comment/issue/PR
   - Verify repository_dispatch event is received
   - Check workflow permissions

2. **Authentication errors**:
   - Verify all secrets are correctly set
   - Check that API keys are valid and not expired
   - Ensure PAT has correct scopes

3. **Omnara not connecting**:
   - Verify OMNARA_API_KEY is passed correctly
   - Check that omnara package is installed
   - Verify Claude Code is installed

4. **Webhook signature validation failing**:
   - Ensure webhook secret matches in both GitHub App and server
   - Check that you're using the correct signature header

### Debug Mode

Add debug logging to your workflow:

```yaml
- name: Debug Information
  run: |
    echo "Event: ${{ github.event_name }}"
    echo "Payload: ${{ toJson(github.event.client_payload) }}"
    echo "Omnara configured: ${{ env.OMNARA_API_KEY != '' }}"
```

## Security Considerations

1. **Never commit secrets**: Always use GitHub Secrets
2. **Validate webhooks**: Always verify webhook signatures
3. **Limit permissions**: Only grant necessary permissions to the GitHub App
4. **Use environment restrictions**: Limit which environments can run the action
5. **Rotate tokens regularly**: Periodically update API keys and tokens

## Advanced Configuration

### Custom Agent Types

Pass different agent types in the payload:

```json
{
  "agent_type": "Code Reviewer",
  "prompt": "Review this PR for security issues"
}
```

### Branch-Specific Configuration

Modify the workflow to use different settings per branch:

```yaml
- name: Run Omnara
  uses: ./integrations/github/claude-code-action
  with:
    model: ${{ github.ref == 'refs/heads/main' && 'claude-3-opus-latest' || 'claude-3-5-sonnet-latest' }}
```

### Rate Limiting

Implement rate limiting in your webhook server to prevent abuse:

```python
from slowapi import Limiter
from slowapi.util import get_remote_address

limiter = Limiter(key_func=get_remote_address)
app.state.limiter = limiter

@app.post("/trigger")
@limiter.limit("10/minute")
async def manual_trigger(request: TriggerRequest):
    # ... existing code
```

## Next Steps

1. **Customize the prompt templates** in the action configuration
2. **Add more webhook events** as needed
3. **Integrate with your Omnara dashboard** for better monitoring
4. **Set up alerts** for failed runs
5. **Create custom MCP tools** for domain-specific operations

## Support

- GitHub Action issues: Create an issue in this repository
- Omnara platform issues: Contact support@omnara.ai
- Community: Join the Omnara Discord server