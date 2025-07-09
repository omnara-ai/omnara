#!/usr/bin/env python3
"""Omnara MCP Server - Stdio Transport

This is the stdio version of the Omnara MCP server that can be installed via pip/pipx.
It provides the same functionality as the hosted server but uses stdio transport.
"""

import argparse
import asyncio
import logging
from collections.abc import Callable, Coroutine
from functools import wraps
from typing import Any, ParamSpec, TypeVar

from fastmcp import FastMCP
from shared.config import settings
from shared.database import Base
from shared.database.session import engine

from .models import AskQuestionResponse, EndSessionResponse, LogStepResponse
from .tools import (
    LOG_STEP_DESCRIPTION,
    ASK_QUESTION_DESCRIPTION,
    END_SESSION_DESCRIPTION,
    log_step_impl,
    ask_question_impl,
    end_session_impl,
)
from .utils import detect_agent_type_from_environment

# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# Type variables for decorator
P = ParamSpec("P")
T = TypeVar("T")


def require_api_key(func: Callable[P, T]) -> Callable[P, Coroutine[Any, Any, T]]:
    """Decorator to ensure API key is provided for stdio server."""

    @wraps(func)
    async def wrapper(*args: P.args, **kwargs: P.kwargs) -> T:
        # For stdio, we get the API key from command line args
        # and use it as the user_id for simplicity
        api_key = getattr(require_api_key, "_api_key", None)
        if not api_key:
            raise ValueError("API key is required. Use --api-key argument.")

        # Add user_id to kwargs for use in the function
        kwargs["_user_id"] = api_key
        result = func(*args, **kwargs)
        # Handle both sync and async functions
        if asyncio.iscoroutine(result):
            return await result
        return result

    return wrapper


# Create FastMCP server
mcp = FastMCP("Omnara Agent Dashboard MCP Server")


@mcp.tool(name="log_step", description=LOG_STEP_DESCRIPTION)
@require_api_key
def log_step_tool(
    agent_instance_id: str | None = None,
    step_description: str = "",
    _user_id: str = "",  # Injected by decorator
) -> LogStepResponse:
    agent_type = detect_agent_type_from_environment()
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
@require_api_key
async def ask_question_tool(
    agent_instance_id: str | None = None,
    question_text: str | None = None,
    _user_id: str = "",  # Injected by decorator
) -> AskQuestionResponse:
    return await ask_question_impl(
        agent_instance_id=agent_instance_id,
        question_text=question_text,
        user_id=_user_id,
    )


@mcp.tool(
    name="end_session",
    description=END_SESSION_DESCRIPTION,
)
@require_api_key
def end_session_tool(
    agent_instance_id: str,
    _user_id: str = "",  # Injected by decorator
) -> EndSessionResponse:
    return end_session_impl(
        agent_instance_id=agent_instance_id,
        user_id=_user_id,
    )


def main():
    """Main entry point for the stdio server"""
    parser = argparse.ArgumentParser(description="Omnara MCP Server (Stdio)")
    parser.add_argument("--api-key", required=True, help="API key for authentication")

    args = parser.parse_args()

    # Store API key for auth decorator
    require_api_key._api_key = args.api_key

    # Ensure database tables exist
    Base.metadata.create_all(bind=engine)
    logger.info("Database tables created/verified")

    logger.info("Starting Omnara MCP server (stdio)")
    logger.info(f"Database URL configured: {settings.database_url[:50]}...")

    try:
        # Run with stdio transport (default)
        mcp.run(transport="stdio")
    except Exception as e:
        logger.error(f"Failed to start MCP server: {e}")
        raise


if __name__ == "__main__":
    main()
