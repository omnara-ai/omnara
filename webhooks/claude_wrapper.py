#!/usr/bin/env python3
"""
Claude JSON Log Wrapper - Run Claude with terminal UI while sending logs to Omnara
"""

import argparse
import asyncio
import json
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
from collections import deque
from pathlib import Path
from typing import Any, Dict, Optional

from omnara.sdk.async_client import AsyncOmnaraClient

# Constants
CLAUDE_LOG_BASE = Path.home() / ".claude" / "projects"


class ClaudeJSONLogWrapper:
    def __init__(self, api_key: Optional[str] = None, base_url: Optional[str] = None):
        # Create a unique session UUID for this instance
        import uuid

        self.session_uuid = str(uuid.uuid4())
        self.instance_id = self.session_uuid[:8]  # Short version for debugging

        # Omnara SDK setup - use provided values or fall back to environment
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
        self.omnara_client: Optional[AsyncOmnaraClient] = None
        self.agent_instance_id: Optional[str] = None
        self.pending_question_id: Optional[str] = None
        self.waiting_for_dashboard_response = False

        # Terminal interaction setup
        self.child_pid = None
        self.master_fd = None
        self.original_tty_attrs = None
        self.input_queue = deque()
        self.current_session_id = None
        self.log_file_path = None
        self.log_monitor_thread = None
        self.running = True
        self.pending_tools = {}  # Track pending tool uses
        self.terminal_buffer = ""  # Buffer for terminal output
        self.permission_active = False
        self.permission_question = None  # Store permission question text
        self.permission_response_time = (
            None  # Track when we last responded to a permission
        )
        self.pending_permission_tool = None  # Track tool waiting for permission
        self.claude_status = (
            "idle"  # idle, working, waiting_for_input, waiting_for_permission
        )
        self.last_terminal_activity = time.time()
        self.terminal_activity_buffer = ""
        self.last_esc_interrupt_seen = None
        self.status_monitor_thread = None
        self.waiting_message_sent = False
        self.last_entry_was_tool_result = False
        self.pending_claude_message = None  # Store Claude message while waiting
        self.pending_claude_message_task = None
        self.check_tool_task = (
            None  # Track the task checking for empty question after tool
        )
        self.existing_logs = set()  # Track log files that existed before we started

        # Debug log file
        self.debug_log_file = None
        self._init_debug_log()

    def _init_debug_log(self):
        """Initialize debug log file."""
        try:
            log_path = Path.home() / ".claude" / "omnara_debug.log"
            log_path.parent.mkdir(exist_ok=True)
            # Clear existing log
            self.debug_log_file = open(log_path, "w")
            self.debug_log(
                f"=== Claude Wrapper Debug Log Started at {time.strftime('%Y-%m-%d %H:%M:%S')} ==="
            )
        except Exception as e:
            print(f"Failed to init debug log: {e}", file=sys.stderr)

    def debug_log(self, message: str):
        """Write to debug log file."""
        if self.debug_log_file:
            try:
                self.debug_log_file.write(
                    f"[{time.strftime('%H:%M:%S')}][{self.instance_id}] {message}\n"
                )
                self.debug_log_file.flush()
            except Exception:
                pass

    async def init_omnara_client(self):
        """Initialize the Omnara SDK client."""
        # Type assertion since we check for None in __init__
        assert self.api_key is not None
        self.omnara_client = AsyncOmnaraClient(
            api_key=self.api_key, base_url=self.base_url
        )
        await self.omnara_client._ensure_session()

    async def log_to_omnara(
        self, description: str, needs_response: bool = False
    ) -> Optional[str]:
        """Log a step or ask a question via Omnara SDK."""
        if not self.omnara_client:
            return None

        try:
            if needs_response:
                # Ask a question and wait for response
                if not self.agent_instance_id:
                    # Create an instance first if we don't have one
                    agent_type = "Claude Code"
                    response = await self.omnara_client.log_step(
                        agent_type=agent_type,
                        step_description="[Session started]",
                        agent_instance_id=None,
                    )
                    self.agent_instance_id = response.agent_instance_id
                    self.debug_log(
                        f"Got agent instance ID from question creation: {self.agent_instance_id}"
                    )

                # Create question but don't wait for response
                data = {
                    "agent_instance_id": self.agent_instance_id,
                    "question_text": description,
                }
                response = await self.omnara_client._make_request(
                    "POST", "/api/v1/questions", json=data, timeout=5
                )
                self.pending_question_id = response["question_id"]
                self.debug_log(
                    f"Created question with ID: {self.pending_question_id} for agent instance: {self.agent_instance_id}"
                )

                # Start a background task to poll for dashboard responses
                asyncio.create_task(
                    self._poll_for_dashboard_answer(response["question_id"])
                )

                return None  # Don't block waiting
            else:
                # Just log a step
                agent_type = "Claude Code"
                response = await self.omnara_client.log_step(
                    agent_type=agent_type,
                    step_description=description,
                    agent_instance_id=self.agent_instance_id,
                )
                # Store instance ID if this is the first call
                if not self.agent_instance_id:
                    self.agent_instance_id = response.agent_instance_id
                    self.debug_log(f"Got agent instance ID: {self.agent_instance_id}")

                # Process any user feedback
                if response.user_feedback:
                    for feedback in response.user_feedback:
                        # User feedback received - could inject into Claude's input if needed
                        pass

        except Exception:
            # Silently handle Omnara errors to avoid terminal interference
            pass

        return None

    async def _poll_for_dashboard_answer(self, question_id: str):
        """Poll for answers from the dashboard in the background."""
        self.debug_log(f"Starting to poll for answer to question: {question_id}")
        try:
            poll_interval = 2.0  # Check every 2 seconds
            timeout = 86400  # 24 hours
            start_time = time.time()

            while time.time() - start_time < timeout:
                if (
                    not self.pending_question_id
                    or self.pending_question_id != question_id
                ):
                    # Question was answered via terminal
                    return

                if not self.omnara_client:
                    return
                status = await self.omnara_client.get_question_status(question_id)
                if status.status == "answered" and status.answer:
                    self.debug_log(
                        f"Got answer for question {question_id}: {status.answer[:50]}..."
                    )
                    # Clear the pending question
                    self.pending_question_id = None
                    # Queue the response to be injected into terminal
                    self.input_queue.append(status.answer)
                    return

                await asyncio.sleep(poll_interval)
        except Exception:
            # Silently handle errors
            pass

    def find_claude_cli(self):
        """Find Claude CLI binary."""
        if cli := shutil.which("claude"):
            return cli

        locations = [
            Path.home() / ".npm-global/bin/claude",
            Path("/usr/local/bin/claude"),
            Path.home() / ".local/bin/claude",
            Path.home() / "node_modules/.bin/claude",
            Path.home() / ".yarn/bin/claude",
        ]

        for path in locations:
            if path.exists() and path.is_file():
                return str(path)

        raise FileNotFoundError(
            "Claude CLI not found. Install with: npm install -g @anthropic-ai/claude-code"
        )

    def get_project_log_dir(self):
        """Get the Claude project log directory for current working directory."""
        cwd = os.getcwd()
        # Convert path to Claude's format
        project_name = cwd.replace("/", "-")
        project_dir = CLAUDE_LOG_BASE / project_name
        return project_dir if project_dir.exists() else None

    def find_latest_log_file(self, project_dir: Path) -> Optional[Path]:
        """Find the most recent JSONL file in the project directory."""
        if not project_dir or not project_dir.exists():
            return None

        jsonl_files = list(project_dir.glob("*.jsonl"))
        if not jsonl_files:
            return None

        # Sort by modification time
        jsonl_files.sort(key=lambda p: p.stat().st_mtime, reverse=True)
        return jsonl_files[0]

    def monitor_log_file(self):
        """Monitor the JSON log file for new messages."""
        # Log monitor thread started

        # Start the status monitor thread too
        if not self.status_monitor_thread:
            self.status_monitor_thread = threading.Thread(
                target=self.monitor_claude_status
            )
            self.status_monitor_thread.daemon = True
            self.status_monitor_thread.start()
            # Status monitor thread started

        # Wait for OUR SPECIFIC session log file to be created
        expected_filename = f"{self.session_uuid}.jsonl"
        self.debug_log(f"Waiting for log file: {expected_filename}")

        while self.running and not self.log_file_path:
            project_dir = self.get_project_log_dir()
            if project_dir:
                # Look for our specific session file
                expected_path = project_dir / expected_filename
                if expected_path.exists():
                    self.log_file_path = expected_path
                    self.debug_log(f"Found our session log file: {expected_filename}")
                    break
            time.sleep(0.5)

        if not self.log_file_path:
            # No log file found
            pass
            return

        # Monitor the file
        try:
            with open(self.log_file_path, "r") as f:
                # Start from beginning to catch existing messages
                f.seek(0)

                while self.running:
                    line = f.readline()
                    if line:
                        try:
                            # Clean the line
                            line = line.strip()
                            if not line:
                                continue

                            data = json.loads(line)

                            # Process in async context
                            asyncio.run_coroutine_threadsafe(
                                self.process_log_entry(data), self.async_loop
                            )
                        except json.JSONDecodeError:
                            # JSON decode error
                            pass
                        except Exception:
                            # Error processing line
                            pass
                    else:
                        # Check if file still exists and is the latest
                        if not self.log_file_path.exists():
                            # Log file no longer exists
                            break
                        time.sleep(0.1)

        except Exception:
            # Error monitoring log file
            pass

    async def process_log_entry(self, data: Dict[str, Any]):
        """Process a log entry and send to Omnara."""
        try:
            msg_type = data.get("type")
            self.debug_log(
                f"Processing log entry type: {msg_type} from file: {self.log_file_path.name if self.log_file_path else 'unknown'}"
            )

            if msg_type == "user":
                # Extract user message
                message = data.get("message", {})
                content = message.get("content", "")

                # User message means Claude will start working on a response
                if self.claude_status != "waiting_for_permission":
                    self.claude_status = "working"
                    self.waiting_message_sent = False

                # Handle both string content and tool result content
                if isinstance(content, str) and content:
                    # Check if we have a pending Claude message waiting to be processed
                    if self.pending_claude_message and not self.pending_question_id:
                        # Cancel the delayed processing
                        if self.pending_claude_message_task:
                            self.pending_claude_message_task.cancel()

                        # Claude was waiting for input, create a question now
                        await self.log_to_omnara(
                            self.pending_claude_message,
                            needs_response=True,
                        )
                        self.pending_claude_message = None

                    # Now handle the user's response
                    if self.pending_question_id and self.omnara_client:
                        try:
                            self.debug_log(
                                f"Answering question {self.pending_question_id} with: {content[:50]}..."
                            )
                            await self.omnara_client.answer_question(
                                question_id=self.pending_question_id, answer=content
                            )
                            self.pending_question_id = None  # Clear after answering
                        except Exception as e:
                            self.debug_log(f"Error answering question: {e}")
                            pass
                    else:
                        # No pending question, send as user feedback
                        if self.agent_instance_id and self.omnara_client:
                            try:
                                await self.omnara_client.add_user_feedback(
                                    agent_instance_id=self.agent_instance_id,
                                    feedback=f"User: {content}",
                                )
                            except Exception:
                                pass
                elif isinstance(content, list):
                    # This is a tool result
                    for item in content:
                        if isinstance(item, dict) and item.get("type") == "tool_result":
                            tool_id = item.get("tool_use_id", "")

                            # Check if this was a pending tool
                            if tool_id in self.pending_tools:
                                self.pending_tools.pop(tool_id)

                # Check if this is a tool result based on the content
                # Tool results have content that's a list with tool_result items
                is_tool_result = False
                if isinstance(content, list):
                    for item in content:
                        if isinstance(item, dict) and item.get("type") == "tool_result":
                            is_tool_result = True
                            break

                if is_tool_result:
                    self.last_entry_was_tool_result = True
                    # Clear permission state when tool completes
                    self.permission_active = False
                    self.permission_question = None
                    # Cancel any existing task checking for tool response
                    if self.check_tool_task and not self.check_tool_task.done():
                        self.check_tool_task.cancel()
                    # Start a new timer to check if Claude responds
                    self.check_tool_task = asyncio.create_task(
                        self.check_for_waiting_after_tool()
                    )

            elif msg_type == "assistant":
                # Claude is responding, so clear the tool result flag
                self.last_entry_was_tool_result = False
                # Cancel any pending check for empty question
                if self.check_tool_task and not self.check_tool_task.done():
                    self.check_tool_task.cancel()

                # Extract assistant message
                message = data.get("message", {})
                # message_id = message.get("id", "")
                content_blocks = message.get("content", [])
                text_parts = []
                tool_parts = []

                self.debug_log(
                    f"Assistant message with {len(content_blocks)} content blocks"
                )

                for block in content_blocks:
                    if isinstance(block, dict):
                        block_type = block.get("type")
                        if block_type == "text":
                            text_content = block.get("text", "")
                            self.debug_log(f"  - Text block: {repr(text_content)}")
                            text_parts.append(text_content)
                        elif block_type == "tool_use":
                            # Format tool use simply
                            tool_name = block.get("name", "unknown")
                            tool_id = block.get("id", "")
                            input_data = block.get("input", {})

                            # Store ALL tool uses as potentially pending
                            self.pending_tools[tool_id] = {
                                "name": tool_name,
                                "input": input_data,
                                "timestamp": time.time(),
                                "shown_prompt": False,
                                "processed": False,  # Track if we've already processed this
                            }

                            # Format tool use message based on tool type
                            if tool_name in ["Write", "Edit", "MultiEdit"]:
                                file_path = input_data.get("file_path", "unknown")
                                tool_parts.append(
                                    f"Using tool: {tool_name} - {file_path}"
                                )
                            elif tool_name == "LS":
                                path = input_data.get("path", ".")
                                tool_parts.append(f"Using tool: {tool_name} - {path}")
                            elif tool_name == "Read":
                                file_path = input_data.get("file_path", "unknown")
                                tool_parts.append(
                                    f"Using tool: {tool_name} - {file_path}"
                                )
                            elif tool_name == "Bash":
                                command = input_data.get("command", "")
                                if len(command) > 50:
                                    command = command[:50] + "..."
                                tool_parts.append(
                                    f"Using tool: {tool_name} - {command}"
                                )
                            elif tool_name == "Grep":
                                pattern = input_data.get("pattern", "")
                                path = input_data.get("path", ".")
                                tool_parts.append(
                                    f"Using tool: {tool_name} - '{pattern}' in {path}"
                                )
                            else:
                                tool_parts.append(f"Using tool: {tool_name}")

                            # Mark this tool as potentially needing permission
                            self.pending_tools[tool_id]["needs_permission_check"] = True
                        elif block_type == "thinking":
                            # Show thinking content
                            thinking_text = block.get("text", "")
                            if thinking_text:
                                text_parts.append(f"[Thinking: {thinking_text}]")
                        else:
                            # Handle unknown block types
                            text_parts.append(f"[{block_type} block]")
                    elif isinstance(block, str):
                        # Sometimes content might be a simple string
                        text_parts.append(block)

                # Combine all parts
                all_parts = text_parts + tool_parts

                # Only process if we have content
                if all_parts:
                    message_content = "\n".join(all_parts)
                    self.debug_log(
                        f"Combined message to process after delay: {repr(message_content)}"
                    )

                    # If we have a pending message that hasn't been processed yet, process it immediately
                    # BUT NOT if we're waiting for a permission prompt to render!
                    if (
                        self.pending_claude_message
                        and self.pending_claude_message_task
                        and not self.permission_active
                    ):
                        self.debug_log(
                            f"Processing previous pending message immediately: {repr(self.pending_claude_message)}"
                        )
                        self.pending_claude_message_task.cancel()
                        # Log the previous message immediately as a step since Claude is still working
                        await self.log_to_omnara(self.pending_claude_message)
                        self.pending_claude_message = None

                    # Store the new message and start a task to process it after delay
                    # Only if it's different from what we just processed
                    if message_content != self.pending_claude_message:
                        self.pending_claude_message = message_content
                        self.pending_claude_message_task = asyncio.create_task(
                            self._process_claude_message_after_delay(message_content)
                        )

            elif msg_type == "summary":
                # Session started - just track the session ID, don't log
                summary = data.get("summary", "")
                if summary:
                    self.current_session_id = data.get("leafUuid")

            elif msg_type == "thinking":
                # Handle thinking messages
                message = data.get("message", {})
                content = message.get("content", "")
                if content:
                    await self.log_to_omnara(f"[Claude thinking: {content}]")

        except Exception:
            # Error processing log entry
            pass

    async def _process_claude_message_after_delay(
        self, message_content: str, delay: float = 3.0
    ):
        """Process a Claude message after waiting to see if Claude is still working."""
        try:
            self.debug_log(
                f"_process_claude_message_after_delay called with delay={delay}, message={repr(message_content[:50])}..."
            )
            # Check if this message is a tool use that might need permission
            tool_use_match = re.match(r"^Using tool: (\w+)", message_content)
            might_need_permission = False

            if tool_use_match:
                tool_name = tool_use_match.group(1)
                # Check if we have pending tools that need permission check
                for tool_id, tool_info in self.pending_tools.items():
                    if tool_info["name"] == tool_name and not tool_info.get(
                        "processed", False
                    ):
                        if tool_info.get("needs_permission_check"):
                            might_need_permission = True
                        # Mark this tool as processed so we don't handle it again
                        tool_info["processed"] = True
                        break

                # Also check if we already have a stored permission question
                if self.permission_question:
                    might_need_permission = True
                    self.debug_log(
                        f"Found stored permission question for tool use: {tool_name}"
                    )

            # Wait for the specified delay
            if delay > 0:
                if (
                    might_need_permission and delay == 3.0
                ):  # Use default longer delay for permission
                    await asyncio.sleep(
                        4.0
                    )  # Give more time for permission prompt to appear
                else:
                    await asyncio.sleep(delay)

            # Only process if this message is still pending
            if self.pending_claude_message != message_content:
                return

            # Check if Claude is still working (has "esc to interrupt)" indicator)
            # OR if there's a permission prompt active
            is_working = self.is_claude_working()
            self.debug_log(
                f"After {delay}s delay: is_claude_working={is_working}, permission_active={self.permission_active}, has_permission_question={bool(self.permission_question)}, might_need_permission={might_need_permission}"
            )

            # If this is a tool use message and we have a permission question stored, use it
            if might_need_permission and self.permission_question:
                # Send the permission question with OPTIONS instead of just the tool message
                self.debug_log(
                    f"Sending permission question for tool use: {repr(self.permission_question[:100])}..."
                )
                await self.log_to_omnara(self.permission_question, needs_response=True)
                self.permission_question = None  # Clear after sending
                # Don't clear permission_active here - keep it true until the user responds
                # Don't clear permission_active here - wait for the response to be processed
                # Clear the pending message since we sent the permission question instead
                self.pending_claude_message = None
                return
            elif is_working:
                # Claude is still working
                self.debug_log(f"Logging as STEP: {repr(message_content)}")
                await self.log_to_omnara(message_content)
            else:
                # Claude is NOT working, so it's waiting for input
                # But if this is a tool use message and permission is active, skip sending it
                # The permission question will be sent instead after extraction
                if might_need_permission and self.permission_active:
                    self.debug_log(
                        "Skipping tool message, waiting for permission question extraction"
                    )
                else:
                    self.debug_log(f"Logging as QUESTION: {repr(message_content)}")
                    await self.log_to_omnara(
                        message_content,
                        needs_response=True,
                    )

            # Clear the pending message (but not if we're waiting for permission extraction)
            if not self.permission_active:
                self.pending_claude_message = None
        except asyncio.CancelledError:
            pass

    async def check_for_waiting_after_tool(self):
        """Check if Claude needs input after a tool result."""
        try:
            # Keep checking until Claude stops working
            while self.is_claude_working() and self.last_entry_was_tool_result:
                await asyncio.sleep(0.5)

            # Wait 3 seconds to ensure Claude is truly done
            await asyncio.sleep(3.0)

            # Claude has stopped working, check if we still need to add the message
            if self.last_entry_was_tool_result:
                # Claude needs input - ask via Omnara
                await self.log_to_omnara(
                    "Waiting for your response...",
                    needs_response=True,
                )
                self.last_entry_was_tool_result = False
        except asyncio.CancelledError:
            # Task was cancelled, this is expected
            pass

    def is_claude_working(self):
        """Check various indicators to determine if Claude is actively working."""
        # Check 1: "esc to interrupt)" indicator
        if self.last_esc_interrupt_seen:
            time_since_esc = time.time() - self.last_esc_interrupt_seen
            if time_since_esc < 3.0:
                return True
        return False

    def monitor_claude_status(self):
        """Monitor Claude's status using various indicators."""
        while self.running:
            time.sleep(0.5)  # Check every 0.5 seconds

            # Only check if we have a session and Claude is not waiting for permission
            if self.log_file_path and self.claude_status != "waiting_for_permission":
                if self.is_claude_working():
                    # Claude is working
                    self.claude_status = "working"
                    self.waiting_message_sent = False
                else:
                    # Claude appears to be idle
                    if not self.waiting_message_sent and self.last_esc_interrupt_seen:
                        self.claude_status = "waiting_for_input"
                        self.waiting_message_sent = True

    def run_claude_with_pty(self):
        """Run Claude CLI in a PTY."""
        claude_path = self.find_claude_cli()

        # Build command - normal mode to preserve terminal UI
        # Try to use --session-id if available (newer Claude versions)
        cmd = [claude_path, "--session-id", self.session_uuid]
        self.debug_log(f"Using session UUID: {self.session_uuid}")

        # Process command line arguments, filtering out -c/--continue and -r/--resume
        if len(sys.argv) > 1:
            i = 1
            while i < len(sys.argv):
                arg = sys.argv[i]
                # Skip continue and resume flags to ensure fresh start
                if arg in ["-c", "--continue"]:
                    i += 1
                elif arg in ["-r", "--resume"]:
                    # Skip the flag and its optional argument
                    i += 1
                    if i < len(sys.argv) and not sys.argv[i].startswith("-"):
                        i += 1
                else:
                    cmd.append(arg)
                    i += 1

        # Starting Claude
        self.debug_log(f"Starting Claude with command: {' '.join(cmd)}")

        # Save original terminal settings if in a terminal
        try:
            self.original_tty_attrs = termios.tcgetattr(sys.stdin)
        except Exception:
            self.original_tty_attrs = None

        # Get terminal size before forking
        try:
            # Get the current terminal size
            cols, rows = os.get_terminal_size()
        except Exception:
            # Default to standard size if unable to get
            cols, rows = 80, 24

        # Create PTY
        self.child_pid, self.master_fd = pty.fork()

        if self.child_pid == 0:
            # Child process - exec Claude CLI
            os.environ["CLAUDE_CODE_ENTRYPOINT"] = "jsonlog-wrapper"
            # Clear any session environment variables that might affect conversation state
            os.environ.pop("CLAUDE_SESSION_ID", None)
            os.environ.pop("CLAUDE_CONVERSATION_ID", None)
            os.execvp(cmd[0], cmd)

        # Parent process - set PTY size to match terminal
        if self.child_pid > 0:
            try:
                # Set the PTY size to match the parent terminal
                import fcntl
                import struct

                # TIOCSWINSZ is the ioctl to set window size
                TIOCSWINSZ = 0x5414  # Linux value, may differ on macOS
                if sys.platform == "darwin":
                    TIOCSWINSZ = 0x80087467  # macOS value

                winsize = struct.pack("HHHH", rows, cols, 0, 0)
                fcntl.ioctl(self.master_fd, TIOCSWINSZ, winsize)
            except Exception:
                pass

        # Parent process - handle I/O
        try:
            # Set stdin to raw mode if in a terminal
            if self.original_tty_attrs:
                tty.setraw(sys.stdin)

            # Set non-blocking mode on master_fd for better performance
            import fcntl

            flags = fcntl.fcntl(self.master_fd, fcntl.F_GETFL)
            fcntl.fcntl(self.master_fd, fcntl.F_SETFL, flags | os.O_NONBLOCK)

            # Buffer for more efficient writing

            while self.running:
                # Use select to multiplex I/O
                rlist, _, _ = select.select([sys.stdin, self.master_fd], [], [], 0.01)

                # Handle terminal output from Claude
                if self.master_fd in rlist:
                    try:
                        # Read more data at once to reduce syscalls
                        data = os.read(self.master_fd, 65536)
                        if data:
                            # Write immediately for better responsiveness
                            os.write(sys.stdout.fileno(), data)
                            sys.stdout.flush()

                            # Check for indicators in terminal output
                            try:
                                text = data.decode("utf-8", errors="ignore")
                                self.terminal_buffer += text

                                # Track terminal activity
                                if (
                                    self.terminal_buffer
                                    != self.terminal_activity_buffer
                                ):
                                    diff_len = abs(
                                        len(self.terminal_buffer)
                                        - len(self.terminal_activity_buffer)
                                    )
                                    if diff_len > 5:
                                        self.last_terminal_activity = time.time()
                                    self.terminal_activity_buffer = self.terminal_buffer

                                # Check for "esc to interrupt)" indicator
                                clean_text = re.sub(r"\x1b\[[0-9;]*m", "", text)
                                if "esc to interrupt)" in clean_text:
                                    self.last_esc_interrupt_seen = time.time()
                                    self.claude_status = "working"
                                    self.waiting_message_sent = False
                                    self.debug_log(
                                        "Detected 'esc to interrupt)' indicator - Claude is working"
                                    )

                                # Keep more terminal buffer to capture full permission prompts
                                if len(self.terminal_buffer) > 20000:
                                    self.terminal_buffer = self.terminal_buffer[-20000:]
                                    self.terminal_activity_buffer = self.terminal_buffer

                                # Check for permission prompt patterns (but only if not already handling one)
                                # Also don't check too soon after responding to avoid re-detection
                                can_check_permission = not self.permission_active
                                if self.permission_response_time:
                                    time_since_response = (
                                        time.time() - self.permission_response_time
                                    )
                                    # Wait at least 2 seconds after responding before checking again
                                    if time_since_response < 2.0:
                                        can_check_permission = False

                                # Look for numbered options with keyboard shortcuts
                                # Must have BOTH "Do you want to" AND numbered options
                                has_question = (
                                    "Do you want to" in self.terminal_buffer
                                    and can_check_permission
                                )
                                # Check if we have the prompt structure with options
                                # Look for "1." followed by any amount of whitespace and "Yes"
                                clean_buffer = re.sub(
                                    r"\s+", " ", self.terminal_buffer
                                )  # Normalize whitespace
                                has_options = has_question and (
                                    "❯" in self.terminal_buffer
                                    or "1. Yes" in clean_buffer
                                    or re.search(r"1\.\s*Yes", self.terminal_buffer)
                                )

                                # Debug what we're seeing
                                if has_question:
                                    # Always log what we're seeing after the question
                                    buffer_after_q = self.terminal_buffer.split(
                                        "Do you want to"
                                    )[-1][:500]
                                    clean_after_q = re.sub(
                                        r"\x1b\[[0-9;]*[a-zA-Z]", "", buffer_after_q
                                    )
                                    self.debug_log(
                                        f"Buffer after 'Do you want to': {repr(clean_after_q)}"
                                    )

                                    # Check for specific indicators
                                    has_arrow = "❯" in self.terminal_buffer
                                    has_opt1 = "1. Yes" in self.terminal_buffer
                                    self.debug_log(
                                        f"has_arrow={has_arrow}, has_opt1={has_opt1}, has_options={has_options}"
                                    )

                                    if not has_options:
                                        self.debug_log(
                                            "Found question but no options detected yet"
                                        )
                                    else:
                                        # Log when we first see both question and options
                                        if not hasattr(
                                            self, "_logged_permission_detection"
                                        ):
                                            self.debug_log(
                                                "Found both question and option 1 in terminal buffer!"
                                            )
                                            self._logged_permission_detection = True

                                if has_question and has_options:
                                    if not self.permission_active:
                                        self.permission_active = True
                                        self.claude_status = "waiting_for_permission"
                                        self.debug_log(
                                            "Permission prompt detected in terminal with options"
                                        )
                                        # Store the time we first detected the prompt
                                        self.permission_prompt_time = time.time()
                                        self.debug_log(
                                            "Setting permission prompt detection time"
                                        )

                                # Check if we should extract options now
                                if self.permission_active and hasattr(
                                    self, "permission_prompt_time"
                                ):
                                    time_since_detection = (
                                        time.time() - self.permission_prompt_time
                                    )
                                    if time_since_detection >= 0.15 and not hasattr(
                                        self, "_permission_extracted"
                                    ):
                                        # Mark as extracted so we don't do it again
                                        self._permission_extracted = True

                                        # Now extract after waiting for prompt to render
                                        self.debug_log(
                                            f"Processing permission prompt after {time_since_detection:.3f}s wait"
                                        )

                                        # Log the full buffer to debug
                                        clean_full_buffer = re.sub(
                                            r"\x1b\[[0-9;]*[a-zA-Z]",
                                            "",
                                            self.terminal_buffer,
                                        )
                                        self.debug_log(
                                            f"Full terminal buffer at extraction time: {repr(clean_full_buffer[-2000:])}"
                                        )

                                        # Extract the question and options from terminal
                                        lines = self.terminal_buffer.split("\n")
                                        question = None
                                        options = []

                                        # Find the question
                                        for i, line in enumerate(lines):
                                            if "Do you want to" in line:
                                                # Strip ANSI codes and clean up
                                                clean_line = re.sub(
                                                    r"\x1b\[[0-9;]*[a-zA-Z]",
                                                    "",
                                                    line,
                                                )
                                                question = (
                                                    clean_line.strip()
                                                    .replace("│", "")
                                                    .strip()
                                                )

                                                # Look for options in the following lines (check more lines)
                                                self.debug_log(
                                                    f"Looking for options starting at line {i + 1}, total lines: {len(lines)}"
                                                )
                                                # Log a few lines after the question to see what's there
                                                for debug_idx in range(
                                                    i + 1, min(i + 5, len(lines))
                                                ):
                                                    debug_line = lines[debug_idx]
                                                    clean_debug = re.sub(
                                                        r"\x1b\[[0-9;]*[a-zA-Z]",
                                                        "",
                                                        debug_line,
                                                    ).strip()
                                                    self.debug_log(
                                                        f"  Line {debug_idx}: {repr(clean_debug[:100])}..."
                                                    )

                                                # Instead of line-by-line, search the entire buffer after the question
                                                # This handles cases where options might be split across lines
                                                remaining_buffer = "\n".join(
                                                    lines[i + 1 :]
                                                )
                                                # Clean ANSI codes from remaining buffer for matching
                                                re.sub(
                                                    r"\x1b\[[0-9;]*[a-zA-Z]",
                                                    "",
                                                    remaining_buffer,
                                                )

                                                # Look in the FULL buffer, not just after the question
                                                # because options might appear on the same line or before
                                                full_clean = re.sub(
                                                    r"\x1b\[[0-9;]*[a-zA-Z]",
                                                    "",
                                                    self.terminal_buffer,
                                                )

                                                # Look for numbered options more flexibly
                                                # Match any line that starts with a number followed by period
                                                option_matches = re.findall(
                                                    r"(\d+)\.\s*([^\n│]+)", full_clean
                                                )

                                                for num, text in option_matches:
                                                    option_text = (
                                                        f"{num}. {text.strip()}"
                                                    )
                                                    if num == "1" and "Yes" in text:
                                                        options.append("1. Yes")
                                                        self.debug_log("Found option 1")
                                                    elif num == "2":
                                                        # Option 2 can vary - just use the full text
                                                        if "Yes" in text:
                                                            options.append(option_text)
                                                            self.debug_log(
                                                                f"Found option 2: {option_text}"
                                                            )
                                                    elif num == "3" and "No" in text:
                                                        options.append(
                                                            "3. No, and tell Claude what to do differently"
                                                        )
                                                        self.debug_log("Found option 3")

                                                self.debug_log(
                                                    f"Total options found: {len(options)}"
                                                )
                                                self.debug_log(
                                                    f"Debug - looking for options in buffer segment: {repr(full_clean[-1000:])}"
                                                )

                                            # Store the permission question to be sent later with the tool message
                                            if (
                                                question
                                                and not self.permission_question
                                            ):  # Only store if we don't already have one
                                                # Find the most recent tool for context
                                                recent_tool = None
                                                if self.pending_tools:
                                                    sorted_tools = sorted(
                                                        self.pending_tools.items(),
                                                        key=lambda x: x[1]["timestamp"],
                                                        reverse=True,
                                                    )
                                                    if sorted_tools:
                                                        recent_tool = sorted_tools[0][1]

                                                # Build the permission question with tool details
                                                permission_msg = question
                                                if recent_tool:
                                                    tool_name = recent_tool["name"]
                                                    tool_input = recent_tool["input"]

                                                    permission_msg += (
                                                        f"\n\nTool: {tool_name}"
                                                    )
                                                    if tool_name in [
                                                        "Write",
                                                        "Edit",
                                                        "MultiEdit",
                                                    ]:
                                                        file_path = tool_input.get(
                                                            "file_path", "unknown"
                                                        )
                                                        permission_msg += (
                                                            f"\nFile: {file_path}"
                                                        )
                                                    elif tool_name == "Bash":
                                                        command = tool_input.get(
                                                            "command", ""
                                                        )
                                                        permission_msg += (
                                                            f"\nCommand: {command}"
                                                        )
                                                    else:
                                                        # Include basic info for any other tool
                                                        permission_msg += (
                                                            f"\nDetails: {tool_input}"
                                                        )

                                                # Format with OPTIONS tag for UI
                                                permission_msg += "\n\n[OPTIONS]\n"

                                                # Use the actual options from the terminal if we found them
                                                if options:
                                                    for option in options:
                                                        permission_msg += f"{option}\n"
                                                else:
                                                    # Fallback to default options
                                                    permission_msg += "1. Yes\n"
                                                    permission_msg += "2. Yes, and don't ask again this session\n"
                                                    permission_msg += "3. No, and tell Claude what to do differently\n"

                                                permission_msg += "[/OPTIONS]"

                                                # Store the permission question to send later
                                                self.permission_question = (
                                                    permission_msg
                                                )
                                                self.debug_log(
                                                    f"Stored permission question: {repr(permission_msg[:100])}..."
                                                )

                                                # Re-process the pending message now that we have the permission question
                                                self.debug_log(
                                                    f"Checking for re-processing: pending_msg={repr(self.pending_claude_message[:50]) if self.pending_claude_message else None}, has_task={bool(self.pending_claude_message_task)}"
                                                )
                                                if (
                                                    self.pending_claude_message
                                                    and self.pending_claude_message_task
                                                ):
                                                    self.debug_log(
                                                        "Re-processing message with permission question"
                                                    )
                                                    # Cancel the current task and create a new one to process immediately
                                                    try:
                                                        self.pending_claude_message_task.cancel()
                                                    except Exception:
                                                        pass
                                                    # Schedule the task in the async loop
                                                    self.pending_claude_message_task = asyncio.run_coroutine_threadsafe(
                                                        self._process_claude_message_after_delay(
                                                            self.pending_claude_message,
                                                            delay=0,
                                                        ),
                                                        self.async_loop,
                                                    )
                                                else:
                                                    # If no pending message, we need to send the permission question directly
                                                    self.debug_log(
                                                        "No pending message to re-process, sending permission question directly"
                                                    )

                                                    async def send_permission():
                                                        if self.permission_question:
                                                            await self.log_to_omnara(
                                                                self.permission_question,
                                                                needs_response=True,
                                                            )
                                                            self.permission_question = (
                                                                None
                                                            )
                                                        # Keep permission_active true until user responds

                                                    asyncio.run_coroutine_threadsafe(
                                                        send_permission(),
                                                        self.async_loop,
                                                    )

                                                # Clear the prompt detection time and flags
                                                if hasattr(
                                                    self, "permission_prompt_time"
                                                ):
                                                    delattr(
                                                        self, "permission_prompt_time"
                                                    )
                                                if hasattr(
                                                    self, "_logged_permission_detection"
                                                ):
                                                    delattr(
                                                        self,
                                                        "_logged_permission_detection",
                                                    )
                                                if hasattr(
                                                    self, "_permission_extracted"
                                                ):
                                                    delattr(
                                                        self,
                                                        "_permission_extracted",
                                                    )
                                else:
                                    # Don't reset permission state too quickly - wait for it to be used
                                    pass

                            except Exception:
                                pass
                        else:
                            break
                    except BlockingIOError:
                        # No data available, continue
                        pass
                    except OSError:
                        break

                # Handle user input
                if sys.stdin in rlist and self.original_tty_attrs:
                    try:
                        # Read user input
                        data = os.read(sys.stdin.fileno(), 4096)
                        if data:
                            # Forward directly to Claude
                            os.write(self.master_fd, data)
                    except OSError:
                        pass

                # Process Omnara responses with less delay
                if self.input_queue:
                    content = self.input_queue.popleft()
                    self.debug_log(f"Processing Omnara response: {repr(content)}")

                    # If this is a permission response, convert to the right number
                    if self.permission_active:
                        content_lower = content.lower().strip()
                        self.debug_log(
                            f"Permission active, converting response: {repr(content_lower)}"
                        )
                        # Map various forms of "yes" to "1"
                        if content_lower in ["yes", "1", "1. yes"]:
                            content = "1"
                        # Map "yes always" variations to "2"
                        elif any(
                            phrase in content_lower
                            for phrase in [
                                "yes always",
                                "yes, and don't ask again",
                                "2. yes",
                            ]
                        ):
                            content = "2"
                        # Map "no" variations to "3"
                        elif any(
                            phrase in content_lower
                            for phrase in ["no", "3. no", "tell claude"]
                        ):
                            content = "3"
                        # For any other response, treat as "No"
                        else:
                            content = "3"
                        self.debug_log(f"Mapped response to: {repr(content)}")
                        # Clear permission state since we're responding to it
                        self.permission_active = False
                        self.permission_response_time = time.time()
                    else:
                        # This is a new user message, not a permission response
                        # Clear any stored permission question from previous interactions
                        if self.permission_question:
                            self.debug_log(
                                "Clearing stored permission question due to new user message"
                            )
                            self.permission_question = None

                    # Send the response immediately
                    os.write(self.master_fd, content.encode())
                    # Small delay to ensure content is processed before sending Enter
                    time.sleep(0.05)
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

    async def run(self):
        """Run Claude with Omnara integration."""
        # Starting Claude with Omnara integration

        # Initialize Omnara client
        await self.init_omnara_client()

        # Store the async loop for use in threads
        self.async_loop = asyncio.get_event_loop()

        # Create initial log step with our session UUID immediately
        session_timestamp = time.strftime("%Y-%m-%d %H:%M:%S")
        self.debug_log(
            f"Creating initial log step with session UUID: {self.session_uuid}"
        )
        await self.log_to_omnara(
            f"[Session {self.session_uuid} started at {session_timestamp}]"
        )

        # Create initial empty question so the dashboard can send input
        await self.log_to_omnara("", needs_response=True)

        # Record existing log files before starting Claude
        project_dir = self.get_project_log_dir()
        self.existing_logs = set()
        if project_dir and project_dir.exists():
            self.existing_logs = set(project_dir.glob("*.jsonl"))
            self.debug_log(
                f"Found {len(self.existing_logs)} existing log files before starting Claude"
            )

        # Start Claude in PTY (in thread) FIRST
        claude_thread = threading.Thread(target=self.run_claude_with_pty)
        claude_thread.daemon = True
        claude_thread.start()

        # Wait a moment for Claude to create its log file
        await asyncio.sleep(1.0)  # Give Claude more time to start

        # Now start log monitor thread
        self.log_monitor_thread = threading.Thread(target=self.monitor_log_file)
        self.log_monitor_thread.daemon = True
        self.log_monitor_thread.start()

        # Keep the async loop running
        try:
            while self.running:
                await asyncio.sleep(1)
        except KeyboardInterrupt:
            pass
        finally:
            # Clean up
            if self.omnara_client:
                if self.agent_instance_id:
                    try:
                        await self.omnara_client.end_session(self.agent_instance_id)
                    except Exception:
                        # Error ending session
                        pass
                await self.omnara_client.close()


def main():
    """Main entry point."""
    parser = argparse.ArgumentParser(
        description="Claude wrapper for Omnara integration",
        add_help=False,  # Disable help to pass through to Claude
    )
    parser.add_argument("--api-key", help="Omnara API key")
    parser.add_argument("--base-url", help="Omnara base URL")

    # Parse known args and pass the rest to Claude
    args, claude_args = parser.parse_known_args()

    # Update sys.argv to only include Claude args
    sys.argv = [sys.argv[0]] + claude_args

    wrapper = ClaudeJSONLogWrapper(api_key=args.api_key, base_url=args.base_url)

    def signal_handler(sig, frame):
        # Shutting down
        wrapper.running = False
        if wrapper.original_tty_attrs:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, wrapper.original_tty_attrs)
        sys.exit(0)

    def handle_resize(sig, frame):
        """Handle terminal resize signal."""
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
    signal.signal(signal.SIGWINCH, handle_resize)  # Handle terminal resize

    try:
        asyncio.run(wrapper.run())
    except KeyboardInterrupt:
        signal_handler(None, None)
    except Exception:
        # Fatal error occurred
        pass
        if wrapper.original_tty_attrs:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, wrapper.original_tty_attrs)
        sys.exit(1)


if __name__ == "__main__":
    main()
