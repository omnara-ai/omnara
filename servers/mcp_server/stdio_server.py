#!/usr/bin/env python3
"""Omnara MCP Server - Stdio Transport

This is the stdio version of the Omnara MCP server that can be installed via pip/pipx.
It provides the same functionality as the hosted server but uses stdio transport.
"""

import argparse
import asyncio
import logging
import os
import subprocess
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

# Global state for permission tracking (agent_instance_id -> permissions)
# Structure: {agent_instance_id: {"Edit": True, "Write": True, "Bash": {"ls": True, "git": True}}}
permission_state: dict[str, dict] = {}

# Global flag for git diff feature
git_diff_enabled: bool = False

# Global to store initial git commit hash
initial_git_hash: Optional[str] = None


def get_client() -> AsyncOmnaraClient:
    """Get the initialized AsyncOmnaraClient instance."""
    if client is None:
        raise RuntimeError("Client not initialized. Run main() first.")
    return client


def format_dict_as_markdown(data: dict) -> str:
    """Format a dictionary as readable markdown.

    Args:
        data: Dictionary to format

    Returns:
        Formatted markdown string
    """
    lines = []
    for key, value in data.items():
        # Bold the key
        lines.append(f"**{key}:**")
        # Format the value based on its type
        if isinstance(value, dict):
            # Recursively format nested dicts with indentation
            nested = format_dict_as_markdown(value)
            indented = "\n".join(f"  {line}" for line in nested.split("\n"))
            lines.append(indented)
        elif isinstance(value, list):
            # Format lists as bullet points
            for item in value:
                lines.append(f"  - {item}")
        elif isinstance(value, str) and "\n" in value:
            # Multi-line strings in code blocks
            lines.append("```")
            lines.append(value)
            lines.append("```")
        else:
            # Simple values inline
            lines.append(f"  {value}")
        lines.append("")  # Empty line between entries
    return "\n".join(lines).rstrip()


def get_git_diff() -> Optional[str]:
    """Get the current git diff if enabled via command-line argument.

    Returns:
        The git diff output if enabled and there are changes, None otherwise.
    """
    # Check if git diff is enabled
    if not git_diff_enabled:
        return None

    try:
        combined_output = ""

        # Get list of worktrees to exclude
        worktree_result = subprocess.run(
            ["git", "worktree", "list", "--porcelain"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        exclude_patterns = []
        if worktree_result.returncode == 0:
            # Parse worktree list to get paths to exclude
            cwd = os.getcwd()
            for line in worktree_result.stdout.strip().split("\n"):
                if line.startswith("worktree "):
                    worktree_path = line[9:]  # Remove "worktree " prefix
                    # Only exclude if it's a subdirectory of current directory
                    if worktree_path != cwd and worktree_path.startswith(
                        os.path.dirname(cwd)
                    ):
                        # Get relative path from current directory
                        try:
                            rel_path = os.path.relpath(worktree_path, cwd)
                            if not rel_path.startswith(".."):
                                exclude_patterns.append(f":(exclude){rel_path}")
                        except ValueError:
                            # Can't compute relative path, skip
                            pass

        # Build git diff command
        if initial_git_hash:
            # Use git diff from initial hash to current working tree
            # This shows ALL changes (committed + uncommitted) as one unified diff
            diff_cmd = ["git", "diff", initial_git_hash]
        else:
            # No initial hash - just show uncommitted changes
            diff_cmd = ["git", "diff", "HEAD"]

        if exclude_patterns:
            diff_cmd.extend(["--"] + exclude_patterns)

        # Run git diff
        result = subprocess.run(diff_cmd, capture_output=True, text=True, timeout=5)
        if result.returncode == 0 and result.stdout.strip():
            combined_output = result.stdout.strip()

        # Get untracked files (with exclusions)
        untracked_cmd = ["git", "ls-files", "--others", "--exclude-standard"]
        if exclude_patterns:
            untracked_cmd.extend(["--"] + exclude_patterns)

        result_untracked = subprocess.run(
            untracked_cmd, capture_output=True, text=True, timeout=5
        )
        if result_untracked.returncode == 0 and result_untracked.stdout.strip():
            untracked_files = result_untracked.stdout.strip().split("\n")
            if untracked_files:
                if combined_output:
                    combined_output += "\n"

                # For each untracked file, show its contents with diff-like format
                for file_path in untracked_files:
                    combined_output += f"diff --git a/{file_path} b/{file_path}\n"
                    combined_output += "new file mode 100644\n"
                    combined_output += "index 0000000..0000000\n"
                    combined_output += "--- /dev/null\n"
                    combined_output += f"+++ b/{file_path}\n"

                    # Read file contents and add with + prefix
                    try:
                        with open(
                            file_path, "r", encoding="utf-8", errors="ignore"
                        ) as f:
                            lines = f.readlines()
                            combined_output += f"@@ -0,0 +1,{len(lines)} @@\n"
                            for line in lines:
                                combined_output += f"+{line}"
                            if lines and not lines[-1].endswith("\n"):
                                combined_output += "\n\\ No newline at end of file\n"
                    except Exception:
                        combined_output += "@@ -0,0 +1,1 @@\n"
                        combined_output += "+[Binary or unreadable file]\n"

                    combined_output += "\n"

        return combined_output if combined_output else None

    except Exception as e:
        logger.warning(f"Failed to get git diff: {e}")

    return None


# Create FastMCP server and metadata
mcp = FastMCP(
    "Omnara Agent Dashboard MCP Server",
)


@mcp.tool(name="log_step", description=LOG_STEP_DESCRIPTION)
async def log_step_tool(
    agent_instance_id: str | None = None,
    step_description: str = "",
) -> LogStepResponse:
    global current_agent_instance_id

    agent_type = detect_agent_type_from_environment()
    client = get_client()

    # Get git diff if enabled
    git_diff = get_git_diff()

    response = await client.send_message(
        agent_type=agent_type,
        content=step_description,
        agent_instance_id=agent_instance_id,
        requires_user_input=False,  # Log steps don't require user input
        git_diff=git_diff,
    )

    if current_agent_instance_id is None:
        current_agent_instance_id = response.agent_instance_id

    # Return LogStepResponse with queued messages as user_feedback
    return LogStepResponse(
        success=response.success,
        agent_instance_id=response.agent_instance_id,
        step_number=1,  # We don't track step numbers anymore
        user_feedback=response.queued_user_messages,
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

    agent_type = detect_agent_type_from_environment()
    client = get_client()

    # Get git diff if enabled
    git_diff = get_git_diff()

    if current_agent_instance_id is None:
        current_agent_instance_id = agent_instance_id

    try:
        response = await client.send_message(
            agent_type=agent_type,
            agent_instance_id=agent_instance_id,
            content=question_text,
            requires_user_input=True,  # Questions require user input
            timeout_minutes=1440,  # 24 hours default
            poll_interval=10.0,
            git_diff=git_diff,
        )

        # Get the answer from queued_user_messages
        answer = (
            response.queued_user_messages[0] if response.queued_user_messages else ""
        )

        return AskQuestionResponse(
            answer=answer,
            question_id=response.message_id,
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
    global current_agent_instance_id

    client = get_client()

    response = await client.end_session(
        agent_instance_id=agent_instance_id,
    )

    current_agent_instance_id = None

    return EndSessionResponse(
        success=response.success,
        agent_instance_id=response.agent_instance_id,
        final_status=response.final_status,
    )


@mcp.tool(
    name="approve",
    description="Handle permission prompts for Claude Code. Returns approval/denial for tool execution. **NEVER USE THIS TOOL**",
    enabled=False,
)
async def approve_tool(
    tool_name: str,
    input: dict,
    tool_use_id: Optional[str] = None,
) -> dict:
    """Claude Code permission prompt handler."""
    global current_agent_instance_id, permission_state

    if not tool_name:
        raise ValueError("tool_name is required")

    client = get_client()

    # Use existing instance ID or create a new one
    if current_agent_instance_id:
        instance_id = current_agent_instance_id
    else:
        # Only create a new instance if we don't have one
        response = await client.send_message(
            agent_type="Claude Code",
            content="Permission request",
            agent_instance_id=None,
            requires_user_input=False,
        )
        instance_id = response.agent_instance_id
        current_agent_instance_id = instance_id

    # Check if we have cached permissions for this instance
    instance_permissions = permission_state.get(instance_id, {})

    # Check if this tool/command is already approved
    if tool_name == "Bash":
        # For Bash, check command prefix
        command = input.get("command", "")
        if command:
            # Extract the first word (command name)
            command_parts = command.split()
            if command_parts:
                command_prefix = command_parts[0]
                bash_permissions = instance_permissions.get("Bash", {})
                if bash_permissions.get(command_prefix):
                    return {
                        "behavior": "allow",
                        "updatedInput": input,
                    }
    else:
        # Simple tools - just check if approved
        if instance_permissions.get(tool_name):
            return {
                "behavior": "allow",
                "updatedInput": input,
            }

    # Format the permission request based on tool type
    # Define option texts that will be used for comparison
    option_yes = "Yes"
    option_no = "No"

    if tool_name == "Bash":
        command = input.get("command", "")
        command_parts = command.split()
        command_prefix = command_parts[0] if command_parts else "command"
        option_yes_session = f"Yes and approve {command_prefix} for rest of session"

        question_text = f"Allow execution of Bash {command_prefix}?\n\n**Full command:**\n```bash\n{command}\n```\n\n"
        question_text += "[OPTIONS]\n"
        question_text += f"1. {option_yes}\n"
        question_text += f"2. {option_yes_session}\n"
        question_text += f"3. {option_no}\n"
        question_text += "[/OPTIONS]"
    else:
        option_yes_session = f"Yes and approve {tool_name} for rest of session"

        # Format the input dict as markdown
        formatted_input = format_dict_as_markdown(input)

        question_text = (
            f"Allow execution of {tool_name}?\n\n**Input:**\n{formatted_input}\n\n"
        )
        question_text += "[OPTIONS]\n"
        question_text += f"1. {option_yes}\n"
        question_text += f"2. {option_yes_session}\n"
        question_text += f"3. {option_no}\n"
        question_text += "[/OPTIONS]"

    try:
        # Ask the permission question
        await asyncio.sleep(
            1
        )  # This prevents a race condition in claude code when a tool call gets logged vs permission gets asked
        response = await client.send_message(
            agent_instance_id=instance_id,
            content=question_text,
            requires_user_input=True,
            timeout_minutes=1440,
            poll_interval=10.0,
        )

        # Parse the answer to determine approval
        # Get the answer from queued_user_messages
        answer = (
            response.queued_user_messages[0].strip()
            if response.queued_user_messages
            else ""
        )

        # Handle option selections by comparing with actual option text
        if answer == option_yes:
            # Yes - allow once
            return {
                "behavior": "allow",
                "updatedInput": input,
            }
        elif answer == option_yes_session:
            # Yes and approve for rest of session
            # Save permission state
            if instance_id not in permission_state:
                permission_state[instance_id] = {}

            if tool_name == "Bash":
                command = input.get("command", "")
                command_parts = command.split()
                if command_parts:
                    command_prefix = command_parts[0]
                    if "Bash" not in permission_state[instance_id]:
                        permission_state[instance_id]["Bash"] = {}
                    permission_state[instance_id]["Bash"][command_prefix] = True
            else:
                permission_state[instance_id][tool_name] = True

            return {
                "behavior": "allow",
                "updatedInput": input,
            }
        elif answer == option_no:
            # No - deny
            return {
                "behavior": "deny",
                "message": "Permission denied by user",
            }
        else:
            # Custom text response - treat as denial with message
            return {
                "behavior": "deny",
                "message": f"Permission denied by user: {answer}",
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
        "--permission-tool",
        action="store_true",
        help="Enable Claude Code permission prompt tool for handling tool execution approvals",
    )
    parser.add_argument(
        "--git-diff",
        action="store_true",
        help="Enable git diff capture for log_step and ask_question (stdio mode only)",
    )
    parser.add_argument(
        "--agent-instance-id",
        type=str,
        help="Pre-existing agent instance ID to use for this session",
    )
    parser.add_argument(
        "--disable-tools",
        action="store_true",
        help="Disable all tools except the permission tool",
    )

    args = parser.parse_args()

    global client, git_diff_enabled, initial_git_hash, current_agent_instance_id
    client = AsyncOmnaraClient(
        api_key=args.api_key,
        base_url=args.base_url,
    )

    if args.agent_instance_id:
        current_agent_instance_id = args.agent_instance_id
        logger.info(f"Using provided agent instance ID: {args.agent_instance_id}")

    # Set git diff flag
    git_diff_enabled = args.git_diff

    # Capture initial git hash if git diff is enabled
    if git_diff_enabled:
        try:
            result = subprocess.run(
                ["git", "rev-parse", "HEAD"], capture_output=True, text=True, timeout=5
            )
            if result.returncode == 0 and result.stdout.strip():
                initial_git_hash = result.stdout.strip()
                logger.info(f"Initial git commit: {initial_git_hash[:8]}")
            else:
                logger.warning("Could not determine initial git commit hash")
        except Exception as e:
            logger.warning(f"Failed to get initial git hash: {e}")

    # Enable/disable tools based on feature flags
    if args.permission_tool:
        approve_tool.enable()
        logger.info("Claude Code permission tool enabled")

    if args.disable_tools:
        # Disable all tools except the permission tool
        log_step_tool.disable()
        ask_question_tool.disable()
        end_session_tool.disable()
        logger.info("All tools disabled except permission tool")

    logger.info("Starting Omnara MCP server (stdio)")
    logger.info(f"Using API server: {args.base_url}")
    logger.info(
        f"Claude Code permission tool: {'enabled' if args.permission_tool else 'disabled'}"
    )
    logger.info(f"Git diff capture: {'enabled' if args.git_diff else 'disabled'}")
    logger.info(f"Tools disabled: {args.disable_tools}")

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
