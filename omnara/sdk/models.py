"""Data models for the Omnara SDK."""

from typing import List
from dataclasses import dataclass


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
class CreateMessageResponse:
    """Response from creating a message."""

    success: bool
    agent_instance_id: str
    message_id: str
    queued_user_messages: List[str]


@dataclass
class Message:
    """A message in the conversation."""

    id: str
    content: str
    sender_type: str  # 'agent' or 'user'
    created_at: str
    requires_user_input: bool


@dataclass
class PendingMessagesResponse:
    """Response from getting pending messages."""

    agent_instance_id: str
    messages: List[Message]
    status: str  # 'ok' or 'stale'
