#!/usr/bin/env python3
"""
AMP Wrapper for Omnara - Full bidirectional integration with Amp CLI
Based on Claude wrapper v3 architecture but adapted for Amp's behavior
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
import subprocess
import sys
import termios
import threading
import time
import tty
import uuid
from collections import deque
from pathlib import Path
from typing import Any, Dict, Optional, Tuple

from omnara.sdk.async_client import AsyncOmnaraClient
from omnara.sdk.client import OmnaraClient
from omnara.sdk.exceptions import AuthenticationError, APIError


# Constants
AMP_LOG_FILE = Path.home() / ".cache/amp/logs/cli.log"
AMP_SETTINGS_FILE = Path.home() / ".config/amp/settings.json"
OMNARA_WRAPPER_LOG_DIR = Path.home() / ".omnara" / "amp_wrapper"

# ANSI escape code regex for stripping
ANSI_ESCAPE = re.compile(r'\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])')

# Terminal patterns for Amp
PATTERNS = {
    'thinking_start': r'Thinking\.{3,}',
    'thinking_end': r'I\s+(need to|will|should|can)',
    'user_prompt_box': r'╭─+╮.*?╰─+╯',
    'welcome': r'Welcome to [Aa][Mm][Pp]',
    'processing': r'Running inference',
    'error': r'Error:|Failed|Cannot|Unable',
    'prompt_ready': r'╭─',  # New prompt box appearing
}


class MessageProcessor:
    """Message processing for Amp integration"""
    
    def __init__(self, wrapper: "AmpWrapper"):
        self.wrapper = wrapper
        self.last_message_id = None
        self.last_message_time = None
        self.web_ui_messages = set()
        self.pending_input_message_id = None
        self.in_thinking = False
        self.thinking_buffer = ""
        
    def process_user_message_sync(self, content: str, from_web: bool) -> None:
        """Process a user message"""
        if from_web:
            # Track web messages to avoid duplicates
            self.web_ui_messages.add(content)
        else:
            # CLI message - send to Omnara if not from web
            if content not in self.web_ui_messages:
                self.wrapper.log(f"[INFO] Sending user message to Omnara: {content[:50]}...")
                if self.wrapper.agent_instance_id and self.wrapper.omnara_client_sync:
                    self.wrapper.omnara_client_sync.send_user_message(
                        agent_instance_id=self.wrapper.agent_instance_id,
                        content=content,
                    )
            else:
                self.web_ui_messages.discard(content)
        
        # Reset idle timer
        self.last_message_time = time.time()
        self.pending_input_message_id = None
    
    def process_assistant_message_sync(self, content: str) -> None:
        """Process an assistant message from Amp"""
        if not self.wrapper.omnara_client_sync:
            return
        
        # Sanitize content for API
        sanitized_content = "".join(
            char if ord(char) >= 32 or char in "\n\r\t" else ""
            for char in content.replace("\x00", "")
        )
        
        # Get git diff if enabled
        git_diff = self.wrapper.get_git_diff()
        if git_diff:
            git_diff = "".join(
                char if ord(char) >= 32 or char in "\n\r\t" else ""
                for char in git_diff.replace("\x00", "")
            )
        
        # Send to Omnara
        self.wrapper.log(f"[INFO] Sending assistant message to Omnara API: {sanitized_content[:100]}...")
        self.wrapper.log(f"[DEBUG] Agent instance ID: {self.wrapper.agent_instance_id}")
        
        response = self.wrapper.omnara_client_sync.send_message(
            content=sanitized_content,
            agent_type="Amp",
            agent_instance_id=self.wrapper.agent_instance_id,
            requires_user_input=False,
            git_diff=git_diff,
        )
        
        self.wrapper.log(f"[INFO] Message sent successfully, response ID: {response.message_id}")
        
        # Store instance ID if first message
        if not self.wrapper.agent_instance_id:
            self.wrapper.agent_instance_id = response.agent_instance_id
            self.wrapper.log(f"[INFO] Stored agent instance ID: {self.wrapper.agent_instance_id}")
        
        # Track message
        self.last_message_id = response.message_id
        self.last_message_time = time.time()
        
        # Process any queued user messages
        if response.queued_user_messages:
            self.wrapper.log(f"[INFO] Got {len(response.queued_user_messages)} queued user messages")
            concatenated = "\n".join(response.queued_user_messages)
            self.web_ui_messages.add(concatenated)
            self.wrapper.input_queue.append(concatenated)
    
    def should_request_input(self) -> Optional[str]:
        """Check if we should request input from web UI"""
        if (
            self.last_message_id
            and self.last_message_id != self.pending_input_message_id
            and self.wrapper.is_amp_idle()
        ):
            return self.last_message_id
        return None
    
    def mark_input_requested(self, message_id: str) -> None:
        """Mark that input has been requested"""
        self.pending_input_message_id = message_id


class AmpWrapper:
    def __init__(self, api_key: Optional[str] = None, base_url: Optional[str] = None):
        # Session management
        self.session_uuid = str(uuid.uuid4())
        self.session_start_time = time.time()
        self.agent_instance_id = None
        
        # Logging
        self.debug_log_file = None
        self._init_logging()
        
        self.log(f"[INFO] AMP Wrapper Session UUID: {self.session_uuid}")
        
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
        
        # Terminal interaction
        self.child_pid = None
        self.master_fd = None
        self.original_tty_attrs = None
        self.input_queue = deque()
        
        # Amp output monitoring
        self.terminal_buffer = ""
        self.output_buffer = ""
        self.last_output_time = time.time()
        self.running = True
        
        # Message processor
        self.message_processor = MessageProcessor(self)
        
        # Async task management
        self.pending_input_task = None
        self.async_loop = None
        self.requested_input_messages = set()
        
        # Git diff tracking
        self.git_diff_enabled = False
        self.initial_git_hash = None
        
        # Amp-specific state
        self.amp_ready = False
        self.last_prompt_sent = None
        self.waiting_for_response = False
        
    def _init_logging(self):
        """Initialize debug logging"""
        try:
            OMNARA_WRAPPER_LOG_DIR.mkdir(exist_ok=True, parents=True)
            log_file_path = OMNARA_WRAPPER_LOG_DIR / f"amp_{self.session_uuid}.log"
            self.debug_log_file = open(log_file_path, "w")
            self.log(f"=== AMP Wrapper Debug Log - {time.strftime('%Y-%m-%d %H:%M:%S')} ===")
            print(f"Debug log: {log_file_path}", file=sys.stderr)
        except Exception as e:
            print(f"Failed to create debug log file: {e}", file=sys.stderr)
    
    def log(self, message: str):
        """Write to debug log"""
        if self.debug_log_file:
            try:
                self.debug_log_file.write(f"[{time.strftime('%H:%M:%S')}] {message}\n")
                self.debug_log_file.flush()
            except Exception:
                pass
    
    def strip_ansi(self, text: str) -> str:
        """Remove ANSI escape codes from text"""
        return ANSI_ESCAPE.sub('', text)
    
    def extract_ansi_codes(self, text: str) -> list:
        """Extract ANSI codes from text to understand formatting"""
        codes = ANSI_ESCAPE.findall(text)
        return codes
    
    def split_by_ansi_style(self, text: str) -> list:
        """Split text into segments with their ANSI style information"""
        segments = []
        current_pos = 0
        
        for match in ANSI_ESCAPE.finditer(text):
            # Add text before this ANSI code
            if match.start() > current_pos:
                segments.append({
                    'text': text[current_pos:match.start()],
                    'code': match.group(0)
                })
            current_pos = match.end()
        
        # Add any remaining text
        if current_pos < len(text):
            segments.append({
                'text': text[current_pos:],
                'code': None
            })
        
        return segments
    
    def init_omnara_clients(self):
        """Initialize Omnara SDK clients"""
        if not self.api_key:
            raise ValueError("API key is required")
        
        # Initialize sync client
        self.omnara_client_sync = OmnaraClient(
            api_key=self.api_key, base_url=self.base_url
        )
        
        # Initialize async client
        self.omnara_client_async = AsyncOmnaraClient(
            api_key=self.api_key, base_url=self.base_url
        )
    
    def find_amp_cli(self):
        """Find Amp CLI binary"""
        if cli := shutil.which("amp"):
            return cli
        
        # Check common installation locations
        locations = [
            Path.home() / ".local/bin/amp",
            Path("/usr/local/bin/amp"),
            Path("/opt/homebrew/bin/amp"),
            Path.home() / ".amp/bin/amp",
        ]
        
        for path in locations:
            if path.exists() and path.is_file():
                return str(path)
        
        raise FileNotFoundError(
            "Amp CLI not found. Please install from ampcode.com"
        )
    
    def create_amp_settings(self):
        """Create temporary Amp settings with MCP servers if needed"""
        settings = {
            "amp.mcpServers": {},  # Can be populated with MCP servers
            "amp.commands.allowlist": ["*"],  # Allow all commands
            "amp.notifications.enabled": False,
        }
        
        temp_settings = Path("/tmp") / f"amp_omnara_{self.session_uuid}.json"
        with open(temp_settings, "w") as f:
            json.dump(settings, f)
        
        return str(temp_settings)
    
    def init_git_tracking(self):
        """Initialize git tracking for file changes"""
        try:
            # Check if we're in a git repo
            result = subprocess.run(
                ["git", "rev-parse", "--show-toplevel"],
                capture_output=True,
                text=True,
                timeout=2,
            )
            
            if result.returncode == 0:
                # Get initial commit hash
                result = subprocess.run(
                    ["git", "rev-parse", "HEAD"],
                    capture_output=True,
                    text=True,
                    timeout=2,
                )
                
                if result.returncode == 0:
                    self.initial_git_hash = result.stdout.strip()
                    self.git_diff_enabled = True
                    self.log(f"[INFO] Git tracking enabled. Initial hash: {self.initial_git_hash}")
                else:
                    # No commits yet
                    self.initial_git_hash = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"  # Empty tree hash
                    self.git_diff_enabled = True
                    self.log("[INFO] Git repo has no commits yet, using empty tree hash")
        except Exception as e:
            self.log(f"[WARNING] Git tracking disabled: {e}")
    
    def get_git_diff(self) -> Optional[str]:
        """Get git diff since session start"""
        if not self.git_diff_enabled or not self.initial_git_hash:
            return None
        
        try:
            # Get staged and unstaged changes
            result = subprocess.run(
                ["git", "diff", self.initial_git_hash],
                capture_output=True,
                text=True,
                timeout=5,
            )
            
            diff_output = result.stdout if result.returncode == 0 else ""
            
            # Get untracked files
            status_result = subprocess.run(
                ["git", "status", "--porcelain"],
                capture_output=True,
                text=True,
                timeout=2,
            )
            
            combined_output = diff_output
            
            # Add untracked files as new file diffs
            if status_result.returncode == 0:
                for line in status_result.stdout.splitlines():
                    if line.startswith("??"):
                        file_path = line[3:].strip()
                        
                        # Add as new file diff
                        combined_output += f"\ndiff --git a/{file_path} b/{file_path}\n"
                        combined_output += "new file mode 100644\n"
                        combined_output += "index 0000000..0000000\n"
                        combined_output += f"--- /dev/null\n"
                        combined_output += f"+++ b/{file_path}\n"
                        
                        # Read file contents
                        try:
                            with open(file_path, "r", encoding="utf-8", errors="ignore") as f:
                                lines = f.readlines()
                                combined_output += f"@@ -0,0 +1,{len(lines)} @@\n"
                                for line in lines:
                                    if line.endswith("\n"):
                                        combined_output += f"+{line}"
                                    else:
                                        combined_output += f"+{line}\n"
                                if lines and not lines[-1].endswith("\n"):
                                    combined_output += "\\ No newline at end of file\n"
                        except Exception:
                            combined_output += "@@ -0,0 +1,1 @@\n"
                            combined_output += "+[Binary or unreadable file]\n"
                        
                        combined_output += "\n"
            
            return combined_output
        
        except Exception as e:
            self.log(f"[WARNING] Failed to get git diff: {e}")
        
        return None
    
    def is_amp_idle(self):
        """Check if Amp is idle and ready for input"""
        # Don't consider idle if we're waiting for a response
        if self.waiting_for_response:
            return False
        
        # Amp is idle if:
        # 1. We see a prompt box in the buffer AND not waiting for response
        # 2. No new output for 2+ seconds AND not waiting for response
        # 3. Not currently processing
        
        clean_buffer = self.strip_ansi(self.terminal_buffer[-500:])
        
        # Check if processing
        if "Running inference" in clean_buffer:
            return False
        
        # Check for prompt box (only valid if not waiting for response)
        if "╭─" in clean_buffer and not self.waiting_for_response:
            return True
        
        # Check for idle time (only valid if not waiting for response)
        if time.time() - self.last_output_time > 2.0 and not self.waiting_for_response:
            return True
        
        return False
    
    # Removed is_response_complete - we now detect completion differently
    
    def extract_message(self, raw_output: str) -> Tuple[str, str]:
        """Extract user prompt and assistant response from Amp output"""
        clean = self.strip_ansi(raw_output)
        
        # Log for debugging - show more output
        self.log(f"[DEBUG] Raw buffer length: {len(raw_output)}, Clean buffer length: {len(clean)}")
        self.log(f"[DEBUG] Clean buffer (first 500 chars): {clean[:500]}")
        
        # Simple approach: everything after the last prompt is the assistant response
        # Look for the user's input (what we sent) and everything after is the response
        
        user_prompt = ""
        assistant_response = ""
        
        # If we have a last prompt sent, find it in the output
        if self.last_prompt_sent:
            prompt_idx = clean.find(self.last_prompt_sent)
            if prompt_idx >= 0:
                # Everything after the prompt is the assistant's response
                response_start = prompt_idx + len(self.last_prompt_sent)
                assistant_response = clean[response_start:].strip()
                
                # Clean up the response - remove any UI elements
                lines = assistant_response.split('\n')
                cleaned_lines = []
                for line in lines:
                    # Skip UI elements and empty lines
                    if not any(char in line for char in ['╭', '╰', '│', '─']):
                        if line.strip() and not line.strip().startswith('Type '):
                            cleaned_lines.append(line)
                
                assistant_response = '\n'.join(cleaned_lines).strip()
        
        # If we didn't find the prompt, try to extract any substantial text
        if not assistant_response:
            lines = clean.split('\n')
            response_lines = []
            for line in lines:
                # Skip UI elements and prompts
                if not any(char in line for char in ['╭', '╰', '│', '─', 'Welcome to', 'Type ']):
                    if line.strip() and len(line.strip()) > 5:
                        response_lines.append(line.strip())
            
            if response_lines:
                assistant_response = '\n'.join(response_lines)
        
        # Log what we extracted
        self.log(f"[DEBUG] Last prompt sent: {self.last_prompt_sent[:50] if self.last_prompt_sent else 'None'}")
        self.log(f"[DEBUG] Extracted assistant_response ({len(assistant_response)} chars): {assistant_response[:200] if assistant_response else 'None'}")
        
        return user_prompt, assistant_response
    
    def send_prompt_to_amp(self, prompt: str):
        """Send a prompt to Amp via PTY"""
        if not self.master_fd:
            self.log("[ERROR] No master_fd available")
            return
        
        self.log(f"[INFO] Sending prompt to Amp: {prompt[:50]}...")
        
        # Send the prompt text
        os.write(self.master_fd, prompt.encode('utf-8'))
        # Small delay to ensure it's processed
        time.sleep(0.1)
        # Send carriage return to submit (like pressing Enter)
        os.write(self.master_fd, b"\r")
        self.log("[DEBUG] Sent prompt with carriage return")
        
        self.last_prompt_sent = prompt
        self.waiting_for_response = True
        self.output_buffer = ""  # Clear buffer for new response
    
    def monitor_amp_output(self):
        """Monitor Amp's terminal output in a separate thread"""
        self.log("[INFO] Starting Amp output monitor")
        
        # Wait for master_fd to be created by the PTY thread
        wait_time = 0
        while self.master_fd is None and wait_time < 10:
            time.sleep(0.1)
            wait_time += 0.1
        
        if self.master_fd is None:
            self.log("[ERROR] master_fd not created after 10 seconds, exiting monitor")
            return
        
        self.log(f"[INFO] master_fd is ready (fd={self.master_fd}), starting output monitoring")
        self.log(f"[DEBUG] Initial conditions: running={self.running}, master_fd={self.master_fd}, master_fd type={type(self.master_fd)}")
        
        loop_count = 0
        while self.running and self.master_fd is not None:
            loop_count += 1
            if loop_count % 50 == 0:  # Log every 5 seconds (0.1 * 50)
                self.log(f"[DEBUG] Monitor loop still running, iteration {loop_count}, running={self.running}, master_fd={self.master_fd}")
            
            try:
                # Check for output
                r, _, _ = select.select([self.master_fd], [], [], 0.1)
                
                if self.master_fd in r:
                    self.log(f"[DEBUG] select() says data available on master_fd")
                    try:
                        data = os.read(self.master_fd, 4096)
                        self.log(f"[DEBUG] Read {len(data) if data else 0} bytes from master_fd")
                        if not data:
                            # EOF - child process closed the PTY
                            self.log("[INFO] EOF on master_fd - AMP has closed PTY")
                            break
                        if data:
                            # Write to stdout for user to see (do this FIRST before decoding)
                            os.write(sys.stdout.fileno(), data)
                            
                            output = data.decode('utf-8', errors='ignore')
                            
                            # We'll capture response text in the processing/completion section below
                            
                            self.terminal_buffer += output
                            self.output_buffer += output
                            self.last_output_time = time.time()
                            
                            # Log ALL output for debugging
                            self.log(f"[DEBUG] Got output chunk ({len(output)} bytes): {repr(output[:200])}")
                            
                            # Keep terminal buffer size manageable
                            if len(self.terminal_buffer) > 10000:
                                self.terminal_buffer = self.terminal_buffer[-5000:]
                            
                            # Check if Amp is ready (shows welcome message)
                            if not self.amp_ready and re.search(PATTERNS['welcome'], output):
                                self.amp_ready = True
                                self.log("[INFO] Amp is ready")
                            
                            # Check for AMP processing/completion
                            if self.waiting_for_response:
                                clean_output = self.strip_ansi(output)
                                
                                # Check if AMP started processing
                                if "Running inference" in clean_output:
                                    if not hasattr(self, 'inference_started') or not self.inference_started:
                                        self.log("[INFO] AMP started processing")
                                        self.message_processor.process_assistant_message_sync("AMP is processing your request...")
                                        self.inference_started = True
                                        self.waiting_for_response = True  # Ensure this is set
                                        # Clear the response buffer to start fresh
                                        self.response_buffer = []
                                        self.last_activity_time = time.time()
                                        self.has_response_content = False
                                        self.log("[DEBUG] Started response capture")
                                
                                # If inference has started, capture ALL output INCLUDING ANSI CODES
                                if hasattr(self, 'inference_started') and self.inference_started:
                                    # Store the RAW chunk (with ANSI codes) for later processing
                                    self.response_buffer.append(output)  # Store raw output, not clean
                                    self.last_activity_time = time.time()
                                    self.log(f"[DEBUG] Buffering chunk ({len(output)} chars, raw with ANSI)")
                                    
                                    # Check if we're seeing actual response content (not UI)
                                    # The response appears AFTER the thinking section completes
                                    # We need to see a blank line or end of thinking, then new text
                                    full_buffer = ''.join(self.response_buffer)
                                    
                                    # Look for the pattern: thinking text ends, then response starts
                                    # The thinking is usually indented or continues from "The user..."
                                    # The actual response is separate and not indented
                                    if "Thinking" in full_buffer and not self.has_response_content:
                                        lines = clean_output.split('\n')
                                        for i, line in enumerate(lines):
                                            stripped = line.strip()
                                            
                                            # Skip UI elements
                                            if any(ui in line for ui in ['───', '╭', '╮', '╯', '╰', '│', 'Ctrl+R', '┃']):
                                                continue
                                            
                                            # Check if this looks like the actual response
                                            # Response characteristics:
                                            # - Not part of thinking (doesn't continue the thought)
                                            # - Usually starts with a capital letter
                                            # - Is a complete sentence/greeting
                                            # - Doesn't contain "user" or thinking-related phrases
                                            if (stripped and len(stripped) > 5 and
                                                not stripped.startswith("The user") and
                                                not "not a request" in stripped.lower() and
                                                not "following the guidelines" in stripped.lower() and
                                                not "I should" in stripped.lower() and
                                                not "I don't need" in stripped.lower() and
                                                not "need to use" in stripped.lower() and
                                                not "Thinking" in stripped and
                                                not "Running inference" in stripped):
                                                
                                                # Check if it looks like a greeting or response
                                                if (stripped[0].isupper() and  # Starts with capital
                                                    ('!' in stripped or '?' in stripped or '.' in stripped)):  # Has punctuation
                                                    self.has_response_content = True
                                                    self.log(f"[INFO] Detected actual response: {stripped[:50]}")
                                
                                # Check for completion based on idle time after seeing response
                                completion_detected = False
                                
                                # Check for thread exit (immediate completion)
                                if "Thread:" in clean_output or "Continue this thread" in clean_output:
                                    completion_detected = True
                                    self.log("[INFO] Detected thread exit")
                                
                                # Note: idle detection happens below when no data is available
                                
                                if completion_detected:
                                    if hasattr(self, 'inference_started') and self.inference_started:
                                        self.log("[INFO] AMP returned to prompt - processing buffered response")
                                        self.waiting_for_response = False
                                        self.inference_started = False
                                        
                                        # Process all buffered chunks to extract the response
                                        if hasattr(self, 'response_buffer'):
                                            full_output = ''.join(self.response_buffer)
                                            self.log(f"[DEBUG] Full buffered output ({len(full_output)} chars)")
                                            
                                            # Use ANSI codes to differentiate thinking from response
                                            # Thinking typically uses dim/faint text (\x1b[2m or \x1b[90m for gray)
                                            # Normal response uses regular text (\x1b[0m for reset or no special codes)
                                            
                                            response_lines = []
                                            response_lines_set = set()  # Use set to avoid duplicates
                                            thinking_lines = []
                                            last_was_empty = False  # Track consecutive empty lines
                                            
                                            # Split into lines while preserving ANSI codes
                                            raw_lines = full_output.split('\n')
                                            
                                            for line in raw_lines:
                                                # First, check if line has content worth processing
                                                clean_line = self.strip_ansi(line).strip()
                                                
                                                # Skip UI elements
                                                if any(ui in clean_line for ui in [
                                                    '───', '╭', '╮', '╯', '╰', '│', 'Running inference', 
                                                    'Ctrl+R', 'Thread:', 'Continue this thread', '┃'
                                                ]):
                                                    continue
                                                
                                                # Handle empty lines - preserve single empty lines as paragraph breaks
                                                if not clean_line:
                                                    if not last_was_empty and response_lines:
                                                        # Add empty line for paragraph break if we have content
                                                        response_lines.append('')
                                                        last_was_empty = True
                                                    continue
                                                
                                                last_was_empty = False
                                                
                                                # Check ANSI codes in the line
                                                # Dim/gray text codes: \x1b[2m (dim), \x1b[90m (bright black/gray), \x1b[37m (gray)
                                                # Normal text: \x1b[0m (reset), \x1b[39m (default color), or no codes
                                                
                                                # Extract ANSI codes from this line
                                                codes_in_line = self.extract_ansi_codes(line)
                                                
                                                # Debug: Log ANSI codes for analysis
                                                if codes_in_line and self.debug:
                                                    self.log(f"[DEBUG] ANSI codes in line: {codes_in_line}")
                                                    self.log(f"[DEBUG] Line content: {clean_line[:80]}")
                                                
                                                # Determine if this is thinking (dimmed) or response (normal)
                                                is_thinking = False
                                                
                                                # Check for dim/gray codes that indicate thinking
                                                for code in codes_in_line:
                                                    if any(pattern in code for pattern in [
                                                        '[2m',     # Dim/faint
                                                        '[90m',    # Bright black (gray)
                                                        '[37m',    # Light gray
                                                        '[38;5;',  # 256-color gray shades
                                                        '[38;2;',  # RGB gray colors
                                                    ]):
                                                        is_thinking = True
                                                        break
                                                
                                                # Also check content patterns
                                                if 'Thinking' in clean_line or '∴' in clean_line:
                                                    is_thinking = True
                                                
                                                # Check for thinking content patterns
                                                if any(phrase in clean_line.lower() for phrase in [
                                                    "the user", "i should", "i need to", "this is a", 
                                                    "this seems", "according to", "let me"
                                                ]):
                                                    is_thinking = True
                                                
                                                # Check for tool output markers
                                                is_tool_output = any(marker in clean_line for marker in [
                                                    '✓', '✔', '⨯', '∿', '≈', 'Web Search', 'Searching'
                                                ])
                                                
                                                # Store based on classification
                                                if is_thinking:
                                                    thinking_lines.append(clean_line)
                                                    self.log(f"[DEBUG] Identified as thinking: {clean_line[:80]}")
                                                elif len(clean_line) > 3:  # Capture more content
                                                    # Include tool outputs, bullet points, headers, and regular text
                                                    if (is_tool_output or  # Tool outputs
                                                        clean_line.startswith(('*', '-', '•', '▪', '▫', '→', '◦')) or  # Bullet points
                                                        clean_line.endswith(':') or  # Headers
                                                        (len(clean_line) > 10 and clean_line[0].isupper()) or  # Regular sentences
                                                        '**' in clean_line):  # Markdown bold
                                                        # Smart deduplication - check if this is a partial or extension of existing lines
                                                        should_add = True
                                                        line_to_remove = None
                                                        
                                                        for existing_line in list(response_lines_set):
                                                            # If new line contains existing line at the start, replace the old one
                                                            if clean_line.startswith(existing_line):
                                                                line_to_remove = existing_line
                                                                break
                                                            # If existing line contains new line at the start, don't add new one
                                                            elif existing_line.startswith(clean_line):
                                                                should_add = False
                                                                break
                                                        
                                                        if line_to_remove:
                                                            # Remove the shorter version and add the longer one
                                                            response_lines_set.remove(line_to_remove)
                                                            response_lines = [l for l in response_lines if l != line_to_remove]
                                                            response_lines_set.add(clean_line)
                                                            response_lines.append(clean_line)
                                                            self.log(f"[DEBUG] Replaced partial line with complete: {clean_line[:80]}")
                                                        elif should_add and clean_line not in response_lines_set:
                                                            response_lines_set.add(clean_line)
                                                            response_lines.append(clean_line)
                                                            self.log(f"[DEBUG] Identified as response: {clean_line[:80]}")
                                            
                                            # Send the captured response
                                            if response_lines:
                                                # Join lines and ensure we strip any remaining ANSI codes
                                                response_text = '\n'.join(response_lines)
                                                # Strip any ANSI codes that might have leaked through
                                                response_text = self.strip_ansi(response_text)
                                                self.log(f"[INFO] Sending captured response ({len(response_text)} chars): {response_text[:200]}")
                                            else:
                                                # Fallback: Try to extract response by looking for text after thinking
                                                # that doesn't have dim/gray ANSI codes
                                                self.log(f"[DEBUG] No response lines found, trying fallback extraction")
                                                self.log(f"[DEBUG] Thinking lines found: {len(thinking_lines)}")
                                                
                                                # Look for any non-dimmed text after thinking content
                                                all_lines = []
                                                found_thinking = False
                                                
                                                for line in raw_lines:
                                                    clean_line = self.strip_ansi(line).strip()
                                                    
                                                    # Track if we've passed thinking section
                                                    if 'Thinking' in clean_line or '∴' in clean_line:
                                                        found_thinking = True
                                                        continue
                                                    
                                                    # Skip UI and empty lines
                                                    if not clean_line or any(ui in clean_line for ui in [
                                                        '───', '╭', '╮', '╯', '╰', '│', 'Running inference',
                                                        'Thread:', 'Continue this thread', 'Ctrl+R', '┃'
                                                    ]):
                                                        continue
                                                    
                                                    # If we found thinking and this line doesn't have dim codes, it might be response
                                                    if found_thinking and len(clean_line) > 10:
                                                        codes = self.extract_ansi_codes(line)
                                                        has_dim_codes = any('[2m' in code or '[90m' in code for code in codes)
                                                        
                                                        # If not dimmed and not a thinking pattern, likely response
                                                        if not has_dim_codes and not any(phrase in clean_line.lower() for phrase in [
                                                            "the user", "i should", "i need to", "this is a"
                                                        ]):
                                                            all_lines.append(clean_line)
                                                            self.log(f"[DEBUG] Fallback captured: {clean_line[:80]}")
                                                
                                                if all_lines:
                                                    response_text = '\n'.join(all_lines)
                                                    self.log(f"[INFO] Sending fallback response ({len(response_text)} chars)")
                                                else:
                                                    response_text = "AMP has completed processing."
                                                    self.log("[INFO] No response text captured, sending default")
                                            
                                            self.message_processor.process_assistant_message_sync(response_text)
                                            self.response_buffer = []
                                            # Reset waiting flag after successfully sending response
                                            self.waiting_for_response = False
                                            self.inference_started = False
                    except OSError as e:
                        # EAGAIN (35) means no data available yet - this is normal for non-blocking I/O
                        if e.errno in (35, 11):  # EAGAIN/EWOULDBLOCK
                            # This is expected - select() sometimes gives false positives with PTYs
                            # Sleep briefly to avoid busy-waiting
                            time.sleep(0.01)  # 10ms
                        else:
                            self.log(f"[ERROR] OSError reading from master_fd: {e}")
                            self.log(f"[DEBUG] OSError errno: {e.errno}, strerror: {e.strerror}")
                            break
                else:
                    # No data available - check for idle completion
                    if (self.waiting_for_response and 
                        hasattr(self, 'inference_started') and self.inference_started and
                        hasattr(self, 'has_response_content') and self.has_response_content and
                        hasattr(self, 'last_activity_time')):
                        
                        idle_time = time.time() - self.last_activity_time
                        if idle_time > 2.0:  # 2 seconds of idle after seeing response
                            self.log(f"[INFO] Detected completion - idle for {idle_time:.1f}s after response")
                            self.waiting_for_response = False
                            self.inference_started = False
                            
                            # Process buffered response
                            if hasattr(self, 'response_buffer'):
                                full_output = ''.join(self.response_buffer)
                                self.log(f"[DEBUG] Processing buffered output after idle ({len(full_output)} chars)")
                                
                                # Extract response (same logic as above)
                                lines = full_output.split('\n')
                                response_lines = []
                                response_lines_set = set()  # Use set to avoid duplicates
                                thinking_section_passed = False
                                last_was_empty = False  # Track consecutive empty lines
                                
                                for line in lines:
                                    # Strip ANSI codes AND whitespace
                                    stripped = self.strip_ansi(line).strip()
                                    
                                    # Skip UI elements
                                    if any(ui in stripped for ui in ['───', '╭', '╮', '╯', '╰', '│', 'Running inference', 'Ctrl+R']):
                                        continue
                                    
                                    # Handle empty lines - preserve single empty lines as paragraph breaks
                                    if not stripped:
                                        if thinking_section_passed and not last_was_empty and response_lines:
                                            # Add empty line for paragraph break if we have content and past thinking
                                            response_lines.append('')
                                            last_was_empty = True
                                        continue
                                    
                                    last_was_empty = False
                                    if "Thinking" in stripped:
                                        thinking_section_passed = True
                                        continue
                                    # Skip ALL thinking content (multi-line thinking text)
                                    if thinking_section_passed and any(phrase in stripped.lower() for phrase in [
                                        "the user", "not a request", "following the guidelines",
                                        "i should", "i don't need", "need to use", "i need to",
                                        "this is", "this seems", "according to"
                                    ]):
                                        continue
                                    if "Thread:" in stripped or "Continue this thread" in stripped:
                                        continue
                                    # Check for tool output markers
                                    is_tool_output = any(marker in stripped for marker in [
                                        '✓', '✔', '⨯', '∿', '≈', 'Web Search', 'Searching'
                                    ])
                                    
                                    # Capture lines that look like actual responses (less restrictive)
                                    if thinking_section_passed and len(stripped) > 3:
                                        if (is_tool_output or  # Tool outputs
                                            stripped.startswith(('*', '-', '•', '▪', '▫', '→', '◦')) or  # Bullet points
                                            stripped.endswith(':') or  # Headers
                                            (len(stripped) > 10 and stripped[0].isupper()) or  # Regular sentences
                                            '**' in stripped):  # Markdown bold
                                            # Smart deduplication - check if this is a partial or extension of existing lines
                                            should_add = True
                                            line_to_remove = None
                                            
                                            for existing_line in list(response_lines_set):
                                                # If new line contains existing line at the start, replace the old one
                                                if stripped.startswith(existing_line):
                                                    line_to_remove = existing_line
                                                    break
                                                # If existing line contains new line at the start, don't add new one
                                                elif existing_line.startswith(stripped):
                                                    should_add = False
                                                    break
                                            
                                            if line_to_remove:
                                                # Remove the shorter version and add the longer one
                                                response_lines_set.remove(line_to_remove)
                                                response_lines = [l for l in response_lines if l != line_to_remove]
                                                response_lines_set.add(stripped)
                                                response_lines.append(stripped)
                                                self.log(f"[DEBUG] Replaced partial line with complete: {stripped[:100]}")
                                            elif should_add and stripped not in response_lines_set:
                                                response_lines_set.add(stripped)
                                                response_lines.append(stripped)
                                                self.log(f"[DEBUG] Captured response line: {stripped[:100]}")
                                
                                if response_lines:
                                    response_text = '\n'.join(response_lines)
                                    self.log(f"[INFO] Sending captured response ({len(response_text)} chars)")
                                else:
                                    response_text = "AMP has completed processing."
                                    self.log("[INFO] No response text captured, sending default")
                                
                                self.message_processor.process_assistant_message_sync(response_text)
                                self.response_buffer = []
                                self.has_response_content = False
                                # Ensure flags are reset
                                self.waiting_for_response = False
                                self.inference_started = False
            except Exception as e:
                self.log(f"[ERROR] Output monitor error: {e}")
                self.log(f"[DEBUG] Exception details: {type(e).__name__}: {str(e)}")
                import traceback
                self.log(f"[DEBUG] Traceback: {traceback.format_exc()}")
                break
        
        self.log(f"[INFO] Output monitor stopped - running={self.running}, master_fd={self.master_fd}, loop_count={loop_count}")
    
    # Removed process_complete_response - we now handle this inline when detecting completion
    
    def run_amp_with_pty(self):
        """Run Amp CLI in a PTY"""
        amp_path = self.find_amp_cli()
        
        # Create settings file
        settings_file = self.create_amp_settings()
        
        # Build command with permission bypass
        cmd = [
            amp_path,
            "--dangerously-allow-all",  # Bypass permission prompts
            "--settings-file", settings_file,
        ]
        
        # Add any additional arguments
        if len(sys.argv) > 1:
            i = 1
            while i < len(sys.argv):
                arg = sys.argv[i]
                # Skip wrapper-specific arguments
                if arg in ["--api-key", "--base-url"]:
                    i += 2
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
            # Child process - exec Amp
            # Set environment
            os.environ['TERM'] = 'xterm-256color'
            os.environ['COLUMNS'] = str(cols)
            os.environ['ROWS'] = str(rows)
            
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
                # Use select to multiplex I/O (only check stdin, monitor thread handles master_fd)
                rlist, _, _ = select.select([sys.stdin], [], [], 0.01)
                
                # Handle stdin (user typing)
                if sys.stdin in rlist:
                    try:
                        data = os.read(sys.stdin.fileno(), 1024)
                        if data:
                            # Pass through to Amp
                            os.write(self.master_fd, data)
                            
                            # Check for Enter key to detect user input
                            if b'\r' in data or b'\n' in data:
                                # User submitted something via CLI
                                self.cancel_pending_input_request()
                                # Mark that we're waiting for a response
                                self.waiting_for_response = True
                                self.output_buffer = ""  # Clear buffer for new response
                                self.log("[DEBUG] User pressed Enter, waiting for response...")
                    except Exception:
                        pass
                
                # Don't read from master_fd here - the monitor thread handles all reading
                # This prevents race conditions and corrupted terminal output
                
                # Check for input from web UI
                if self.input_queue:
                    web_input = self.input_queue.popleft()
                    self.log(f"[INFO] Processing web input: {web_input[:50]}...")
                    self.send_prompt_to_amp(web_input)
                
                # Check if child process has exited
                if self.child_pid:
                    pid, status = os.waitpid(self.child_pid, os.WNOHANG)
                    if pid != 0:
                        self.log(f"[INFO] AMP process exited with status {status}")
                        self.running = False
                        if self.async_loop:
                            self.async_loop.call_soon_threadsafe(self.async_loop.stop)
                        break
        
        finally:
            # Restore terminal settings
            if self.original_tty_attrs:
                try:
                    termios.tcsetattr(sys.stdin, termios.TCSADRAIN, self.original_tty_attrs)
                except Exception:
                    pass
    
    def cancel_pending_input_request(self):
        """Cancel any pending input request"""
        if self.pending_input_task and not self.pending_input_task.done():
            self.log("[INFO] Cancelling pending input request")
            self.pending_input_task.cancel()
    
    async def request_user_input(self, message_id: str):
        """Request user input from web UI"""
        if not self.omnara_client_async:
            self.log("[WARNING] No async client available for input request")
            return
        
        if message_id in self.requested_input_messages:
            return
        
        self.requested_input_messages.add(message_id)
        
        try:
            self.log(f"[INFO] Requesting user input for message {message_id}")
            
            # Long-polling request for user input (like Claude does)
            user_responses = await self.omnara_client_async.request_user_input(
                message_id=message_id,
                timeout_minutes=1440,  # 24 hours
                poll_interval=3.0,
            )
            
            # Process responses
            for response in user_responses:
                if response:
                    self.log(f"[INFO] Got user response from web UI: {response[:50]}...")
                    self.message_processor.web_ui_messages.add(response)
                    self.input_queue.append(response)
        
        except asyncio.CancelledError:
            self.log("[INFO] Input request cancelled")
            raise
        except Exception as e:
            self.log(f"[ERROR] Failed to request input: {e}")
    
    async def idle_monitor_loop(self):
        """Monitor for idle state and request input when needed"""
        await self.omnara_client_async._ensure_session()
        
        while self.running:
            try:
                # Check if child process is still alive
                if self.child_pid:
                    try:
                        # Check if process exists (0 signal doesn't kill)
                        os.kill(self.child_pid, 0)
                    except ProcessLookupError:
                        self.log("[INFO] AMP process has exited")
                        self.running = False
                        break
                
                # Check if we should request input
                message_id = self.message_processor.should_request_input()
                
                if message_id:
                    self.log(f"[DEBUG] Idle detected, requesting input for message {message_id}")
                    # Cancel any existing request
                    self.cancel_pending_input_request()
                    
                    # Mark as requested BEFORE creating task
                    self.message_processor.mark_input_requested(message_id)
                    
                    # Create new request task (this will long-poll for input)
                    self.pending_input_task = asyncio.create_task(
                        self.request_user_input(message_id)
                    )
                    
                    # Don't wait for completion here - let it run in background
                    # The task will complete when user provides input
                    self.log("[DEBUG] Input request task created, continuing idle monitor")
                
                await asyncio.sleep(1.0)
            
            except Exception as e:
                self.log(f"[ERROR] Idle monitor error: {e}")
                await asyncio.sleep(1.0)
    
    def run(self):
        """Main run method"""
        try:
            # Initialize Omnara clients
            self.init_omnara_clients()
            self.log("[INFO] Omnara clients initialized")
            
            # Send initial message
            if self.omnara_client_sync:
                self.log("[INFO] Sending initial session start message...")
                response = self.omnara_client_sync.send_message(
                    content="Amp session started - waiting for your input...",
                    agent_type="Amp",
                    requires_user_input=False,  # Don't request input yet
                )
                self.agent_instance_id = response.agent_instance_id
                self.log(f"[INFO] Initial message sent, agent instance ID: {self.agent_instance_id}")
                
                # Initialize message processor with first message
                self.message_processor.last_message_id = response.message_id
                self.message_processor.last_message_time = time.time()
                self.log(f"[INFO] Set initial message ID: {response.message_id}")
                
                # Process any queued messages from initial response
                if response.queued_user_messages:
                    self.log(f"[INFO] Got {len(response.queued_user_messages)} queued messages from initial response")
                    for msg in response.queued_user_messages:
                        self.message_processor.web_ui_messages.add(msg)
                        self.input_queue.append(msg)
        
        except (AuthenticationError, APIError) as e:
            error_msg = str(e)
            
            if "Invalid API key" in error_msg or "Unauthorized" in error_msg:
                print(
                    "\nError: Invalid API key. Please check your Omnara API key.",
                    file=sys.stderr,
                )
            elif "Failed to connect" in error_msg:
                print(
                    f"\nError: Could not connect to Omnara servers at {self.base_url}",
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
        
        # Initialize git tracking
        self.init_git_tracking()
        
        # Start Amp in PTY (in thread)
        amp_thread = threading.Thread(target=self.run_amp_with_pty)
        amp_thread.daemon = True
        amp_thread.start()
        
        # Start output monitor thread (it will wait for master_fd)
        monitor_thread = threading.Thread(target=self.monitor_amp_output)
        monitor_thread.daemon = True
        monitor_thread.start()
        
        # Wait for Amp to be ready
        timeout = 10
        start_time = time.time()
        while not self.amp_ready and time.time() - start_time < timeout:
            time.sleep(0.5)
        
        if not self.amp_ready:
            self.log("[WARNING] Amp did not show ready message, continuing anyway")
        
        # Run async idle monitor
        try:
            self.async_loop = asyncio.new_event_loop()
            asyncio.set_event_loop(self.async_loop)
            self.async_loop.run_until_complete(self.idle_monitor_loop())
        except (KeyboardInterrupt, RuntimeError) as e:
            self.log(f"[INFO] Event loop interrupted: {e}")
        finally:
            # Clean up
            self.running = False
            self.log("[INFO] Shutting down AMP wrapper...")
            
            # Print exit message
            if not sys.exc_info()[0]:
                print("\nEnded Omnara Amp Session\n", file=sys.stderr)
            
            # Cancel pending tasks
            self.cancel_pending_input_request()
            
            # Clean up in background
            def background_cleanup():
                try:
                    if self.omnara_client_sync and self.agent_instance_id:
                        self.omnara_client_sync.end_session(self.agent_instance_id)
                        self.log("[INFO] Session ended successfully")
                    
                    if self.omnara_client_sync:
                        self.omnara_client_sync.close()
                    
                    if self.omnara_client_async:
                        loop = asyncio.new_event_loop()
                        asyncio.set_event_loop(loop)
                        loop.run_until_complete(self.omnara_client_async.close())
                        loop.close()
                    
                    if self.debug_log_file:
                        self.log("=== AMP Wrapper Log Ended ===")
                        self.debug_log_file.flush()
                        self.debug_log_file.close()
                    
                    # Clean up temp settings file
                    temp_settings = Path("/tmp") / f"amp_omnara_{self.session_uuid}.json"
                    if temp_settings.exists():
                        temp_settings.unlink()
                
                except Exception as e:
                    self.log(f"[ERROR] Cleanup error: {e}")
            
            cleanup_thread = threading.Thread(target=background_cleanup)
            cleanup_thread.daemon = False
            cleanup_thread.start()
            
            # Give cleanup a moment
            cleanup_thread.join(timeout=5)
            
            # Terminate Amp process if still running
            if self.child_pid:
                try:
                    os.kill(self.child_pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass


def main():
    """Main entry point"""
    parser = argparse.ArgumentParser(
        description="AMP wrapper for Omnara integration",
        add_help=False,  # Disable help to pass through to Amp
    )
    
    # Wrapper-specific arguments
    parser.add_argument(
        "--api-key",
        help="Omnara API key (can also use OMNARA_API_KEY env var)",
    )
    parser.add_argument(
        "--base-url",
        help="Omnara base URL (defaults to production)",
    )
    
    # Parse known args only
    args, unknown = parser.parse_known_args()
    
    # Restore sys.argv for Amp
    sys.argv = ["amp"] + unknown
    
    # Create and run wrapper
    wrapper = AmpWrapper(api_key=args.api_key, base_url=args.base_url)
    wrapper.run()


if __name__ == "__main__":
    main()