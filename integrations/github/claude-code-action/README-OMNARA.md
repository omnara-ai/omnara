# Omnara Code Assistant - User Installation Guide

## Quick Start (3 Steps)

### Step 1: Install the GitHub App
1. Go to [Omnara GitHub App](https://github.com/apps/omnara-code-assistant)
2. Click "Install"
3. Select the repositories you want to use with Omnara
4. Click "Install & Authorize"

### Step 2: Add the Workflow File
Create `.github/workflows/omnara.yml` in your repository:

```yaml
name: Omnara Code Assistant

on:
  repository_dispatch:
    types: [omnara_trigger]

jobs:
  omnara-claude:
    runs-on: ubuntu-latest
    permissions:
      contents: write
      issues: write
      pull-requests: write
      actions: read
      
    steps:
      - name: Checkout repository
        uses: actions/checkout@v4
        with:
          ref: ${{ github.event.client_payload.branch_name || github.event.default_branch }}
      
      - name: Run Omnara Code Action
        uses: omnara/omnara-code-action@main
        with:
          mode: "agent"
          direct_prompt: "${{ github.event.client_payload.prompt }}"
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
          base_branch: ${{ github.event.client_payload.branch_name || github.event.default_branch }}
          branch_prefix: "omnara/"
          allowed_tools: "Edit,MultiEdit,Glob,Grep,LS,Read,Write,Bash(git:*)"
        env:
          OMNARA_AGENT_INSTANCE_ID: ${{ github.event.client_payload.agent_instance_id }}
          OMNARA_API_KEY: ${{ github.event.client_payload.omnara_api_key }}
          OMNARA_AGENT_TYPE: ${{ github.event.client_payload.agent_type }}
```

### Step 3: Add Your Anthropic API Key
1. Go to your repository's Settings → Secrets and variables → Actions
2. Click "New repository secret"
3. Name: `ANTHROPIC_API_KEY`
4. Value: Your Anthropic API key (starts with `sk-ant-`)
5. Click "Add secret"

## That's It! 🎉

Your repository is now ready to use with Omnara. No servers to set up, no webhooks to configure.

## How to Use

### From Omnara Platform
1. Log into [Omnara](https://omnara.com)
2. Connect your GitHub repository using your installation ID
3. Create a task with your prompt
4. Omnara will trigger the workflow in your repository
5. Review and approve changes through Omnara's human-in-the-loop interface
6. Changes are automatically committed to your repository

### Getting Your Installation ID
After installing the GitHub App, you can find your installation ID:
1. Go to Settings → Integrations → GitHub Apps
2. Click "Configure" next to Omnara Code Assistant
3. The URL will contain your installation ID: `github.com/settings/installations/{INSTALLATION_ID}`

## Features

- ✅ **No hosting required** - Runs entirely on GitHub Actions
- ✅ **Human-in-the-loop** - Review and approve AI changes before they're applied
- ✅ **Branch management** - Automatically creates feature branches
- ✅ **Full repository access** - Can read, write, and modify any files
- ✅ **Git operations** - Can commit, push, and create pull requests
- ✅ **Secure** - Your API keys never leave GitHub's secure environment

## Customization

### Workflow Options
You can customize the workflow by modifying the action parameters:

```yaml
- uses: omnara/omnara-code-action@main
  with:
    # Limit to specific tools
    allowed_tools: "Read,Write,Edit"
    
    # Set maximum conversation turns
    max_turns: "10"
    
    # Use a specific model
    model: "claude-3-opus-20240229"
    
    # Custom branch prefix
    branch_prefix: "ai/"
```

### Environment Variables
The following environment variables are automatically set by Omnara:

- `OMNARA_AGENT_INSTANCE_ID` - Unique session identifier
- `OMNARA_API_KEY` - For callbacks to Omnara platform
- `OMNARA_AGENT_TYPE` - The type of agent (debugging, feature, etc.)

## Troubleshooting

### Workflow Not Triggering
- Ensure the GitHub App is installed on your repository
- Check that the workflow file is in `.github/workflows/`
- Verify the workflow file name matches what Omnara expects

### Permission Errors
- Make sure the workflow has the correct permissions (contents: write)
- Verify your Anthropic API key is set correctly

### No Changes Appearing
- Check the Actions tab in your repository to see workflow runs
- Look at the workflow logs for any errors
- Ensure your Anthropic API key has sufficient credits

## Support

- **Documentation**: [Omnara Docs](https://omnara.com/docs)
- **Issues**: [GitHub Issues](https://github.com/omnara/omnara-code-action/issues)
- **Email**: support@omnara.com

## Security

- Your Anthropic API key is stored securely in GitHub Secrets
- The Omnara API key is passed per-session and not stored
- All code changes go through GitHub's security model
- You maintain full control over your repository

## Pricing

- **GitHub Actions**: Standard GitHub Actions pricing applies
- **Anthropic API**: You use your own API key and credits
- **Omnara Platform**: See [omnara.com/pricing](https://omnara.com/pricing)

## License

MIT - See [LICENSE](LICENSE) file for details