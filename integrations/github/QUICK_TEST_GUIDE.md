# Quick Test Guide for Omnara GitHub Integration

## What This Does

This integration allows you to trigger Omnara (which runs Claude Code) from GitHub issues and PRs by mentioning `@omnara`.

## Option 1: Simple Test (Direct GitHub Events)

### 1. Copy the Workflow to Your Test Repo

Copy `test-workflow.yml` to `.github/workflows/omnara.yml` in your test repository.

### 2. Add Required Secrets

In your repository settings → Secrets → Actions, add:
- `ANTHROPIC_API_KEY` - Your Anthropic API key for Claude
- `OMNARA_API_KEY` - Your Omnara dashboard API key

### 3. Test It

Create an issue or PR comment with:
```
@omnara help me write a hello world function in Python
```

The workflow will:
1. Trigger on the `@omnara` mention
2. Run omnara headless with your prompt
3. Connect to your Omnara dashboard
4. Execute Claude Code to respond

## Option 2: Test via repository_dispatch (API/CLI)

### Using GitHub CLI

```bash
# Replace YOUR_REPO with your actual repo (e.g., "username/repo-name")
gh api repos/YOUR_REPO/dispatches \
  --method POST \
  -f event_type='omnara-trigger' \
  -F 'client_payload[prompt]=Write a Python hello world function' \
  -F 'client_payload[omnara_api_key]=YOUR_OMNARA_API_KEY' \
  -F 'client_payload[agent_instance_id]=test-123'
```

### Using curl

```bash
# You need a GitHub Personal Access Token (PAT) with repo scope
curl -X POST \
  -H "Authorization: Bearer YOUR_GITHUB_PAT" \
  -H "Accept: application/vnd.github.v3+json" \
  https://api.github.com/repos/YOUR_REPO/dispatches \
  -d '{
    "event_type": "omnara-trigger",
    "client_payload": {
      "prompt": "Write a Python hello world function",
      "omnara_api_key": "YOUR_OMNARA_API_KEY",
      "agent_instance_id": "test-123"
    }
  }'
```

## Option 3: Full GitHub App Setup (Production)

### Why Use a GitHub App?

- Centralized webhook handling for multiple repos
- No need to add workflows to each repository
- Better security with app-level permissions

### Setup Steps

1. **Deploy the Webhook Server**
   - Use `webhook-server.py` 
   - Deploy to any platform (Render, Heroku, AWS, etc.)
   - Set environment variables:
     - `GITHUB_WEBHOOK_SECRET` - A secure random string
     - `GITHUB_PAT` - GitHub token with repo access
     - `OMNARA_API_KEY` - Your Omnara API key

2. **Create GitHub App**
   - Go to GitHub Settings → Developer settings → GitHub Apps → New
   - Use `github-app-manifest.json` as a template
   - Set webhook URL to your deployed server

3. **Install the App**
   - Install it on your test repository
   - It will now listen for `@omnara` mentions and trigger workflows

## Monitoring

### Check GitHub Actions
Go to Actions tab in your repo to see workflow runs.

### Check Omnara Dashboard
Log into omnara.com to see your agent instances and their execution.

## Troubleshooting

**Workflow not triggering?**
- Check if `@omnara` is in the comment/issue
- Verify secrets are set correctly
- Check Actions tab for any error messages

**Authentication errors?**
- Verify ANTHROPIC_API_KEY is valid
- Verify OMNARA_API_KEY is valid
- Check that both are set as repository secrets

**Omnara not connecting?**
- Check the agent instance ID in Omnara dashboard
- Verify the API key has correct permissions

## What Happens When It Works

1. You mention `@omnara` with a request
2. GitHub Action triggers
3. Omnara headless starts and connects to dashboard
4. Claude Code processes your request
5. Response appears as a GitHub comment
6. You can monitor progress in Omnara dashboard

## Next Steps

- Customize the trigger phrase (change from `@omnara` to something else)
- Add custom instructions in the workflow
- Set up different models or parameters
- Create branch-specific configurations