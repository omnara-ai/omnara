# Omnara CLI Authentication Guide

This guide explains how to use the new streamlined authentication flow for Omnara CLI.

## Overview

The new authentication system eliminates the need to manually copy webhook URLs and secrets between your terminal and the Omnara dashboard. Everything is now handled automatically!

## Quick Start

### 1. One-time Login

First, authenticate with your Omnara account:

```bash
omnara login
```

This will:
- Open your browser to the Omnara authentication page
- Securely store your credentials locally in `~/.omnara/config.json`
- Enable automatic webhook registration

### 2. Connect Claude Code

Start a Claude Code session that automatically registers with Omnara:

```bash
omnara connect
```

Or with a custom session name:

```bash
omnara connect --session-name "my-ai-project"
```

This will:
- Start the webhook server with Cloudflare tunnel
- Automatically register the webhook URL and secret with your dashboard
- Display the session name for reference
- No manual copy-paste required!

## Command Reference

### `omnara login`
Authenticate with your Omnara account via OAuth.

Options:
- `--base-url` - Custom Omnara API URL (default: https://agent-dashboard-mcp.onrender.com)

### `omnara connect`
Start a Claude Code webhook session with automatic registration.

Options:
- `--session-name` - Name for this session in the dashboard
- `--port` - Custom port (default: 6662)
- `--local` - Run without Cloudflare tunnel (local only)
- `--dangerously-skip-permissions` - Skip Claude Code permission prompts

### `omnara logout`
Remove stored credentials.

## How It Works

1. **Authentication Flow**:
   - `omnara login` opens your browser to Omnara's OAuth page
   - You authenticate with your preferred provider (GitHub, Google, etc.)
   - Omnara exchanges the auth code for a long-lived API token
   - The token is stored securely in your local config

2. **Webhook Registration**:
   - When you run `omnara connect`, it checks for stored credentials
   - If logged in, it automatically creates/updates a UserAgent in the dashboard
   - The webhook URL and secret are registered without manual intervention
   - You can view and manage sessions in the Omnara dashboard

3. **Session Management**:
   - Each session gets a unique name (or you can specify one)
   - Sessions appear in your dashboard under "Agents"
   - You can have multiple concurrent sessions with different names

## Benefits

- **No Manual Copy-Paste**: Webhook credentials are handled automatically
- **Secure**: OAuth-based authentication with secure local storage
- **Multiple Sessions**: Easy to run and track multiple Claude Code instances
- **Dashboard Integration**: All sessions visible and manageable from the web UI

## Troubleshooting

### "Not logged in to Omnara"
Run `omnara login` first to authenticate.

### OAuth callback fails
Ensure your firewall allows connections to localhost:8765 (the callback server).

### Webhook registration fails
Check that you have an active internet connection and the Omnara API is accessible.

## Legacy Commands

The original commands still work if you prefer manual setup:

```bash
# Manual webhook with copy-paste
omnara --claude-code-webhook --cloudflare-tunnel

# MCP stdio server with manual API key
omnara --api-key YOUR_API_KEY
```

## Security Notes

- Credentials are stored in `~/.omnara/config.json` with restricted permissions (600)
- API tokens expire after 1 year
- You can revoke tokens anytime from the Omnara dashboard
- Use `omnara logout` to remove local credentials