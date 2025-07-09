#!/usr/bin/env python3
"""Omnara MCP Server - Stdio Transport

This is the stdio version of the Omnara MCP server that can be installed via pip/pipx.
It provides the same functionality as the hosted server but uses stdio transport.
"""

import argparse
import asyncio
import logging
from typing import Optional

from fastmcp import FastMCP
from omnara.sdk import AsyncOmnaraClient
from omnara.sdk.exceptions import TimeoutError as OmnaraTimeoutError

from .models import AskQuestionResponse, EndSessionResponse, LogStepResponse
from .descriptions import (
    LOG_STEP_DESCRIPTION,
    ASK_QUESTION_DESCRIPTION,
    END_SESSION_DESCRIPTION,
)
from .utils import detect_agent_type_from_environment

# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# Global client instance
client: Optional[AsyncOmnaraClient] = None


def get_client() -> AsyncOmnaraClient:
    """Get the initialized AsyncOmnaraClient instance."""
    if client is None:
        raise RuntimeError("Client not initialized. Run main() first.")
    return client


# Create FastMCP server
mcp = FastMCP("Omnara Agent Dashboard MCP Server")


@mcp.tool(name="log_step", description=LOG_STEP_DESCRIPTION)
async def log_step_tool(
    agent_instance_id: str | None = None,
    step_description: str = "",
) -> LogStepResponse:
    agent_type = detect_agent_type_from_environment()
    client = get_client()

    response = await client.log_step(
        agent_type=agent_type,
        step_description=step_description,
        agent_instance_id=agent_instance_id,
    )

    return LogStepResponse(
        success=response.success,
        agent_instance_id=response.agent_instance_id,
        step_number=response.step_number,
        user_feedback=response.user_feedback,
    )


@mcp.tool(
    name="ask_question",
    description=ASK_QUESTION_DESCRIPTION,
)
async def ask_question_tool(
    agent_instance_id: str | None = None,
    question_text: str | None = None,
) -> AskQuestionResponse:
    if not agent_instance_id:
        raise ValueError("agent_instance_id is required")
    if not question_text:
        raise ValueError("question_text is required")

    client = get_client()

    try:
        response = await client.ask_question(
            agent_instance_id=agent_instance_id,
            question_text=question_text,
            timeout_minutes=1440,  # 24 hours default
            poll_interval=1.0,
        )

        return AskQuestionResponse(
            answer=response.answer,
            question_id=response.question_id,
        )
    except OmnaraTimeoutError:
        raise TimeoutError("Question timed out waiting for user response")


@mcp.tool(
    name="end_session",
    description=END_SESSION_DESCRIPTION,
)
async def end_session_tool(
    agent_instance_id: str,
) -> EndSessionResponse:
    client = get_client()

    response = await client.end_session(
        agent_instance_id=agent_instance_id,
    )

    return EndSessionResponse(
        success=response.success,
        agent_instance_id=response.agent_instance_id,
        final_status=response.final_status,
    )


def main():
    """Main entry point for the stdio server"""
    parser = argparse.ArgumentParser(description="Omnara MCP Server (Stdio)")
    parser.add_argument("--api-key", required=True, help="API key for authentication")
    parser.add_argument(
        "--base-url",
        default="https://agent-dashboard-mcp.onrender.com",
        help="Base URL of the Omnara API server",
    )

    args = parser.parse_args()

    # Initialize the global client
    global client
    client = AsyncOmnaraClient(
        api_key=args.api_key,
        base_url=args.base_url,
    )

    logger.info("Starting Omnara MCP server (stdio)")
    logger.info(f"Using API server: {args.base_url}")

    try:
        # Run with stdio transport (default)
        mcp.run(transport="stdio")
    except Exception as e:
        logger.error(f"Failed to start MCP server: {e}")
        raise
    finally:
        # Clean up client
        if client:
            asyncio.run(client.close())


if __name__ == "__main__":
    main()
