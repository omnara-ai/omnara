#!/usr/bin/env python3
"""
Claude JSON Log Wrapper - Run Claude with terminal UI while monitoring JSON logs for web interface
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
        self.claude_status = (
            "idle"  # idle, working, waiting_for_input, waiting_for_permission
        )
        self.last_terminal_activity = time.time()  # Track last terminal activity
        self.terminal_activity_buffer = ""  # Previous terminal buffer for comparison
        self.last_esc_interrupt_seen = (
            None  # Track when we last saw "(esc to interrupt)"
        )
        self.status_monitor_thread = None
        self.waiting_message_sent = False  # Track if we already sent waiting message
        self.last_entry_was_tool_result = False  # Track if last entry was a tool result
        self.tool_result_timer = None  # Timer to check if Claude responds after tool

        # Clear debug logs on startup
        for log_file in [
            "/tmp/claude_message_debug.log",
            "/tmp/claude_terminal_output.log",
            "/tmp/claude_tool_debug.log",
        ]:
            try:
                with open(log_file, "w") as f:
                    f.write(f"=== Log started at {time.time()} ===\n")
            except (OSError, IOError):
                pass

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

        .claude-indicator {
            display: inline-flex;
            align-items: center;
            gap: 0.5rem;
            padding: 0.25rem 0.75rem;
            border-radius: 12px;
            font-size: 0.875rem;
            margin-left: 1rem;
        }

        .claude-working {
            background-color: #3498db;
            color: white;
        }

        .claude-waiting-input {
            background-color: #27ae60;
            color: white;
        }

        .claude-waiting-permission {
            background-color: #e74c3c;
            color: white;
        }

        .pulse {
            animation: pulse 1.5s infinite;
        }

        @keyframes pulse {
            0% { opacity: 1; }
            50% { opacity: 0.5; }
            100% { opacity: 1; }
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
        <div style="display: flex; align-items: center; justify-content: center; margin-top: 0.5rem;">
            <div class="connection-status waiting" id="status">Waiting for session...</div>
            <div class="claude-indicator" id="claude-status" style="display: none;">
                <span id="claude-status-icon">●</span>
                <span id="claude-status-text">Idle</span>
            </div>
        </div>
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
        const claudeStatusEl = document.getElementById('claude-status');
        const claudeStatusIconEl = document.getElementById('claude-status-icon');
        const claudeStatusTextEl = document.getElementById('claude-status-text');

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

        function updateClaudeStatus(status) {
            claudeStatusEl.style.display = status !== 'idle' ? 'inline-flex' : 'none';

            // Remove all status classes
            claudeStatusEl.classList.remove('claude-working', 'claude-waiting-input', 'claude-waiting-permission', 'pulse');

            switch(status) {
                case 'working':
                    claudeStatusEl.classList.add('claude-working', 'pulse');
                    claudeStatusIconEl.textContent = '●';
                    claudeStatusTextEl.textContent = 'Claude is working...';
                    break;
                case 'waiting_for_input':
                    claudeStatusEl.classList.add('claude-waiting-input');
                    claudeStatusIconEl.textContent = '💬';
                    claudeStatusTextEl.textContent = 'Waiting for your response...';
                    claudeStatusEl.style.backgroundColor = '#28a745';
                    claudeStatusEl.style.color = 'white';
                    break;
                case 'waiting_for_permission':
                    claudeStatusEl.classList.add('claude-waiting-permission');
                    claudeStatusIconEl.textContent = '!';
                    claudeStatusTextEl.textContent = 'Permission needed';
                    break;
                default:
                    claudeStatusEl.style.display = 'none';
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

            // Add indicator if Claude needs input
            if (data.needs_input && data.type === 'assistant') {
                const indicatorEl = document.createElement('div');
                indicatorEl.className = 'needs-input-indicator';
                indicatorEl.innerHTML = '⏸ Waiting for your response...';
                indicatorEl.style.cssText = 'font-size: 0.875rem; color: #666; margin-top: 0.5rem; font-style: italic;';
                messageEl.appendChild(indicatorEl);
            }

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

                    // Update Claude status
                    if (data.claude_status) {
                        updateClaudeStatus(data.claude_status);
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
                    "claude_status": self.claude_status,
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

        # Start the status monitor thread too
        if not self.status_monitor_thread:
            self.status_monitor_thread = threading.Thread(
                target=self.monitor_claude_status
            )
            self.status_monitor_thread.daemon = True
            self.status_monitor_thread.start()
            print("Status monitor thread started", file=sys.stderr)

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

                # User message means Claude will start working on a response
                if self.claude_status != "waiting_for_permission":
                    self.claude_status = "working"
                    self.waiting_message_sent = False  # Reset the flag

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
                            # Don't show tool results in the chat - they're internal
                            # self.add_message(
                            #     "system",
                            #     f"[Tool result for {tool_id[:8]}...]: {result_content}",
                            # )

                        else:
                            # Other content types
                            self.add_message("user", str(item))

                # Check if this entry has toolUseResult - that means a tool was executed
                if data.get("toolUseResult"):
                    self.last_entry_was_tool_result = True
                    # Cancel any existing timer
                    if self.tool_result_timer:
                        self.tool_result_timer = None
                    # Start a new timer to check if Claude responds
                    self.tool_result_timer = threading.Thread(
                        target=self.check_for_waiting_after_tool, daemon=True
                    )
                    self.tool_result_timer.start()
                    with open("/tmp/claude_message_debug.log", "a") as f:
                        f.write(
                            f"Tool result detected, starting timer at {time.time()}\n"
                        )

            elif msg_type == "assistant":
                # Claude is responding, so clear the tool result flag
                self.last_entry_was_tool_result = False

                # Extract assistant message
                message = data.get("message", {})
                message_id = message.get("id", "")
                content_blocks = message.get("content", [])
                text_parts = []
                tool_parts = []

                # Check stop reason to determine Claude's status
                stop_reason = message.get("stop_reason")
                print(
                    f"DEBUG: Assistant message {message_id} stop_reason: {stop_reason}",
                    file=sys.stderr,
                )

                if stop_reason is None:
                    self.claude_status = "working"
                elif stop_reason in ["end_turn", "stop_sequence"]:
                    self.claude_status = "waiting_for_input"
                elif stop_reason == "tool_use":
                    self.claude_status = "working"  # Still working, will use tool
                    # The actual waiting detection is handled by toolUseResult tracking

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

                # Only process if we have content
                if all_parts:
                    message_content = "\n".join(all_parts)

                    # ALWAYS delay assistant messages by 3 seconds to check status
                    threading.Thread(
                        target=self.add_message_with_status_check,
                        args=(message_content,),
                        daemon=True,
                    ).start()

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

    def add_message_with_status_check(self, content: str):
        """Add assistant message after checking if Claude is waiting for input."""
        # Wait 3 seconds
        time.sleep(3.0)

        # Check if Claude is working based on "esc to interrupt)" indicator
        needs_input = not self.is_claude_working()

        # Add message with appropriate needs_input flag
        self.add_message("assistant", content, needs_input=needs_input)

    def check_for_waiting_after_tool(self):
        """Check if Claude needs input after a tool result."""
        # Wait 3 seconds to see if Claude responds
        time.sleep(3.0)

        # Only add waiting message if:
        # 1. We're still marked as tool result being the last entry (Claude didn't respond)
        # 2. Claude is NOT currently working
        if self.last_entry_was_tool_result and not self.is_claude_working():
            # Add an empty message with waiting indicator
            self.add_message("assistant", "", needs_input=True)
            self.last_entry_was_tool_result = False
            with open("/tmp/claude_message_debug.log", "a") as f:
                f.write(f"Added waiting message after tool result at {time.time()}\n")
        elif self.last_entry_was_tool_result and self.is_claude_working():
            # Claude is still working, check again later
            with open("/tmp/claude_message_debug.log", "a") as f:
                f.write(
                    f"Skipped waiting message - Claude still working at {time.time()}\n"
                )
            # Start a new thread to check again when Claude stops working
            threading.Thread(
                target=self.check_again_when_claude_stops, daemon=True
            ).start()

    def check_again_when_claude_stops(self):
        """Keep checking until Claude stops working, then add waiting message if needed."""
        # Check every 0.5 seconds until Claude stops working
        while self.is_claude_working() and self.last_entry_was_tool_result:
            time.sleep(0.5)

        # Claude has stopped working, check if we still need to add the message
        if self.last_entry_was_tool_result:
            # Add an empty message with waiting indicator
            self.add_message("assistant", "", needs_input=True)
            self.last_entry_was_tool_result = False
            with open("/tmp/claude_message_debug.log", "a") as f:
                f.write(
                    f"Added waiting message after Claude stopped working at {time.time()}\n"
                )

    def is_claude_working(self):
        """Check various indicators to determine if Claude is actively working."""
        # List of indicators that show Claude is working
        # We can easily add more indicators here in the future

        # Check 1: "esc to interrupt)" indicator (with closing parenthesis)
        if self.last_esc_interrupt_seen:
            time_since_esc = time.time() - self.last_esc_interrupt_seen
            if time_since_esc < 3.0:
                with open("/tmp/claude_message_debug.log", "a") as f:
                    f.write(
                        f"is_claude_working: YES - esc seen {time_since_esc:.2f}s ago\n"
                    )
                return True

        # Check 2: Could add animation detection here
        # if self.has_terminal_animation():
        #     return True

        # Check 3: Could add other indicators like "Thinking..." etc
        # if "Thinking" in self.terminal_buffer:
        #     return True

        with open("/tmp/claude_message_debug.log", "a") as f:
            f.write(
                f"is_claude_working: NO - last_esc={self.last_esc_interrupt_seen}\n"
            )
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
                    self.waiting_message_sent = False  # Reset flag
                else:
                    # Claude appears to be idle
                    if not self.waiting_message_sent and self.last_esc_interrupt_seen:
                        # Only update status, don't send a separate message
                        # The waiting indicator is already handled when we process assistant messages
                        self.claude_status = "waiting_for_input"
                        self.waiting_message_sent = True
                        # Don't add a separate message here - it's handled in process_log_entry
                        # self.add_message("assistant", "💬", needs_input=True)
                        print(
                            "DEBUG: Claude appears idle, status updated",
                            file=sys.stderr,
                        )

    def add_message(
        self, msg_type: str, content: str, needs_input: Optional[bool] = None
    ):
        """Add a message to the list."""
        self.message_id += 1
        msg = {
            "id": self.message_id,
            "type": msg_type,
            "content": content,
            "timestamp": datetime.now().isoformat(),
        }

        # Add needs_input flag if specified
        if needs_input is not None:
            msg["needs_input"] = needs_input

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

                                # Track terminal activity - only significant changes
                                if (
                                    self.terminal_buffer
                                    != self.terminal_activity_buffer
                                ):
                                    # Check if it's a significant change (more than just cursor/prompt)
                                    diff_len = abs(
                                        len(self.terminal_buffer)
                                        - len(self.terminal_activity_buffer)
                                    )
                                    if diff_len > 5:  # More than 5 chars different
                                        self.last_terminal_activity = time.time()
                                    self.terminal_activity_buffer = self.terminal_buffer

                                # Check for "esc to interrupt)" indicator (may have escape sequences)
                                # Remove ANSI escape sequences first
                                clean_text = re.sub(r"\x1b\[[0-9;]*m", "", text)
                                if "esc to interrupt)" in clean_text:
                                    self.last_esc_interrupt_seen = time.time()
                                    self.claude_status = "working"
                                    self.waiting_message_sent = False  # Reset flag
                                    with open(
                                        "/tmp/claude_message_debug.log", "a"
                                    ) as f:
                                        f.write(
                                            f"FOUND ESC INTERRUPT at {time.time()}\n"
                                        )

                                # Debug: log what we're seeing in terminal
                                if len(text.strip()) > 0 and not text.isspace():
                                    with open(
                                        "/tmp/claude_terminal_output.log", "a"
                                    ) as f:
                                        f.write(f"=== Terminal at {time.time()} ===\n")
                                        f.write(repr(text) + "\n")
                                        f.write(
                                            f"Buffer size: {len(self.terminal_buffer)}\n"
                                        )

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
                                        has_three_options = False

                                        for line in lines:
                                            if "Do you want to" in line:
                                                question = (
                                                    line.strip()
                                                    .replace("│", "")
                                                    .strip()
                                                )
                                            # Check if it's a 3-option prompt
                                            if (
                                                "don't ask again" in line
                                                or "shift+tab" in line
                                            ):
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
                                        # If no longer waiting for permission, update status based on last message
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
