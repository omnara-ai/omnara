"""Data models for the Omnara SDK."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional

from shared.message_types import (
    MessageMetadataError,
    ParsedMessageMetadata,
    parse_message_metadata,
)


@dataclass
class LogStepResponse:
    """Response from logging a step."""

    success: bool
    agent_instance_id: str
    step_number: int
    user_feedback: List[str]


@dataclass
class EndSessionResponse:
    """Response from ending a session."""

    success: bool
    agent_instance_id: str
    final_status: str


@dataclass
class Message:
    """A message in the conversation returned by the Omnara API."""

    id: str
    content: str
    sender_type: str  # 'agent' or 'user'
    created_at: str
    requires_user_input: bool
    sender_user_id: Optional[str] = None
    sender_user_email: Optional[str] = None
    sender_user_display_name: Optional[str] = None
    message_type: Optional[str] = None
    message_metadata: Optional[Dict[str, Any]] = None
    parsed_metadata: ParsedMessageMetadata = field(init=False, repr=False)

    def __post_init__(self) -> None:
        try:
            parsed = parse_message_metadata(self.message_metadata)
        except MessageMetadataError:
            raw_metadata = (
                dict(self.message_metadata)
                if isinstance(self.message_metadata, dict)
                else None
            )
            parsed = ParsedMessageMetadata(
                raw=raw_metadata,
                message_type=self.message_type,
                payload=raw_metadata,
                is_legacy=raw_metadata is not None,
            )

        self.parsed_metadata = parsed
        self.message_metadata = parsed.raw

        if parsed.message_type:
            self.message_type = parsed.message_type

    @property
    def payload(self) -> Any:
        """Return the structured payload (if any) for this message."""

        return self.parsed_metadata.payload


@dataclass
class CreateMessageResponse:
    """Response from creating a message."""

    success: bool
    agent_instance_id: str
    message_id: str
    queued_user_messages: List[Message] = field(default_factory=list)

    @property
    def queued_contents(self) -> List[str]:
        """Convenience accessor for queued user message text."""

        return [msg.content for msg in self.queued_user_messages]


@dataclass
class PendingMessagesResponse:
    """Response from getting pending messages."""

    agent_instance_id: str
    messages: List[Message] = field(default_factory=list)
    status: str = "ok"  # 'ok' or 'stale'

    @property
    def contents(self) -> List[str]:
        """Return message content for each pending message."""

        return [msg.content for msg in self.messages]
