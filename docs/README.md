# docs/

This directory contains the documentation for the Omnara project.

## Structure

- **`cli/`** - CLI command reference
  - `overview.mdx` - CLI overview and installation
  - `commands/` - Individual command documentation
    - `default.mdx` - Default interactive command
    - `terminal.mdx` - WebSocket relay streaming
    - `headless.mdx` - Background mode
    - `serve.mdx` - Webhook server
    - `mcp.mdx` - MCP stdio server
  - `agents.mdx` - Agent configuration
  - `environment-variables.mdx` - Environment variable reference
  - `config-files.mdx` - Configuration files guide

- **`integrations/`** - Integration guides
  - `n8n.mdx` - n8n workflow integration
  - `github-actions.mdx` - GitHub Actions integration
  - `mcp-clients.mdx` - MCP client configuration

- **`api/`** - API documentation
  - `overview.mdx` - REST API overview
  - `authentication.mdx` - API authentication
  - `sdk.mdx` - Python SDK documentation

- **`assets/`** - Documentation assets
  - Images, diagrams, and screenshots
  - Logos and favicons
  - UI mockups and wireframes

- **`guides/`** - Developer guides
  - Architecture documentation
  - Development workflows

## Getting Started Pages

- `introduction.mdx` - Product introduction and overview
- `quickstart.mdx` - Quick start guide
- `authentication.mdx` - Authentication setup

## Configuration

- `mint.json` - Mintlify configuration with navigation, branding, and metadata

## Viewing Documentation

To preview the documentation locally:

```bash
npm i -g mintlify
mintlify dev
```

Then visit `http://localhost:3000`

## Deployment

The documentation is configured to deploy to Mintlify hosting. See [mintlify.com/docs](https://mintlify.com/docs) for deployment instructions.

