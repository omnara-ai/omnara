# Omnara Code Assistant - User Installation Guide

## Quick Start (3 Steps)

### Step 1: Install the GitHub App
1. Go to [Omnara GitHub App](https://github.com/apps/omnara-code-assistant)
2. Click "Install"
3. Select the repositories you want to use with Omnara
4. Click "Install & Authorize"

### Step 2: Add Your Claude Authentication
Since Omnara uses Claude Code under the hood, you need to provide Claude authentication.

**Option A: Using Anthropic API Key (Most Common)**
1. Go to your repository's Settings → Secrets and variables → Actions
2. Click "New repository secret"
3. Name: `ANTHROPIC_API_KEY`
4. Value: Your Anthropic API key (starts with `sk-ant-`)
5. Click "Add secret"

**Option B: Using AWS Bedrock or Google Vertex AI**
See the [Cloud Providers](#cloud-providers) section below for setup instructions.

### Step 3: Add the Workflow File
Create `.github/workflows/omnara.yml` in your repository:

```yaml
name: Omnara Code Assistant

on:
  repository_dispatch:
    types: [omnara_trigger]

jobs:
  omnara-agent:
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
          # Claude authentication (choose one):
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}  # Option A
          # use_bedrock: true  # Option B - AWS Bedrock
          # use_vertex: true   # Option C - Google Vertex
          base_branch: ${{ github.event.client_payload.branch_name || github.event.default_branch }}
          branch_prefix: "omnara/"
          allowed_tools: "Edit,MultiEdit,Glob,Grep,LS,Read,Write,Bash(git:*)"
        env:
          # Omnara configuration (automatically provided by Omnara platform):
          OMNARA_AGENT_INSTANCE_ID: ${{ github.event.client_payload.agent_instance_id }}
          OMNARA_API_KEY: ${{ github.event.client_payload.omnara_api_key }}
          OMNARA_AGENT_TYPE: ${{ github.event.client_payload.agent_type }}
```

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
- ✅ **Always Human-in-the-loop** - All actions go through Omnara dashboard for review
- ✅ **Real-time monitoring** - Watch your AI agent work in the Omnara dashboard
- ✅ **Branch management** - Automatically creates feature branches
- ✅ **Full repository access** - Can read, write, and modify any files
- ✅ **Git operations** - Can commit, push, and create pull requests
- ✅ **Secure** - API keys are managed through Omnara, not stored in GitHub

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

#### Omnara Configuration (Automatically Provided)
These are passed by the Omnara platform when triggering your workflow:
- `OMNARA_API_KEY` - **Required**: Connects the action to your Omnara dashboard
- `OMNARA_AGENT_INSTANCE_ID` - **Required**: Unique session identifier for tracking
- `OMNARA_AGENT_TYPE` - Optional: The type of agent (defaults to "GitHub Action")

#### Claude Authentication (You Provide)
Since Omnara uses Claude Code under the hood, you need one of:
- `ANTHROPIC_API_KEY` - Direct Anthropic API access
- AWS Bedrock credentials (when `use_bedrock: true`)
- Google Vertex credentials (when `use_vertex: true`)

## Troubleshooting

### Workflow Not Triggering
- Ensure the GitHub App is installed on your repository
- Check that the workflow file is in `.github/workflows/`
- Verify the workflow file name matches what Omnara expects

### Permission Errors
- Make sure the workflow has the correct permissions (contents: write)
- Verify the OMNARA_API_KEY is being passed correctly from the dispatch event

### No Changes Appearing
- Check the Actions tab in your repository to see workflow runs
- Look at the workflow logs for any errors
- Check your Omnara dashboard to see if the agent is waiting for approval
- Ensure your Omnara account has sufficient credits

## Support

- **Documentation**: [Omnara Docs](https://omnara.com/docs)
- **Issues**: [GitHub Issues](https://github.com/omnara/omnara-code-action/issues)
- **Email**: support@omnara.com

## Security

- Your Anthropic API key is stored securely in GitHub Secrets
- The Omnara API key is passed per-session and not stored
- All code changes go through GitHub's security model
- You maintain full control over your repository

## Cloud Providers

### Using AWS Bedrock
If you prefer to use AWS Bedrock instead of the Anthropic API directly:

```yaml
with:
  use_bedrock: true
  model: "anthropic.claude-3-5-sonnet-20241022-v2:0"
env:
  AWS_REGION: ${{ vars.AWS_REGION }}
  AWS_ACCESS_KEY_ID: ${{ secrets.AWS_ACCESS_KEY_ID }}
  AWS_SECRET_ACCESS_KEY: ${{ secrets.AWS_SECRET_ACCESS_KEY }}
```

### Using Google Vertex AI
For Google Cloud users:

```yaml
with:
  use_vertex: true
  model: "claude-3-5-sonnet-v2@20241022"
env:
  ANTHROPIC_VERTEX_PROJECT_ID: ${{ vars.GCP_PROJECT_ID }}
  CLOUD_ML_REGION: ${{ vars.GCP_REGION }}
  GOOGLE_APPLICATION_CREDENTIALS: ${{ secrets.GCP_CREDENTIALS }}
```

## Pricing

- **GitHub Actions**: Standard GitHub Actions pricing applies
- **Claude API**: You provide your own credentials (Anthropic, AWS, or GCP)
- **Omnara Platform**: See [omnara.com/pricing](https://omnara.com/pricing)

## License

MIT - See [LICENSE](LICENSE) file for details