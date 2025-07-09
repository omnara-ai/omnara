"""MCP Server for Agent Dashboard"""

import asyncio
import logging
from collections.abc import Callable, Coroutine
from functools import wraps
from typing import Any, ParamSpec, TypeVar

from fastmcp import FastMCP, Context
from fastmcp.server.auth import BearerAuthProvider
from fastmcp.server.dependencies import get_access_token
from shared.config import settings

from .models import AskQuestionResponse, EndSessionResponse, LogStepResponse
from .descriptions import (
    LOG_STEP_DESCRIPTION,
    ASK_QUESTION_DESCRIPTION,
    END_SESSION_DESCRIPTION,
)
from .tools import (
    log_step_impl,
    ask_question_impl,
    end_session_impl,
)
from .utils import detect_agent_type_from_headers

# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# Type variables for decorator
P = ParamSpec("P")
T = TypeVar("T")


def require_auth(func: Callable[P, T]) -> Callable[P, Coroutine[Any, Any, T]]:
    """Decorator to ensure user is authenticated before executing tool."""

    @wraps(func)
    async def wrapper(*args: P.args, **kwargs: P.kwargs) -> T:
        # Get authenticated user info - this should be the first check
        access_token = get_access_token()
        if access_token is None:
            raise ValueError("Authentication required. Please provide a valid API key.")

        # Add user_id to kwargs for use in the function
        kwargs["_user_id"] = access_token.client_id
        result = func(*args, **kwargs)
        # Handle both sync and async functions
        if asyncio.iscoroutine(result):
            return await result
        return result

    return wrapper


# Configure authentication
if not settings.jwt_public_key:
    raise ValueError(
        "JWT_PUBLIC_KEY environment variable is not set. "
        "Please generate keys using scripts/generate_jwt_keys.py "
        "and add them to your .env file"
    )

auth = BearerAuthProvider(
    public_key=settings.jwt_public_key,
)


# Create FastMCP server with authentication
mcp = FastMCP("Agent Dashboard MCP Server", auth=auth)


@mcp.tool(name="log_step", description=LOG_STEP_DESCRIPTION)
@require_auth
def log_step_tool(
    agent_instance_id: str | None = None,
    step_description: str = "",
    _user_id: str = "",  # Injected by decorator
) -> LogStepResponse:
    agent_type = detect_agent_type_from_headers()
    return log_step_impl(
        agent_instance_id=agent_instance_id,
        agent_type=agent_type,
        step_description=step_description,
        user_id=_user_id,
    )


@mcp.tool(
    name="ask_question",
    description=ASK_QUESTION_DESCRIPTION,
)
@require_auth
async def ask_question_tool(
    ctx: Context,
    agent_instance_id: str | None = None,
    question_text: str | None = None,
    _user_id: str = "",  # Injected by decorator
) -> AskQuestionResponse:
    return await ask_question_impl(
        agent_instance_id=agent_instance_id,
        question_text=question_text,
        user_id=_user_id,
        tool_context=ctx,
    )


@mcp.tool(
    name="end_session",
    description=END_SESSION_DESCRIPTION,
)
@require_auth
def end_session_tool(
    agent_instance_id: str,
    _user_id: str = "",  # Injected by decorator
) -> EndSessionResponse:
    return end_session_impl(
        agent_instance_id=agent_instance_id,
        user_id=_user_id,
    )


def main():
    """Run the MCP server"""
    # Database tables should be managed by Alembic migrations
    logger.info("Starting MCP server...")

    # Log configuration for debugging
    logger.info(f"Starting MCP server on port: {settings.mcp_server_port}")
    logger.info(f"Database URL configured: {settings.database_url[:50]}...")
    logger.info(
        f"JWT public key configured: {'Yes' if settings.jwt_public_key else 'No'}"
    )

    try:
        # Use streamable-http which handles both HTTP POST and SSE on same endpoint
        mcp.run(
            transport="streamable-http",
            host="0.0.0.0",
            port=settings.mcp_server_port,
            path="/mcp",
        )
    except Exception as e:
        logger.error(f"Failed to start MCP server: {e}")
        raise


if __name__ == "__main__":
    main()
