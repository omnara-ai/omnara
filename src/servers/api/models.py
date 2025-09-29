"""Pydantic models for FastAPI request/response schemas."""

from pydantic import BaseModel, Field

from servers.shared.models import (
    BaseEndSessionRequest,
    BaseEndSessionResponse,
)


# Request models
class RegisterAgentInstanceRequest(BaseModel):
    """Request model for registering an agent instance."""

    agent_type: str = Field(
        ..., description="Name of the agent type to associate the instance with"
    )
    agent_instance_id: str | None = Field(
        default=None,
        description="Optional existing instance id to reuse for reconnect scenarios",
    )
    name: str | None = Field(default=None, description="Friendly name for the instance")
    transport: str | None = Field(
        default=None, description="Transport mode for the instance (e.g. 'ssh')"
    )


class CreateMessageRequest(BaseModel):
    """Request model for creating a new message."""

    agent_instance_id: str = Field(
        ...,
        description="Existing agent instance ID. Creates a new agent instance if ID doesn't exist.",
    )
    agent_type: str | None = Field(
        None, description="Type of agent (e.g., 'claude_code', 'cursor')"
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
        description=(
            "Git diff content to store with the instance. "
            "Base64-encoded values are automatically detected and decoded."
        ),
    )
    message_metadata: dict | None = Field(
        None,
        description="Optional metadata to store with the message (e.g., webhook URLs)",
    )


class CreateUserMessageRequest(BaseModel):
    """Request model for creating a user message."""

    agent_instance_id: str = Field(
        ..., description="Agent instance ID to send the message to"
    )
    content: str = Field(..., description="Message content")
    mark_as_read: bool = Field(
        True,
        description="Whether to mark this message as read (update last_read_message_id)",
    )


class EndSessionRequest(BaseEndSessionRequest):
    """FastAPI-specific request model for ending a session."""

    pass


# Response models
class RegisterAgentInstanceResponse(BaseModel):
    """Response payload for registering an agent instance."""

    agent_instance_id: str = Field(..., description="Identifier of the agent instance")
    agent_type_id: str | None = Field(
        default=None, description="Database identifier of the agent type"
    )
    agent_type_name: str | None = Field(
        default=None, description="Human-readable agent type name"
    )
    status: str = Field(..., description="Current status of the agent instance")
    name: str | None = Field(
        default=None, description="Friendly name stored on the instance"
    )
    instance_metadata: dict | None = Field(
        default=None, description="Arbitrary metadata associated with the instance"
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


class CreateMessageResponse(BaseModel):
    """Response model for create message endpoint."""

    success: bool = Field(
        ..., description="Whether the message was created successfully"
    )
    agent_instance_id: str = Field(
        ..., description="Agent instance ID (new or existing)"
    )
    message_id: str = Field(..., description="ID of the message that was created")
    queued_user_messages: list[MessageResponse] = Field(
        default_factory=list,
        description="List of queued user messages with full metadata",
    )


class CreateUserMessageResponse(BaseModel):
    """Response model for create user message endpoint."""

    success: bool = Field(
        ..., description="Whether the message was created successfully"
    )
    message_id: str = Field(..., description="ID of the created message")
    marked_as_read: bool = Field(
        ..., description="Whether the message was marked as read"
    )


class GetMessagesResponse(BaseModel):
    """Response model for get messages endpoint."""

    agent_instance_id: str = Field(..., description="Agent instance ID")
    messages: list[MessageResponse] = Field(
        default_factory=list, description="List of messages"
    )
    status: str = Field(
        "ok",
        description="Status: 'ok' if messages retrieved, 'stale' if last_read_message_id is outdated",
    )


class EndSessionResponse(BaseEndSessionResponse):
    """FastAPI-specific response model for end session endpoint."""

    pass


class VerifyAuthResponse(BaseModel):
    """Response model for auth verification endpoint."""

    success: bool = Field(..., description="Whether authentication was successful")
    user_id: str = Field(..., description="Authenticated user's ID")
    email: str = Field(..., description="User's email address")
    display_name: str | None = Field(None, description="User's display name")
    message: str = Field(..., description="Success or error message")
