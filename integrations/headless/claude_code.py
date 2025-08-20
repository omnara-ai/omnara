#!/usr/bin/env python3
"""
Headless Claude Code Integration for Omnara

This module provides a headless version of Claude Code that integrates with the Omnara SDK,
allowing human users to interact with Claude through the web dashboard while Claude runs
autonomously using the Claude Code SDK.
"""

import argparse
import asyncio
import logging
import os
import sys
import uuid
from pathlib import Path
from typing import Optional, List, Dict, Union, cast

from omnara.sdk.async_client import AsyncOmnaraClient
from integrations.utils import GitDiffTracker

try:
    from claude_code_sdk import (
        ClaudeSDKClient,
        ClaudeCodeOptions,
        AssistantMessage,
        UserMessage,
        SystemMessage,
        ResultMessage,
        TextBlock,
        ToolUseBlock,
        ToolResultBlock,
        CLINotFoundError,
        ProcessError,
        PermissionMode,
        McpServerConfig,
    )
except ImportError as e:
    print(
        "Error: Claude Code SDK not found. Please install it with: pip install claude-code-sdk"
    )
    print(f"Import error: {e}")
    sys.exit(1)


def setup_logging(session_id: str, console_output: bool = True):
    """Setup logging with session-specific log file.

    Args:
        session_id: Session ID for the log file name
        console_output: Whether to also log to console (default True for standalone, False for webhook)
    """
    logger = logging.getLogger(__name__)
    logger.setLevel(logging.INFO)

    # Clear any existing handlers
    logger.handlers.clear()

    # Create .omnara/claude_headless directory and log file with session UUID
    omnara_dir = Path.home() / ".omnara"
    claude_headless_dir = omnara_dir / "claude_headless"
    claude_headless_dir.mkdir(exist_ok=True, parents=True)

    log_file = claude_headless_dir / f"{session_id}.log"

    # Create formatters
    formatter = logging.Formatter(
        "%(asctime)s - %(name)s - %(levelname)s - %(message)s"
    )

    # File handler (always add)
    file_handler = logging.FileHandler(log_file)
    file_handler.setLevel(logging.DEBUG)
    file_handler.setFormatter(formatter)
    logger.addHandler(file_handler)

    # Console handler (only if requested)
    if console_output:
        console_handler = logging.StreamHandler()
        console_handler.setLevel(logging.INFO)
        console_handler.setFormatter(formatter)
        logger.addHandler(console_handler)
        logger.info(f"Logging to: {log_file}")

    return logger


class HeadlessClaudeRunner:
    """Headless Claude Code runner that integrates with Omnara SDK."""

    def __init__(
        self,
        omnara_api_key: str,
        session_id: str,
        omnara_base_url: str = "https://agent-dashboard-mcp.onrender.com",
        initial_prompt: Optional[str] = None,
        extra_args: Optional[Dict[str, Optional[str]]] = None,
        permission_mode: Optional[PermissionMode] = None,
        allowed_tools: Optional[List[str]] = None,
        disallowed_tools: Optional[List[str]] = None,
        cwd: Optional[Union[str, Path]] = None,
        console_output: bool = True,
    ):
        self.omnara_api_key = omnara_api_key
        self.omnara_base_url = omnara_base_url
        self.initial_prompt = initial_prompt
        self.session_id = session_id
        self.last_message_id: Optional[str] = None
        self.cwd = cwd or os.getcwd()  # Store cwd before using it

        # Setup logging for this session
        setup_logging(session_id, console_output=console_output)
        self.logger = logging.getLogger(__name__)

        # Create default Omnara MCP server config
        omnara_mcp_server = {
            "command": "omnara",
            "args": [
                "mcp",
                "--api-key",
                omnara_api_key,
                "--permission-tool",
                "--disable-tools",
                "--agent-instance-id",
                session_id,
            ],
        }

        # Always include default Omnara MCP server
        default_mcp_servers = cast(
            Dict[str, McpServerConfig], {"omnara": omnara_mcp_server}
        )

        # Claude Code SDK options
        self.claude_options = ClaudeCodeOptions(
            mcp_servers=default_mcp_servers,
            permission_mode=permission_mode,
            allowed_tools=(allowed_tools or []) + ["mcp__omnara__approve"],
            permission_prompt_tool_name="mcp__omnara__approve",
            disallowed_tools=disallowed_tools or [],
            cwd=self.cwd,
            extra_args=extra_args or {},
        )

        # Omnara client and Claude client
        self.omnara_client: Optional[AsyncOmnaraClient] = None
        self.claude_client: Optional[ClaudeSDKClient] = None
        self.running = True
        self.conversation_started = False

        # Initialize git diff tracker with our logger and working directory
        self.git_tracker = GitDiffTracker(
            enabled=True, logger=self.logger, cwd=str(self.cwd) if self.cwd else None
        )
        self.previous_git_diff = None  # Track previous diff to avoid duplicates

    async def initialize(self):
        """Initialize the Omnara and Claude clients and create initial session."""
        self.logger.info("Initializing Omnara client...")

        self.omnara_client = AsyncOmnaraClient(
            api_key=self.omnara_api_key, base_url=self.omnara_base_url
        )

        # Initialize persistent Claude client
        self.logger.info("Initializing Claude Code SDK client...")
        self.claude_client = ClaudeSDKClient(options=self.claude_options)
        await self.claude_client.__aenter__()  # Start the async context

        # Create initial session
        self.logger.info("Creating initial Omnara session...")
        if not self.omnara_client:
            raise RuntimeError("Omnara client not initialized")

        # If we have an initial prompt, send it as a USER message to Omnara
        if self.initial_prompt:
            self.logger.info("Sending initial prompt as user message to Omnara")
            # Send agent ready message (not waiting for input since we have the prompt)
            await self.omnara_client.send_message(
                content="Claude Code session started - processing your request",
                agent_instance_id=self.session_id,
                agent_type="Claude Code (Headless)",
                requires_user_input=False,
            )

            # Send the prompt as a USER message so it shows in the dashboard
            await self.omnara_client.send_user_message(
                agent_instance_id=self.session_id,
                content=self.initial_prompt,
            )
            # Return the prompt as the first user input
            return self.initial_prompt
        else:
            # No initial prompt, send agent message and wait for user input
            response = await self.omnara_client.send_message(
                content="Claude Code session started - ready for your instructions",
                agent_instance_id=self.session_id,
                agent_type="Claude Code (Headless)",
                requires_user_input=True,  # Wait for user input
            )

            # Process any initial queued messages
            if response.queued_user_messages:
                return response.queued_user_messages[
                    0
                ]  # Return first user message to start with

        return None

    async def send_to_omnara(
        self,
        content: str,
        requires_user_input: bool = False,
    ) -> Optional[str]:
        """Send a message to Omnara and optionally wait for user response.

        Args:
            content: Message content to send
            requires_user_input: Whether to wait for user input
        """
        if not self.omnara_client or not self.session_id:
            self.logger.error("Omnara client not initialized")
            return None

        try:
            # Get git diff if requested, but only if it changed
            git_diff = None
            current_diff = self.git_tracker.get_diff()
            if current_diff != self.previous_git_diff:
                git_diff = current_diff
                self.previous_git_diff = current_diff
                self.logger.info(
                    f"Git diff changed, sending {len(git_diff) if git_diff else 0} chars"
                )
                if git_diff:
                    self.logger.debug(f"Git diff preview: {git_diff[:200]}...")
            else:
                self.logger.info("Git diff unchanged, not sending")

            response = await self.omnara_client.send_message(
                content=content,
                agent_type="Claude Code (Headless)",
                agent_instance_id=self.session_id,
                requires_user_input=requires_user_input,
                git_diff=git_diff,
                timeout_minutes=1440,  # 24 hours max wait
                poll_interval=3.0,
            )

            # Store message ID for potential user input requests
            if hasattr(response, "message_id"):
                self.last_message_id = response.message_id

            # If we asked for user input, return the first response as string
            if requires_user_input and response.queued_user_messages:
                return response.queued_user_messages[0]

            # For intermediate messages, return None (we stored the message_id above)
            return None

        except Exception as e:
            self.logger.error(f"Failed to send message to Omnara: {e}")

        return None

    def format_message_content(self, message) -> str:
        """Format a Claude SDK message for display in Omnara."""
        if isinstance(message, AssistantMessage):
            parts = []

            for block in message.content:
                if isinstance(block, TextBlock):
                    parts.append(block.text)
                elif isinstance(block, ToolUseBlock):
                    parts.append(f"🔧 Using tool: {block.name}")
                    if hasattr(block, "input") and block.input:
                        # Add key details about tool usage
                        if block.name == "Write" and "file_path" in block.input:
                            parts.append(f"   Writing to: {block.input['file_path']}")
                        elif block.name == "Read" and "file_path" in block.input:
                            parts.append(f"   Reading: {block.input['file_path']}")
                        elif block.name == "Bash" and "command" in block.input:
                            parts.append(f"   Command: {block.input['command']}")
                elif isinstance(block, ToolResultBlock):
                    # Summarize tool results without overwhelming detail
                    if hasattr(block, "content") and block.content:
                        result_summary = str(block.content)[:200]
                        if len(str(block.content)) > 200:
                            result_summary += "..."
                        parts.append(f"   Result: {result_summary}")

            return "\n".join(parts) if parts else "Claude is thinking..."

        elif isinstance(message, UserMessage):
            # UserMessage should have content attribute
            content = getattr(message, "content", str(message))
            return f"User: {content}"
        elif isinstance(message, SystemMessage):
            # SystemMessage might not have content attribute, handle gracefully
            content = getattr(
                message, "content", getattr(message, "text", str(message))
            )
            return f"System: {content}"
        elif isinstance(message, ResultMessage):
            # ResultMessage also might not have content
            content = getattr(
                message,
                "content",
                getattr(message, "text", "Claude completed this task."),
            )
            return content if content != str(message) else "Claude completed this task."

        return str(message)

    async def run_conversation_turn(self, user_input: str) -> Optional[str]:
        """Run a single conversation turn with Claude and return next user input needed."""
        self.logger.info(
            f"Starting conversation turn with input: {user_input[:100]}..."
        )

        if not self.claude_client:
            self.logger.error("Claude client not initialized")
            return None

        try:
            # Send the user input to the persistent Claude client
            if not self.conversation_started:
                # Just use the input directly - it already contains the full prompt
                await self.claude_client.query(user_input)
                self.conversation_started = True
            else:
                # For subsequent messages, just send the user input
                await self.claude_client.query(user_input)

            # Stream Claude's response - send everything immediately
            message_count = 0
            async for message in self.claude_client.receive_response():
                message_count += 1
                self.logger.info(
                    f"Message #{message_count} - Type: {type(message).__name__}"
                )

                if isinstance(message, SystemMessage):
                    # Log system messages but don't send to Omnara
                    if hasattr(message, "subtype") and message.subtype == "init":
                        self.logger.warning(
                            "Received init SystemMessage - context may have been reset"
                        )
                    self.logger.info(f"System message: {message}")
                    continue

                if isinstance(message, UserMessage):
                    # Log user messages but don't send to Omnara (these are echoes of user input)
                    self.logger.debug(f"User message (not forwarding): {message}")
                    continue

                if isinstance(message, ResultMessage):
                    # Conversation turn is complete
                    self.logger.info("Conversation turn completed")
                    break

                # Format and send all other messages (mainly AssistantMessages) immediately
                formatted_content = self.format_message_content(message)
                if formatted_content:
                    await self.send_to_omnara(
                        formatted_content,
                        requires_user_input=False,
                    )

            # After all messages are sent, request user input on the last message
            if self.last_message_id and self.omnara_client:
                self.logger.info(
                    f"Requesting user input on last message {self.last_message_id}"
                )
                try:
                    user_responses = await self.omnara_client.request_user_input(
                        message_id=self.last_message_id,
                        timeout_minutes=1440,
                        poll_interval=3.0,
                    )
                    if user_responses:
                        return user_responses[0]
                except Exception as e:
                    self.logger.error(f"Failed to request user input: {e}")

            # Fallback if no last_message_id or request failed
            next_user_input = await self.send_to_omnara(
                "What would you like me to do next?",
                requires_user_input=True,
            )
            return next_user_input

        except CLINotFoundError:
            error_msg = "❌ Claude Code CLI not found. Please install it with: npm install -g @anthropic-ai/claude-code"
            await self.send_to_omnara(error_msg, requires_user_input=True)
            self.logger.error(error_msg)
            return None
        except ProcessError as e:
            error_msg = f"❌ Claude Code process error: {e}"
            await self.send_to_omnara(error_msg, requires_user_input=True)
            self.logger.error(error_msg)
            return None
        except Exception as e:
            error_msg = f"❌ Error during conversation turn: {e}"
            await self.send_to_omnara(error_msg, requires_user_input=True)
            self.logger.error(error_msg)
            return None

        return None

    async def run(self):
        """Main run loop for the headless Claude runner."""
        try:
            # Initialize Omnara connection
            initial_user_input = await self.initialize()

            if not initial_user_input:
                # Wait for first user input
                self.logger.info("Waiting for initial user input...")
                initial_user_input = await self.send_to_omnara(
                    "Headless Claude is ready. What would you like me to help you with?",
                    requires_user_input=True,
                )

            if not initial_user_input:
                self.logger.error("Failed to get initial user input")
                return

            # Start with the user input
            current_input = initial_user_input

            # Main conversation loop
            while self.running and current_input:
                next_input = await self.run_conversation_turn(current_input)

                if next_input:
                    current_input = next_input
                else:
                    self.logger.info("No more user input, ending session")
                    break

        except (KeyboardInterrupt, asyncio.CancelledError):
            self.logger.info("Received interrupt/cancel signal, shutting down...")
            self.running = False
        except Exception as e:
            self.logger.error(f"Fatal error in headless runner: {e}")
            if self.omnara_client and self.session_id:
                await self.send_to_omnara(
                    f"Headless Claude encountered a fatal error: {e}",
                    requires_user_input=False,
                )
        finally:
            # Clean up
            if self.claude_client:
                try:
                    await self.claude_client.__aexit__(None, None, None)
                    self.logger.info("Claude client closed")
                except Exception as e:
                    self.logger.error(f"Error closing Claude client: {e}")

            if self.omnara_client and self.session_id:
                try:
                    await self.omnara_client.end_session(self.session_id)
                    self.logger.info("Session ended successfully")
                except Exception as e:
                    self.logger.error(f"Error ending session: {e}")

            if self.omnara_client:
                await self.omnara_client.close()


def parse_list_argument(value: str) -> List[str]:
    """Parse a comma-separated list argument."""
    if not value:
        return []
    return [item.strip() for item in value.split(",") if item.strip()]


def main():
    """Main entry point for headless Claude Code integration."""
    parser = argparse.ArgumentParser(
        description="Headless Claude Code integration with Omnara",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )

    # Omnara configuration
    parser.add_argument("--api-key", required=True, help="Omnara API key")
    parser.add_argument(
        "--base-url",
        default="https://agent-dashboard-mcp.onrender.com",
        help="Omnara base URL",
    )

    # Claude Code configuration
    parser.add_argument(
        "--prompt",
        default="You are starting a coding session",
        help="Initial prompt to send to Claude",
    )
    parser.add_argument(
        "--permission-mode",
        choices=["acceptEdits", "bypassPermissions", "default", "plan"],
        help="Permission mode for Claude Code",
    )
    parser.add_argument(
        "--allowed-tools",
        type=str,
        help="Comma-separated list of allowed tools (e.g., 'Read,Write,Bash')",
    )
    parser.add_argument(
        "--disallowed-tools", type=str, help="Comma-separated list of disallowed tools"
    )
    parser.add_argument(
        "--cwd",
        type=str,
        help="Working directory for Claude (defaults to current directory)",
    )
    parser.add_argument(
        "--session-id",
        type=str,
        help="Custom session ID for logging (defaults to random UUID)",
    )

    args, unknown_args = parser.parse_known_args()

    # Setup logging with session ID (default to random UUID if not provided)
    session_id = (
        args.session_id
        if hasattr(args, "session_id") and args.session_id
        else str(uuid.uuid4())
    )
    logger = setup_logging(session_id)

    # Parse list arguments
    allowed_tools = (
        parse_list_argument(args.allowed_tools) if args.allowed_tools else None
    )
    disallowed_tools = (
        parse_list_argument(args.disallowed_tools) if args.disallowed_tools else None
    )

    # Convert unknown arguments to extra_args dict for Claude Code SDK
    extra_args: Dict[str, Optional[str]] = {}
    i = 0
    while i < len(unknown_args):
        arg = unknown_args[i]
        if arg.startswith("--"):
            key = arg[2:]  # Remove '--' prefix
            # Check if next argument is the value (doesn't start with '-')
            if i + 1 < len(unknown_args) and not unknown_args[i + 1].startswith("-"):
                extra_args[key] = unknown_args[i + 1]
                i += 2
            else:
                # Flag without value
                extra_args[key] = None
                i += 1
        else:
            # Skip non-flag arguments
            i += 1

    # Create and run headless Claude
    runner = HeadlessClaudeRunner(
        omnara_api_key=args.api_key,
        session_id=session_id,
        omnara_base_url=args.base_url,
        initial_prompt=args.prompt,
        extra_args=extra_args,
        permission_mode=args.permission_mode,
        allowed_tools=allowed_tools,
        disallowed_tools=disallowed_tools,
        cwd=args.cwd,
    )

    logger.info("Starting headless Claude Code session...")

    try:
        asyncio.run(runner.run())
    except KeyboardInterrupt:
        logger.info("Headless Claude session interrupted by user")
    except Exception as e:
        logger.error(f"Fatal error: {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()
