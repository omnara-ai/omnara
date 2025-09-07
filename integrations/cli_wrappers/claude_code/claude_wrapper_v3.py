#!/usr/bin/env python3
"""
Claude Wrapper V3 (Refactored) - Simplified bidirectional wrapper with better async/sync separation

Key improvements:
- Sync operations where async isn't needed
- Cancellable request_user_input for race condition handling
- Clear separation of concerns
"""

import argparse
import asyncio
import json
import logging
import os
import pty
import re
import select
import shutil
import signal
import sys
import termios
import threading
import time
import tty
import uuid
from collections import deque
from pathlib import Path
from typing import Any, Dict, Optional
from omnara.sdk.async_client import AsyncOmnaraClient
from omnara.sdk.client import OmnaraClient
from omnara.sdk.exceptions import AuthenticationError, APIError
from integrations.cli_wrappers.claude_code.session_reset_handler import (
    SessionResetHandler,
)
from integrations.cli_wrappers.claude_code.format_utils import format_content_block
from integrations.utils.git_utils import GitDiffTracker


# Constants
CLAUDE_LOG_BASE = Path.home() / ".claude" / "projects"
OMNARA_WRAPPER_LOG_DIR = Path.home() / ".omnara" / "claude_wrapper"


def find_claude_cli():
    """Find Claude CLI binary"""
    if cli := shutil.which("claude"):
        return cli

    locations = [
        Path.home() / ".npm-global/bin/claude",
        Path("/usr/local/bin/claude"),
        Path.home() / ".local/bin/claude",
        Path.home() / "node_modules/.bin/claude",
        Path.home() / ".yarn/bin/claude",
        Path.home() / ".claude/local/claude",
    ]

    for path in locations:
        if path.exists() and path.is_file():
            return str(path)

    raise FileNotFoundError(
        "Claude CLI not found. Install with: npm install -g @anthropic-ai/claude-code"
    )


class MessageProcessor:
    """Message processing implementation"""

    def __init__(self, wrapper: "ClaudeWrapperV3"):
        self.wrapper = wrapper
        self.last_message_id = None
        self.last_message_time = None
        self.web_ui_messages = set()  # Track messages from web UI to avoid duplicates
        self.pending_input_message_id = None  # Track if we're waiting for input
        self.last_was_tool_use = False  # Track if last assistant message used tools

    def process_user_message_sync(self, content: str, from_web: bool) -> None:
        """Process a user message (sync version for monitor thread)"""
        if from_web:
            # Message from web UI - track it to avoid duplicate sends
            self.web_ui_messages.add(content)
        else:
            # Message from CLI - send to Omnara if not already from web
            if content not in self.web_ui_messages:
                self.wrapper.log(
                    f"[INFO] Sending CLI message to Omnara: {content[:50]}..."
                )
                if self.wrapper.agent_instance_id and self.wrapper.omnara_client_sync:
                    self.wrapper.omnara_client_sync.send_user_message(
                        agent_instance_id=self.wrapper.agent_instance_id,
                        content=content,
                    )
            else:
                # Remove from tracking set
                self.web_ui_messages.discard(content)

        # Reset idle timer and clear pending input
        self.last_message_time = time.time()
        self.pending_input_message_id = None

    def process_assistant_message_sync(
        self, content: str, tools_used: list[str]
    ) -> None:
        """Process an assistant message (sync version for monitor thread)"""
        if not self.wrapper.agent_instance_id or not self.wrapper.omnara_client_sync:
            return

        # Use lock to ensure atomic message processing
        with self.wrapper.send_message_lock:
            # Track if this message uses tools
            self.last_was_tool_use = bool(tools_used)

            # Sanitize content - remove NUL characters and control characters that break the API
            # This handles binary content from .docx, PDFs, etc.
            sanitized_content = "".join(
                char if ord(char) >= 32 or char in "\n\r\t" else ""
                for char in content.replace("\x00", "")
            )

            # Get git diff if enabled
            git_diff = (
                self.wrapper.git_tracker.get_diff()
                if self.wrapper.git_tracker
                else None
            )
            # Sanitize git diff as well if present (handles binary files in git diff)
            if git_diff:
                git_diff = "".join(
                    char if ord(char) >= 32 or char in "\n\r\t" else ""
                    for char in git_diff.replace("\x00", "")
                )

            # Send to Omnara
            response = self.wrapper.omnara_client_sync.send_message(
                content=sanitized_content,
                agent_type=self.wrapper.name,
                agent_instance_id=self.wrapper.agent_instance_id,
                requires_user_input=False,
                git_diff=git_diff,
            )

            # Track message for idle detection
            self.last_message_id = response.message_id
            self.last_message_time = time.time()

            # Clear old tracked input requests since we have a new message
            self.wrapper.requested_input_messages.clear()

            # Clear pending permission options since we have a new message
            self.wrapper.pending_permission_options.clear()

            # Process any queued user messages
            if response.queued_user_messages:
                concatenated = "\n".join(response.queued_user_messages)
                self.web_ui_messages.add(concatenated)
                self.wrapper.input_queue.append(concatenated)

    def should_request_input(self) -> Optional[str]:
        """Check if we should request input, returns message_id if yes"""
        # Don't request input if we might have a permission prompt
        if self.last_was_tool_use and self.wrapper.is_claude_idle():
            # We're in a state where a permission prompt might appear
            return None

        # Only request if:
        # 1. We have a message to request input for
        # 2. We haven't already requested input for it
        # 3. Claude is idle
        if (
            self.last_message_id
            and self.last_message_id != self.pending_input_message_id
            and self.wrapper.is_claude_idle()
        ):
            return self.last_message_id

        return None

    def mark_input_requested(self, message_id: str) -> None:
        """Mark that input has been requested for a message"""
        self.pending_input_message_id = message_id


class ClaudeWrapperV3:
    def __init__(
        self,
        api_key: Optional[str] = None,
        base_url: Optional[str] = None,
        permission_mode: Optional[str] = None,
        dangerously_skip_permissions: bool = False,
        name: str = "Claude Code",
        idle_delay: float = 3.5,
    ):
        # Session management
        self.agent_instance_id = str(uuid.uuid4())
        self.permission_mode = permission_mode
        self.dangerously_skip_permissions = dangerously_skip_permissions
        self.name = name
        self.idle_delay = idle_delay

        # Set up logging
        self.debug_log_file = None
        self._init_logging()

        self.log(f"[INFO] Agent Instance ID: {self.agent_instance_id}")

        # Omnara SDK setup
        self.api_key = api_key or os.environ.get("OMNARA_API_KEY")
        if not self.api_key:
            print(
                "ERROR: API key must be provided via --api-key or OMNARA_API_KEY environment variable",
                file=sys.stderr,
            )
            sys.exit(1)

        self.base_url = base_url or os.environ.get(
            "OMNARA_BASE_URL", "https://agent-dashboard-mcp.onrender.com"
        )
        self.omnara_client_async: Optional[AsyncOmnaraClient] = None
        self.omnara_client_sync: Optional[OmnaraClient] = None

        # Terminal interaction setup
        self.child_pid = None
        self.master_fd = None
        self.original_tty_attrs = None
        self.input_queue = deque()
        self.stdin_line_buffer = ""  # Buffer to accumulate stdin input until Enter

        # Session reset handler
        self.reset_handler = SessionResetHandler(log_func=self.log)

        # Claude JSONL log monitoring
        self.claude_jsonl_path = None
        self.jsonl_monitor_thread = None
        self.running = True
        # Heartbeat
        self.heartbeat_thread = None
        self.heartbeat_interval = 30.0  # seconds

        # Claude status monitoring
        self.terminal_buffer = ""
        self.last_esc_interrupt_seen = None

        # Message processor
        self.message_processor = MessageProcessor(self)

        # Async task management
        self.pending_input_task = None
        self.async_loop = None
        self.requested_input_messages = (
            set()
        )  # Track messages we've already requested input for
        self.pending_permission_options = {}  # Map option text to number for permission prompts
        self.send_message_lock = (
            threading.Lock()
        )  # Lock for message sending synchronization

        # Git diff tracking
        self.git_tracker: Optional[GitDiffTracker] = None
        self._init_git_tracker()

    def _suspend_for_ctrl_z(self):
        """Handle Ctrl+Z while in raw mode: restore TTY, stop child and self."""
        try:
            self.log("[INFO] Ctrl+Z detected: suspending (raw mode)")
            if self.original_tty_attrs:
                termios.tcsetattr(sys.stdin, termios.TCSADRAIN, self.original_tty_attrs)
            if self.child_pid:
                try:
                    os.kill(self.child_pid, signal.SIGTSTP)
                except Exception as e:
                    self.log(f"[WARNING] Failed to SIGTSTP child: {e}")
            # Suspend this wrapper
            try:
                os.kill(os.getpid(), signal.SIGTSTP)
            except Exception as e:
                self.log(f"[WARNING] Failed to SIGTSTP self: {e}")
        except Exception as e:
            self.log(f"[ERROR] Error handling Ctrl+Z: {e}")

    def _init_logging(self):
        """Initialize debug logging"""
        try:
            OMNARA_WRAPPER_LOG_DIR.mkdir(exist_ok=True, parents=True)
            log_file_path = OMNARA_WRAPPER_LOG_DIR / f"{self.agent_instance_id}.log"
            self.debug_log_file = open(log_file_path, "w")
            self.log(
                f"=== Claude Wrapper V3 Debug Log - {time.strftime('%Y-%m-%d %H:%M:%S')} ==="
            )
        except Exception as e:
            print(f"Failed to create debug log file: {e}", file=sys.stderr)

    def _init_git_tracker(self):
        """Initialize git diff tracking"""
        try:
            # Create a logger that routes to our debug log
            git_logger = logging.getLogger("ClaudeWrapper.GitTracker")
            git_logger.setLevel(logging.DEBUG)

            # Add a custom handler that uses our log method
            class LogHandler(logging.Handler):
                def __init__(self, log_func):
                    super().__init__()
                    self.log_func = log_func

                def emit(self, record):
                    msg = self.format(record)
                    level = record.levelname
                    self.log_func(f"[{level}] {msg}")

            handler = LogHandler(self.log)
            handler.setFormatter(logging.Formatter("%(message)s"))
            git_logger.addHandler(handler)
            git_logger.propagate = False  # Don't propagate to root logger

            self.git_tracker = GitDiffTracker(enabled=True, logger=git_logger)
        except Exception as e:
            self.log(f"[WARNING] Failed to initialize git tracker: {e}")
            self.git_tracker = None

    def log(self, message: str):
        """Write to debug log file"""
        if self.debug_log_file:
            try:
                self.debug_log_file.write(f"[{time.strftime('%H:%M:%S')}] {message}\n")
                self.debug_log_file.flush()
            except Exception:
                pass

    def init_omnara_clients(self):
        """Initialize both sync and async Omnara SDK clients"""
        if not self.api_key:
            raise ValueError("API key is required to initialize Omnara clients")

        # Initialize sync client
        self.omnara_client_sync = OmnaraClient(
            api_key=self.api_key, base_url=self.base_url
        )

        # Initialize async client (we'll ensure session when needed)
        self.omnara_client_async = AsyncOmnaraClient(
            api_key=self.api_key, base_url=self.base_url
        )

    def _heartbeat_loop(self):
        """Background loop to POST heartbeat while running"""
        if not self.omnara_client_sync:
            return
        session = self.omnara_client_sync.session
        url = (
            self.base_url.rstrip("/")
            + f"/api/v1/agents/instances/{self.agent_instance_id}/heartbeat"
        )
        # Small stagger to avoid herd
        import random

        jitter = random.uniform(0, 2.0)
        time.sleep(jitter)
        while self.running:
            try:
                resp = session.post(url, timeout=10)
                if resp.status_code >= 400:
                    self.log(
                        f"[WARN] Heartbeat failed {resp.status_code}: {resp.text[:120]}"
                    )
            except Exception as e:
                self.log(f"[WARN] Heartbeat error: {e}")
            # Sleep interval with small jitter
            delay = self.heartbeat_interval + random.uniform(-2.0, 2.0)
            if delay < 5:
                delay = 5
            for _ in range(int(delay * 10)):
                if not self.running:
                    break
                time.sleep(0.1)

    def get_project_log_dir(self):
        """Get the Claude project log directory for current working directory"""
        cwd = os.getcwd()
        # Convert path to Claude's format
        project_name = re.sub(r"[^a-zA-Z0-9]", "-", cwd)
        project_dir = CLAUDE_LOG_BASE / project_name
        return project_dir if project_dir.exists() else None

    def monitor_claude_jsonl(self):
        """Monitor Claude's JSONL log file for messages"""
        # Wait for log file to be created
        while self.running and not self.claude_jsonl_path:
            project_dir = self.get_project_log_dir()
            if project_dir:
                expected_filename = f"{self.agent_instance_id}.jsonl"
                expected_path = project_dir / expected_filename
                if expected_path.exists():
                    self.claude_jsonl_path = expected_path
                    self.log(f"[INFO] Found Claude JSONL log: {expected_path}")
                    break
            time.sleep(0.5)

        if not self.claude_jsonl_path:
            return

        # Monitor the file
        while self.running:
            try:
                with open(self.claude_jsonl_path, "r") as f:
                    f.seek(0)  # Start from beginning
                    self.log(
                        f"[INFO] Monitoring JSONL file: {self.claude_jsonl_path.name}"
                    )

                    while self.running:
                        # Check for session reset
                        if self.reset_handler.is_reset_pending():
                            self.log(
                                "[INFO] Session reset pending, waiting for new JSONL file..."
                            )

                            project_dir = self.get_project_log_dir()

                            if project_dir:
                                # Look for new session file
                                new_jsonl_path = (
                                    self.reset_handler.find_reset_session_file(
                                        project_dir=project_dir,
                                        current_file=self.claude_jsonl_path,
                                        max_wait=10.0,
                                    )
                                )
                            else:
                                new_jsonl_path = None
                                self.log("[WARNING] Could not get project directory")

                            if new_jsonl_path:
                                old_path = self.claude_jsonl_path.name
                                self.claude_jsonl_path = new_jsonl_path
                                self.log(
                                    f"[INFO] ✅ Switched from {old_path} to {new_jsonl_path.name}"
                                )

                                # Reset the handler state
                                self.reset_handler.clear_reset_state()

                                # Break out of inner loop to reopen with new file
                                break
                            else:
                                # Couldn't find new file, continue with current
                                self.log(
                                    "[WARNING] Could not find new session file, continuing with current"
                                )
                                self.reset_handler.clear_reset_state()

                        # Read next line from current file
                        line = f.readline()
                        if line:
                            try:
                                data = json.loads(line.strip())
                                # Process directly with sync client
                                self.process_claude_log_entry(data)
                            except json.JSONDecodeError:
                                pass
                        else:
                            # Check if file still exists
                            if not self.claude_jsonl_path.exists():
                                self.log(
                                    "[WARNING] Current JSONL file no longer exists"
                                )
                                break
                            time.sleep(0.1)

            except Exception as e:
                self.log(f"[ERROR] Error monitoring Claude JSONL: {e}")
                # If we hit an error, wait a bit before retrying
                time.sleep(1)

    def process_claude_log_entry(self, data: Dict[str, Any]):
        """Process a log entry from Claude's JSONL (sync)"""
        try:
            msg_type = data.get("type")

            if msg_type == "user":
                # Skip meta messages (like "Caveat:" messages)
                if data.get("isMeta", False):
                    self.log("[INFO] Skipping meta message")
                    return

                # User message
                message = data.get("message", {})
                content = message.get("content", "")

                # Handle both string content and structured content blocks
                if isinstance(content, str) and content:
                    # Skip empty command output
                    if (
                        content.strip()
                        == "<local-command-stdout></local-command-stdout>"
                    ):
                        self.log("[INFO] Skipping empty command output")
                        return

                    # Check for command messages and extract the actual command
                    if "<command-name>" in content:
                        # Parse command name and args
                        command_match = re.search(
                            r"<command-name>(.*?)</command-name>", content
                        )
                        args_match = re.search(
                            r"<command-args>(.*?)</command-args>", content
                        )

                        if command_match:
                            command = command_match.group(1).strip()
                            args = args_match.group(1).strip() if args_match else ""

                            # Replace content with the actual command
                            content = f"{command} {args}".strip()

                    self.log(f"[INFO] User message in JSONL: {content[:50]}...")
                    # CLI user input arrived - cancel any pending web input request
                    self.cancel_pending_input_request()
                    self.message_processor.process_user_message_sync(
                        content, from_web=False
                    )
                elif isinstance(content, list):
                    # Handle structured content (e.g., tool results)
                    formatted_parts = []
                    for block in content:
                        if isinstance(block, dict):
                            formatted_content = format_content_block(block)
                            if formatted_content:
                                formatted_parts.append(formatted_content)

                    if formatted_parts:
                        combined_content = "\n".join(formatted_parts)
                        self.log(
                            f"[INFO] User message with blocks: {combined_content[:100]}..."
                        )
                        # Don't process tool results as user messages
                        # They're just acknowledgements of tool execution

            elif msg_type == "assistant":
                # Claude's response
                message = data.get("message", {})
                content_blocks = message.get("content", [])
                formatted_parts = []
                tools_used = []

                for block in content_blocks:
                    if isinstance(block, dict):
                        formatted_content = format_content_block(block)
                        if formatted_content:
                            formatted_parts.append(formatted_content)
                            # Track if this was a tool use
                            if block.get("type") == "tool_use":
                                tools_used.append(formatted_content)

                # Process message if we have content
                if formatted_parts:
                    message_content = "\n".join(formatted_parts)
                    self.message_processor.process_assistant_message_sync(
                        message_content, tools_used
                    )

            elif msg_type == "summary":
                # Session started
                summary = data.get("summary", "")
                if summary and not self.agent_instance_id and self.omnara_client_sync:
                    # Send initial message
                    self.omnara_client_sync.send_message(
                        content=f"Claude session started: {summary}",
                        agent_type=self.name,
                        agent_instance_id=self.agent_instance_id,
                        requires_user_input=False,
                    )

        except Exception as e:
            self.log(f"[ERROR] Error processing Claude log entry: {e}")

    def is_claude_idle(self):
        """Check if Claude is idle (hasn't shown 'esc to interrupt' for idle_delay seconds)"""
        if self.last_esc_interrupt_seen:
            time_since_esc = time.time() - self.last_esc_interrupt_seen
            return time_since_esc >= self.idle_delay
        return True

    def cancel_pending_input_request(self):
        """Cancel any pending input request task"""
        if self.pending_input_task and not self.pending_input_task.done():
            self.log("[INFO] Cancelling pending input request due to CLI input")
            self.pending_input_task.cancel()
            self.pending_input_task = None

    async def request_user_input_async(self, message_id: str):
        """Async task to request user input from web UI"""
        try:
            self.log(f"[INFO] Starting request_user_input for message {message_id}")

            if not self.omnara_client_async:
                self.log("[ERROR] Omnara async client not initialized")
                return

            # Ensure async client session exists
            await self.omnara_client_async._ensure_session()

            # Long-polling request for user input
            user_responses = await self.omnara_client_async.request_user_input(
                message_id=message_id,
                timeout_minutes=1440,  # 24 hours
                poll_interval=3.0,
            )

            # Process responses
            for response in user_responses:
                self.log(f"[INFO] Got user response from web UI: {response[:50]}...")
                self.message_processor.process_user_message_sync(
                    response, from_web=True
                )
                self.input_queue.append(response)

        except asyncio.CancelledError:
            self.log(f"[INFO] request_user_input cancelled for message {message_id}")
            raise
        except Exception as e:
            self.log(f"[ERROR] Failed to request user input: {e}")

            # If we get a 400 error about message already requiring input,
            # send a new message instead
            if "400" in str(e) and "already requires user input" in str(e):
                self.log("[INFO] Message already requires input, sending new message")
                try:
                    if self.omnara_client_async:
                        response = await self.omnara_client_async.send_message(
                            content="Waiting for your input...",
                            agent_type=self.name,
                            agent_instance_id=self.agent_instance_id,
                            requires_user_input=True,
                            poll_interval=3.0,
                        )
                        self.log(
                            f"[INFO] Sent new message with requires_user_input=True: {response.message_id}"
                        )

                        # Process responses
                        for response in response.queued_user_messages:
                            self.log(
                                f"[INFO] Got user response from web UI: {response[:50]}..."
                            )
                            self.message_processor.process_user_message_sync(
                                response, from_web=True
                            )
                            self.input_queue.append(response)

                except Exception as send_error:
                    self.log(f"[ERROR] Failed to send new message: {send_error}")

    def _extract_permission_prompt(
        self, clean_buffer: str
    ) -> tuple[str, list[str], dict[str, str]]:
        """Extract permission/plan mode prompt from terminal buffer
        Returns: (question, options_list, options_map)
        """

        # Check if this is plan mode - look for the specific options
        is_plan_mode = "Would you like to proceed" in clean_buffer and (
            "auto-accept edits" in clean_buffer
            or "manually approve edits" in clean_buffer
        )

        # Find the question - support both permission and plan mode prompts
        question = ""
        plan_content = ""

        if is_plan_mode:
            # For plan mode, extract the question from buffer
            question = "Would you like to proceed with this plan?"

            # Simple approach: Just use the terminal buffer for plan extraction
            # Look for "Ready to code?" marker in the buffer
            plan_marker = "Ready to code?"
            plan_start = clean_buffer.rfind(plan_marker)

            if plan_start != -1:
                # Extract everything after "Ready to code?" up to the prompt
                plan_end = clean_buffer.find("Would you like to proceed", plan_start)
                if plan_end != -1:
                    plan_content = clean_buffer[
                        plan_start + len(plan_marker) : plan_end
                    ]

                    # Clean up the plan content - remove ANSI codes and box characters
                    lines = []
                    for line in plan_content.split("\n"):
                        # Remove box drawing characters and clean up
                        cleaned = re.sub(r"^[│\s]+", "", line)
                        cleaned = re.sub(r"[│\s]+$", "", cleaned)
                        cleaned = cleaned.strip()

                        # Skip empty lines and box borders
                        if cleaned and not re.match(r"^[╭─╮╰╯]+$", cleaned):
                            lines.append(cleaned)

                    plan_content = "\n".join(lines).strip()
                else:
                    plan_content = ""
            else:
                # No "Ready to code?" found - might be a very short plan or scrolled off
                plan_content = ""
        else:
            # Regular permission prompt - find the actual question
            lines = clean_buffer.split("\n")
            # Look for "Do you want" line - search from end to get most recent
            for i in range(len(lines) - 1, -1, -1):
                line_clean = lines[i].strip().replace("\u2502", "").strip()
                if "Do you want" in line_clean:
                    question = line_clean
                    break

        # Default question if not found
        if not question:
            question = "Permission required"

        # Find the options
        options_dict = {}

        if is_plan_mode:
            # For plan mode, use hardcoded options since they're always the same
            options_dict = {
                "1": "1. Yes, and auto-accept edits",
                "2": "2. Yes, and manually approve edits",
                "3": "3. No, keep planning",
            }
        else:
            # Regular permission prompt - look for numbered options
            lines = clean_buffer.split("\n")

            # Look for lines that start with "1. " to find option groups
            # Then extract consecutive numbered options from that point

            # Find all lines starting with "1. "
            option_starts = []
            for i, line in enumerate(lines):
                clean_line = line.strip().replace("\u2502", "").strip()
                clean_line = clean_line.replace("\u276f", "").strip()
                if re.match(r"^1\.\s+", clean_line):
                    option_starts.append(i)

            # Process the last (most recent) option group
            if option_starts:
                start_line = option_starts[-1]

                # Extract consecutive numbered options from this point
                current_num = 1
                for i in range(
                    start_line, min(start_line + 10, len(lines))
                ):  # Check up to 10 lines
                    clean_line = lines[i].strip().replace("\u2502", "").strip()
                    clean_line = clean_line.replace("\u276f", "").strip()

                    # Check if this line is the expected next option
                    pattern = rf"^{current_num}\.\s+(.+)"
                    match = re.match(pattern, clean_line)
                    if match:
                        options_dict[str(current_num)] = clean_line
                        current_num += 1
                    elif current_num > 1 and not clean_line:
                        # Empty line might be between options, continue
                        continue
                    elif current_num > 1:
                        # Non-empty line that's not an option, stop here
                        break

                # Log summary of what was found
                if options_dict:
                    self.log(f"[INFO] Found {len(options_dict)} permission options")
            else:
                self.log(
                    "[WARNING] No permission options found in buffer, using defaults"
                )

        # Convert to list maintaining order
        options = [options_dict[key] for key in sorted(options_dict.keys())]

        # Build options mapping
        options_map = {}
        if is_plan_mode:
            # For plan mode, use specific mapping
            options_map = {
                "Yes, and auto-accept edits": "1",
                "Yes, and manually approve edits": "2",
                "No, keep planning": "3",
            }
        else:
            # Regular mapping
            for option in options:
                # Parse "1. Yes" -> {"Yes": "1"}
                parts = option.split(". ", 1)
                if len(parts) == 2:
                    number = parts[0].strip()
                    text = parts[1].strip()
                    options_map[text] = number

        # Return plan content as part of question if available
        if plan_content:
            question = f"{question}\n\n{plan_content}"
            # Clear terminal buffer after extracting plan to avoid old plans
            self.terminal_buffer = ""

        return question, options, options_map

    def run_claude_with_pty(self):
        """Run Claude CLI in a PTY"""
        claude_path = find_claude_cli()
        self.log(f"[INFO] Found Claude CLI at: {claude_path}")

        # Always add session ID for tracking
        cmd = [claude_path, "--session-id", self.agent_instance_id]

        # Add permission-mode flag if specified
        if self.permission_mode:
            cmd.extend(["--permission-mode", self.permission_mode])
            self.log(
                f"[INFO] Added permission-mode to Claude command: {self.permission_mode}"
            )

        # Add dangerously-skip-permissions flag if specified
        if self.dangerously_skip_permissions:
            cmd.append("--dangerously-skip-permissions")
            self.log("[INFO] Added dangerously-skip-permissions to Claude command")

        # Log the final command for debugging
        self.log(f"[INFO] Final Claude command: {' '.join(cmd)}")

        # Save original terminal settings
        try:
            self.original_tty_attrs = termios.tcgetattr(sys.stdin)
        except Exception:
            self.original_tty_attrs = None

        # Get terminal size
        try:
            cols, rows = os.get_terminal_size()
            self.log(f"[INFO] Terminal size: {cols}x{rows}")
        except Exception:
            cols, rows = 80, 24

        # Create PTY
        self.child_pid, self.master_fd = pty.fork()

        if self.child_pid == 0:
            # Child process - exec Claude CLI
            os.environ["CLAUDE_CODE_ENTRYPOINT"] = "jsonlog-wrapper"
            os.execvp(cmd[0], cmd)

        # Parent process - set PTY size
        if self.child_pid > 0:
            try:
                import fcntl
                import struct

                TIOCSWINSZ = 0x5414  # Linux
                if sys.platform == "darwin":
                    TIOCSWINSZ = 0x80087467  # macOS

                winsize = struct.pack("HHHH", rows, cols, 0, 0)
                fcntl.ioctl(self.master_fd, TIOCSWINSZ, winsize)
            except Exception:
                pass

        # Parent process - handle I/O
        try:
            if self.original_tty_attrs:
                tty.setraw(sys.stdin)

            # Set non-blocking mode on master_fd
            import fcntl

            flags = fcntl.fcntl(self.master_fd, fcntl.F_GETFL)
            fcntl.fcntl(self.master_fd, fcntl.F_SETFL, flags | os.O_NONBLOCK)

            while self.running:
                # Use select to multiplex I/O
                rlist, _, _ = select.select([sys.stdin, self.master_fd], [], [], 0.01)

                # When expecting permission prompt, check if we need to handle it
                if self.message_processor.last_was_tool_use and self.is_claude_idle():
                    # After tool use + idle, assume permission prompt is shown
                    if not hasattr(self, "_permission_assumed_time"):
                        self._permission_assumed_time = time.time()

                    # After 0.5 seconds, check if we can parse the prompt from buffer
                    elif time.time() - self._permission_assumed_time > 0.5:
                        # Clean the buffer to check for content

                        clean_buffer = re.sub(
                            r"\x1b\[[0-9;]*[a-zA-Z]", "", self.terminal_buffer
                        )

                        # If we see permission/plan prompt, extract it
                        # For plan mode: "Would you like to proceed" without "(esc"
                        # For permission: "Do you want" with "(esc"
                        if (
                            "Do you want" in clean_buffer and "(esc" in clean_buffer
                        ) or (
                            "Would you like to proceed" in clean_buffer
                            and "No, keep planning" in clean_buffer
                        ):
                            if not hasattr(self, "_permission_handled"):
                                self._permission_handled = True

                                # Use lock to ensure atomic permission prompt handling
                                with self.send_message_lock:
                                    # Extract prompt components using the shared method
                                    question, options, options_map = (
                                        self._extract_permission_prompt(clean_buffer)
                                    )

                                    # Build the message
                                    if options:
                                        options_text = "\n".join(options)
                                        permission_msg = f"{question}\n\n[OPTIONS]\n{options_text}\n[/OPTIONS]"
                                        self.pending_permission_options = options_map
                                        self.log(
                                            f"[INFO] Permission prompt with {len(options)} options sent to Omnara"
                                        )
                                    else:
                                        # Fallback if parsing fails
                                        permission_msg = f"{question}\n\n[OPTIONS]\n1. Yes\n2. Yes, and don't ask again this session\n3. No\n[/OPTIONS]"
                                        self.pending_permission_options = {
                                            "Yes": "1",
                                            "Yes, and don't ask again this session": "2",
                                            "No": "3",
                                        }
                                        self.log(
                                            "[WARNING] Using default permission options (extraction failed)"
                                        )

                                    # Send to Omnara with extracted text
                                    if (
                                        self.agent_instance_id
                                        and self.omnara_client_sync
                                    ):
                                        response = self.omnara_client_sync.send_message(
                                            content=permission_msg,
                                            agent_type=self.name,
                                            agent_instance_id=self.agent_instance_id,
                                            requires_user_input=False,
                                        )
                                        self.message_processor.last_message_id = (
                                            response.message_id
                                        )
                                        self.message_processor.last_message_time = (
                                            time.time()
                                        )
                                        self.message_processor.last_was_tool_use = False

                        # Fallback after 1 second if we still don't have the full prompt
                        elif time.time() - self._permission_assumed_time > 1.0:
                            if not hasattr(self, "_permission_handled"):
                                self._permission_handled = True
                            if self.agent_instance_id and self.omnara_client_sync:
                                response = self.omnara_client_sync.send_message(
                                    content="Waiting for your input...",
                                    agent_type=self.name,
                                    agent_instance_id=self.agent_instance_id,
                                    requires_user_input=False,
                                )
                                self.message_processor.last_message_id = (
                                    response.message_id
                                )
                                self.message_processor.last_message_time = time.time()
                                self.message_processor.last_was_tool_use = False

                else:
                    # Clear state when conditions change
                    if hasattr(self, "_permission_assumed_time"):
                        delattr(self, "_permission_assumed_time")
                    if hasattr(self, "_permission_handled"):
                        delattr(self, "_permission_handled")

                # Handle terminal output from Claude
                if self.master_fd in rlist:
                    try:
                        data = os.read(self.master_fd, 65536)
                        if data:
                            # Write to stdout
                            os.write(sys.stdout.fileno(), data)
                            sys.stdout.flush()

                            # Check for "esc to interrupt" indicator
                            try:
                                text = data.decode("utf-8", errors="ignore")
                                self.terminal_buffer += text

                                # Keep buffer large enough for long plans
                                if len(self.terminal_buffer) > 200000:
                                    self.terminal_buffer = self.terminal_buffer[
                                        -200000:
                                    ]

                                # Check for the indicator
                                clean_text = re.sub(r"\x1b\[[0-9;]*m", "", text)

                                # Check for both "esc to interrupt" and "ctrl+b to run in background"
                                if (
                                    "esc to interrupt" in clean_text
                                    or "ctrl+b to run in background" in clean_text
                                ):
                                    self.last_esc_interrupt_seen = time.time()

                            except Exception:
                                pass
                        else:
                            # Claude process has exited - trigger cleanup
                            self.log(
                                "[INFO] Claude process exited, shutting down wrapper"
                            )
                            self.running = False
                            if self.async_loop and self.async_loop.is_running():
                                self.async_loop.call_soon_threadsafe(
                                    self.async_loop.stop
                                )
                            break
                    except BlockingIOError:
                        pass
                    except OSError:
                        # Claude process has exited - trigger cleanup
                        self.log(
                            "[INFO] Claude process exited (OSError), shutting down wrapper"
                        )
                        self.running = False
                        if self.async_loop and self.async_loop.is_running():
                            self.async_loop.call_soon_threadsafe(self.async_loop.stop)
                        break

                # Handle user input from stdin
                if sys.stdin in rlist and self.original_tty_attrs:
                    try:
                        # Read available data (larger buffer for efficiency)
                        data = os.read(sys.stdin.fileno(), 65536)
                        if data and b"\x1a" in data:
                            data = data.replace(b"\x1a", b"")
                            # Ctrl+Z: suspend child and wrapper
                            self._suspend_for_ctrl_z()
                            try:
                                if self.original_tty_attrs:
                                    tty.setraw(sys.stdin)
                            except Exception:
                                pass
                        if data:
                            # Log user input for debugging
                            try:
                                # Try to decode and log readable text
                                text_input = data.decode("utf-8", errors="replace")

                                # Process the input character by character to handle backspaces
                                for char in text_input:
                                    if char in ["\x7f", "\x08"]:  # Backspace or DEL
                                        # Remove last character from buffer if present
                                        if self.stdin_line_buffer:
                                            self.stdin_line_buffer = (
                                                self.stdin_line_buffer[:-1]
                                            )
                                    elif char not in ["\n", "\r"]:
                                        # Add regular characters to buffer
                                        self.stdin_line_buffer += char

                                # Check if Enter was pressed (newline or carriage return)
                                if "\n" in text_input or "\r" in text_input:
                                    # Log the complete line
                                    line = self.stdin_line_buffer.strip()
                                    if line:
                                        self.log(f"[STDIN] User entered: {repr(line)}")

                                        # Clean the line - remove escape sequences and get just the text
                                        # Remove various ANSI escape sequences
                                        clean_line = re.sub(
                                            r"\x1b\[[^m]*m", "", line
                                        )  # Color codes
                                        clean_line = re.sub(
                                            r"\x1b\[[0-9;]*[A-Za-z]", "", clean_line
                                        )  # Cursor movement
                                        clean_line = re.sub(
                                            r"\x1b[>=\[\]OPI]", "", clean_line
                                        )  # Various single char escapes
                                        clean_line = re.sub(
                                            r"\x1b\([AB012]", "", clean_line
                                        )  # Character set selection
                                        clean_line = re.sub(
                                            r"\x1b\].*?\x07", "", clean_line
                                        )  # OSC sequences
                                        # Remove all remaining control characters except spaces
                                        clean_line = "".join(
                                            c
                                            for c in clean_line
                                            if c.isprintable() or c.isspace()
                                        )
                                        clean_line = clean_line.strip()

                                        # Check for special commands like /clear
                                        if clean_line.startswith("/"):
                                            self.log(
                                                f"[STDIN] ⚠️ Detected slash command: {clean_line}"
                                            )

                                            # Check for session reset commands
                                            if self.reset_handler.check_for_reset_command(
                                                clean_line
                                            ):
                                                self.reset_handler.mark_reset_detected(
                                                    clean_line
                                                )

                                    # Reset buffer for next line
                                    self.stdin_line_buffer = ""
                            except Exception:
                                # If decode fails, log the raw bytes
                                self.log(
                                    f"[STDIN] User input (raw bytes): {data[:100]}"
                                )

                            # Store data in a buffer attribute if PTY is full
                            if not hasattr(self, "pending_write_buffer"):
                                self.pending_write_buffer = b""

                            # Add new data to any pending data (post-processed)
                            self.pending_write_buffer += data

                            # Try to write as much as possible
                            if self.pending_write_buffer:
                                try:
                                    bytes_written = os.write(
                                        self.master_fd, self.pending_write_buffer
                                    )
                                    # Remove written data from buffer
                                    self.pending_write_buffer = (
                                        self.pending_write_buffer[bytes_written:]
                                    )
                                except OSError as e:
                                    if e.errno in (
                                        35,
                                        11,
                                    ):  # EAGAIN/EWOULDBLOCK (35=macOS, 11=Linux)
                                        # PTY buffer full, data remains in pending_write_buffer
                                        pass
                                    else:
                                        self.log(
                                            f"[ERROR] Unexpected error writing to PTY: {e}"
                                        )
                                        raise
                    except OSError as e:
                        self.log(f"[ERROR] Error reading from stdin: {e}")
                        pass

                # Try to flush pending write buffer when PTY might be ready
                if hasattr(self, "pending_write_buffer") and self.pending_write_buffer:
                    try:
                        bytes_written = os.write(
                            self.master_fd, self.pending_write_buffer
                        )
                        self.pending_write_buffer = self.pending_write_buffer[
                            bytes_written:
                        ]
                    except OSError as e:
                        if e.errno not in (35, 11):  # Log unexpected errors
                            self.log(f"[ERROR] Unexpected error flushing buffer: {e}")
                        # PTY still full or other error, will retry next iteration
                        pass

                # Process messages from Omnara web UI
                if self.input_queue:
                    content = self.input_queue.popleft()

                    # Check if this is a permission prompt response
                    if self.pending_permission_options:
                        if content in self.pending_permission_options:
                            # Convert full text to number
                            converted = self.pending_permission_options[content]
                            self.log(
                                f"[INFO] Converting permission response '{content}' to '{converted}'"
                            )
                            content = converted
                        else:
                            # Default to the highest numbered option (last option)
                            max_option = max(self.pending_permission_options.values())
                            self.log(
                                f"[INFO] Unmatched permission response '{content}' - defaulting to option {max_option}"
                            )
                            content = max_option

                        # Always clear the mapping after handling a permission response
                        self.pending_permission_options = {}
                        self.terminal_buffer = ""

                    self.log(
                        f"[INFO] Sending web UI message to Claude: {content[:50]}..."
                    )

                    # Check for session reset commands from web UI
                    if self.reset_handler.check_for_reset_command(content.strip()):
                        self.reset_handler.mark_reset_detected(content.strip())

                    # Send to Claude
                    os.write(self.master_fd, content.encode())
                    time.sleep(0.1)
                    os.write(self.master_fd, b"\r")

        finally:
            # Restore terminal settings
            if self.original_tty_attrs:
                termios.tcsetattr(sys.stdin, termios.TCSADRAIN, self.original_tty_attrs)

            # Clean up child process
            if self.child_pid:
                try:
                    os.kill(self.child_pid, signal.SIGTERM)
                    os.waitpid(self.child_pid, 0)
                except Exception:
                    pass

    async def idle_monitor_loop(self):
        """Async loop to monitor idle state and request input"""
        self.log("[INFO] Started idle monitor loop")

        if not self.omnara_client_async:
            self.log("[ERROR] Omnara async client not initialized")
            return

        # Ensure async client session
        await self.omnara_client_async._ensure_session()

        while self.running:
            await asyncio.sleep(0.5)  # Check every 500ms

            # Check if we should request input
            message_id = self.message_processor.should_request_input()

            if message_id and message_id in self.requested_input_messages:
                await asyncio.sleep(0.5)
                self.requested_input_messages.clear()
            elif message_id and message_id not in self.requested_input_messages:
                self.log(
                    f"[INFO] Claude is idle, starting request_user_input for message {message_id}"
                )

                # Track that we've requested input for this message
                self.requested_input_messages.add(message_id)

                # Mark as requested
                self.message_processor.mark_input_requested(message_id)

                # Cancel any existing task
                self.cancel_pending_input_request()

                # Start new input request task
                self.pending_input_task = asyncio.create_task(
                    self.request_user_input_async(message_id)
                )

    def run(self):
        """Run Claude with Omnara integration (main entry point)"""
        self.log("[INFO] Starting run() method")

        try:
            # Initialize Omnara clients (sync)
            self.log("[INFO] Initializing Omnara clients...")
            self.init_omnara_clients()
            self.log("[INFO] Omnara clients initialized")

            # Create initial session (sync)
            self.log("[INFO] Creating initial Omnara session...")
            if self.omnara_client_sync:
                response = self.omnara_client_sync.send_message(
                    content=f"{self.name} session started - waiting for your input...",
                    agent_type=self.name,
                    agent_instance_id=self.agent_instance_id,
                    requires_user_input=False,
                )

                # Initialize message processor with first message
                if hasattr(self.message_processor, "last_message_id"):
                    self.message_processor.last_message_id = response.message_id
                    self.message_processor.last_message_time = time.time()

            # Start heartbeat thread
            try:
                if not self.heartbeat_thread:
                    self.heartbeat_thread = threading.Thread(
                        target=self._heartbeat_loop, daemon=True
                    )
                    self.heartbeat_thread.start()
                    self.log("[INFO] Heartbeat loop started")
            except Exception as e:
                self.log(f"[WARN] Failed to start heartbeat loop: {e}")
        except AuthenticationError as e:
            # Log the error
            self.log(f"[ERROR] Authentication failed: {e}")

            # Print user-friendly error message
            print(
                "\nError: Authentication failed. Please check for valid Omnara API key in ~/.omnara/credentials.json.",
                file=sys.stderr,
            )

            # Clean up and exit
            if self.omnara_client_sync:
                self.omnara_client_sync.close()
            if self.debug_log_file:
                self.debug_log_file.close()
            sys.exit(1)

        except APIError as e:
            # Log the error
            self.log(f"[ERROR] API error: {e}")

            # Print user-friendly error message based on status code
            if e.status_code >= 500:
                print(
                    "\nError: Omnara server error. Please try again later.",
                    file=sys.stderr,
                )
            elif e.status_code == 404:
                print(
                    "\nError: Omnara endpoint not found. Please check your base URL.",
                    file=sys.stderr,
                )
            else:
                print(f"\nError: Omnara API error: {e}", file=sys.stderr)

            # Clean up and exit
            if self.omnara_client_sync:
                self.omnara_client_sync.close()
            if self.debug_log_file:
                self.debug_log_file.close()
            sys.exit(1)

        except Exception as e:
            # Log the error
            self.log(f"[ERROR] Failed to initialize Omnara connection: {e}")

            # Print user-friendly error message
            error_msg = str(e)
            if "connection" in error_msg.lower() or "refused" in error_msg.lower():
                print("\nError: Could not connect to Omnara server.", file=sys.stderr)
                print(
                    "Please check your internet connection and try again.",
                    file=sys.stderr,
                )
            else:
                print(
                    f"\nError: Failed to connect to Omnara: {error_msg}",
                    file=sys.stderr,
                )

            # Clean up and exit
            if self.omnara_client_sync:
                self.omnara_client_sync.close()
            if self.debug_log_file:
                self.debug_log_file.close()
            sys.exit(1)

        # Start Claude in PTY (in thread)
        claude_thread = threading.Thread(target=self.run_claude_with_pty)
        claude_thread.daemon = True
        claude_thread.start()

        # Wait a moment for Claude to start
        time.sleep(1.0)

        # Start JSONL monitor thread
        self.jsonl_monitor_thread = threading.Thread(target=self.monitor_claude_jsonl)
        self.jsonl_monitor_thread.daemon = True
        self.jsonl_monitor_thread.start()

        # Run async idle monitor in event loop
        try:
            self.async_loop = asyncio.new_event_loop()
            asyncio.set_event_loop(self.async_loop)
            self.async_loop.run_until_complete(self.idle_monitor_loop())
        except (KeyboardInterrupt, RuntimeError):
            # RuntimeError happens when loop.stop() is called
            pass
        finally:
            # Clean up
            self.running = False
            self.log("[INFO] Shutting down wrapper...")

            # Print exit message immediately for better UX
            if not sys.exc_info()[0]:
                print("\nEnded Omnara Claude Session\n", file=sys.stderr)

            # Quick cleanup - cancel pending tasks
            self.cancel_pending_input_request()

            # Run cleanup in background thread with timeout
            def background_cleanup():
                import threading

                # Create a timer to force exit after 10 seconds
                def force_exit():
                    self.log("[WARNING] Cleanup timeout reached, forcing exit")
                    if self.debug_log_file:
                        self.debug_log_file.flush()
                    os._exit(0)

                timer = threading.Timer(10.0, force_exit)
                timer.daemon = True
                timer.start()

                try:
                    if self.omnara_client_sync and self.agent_instance_id:
                        self.omnara_client_sync.end_session(self.agent_instance_id)
                        self.log("[INFO] Session ended successfully")

                    if self.omnara_client_sync:
                        self.omnara_client_sync.close()

                    if self.omnara_client_async:
                        # Close async client synchronously
                        loop = asyncio.new_event_loop()
                        asyncio.set_event_loop(loop)
                        loop.run_until_complete(self.omnara_client_async.close())
                        loop.close()

                    if self.debug_log_file:
                        self.log("=== Claude Wrapper V3 Log Ended ===")
                        self.debug_log_file.flush()
                        self.debug_log_file.close()

                    # Cancel timer if cleanup completed successfully
                    timer.cancel()

                except Exception as e:
                    self.log(f"[ERROR] Background cleanup error: {e}")
                    if self.debug_log_file:
                        self.debug_log_file.flush()
                    timer.cancel()

            # Start background cleanup as non-daemon thread
            cleanup_thread = threading.Thread(target=background_cleanup)
            cleanup_thread.daemon = False
            cleanup_thread.start()


def main():
    """Main entry point"""
    parser = argparse.ArgumentParser(
        description="Claude wrapper V3 for Omnara integration",
        add_help=False,  # Disable help to pass through to Claude
    )
    parser.add_argument("--api-key", help="Omnara API key")
    parser.add_argument("--base-url", help="Omnara base URL")
    parser.add_argument(
        "--name",
        default="Claude Code",
        help="Name of the agent (defaults to 'Claude Code')",
    )
    parser.add_argument(
        "--permission-mode",
        choices=["acceptEdits", "bypassPermissions", "default", "plan"],
        help="Permission mode to use for the session",
    )
    parser.add_argument(
        "--dangerously-skip-permissions",
        action="store_true",
        help="Bypass all permission checks. Recommended only for sandboxes with no internet access.",
    )
    parser.add_argument(
        "--idle-delay",
        type=float,
        default=3.5,
        help="Delay in seconds before considering Claude idle (default: 3.5)",
    )

    # Parse known args and pass the rest to Claude
    args, claude_args = parser.parse_known_args()

    # Check if --continue or --resume in claude_args and bypass Omnara
    if any(arg in ["--continue", "-c", "--resume", "-r"] for arg in claude_args):
        print(
            "\n⚠️  Warning: --continue and --resume flags are not yet fully supported by Omnara.",
            file=sys.stderr,
        )
        print(
            "   The flags will be passed to Claude Code, but conversation history may not appear in the Omnara dashboard.\n",
            file=sys.stderr,
        )
        try:
            claude_path = find_claude_cli()
            # claude_args already has Omnara flags filtered out!
            os.execvp(claude_path, [claude_path] + claude_args)
            # Never returns - process is replaced
        except FileNotFoundError as e:
            print(f"Error: {e}", file=sys.stderr)
            sys.exit(1)

    # Update sys.argv to only include Claude args
    sys.argv = [sys.argv[0]] + claude_args

    wrapper = ClaudeWrapperV3(
        api_key=args.api_key,
        base_url=args.base_url,
        permission_mode=args.permission_mode,
        dangerously_skip_permissions=args.dangerously_skip_permissions,
        name=args.name,
        idle_delay=args.idle_delay,
    )

    def signal_handler(sig, frame):
        # Check if this is a repeated Ctrl+C (user really wants to exit)
        if not wrapper.running:
            # Second Ctrl+C - exit immediately
            print("\nForce exiting...", file=sys.stderr)
            os._exit(1)

        # First Ctrl+C - initiate graceful shutdown
        wrapper.running = False
        wrapper.log("[INFO] SIGINT received, initiating shutdown")

        # Stop the async event loop to trigger cleanup
        if wrapper.async_loop and wrapper.async_loop.is_running():
            wrapper.async_loop.call_soon_threadsafe(wrapper.async_loop.stop)

        if wrapper.child_pid:
            try:
                # Kill Claude process to trigger exit
                os.kill(wrapper.child_pid, signal.SIGTERM)
            except Exception:
                pass

    def handle_resize(sig, frame):
        """Handle terminal resize signal"""
        if wrapper.master_fd:
            try:
                # Get new terminal size
                cols, rows = os.get_terminal_size()
                # Update PTY size
                import fcntl
                import struct

                TIOCSWINSZ = 0x80087467 if sys.platform == "darwin" else 0x5414
                winsize = struct.pack("HHHH", rows, cols, 0, 0)
                fcntl.ioctl(wrapper.master_fd, TIOCSWINSZ, winsize)
            except Exception:
                pass

    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)  # Handle terminal close
    signal.signal(signal.SIGHUP, signal_handler)  # Handle terminal disconnect
    signal.signal(signal.SIGWINCH, handle_resize)  # Handle terminal resize

    try:
        wrapper.run()
    except Exception as e:
        # Fatal errors still go to stderr
        print(f"Fatal error: {e}", file=sys.stderr)
        if wrapper.original_tty_attrs:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, wrapper.original_tty_attrs)
        if hasattr(wrapper, "debug_log_file") and wrapper.debug_log_file:
            wrapper.log(f"[FATAL] {e}")
            wrapper.debug_log_file.close()
        sys.exit(1)


if __name__ == "__main__":
    main()
