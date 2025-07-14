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

# Global state for current agent instance (primarily used for claude code approval tool)
current_agent_instance_id: Optional[str] = None


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
    global current_agent_instance_id

    agent_type = detect_agent_type_from_environment()
    client = get_client()

    response = await client.log_step(
        agent_type=agent_type,
        step_description=step_description,
        agent_instance_id=agent_instance_id,
    )

    # Store the instance ID for use by other tools
    current_agent_instance_id = response.agent_instance_id

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
    global current_agent_instance_id

    if not agent_instance_id:
        raise ValueError("agent_instance_id is required")
    if not question_text:
        raise ValueError("question_text is required")

    client = get_client()

    # Store the instance ID for use by other tools
    current_agent_instance_id = agent_instance_id

    try:
        response = await client.ask_question(
            agent_instance_id=agent_instance_id,
            question_text=question_text,
            timeout_minutes=1440,  # 24 hours default
            poll_interval=10.0,
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


@mcp.tool(
    name="approve",
    description="Handle permission prompts for Claude Code. Returns approval/denial for tool execution.",
    enabled=False,
)
async def approve_tool(
    tool_name: str,
    input: dict,
    tool_use_id: Optional[str] = None,
) -> dict:
    """Claude Code permission prompt handler."""
    global current_agent_instance_id

    if not tool_name:
        raise ValueError("tool_name is required")

    client = get_client()

    # Format the permission request as a question
    question_text = f"Allow execution of {tool_name}? Input: {input}"

    try:
        # Use existing instance ID or create a new one
        if current_agent_instance_id:
            instance_id = current_agent_instance_id
        else:
            # Only create a new instance if we don't have one
            response = await client.log_step(
                agent_type="Claude Code",
                step_description="Permission request",
                agent_instance_id=None,
            )
            instance_id = response.agent_instance_id
            current_agent_instance_id = instance_id

        # Ask the permission question
        answer_response = await client.ask_question(
            agent_instance_id=instance_id,
            question_text=question_text,
            timeout_minutes=1440,
            poll_interval=10.0,
        )

        # Parse the answer to determine approval
        answer = answer_response.answer.lower().strip()
        if answer in ["yes", "y", "allow", "approve", "ok"]:
            return {
                "behavior": "allow",
                "updatedInput": input,
            }
        else:
            return {
                "behavior": "deny",
                "message": f"Permission denied by user: {answer_response.answer}",
            }

    except OmnaraTimeoutError:
        return {
            "behavior": "deny",
            "message": "Permission request timed out",
        }


def main():
    """Main entry point for the stdio server"""
    parser = argparse.ArgumentParser(description="Omnara MCP Server (Stdio)")
    parser.add_argument("--api-key", required=True, help="API key for authentication")
    parser.add_argument(
        "--base-url",
        default="https://agent-dashboard-mcp.onrender.com",
        help="Base URL of the Omnara API server",
    )
    parser.add_argument(
        "--claude-code-permission-tool",
        action="store_true",
        help="Enable Claude Code permission prompt tool for handling tool execution approvals",
    )

    args = parser.parse_args()

    # Initialize the global client
    global client
    client = AsyncOmnaraClient(
        api_key=args.api_key,
        base_url=args.base_url,
    )

    # Enable/disable tools based on feature flags
    if args.claude_code_permission_tool:
        approve_tool.enable()
        logger.info("Claude Code permission tool enabled")

    logger.info("Starting Omnara MCP server (stdio)")
    logger.info(f"Using API server: {args.base_url}")
    logger.info(
        f"Claude Code permission tool: {'enabled' if args.claude_code_permission_tool else 'disabled'}"
    )

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
