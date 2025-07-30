#!/usr/bin/env python3
"""
Claude JSON Log Wrapper - Run Claude with terminal UI while monitoring JSON logs for web interface
"""

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
from collections import deque
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, List, Optional

import uvicorn
from fastapi import FastAPI
from fastapi.responses import HTMLResponse, JSONResponse

# Constants
CLAUDE_LOG_BASE = Path.home() / ".claude" / "projects"
PROJECT_DIR_PREFIX = "-Users-kartiksarangmath-Documents-omnara-claude-code-sdk-python"


class ClaudeJSONLogWrapper:
    def __init__(self):
        self.app = FastAPI()
        self.messages: List[Dict[str, Any]] = []
        self.message_id = 0
        self.child_pid = None
        self.master_fd = None
        self.original_tty_attrs = None
        self.input_queue = deque()
        self.current_session_id = None
        self.log_file_path = None
        self.log_monitor_thread = None
        self.running = True
        self.pending_tools = {}  # Track pending tool uses
        self.permission_check_thread = None
        self.terminal_buffer = ""  # Buffer for terminal output
        self.permission_active = False

        self.setup_routes()

    def setup_routes(self):
        """Setup FastAPI routes."""

        @self.app.get("/")
        async def get_index():
            """Serve the main HTML page."""
            return HTMLResponse(
                content="""
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Claude Code - Terminal + Web Interface</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            background-color: #f5f5f5;
            height: 100vh;
            display: flex;
            flex-direction: column;
        }

        .header {
            background-color: #2c3e50;
            color: white;
            padding: 1rem;
            text-align: center;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }

        .header h1 {
            font-size: 1.5rem;
            font-weight: 500;
        }

        .header .subtitle {
            font-size: 0.9rem;
            opacity: 0.8;
            margin-top: 0.25rem;
        }

        .connection-status {
            position: absolute;
            top: 1rem;
            right: 1rem;
            padding: 0.5rem 1rem;
            border-radius: 20px;
            font-size: 0.875rem;
            font-weight: 500;
        }

        .connected {
            background-color: #27ae60;
            color: white;
        }

        .disconnected {
            background-color: #e74c3c;
            color: white;
        }

        .waiting {
            background-color: #f39c12;
            color: white;
        }

        .chat-container {
            flex: 1;
            overflow: hidden;
            display: flex;
            flex-direction: column;
            max-width: 1200px;
            width: 100%;
            margin: 0 auto;
            background-color: white;
            box-shadow: 0 0 10px rgba(0,0,0,0.1);
        }

        .messages {
            flex: 1;
            overflow-y: auto;
            padding: 1.5rem;
            display: flex;
            flex-direction: column;
            gap: 1rem;
        }

        .message {
            max-width: 70%;
            padding: 0.75rem 1rem;
            border-radius: 18px;
            word-wrap: break-word;
            animation: fadeIn 0.3s ease-in;
            white-space: pre-wrap;
        }

        @keyframes fadeIn {
            from {
                opacity: 0;
                transform: translateY(10px);
            }
            to {
                opacity: 1;
                transform: translateY(0);
            }
        }

        .user {
            align-self: flex-end;
            background-color: #007AFF;
            color: white;
            border-bottom-right-radius: 4px;
        }

        .assistant {
            align-self: flex-start;
            background-color: #e9ecef;
            color: #333;
            border-bottom-left-radius: 4px;
        }

        .system {
            align-self: center;
            background-color: #fff3cd;
            color: #856404;
            font-size: 0.875rem;
            border-radius: 12px;
            padding: 0.5rem 1rem;
        }

        .input-container {
            padding: 1rem;
            background-color: white;
            border-top: 1px solid #e0e0e0;
            display: flex;
            gap: 0.5rem;
        }

        #input {
            flex: 1;
            padding: 0.75rem 1rem;
            border: 2px solid #e0e0e0;
            border-radius: 24px;
            font-size: 1rem;
            outline: none;
            transition: border-color 0.2s;
        }

        #input:focus {
            border-color: #007AFF;
        }

        #input:disabled {
            background-color: #f5f5f5;
            cursor: not-allowed;
        }

        #send-button {
            padding: 0.75rem 1.5rem;
            background-color: #007AFF;
            color: white;
            border: none;
            border-radius: 24px;
            cursor: pointer;
            font-size: 1rem;
            font-weight: 500;
            transition: background-color 0.2s;
        }

        #send-button:hover:not(:disabled) {
            background-color: #0056b3;
        }

        #send-button:disabled {
            background-color: #ccc;
            cursor: not-allowed;
        }

        .message pre {
            background-color: #f4f4f4;
            padding: 0.5rem;
            border-radius: 4px;
            overflow-x: auto;
            margin: 0.5rem 0;
            font-family: 'Courier New', monospace;
            font-size: 0.875rem;
        }

        .message code {
            background-color: #f4f4f4;
            padding: 0.125rem 0.25rem;
            border-radius: 3px;
            font-family: 'Courier New', monospace;
            font-size: 0.875rem;
        }

        .info-box {
            background-color: #e3f2fd;
            color: #1976d2;
            padding: 1rem;
            margin: 1rem;
            border-radius: 8px;
            text-align: center;
            font-size: 0.9rem;
        }

        .quick-responses {
            display: flex;
            gap: 0.5rem;
            margin-bottom: 0.5rem;
            flex-wrap: wrap;
        }

        .quick-response {
            padding: 0.5rem 1rem;
            background-color: #f5f5f5;
            border: 1px solid #ddd;
            border-radius: 20px;
            cursor: pointer;
            font-size: 0.875rem;
            transition: all 0.2s;
        }

        .quick-response:hover {
            background-color: #e0e0e0;
            border-color: #007AFF;
        }

        .quick-response.primary {
            background-color: #007AFF;
            color: white;
            border-color: #007AFF;
        }

        .quick-response.primary:hover {
            background-color: #0056b3;
        }

        @media (max-width: 768px) {
            .message {
                max-width: 85%;
            }

            .header h1 {
                font-size: 1.25rem;
            }

            .connection-status {
                position: static;
                margin-top: 0.5rem;
                display: inline-block;
            }
        }
    </style>
</head>
<body>
    <div class="header">
        <h1>Claude Code Web Interface</h1>
        <div class="subtitle">Terminal running in background • Web chat synced via JSON logs</div>
        <div class="connection-status waiting" id="status">Waiting for session...</div>
    </div>

    <div class="chat-container">
        <div class="messages" id="messages">
            <div class="info-box">
                💡 This interface reads from Claude's JSON logs. Use your terminal or type below!
            </div>
        </div>

        <div class="input-container">
            <div class="quick-responses" id="quick-responses" style="display: none;">
                <button class="quick-response primary" onclick="sendQuickResponse('1')">1 - Yes</button>
                <button class="quick-response" onclick="sendQuickResponse('2')">2 - Yes, always</button>
                <button class="quick-response" onclick="sendQuickResponse('3')">3 - No</button>
            </div>
            <input
                type="text"
                id="input"
                placeholder="Type a message..."
                autocomplete="off"
            />
            <button id="send-button">Send</button>
        </div>
    </div>

    <script>
        let lastMessageId = 0;
        let waitingForPermission = false;

        const statusEl = document.getElementById('status');
        const messagesEl = document.getElementById('messages');
        const inputEl = document.getElementById('input');
        const sendButton = document.getElementById('send-button');
        const quickResponsesEl = document.getElementById('quick-responses');

        function setConnectionStatus(status) {
            statusEl.className = 'connection-status ' + status;
            if (status === 'connected') {
                statusEl.textContent = 'Connected';
            } else if (status === 'waiting') {
                statusEl.textContent = 'Waiting for session...';
            } else {
                statusEl.textContent = 'Disconnected';
            }
        }

        function formatContent(content) {
            // Basic markdown-like formatting
            return content
                .replace(/```(\\w+)?\\n([\\s\\S]*?)```/g, '<pre><code>$2</code></pre>')
                .replace(/`([^`]+)`/g, '<code>$1</code>');
        }

        function addMessage(data) {
            const messageEl = document.createElement('div');
            messageEl.className = `message ${data.type}`;

            const contentEl = document.createElement('div');
            contentEl.innerHTML = formatContent(data.content);
            messageEl.appendChild(contentEl);

            messagesEl.appendChild(messageEl);
            messagesEl.scrollTop = messagesEl.scrollHeight;

            // Check if this is a permission prompt
            if (data.type === 'system' && data.content.includes('📝 Reply with:')) {
                waitingForPermission = true;
                quickResponsesEl.style.display = 'flex';

                // Update buttons based on prompt type
                const buttons = quickResponsesEl.querySelectorAll('.quick-response');
                if (data.content.includes('1 (Yes) or 2 (No)')) {
                    // Two-option prompt
                    buttons[0].textContent = '1 - Yes';
                    buttons[0].onclick = () => sendQuickResponse('1');
                    buttons[1].textContent = '2 - No';
                    buttons[1].onclick = () => sendQuickResponse('2');
                    buttons[2].style.display = 'none';
                } else {
                    // Three-option prompt
                    buttons[0].textContent = '1 - Yes';
                    buttons[0].onclick = () => sendQuickResponse('1');
                    buttons[1].textContent = '2 - Yes, always';
                    buttons[1].onclick = () => sendQuickResponse('2');
                    buttons[2].textContent = '3 - No';
                    buttons[2].onclick = () => sendQuickResponse('3');
                    buttons[2].style.display = 'inline-block';
                }
            }
            // Check if permission was granted or prompt is gone
            else if (data.type === 'system' && (
                data.content.includes('✅ Permission granted') ||
                data.content.includes('[Permission response:')
            )) {
                waitingForPermission = false;
                quickResponsesEl.style.display = 'none';
            }
        }

        async function pollMessages() {
            try {
                const response = await fetch(`/messages?since=${lastMessageId}`);
                const data = await response.json();

                if (data.session_active) {
                    setConnectionStatus('connected');

                    if (data.messages && data.messages.length > 0) {
                        data.messages.forEach(msg => {
                            addMessage(msg);
                            lastMessageId = Math.max(lastMessageId, msg.id);
                        });
                    }
                } else {
                    setConnectionStatus('waiting');
                }
            } catch (error) {
                console.error('Failed to poll messages:', error);
                setConnectionStatus('disconnected');
            }
        }

        async function sendMessage() {
            const content = inputEl.value.trim();
            if (!content) return;

            try {
                const response = await fetch('/send', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ content: content })
                });

                const result = await response.json();
                if (result.status === 'queued') {
                    inputEl.value = '';
                    inputEl.focus();
                }
            } catch (error) {
                console.error('Failed to send message:', error);
                alert('Failed to send message: ' + error.message);
            }
        }

        async function sendQuickResponse(response) {
            try {
                const result = await fetch('/send', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ content: response })
                });

                const data = await result.json();
                if (data.status === 'queued') {
                    // Hide quick responses immediately
                    quickResponsesEl.style.display = 'none';
                }
            } catch (error) {
                console.error('Failed to send quick response:', error);
            }
        }

        // Event listeners
        inputEl.addEventListener('keypress', (e) => {
            if (e.key === 'Enter') {
                e.preventDefault();
                sendMessage();
            }
        });

        sendButton.addEventListener('click', sendMessage);

        // Auto-focus input when it becomes enabled
        const observer = new MutationObserver(() => {
            if (!inputEl.disabled) {
                inputEl.focus();
            }
        });
        observer.observe(inputEl, { attributes: true, attributeFilter: ['disabled'] });

        // Start polling
        setInterval(pollMessages, 500);
        pollMessages(); // Initial poll
    </script>
</body>
</html>
            """
            )

        @self.app.get("/messages")
        async def get_messages(since: int = 0):
            """Get messages since a given ID."""
            try:
                new_messages = [msg for msg in self.messages if msg["id"] > since]
                response_data = {
                    "messages": new_messages,
                    "total": len(self.messages),
                    "session_active": self.log_file_path is not None,
                    "ready": True,  # Always ready
                }

                return JSONResponse(response_data)
            except Exception as e:
                print(f"ERROR in /messages endpoint: {str(e)}", file=sys.stderr)
                return JSONResponse({"error": str(e)}, status_code=500)

        @self.app.post("/send")
        async def send_message(data: dict):
            """Queue a message to send to Claude."""
            content = data.get("content", "")

            # Check if this is a permission response
            if content.strip() in ["1", "2", "3"]:
                # Check if we have pending tools (user is likely responding to a prompt)
                if self.pending_tools:
                    # Add a note that this is a permission response
                    self.add_message("user", f"[Permission response: {content}]")

            # Allow empty content to test just sending Enter
            self.input_queue.append(content)
            return {"status": "queued"}

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
        print("Log monitor thread started", file=sys.stderr)

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
                        print(
                            f"Found new log file: {self.log_file_path}", file=sys.stderr
                        )
                        break
            time.sleep(0.5)

        if not self.log_file_path:
            print("No log file found", file=sys.stderr)
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

                            self.process_log_entry(data)
                        except json.JSONDecodeError as e:
                            # Try to show partial data
                            print(f"JSON decode error: {e}", file=sys.stderr)
                            if len(line) > 100:
                                print(f"Line start: {line[:100]}...", file=sys.stderr)
                            else:
                                print(f"Line: {line}", file=sys.stderr)
                        except Exception as e:
                            print(f"Error processing line: {e}", file=sys.stderr)
                            import traceback

                            traceback.print_exc(file=sys.stderr)
                    else:
                        # Check if file still exists and is the latest
                        if not self.log_file_path.exists():
                            print("Log file no longer exists", file=sys.stderr)
                            break
                        time.sleep(0.1)

        except Exception as e:
            print(f"Error monitoring log file: {e}", file=sys.stderr)
            import traceback

            traceback.print_exc(file=sys.stderr)

    def process_log_entry(self, data: Dict[str, Any]):
        """Process a log entry and extract relevant messages."""
        try:
            msg_type = data.get("type")

            if msg_type == "user":
                # Extract user message
                message = data.get("message", {})
                content = message.get("content", "")

                # Handle both string content and tool result content
                if isinstance(content, str) and content:
                    self.add_message("user", content)
                elif isinstance(content, list):
                    # This is a tool result
                    for item in content:
                        if isinstance(item, dict) and item.get("type") == "tool_result":
                            tool_id = item.get("tool_use_id", "")
                            result_content = item.get("content", "")

                            # Check if this was a pending tool
                            if tool_id in self.pending_tools:
                                pending_info = self.pending_tools.pop(tool_id)
                                elapsed = time.time() - pending_info["timestamp"]

                                # If it took more than 2 seconds, user likely saw a prompt
                                if elapsed > 2 and pending_info.get(
                                    "shown_prompt", False
                                ):
                                    self.add_message(
                                        "system",
                                        f"[✅ Permission granted for {pending_info['name']} after {elapsed:.1f}s]",
                                    )

                            if len(result_content) > 200:
                                result_content = result_content[:200] + "..."
                            self.add_message(
                                "user",
                                f"[Tool result for {tool_id[:8]}...]: {result_content}",
                            )
                        else:
                            # Other content types
                            self.add_message("user", str(item))

            elif msg_type == "assistant":
                # Extract assistant message
                message = data.get("message", {})
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
                                    f"[Using tool: {tool_name} - {file_path}]"
                                )
                            elif tool_name == "LS":
                                path = input_data.get("path", ".")
                                tool_parts.append(f"[Using tool: {tool_name} - {path}]")
                            elif tool_name == "Read":
                                file_path = input_data.get("file_path", "unknown")
                                tool_parts.append(
                                    f"[Using tool: {tool_name} - {file_path}]"
                                )
                            elif tool_name == "Bash":
                                command = input_data.get("command", "")
                                if len(command) > 50:
                                    command = command[:50] + "..."
                                tool_parts.append(
                                    f"[Using tool: {tool_name} - {command}]"
                                )
                            elif tool_name == "Grep":
                                pattern = input_data.get("pattern", "")
                                path = input_data.get("path", ".")
                                tool_parts.append(
                                    f"[Using tool: {tool_name} - '{pattern}' in {path}]"
                                )
                            else:
                                tool_parts.append(f"[Using tool: {tool_name}]")
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
                if all_parts:
                    self.add_message("assistant", "\n".join(all_parts))

            elif msg_type == "summary":
                # Session started
                summary = data.get("summary", "")
                if summary:
                    self.add_message("system", f"[Session: {summary}]")
                    self.current_session_id = data.get("leafUuid")

            elif msg_type == "result":
                # Tool result or other results
                subtype = data.get("subtype", "")
                if subtype:
                    self.add_message("system", f"[Result: {subtype}]")

            elif msg_type == "tool_result":
                # Handle tool results
                output = data.get("output", "")
                if output:
                    # Truncate long outputs
                    if len(output) > 200:
                        output = output[:200] + "..."
                    self.add_message("system", f"[Tool result: {output}]")

            elif msg_type == "thinking":
                # Handle thinking messages
                message = data.get("message", {})
                content = message.get("content", "")
                if content:
                    self.add_message("system", f"[Thinking: {content}]")

            elif msg_type in ["create", "update", "message"]:
                # These are metadata messages, skip them
                pass

            else:
                # Unknown message type - show it anyway
                self.add_message("system", f"[{msg_type}]: {str(data)[:100]}...")

        except Exception as e:
            # If there's any error, just show the raw data
            try:
                self.add_message("system", f"[Error parsing message: {str(e)}]")
            except Exception:
                # Last resort - just log the error
                print(f"Critical error processing log entry: {e}", file=sys.stderr)

    def add_message(self, msg_type: str, content: str):
        """Add a message to the list."""
        self.message_id += 1
        msg = {
            "id": self.message_id,
            "type": msg_type,
            "content": content,
            "timestamp": datetime.now().isoformat(),
        }
        self.messages.append(msg)

        # Keep only last 1000 messages
        if len(self.messages) > 1000:
            self.messages = self.messages[-1000:]

    def check_pending_tools(self):
        """Monitor pending tools and show prompt after a delay."""
        # Disable automatic permission detection for now
        # This approach doesn't work well with long-running commands
        # TODO: Find a better way to detect permission prompts
        return

    def run_claude_with_pty(self):
        """Run Claude CLI in a PTY."""
        claude_path = self.find_claude_cli()

        # Build command - normal mode to preserve terminal UI
        cmd = [claude_path]
        if len(sys.argv) > 1:
            cmd.extend(sys.argv[1:])

        print(f"Starting Claude: {' '.join(cmd)}", file=sys.stderr)

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

                            # Check for permission prompts in terminal output
                            try:
                                text = data.decode("utf-8", errors="ignore")
                                self.terminal_buffer += text

                                # Keep only last 1000 chars
                                if len(self.terminal_buffer) > 1000:
                                    self.terminal_buffer = self.terminal_buffer[-1000:]

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
                                        # Extract what Claude is asking permission for
                                        lines = self.terminal_buffer.split("\n")
                                        question = None
                                        has_three_options = False

                                        for line in lines:
                                            if "Do you want to" in line:
                                                question = (
                                                    line.strip()
                                                    .replace("│", "")
                                                    .strip()
                                                )
                                            # Check if it's a 3-option prompt
                                            if "2. Yes, and don't ask again" in line:
                                                has_three_options = True

                                        if question:
                                            self.add_message(
                                                "system",
                                                f"[⏳ Permission prompt detected: {question}]",
                                            )

                                        # Show appropriate options
                                        if has_three_options:
                                            self.add_message(
                                                "system",
                                                "[📝 Reply with: 1 (Yes), 2 (Yes always), or 3 (No)]",
                                            )
                                        else:
                                            self.add_message(
                                                "system",
                                                "[📝 Reply with: 1 (Yes) or 2 (No)]",
                                            )
                                else:
                                    # Reset if prompt is gone
                                    if self.permission_active and "❯" not in text:
                                        self.permission_active = False
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

                # Process web input
                if self.input_queue:
                    content = self.input_queue.popleft()

                    # Special case: if content is empty, just send Enter
                    if not content:
                        os.write(self.master_fd, b"\r")
                    else:
                        # Send the complete text at once
                        os.write(self.master_fd, content.encode())
                        # Small delay
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

    async def run(self):
        """Run web server and Claude."""
        print("Starting web interface on http://localhost:8081", file=sys.stderr)
        print("Starting Claude CLI with JSON log monitoring...\n", file=sys.stderr)

        # Start log monitor thread
        self.log_monitor_thread = threading.Thread(target=self.monitor_log_file)
        self.log_monitor_thread.daemon = True
        self.log_monitor_thread.start()

        # Start permission check thread
        self.permission_check_thread = threading.Thread(target=self.check_pending_tools)
        self.permission_check_thread.daemon = True
        self.permission_check_thread.start()

        # Start Claude in PTY (in thread)
        claude_thread = threading.Thread(target=self.run_claude_with_pty)
        claude_thread.daemon = True
        claude_thread.start()

        # Give Claude a moment to start
        await asyncio.sleep(0.5)

        # Run web server
        config = uvicorn.Config(
            self.app,
            host="0.0.0.0",
            port=8081,
            log_level="error",
        )
        server = uvicorn.Server(config)
        await server.serve()


def main():
    """Main entry point."""
    wrapper = ClaudeJSONLogWrapper()

    def signal_handler(sig, frame):
        print("\nShutting down...", file=sys.stderr)
        wrapper.running = False
        if wrapper.original_tty_attrs:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, wrapper.original_tty_attrs)
        sys.exit(0)

    signal.signal(signal.SIGINT, signal_handler)

    try:
        asyncio.run(wrapper.run())
    except KeyboardInterrupt:
        signal_handler(None, None)
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        import traceback

        traceback.print_exc()
        if wrapper.original_tty_attrs:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, wrapper.original_tty_attrs)
        sys.exit(1)


if __name__ == "__main__":
    main()
