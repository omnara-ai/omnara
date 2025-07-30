#!/usr/bin/env python3
"""
Claude JSON Log Wrapper - Run Claude with terminal UI while sending logs to Omnara
"""

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
    def __init__(self):
        # Omnara SDK setup
        api_key = os.environ.get("OMNARA_API_KEY")
        if not api_key:
            print("ERROR: OMNARA_API_KEY environment variable not set", file=sys.stderr)
            sys.exit(1)
        # api_key is guaranteed to be str after the check above
        self.api_key: str = api_key

        self.base_url = os.environ.get(
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
        self.claude_status = (
            "idle"  # idle, working, waiting_for_input, waiting_for_permission
        )
        self.last_terminal_activity = time.time()
        self.terminal_activity_buffer = ""
        self.last_esc_interrupt_seen = None
        self.status_monitor_thread = None
        self.waiting_message_sent = False
        self.last_entry_was_tool_result = False

        # Debug logs disabled to avoid terminal interference
        pass

    async def init_omnara_client(self):
        """Initialize the Omnara SDK client."""
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
                    response = await self.omnara_client.log_step(
                        agent_type="Claude Code",
                        step_description="[Session started]",
                        agent_instance_id=None,
                    )
                    self.agent_instance_id = response.agent_instance_id

                # Create question but don't wait for response
                data = {
                    "agent_instance_id": self.agent_instance_id,
                    "question_text": description,
                }
                response = await self.omnara_client._make_request(
                    "POST", "/api/v1/questions", json=data, timeout=5
                )
                self.pending_question_id = response["question_id"]

                # Start a background task to poll for dashboard responses
                asyncio.create_task(
                    self._poll_for_dashboard_answer(response["question_id"])
                )

                return None  # Don't block waiting
            else:
                # Just log a step
                response = await self.omnara_client.log_step(
                    agent_type="Claude Code",
                    step_description=description,
                    agent_instance_id=self.agent_instance_id,
                )
                # Store instance ID if this is the first call
                if not self.agent_instance_id:
                    self.agent_instance_id = response.agent_instance_id

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

        # Wait for log file to be created
        while self.running and not self.log_file_path:
            project_dir = self.get_project_log_dir()
            if project_dir:
                # Check for new log file
                latest_log = self.find_latest_log_file(project_dir)
                if latest_log and (
                    not self.log_file_path or latest_log != self.log_file_path
                ):
                    # Check if this is a new file (created recently)
                    if (
                        time.time() - latest_log.stat().st_mtime < 10
                    ):  # Created in last 10 seconds
                        self.log_file_path = latest_log
                        # Found new log file
                        pass
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
                    # Check if we have a pending question to answer
                    if self.pending_question_id and self.omnara_client:
                        try:
                            await self.omnara_client.answer_question(
                                question_id=self.pending_question_id, answer=content
                            )
                            self.pending_question_id = None  # Clear after answering
                        except Exception:
                            pass
                    # Don't log user messages - we already see them when answering questions
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
                    # Start a new timer to check if Claude responds
                    asyncio.create_task(self.check_for_waiting_after_tool())

            elif msg_type == "assistant":
                # Claude is responding, so clear the tool result flag
                self.last_entry_was_tool_result = False

                # Extract assistant message
                message = data.get("message", {})
                # message_id = message.get("id", "")
                content_blocks = message.get("content", [])
                text_parts = []
                tool_parts = []

                for block in content_blocks:
                    if isinstance(block, dict):
                        block_type = block.get("type")
                        if block_type == "text":
                            text_parts.append(block.get("text", ""))
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

                    # Wait 3 seconds to see if Claude is still working
                    await asyncio.sleep(3.0)

                    # Check if Claude is still working (has "esc to interrupt)" indicator)
                    if self.is_claude_working():
                        # Claude is still working, just log the message
                        await self.log_to_omnara(message_content)
                    else:
                        # Claude is NOT working (no "esc to interrupt)"), so it's waiting for input
                        await self.log_to_omnara(
                            message_content,
                            needs_response=True,
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

    async def check_for_waiting_after_tool(self):
        """Check if Claude needs input after a tool result."""
        # Keep checking until Claude stops working
        while self.is_claude_working() and self.last_entry_was_tool_result:
            await asyncio.sleep(0.5)

        # Claude has stopped working, check if we still need to add the message
        if self.last_entry_was_tool_result:
            # Claude needs input - ask via Omnara with empty question
            await self.log_to_omnara(
                "",
                needs_response=True,
            )
            self.last_entry_was_tool_result = False

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
        cmd = [claude_path]
        if len(sys.argv) > 1:
            cmd.extend(sys.argv[1:])

        # Starting Claude

        # Save original terminal settings if in a terminal
        try:
            self.original_tty_attrs = termios.tcgetattr(sys.stdin)
        except Exception:
            self.original_tty_attrs = None

        # Create PTY
        self.child_pid, self.master_fd = pty.fork()

        if self.child_pid == 0:
            # Child process - exec Claude CLI
            os.environ["CLAUDE_CODE_ENTRYPOINT"] = "jsonlog-wrapper"
            os.execvp(cmd[0], cmd)

        # Parent process - handle I/O
        try:
            # Set stdin to raw mode if in a terminal
            if self.original_tty_attrs:
                tty.setraw(sys.stdin)

            while self.running:
                # Use select to multiplex I/O
                rlist, _, _ = select.select([sys.stdin, self.master_fd], [], [], 0.05)

                # Handle terminal I/O first
                if self.master_fd in rlist:
                    # Read from PTY
                    try:
                        data = os.read(self.master_fd, 4096)
                        if data:
                            # Forward to stdout (terminal)
                            os.write(sys.stdout.fileno(), data)

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

                                # Keep only last 1000 chars
                                if len(self.terminal_buffer) > 1000:
                                    self.terminal_buffer = self.terminal_buffer[-1000:]
                                    self.terminal_activity_buffer = self.terminal_buffer

                                # Check for permission prompt patterns
                                if any(
                                    pattern in self.terminal_buffer
                                    for pattern in [
                                        "Do you want to",
                                        "❯ 1. Yes",
                                        "❯ 1. Proceed",
                                    ]
                                ):
                                    if not self.permission_active:
                                        self.permission_active = True
                                        self.claude_status = "waiting_for_permission"

                                        # Extract what Claude is asking permission for
                                        lines = self.terminal_buffer.split("\n")
                                        question = None
                                        for line in lines:
                                            if "Do you want to" in line:
                                                question = (
                                                    line.strip()
                                                    .replace("│", "")
                                                    .strip()
                                                )
                                                break

                                        if question:
                                            # Ask via Omnara
                                            asyncio.run_coroutine_threadsafe(
                                                self.handle_permission_prompt(question),
                                                self.async_loop,
                                            )
                                else:
                                    # Reset if prompt is gone
                                    if self.permission_active and "❯" not in text:
                                        self.permission_active = False
                                        if (
                                            self.claude_status
                                            == "waiting_for_permission"
                                        ):
                                            self.claude_status = "waiting_for_input"
                            except Exception:
                                pass
                        else:
                            break
                    except OSError:
                        break

                if sys.stdin in rlist and self.original_tty_attrs:
                    # Forward stdin to PTY
                    data = os.read(sys.stdin.fileno(), 1024)
                    if data:
                        os.write(self.master_fd, data)

                # Process Omnara responses
                if self.input_queue:
                    content = self.input_queue.popleft()

                    # Send the complete text at once
                    os.write(self.master_fd, content.encode())
                    time.sleep(0.05)
                    # Send Enter
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

    async def handle_permission_prompt(self, question: str):
        """Handle permission prompts via Omnara."""
        response = await self.log_to_omnara(
            f"Permission requested: {question}\n\nReply with: 1 (Yes), 2 (Yes always), or 3 (No)",
            needs_response=True,
        )
        if response:
            self.input_queue.append(response)

    async def run(self):
        """Run Claude with Omnara integration."""
        # Starting Claude with Omnara integration

        # Initialize Omnara client
        await self.init_omnara_client()

        # Store the async loop for use in threads
        self.async_loop = asyncio.get_event_loop()

        # Start log monitor thread
        self.log_monitor_thread = threading.Thread(target=self.monitor_log_file)
        self.log_monitor_thread.daemon = True
        self.log_monitor_thread.start()

        # Start Claude in PTY (in thread)
        claude_thread = threading.Thread(target=self.run_claude_with_pty)
        claude_thread.daemon = True
        claude_thread.start()

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
    wrapper = ClaudeJSONLogWrapper()

    def signal_handler(sig, frame):
        # Shutting down
        wrapper.running = False
        if wrapper.original_tty_attrs:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, wrapper.original_tty_attrs)
        sys.exit(0)

    signal.signal(signal.SIGINT, signal_handler)

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
