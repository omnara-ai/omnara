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
from omnara.sdk.client import OmnaraClient


# Constants
CLAUDE_LOG_BASE = Path.home() / ".claude" / "projects"
WRAPPER_DEBUG_LOG = Path.home() / ".claude" / "wrapper_v3_debug.log"


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

        # Track if this message uses tools
        self.last_was_tool_use = bool(tools_used)

        # Send to Omnara
        response = self.wrapper.omnara_client_sync.send_message(
            content=content,
            agent_type="Claude Code",
            agent_instance_id=self.wrapper.agent_instance_id,
            requires_user_input=False,
        )

        # Store instance ID if first message
        if not self.wrapper.agent_instance_id:
            self.wrapper.agent_instance_id = response.agent_instance_id

        # Track message for idle detection
        self.last_message_id = response.message_id
        self.last_message_time = time.time()

        # Clear old tracked input requests since we have a new message
        if hasattr(self.wrapper, "requested_input_messages"):
            self.wrapper.requested_input_messages.clear()

        # Clear pending permission options since we have a new message
        if hasattr(self.wrapper, "pending_permission_options"):
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
    def __init__(self, api_key: Optional[str] = None, base_url: Optional[str] = None):
        # Session management
        self.session_uuid = str(uuid.uuid4())
        self.agent_instance_id = None

        # Set up logging
        self.debug_log_file = None
        self._init_logging()

        self.log(f"[INFO] Session UUID: {self.session_uuid}")

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

        # Claude JSONL log monitoring
        self.claude_jsonl_path = None
        self.jsonl_monitor_thread = None
        self.running = True

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

    def _init_logging(self):
        """Initialize debug logging"""
        try:
            WRAPPER_DEBUG_LOG.parent.mkdir(exist_ok=True, parents=True)
            self.debug_log_file = open(WRAPPER_DEBUG_LOG, "w")
            self.log(
                f"=== Claude Wrapper V3 Debug Log - {time.strftime('%Y-%m-%d %H:%M:%S')} ==="
            )
        except Exception as e:
            print(f"Failed to create debug log file: {e}", file=sys.stderr)

    def log(self, message: str):
        """Write to debug log file"""
        if self.debug_log_file:
            try:
                self.debug_log_file.write(f"[{time.strftime('%H:%M:%S')}] {message}\n")
                self.debug_log_file.flush()
            except Exception:
                pass

    def _truncate_text(self, text: str, max_length: int = 100) -> str:
        """Truncate text to max length with ellipsis if needed"""
        if len(text) <= max_length:
            return text
        return text[:max_length] + "..."

    def _format_tool_usage(self, tool_name: str, input_data: Dict[str, Any]) -> str:
        """Format tool usage information based on tool type with markdown"""
        # Skip MCP omnara tools - just show tool name
        if tool_name.startswith("mcp__omnara__"):
            return f"Using tool: {tool_name}"

        # Write tool - show content in code block
        if tool_name == "Write":
            file_path = input_data.get("file_path", "unknown")
            content = input_data.get("content", "")

            # Truncate if too long
            max_content_length = 300
            content_truncated = self._truncate_text(content, max_content_length)

            # Detect file type for syntax highlighting
            file_ext = file_path.split(".")[-1] if "." in file_path else ""
            lang_map = {
                "py": "python",
                "js": "javascript",
                "ts": "typescript",
                "jsx": "jsx",
                "tsx": "tsx",
                "java": "java",
                "cpp": "cpp",
                "c": "c",
                "cs": "csharp",
                "rb": "ruby",
                "go": "go",
                "rs": "rust",
                "php": "php",
                "swift": "swift",
                "kt": "kotlin",
                "yaml": "yaml",
                "yml": "yaml",
                "json": "json",
                "xml": "xml",
                "html": "html",
                "css": "css",
                "scss": "scss",
                "sql": "sql",
                "sh": "bash",
                "bash": "bash",
                "md": "markdown",
                "txt": "text",
            }
            lang = lang_map.get(file_ext, "")

            lines = [f"Using tool: Write - `{file_path}`"]
            lines.append(f"```{lang}")
            lines.append(content_truncated)
            lines.append("```")
            return "\n".join(lines)

        # Other file-related tools
        elif tool_name in ["Read", "NotebookRead", "NotebookEdit"]:
            file_path = input_data.get(
                "file_path", input_data.get("notebook_path", "unknown")
            )
            return f"Using tool: {tool_name} - `{file_path}`"

        # Edit tool - show diff
        elif tool_name == "Edit":
            file_path = input_data.get("file_path", "unknown")
            old_string = input_data.get("old_string", "")
            new_string = input_data.get("new_string", "")

            # Truncate if too long
            max_diff_length = 200
            old_truncated = self._truncate_text(old_string, max_diff_length)
            new_truncated = self._truncate_text(new_string, max_diff_length)

            # Create a markdown diff
            diff_lines = []
            diff_lines.append(f"Using tool: Edit - `{file_path}`")

            # If both are single line and short, show inline
            if (
                "\n" not in old_truncated
                and "\n" not in new_truncated
                and len(old_truncated) < 80
                and len(new_truncated) < 80
            ):
                diff_lines.append("```diff")
                diff_lines.append(f"- {old_truncated}")
                diff_lines.append(f"+ {new_truncated}")
                diff_lines.append("```")
            else:
                # Multi-line diff
                diff_lines.append("```diff")
                if old_truncated:
                    diff_lines.append("@@ Removed @@")
                    for line in old_truncated.splitlines():
                        diff_lines.append(f"- {line}")
                if old_truncated and new_truncated:
                    diff_lines.append("@@ Added @@")
                for line in new_truncated.splitlines():
                    diff_lines.append(f"+ {line}")
                diff_lines.append("```")

            return "\n".join(diff_lines)

        # MultiEdit tool - show file path and number of edits
        elif tool_name == "MultiEdit":
            file_path = input_data.get("file_path", "unknown")
            edits = input_data.get("edits", [])
            return f"Using tool: {tool_name} - `{file_path}` ({len(edits)} edits)"

        # Command execution
        elif tool_name == "Bash":
            command = input_data.get("command", "")
            return f"Using tool: Bash - `{self._truncate_text(command, 80)}`"

        # Search tools
        elif tool_name in ["Grep", "Glob"]:
            pattern = input_data.get("pattern", "unknown")
            path = input_data.get("path", "current directory")
            return f"Using tool: {tool_name} - `{self._truncate_text(pattern, 50)}` in {path}"

        # Directory listing
        elif tool_name == "LS":
            path = input_data.get("path", "unknown")
            return f"Using tool: LS - `{path}`"

        # Todo management
        elif tool_name == "TodoWrite":
            todos = input_data.get("todos", [])
            return f"Using tool: TodoWrite - updating {len(todos)} todo(s)"

        # Task delegation
        elif tool_name == "Task":
            description = input_data.get("description", "unknown task")
            subagent_type = input_data.get("subagent_type", "unknown")
            return f"Using tool: Task - {self._truncate_text(description, 50)} (agent: {subagent_type})"

        # Web operations
        elif tool_name == "WebFetch":
            url = input_data.get("url", "unknown")
            return f"Using tool: WebFetch - `{self._truncate_text(url, 80)}`"

        elif tool_name == "WebSearch":
            query = input_data.get("query", "unknown")
            return f"Using tool: WebSearch - {self._truncate_text(query, 80)}"

        # MCP resource listing
        elif tool_name == "ListMcpResourcesTool":
            return "Using tool: List MCP Resources"

        # Default case for unknown tools
        else:
            # Try to extract meaningful info from input_data
            if input_data:
                # Look for common parameter names
                for key in [
                    "file",
                    "path",
                    "query",
                    "content",
                    "message",
                    "description",
                    "name",
                ]:
                    if key in input_data:
                        value = str(input_data[key])
                        return f"Using tool: {tool_name} - {self._truncate_text(value, 50)}"

            return f"Using tool: {tool_name}"

    def _format_content_block(self, block: Dict[str, Any]) -> Optional[str]:
        """Format different types of content blocks with markdown"""
        block_type = block.get("type")

        if block_type == "text":
            text_content = block.get("text", "")
            if not text_content:
                return None
            return text_content

        elif block_type == "tool_use":
            # Track tool usage
            tool_name = block.get("name", "unknown")
            input_data = block.get("input", {})
            return self._format_tool_usage(tool_name, input_data)

        elif block_type == "tool_result":
            # Format tool results
            content = block.get("content", [])
            if isinstance(content, list):
                # Extract text from tool result content
                result_texts = []
                for item in content:
                    if isinstance(item, dict) and item.get("type") == "text":
                        result_text = item.get("text", "")
                        if result_text:
                            # Try to parse as JSON for cleaner display
                            try:
                                import json

                                parsed = json.loads(result_text)
                                # Just show a compact summary for JSON results
                                if isinstance(parsed, dict):
                                    keys = list(parsed.keys())[:3]
                                    summary = (
                                        f"JSON object with keys: {', '.join(keys)}"
                                    )
                                    if len(parsed) > 3:
                                        summary += f" and {len(parsed) - 3} more"
                                    result_texts.append(summary)
                                else:
                                    result_texts.append(
                                        self._truncate_text(result_text, 100)
                                    )
                            except (json.JSONDecodeError, ValueError):
                                # Not JSON, just add as text
                                result_texts.append(
                                    self._truncate_text(result_text, 100)
                                )
                if result_texts:
                    combined = " | ".join(result_texts)
                    return f"Result: {combined}"
            elif isinstance(content, str):
                return f"Result: {self._truncate_text(content, 200)}"
            return "Result: [empty]"

        elif block_type == "thinking":
            # Include thinking content
            thinking_text = block.get("text", "")
            if thinking_text:
                return f"[Thinking: {self._truncate_text(thinking_text, 200)}]"

        # Unknown block type
        return None

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

    def find_claude_cli(self):
        """Find Claude CLI binary"""
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
        """Get the Claude project log directory for current working directory"""
        cwd = os.getcwd()
        # Convert path to Claude's format
        project_name = cwd.replace("/", "-")
        project_dir = CLAUDE_LOG_BASE / project_name
        return project_dir if project_dir.exists() else None

    def monitor_claude_jsonl(self):
        """Monitor Claude's JSONL log file for messages"""
        # Wait for log file to be created
        expected_filename = f"{self.session_uuid}.jsonl"

        while self.running and not self.claude_jsonl_path:
            project_dir = self.get_project_log_dir()
            if project_dir:
                expected_path = project_dir / expected_filename
                if expected_path.exists():
                    self.claude_jsonl_path = expected_path
                    self.log(f"[INFO] Found Claude JSONL log: {expected_path}")
                    break
            time.sleep(0.5)

        if not self.claude_jsonl_path:
            return

        # Monitor the file
        try:
            with open(self.claude_jsonl_path, "r") as f:
                f.seek(0)  # Start from beginning

                while self.running:
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
                            break
                        time.sleep(0.1)

        except Exception as e:
            self.log(f"[ERROR] Error monitoring Claude JSONL: {e}")

    def process_claude_log_entry(self, data: Dict[str, Any]):
        """Process a log entry from Claude's JSONL (sync)"""
        try:
            msg_type = data.get("type")

            if msg_type == "user":
                # User message
                message = data.get("message", {})
                content = message.get("content", "")

                # Handle both string content and structured content blocks
                if isinstance(content, str) and content:
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
                            formatted_content = self._format_content_block(block)
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
                        formatted_content = self._format_content_block(block)
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
                    response = self.omnara_client_sync.send_message(
                        content=f"Claude session started: {summary}",
                        agent_type="Claude Code",
                        requires_user_input=False,
                    )
                    self.agent_instance_id = response.agent_instance_id

        except Exception as e:
            self.log(f"[ERROR] Error processing Claude log entry: {e}")

    def is_claude_idle(self):
        """Check if Claude is idle (hasn't shown 'esc to interrupt' for 0.5+ seconds)"""
        if self.last_esc_interrupt_seen:
            time_since_esc = time.time() - self.last_esc_interrupt_seen
            return time_since_esc >= 0.5
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
                            agent_type="Claude Code",
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
        import re

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
            import re

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
            # Regular permission prompt
            for line in clean_buffer.split("\n"):
                line_clean = line.strip().replace("\u2502", "").strip()
                if "Do you want to" in line_clean:
                    question = line_clean
                    break

        # Default question if not found
        if not question:
            question = "Permission required"
            self.log("[DEBUG] No question found, using default")

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
            # Look for options from bottom to top to get the actual prompt options
            for i in range(len(lines) - 1, -1, -1):
                line = lines[i].strip().replace("\u2502", "").strip()
                # Remove selection indicators
                line = line.replace("\u276f", "").strip()

                # Check for specific permission prompt options
                if line.startswith("1.") and "Yes" in line and "1" not in options_dict:
                    options_dict["1"] = line
                elif (
                    line.startswith("2.")
                    and ("don't ask again" in line or "Yes" in line)
                    and "2" not in options_dict
                ):
                    options_dict["2"] = line
                elif line.startswith("3.") and "No" in line and "3" not in options_dict:
                    options_dict["3"] = line

                # Stop if we've found all three options
                if len(options_dict) == 3:
                    break

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

                # When expecting permission prompt, check if we need to handle it
                if self.message_processor.last_was_tool_use and self.is_claude_idle():
                    # After tool use + idle, assume permission prompt is shown
                    if not hasattr(self, "_permission_assumed_time"):
                        self._permission_assumed_time = time.time()

                    # After 0.25 seconds, check if we can parse the prompt from buffer
                    elif time.time() - self._permission_assumed_time > 0.25:
                        # Clean the buffer to check for content
                        import re

                        clean_buffer = re.sub(
                            r"\x1b\[[0-9;]*[a-zA-Z]", "", self.terminal_buffer
                        )

                        # If we see permission/plan prompt, extract it
                        # For plan mode: "Would you like to proceed" without "(esc"
                        # For permission: "Do you want to" with "(esc"
                        if (
                            "Do you want to" in clean_buffer and "(esc" in clean_buffer
                        ) or (
                            "Would you like to proceed" in clean_buffer
                            and "No, keep planning" in clean_buffer
                        ):
                            if not hasattr(self, "_permission_handled"):
                                self._permission_handled = True

                                # Extract prompt components using the shared method
                                question, options, options_map = (
                                    self._extract_permission_prompt(clean_buffer)
                                )

                                # Build the message
                                if options:
                                    options_text = "\n".join(options)
                                    permission_msg = f"{question}\n\n[OPTIONS]\n{options_text}\n[/OPTIONS]"
                                    self.pending_permission_options = options_map
                                else:
                                    # Fallback if parsing fails
                                    permission_msg = f"{question}\n\n[OPTIONS]\n1. Yes\n2. Yes, and don't ask again this session\n3. No\n[/OPTIONS]"
                                    self.pending_permission_options = {
                                        "Yes": "1",
                                        "Yes, and don't ask again this session": "2",
                                        "No": "3",
                                    }

                                self.log(
                                    f"[INFO] Permission prompt extracted: {permission_msg[:100]}..."
                                )

                                # Send to Omnara with extracted text
                                if self.agent_instance_id and self.omnara_client_sync:
                                    response = self.omnara_client_sync.send_message(
                                        content=permission_msg,
                                        agent_type="Claude Code",
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
                                    agent_type="Claude Code",
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
                                import re

                                clean_text = re.sub(r"\x1b\[[0-9;]*m", "", text)
                                if "esc to interrupt)" in clean_text:
                                    self.last_esc_interrupt_seen = time.time()

                            except Exception:
                                pass
                        else:
                            break
                    except BlockingIOError:
                        pass
                    except OSError:
                        break

                # Handle user input from stdin
                if sys.stdin in rlist and self.original_tty_attrs:
                    try:
                        data = os.read(sys.stdin.fileno(), 4096)
                        if data:
                            # Forward to Claude
                            os.write(self.master_fd, data)
                    except OSError:
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

                    self.log(
                        f"[INFO] Sending web UI message to Claude: {content[:50]}..."
                    )

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

        # Initialize Omnara clients (sync)
        self.log("[INFO] Initializing Omnara clients...")
        self.init_omnara_clients()
        self.log("[INFO] Omnara clients initialized")

        # Create initial session (sync)
        self.log("[INFO] Creating initial Omnara session...")
        try:
            if self.omnara_client_sync:
                response = self.omnara_client_sync.send_message(
                    content="Claude wrapper V3 session started - waiting for your input...",
                    agent_type="Claude Code",
                    requires_user_input=False,
                )
                self.agent_instance_id = response.agent_instance_id
                self.log(f"[INFO] Omnara agent instance ID: {self.agent_instance_id}")

                # Initialize message processor with first message
                if hasattr(self.message_processor, "last_message_id"):
                    self.message_processor.last_message_id = response.message_id
                    self.message_processor.last_message_time = time.time()
        except Exception as e:
            self.log(f"[ERROR] Failed to create initial session: {e}")

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
        except KeyboardInterrupt:
            pass
        finally:
            # Clean up
            self.running = False
            self.log("[INFO] Shutting down wrapper...")

            # Cancel pending tasks
            self.cancel_pending_input_request()

            # Close clients
            if self.omnara_client_sync:
                if self.agent_instance_id:
                    try:
                        self.omnara_client_sync.end_session(self.agent_instance_id)
                    except Exception as e:
                        self.log(f"[ERROR] Failed to end session: {e}")
                self.omnara_client_sync.close()

            # Close async client
            if self.omnara_client_async and self.async_loop:

                async def close_async_client():
                    if self.omnara_client_async:
                        await self.omnara_client_async.close()

                self.async_loop.run_until_complete(close_async_client())

            if self.debug_log_file:
                self.log("=== Claude Wrapper V3 Log Ended ===")
                self.debug_log_file.close()


def main():
    """Main entry point"""
    parser = argparse.ArgumentParser(
        description="Claude wrapper V3 for Omnara integration",
        add_help=False,  # Disable help to pass through to Claude
    )
    parser.add_argument("--api-key", help="Omnara API key")
    parser.add_argument("--base-url", help="Omnara base URL")

    # Parse known args and pass the rest to Claude
    args, claude_args = parser.parse_known_args()

    # Update sys.argv to only include Claude args
    sys.argv = [sys.argv[0]] + claude_args

    wrapper = ClaudeWrapperV3(api_key=args.api_key, base_url=args.base_url)

    def signal_handler(sig, frame):
        wrapper.running = False
        if wrapper.original_tty_attrs:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, wrapper.original_tty_attrs)
        sys.exit(0)

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
    signal.signal(signal.SIGWINCH, handle_resize)  # Handle terminal resize

    try:
        wrapper.run()
    except KeyboardInterrupt:
        signal_handler(None, None)
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
