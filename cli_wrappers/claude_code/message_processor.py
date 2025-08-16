"""Message processing implementation for Claude wrapper"""

import time
from typing import TYPE_CHECKING, Optional

if TYPE_CHECKING:
    from .claude_wrapper_v3 import ClaudeWrapperV3


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

        # Sanitize content - remove NUL characters and control characters that break the API
        # This handles binary content from .docx, PDFs, etc.
        sanitized_content = "".join(
            char if ord(char) >= 32 or char in "\n\r\t" else ""
            for char in content.replace("\x00", "")
        )

        # Get git diff if enabled
        git_diff = self.wrapper.get_git_diff()
        # Sanitize git diff as well if present (handles binary files in git diff)
        if git_diff:
            git_diff = "".join(
                char if ord(char) >= 32 or char in "\n\r\t" else ""
                for char in git_diff.replace("\x00", "")
            )

        # Send to Omnara
        response = self.wrapper.omnara_client_sync.send_message(
            content=sanitized_content,
            agent_type="Claude Code",
            agent_instance_id=self.wrapper.agent_instance_id,
            requires_user_input=False,
            git_diff=git_diff,
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