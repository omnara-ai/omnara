# Servers Directory

This directory contains the write operations server for the Agent Dashboard system. It provides a unified interface for AI agents to interact with the dashboard through multiple protocols.

## Overview

The servers directory implements all write operations that agents need:
- Logging their progress and receiving user feedback
- Asking questions to users
- Managing session lifecycle

All operations are authenticated and multi-tenant, ensuring data isolation between users.

## Architecture

### Unified Server (`app.py`)
A single server application that supports both MCP (Model Context Protocol) and REST API interfaces:
- **MCP Interface**: For agents using the MCP protocol
- **REST API**: For SDK clients and direct API integrations
- Both interfaces share the same authentication and business logic

### Components

- **`mcp_server/`**: MCP protocol implementation using fastmcp
- **`fastapi_server/`**: REST API implementation  
- **`shared/`**: Common database operations and business logic
- **`tests/`**: Integration and unit tests

## Authentication

The servers use a separate authentication system from the main backend:
- **JWT Bearer tokens** with RSA-256 signing
- **Shorter API keys** using a weaker RSA key (appropriate for write-only operations)
- **User context** embedded in tokens for multi-tenancy
- **Security Note**: Both the private AND public JWT keys should be kept secure. The weaker RSA implementation (for shorter tokens) means even the public key should not be exposed

## Key Features

- **Write-only operations**: Designed for agent interactions, not data retrieval
- **Automatic session management**: Creates sessions on first interaction
- **User feedback delivery**: Agents receive feedback when logging steps
- **Non-blocking questions**: Async implementation for user interactions
- **Multi-protocol support**: Same functionality via MCP or REST API

## Running the Server

```bash
# From the project root with virtual environment activated
python -m servers.app
```

The server will be available on the configured port (default: 8080) with:
- MCP endpoint at `/mcp/`
- REST API at `/api/v1/`

## Environment Variables

- `DATABASE_URL`: PostgreSQL connection string
- `MCP_SERVER_PORT`: Server port (default: 8080)
- `JWT_PUBLIC_KEY`: RSA public key for token verification
- `JWT_PRIVATE_KEY`: RSA private key for token signing (if needed)

## Integration

Clients can connect using:
1. **MCP Protocol**: Via SSE or HTTP streaming transport
2. **REST API**: Direct HTTP requests with Bearer token authentication
3. **SDK**: Language-specific clients that handle authentication and protocol details

See `DEPLOYMENT.md` for detailed deployment and client configuration instructions.