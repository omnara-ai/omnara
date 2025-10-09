# CLI Reference

The Omnara CLI provides multiple commands for running and managing AI coding agents with dashboard integration.

## Installation

```bash
pip install omnara
```

## Quick Start

```bash
# Start Claude Code with full integration
omnara

# Start with a different agent
omnara --agent codex
omnara --agent amp
```

## Commands

### Default Command (Interactive Session)

Start an AI coding agent with full terminal integration and dashboard sync.

```bash
omnara [OPTIONS]
```

**Common Options:**
- `--agent <name>` - Choose agent: `claude` (default), `codex`, or `amp`
- `--api-key <key>` - API key for authentication (or set `OMNARA_API_KEY`)
- `--name <display_name>` - Custom display name for the dashboard
- `--no-relay` - Disable WebSocket streaming (local-only session)
- `--agent-instance-id <id>` - Resume an existing session

**Examples:**
```bash
# Start Claude Code (default)
omnara

# Start Codex with custom name
omnara --agent codex --name "Backend Refactor"

# Local-only session without dashboard streaming
omnara --no-relay
```

### `omnara headless`

Run Claude Code in background mode without a terminal UI. Perfect for dashboard-only interaction or automation.

```bash
omnara headless [OPTIONS]
```

**Options:**
- `--prompt <text>` - Initial prompt to send (default: "You are starting a coding session")
- `--permission-mode <mode>` - Permission handling:
  - `acceptEdits` - Auto-accept all edits
  - `bypassPermissions` - Bypass all permission checks
  - `plan` - Planning mode
  - `default` - Normal prompts
- `--allowed-tools <list>` - Comma-separated tool whitelist (e.g., `Read,Write,Bash`)
- `--disallowed-tools <list>` - Comma-separated tool blacklist
- `--cwd <path>` - Working directory (defaults to current)

**Examples:**
```bash
# Basic headless session
omnara headless

# Auto-accept edits with specific tools
omnara headless --permission-mode acceptEdits --allowed-tools Read,Write,Bash

# Start with custom prompt
omnara headless --prompt "Review and refactor the auth module" --cwd /path/to/project
```

### `omnara serve`

Start a webhook server that allows remote triggering of Claude Code sessions from the dashboard or other integrations.

```bash
omnara serve [OPTIONS]
```

**Options:**
- `--no-tunnel` - Run locally without Cloudflare tunnel (default: tunnel enabled)
- `--port <number>` - Server port (default: 6662)
- Permission flags are passed through to Claude Code instances

**Examples:**
```bash
# Start with Cloudflare tunnel (public URL)
omnara serve

# Local-only webhook server
omnara serve --no-tunnel --port 8080

# With permission settings
omnara serve --permission-mode acceptEdits
```

**Usage:**
1. Run `omnara serve` to start the webhook server
2. Copy the displayed webhook URL and API key
3. Configure in your Omnara dashboard under Settings → Integrations
4. Trigger Claude Code sessions remotely from your phone or web dashboard

### `omnara mcp`

Run the MCP (Model Context Protocol) stdio server for integration with MCP-compatible clients.

```bash
omnara mcp [OPTIONS]
```

**Options:**
- `--api-key <key>` - API key for authentication (required)
- `--permission-tool` - Enable Claude Code permission prompt tool
- `--git-diff` - Enable automatic git diff capture for messages
- `--agent-instance-id <id>` - Use existing agent instance
- `--disable-tools` - Disable all tools except permission tool

**Examples:**
```bash
# Basic MCP server
omnara mcp --api-key YOUR_KEY

# With git diff tracking and permission prompts
omnara mcp --api-key YOUR_KEY --git-diff --permission-tool
```

**MCP Client Configuration:**

For Claude Desktop (`claude_desktop_config.json`):
```json
{
  "mcpServers": {
    "omnara": {
      "command": "omnara",
      "args": ["mcp", "--api-key", "YOUR_API_KEY"]
    }
  }
}
```

For pipx installations:
```json
{
  "mcpServers": {
    "omnara": {
      "command": "pipx",
      "args": ["run", "--no-cache", "omnara", "mcp", "--api-key", "YOUR_API_KEY"]
    }
  }
}
```

## Authentication

### First-Time Setup

```bash
omnara --auth
```

Opens your browser for authentication. The API key is automatically saved to `~/.omnara/credentials.json`.

### Re-authenticate

```bash
omnara --reauth
```

Forces re-authentication even if credentials exist.

### Environment Variable

```bash
export OMNARA_API_KEY="your-api-key-here"
omnara
```

### Manual API Key

```bash
omnara --api-key YOUR_API_KEY
```

## Agent Configuration

### Set Default Agent

```bash
# Set default and exit
omnara --set-default codex

# Set default and launch immediately
omnara --agent amp --set-default
```

The default is stored in `~/.omnara/config.json` and used for all future sessions.

### Available Agents

- **claude** (default) - Anthropic's Claude Code
- **codex** - Codex CLI
- **amp** - Amp CLI

## Environment Variables

| Variable | Description |
|----------|-------------|
| `OMNARA_API_KEY` | API key for authentication |
| `OMNARA_API_URL` | API server URL (default: `https://agent.omnara.com`) |
| `OMNARA_AGENT_INSTANCE_ID` | Pre-existing session ID to resume |
| `OMNARA_AGENT_DISPLAY_NAME` | Display name in dashboard |
| `OMNARA_RELAY_DISABLED` | Set to `1` to disable WebSocket relay |
| `OMNARA_CODEX_PATH` | Path to Codex binary (Codex agent only) |

## Configuration Files

### `~/.omnara/credentials.json`

Stores authentication credentials (API keys).

```json
{
  "write_key": "omr_xxxxxxxxxxxxxxxxxxxx"
}
```

### `~/.omnara/config.json`

Stores user preferences (non-sensitive settings).

```json
{
  "default_agent": "claude"
}
```

## Global Options

These options work across all commands:

- `--version` - Show version information
- `--auth` - Authenticate or re-authenticate
- `--reauth` - Force re-authentication
- `--base-url <url>` - Omnara API server URL
- `--auth-url <url>` - Authentication frontend URL

## Version Information

```bash
omnara --version
```

## Examples

### Development Workflow

```bash
# Initial setup
omnara --auth

# Set Claude as default
omnara --set-default claude

# Start a coding session
omnara --name "Feature Development"
```

### Remote Triggering

```bash
# Terminal 1: Start webhook server
omnara serve

# Terminal 2: Trigger from another machine via dashboard
# Use the webhook URL displayed in Terminal 1
```

### Headless Automation

```bash
# Run automated code review
omnara headless \
  --prompt "Review all files in src/ for security issues" \
  --permission-mode acceptEdits \
  --allowed-tools Read,Grep \
  --cwd /path/to/project
```

### MCP Integration

```bash
# Start MCP server with full features
omnara mcp \
  --api-key YOUR_KEY \
  --git-diff \
  --permission-tool
```

## Troubleshooting

### Authentication Issues

If authentication fails:
```bash
omnara --reauth
```

### Connection Issues

Check your API configuration:
```bash
cat ~/.omnara/credentials.json
cat ~/.omnara/config.json
```

### Log Locations

Logs are stored in:
- Claude Code: `~/.omnara/claude_wrapper/<session-id>.log`
- Headless: `~/.omnara/claude_headless/<session-id>.log`
- Codex: `~/.omnara/codex_wrapper/<session-id>.log`
- Amp: `~/.omnara/amp_wrapper/<session-id>.log`

## Upgrade

```bash
# Using pip
pip install omnara --upgrade

# Using uv
uv tool upgrade omnara

# Using pipx
pipx upgrade omnara
```
