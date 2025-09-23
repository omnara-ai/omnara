# src/

This directory contains all source code for the Omnara project.

## Structure

- **`omnara/`** - Main Python package (CLI & SDK)
- **`backend/`** - FastAPI web dashboard API
- **`servers/`** - MCP & REST servers
  - `mcp/` - MCP protocol server
  - `api/` - REST API server
  - `shared/` - Shared server code
- **`shared/`** - Shared database models and configurations
- **`mcp-installer/`** - NPX package for configuring MCP clients
- **`integrations/`** - Integration connectors (flat structure)
  - `cli_wrappers/` - Claude Code, Codex CLI wrappers
  - `headless/` - Background agent runners
  - `n8n/` - Node.js n8n workflow package
  - `github/` - GitHub Actions YAML
  - `utils/` - Shared integration utilities
  - `webhooks/` - Webhook handlers

This flattened organization provides clear separation by function while keeping related code together.