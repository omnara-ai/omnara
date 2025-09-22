"""Utility functions for MCP server"""

import os
from fastmcp.server.dependencies import get_http_headers


def detect_agent_type_from_headers() -> str:
    """Detect the agent type from HTTP User-Agent header."""
    try:
        headers = get_http_headers()

        # First check for explicit client type header
        explicit_client = headers.get("x-client-type") or headers.get("X-Client-Type")
        if explicit_client:
            return explicit_client

        # Fall back to User-Agent parsing
        user_agent = headers.get("user-agent", "").lower()

        # Parse User-Agent patterns for supported clients
        if "cursor" in user_agent:
            return "cursor"
        elif "claude" in user_agent:
            if "claude-code" in user_agent or "claude code" in user_agent:
                return "claude-code"
            else:
                return "claude"
        elif "cline" in user_agent:
            if "roo-cline" in user_agent or "roo cline" in user_agent:
                return "roo-cline"
            else:
                return "cline"
        elif "windsurf" in user_agent:
            return "windsurf"
        elif "witsy" in user_agent:
            return "witsy"
        elif "enconvo" in user_agent:
            return "enconvo"
        elif "vscode" in user_agent or "code" in user_agent:
            return "vscode"
        elif "postman" in user_agent:
            return "postman"

        # Return "unknown" for HTTP requests where we can't identify the client
        return "unknown"

    except ImportError:
        # FastMCP dependencies not available - likely stdio transport
        return "unknown"
    except Exception:
        # Other errors (no request context, etc.) - likely stdio transport
        return "unknown"


def detect_agent_type_from_environment() -> str:
    """Detect agent type from environment variables (for stdio transport)."""
    # Check for our custom environment variable first
    omnara_client_type = os.getenv("OMNARA_CLIENT_TYPE")
    if omnara_client_type:
        return omnara_client_type

    # If no explicit client type is set, return unknown
    # This typically means the client wasn't installed via our CLI
    return "unknown"
