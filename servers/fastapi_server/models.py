"""Pydantic models for FastAPI request/response schemas."""

from typing import Optional

from pydantic import BaseModel, Field

from servers.shared.models import (
    BaseEndSessionRequest,
    BaseEndSessionResponse,
)


# Request models
class CreateMessageRequest(BaseModel):
    """Request model for creating a new message."""

    agent_instance_id: str | None = Field(
        None,
        description="Existing agent instance ID. If not provided, creates a new instance.",
    )
    agent_type: str = Field(
        ..., description="Type of agent (e.g., 'claude_code', 'cursor')"
    )
    content: str = Field(
        ..., description="Message content (step description or question text)"
    )
    requires_user_input: bool = Field(
        False, description="Whether this message requires user input (is a question)"
    )
    send_email: bool | None = Field(
        None,
        description="Whether to send email notification (overrides user preference)",
    )
    send_sms: bool | None = Field(
        None, description="Whether to send SMS notification (overrides user preference)"
    )
    send_push: bool | None = Field(
        None,
        description="Whether to send push notification (overrides user preference)",
    )
    git_diff: str | None = Field(
        None,
        description="Git diff content to store with the instance",
    )


class EndSessionRequest(BaseEndSessionRequest):
    """FastAPI-specific request model for ending a session."""

    pass


# Response models
class CreateMessageResponse(BaseModel):
    """Response model for create message endpoint."""

    success: bool = Field(..., description="Whether the message was created successfully")
    agent_instance_id: str = Field(
        ..., description="Agent instance ID (new or existing)"
    )
    queued_user_messages: list[str] = Field(
        default_factory=list,
        description="List of queued user message contents",
    )


class MessageResponse(BaseModel):
    """Response model for individual messages."""

    id: str = Field(..., description="Message ID")
    content: str = Field(..., description="Message content")
    sender_type: str = Field(..., description="Sender type: 'agent' or 'user'")
    created_at: str = Field(..., description="ISO timestamp when message was created")
    requires_user_input: bool = Field(
        ..., description="Whether this message requires user input"
    )


class GetMessagesResponse(BaseModel):
    """Response model for get messages endpoint."""

    agent_instance_id: str = Field(..., description="Agent instance ID")
    messages: list[MessageResponse] = Field(
        default_factory=list, description="List of messages"
    )
    status: str = Field(
        "ok", description="Status: 'ok' if messages retrieved, 'stale' if last_read_message_id is outdated"
    )


class EndSessionResponse(BaseEndSessionResponse):
    """FastAPI-specific response model for end session endpoint."""

    pass
