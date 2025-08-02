#!/usr/bin/env python3
"""
Claude Wrapper V2 - Simplified wrapper for streaming Claude to Omnara web UI

Usage:
    # Run with environment variable
    OMNARA_API_KEY=your_api_key ./claude_wrapper_v2.py

    # Or with command line argument
    ./claude_wrapper_v2.py --api-key your_api_key

    # Pass additional arguments to Claude
    ./claude_wrapper_v2.py --api-key your_api_key "Help me debug this code"

    # Use custom Omnara base URL
    ./claude_wrapper_v2.py --api-key your_api_key --base-url http://localhost:8080
"""

import argparse
import asyncio
import json
import os
import pty
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

# Constants
CLAUDE_LOG_BASE = Path.home() / ".claude" / "projects"


class ClaudeWrapperV2:
    def __init__(self, api_key: Optional[str] = None, base_url: Optional[str] = None):
        # Session management
        self.session_uuid = str(uuid.uuid4())
        self.agent_instance_id = None

        # Set up logging to file
        self.log_file = None
        self._init_logging()

        self.log(f"[INFO] Session UUID: {self.session_uuid}")

        # Omnara SDK setup
        self.api_key = api_key or os.environ.get("OMNARA_API_KEY")
        if not self.api_key:
            # Still print this to stderr since it's a fatal error before logging is set up
            print(
                "ERROR: API key must be provided via --api-key or OMNARA_API_KEY environment variable",
                file=sys.stderr,
            )
            sys.exit(1)
        self.log("[INFO] API key configured")

        self.base_url = base_url or os.environ.get(
            "OMNARA_BASE_URL", "https://agent-dashboard-mcp.onrender.com"
        )
        self.omnara_client: Optional[AsyncOmnaraClient] = None

        # Terminal interaction setup
        self.child_pid = None
        self.master_fd = None
        self.original_tty_attrs = None
        self.input_queue = deque()

        # Log monitoring
        self.log_file_path = None
        self.log_monitor_thread = None
        self.running = True

        # Claude status monitoring
        self.terminal_buffer = ""
        self.last_esc_interrupt_seen = None
        self.last_message_id = None
        self.last_message_time = None
        self.claude_idle_task = None
        self.terminal_monitor_thread = None
        self.web_ui_messages = (
            set()
        )  # Track messages that came from web UI to avoid duplicates

        # Permission handling
        self.permission_active = False
        self.permission_prompt_time = None
        self.permission_question = None
        self.permission_extracted = False
        self.permission_message_id = None
        self.need_followup_prompt = False

        # Async event loop reference
        self.async_loop = None

    def _init_logging(self):
        """Initialize file-based logging."""
        try:
            # Always use the same log file, overwrite each run
            log_path = Path.home() / ".claude" / "wrapper_debug.log"
            log_path.parent.mkdir(exist_ok=True, parents=True)
            self.log_file = open(log_path, "w")
            self.log(
                f"=== Claude Wrapper V2 Debug Log - {time.strftime('%Y-%m-%d %H:%M:%S')} ==="
            )
        except Exception as e:
            # If we can't create log file, log to stderr as last resort
            print(f"Failed to create log file: {e}", file=sys.stderr)

    def log(self, message: str):
        """Write to log file."""
        if self.log_file:
            try:
                self.log_file.write(f"[{time.strftime('%H:%M:%S')}] {message}\n")
                self.log_file.flush()
            except Exception:
                pass

    async def init_omnara_client(self):
        """Initialize the Omnara SDK client."""
        self.omnara_client = AsyncOmnaraClient(
            api_key=self.api_key, base_url=self.base_url
        )
        await self.omnara_client._ensure_session()

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

    def monitor_log_file(self):
        """Monitor the JSON log file for new messages."""
        # Wait for our specific session log file to be created
        expected_filename = f"{self.session_uuid}.jsonl"

        while self.running and not self.log_file_path:
            project_dir = self.get_project_log_dir()
            if project_dir:
                expected_path = project_dir / expected_filename
                if expected_path.exists():
                    self.log_file_path = expected_path
                    self.log(f"[INFO] Found log file: {expected_path}")
                    break
            time.sleep(0.5)

        if not self.log_file_path:
            return

        # Monitor the file
        try:
            with open(self.log_file_path, "r") as f:
                # Start from beginning to catch all messages
                f.seek(0)

                while self.running:
                    line = f.readline()
                    if line:
                        try:
                            line = line.strip()
                            if not line:
                                continue

                            data = json.loads(line)

                            # Process in async context
                            asyncio.run_coroutine_threadsafe(
                                self.process_log_entry(data), self.async_loop
                            )
                        except json.JSONDecodeError:
                            pass
                        except Exception as e:
                            self.log(f"Error processing log entry: {e}")
                    else:
                        # Check if file still exists
                        if not self.log_file_path.exists():
                            break
                        time.sleep(0.1)

        except Exception as e:
            self.log(f"Error monitoring log file: {e}")

    async def process_log_entry(self, data: Dict[str, Any]):
        """Process a log entry and send to Omnara."""
        try:
            msg_type = data.get("type")

            if msg_type == "user":
                # User message
                message = data.get("message", {})
                content = message.get("content", "")

                if isinstance(content, str) and content:
                    self.log(f"[INFO] User message in log: {content[:50]}...")

                    # Check if this message came from web UI (via request_user_input)
                    if content in self.web_ui_messages:
                        self.log(
                            "[INFO] Skipping send - message from web UI (already in DB)"
                        )
                        self.web_ui_messages.discard(content)  # Remove to free memory
                    elif self.agent_instance_id and self.omnara_client:
                        # This message came from CLI - send it to Omnara
                        self.log(
                            f"[INFO] Sending CLI message to Omnara: {content[:50]}..."
                        )
                        await self.omnara_client.send_user_message(
                            agent_instance_id=self.agent_instance_id,
                            content=content,
                        )

                    # Reset idle timer since user sent a message
                    self.last_message_time = time.time()

            elif msg_type == "assistant":
                # Claude's response
                message = data.get("message", {})
                content_blocks = message.get("content", [])
                text_parts = []
                tool_parts = []

                for block in content_blocks:
                    if isinstance(block, dict):
                        block_type = block.get("type")
                        if block_type == "text":
                            text_content = block.get("text", "")
                            text_parts.append(text_content)
                        elif block_type == "tool_use":
                            # Format tool use
                            tool_name = block.get("name", "unknown")
                            input_data = block.get("input", {})

                            # Simple tool formatting
                            if tool_name in ["Write", "Edit", "MultiEdit"]:
                                file_path = input_data.get("file_path", "unknown")
                                tool_parts.append(
                                    f"Using tool: {tool_name} - {file_path}"
                                )
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
                            else:
                                tool_parts.append(f"Using tool: {tool_name}")
                        elif block_type == "thinking":
                            # Include thinking content
                            thinking_text = block.get("text", "")
                            if thinking_text:
                                text_parts.append(f"[Thinking: {thinking_text}]")

                # Combine all parts
                all_parts = text_parts + tool_parts

                if all_parts:
                    message_content = "\n".join(all_parts)

                    # Always send as agent message WITHOUT requiring user input
                    response = await self.omnara_client.send_message(
                        content=message_content,
                        agent_type="Claude Code",
                        agent_instance_id=self.agent_instance_id,
                        requires_user_input=False,  # ALWAYS False
                    )

                    # Store instance ID if this is the first message
                    if not self.agent_instance_id:
                        self.agent_instance_id = response.agent_instance_id

                    # Track the message ID and time
                    self.last_message_id = response.message_id
                    self.last_message_time = time.time()
                    self.log(
                        f"[INFO] Sent message {self.last_message_id}, Claude working: {self.is_claude_working()}"
                    )

                    # Process any queued user messages
                    if response.queued_user_messages:
                        for user_msg in response.queued_user_messages:
                            self.input_queue.append(user_msg)

            elif msg_type == "summary":
                # Session started
                summary = data.get("summary", "")
                if summary and not self.agent_instance_id:
                    # Send initial message
                    response = await self.omnara_client.send_message(
                        content=f"[Claude session started: {summary}]",
                        agent_type="Claude Code",
                        requires_user_input=False,
                    )
                    self.agent_instance_id = response.agent_instance_id

        except Exception as e:
            self.log(f"Error processing log entry: {e}")

    def is_claude_working(self):
        """Check if Claude is actively working based on terminal output."""
        if self.last_esc_interrupt_seen:
            time_since_esc = time.time() - self.last_esc_interrupt_seen
            # Claude is working if we've seen "esc to interrupt" within last 3 seconds
            return time_since_esc < 3.0
        return False

    async def extract_and_send_permission_prompt(self):
        """Extract permission prompt details and send to Omnara."""
        if not self.permission_active or self.permission_extracted:
            return

        self.log("[INFO] Extracting permission prompt from terminal")
        self.permission_extracted = True
        self._last_permission_extraction_time = time.time()

        # Extract question and options from terminal buffer
        import re

        lines = self.terminal_buffer.split("\n")
        question = None
        options = []

        # Find the question
        for i, line in enumerate(lines):
            if "Do you want to" in line:
                # Clean ANSI codes
                clean_line = re.sub(r"\x1b\[[0-9;]*[a-zA-Z]", "", line)
                question = clean_line.strip().replace("│", "").strip()
                break

        # Extract options from the full buffer
        full_clean = re.sub(r"\x1b\[[0-9;]*[a-zA-Z]", "", self.terminal_buffer)
        option_matches = re.findall(r"(\d+)\.\s*([^\n│]+)", full_clean)

        for num, text in option_matches:
            option_text = f"{num}. {text.strip()}"
            if num == "1" and "Yes" in text:
                options.append("1. Yes")
            elif num == "2" and "Yes" in text:
                options.append(option_text)
            elif num == "3" and "No" in text:
                options.append("3. No, and tell Claude what to do differently")

        if question and options:
            # Build the permission message
            permission_msg = question

            # Add tool context from the last assistant message
            # This is simpler than v1 - we just need the basic context
            permission_msg += "\n\n[OPTIONS]\n"
            for option in options:
                permission_msg += f"{option}\n"
            permission_msg += "[/OPTIONS]"

            self.log(f"[INFO] Sending permission prompt: {permission_msg[:100]}...")

            # Send as a message NOT requiring user input initially
            # We'll let the normal idle detection convert it
            if self.agent_instance_id and self.omnara_client:
                response = await self.omnara_client.send_message(
                    content=permission_msg,
                    agent_type="Claude Code",
                    agent_instance_id=self.agent_instance_id,
                    requires_user_input=False,
                )

                # Update last message tracking so idle detection will handle it
                self.last_message_id = response.message_id
                self.last_message_time = time.time()

                # Mark that we're waiting for permission response
                self.permission_message_id = response.message_id

                self.log(
                    "[INFO] Permission message sent, will be converted to require input by idle detection"
                )

    def run_claude_with_pty(self):
        """Run Claude CLI in a PTY."""
        claude_path = self.find_claude_cli()

        # Build command with session ID
        cmd = [claude_path, "--session-id", self.session_uuid]

        # Process any additional command line arguments
        if len(sys.argv) > 1:
            i = 1
            while i < len(sys.argv):
                arg = sys.argv[i]
                # Skip wrapper-specific arguments
                if arg in ["--api-key", "--base-url"]:
                    i += 2  # Skip the argument and its value
                else:
                    cmd.append(arg)
                    i += 1

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
            self.log(f"[INFO] Using default terminal size: {cols}x{rows}")

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
            # Set stdin to raw mode
            if self.original_tty_attrs:
                tty.setraw(sys.stdin)

            # Set non-blocking mode on master_fd
            import fcntl

            flags = fcntl.fcntl(self.master_fd, fcntl.F_GETFL)
            fcntl.fcntl(self.master_fd, fcntl.F_SETFL, flags | os.O_NONBLOCK)

            while self.running:
                # Use select to multiplex I/O
                rlist, _, _ = select.select([sys.stdin, self.master_fd], [], [], 0.01)

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

                                # Keep buffer size reasonable
                                if len(self.terminal_buffer) > 10000:
                                    self.terminal_buffer = self.terminal_buffer[-10000:]

                                # Check for the indicator (handle ANSI codes)
                                import re

                                clean_text = re.sub(r"\x1b\[[0-9;]*m", "", text)
                                if "esc to interrupt)" in clean_text:
                                    self.last_esc_interrupt_seen = time.time()
                                    self.log(
                                        "[INFO] Detected 'esc to interrupt' - Claude is working"
                                    )

                                # Check for permission prompt
                                # Only check if we're not already handling a permission and haven't extracted recently
                                time_since_extracted = float("inf")
                                if hasattr(self, "_last_permission_extraction_time"):
                                    time_since_extracted = (
                                        time.time()
                                        - self._last_permission_extraction_time
                                    )

                                if (
                                    "Do you want to" in self.terminal_buffer
                                    and not self.permission_active
                                    and time_since_extracted > 5.0
                                ):  # Wait at least 5 seconds between detections
                                    # Look for numbered options
                                    clean_buffer = re.sub(
                                        r"\s+", " ", self.terminal_buffer
                                    )
                                    has_options = (
                                        "❯" in self.terminal_buffer
                                        or "1. Yes" in clean_buffer
                                        or re.search(r"1\.\s*Yes", self.terminal_buffer)
                                    )

                                    if has_options:
                                        self.permission_active = True
                                        self.permission_prompt_time = time.time()
                                        self.log("[INFO] Permission prompt detected")
                            except Exception:
                                pass
                        else:
                            break
                    except BlockingIOError:
                        pass
                    except OSError:
                        break

                # Handle user input
                if sys.stdin in rlist and self.original_tty_attrs:
                    try:
                        data = os.read(sys.stdin.fileno(), 4096)
                        if data:
                            # Forward to Claude
                            os.write(self.master_fd, data)
                    except OSError:
                        pass

                # Process Omnara responses
                if self.input_queue:
                    content = self.input_queue.popleft()
                    self.log(f"[INFO] Sending message to Claude: {content[:50]}...")

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

    async def check_claude_idle(self):
        """Monitor Claude's idle state and request user input when needed."""
        self.log("[INFO] Started check_claude_idle task")
        while self.running:
            await asyncio.sleep(0.5)  # Check every 500ms

            # Check if we need to extract permission prompt
            if self.permission_active and self.permission_prompt_time:
                time_since_prompt = time.time() - self.permission_prompt_time
                if time_since_prompt >= 0.2 and not self.permission_extracted:
                    # Extract and send permission prompt
                    await self.extract_and_send_permission_prompt()
                    continue  # Skip normal idle checking during permission

            # Check if we need to send a follow-up prompt after "No" selection
            if self.need_followup_prompt:
                # Check if Claude has been idle for at least 1 second (instead of waiting for full idle)
                time_since_esc = (
                    time.time() - self.last_esc_interrupt_seen
                    if self.last_esc_interrupt_seen
                    else 999
                )
                if time_since_esc > 1.0:  # Reduced from 3.0 seconds
                    # Clear the flag FIRST to prevent multiple sends
                    self.need_followup_prompt = False
                    self.log("[INFO] Sending follow-up prompt after No selection")

                    # Send a message asking what to do next
                    if self.agent_instance_id and self.omnara_client:
                        response = await self.omnara_client.send_message(
                            content="What would you like me to do instead?",
                            agent_type="Claude Code",
                            agent_instance_id=self.agent_instance_id,
                            requires_user_input=False,  # Will be converted by idle detection
                        )
                        # Track this message for idle detection
                        self.last_message_id = response.message_id
                        self.last_message_time = time.time()

                    continue

            # Log current state every check
            if self.last_message_id:
                time_since_message = (
                    time.time() - self.last_message_time
                    if self.last_message_time
                    else 0
                )
                is_working = self.is_claude_working()
                time_since_esc = (
                    time.time() - self.last_esc_interrupt_seen
                    if self.last_esc_interrupt_seen
                    else 999
                )

                self.log(
                    f"[DEBUG] Check idle: msg_id={self.last_message_id}, time_since_msg={time_since_message:.1f}s, is_working={is_working}, time_since_esc={time_since_esc:.1f}s"
                )

                # Don't skip idle checking - we still need to handle normal messages

                # If Claude hasn't been working for 2+ seconds after sending a message
                if not is_working and time_since_message >= 2.0:
                    self.log(
                        f"[INFO] Claude idle for {time_since_message:.1f}s, requesting user input for message {self.last_message_id}"
                    )

                    # Clear these first to avoid repeated calls while waiting
                    message_id_to_update = self.last_message_id
                    self.last_message_id = None
                    self.last_message_time = None

                    try:
                        # Start request_user_input in background - don't await it
                        self.log(
                            f"[INFO] Starting request_user_input task for message {message_id_to_update}"
                        )

                        async def request_input_task():
                            try:
                                self.log(
                                    f"[INFO] Calling request_user_input for message {message_id_to_update}"
                                )

                                # The request_user_input will update the message and wait for a response
                                # It uses the message_id as the last_read_message_id for polling
                                # So it will only get messages AFTER this message
                                user_responses = (
                                    await self.omnara_client.request_user_input(
                                        message_id=message_id_to_update,
                                        timeout_minutes=1440,  # 24 hours
                                        poll_interval=1.0,
                                    )
                                )

                                self.log(
                                    f"[INFO] Got {len(user_responses)} responses from request_user_input"
                                )
                                for response in user_responses:
                                    # Check if this is a permission response
                                    if (
                                        self.permission_message_id
                                        == message_id_to_update
                                    ):
                                        response_text = response.lower().strip()

                                        # Check if it's a yes response (1 or 2)
                                        if response_text in ["1", "2"]:
                                            # For yes responses, just send the number
                                            self.log(
                                                f"[INFO] Permission YES response: {response} -> {response_text}"
                                            )
                                            self.web_ui_messages.add(response_text)
                                            self.input_queue.append(response_text)
                                        else:
                                            # For any other response, just send "3"
                                            self.log(
                                                f"[INFO] Permission NO/other response: {response} -> sending '3'"
                                            )
                                            self.web_ui_messages.add("3")
                                            self.input_queue.append("3")

                                            # Mark that we need to send a follow-up prompt after this
                                            self.need_followup_prompt = True

                                        # Reset permission state and clear buffer
                                        self.permission_active = False
                                        self.permission_extracted = False
                                        self.permission_prompt_time = None
                                        self.permission_message_id = None
                                        # Clear terminal buffer to prevent re-detection
                                        self.terminal_buffer = ""
                                    else:
                                        # Normal message
                                        self.log(
                                            f"[INFO] Queueing user response from request_user_input: {response[:50]}..."
                                        )
                                        self.web_ui_messages.add(
                                            response
                                        )  # Track that this came from web UI
                                        self.input_queue.append(response)

                            except Exception as e:
                                self.log(f"[ERROR] request_user_input task failed: {e}")
                            finally:
                                self.log("[INFO] request_user_input task completed")

                        # Start the task without awaiting
                        asyncio.create_task(request_input_task())
                        self.log("[INFO] Started request_user_input task in background")

                    except Exception as e:
                        self.log(
                            f"[ERROR] Failed to start request_user_input task: {e}"
                        )

    async def run(self):
        """Run Claude with Omnara integration."""
        self.log("[INFO] Starting run() method")

        # Initialize Omnara client
        self.log("[INFO] Initializing Omnara client...")
        await self.init_omnara_client()
        self.log("[INFO] Omnara client initialized")

        # Log available methods for debugging
        methods = [
            method for method in dir(self.omnara_client) if not method.startswith("_")
        ]
        self.log(f"[DEBUG] Available Omnara client methods: {methods}")

        # Store the async loop for use in threads
        self.async_loop = asyncio.get_event_loop()

        # Create initial session
        self.log("[INFO] Creating initial Omnara session...")
        try:
            response = await self.omnara_client.send_message(
                content="Claude wrapper session started - waiting for your input...",
                agent_type="Claude Code",
                requires_user_input=False,
            )
            self.agent_instance_id = response.agent_instance_id
            self.log(f"[INFO] Omnara agent instance ID: {self.agent_instance_id}")

            # Track this initial message for idle checking
            self.last_message_id = response.message_id
            self.last_message_time = time.time()
            self.log(f"[INFO] Initial message ID: {self.last_message_id}")
        except Exception as e:
            self.log(f"[ERROR] Failed to create initial session: {e}")

        # Start background tasks
        asyncio.create_task(self.check_claude_idle())

        # Start Claude in PTY (in thread)
        claude_thread = threading.Thread(target=self.run_claude_with_pty)
        claude_thread.daemon = True
        claude_thread.start()

        # Wait a moment for Claude to start
        await asyncio.sleep(1.0)

        # Start log monitor thread
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
            self.running = False
            self.log("[INFO] Shutting down wrapper...")
            if self.omnara_client:
                if self.agent_instance_id:
                    try:
                        await self.omnara_client.end_session(self.agent_instance_id)
                    except Exception as e:
                        self.log(f"[ERROR] Failed to end session: {e}")
                await self.omnara_client.close()
            if self.log_file:
                self.log("=== Claude Wrapper V2 Log Ended ===")
                self.log_file.close()


def main():
    """Main entry point."""
    parser = argparse.ArgumentParser(
        description="Claude wrapper for Omnara streaming integration",
        add_help=False,  # Disable help to pass through to Claude
    )
    parser.add_argument("--api-key", help="Omnara API key")
    parser.add_argument("--base-url", help="Omnara base URL")

    # Parse known args and pass the rest to Claude
    args, claude_args = parser.parse_known_args()

    # Update sys.argv to only include Claude args
    sys.argv = [sys.argv[0]] + claude_args

    wrapper = ClaudeWrapperV2(api_key=args.api_key, base_url=args.base_url)

    def signal_handler(sig, frame):
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
    except Exception as e:
        # Fatal errors still go to stderr
        print(f"Fatal error: {e}", file=sys.stderr)
        if wrapper.original_tty_attrs:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, wrapper.original_tty_attrs)
        if hasattr(wrapper, "log_file") and wrapper.log_file:
            wrapper.log(f"[FATAL] {e}")
            wrapper.log_file.close()
        sys.exit(1)


if __name__ == "__main__":
    main()
